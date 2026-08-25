package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/platform"
	jobsqlite "github.com/flidai/leapview/internal/platform/jobs/sqlite"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	releasegen "github.com/flidai/leapview/internal/release/api/gen"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/google/go-cmp/cmp"
	ocidigest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestReleaseRepositoryRoundTripsAndValidatesImmutableProvenance(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	identity := testServingIdentity("commerce", "dev", "generation_1")
	insertServingState(t, store, identity)
	repo := NewRepository(store.SQLDB())
	provenance := testReleaseProvenance(t, identity)
	created, err := repo.Create(t.Context(), release.CreateInput{
		ID: "rel_provenance", ServingIdentity: identity,
		ProjectDigest: provenance.Artifact.ProjectDigest, ArtifactDigest: provenance.Artifact.ContentDigest,
		RequestDigest: testDigest("6"), IdempotencyKey: "provenance", CreatedBy: "principal_1",
		Connections: []release.ConnectionPin{{ConnectionID: "orders", RevisionID: testDigest("7")}}, Provenance: &provenance,
	})
	require.NoError(t, err)
	require.NotNil(t, created.Provenance)
	require.Empty(t, cmp.Diff(provenance, *created.Provenance), "created provenance mismatch (-want +got)")
	if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE api_releases SET provenance_json = json_set(provenance_json, '$.plan.runtimeVersion', 'tampered') WHERE id = ?`, created.ID); err != nil {
		t.Fatal(err)
	}
	projectID, err := projectgraph.NewResourceID("commerce")
	require.NoError(t, err)
	if _, err := repo.Get(t.Context(), projectID, created.ID); !errors.Is(err, release.ErrProvenanceInvalid) {
		t.Fatalf("Get(tampered provenance) error = %v, want ErrProvenanceInvalid", err)
	}
}

func TestReleaseCreateAuditIntentIsAtomicAndIdempotent(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	identity := testServingIdentity("commerce", "dev", "generation_audit")
	insertServingState(t, store, identity)
	provenance := testReleaseProvenance(t, identity)
	intent := access.AuditIntent{
		EventID: "release-create-audit", Source: "release", Operation: "createRelease", PrincipalID: "principal_1",
		Action: "release.created", ResourceKind: "project", ResourceID: "commerce", Capability: access.CapabilityResourcePublish,
		Outcome: "success", AggregateKey: "release:commerce:rel_audit", AggregateSequence: 1,
		MetadataJSON: `{"createdBy":"principal_1","operationId":"createRelease","projectDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","projectId":"commerce","releaseId":"rel_audit","status":"draft"}`,
	}
	repo := NewRepositoryWithWorkflowAndAudit(store.SQLDB(), nil, accesssqlite.NewRepository(store.SQLDB()))
	input := release.CreateInput{ID: "rel_audit", ServingIdentity: identity, ProjectDigest: provenance.Artifact.ProjectDigest, ArtifactDigest: provenance.Artifact.ContentDigest, RequestDigest: testDigest("6"), IdempotencyKey: "audit", CreatedBy: "principal_1", Provenance: &provenance}
	if _, err := repo.Create(release.WithAuditIntent(t.Context(), intent), input); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_outbox WHERE event_id = ?`, intent.EventID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("audit outbox count = %d, want 1", count)
	}
	// A replay returns the immutable row without creating a duplicate intent.
	if _, err := repo.Create(release.WithAuditIntent(t.Context(), intent), input); err != nil {
		t.Fatalf("idempotent release replay: %v", err)
	}
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_outbox WHERE event_id = ?`, intent.EventID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("replayed audit outbox count = %d, want 1", count)
	}
}

func TestReleaseCreateRollsBackWhenAuditIntentFails(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	identity := testServingIdentity("commerce", "dev", "generation_audit_failure")
	insertServingState(t, store, identity)
	provenance := testReleaseProvenance(t, identity)
	repo := NewRepositoryWithWorkflowAndAudit(store.SQLDB(), nil, access.AuditIntentRecorderFunc(func(context.Context, transaction.Transaction, access.AuditIntent) error {
		return errors.New("injected audit failure")
	}))
	intent := access.AuditIntent{EventID: "release-create-rollback", Source: "release", Operation: "createRelease", Action: "release.created", Outcome: "success", AggregateKey: "release:commerce:rollback", AggregateSequence: 1, MetadataJSON: `{}`}
	input := release.CreateInput{ID: "rel_rollback", ServingIdentity: identity, ProjectDigest: provenance.Artifact.ProjectDigest, ArtifactDigest: provenance.Artifact.ContentDigest, RequestDigest: testDigest("7"), IdempotencyKey: "rollback", CreatedBy: "principal_1", Provenance: &provenance}
	if _, err := repo.Create(release.WithAuditIntent(t.Context(), intent), input); err == nil {
		t.Fatal("release create unexpectedly succeeded")
	}
	var count int
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM api_releases WHERE id = ?`, input.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled-back release count = %d, want 0", count)
	}
}

