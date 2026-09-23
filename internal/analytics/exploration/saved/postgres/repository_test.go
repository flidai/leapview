package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	canonical "github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/analytics/exploration/saved"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5/pgxpool"
)

const testActorID = "01900000-0000-7000-8000-000000000001"

func TestRepositoryTypedNilDependenciesFailClosed(t *testing.T) {
	var db *pgxpool.Pool
	var audit *accessAuditAdapter
	repository := New(db, audit)
	if repository.Configured() || repository.AuditCapable() {
		t.Fatal("typed nil PostgreSQL dependencies were reported as configured")
	}
	if _, err := repository.GetLifecycle(t.Context(), saved.ReadInput{ProjectID: "project:sales", ID: "exploration-1"}); !errors.Is(err, saved.ErrUnavailable) {
		t.Fatalf("typed nil lifecycle read = %v, want unavailable", err)
	}
}

type accessAuditAdapter struct {
	repository *accesspostgres.AuditRepository
}

func (a accessAuditAdapter) RecordAuditEvent(ctx context.Context, tx Tx, intent access.AuditIntent) error {
	_, err := a.repository.RecordAuditEvent(ctx, tx, intent)
	return err
}

type failingAuditAdapter struct{ err error }

func (a failingAuditAdapter) RecordAuditEvent(context.Context, Tx, access.AuditIntent) error {
	return a.err
}

type repositoryFixture struct {
	db       *pgxpool.Pool
	repo     *Repository
	now      time.Time
	payload  saved.ExplorationSpecPayload
	identity projectgraph.ServingIdentity
}

