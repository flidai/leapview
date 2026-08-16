package sqlite

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

var publicationIdentity = projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: "dev", GenerationID: "candidate"}

func TestPublicationCompletesRootAndChildrenAtomically(t *testing.T) {
	store, version := seedPublicationTree(t, "running")
	defer store.Close()
	unit := NewPublicationUnitOfWork(store.SQLDB(), nil)
	if err := unit.Publish(t.Context(), publicationIdentity, version); err != nil {
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
	if err := unit.Publish(t.Context(), publicationIdentity, version); !errors.Is(err, refreshrun.ErrLeaseLost) {
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
	if err := unit.Publish(t.Context(), publicationIdentity, version); !errors.Is(err, refreshrun.ErrLeaseLost) {
		t.Fatalf("Publish() error = %v, want ErrLeaseLost", err)
	}
	assertPublicationTreeStatuses(t, store, refreshrun.RunStatusPrepared, refreshrun.RunStatusSucceeded, refreshrun.RunStatusRunning, refreshrun.RunStatusQueued)
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