func TestReleaseArtifactAuditIsAtomicAndIdempotent(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	identity := testServingIdentity("commerce", "dev", "generation_artifact_audit")
	insertServingState(t, store, identity)
	provenance := testReleaseProvenance(t, identity)
	repo := NewRepositoryWithWorkflowAndAudit(store.SQLDB(), nil, accesssqlite.NewRepository(store.SQLDB()))
	created, err := repo.Create(t.Context(), release.CreateInput{ID: "rel_artifact_audit", ServingIdentity: identity, ProjectDigest: provenance.Artifact.ProjectDigest, ArtifactDigest: provenance.Artifact.ContentDigest, RequestDigest: testDigest("6"), IdempotencyKey: "artifact-audit", CreatedBy: "principal_1", Provenance: &provenance})
	require.NoError(t, err)
	metadata, err := releasegen.EncodeGenUploadReleaseArtifactAuditPayload(releasegen.GenSchemaReleaseArtifactUploadedAuditPayload{OperationId: "uploadReleaseArtifact", ReleaseId: created.ID, GenerationId: identity.GenerationID, Digest: created.ArtifactDigest, SizeBytes: 42})
	require.NoError(t, err)
	intent := access.AuditIntent{EventID: "release-artifact-audit", Source: "release", Operation: "uploadReleaseArtifact", PrincipalID: "principal_1", Action: "release.artifact_uploaded", ResourceKind: "project", ResourceID: "commerce", Capability: access.CapabilityResourcePublish, Outcome: "success", AggregateKey: "release:commerce:" + created.ID, AggregateSequence: 2, MetadataJSON: metadata}
	artifact := release.Artifact{ReleaseID: created.ID, ServingIdentity: identity, ExpectedDigest: created.ArtifactDigest, ActualDigest: created.ArtifactDigest, SizeBytes: 42}
	require.NoError(t, repo.RecordArtifact(release.WithAuditIntent(t.Context(), intent), artifact))
	var count int
	require.NoError(t, store.SQLDB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_outbox WHERE event_id = ?`, intent.EventID).Scan(&count))
	require.Equal(t, 1, count)
	// A replay with the same durable intent cannot create a second outbox row.
	if err := repo.RecordArtifact(release.WithAuditIntent(t.Context(), intent), artifact); !errors.Is(err, release.ErrConflict) {
		t.Fatalf("artifact replay error = %v, want ErrConflict", err)
	}
}

func TestReleaseArtifactAuditFailureRollsBackArtifactTransition(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	identity := testServingIdentity("commerce", "dev", "generation_artifact_rollback")
	insertServingState(t, store, identity)
	provenance := testReleaseProvenance(t, identity)
	require.NoError(t, err)
	created, err := NewRepository(store.SQLDB()).Create(t.Context(), release.CreateInput{ID: "rel_artifact_rollback", ServingIdentity: identity, ProjectDigest: provenance.Artifact.ProjectDigest, ArtifactDigest: provenance.Artifact.ContentDigest, RequestDigest: testDigest("6"), IdempotencyKey: "artifact-rollback", CreatedBy: "principal_1", Provenance: &provenance})
	require.NoError(t, err)
	repo := NewRepositoryWithWorkflowAndAudit(store.SQLDB(), nil, access.AuditIntentRecorderFunc(func(context.Context, transaction.Transaction, access.AuditIntent) error {
		return errors.New("injected artifact audit failure")
	}))
	intent := access.AuditIntent{EventID: "release-artifact-rollback", Source: "release", Operation: "uploadReleaseArtifact", PrincipalID: "principal_1", Action: "release.artifact_uploaded", ResourceKind: "project", ResourceID: "commerce", Capability: access.CapabilityResourcePublish, Outcome: "success", AggregateKey: "release:commerce:" + created.ID, AggregateSequence: 2, MetadataJSON: `{}`}
	artifact := release.Artifact{ReleaseID: created.ID, ServingIdentity: identity, ExpectedDigest: created.ArtifactDigest, ActualDigest: created.ArtifactDigest, SizeBytes: 42}
	if err := repo.RecordArtifact(release.WithAuditIntent(t.Context(), intent), artifact); err == nil {
		t.Fatal("artifact transition unexpectedly succeeded")
	}
	var uploadedAt sql.NullString
	require.NoError(t, store.SQLDB().QueryRowContext(t.Context(), `SELECT artifact_uploaded_at FROM api_releases WHERE id = ?`, created.ID).Scan(&uploadedAt))
	if uploadedAt.Valid {
		t.Fatalf("artifact timestamp persisted after audit rollback: %q", uploadedAt.String)
	}
}

func TestReleaseFinalizationAuditIsAtomicAndReplaySafe(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	identity := testServingIdentity("commerce", "dev", "generation_finalize_audit")
	insertServingState(t, store, identity)
	provenance := testReleaseProvenance(t, identity)
	repo := NewRepositoryWithWorkflowAndAudit(store.SQLDB(), nil, accesssqlite.NewRepository(store.SQLDB()))
	created, err := repo.Create(t.Context(), release.CreateInput{ID: "rel_finalize_audit", ServingIdentity: identity, ProjectDigest: provenance.Artifact.ProjectDigest, ArtifactDigest: provenance.Artifact.ContentDigest, RequestDigest: testDigest("6"), IdempotencyKey: "finalize-audit", CreatedBy: "principal_1", Provenance: &provenance})
	require.NoError(t, err)
	require.NoError(t, repo.RecordArtifact(t.Context(), release.Artifact{ReleaseID: created.ID, ServingIdentity: identity, ExpectedDigest: created.ArtifactDigest, ActualDigest: created.ArtifactDigest, SizeBytes: 42}))
	metadata, err := releasegen.EncodeGenFinalizeReleaseAuditPayload(releasegen.GenSchemaReleaseValidatingAuditPayload{OperationId: "finalizeRelease", ReleaseId: created.ID, ProjectId: identity.ProjectID.String(), Status: "validating"})
	require.NoError(t, err)
	intent := access.AuditIntent{EventID: "release-finalize-audit", Source: "release", Operation: "finalizeRelease", PrincipalID: "principal_1", Action: "release.validating", ResourceKind: "project", ResourceID: identity.ProjectID.String(), Capability: access.CapabilityResourcePublish, Outcome: "success", AggregateKey: "release:" + identity.ProjectID.String() + ":" + created.ID, AggregateSequence: 3, MetadataJSON: metadata}
	ctx := release.WithAuditIntent(t.Context(), intent)
	if _, err := repo.BeginFinalization(ctx, identity.ProjectID.String(), created.ID, jobs.WorkflowIntent{}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.BeginFinalization(ctx, identity.ProjectID.String(), created.ID, jobs.WorkflowIntent{}); err != nil {
		t.Fatalf("finalization replay: %v", err)
	}
	var count int
	require.NoError(t, store.SQLDB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_outbox WHERE event_id = ?`, intent.EventID).Scan(&count))
	require.Equal(t, 1, count)
}

