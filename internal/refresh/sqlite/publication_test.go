package sqlite

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

var publicationIdentity = projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: "dev", GenerationID: "candidate"}

func TestPublicationCompletesRootAndChildrenAtomically(t *testing.T) {
	store, version := seedPublicationTree(t, "running")
	defer store.Close()
	unit := NewPublicationUnitOfWork(store.SQLDB(), nil)
	if err := unit.Publish(t.Context(), publicationIdentity, servingstate.ID(publicationIdentity.GenerationID), version); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	assertPublicationTreeStatuses(t, store, refreshrun.RunStatusSucceeded, refreshrun.RunStatusSucceeded, refreshrun.RunStatusSucceeded, refreshrun.RunStatusSucceeded)
}

func TestPublicationRejectsExpiredFenceWithoutMutation(t *testing.T) {
	store, version := seedPublicationTree(t, "running")
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE refresh_jobs SET lease_expires_at = datetime('now', '-1 second') WHERE id = 'root_job'`); err != nil {
		t.Fatal(err)
	}
	before := publicationSnapshot(t, store)
	unit := NewPublicationUnitOfWork(store.SQLDB(), nil)
	if err := unit.Publish(t.Context(), publicationIdentity, servingstate.ID(publicationIdentity.GenerationID), version); !errors.Is(err, refreshrun.ErrLeaseLost) {
		t.Fatalf("Publish() error = %v, want ErrLeaseLost", err)
	}
	assertPublicationTreeStatuses(t, store, refreshrun.RunStatusPrepared, refreshrun.RunStatusRunning, refreshrun.RunStatusRunning, refreshrun.RunStatusQueued)
	if after := publicationSnapshot(t, store); after != before {
		t.Fatalf("expired publication mutated durable fields: before=%q after=%q", before, after)
	}
}

func publicationSnapshot(t *testing.T, store *platform.Store) string {
	t.Helper()
	var snapshot string
	err := store.SQLDB().QueryRowContext(t.Context(), `SELECT group_concat(value, '|') FROM (SELECT printf('%s|%s|%s|%s|%d|%s|%s', j.status, r.status, r.error, COALESCE(r.finished_at,''), j.lease_revision, COALESCE(j.lease_owner,''), COALESCE(j.lease_expires_at,'')) AS value FROM refresh_jobs j JOIN refresh_job_runs r ON r.job_id=j.id WHERE j.id IN ('root_job','child_job') ORDER BY j.id)`).Scan(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestPublicationIneligibleChildRollsBackWholeTree(t *testing.T) {
	store, version := seedPublicationTree(t, "succeeded")
	defer store.Close()
	unit := NewPublicationUnitOfWork(store.SQLDB(), nil)
	if err := unit.Publish(t.Context(), publicationIdentity, servingstate.ID(publicationIdentity.GenerationID), version); !errors.Is(err, refreshrun.ErrLeaseLost) {
		t.Fatalf("Publish() error = %v, want ErrLeaseLost", err)
	}
	assertPublicationTreeStatuses(t, store, refreshrun.RunStatusPrepared, refreshrun.RunStatusSucceeded, refreshrun.RunStatusRunning, refreshrun.RunStatusQueued)
}

func TestCompleteCanonicalRefreshPersistsPublishedGenerationDataVersion(t *testing.T) {
	store, job, result := seedCanonicalPublication(t)
	defer store.Close()

	unit := NewPublicationUnitOfWork(store.SQLDB(), nil)
	if err := unit.CompleteCanonicalRefresh(t.Context(), job, result); err != nil {
		t.Fatalf("CompleteCanonicalRefresh() error = %v", err)
	}
	assertPublicationTreeStatuses(t, store, refreshrun.RunStatusSucceeded, refreshrun.RunStatusSucceeded, refreshrun.RunStatusSucceeded, refreshrun.RunStatusSucceeded)
	assertCanonicalDataVersion(t, store)
}

func TestCompleteCanonicalRefreshRecoversAfterLeaseExpires(t *testing.T) {
	store, job, result := seedCanonicalPublication(t)
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE refresh_jobs SET lease_expires_at = datetime('now', '-1 second') WHERE id = 'root_job'`); err != nil {
		t.Fatal(err)
	}

	unit := NewPublicationUnitOfWork(store.SQLDB(), nil)
	if err := unit.CompleteCanonicalRefresh(t.Context(), job, result); err != nil {
		t.Fatalf("CompleteCanonicalRefresh() recovery error = %v", err)
	}
	assertPublicationTreeStatuses(t, store, refreshrun.RunStatusSucceeded, refreshrun.RunStatusSucceeded, refreshrun.RunStatusSucceeded, refreshrun.RunStatusSucceeded)
	assertCanonicalDataVersion(t, store)
}

