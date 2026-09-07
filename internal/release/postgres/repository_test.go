package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	access "github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	postgresmigrations "github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	jobs "github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "release_authority_test")
	p, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if err := postgresmigrations.ApplyRiver(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return p
}

func testEffectsDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "release_effects_test")
	p, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if err := postgresmigrations.ApplyRiver(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), eventspostgres.SchemaSQL()); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := jobspostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := accesspostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return p
}

func digest(ch string) string { return "sha256:" + strings.Repeat(ch, 64) }

func identity(t *testing.T, generation string) projectgraph.ServingIdentity {
	t.Helper()
	id, err := projectgraph.NewServingIdentity("commerce", "dev", generation)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func provenance(t *testing.T, id projectgraph.ServingIdentity) release.Provenance {
	t.Helper()
	in := release.ProvenanceInput{
		Artifact:  release.ProjectArtifactProvenance{SourceDigest: digest("1"), ProjectDigest: digest("2"), ContentDigest: digest("3"), CompilerVersion: "leapview:test", SchemaVersion: 3},
		Candidate: release.CandidateProvenance{ID: "candidate_1", Revision: 1, OwnerID: "principal_1"},
		Plan:      release.GenerationPlanProvenance{Identity: id, TargetID: "target_1", RuntimeVersion: "runtime:test", PolicyDigest: digest("4"), DataRevision: "snapshot:1", DataMode: release.GenerationDataReuseBase},
	}
	binding := release.BindingFingerprint(in.Plan.Bindings)
	evidence, err := (release.GateEvidence{Version: 1, CandidateID: in.Candidate.ID, SourceDigest: in.Artifact.SourceDigest, BindingGeneration: binding, RuntimeVersion: in.Plan.RuntimeVersion, DuckDBVersion: "duckdb:test", Outcome: release.GateSuccess, EvaluatedAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC), Bounds: release.GateBounds{MaxRows: 100, MaxQueries: 10, MaxMillis: 1000}}).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	in.Plan.GateEvidence = &evidence
	p, err := release.NewProvenance(in)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// testAuditAppender and testEventAppender keep the repository tests focused
// on transaction behavior while adapting the sibling authorities through the
// same narrow contracts used by app composition.
type testAuditAppender struct{}

func (testAuditAppender) RecordAuditEvent(ctx context.Context, tx Tx, intent access.AuditIntent) (AuditEvent, error) {
	stored, err := accesspostgres.New().RecordAuditEvent(ctx, tx, intent)
	if err != nil {
		return AuditEvent{}, err
	}
	return AuditEvent{AuditID: stored.AuditID, DomainEventID: stored.DomainEventID, ScopeID: stored.ScopeID, ActorID: stored.ActorID, PrincipalID: stored.PrincipalID, Source: stored.Source, Operation: stored.Operation, Action: stored.Action, ResourceKind: stored.ResourceKind, ResourceID: stored.ResourceID, Capability: stored.Capability, Outcome: stored.Outcome, RequestID: stored.RequestID, RequestDigest: stored.RequestDigest, CorrelationID: stored.CorrelationID, AggregateKey: stored.AggregateKey, AggregateSequence: stored.AggregateSequence, MetadataJSON: stored.MetadataJSON, OccurredAt: stored.OccurredAt, IntentDigest: stored.IntentDigest}, nil
}

type testEventAppender struct{}

func (testEventAppender) AppendEvent(ctx context.Context, tx Tx, input EventInput) (Event, error) {
	stored, err := eventspostgres.New().AppendEvent(ctx, tx, eventspostgres.EventInput{EventID: input.EventID, ScopeID: input.ScopeID, AggregateType: input.AggregateType, AggregateID: input.AggregateID, EventType: input.EventType, SchemaVersion: input.SchemaVersion, CorrelationID: input.CorrelationID, Payload: input.Payload})
	if err != nil {
		return Event{}, err
	}
	return Event{EventID: stored.EventID, ScopeID: stored.ScopeID, AggregateType: stored.AggregateType, AggregateID: stored.AggregateID, AggregateVersion: stored.AggregateVersion, EventType: stored.EventType, SchemaVersion: stored.SchemaVersion, OccurredAt: stored.OccurredAt, CorrelationID: stored.CorrelationID, Payload: stored.Payload}, nil
}

type testWorkflowAppender struct{ repository *jobspostgres.Repository }

func (a testWorkflowAppender) RecordWorkflow(ctx context.Context, tx Tx, intent jobs.WorkflowIntent) error {
	return a.repository.RecordWorkflow(ctx, tx, intent)
}

func TestReleasePostgresLifecycleAndIdempotentReplay(t *testing.T) {
	p := testDB(t)
	id := identity(t, "generation_1")
	prov := provenance(t, id)
	in := release.CreateInput{ID: "release_1", ServingIdentity: id, ProjectDigest: prov.Artifact.ProjectDigest, ArtifactDigest: prov.Artifact.ContentDigest, RequestDigest: digest("6"), IdempotencyKey: "request_1", CreatedBy: "principal_1", Connections: []release.ConnectionPin{{ConnectionID: "orders", RevisionID: digest("7")}, {ConnectionID: "customers", RevisionID: digest("8")}}, Provenance: &prov}
	r := New(p)
	created, err := r.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != release.StatusDraft || len(created.Manifest.Connections) != 2 || created.Manifest.Connections[0].ConnectionID != "customers" || created.Manifest.Connections[1].ConnectionID != "orders" {
		t.Fatalf("created = %#v", created)
	}
	replayInput := in
	replayInput.Connections = []release.ConnectionPin{in.Connections[1], in.Connections[0]}
	replayed, err := r.Create(context.Background(), replayInput)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != created.ID || len(replayed.Manifest.Connections) != 2 || replayed.Manifest.Connections[0].ConnectionID != "customers" || replayed.Manifest.Connections[1].ConnectionID != "orders" {
		t.Fatalf("replay = %#v", replayed)
	}
	if err := r.RecordArtifact(context.Background(), release.Artifact{ReleaseID: created.ID, ServingIdentity: id, ExpectedDigest: in.ArtifactDigest, ActualDigest: in.ArtifactDigest, SizeBytes: 42}); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordArtifact(context.Background(), release.Artifact{ReleaseID: created.ID, ServingIdentity: id, ExpectedDigest: in.ArtifactDigest, ActualDigest: in.ArtifactDigest, SizeBytes: 42}); err != nil {
		t.Fatalf("exact artifact replay: %v", err)
	}
	if err := r.RecordArtifact(context.Background(), release.Artifact{ReleaseID: created.ID, ServingIdentity: id, ExpectedDigest: in.ArtifactDigest, ActualDigest: in.ArtifactDigest, SizeBytes: 43}); !errors.Is(err, release.ErrConflict) {
		t.Fatalf("divergent artifact replay = %v, want conflict", err)
	}
	if _, err := r.BeginFinalization(context.Background(), id.ProjectID.String(), created.ID, structWorkflow()); err != nil {
		t.Fatal(err)
	}
	ready, err := r.CompleteFinalization(context.Background(), id.ProjectID.String(), created.ID, in.ArtifactDigest)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Status != release.StatusReady || ready.FinalizedAt == "" {
		t.Fatalf("ready = %#v", ready)
	}
	if _, err := r.CompleteFinalization(context.Background(), id.ProjectID.String(), created.ID, digest("8")); !errors.Is(err, release.ErrConflict) {
		t.Fatalf("divergent ready replay = %v, want conflict", err)
	}
	if _, err := r.CompleteFinalization(context.Background(), id.ProjectID.String(), created.ID, in.ArtifactDigest); err != nil {
		t.Fatal(err)
	}
}

func TestReleasePostgresConcurrentCompletionConvergesAndAppendsEffectsOnce(t *testing.T) {
	p := testEffectsDB(t)
	id := identity(t, "generation_complete_concurrent")
	prov := provenance(t, id)
	in := release.CreateInput{ID: "release_complete_concurrent", ServingIdentity: id, ProjectDigest: prov.Artifact.ProjectDigest, ArtifactDigest: prov.Artifact.ContentDigest, RequestDigest: digest("6"), IdempotencyKey: "request_complete_concurrent", CreatedBy: "principal_1", Provenance: &prov}
	r := NewWithOptions(p, Options{Audit: testAuditAppender{}, Events: testEventAppender{}})
	created, err := r.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RecordArtifact(context.Background(), release.Artifact{ReleaseID: created.ID, ServingIdentity: id, ExpectedDigest: in.ArtifactDigest, ActualDigest: in.ArtifactDigest, SizeBytes: 42}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.BeginFinalization(context.Background(), id.ProjectID.String(), created.ID, structWorkflow()); err != nil {
		t.Fatal(err)
	}
	intent := access.AuditIntent{EventID: "20000000-0000-0000-0000-000000000003", Source: "release", Operation: "completeRelease", Action: "release.ready", ResourceKind: "project", ResourceID: id.ProjectID.String(), Capability: access.CapabilityResourcePublish, Outcome: "success", AggregateKey: "release:" + id.ProjectID.String() + ":" + created.ID, AggregateSequence: 1, MetadataJSON: `{}`}
	ctx := release.WithAuditIntent(context.Background(), intent)
	const n = 8
	results := make(chan error, n)
	statuses := make(chan release.Status, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			row, completeErr := r.CompleteFinalization(ctx, id.ProjectID.String(), created.ID, in.ArtifactDigest)
			if completeErr == nil {
				statuses <- row.Status
			}
			results <- completeErr
		}()
	}
	wg.Wait()
	close(results)
	close(statuses)
	for completeErr := range results {
		if completeErr != nil {
			t.Errorf("concurrent completion: %v", completeErr)
		}
	}
	for status := range statuses {
		if status != release.StatusReady {
			t.Errorf("completion status = %q, want ready", status)
		}
	}
	var auditCount, readyEventCount int
	if err := p.QueryRow(context.Background(), `SELECT count(*) FROM audit.audit_event WHERE audit_id=$1::uuid`, intent.EventID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(context.Background(), `SELECT count(*) FROM event.event_log WHERE aggregate_type='release' AND aggregate_id=$1 AND event_type='release.ready'`, created.ID).Scan(&readyEventCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 || readyEventCount != 1 {
		t.Fatalf("ready side effects audit=%d events=%d, want one each", auditCount, readyEventCount)
	}
}

func structWorkflow() jobs.WorkflowIntent { return jobs.WorkflowIntent{} }

func TestReleasePostgresConcurrentCreateConverges(t *testing.T) {
	p := testDB(t)
	id := identity(t, "generation_concurrent")
	prov := provenance(t, id)
	in := release.CreateInput{ID: "release_concurrent", ServingIdentity: id, ProjectDigest: prov.Artifact.ProjectDigest, ArtifactDigest: prov.Artifact.ContentDigest, RequestDigest: digest("6"), IdempotencyKey: "request_concurrent", CreatedBy: "principal_1", Provenance: &prov}
	r := New(p)
	const n = 8
	results := make(chan error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() { defer wg.Done(); _, err := r.Create(context.Background(), in); results <- err }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Errorf("concurrent create: %v", err)
		}
	}
	rows, err := r.List(context.Background(), id.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want one immutable release", len(rows))
	}
	if _, err := r.Create(context.Background(), release.CreateInput{ID: "other", ServingIdentity: id, ProjectDigest: in.ProjectDigest, ArtifactDigest: in.ArtifactDigest, RequestDigest: digest("8"), IdempotencyKey: in.IdempotencyKey, CreatedBy: in.CreatedBy, Provenance: &prov}); !errors.Is(err, release.ErrConflict) {
		t.Fatalf("idempotency conflict = %v", err)
	}
}

func TestReleasePostgresSourceTransactionAuditEventAtomicAndReplaySafe(t *testing.T) {
	p := testEffectsDB(t)
	id := identity(t, "generation_effects")
	prov := provenance(t, id)
	intent := access.AuditIntent{EventID: "20000000-0000-0000-0000-000000000001", Source: "release", Operation: "createRelease", Action: "release.created", ResourceKind: "project", ResourceID: id.ProjectID.String(), Capability: access.CapabilityResourcePublish, Outcome: "success", AggregateKey: "release:" + id.ProjectID.String() + ":release_effects", AggregateSequence: 1, MetadataJSON: `{}`}
	in := release.CreateInput{ID: "release_effects", ServingIdentity: id, ProjectDigest: prov.Artifact.ProjectDigest, ArtifactDigest: prov.Artifact.ContentDigest, RequestDigest: digest("6"), IdempotencyKey: "request_effects", CreatedBy: "principal_1", Provenance: &prov}
	r := NewWithOptions(p, Options{Audit: testAuditAppender{}, Events: testEventAppender{}})
	if _, err := r.Create(release.WithAuditIntent(context.Background(), intent), in); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Create(release.WithAuditIntent(context.Background(), intent), in); err != nil {
		t.Fatal(err)
	}
	var auditCount, eventCount int
	if err := p.QueryRow(context.Background(), `SELECT count(*) FROM audit.audit_event WHERE audit_id = $1::uuid`, intent.EventID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(context.Background(), `SELECT count(*) FROM event.event_log WHERE aggregate_type='release' AND aggregate_id=$1`, in.ID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 || eventCount != 1 {
		t.Fatalf("side effects audit=%d event=%d, want one each", auditCount, eventCount)
	}
	var eventID string
	if err := p.QueryRow(context.Background(), `SELECT event_id::text FROM event.event_log WHERE aggregate_id=$1`, in.ID).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	if len(eventID) != 36 || eventID[14] != '7' {
		t.Fatalf("event id = %q, want UUIDv7", eventID)
	}
}

func TestReleasePostgresTransactionCommitsAndRollsBackWorkflowAuditEventTogether(t *testing.T) {
	p := testEffectsDB(t)
	id := identity(t, "generation_workflow_atomic")
	prov := provenance(t, id)
	in := release.CreateInput{ID: "release_workflow_atomic", ServingIdentity: id, ProjectDigest: prov.Artifact.ProjectDigest, ArtifactDigest: prov.Artifact.ContentDigest, RequestDigest: digest("6"), IdempotencyKey: "request_workflow_atomic", CreatedBy: "principal_1", Provenance: &prov}
	base := NewWithOptions(p, Options{Audit: testAuditAppender{}, Events: testEventAppender{}, Workflow: testWorkflowAppender{repository: jobspostgres.NewRepository(p)}})
	created, err := base.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if err := base.RecordArtifact(context.Background(), release.Artifact{ReleaseID: created.ID, ServingIdentity: id, ExpectedDigest: in.ArtifactDigest, ActualDigest: in.ArtifactDigest, SizeBytes: 42}); err != nil {
		t.Fatal(err)
	}
	jobID := "release:" + created.ID + ":finalize"
	workflow := jobs.WorkflowIntent{
		Event: jobs.EventInput{Key: "release.workflow.atomic", ResourceKind: "release", ResourceID: created.ID, EventType: "release.workflow", Data: []byte(`{"status":"queued"}`)},
		Job:   jobs.EnqueueInput{ID: jobID, Kind: "release.finalize", WorkloadClass: "control", PrincipalID: "principal_1", PartitionKey: "release:" + id.ProjectID.String(), ResourceKind: "release", ResourceID: created.ID, EstimatedMemoryBytes: 16 << 20, Payload: []byte(`{}`)},
	}
	intent := access.AuditIntent{EventID: "20000000-0000-0000-0000-000000000010", Source: "release", Operation: "finalizeRelease", Action: "release.validating", ResourceKind: "project", ResourceID: id.ProjectID.String(), Capability: access.CapabilityResourcePublish, Outcome: "success", AggregateKey: "release:" + id.ProjectID.String() + ":" + created.ID, AggregateSequence: 3, MetadataJSON: `{}`}

	tx, err := p.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.BeginFinalizationTx(release.WithAuditIntent(context.Background(), intent), tx, id.ProjectID.String(), created.ID, workflow); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := p.QueryRow(context.Background(), `SELECT status FROM release.release_record WHERE release_id=$1`, created.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(release.StatusDraft) {
		t.Fatalf("rolled back release status = %q, want draft", status)
	}
	var count int
	if err := p.QueryRow(context.Background(), `SELECT count(*) FROM audit.audit_event WHERE audit_id=$1::uuid`, intent.EventID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled back audit rows = %d, want zero", count)
	}
	if err := p.QueryRow(context.Background(), `SELECT count(*) FROM jobs.event WHERE resource_kind='release' AND resource_id=$1 AND event_key=$2`, created.ID, workflow.Event.Key).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled back workflow rows = %d, want zero", count)
	}
	if err := p.QueryRow(context.Background(), `SELECT count(*) FROM jobs.job_history WHERE id=$1`, jobID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled back workflow jobs = %d, want zero", count)
	}
	if err := p.QueryRow(context.Background(), `SELECT count(*) FROM event.event_log WHERE aggregate_id=$1 AND event_type='release.validating'`, created.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled back domain event rows = %d, want zero", count)
	}

	tx, err = p.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.BeginFinalizationTx(release.WithAuditIntent(context.Background(), intent), tx, id.ProjectID.String(), created.ID, workflow); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(context.Background(), `SELECT status FROM release.release_record WHERE release_id=$1`, created.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(release.StatusValidating) {
		t.Fatalf("committed release status = %q, want validating", status)
	}
	if err := p.QueryRow(context.Background(), `SELECT count(*) FROM audit.audit_event WHERE audit_id=$1::uuid`, intent.EventID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("committed audit rows = %d, want one", count)
	}
	if err := p.QueryRow(context.Background(), `SELECT count(*) FROM jobs.event WHERE resource_kind='release' AND resource_id=$1 AND event_key=$2`, created.ID, workflow.Event.Key).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("committed workflow rows = %d, want one", count)
	}
	if err := p.QueryRow(context.Background(), `SELECT count(*) FROM jobs.job_history WHERE id=$1`, jobID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("committed workflow jobs = %d, want one", count)
	}
	if err := p.QueryRow(context.Background(), `SELECT count(*) FROM event.event_log WHERE aggregate_id=$1 AND event_type='release.validating'`, created.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("committed domain event rows = %d, want one", count)
	}
}

type successfulAudit struct{}

func TestWithTxPreservesTransactionalSideEffectAuthorities(t *testing.T) {
	p := testEffectsDB(t)
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	base := NewWithOptions(p, Options{Audit: testAuditAppender{}, Events: testEventAppender{}, Workflow: testWorkflowAppender{repository: jobspostgres.NewRepository(p)}})
	bound := base.WithTx(tx)
	if !bound.AuditCapable() || !bound.EventCapable() || !bound.WorkflowCapable() {
		t.Fatalf("transaction-bound repository dropped side-effect authorities: audit=%t events=%t workflow=%t", bound.AuditCapable(), bound.EventCapable(), bound.WorkflowCapable())
	}
}

func (successfulAudit) RecordAuditEvent(context.Context, Tx, access.AuditIntent) (AuditEvent, error) {
	return AuditEvent{}, nil
}

type failingEvent struct{}

func (failingEvent) AppendEvent(context.Context, Tx, EventInput) (Event, error) {
	return Event{}, errors.New("event append failed")
}

func TestReleasePostgresEventFailureRollsBackSourceMutation(t *testing.T) {
	p := testEffectsDB(t)
	id := identity(t, "generation_event_rollback")
	prov := provenance(t, id)
	in := release.CreateInput{ID: "release_event_rollback", ServingIdentity: id, ProjectDigest: prov.Artifact.ProjectDigest, ArtifactDigest: prov.Artifact.ContentDigest, RequestDigest: digest("6"), IdempotencyKey: "request_event_rollback", CreatedBy: "principal_1", Provenance: &prov}
	intent := access.AuditIntent{EventID: "20000000-0000-0000-0000-000000000002", Source: "release", Operation: "createRelease", Action: "release.created", ResourceKind: "project", ResourceID: id.ProjectID.String(), Capability: access.CapabilityResourcePublish, Outcome: "success", AggregateKey: "release:" + id.ProjectID.String() + ":" + in.ID, AggregateSequence: 1, MetadataJSON: `{}`}
	r := NewWithOptions(p, Options{Audit: testAuditAppender{}, Events: failingEvent{}})
	if _, err := r.Create(release.WithAuditIntent(context.Background(), intent), in); err == nil {
		t.Fatal("Create unexpectedly succeeded")
	}
	var count int
	if err := p.QueryRow(context.Background(), `SELECT count(*) FROM release.release_record WHERE release_id=$1`, in.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("release rows after event rollback = %d, want zero", count)
	}
	if err := p.QueryRow(context.Background(), `SELECT count(*) FROM audit.audit_event WHERE audit_id=$1::uuid`, intent.EventID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("audit rows after event rollback = %d, want zero", count)
	}
}

func TestReleasePostgresDatabaseGuardsStateShapesAndImmutableEvidence(t *testing.T) {
	p := testDB(t)
	id := identity(t, "generation_guards")
	prov := provenance(t, id)
	in := release.CreateInput{ID: "release_guards", ServingIdentity: id, ProjectDigest: prov.Artifact.ProjectDigest, ArtifactDigest: prov.Artifact.ContentDigest, RequestDigest: digest("6"), IdempotencyKey: "request_guards", CreatedBy: "principal_1", Provenance: &prov}
	r := New(p)
	if _, err := r.Create(context.Background(), in); err != nil {
		t.Fatal(err)
	}

	// State-shape checks reject malformed terminal rows even for direct SQL.
	if _, err := p.Exec(context.Background(), `
		INSERT INTO release.release_record
		    (release_id, project_id, environment, generation_id, project_digest,
		     artifact_digest, request_digest, idempotency_key, status, provenance, created_by)
		VALUES ('malformed', $1, $2, $3, $4, $5, $6, 'malformed', 'ready', '{}'::jsonb, 'tester')`,
		id.ProjectID.String(), id.Environment, id.GenerationID, in.ProjectDigest, in.ArtifactDigest, digest("9")); err == nil {
		t.Fatal("malformed ready insert unexpectedly succeeded")
	}

	// Identity, timestamps, terminal fields, and partial artifact writes are immutable.
	for name, query := range map[string]string{
		"created_at":       `UPDATE release.release_record SET created_at = created_at + interval '1 second' WHERE release_id = 'release_guards'`,
		"draft_error":      `UPDATE release.release_record SET error = 'tampered' WHERE release_id = 'release_guards'`,
		"partial_artifact": `UPDATE release.release_record SET artifact_actual_digest = $1 WHERE release_id = 'release_guards'`,
		"direct_ready":     `UPDATE release.release_record SET status = 'ready' WHERE release_id = 'release_guards'`,
	} {
		var err error
		if name == "partial_artifact" {
			_, err = p.Exec(context.Background(), query, in.ArtifactDigest)
		} else {
			_, err = p.Exec(context.Background(), query)
		}
		if err == nil {
			t.Fatalf("%s mutation unexpectedly succeeded", name)
		}
	}

	// Connection pins can be inserted while the release is still an un-uploaded draft.
	if _, err := p.Exec(context.Background(), `
		INSERT INTO release.release_connection (release_id, connection_id, revision_id)
		VALUES ('release_guards', 'initial', $1)`, digest("7")); err != nil {
		t.Fatal("draft connection insert: ", err)
	}
	if err := r.RecordArtifact(context.Background(), release.Artifact{ReleaseID: in.ID, ServingIdentity: id, ExpectedDigest: in.ArtifactDigest, ActualDigest: in.ArtifactDigest, SizeBytes: 42}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(context.Background(), `
		INSERT INTO release.release_connection (release_id, connection_id, revision_id)
		VALUES ('release_guards', 'late', $1)`, digest("8")); err == nil {
		t.Fatal("post-upload connection insert unexpectedly succeeded")
	}
	if _, err := p.Exec(context.Background(), `
		UPDATE release.release_record SET artifact_size_bytes = 43 WHERE release_id = 'release_guards'`); err == nil {
		t.Fatal("uploaded artifact mutation unexpectedly succeeded")
	}

	if _, err := r.BeginFinalization(context.Background(), id.ProjectID.String(), in.ID, structWorkflow()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CompleteFinalization(context.Background(), id.ProjectID.String(), in.ID, in.ArtifactDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(context.Background(), `
		UPDATE release.release_record SET error = 'tampered' WHERE release_id = 'release_guards'`); err == nil {
		t.Fatal("terminal error mutation unexpectedly succeeded")
	}
}