func newRepositoryFixture(t *testing.T) *repositoryFixture {
	t.Helper()
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "saved_exploration_test")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	tx, err := db.Begin(t.Context())
	if err != nil {
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
	payload, err := saved.NewExplorationSpecPayload(canonical.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales",
		Dimensions: []canonical.ExplorationDimensionRef{{Field: "orders.status"}},
		Metrics:    []canonical.ExplorationMetricRef{{Field: "order_count"}},
		Filters:    []canonical.ExplorationFilter{}, Sort: []canonical.ExplorationSort{}, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project:sales", "production", "generation-1")
	if err != nil {
		t.Fatal(err)
	}
	audit := accessAuditAdapter{repository: accesspostgres.New()}
	return &repositoryFixture{db: db, repo: New(db, audit), now: time.Date(2026, 9, 4, 12, 0, 0, 123456789, time.UTC), payload: payload, identity: identity}
}

func (f *repositoryFixture) evidence(t *testing.T, action saved.MutationAction, key string, at time.Time) saved.MutationEvidence {
	t.Helper()
	fingerprint, err := saved.CanonicalFingerprint(struct {
		Action saved.MutationAction `json:"action"`
		Key    string               `json:"key"`
	}{action, key})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := saved.NewMutationEvidence(testActorID, action, key, fingerprint, "request-"+key, "correlation-"+key, at)
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func (f *repositoryFixture) intent() access.AuditIntent { return access.AuditIntent{} }

func (f *repositoryFixture) createInput(t *testing.T, id, slug string, evidence saved.MutationEvidence, createdAt time.Time) saved.CreateInput {
	t.Helper()
	revision, err := saved.NewRevision(saved.RevisionID("revision-"+id), 1, createdAt, testActorID, f.payload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	return saved.CreateInput{ProjectID: "project:sales", ID: saved.ExplorationID(id), OwnerPrincipalID: testActorID, Title: "Orders " + id, Slug: slug,
		Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: createdAt, Revision: revision, Evidence: evidence}
}

func (f *repositoryFixture) create(t *testing.T, id, slug, key string) (saved.CreateInput, saved.MutationResult) {
	t.Helper()
	evidence := f.evidence(t, saved.MutationActionCreate, key, f.now)
	input := f.createInput(t, id, slug, evidence, f.now)
	result, err := f.repo.Create(saved.WithAuditIntent(t.Context(), f.intent()), input)
	if err != nil {
		t.Fatal(err)
	}
	return input, result
}

func TestRepositoryPostgreSQLExactBytesTimestampsAndReplayWithoutAudit(t *testing.T) {
	f := newRepositoryFixture(t)
	input, created := f.create(t, "exploration-1", "orders", "create-1")
	if !created.Lifecycle.CreatedAt.Equal(f.now) || !created.Lifecycle.CurrentRevision.CreatedAt.Equal(f.now) {
		t.Fatalf("nanosecond timestamps changed: lifecycle=%s revision=%s want=%s", created.Lifecycle.CreatedAt, created.Lifecycle.CurrentRevision.CreatedAt, f.now)
	}
	loaded, err := f.repo.GetRevision(t.Context(), saved.RevisionReadInput{ProjectID: input.ProjectID, ID: input.ID, Revision: created.AppliedRevision})
	if err != nil {
		t.Fatal(err)
	}
	if string(loaded.Payload.Canonical()) != string(input.Revision.Payload.Canonical()) || loaded.Metadata.CreatedAt != input.Revision.Metadata.CreatedAt {
		t.Fatal("PostgreSQL revision did not preserve exact canonical bytes or timestamp")
	}
	// An exact retry replays from the durable operation snapshot and does not
	// require the audit recorder or an audit intent in its context.
	replayRepo := New(f.db, nil)
	replay, err := replayRepo.Create(t.Context(), input)
	if err != nil || !replay.Replayed || replay.AppliedRevision != created.AppliedRevision {
		t.Fatalf("replay without audit = %#v, %v", replay, err)
	}
	if _, err := f.repo.GetLifecycle(t.Context(), saved.ReadInput{ProjectID: "project:other", ID: input.ID}); !errors.Is(err, saved.ErrNotFound) {
		t.Fatalf("cross-project lifecycle = %v, want not found", err)
	}
}

func TestRepositoryPostgreSQLSameTimestampUpdateAndArchive(t *testing.T) {
	f := newRepositoryFixture(t)
	_, created := f.create(t, "exploration-same-time", "same-time", "same-time-create")
	next, err := saved.NewRevision("revision-same-time-2", 2, f.now, testActorID, f.payload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	evidence := f.evidence(t, saved.MutationActionUpdate, "same-time-update", f.now)
	updated, err := f.repo.UpdateVersion(saved.WithAuditIntent(t.Context(), f.intent()), saved.UpdateVersionInput{
		ProjectID: "project:sales", ID: created.Lifecycle.ID, ExpectedRevision: created.AppliedRevision, Revision: next,
		Title: "Same time", Slug: "same-time-v2", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", UpdatedAt: f.now, Evidence: evidence,
	})
	if err != nil {
		t.Fatal(err)
	}
	archiveEvidence := f.evidence(t, saved.MutationActionArchive, "same-time-archive", f.now)
	archived, err := f.repo.Archive(saved.WithAuditIntent(t.Context(), f.intent()), saved.ArchiveInput{ProjectID: "project:sales", ID: created.Lifecycle.ID, ExpectedRevision: updated.AppliedRevision, ArchivedAt: f.now, Evidence: archiveEvidence})
	if err != nil {
		t.Fatal(err)
	}
	if archived.Lifecycle.Status != saved.StatusArchived || archived.Lifecycle.ArchivedAt == nil || !archived.Lifecycle.ArchivedAt.Equal(f.now) {
		t.Fatalf("archive = %#v", archived)
	}
}

func TestRepositoryPostgreSQLAuditFailureRollsBackAndStaleCAS(t *testing.T) {
	f := newRepositoryFixture(t)
	_, created := f.create(t, "exploration-rollback", "rollback", "rollback-create")
	next, err := saved.NewRevision("revision-rollback-2", 2, f.now.Add(time.Minute), testActorID, f.payload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	evidence := f.evidence(t, saved.MutationActionUpdate, "rollback-update", f.now.Add(time.Minute))
	failing := New(f.db, failingAuditAdapter{err: errors.New("injected audit failure")})
	_, err = failing.UpdateVersion(saved.WithAuditIntent(t.Context(), f.intent()), saved.UpdateVersionInput{ProjectID: "project:sales", ID: created.Lifecycle.ID, ExpectedRevision: created.AppliedRevision, Revision: next, Title: "Failed", Slug: "failed", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", UpdatedAt: f.now.Add(time.Minute), Evidence: evidence})
	if !errors.Is(err, saved.ErrUnavailable) || err.Error() == "" {
		t.Fatalf("audit failure = %v, want unavailable", err)
	}
	var revisions int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM saved_exploration.saved_exploration_revisions WHERE project_id=$1 AND exploration_id=$2`, "project:sales", created.Lifecycle.ID.String()).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if revisions != 1 {
		t.Fatalf("revision rows after audit failure = %d, want 1", revisions)
	}
	stale := created.AppliedRevision
	stale.RevisionID = "revision-stale"
	staleEvidence := f.evidence(t, saved.MutationActionUpdate, "stale-update", f.now.Add(time.Minute))
	_, err = f.repo.UpdateVersion(saved.WithAuditIntent(t.Context(), f.intent()), saved.UpdateVersionInput{ProjectID: "project:sales", ID: created.Lifecycle.ID, ExpectedRevision: stale, Revision: next, Title: "Stale", Slug: "stale", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", UpdatedAt: f.now.Add(time.Minute), Evidence: staleEvidence})
	if !errors.Is(err, saved.ErrStaleRevision) {
		t.Fatalf("stale CAS = %v, want stale revision", err)
	}
}

func TestRepositoryPostgreSQLConcurrentSameKeyCreateReplays(t *testing.T) {
	f := newRepositoryFixture(t)
	evidence := f.evidence(t, saved.MutationActionCreate, "parallel-create", f.now)
	input := f.createInput(t, "exploration-parallel", "parallel", evidence, f.now)
	ctx := saved.WithAuditIntent(t.Context(), f.intent())
	results := make([]saved.MutationResult, 2)
	errs := make([]error, 2)
	var group sync.WaitGroup
	for index := range results {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			results[index], errs[index] = f.repo.Create(ctx, input)
		}(index)
	}
	group.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("parallel create %d = %v", index, err)
		}
	}
	if results[0].Replayed == results[1].Replayed {
		t.Fatalf("parallel replay flags = %t/%t, want one replay", results[0].Replayed, results[1].Replayed)
	}
	var lifecycle, revisions, operations, audits int
	queries := []struct {
		query string
		into  *int
	}{
		{`SELECT count(*) FROM saved_exploration.saved_explorations`, &lifecycle},
		{`SELECT count(*) FROM saved_exploration.saved_exploration_revisions`, &revisions},
		{`SELECT count(*) FROM saved_exploration.saved_exploration_operations`, &operations},
		{`SELECT count(*) FROM audit.audit_event`, &audits},
	}
	for _, query := range queries {
		if err := f.db.QueryRow(t.Context(), query.query).Scan(query.into); err != nil {
			t.Fatal(err)
		}
	}
	if lifecycle != 1 || revisions != 1 || operations != 1 || audits != 1 {
		t.Fatalf("parallel durable counts = lifecycle=%d revisions=%d operations=%d audits=%d", lifecycle, revisions, operations, audits)
	}
}

func TestRepositoryPostgreSQLSchemaRejectsInvalidTimestampAndInitialRevision(t *testing.T) {
	f := newRepositoryFixture(t)
	var valid, invalidDate, invalidHour, invalidSecond bool
	if err := f.db.QueryRow(t.Context(), `SELECT saved_exploration.is_utc_timestamp($1), saved_exploration.is_utc_timestamp($2), saved_exploration.is_utc_timestamp($3), saved_exploration.is_utc_timestamp($4)`, "2026-02-28T12:00:00.123456789Z", "2026-02-30T12:00:00Z", "2026-02-28T24:00:00Z", "2026-02-28T12:00:60Z").Scan(&valid, &invalidDate, &invalidHour, &invalidSecond); err != nil {
		t.Fatal(err)
	}
	if !valid || invalidDate || invalidHour || invalidSecond {
		t.Fatalf("timestamp validator = valid:%t date:%t hour:%t second:%t", valid, invalidDate, invalidHour, invalidSecond)
	}
	// The initial-lifecycle trigger must reject a direct revision-two identity,
	// even though repository inputs already validate this at the domain edge.
	_, err := f.db.Exec(t.Context(), `INSERT INTO saved_exploration.saved_explorations (project_id, exploration_id, owner_principal_id, title, slug, visibility, status, semantic_model_id, created_at, updated_at, current_revision_id, current_revision_number, current_content_hash) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9,$10,$11,$12)`, "project:sales", "tampered", testActorID, "Tampered", "tampered", "private", "active", "semantic:sales", formatTime(f.now), "revision-2", 2, f.payload.ContentHash())
	if err == nil {
		t.Fatal("initial revision-two identity was accepted")
	}
	_, _ = f.create(t, "tamper-sequence", "tamper-sequence", "tamper-sequence-create")
	_, err = f.db.Exec(t.Context(), `INSERT INTO saved_exploration.saved_exploration_revisions (project_id, exploration_id, revision_id, revision_number, spec_envelope_version, spec_canonical_json, content_hash, created_by, created_at, serving_project_id, serving_environment, serving_generation_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$1,$10,$11)`, "project:sales", "tamper-sequence", "revision-3", 3, int32(f.payload.Version()), f.payload.Canonical(), f.payload.ContentHash(), testActorID, formatTime(f.now.Add(time.Minute)), "production", "generation-1")
	if err == nil {
		t.Fatal("noncontiguous revision was accepted")
	}
	_, _ = f.create(t, "tamper-json", "tamper-json", "tamper-json-create")
	_, err = f.db.Exec(t.Context(), `INSERT INTO saved_exploration.saved_exploration_revisions (project_id, exploration_id, revision_id, revision_number, spec_envelope_version, spec_canonical_json, content_hash, created_by, created_at, serving_project_id, serving_environment, serving_generation_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$1,$10,$11)`, "project:sales", "tamper-json", "revision-2", 2, int32(1), []byte(`{"version":1,"spec":{"modelId":123}}`), f.payload.ContentHash(), testActorID, formatTime(f.now), "production", "generation-1")
	if err == nil {
		t.Fatal("numeric semantic model id was accepted")
	}
}