func TestCompleteCanonicalRefreshRejectsUnrelatedPublication(t *testing.T) {
	store, job, result := seedCanonicalPublication(t)
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE refresh_jobs SET lease_expires_at = datetime('now', '-1 second') WHERE id = 'root_job'`); err != nil {
		t.Fatal(err)
	}
	result.PlanID = "plan-unrelated"

	unit := NewPublicationUnitOfWork(store.SQLDB(), nil)
	if err := unit.CompleteCanonicalRefresh(t.Context(), job, result); err == nil || !strings.Contains(err.Error(), "publication evidence is missing") {
		t.Fatalf("CompleteCanonicalRefresh() error = %v, want missing evidence", err)
	}
	assertPublicationTreeStatuses(t, store, refreshrun.RunStatusPrepared, refreshrun.RunStatusRunning, refreshrun.RunStatusRunning, refreshrun.RunStatusQueued)
	var versions int
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM semantic_model_data_versions WHERE generation_id = 'state-refresh'`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 0 {
		t.Fatalf("unrelated publication persisted %d data versions", versions)
	}
}

func seedCanonicalPublication(t *testing.T) (*platform.Store, refreshrun.JobRecord, refreshrun.CanonicalRefreshResult) {
	t.Helper()
	store, _ := seedPublicationTree(t, "running")
	sha := "sha256:" + strings.Repeat("a", 64)
	conn, err := store.SQLDB().Conn(t.Context())
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), `PRAGMA foreign_keys = OFF`); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `
INSERT INTO serving_states (id, project_id, environment, status, source, digest, manifest_json, created_by, ducklake_snapshot_id)
VALUES ('state-refresh', 'project_sales', 'dev', 'active', 'refresh', 'digest-refresh', '{}', 'test', 0);
INSERT INTO delivery_plans (
  id, target_id, project_id, environment, operation_kind, source_digest, base_generation_id,
  base_target_revision, execution_digest, execution_inputs_json, provenance_json, governance_json,
  provenance_digest, governance_digest, plan_digest, status, expires_at, created_at, actor_id
) VALUES (
  'plan-refresh', 'target-dev', 'project_sales', 'dev', 'restatement', ?, 'delivery-base',
  1, ?, '{}', '{}', '{}', ?, ?, ?, 'planned', '2026-08-20T00:00:00Z', '2026-08-19T00:00:00Z', 'user:test'
);
INSERT INTO delivery_generations (
  id, candidate_id, plan_id, plan_digest, target_id, project_id, environment, catalog_digest,
  catalog_object_key, physical_pool_id, serving_artifact_id, serving_artifact_digest,
  serving_state_id, compatibility_digest, rollback_class, status, created_at, activated_at
) VALUES (
  'delivery-base', 'candidate-base', 'plan-base', ?, 'target-dev', 'project_sales', 'dev', ?,
  'catalogs/base.ducklake', 'pool-dev', 'artifact-base', ?, 'candidate', ?,
  'rollback_safe', 'active', '2026-08-18T00:00:00Z', '2026-08-18T00:01:00Z'
);
INSERT INTO delivery_generations (
  id, candidate_id, plan_id, plan_digest, target_id, project_id, environment, catalog_digest,
  catalog_object_key, physical_pool_id, serving_artifact_id, serving_artifact_digest,
  serving_state_id, compatibility_digest, rollback_class, status, created_at, activated_at
) VALUES (
  'delivery-refresh', 'candidate-refresh', 'plan-refresh', ?, 'target-dev', 'project_sales', 'dev', ?,
  'catalogs/refresh.ducklake', 'pool-dev', 'artifact-refresh', ?, 'state-refresh', ?,
  'rollback_safe', 'active', '2026-08-19T00:00:00Z', '2026-08-19T00:01:00Z'
);
INSERT INTO delivery_publications (
  id, request_digest, target_id, project_id, environment, plan_id, plan_digest, candidate_id,
  generation_id, expected_base_generation_id, expected_target_revision, result_target_revision,
  status, created_at, completed_at
) VALUES (
  'publication-refresh', ?, 'target-dev', 'project_sales', 'dev', 'plan-refresh', ?, 'candidate-refresh',
  'delivery-refresh', 'delivery-base', 1, 2, 'committed', '2026-08-19T00:00:00Z', '2026-08-19T00:01:00Z'
);
INSERT INTO delivery_build_attempts (
  id, plan_id, idempotency_key, plan_digest, source_digest, execution_digest, base_generation_id,
  physical_pool_id, writer_lease_id, status, seal_id, candidate_id, qualified_snapshot_id,
  revision, created_at, updated_at, terminal_at
) VALUES (
  'attempt-refresh', 'plan-refresh', 'refresh-build-root_run', ?, ?, ?, 'delivery-base',
  'pool-dev', 'lease-refresh', 'sealed', 'seal-refresh', 'candidate-refresh', 84,
  2, '2026-08-19T00:00:00Z', '2026-08-19T00:01:00Z', '2026-08-19T00:01:00Z'
);`, sha, sha, sha, sha, sha, sha, sha, sha, sha, sha, sha, sha, sha, sha, sha, sha, sha); err != nil {
		store.Close()
		t.Fatalf("seed canonical publication: %v", err)
	}
	job := refreshrun.JobRecord{
		ID: "root_job", Identity: publicationIdentity, SemanticModelID: "semantic_sales", PipelineID: "pipeline_daily", PipelinePlan: testSQLitePipelinePlan(publicationIdentity, "pipeline_daily", "semantic_sales"), TriggerID: "manual",
		PrincipalID: "user:test", Kind: refreshrun.JobKindRefreshPipeline, EstimatedMemoryBytes: 67108864,
		RunID: "root_run", TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline_daily",
		TargetRevision: 1, TriggerType: refreshrun.TriggerManual, LeaseOwner: "worker-1", LeaseRevision: 1,
	}
	return store, job, refreshrun.CanonicalRefreshResult{PlanID: "plan-refresh", ServingStateID: "state-refresh", SnapshotID: 84}
}