func TestConnectionCatalogReportsActiveServingGenerationRevision(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	db := store.SQLDB()
	identity := testServingIdentity("commerce", "prod", "generation-active")
	insertServingState(t, store, identity)
	revisionDigest := testDigest("9")
	statements := []string{
		`INSERT INTO managed_data_collections (id,project_id,connection_id,name) VALUES ('collection-orders','commerce','connection:orders','Orders')`,
		`INSERT INTO managed_data_revisions (id,collection_id,sequence,digest,status,manifest_json,file_count,size_bytes,ready_at) VALUES ('revision-orders','collection-orders',1,'` + revisionDigest + `','ready','{"files":[]}',0,0,'2026-01-01T00:00:00Z')`,
		`INSERT INTO delivery_target_revisions (target_id,project_id,environment,target_revision,active_generation_id,created_at,updated_at) VALUES ('target-prod','commerce','prod',3,'generation-active','2026-01-01T00:00:00Z','2026-01-02T00:00:00Z')`,
		`INSERT INTO managed_data_serving_state_binding_sets (project_id,environment,generation_id,binding_digest,binding_count) VALUES ('commerce','prod','generation-active','` + testDigest("8") + `',1)`,
		`INSERT INTO managed_data_serving_state_bindings (project_id,environment,generation_id,collection_id,revision_id) VALUES ('commerce','prod','generation-active','collection-orders','revision-orders')`,
	}
	for _, statement := range statements {
		_, err := db.ExecContext(t.Context(), statement)
		require.NoError(t, err)
	}
	repo := NewRepository(db)
	connections, err := repo.ListConnections(t.Context(), "commerce", "prod")
	require.NoError(t, err)
	if len(connections) != 1 || connections[0].ActiveRevisionID != revisionDigest {
		t.Fatalf("connections = %#v, want active revision %q", connections, revisionDigest)
	}
	connection, err := repo.GetConnection(t.Context(), "commerce", "connection:orders", "prod")
	require.NoError(t, err)
	if connection.ActiveRevisionID != revisionDigest {
		t.Fatalf("connection = %#v, want active revision %q", connection, revisionDigest)
	}
}