func assertCanonicalDataVersion(t *testing.T, store *platform.Store) {
	t.Helper()
	var snapshotID int64
	var generationID, source, pipelineID, runID string
	if err := store.SQLDB().QueryRowContext(t.Context(), `
SELECT snapshot_id, generation_id, source, pipeline_id, run_id
FROM semantic_model_data_versions
WHERE project_id = 'project_sales' AND environment = 'dev' AND semantic_model_id = 'semantic_sales' AND generation_id = 'state-refresh'`).Scan(&snapshotID, &generationID, &source, &pipelineID, &runID); err != nil {
		t.Fatal(err)
	}
	if snapshotID != 84 || generationID != "state-refresh" || source != "refresh" || pipelineID != "pipeline_daily" || runID != "root_run" {
		t.Fatalf("canonical data version = %d/%q/%q/%q/%q", snapshotID, generationID, source, pipelineID, runID)
	}
	var servingSnapshot int64
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT ducklake_snapshot_id FROM serving_states WHERE id = 'state-refresh'`).Scan(&servingSnapshot); err != nil {
		t.Fatal(err)
	}
	if servingSnapshot != 0 {
		t.Fatalf("sealed serving state pinned DuckLake snapshot %d", servingSnapshot)
	}
}

func seedPublicationTree(t *testing.T, childStatus string) (*platform.Store, refreshschedule.DataVersion) {
	t.Helper()
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `
INSERT INTO serving_states (id, project_id, environment, status, source, digest, manifest_json, created_by, ducklake_snapshot_id)
VALUES ('candidate', 'project_sales', 'dev', 'validated', 'refresh', 'digest', '{}', 'test', 42);
INSERT INTO principals (id, email, display_name)
VALUES ('user:test', 'test@example.test', 'Test');
INSERT INTO refresh_jobs (id, project_id, generation_id, semantic_model_id, pipeline_id, principal_id, group_ids_json, estimated_memory_bytes, kind, status, lease_owner, lease_revision, lease_expires_at)
VALUES ('root_job', 'project_sales', 'candidate', 'semantic_sales', 'pipeline_daily', 'user:test', '[]', 67108864, 'refresh_pipeline', 'running', 'worker-1', 1, datetime('now', '+5 minutes'));
INSERT INTO refresh_jobs (id, project_id, generation_id, semantic_model_id, pipeline_id, principal_id, group_ids_json, estimated_memory_bytes, kind, status)
VALUES ('child_job', 'project_sales', 'candidate', 'semantic_sales', 'pipeline_daily', 'user:test', '[]', 67108864, 'child_run', 'queued');
INSERT INTO refresh_job_runs (id, job_id, principal_id, environment, target_type, target_id, target_revision, trigger_type, status, created_sequence)
VALUES ('root_run', 'root_job', 'user:test', 'dev', 'refresh_pipeline', 'pipeline_daily', 1, 'manual', 'prepared', 1);
INSERT INTO refresh_job_runs (id, job_id, principal_id, environment, target_type, target_id, target_revision, trigger_type, parent_run_id, status, created_sequence)
VALUES ('child_run', 'child_job', 'user:test', 'dev', 'model_table', 'table_orders', 1, 'dependency', 'root_run', ?, 2);`, childStatus); err != nil {
		t.Fatal(err)
	}
	version := refreshschedule.DataVersion{Identity: publicationIdentity, SemanticModelID: "semantic_sales", SnapshotID: 42, RefreshedAt: time.Now().UTC(), Source: refreshschedule.DataVersionSourceRefresh, PipelineID: "pipeline_daily", RunID: "root_run", TargetRevision: 1, LeaseOwner: "worker-1", LeaseRevision: 1}
	return store, version
}

func assertPublicationTreeStatuses(t *testing.T, store *platform.Store, wantRoot, wantChild, wantRootJob, wantChildJob string) {
	t.Helper()
	var root, child, rootJob, childJob string
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT status FROM refresh_job_runs WHERE id='root_run'`).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT status FROM refresh_job_runs WHERE id='child_run'`).Scan(&child); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT status FROM refresh_jobs WHERE id='root_job'`).Scan(&rootJob); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT status FROM refresh_jobs WHERE id='child_job'`).Scan(&childJob); err != nil {
		t.Fatal(err)
	}
	if root != wantRoot || child != wantChild || rootJob != wantRootJob || childJob != wantChildJob {
		t.Fatalf("tree statuses = %q/%q jobs %q/%q, want %q/%q", root, child, rootJob, childJob, wantRoot, wantChild)
	}
}