func TestReleaseRepositoryRejectsMalformedPersistedServingIdentity(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	identity := testServingIdentity("commerce", "dev", "generation_1")
	insertServingState(t, store, identity)
	repo := NewRepository(store.SQLDB())
	provenance := testReleaseProvenance(t, identity)
	created, err := repo.Create(t.Context(), release.CreateInput{
		ID: "rel_invalid_identity", ServingIdentity: identity,
		ProjectDigest: provenance.Artifact.ProjectDigest, ArtifactDigest: provenance.Artifact.ContentDigest, RequestDigest: testDigest("6"),
		IdempotencyKey: "invalid-identity", CreatedBy: "principal_1", Provenance: &provenance,
	})
	require.NoError(t, err)
	connection, err := store.SQLDB().Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(t.Context(), `PRAGMA foreign_keys = OFF`); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(t.Context(), `UPDATE api_releases SET environment = 'prod/env' WHERE id = ?`, created.ID); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(t.Context(), identity.ProjectID, created.ID); err == nil {
		t.Fatal("Get accepted malformed persisted serving identity")
	}
}

func TestReleaseRepositoryRetainsCandidateProvenanceImmutably(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	repo := NewRepository(store.SQLDB())
	provenance := candidateReleaseProvenance(t)
	projectID, err := projectgraph.NewResourceID("commerce")
	require.NoError(t, err)
	retained, err := repo.RetainCandidateProvenance(t.Context(), projectID, provenance)
	require.NoError(t, err)
	replayed, err := repo.RetainCandidateProvenance(t.Context(), projectID, provenance)
	require.NoError(t, err)
	require.Empty(t, cmp.Diff(retained, replayed), "replayed provenance mismatch (-want +got)")
	loaded, err := repo.CandidateProvenance(t.Context(), projectID, provenance.Candidate.ID, provenance.Candidate.Revision)
	require.NoError(t, err)
	require.Empty(t, cmp.Diff(provenance, loaded), "loaded provenance mismatch (-want +got)")

	changed := provenance
	changed.Plan.RuntimeVersion = "runtime:changed"
	changedGate := *changed.Plan.GateEvidence
	changedGate.RuntimeVersion = changed.Plan.RuntimeVersion
	changedGate, err = changedGate.Canonical()
	require.NoError(t, err)
	changed.Plan.GateEvidence = &changedGate
	changed, err = release.NewProvenance(release.ProvenanceInput{Artifact: changed.Artifact, Candidate: changed.Candidate, Plan: changed.Plan})
	require.NoError(t, err)
	if _, err := repo.RetainCandidateProvenance(t.Context(), projectID, changed); !errors.Is(err, release.ErrConflict) {
		t.Fatalf("changed replay error = %v, want ErrConflict", err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE release_candidate_provenance SET provenance_json = json_set(provenance_json, '$.plan.runtimeVersion', 'runtime:tampered') WHERE candidate_id = ? AND candidate_revision = ?`, provenance.Candidate.ID, provenance.Candidate.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CandidateProvenance(t.Context(), projectID, provenance.Candidate.ID, provenance.Candidate.Revision); !errors.Is(err, release.ErrProvenanceInvalid) {
		t.Fatalf("tampered candidate provenance error = %v", err)
	}
}

func testProvenanceDigest(value any) string {
	encoded, _ := json.Marshal(value)
	return ocidigest.FromBytes(encoded).String()
}

func TestReleaseRepositoryLoadsReadyProvenanceByServingStateAfterRestart(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	identity := testServingIdentity("commerce", "dev", "generation_ready")
	insertServingState(t, store, identity)
	eventRepo := jobsqlite.NewRepository(store.SQLDB())
	repo := NewRepositoryWithWorkflow(store.SQLDB(), eventRepo)
	provenance := testReleaseProvenance(t, identity)
	created, err := repo.Create(t.Context(), release.CreateInput{ID: "rel_active", ServingIdentity: identity, ProjectDigest: provenance.Artifact.ProjectDigest, ArtifactDigest: provenance.Artifact.ContentDigest, RequestDigest: testDigest("6"), IdempotencyKey: "active-provenance", CreatedBy: "principal_1", Provenance: &provenance})
	require.NoError(t, err)
	require.NoError(t, repo.RecordArtifact(t.Context(), release.Artifact{ReleaseID: created.ID, ServingIdentity: identity, ExpectedDigest: provenance.Artifact.ContentDigest, ActualDigest: provenance.Artifact.ContentDigest, SizeBytes: 42}))
	if _, err := repo.BeginFinalization(t.Context(), created.ServingIdentity.ProjectID.String(), created.ID, jobs.WorkflowIntent{}); err != nil {
		t.Fatal(err)
	}
	_, err = repo.CompleteFinalization(t.Context(), created.ServingIdentity.ProjectID.String(), created.ID, provenance.Artifact.ContentDigest)
	require.NoError(t, err)
	restarted := NewRepository(store.SQLDB())
	loaded, err := restarted.ProvenanceForServingState(t.Context(), identity)
	require.NoError(t, err)
	require.Empty(t, cmp.Diff(provenance, loaded))
	other := testServingIdentity("commerce", "dev", "generation_other")
	_, err = restarted.ProvenanceForServingState(t.Context(), other)
	require.ErrorIs(t, err, release.ErrNotFound)
}

func TestReleaseRepositoryLoadsSealedCandidateProvenanceByServingStateAfterRestart(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	identity := testServingIdentity("commerce", "dev", "generation_candidate")
	insertServingState(t, store, identity)
	provenance := testReleaseProvenance(t, identity)
	repo := NewRepository(store.SQLDB())
	_, err = repo.RetainCandidateProvenance(t.Context(), identity.ProjectID, provenance)
	require.NoError(t, err)

	restarted := NewRepository(store.SQLDB())
	loaded, err := restarted.ProvenanceForServingState(t.Context(), identity)
	require.NoError(t, err)
	require.Empty(t, cmp.Diff(provenance, loaded))
	duplicatePlan := provenance.Plan
	duplicateGate := *duplicatePlan.GateEvidence
	duplicateGate.CandidateID = "cand_2"
	duplicateGate, err = duplicateGate.Canonical()
	require.NoError(t, err)
	duplicatePlan.GateEvidence = &duplicateGate
	duplicate, err := release.NewProvenance(release.ProvenanceInput{
		Artifact: provenance.Artifact,
		Candidate: release.CandidateProvenance{
			ID: "cand_2", Revision: provenance.Candidate.Revision, OwnerID: provenance.Candidate.OwnerID,
		},
		SourceRevision: provenance.SourceRevision,
		Plan:           duplicatePlan,
	})
	require.NoError(t, err)
	_, err = repo.RetainCandidateProvenance(t.Context(), identity.ProjectID, duplicate)
	require.NoError(t, err)
	_, err = restarted.ProvenanceForServingState(t.Context(), identity)
	require.ErrorIs(t, err, release.ErrConflict)

	other := testServingIdentity("commerce", "dev", "generation_other")
	_, err = restarted.ProvenanceForServingState(t.Context(), other)
	require.ErrorIs(t, err, release.ErrNotFound)
}

func TestPriorDeploymentReleaseSkipsRequestsThatNeverActivated(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	db := store.SQLDB()
	for _, generation := range []string{"generation_1", "generation_failed", "generation_2"} {
		insertServingState(t, store, testServingIdentity("project", "prod", generation))
	}
	for _, item := range []struct{ id, generation string }{{"rel_1", "generation_1"}, {"rel_failed", "generation_failed"}, {"rel_2", "generation_2"}} {
		if _, err := db.ExecContext(t.Context(), `INSERT INTO api_releases (id, project_id, environment, generation_id, project_digest, artifact_digest, request_digest, idempotency_key, status, created_by, finalized_at) VALUES (?, 'project', 'prod', ?, ?, ?, ?, ?, 'ready', 'principal', CURRENT_TIMESTAMP)`, item.id, item.generation, testDigest("1"), testDigest("2"), testDigest("3"), item.id); err != nil {
			t.Fatal(err)
		}
	}
	deployments := []struct{ id, generation, status, createdAt, failure string }{
		{"dep_1", "generation_1", "superseded", "2026-01-01T00:00:00Z", ""},
		{"dep_failed", "generation_failed", "failed", "2026-01-02T00:00:00Z", "verification failed"},
		{"dep_2", "generation_2", "active", "2026-01-03T00:00:00Z", ""},
	}
	for _, item := range deployments {
		activatedAt := any(nil)
		if item.status == "active" || item.status == "superseded" {
			activatedAt = item.createdAt
		}
		if _, err := db.ExecContext(t.Context(), `INSERT INTO project_deployments (id, project_id, environment, generation_id, artifact_digest, request_digest, status, created_by, created_at, activated_at, error) VALUES (?, 'project', 'prod', ?, ?, ?, ?, 'principal', ?, ?, ?)`, item.id, item.generation, testDigest("2"), testDigest("3"), item.status, item.createdAt, activatedAt, item.failure); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct{ deployment, release, createdAt string }{{"dep_1", "rel_1", "2026-01-01T00:00:00Z"}, {"dep_failed", "rel_failed", "2026-01-02T00:00:00Z"}, {"dep_2", "rel_2", "2026-01-03T00:00:00Z"}} {
		if _, err := db.ExecContext(t.Context(), `INSERT INTO api_deployment_releases (deployment_id, project_id, release_id, created_at) VALUES (?, 'project', ?, ?)`, item.deployment, item.release, item.createdAt); err != nil {
			t.Fatal(err)
		}
	}
	got, err := NewRepository(db).PriorDeploymentRelease(t.Context(), "project", "dep_2")
	require.NoError(t, err)
	if got != "rel_1" {
		t.Fatalf("prior release = %q, want retained active release rel_1", got)
	}
}

func TestReleaseLifecycleIsIdempotentAndImmutable(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	eventRepo := jobsqlite.NewRepository(store.SQLDB())
	repo := NewRepositoryWithWorkflow(store.SQLDB(), eventRepo)
	identity := testServingIdentity("commerce", "dev", "generation_1")
	insertServingState(t, store, identity)
	provenance := testReleaseProvenance(t, identity)
	input := release.CreateInput{ID: "rel_1", ServingIdentity: identity, ProjectDigest: provenance.Artifact.ProjectDigest, ArtifactDigest: provenance.Artifact.ContentDigest, RequestDigest: testDigest("6"), IdempotencyKey: "release-1", CreatedBy: "principal", Provenance: &provenance, Connections: []release.ConnectionPin{{ConnectionID: "warehouse", RevisionID: testDigest("7")}}}
	created, err := repo.Create(t.Context(), input)
	require.NoError(t, err)
	if created.Status != release.StatusDraft {
		t.Fatalf("Create() status = %q", created.Status)
	}
	replayed, err := repo.Create(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, created.ID, replayed.ID)
	conflict := input
	conflict.RequestDigest = testDigest("8")
	if _, err := repo.Create(t.Context(), conflict); !errors.Is(err, release.ErrConflict) {
		t.Fatalf("conflicting replay error = %v", err)
	}
	require.NoError(t, repo.RecordArtifact(t.Context(), release.Artifact{ReleaseID: created.ID, ServingIdentity: identity, ExpectedDigest: input.ArtifactDigest, ActualDigest: input.ArtifactDigest, SizeBytes: 42}))
	if _, err := repo.BeginFinalization(t.Context(), identity.ProjectID.String(), created.ID, jobs.WorkflowIntent{}); err != nil {
		t.Fatal(err)
	}
	ready, err := repo.CompleteFinalization(t.Context(), identity.ProjectID.String(), created.ID, input.ArtifactDigest)
	if err != nil || ready.Status != release.StatusReady || ready.FinalizedAt == "" {
		t.Fatalf("CompleteFinalization() = %#v, %v", ready, err)
	}
	events, err := eventRepo.ListEvents(t.Context(), "release", created.ID, 0, 10)
	if err != nil || len(events) != 1 || events[0].EventType != "release.ready" {
		t.Fatalf("ready events = %#v, %v", events, err)
	}
	var readyEvent map[string]any
	require.NoError(t, json.Unmarshal(events[0].Data, &readyEvent))
	if readyEvent["status"] != string(release.StatusReady) || readyEvent["finalizedAt"] == "" {
		t.Fatalf("ready event data = %#v", readyEvent)
	}
	if err := repo.RecordArtifact(t.Context(), release.Artifact{ReleaseID: created.ID, ServingIdentity: identity, ExpectedDigest: input.ArtifactDigest, ActualDigest: input.ArtifactDigest, SizeBytes: 1}); !errors.Is(err, release.ErrConflict) {
		t.Fatalf("post-finalize artifact error = %v", err)
	}
}

func TestReleaseFinalizationRejectsMissingOrMismatchedArtifacts(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	repo := NewRepository(store.SQLDB())
	identity := testServingIdentity("commerce", "dev", "generation_missing")
	insertServingState(t, store, identity)
	provenance := testReleaseProvenance(t, identity)
	created, err := repo.Create(t.Context(), release.CreateInput{ID: "rel_2", ServingIdentity: identity, ProjectDigest: provenance.Artifact.ProjectDigest, ArtifactDigest: provenance.Artifact.ContentDigest, RequestDigest: testDigest("6"), IdempotencyKey: "release-2", CreatedBy: "principal", Provenance: &provenance})
	require.NoError(t, err)
	if _, err := repo.BeginFinalization(t.Context(), identity.ProjectID.String(), created.ID, jobs.WorkflowIntent{}); !errors.Is(err, release.ErrIncomplete) {
		t.Fatalf("missing artifact error = %v", err)
	}
}

func TestBeginFinalizationRollsBackWhenWorkflowCannotBeRecorded(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	injected := errors.New("injected workflow failure")
	repo := NewRepositoryWithWorkflow(store.SQLDB(), failingWorkflowRecorder{err: injected})
	identity := testServingIdentity("commerce", "dev", "generation_atomic")
	insertServingState(t, store, identity)
	provenance := testReleaseProvenance(t, identity)
	created, err := repo.Create(t.Context(), release.CreateInput{ID: "rel_atomic", ServingIdentity: identity, ProjectDigest: provenance.Artifact.ProjectDigest, ArtifactDigest: provenance.Artifact.ContentDigest, RequestDigest: testDigest("6"), IdempotencyKey: "atomic", CreatedBy: "principal", Provenance: &provenance})
	require.NoError(t, err)
	require.NoError(t, repo.RecordArtifact(t.Context(), release.Artifact{ReleaseID: created.ID, ServingIdentity: identity, ExpectedDigest: provenance.Artifact.ContentDigest, ActualDigest: provenance.Artifact.ContentDigest, SizeBytes: 42}))
	_, err = repo.BeginFinalization(t.Context(), identity.ProjectID.String(), created.ID, jobs.WorkflowIntent{Event: jobs.EventInput{Key: "validating", ResourceKind: "release", ResourceID: created.ID, EventType: "release.validating", Data: []byte(`{}`)}, Job: jobs.EnqueueInput{ID: "release:" + created.ID + ":finalize"}})
	if !errors.Is(err, injected) {
		t.Fatalf("BeginFinalization() error = %v, want injected failure", err)
	}
	current, err := repo.Get(t.Context(), identity.ProjectID, created.ID)
	require.NoError(t, err)
	if current.Status != release.StatusDraft {
		t.Fatalf("status after workflow failure = %q, want draft", current.Status)
	}
}

func TestCompleteFinalizationRollsBackWhenReadyEventCannotBeRecorded(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	injected := errors.New("injected terminal event failure")
	repo := NewRepositoryWithWorkflow(store.SQLDB(), failingWorkflowRecorder{err: injected})
	identity := testServingIdentity("commerce", "dev", "generation_atomic_ready")
	insertServingState(t, store, identity)
	provenance := testReleaseProvenance(t, identity)
	created, err := repo.Create(t.Context(), release.CreateInput{ID: "rel_atomic_ready", ServingIdentity: identity, ProjectDigest: provenance.Artifact.ProjectDigest, ArtifactDigest: provenance.Artifact.ContentDigest, RequestDigest: testDigest("6"), IdempotencyKey: "atomic-ready", CreatedBy: "principal", Provenance: &provenance})
	require.NoError(t, err)
	require.NoError(t, repo.RecordArtifact(t.Context(), release.Artifact{ReleaseID: created.ID, ServingIdentity: identity, ExpectedDigest: provenance.Artifact.ContentDigest, ActualDigest: provenance.Artifact.ContentDigest, SizeBytes: 42}))
	// A recorder that fails only terminal events lets validating persist first.
	if _, err := repo.BeginFinalization(t.Context(), identity.ProjectID.String(), created.ID, jobs.WorkflowIntent{}); err != nil {
		t.Fatal(err)
	}
	_, err = repo.CompleteFinalization(t.Context(), identity.ProjectID.String(), created.ID, provenance.Artifact.ContentDigest)
	if !errors.Is(err, injected) {
		t.Fatalf("CompleteFinalization() error = %v, want injected failure", err)
	}
	current, err := repo.Get(t.Context(), identity.ProjectID, created.ID)
	require.NoError(t, err)
	if current.Status != release.StatusValidating {
		t.Fatalf("status after terminal event failure = %q, want validating", current.Status)
	}
}

func TestFailFinalizationRollsBackWhenFailedEventCannotBeRecorded(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	injected := errors.New("injected terminal event failure")
	repo := NewRepositoryWithWorkflow(store.SQLDB(), failingWorkflowRecorder{err: injected})
	identity := testServingIdentity("commerce", "dev", "generation_atomic_failed")
	insertServingState(t, store, identity)
	provenance := testReleaseProvenance(t, identity)
	created, err := repo.Create(t.Context(), release.CreateInput{ID: "rel_atomic_failed", ServingIdentity: identity, ProjectDigest: provenance.Artifact.ProjectDigest, ArtifactDigest: provenance.Artifact.ContentDigest, RequestDigest: testDigest("6"), IdempotencyKey: "atomic-failed", CreatedBy: "principal", Provenance: &provenance})
	require.NoError(t, err)
	require.NoError(t, repo.RecordArtifact(t.Context(), release.Artifact{ReleaseID: created.ID, ServingIdentity: identity, ExpectedDigest: provenance.Artifact.ContentDigest, ActualDigest: provenance.Artifact.ContentDigest, SizeBytes: 42}))
	if _, err := repo.BeginFinalization(t.Context(), identity.ProjectID.String(), created.ID, jobs.WorkflowIntent{}); err != nil {
		t.Fatal(err)
	}
	_, err = repo.FailFinalization(t.Context(), identity.ProjectID.String(), created.ID, errors.New("invalid artifact"))
	if !errors.Is(err, injected) {
		t.Fatalf("FailFinalization() error = %v, want injected failure", err)
	}
	current, err := repo.Get(t.Context(), identity.ProjectID, created.ID)
	require.NoError(t, err)
	if current.Status != release.StatusValidating {
		t.Fatalf("status after terminal event failure = %q, want validating", current.Status)
	}
}

func testDigest(hexDigit string) string { return "sha256:" + strings.Repeat(hexDigit, 64) }

func testServingIdentity(project, environment, generation string) projectgraph.ServingIdentity {
	identity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID(project), environment, generation)
	if err != nil {
		panic(err)
	}
	return identity
}

func testReleaseProvenance(t *testing.T, identity projectgraph.ServingIdentity) release.Provenance {
	t.Helper()
	input := release.ProvenanceInput{Artifact: release.ProjectArtifactProvenance{SourceDigest: testDigest("1"), ProjectDigest: testDigest("2"), ContentDigest: testDigest("3"), CompilerVersion: "leapview:test", SchemaVersion: 3}, Candidate: release.CandidateProvenance{ID: "cand_1", Revision: 2, OwnerID: "principal_1"}, Plan: release.GenerationPlanProvenance{Identity: identity, TargetID: "target_1", RuntimeVersion: "runtime:test", PolicyDigest: testDigest("4"), DataRevision: "snapshot:1", DataMode: release.GenerationDataRefreshSources, ManagedDataPins: []release.ManagedDataPin{{ConnectionID: "orders", RevisionID: testDigest("7")}}, Bindings: []release.BindingEvidence{{BindingID: "warehouse", ConnectionID: "warehouse", ConnectorKind: "postgres", Revision: 2, ValidatedVersion: "version-7", EndpointConfigHash: testDigest("8")}}}}
	binding := release.BindingFingerprint(input.Plan.Bindings)
	evidence, err := (release.GateEvidence{Version: 1, CandidateID: input.Candidate.ID, SourceDigest: input.Artifact.SourceDigest, BindingGeneration: binding, RuntimeVersion: input.Plan.RuntimeVersion, DuckDBVersion: "duckdb:test", Outcome: release.GateSuccess, EvaluatedAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC), Bounds: release.GateBounds{MaxRows: 100, MaxQueries: 10, MaxMillis: 1000}}).Canonical()
	require.NoError(t, err)
	input.Plan.GateEvidence = &evidence
	p, err := release.NewProvenance(input)
	require.NoError(t, err)
	return p
}

func candidateReleaseProvenance(t *testing.T) release.Provenance {
	return testReleaseProvenance(t, testServingIdentity("commerce", "dev", "generation_candidate"))
}

func insertServingState(t *testing.T, store *platform.Store, identity projectgraph.ServingIdentity) {
	t.Helper()
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO serving_states (id, project_id, environment, status, source, created_by) VALUES (?, ?, ?, 'pending', 'publish', 'principal')`, identity.GenerationID, identity.ProjectID.String(), identity.Environment); err != nil {
		t.Fatal(err)
	}
}

type failingWorkflowRecorder struct{ err error }

func (r failingWorkflowRecorder) RecordWorkflow(context.Context, transaction.Transaction, jobs.WorkflowIntent) error {
	return r.err
}
