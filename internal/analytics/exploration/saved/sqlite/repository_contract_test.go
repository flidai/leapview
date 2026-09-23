package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	canonical "github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/analytics/exploration/saved"
	"github.com/flidai/leapview/internal/platform"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type repositoryContractFixture struct {
	db       *sql.DB
	store    *platform.Store
	dbPath   string
	repo     *Repository
	now      time.Time
	payload  saved.ExplorationSpecPayload
	identity projectgraph.ServingIdentity
}

type repositoryRowCounts struct {
	lifecycle, revisions, operations, outbox int
}

func newRepositoryContractFixture(t *testing.T) *repositoryContractFixture {
	t.Helper()
	ctx := context.Background()
	dbPath := t.TempDir() + "/platform.db"
	store, err := platform.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	db := store.SQLDB()
	for _, principal := range []string{"owner", "actor-two"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES (?, ?, ?)`, principal, principal+"@example.test", principal); err != nil {
			t.Fatal(err)
		}
	}
	payload, err := saved.NewExplorationSpecPayload(canonical.ExplorationSpec{
		SchemaVersion: 1,
		ModelID:       "semantic:sales",
		Dimensions:    []canonical.ExplorationDimensionRef{{Field: "orders.status"}},
		Metrics:       []canonical.ExplorationMetricRef{{Field: "order_count"}},
		Filters:       []canonical.ExplorationFilter{}, Sort: []canonical.ExplorationSort{}, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project:sales", "production", "generation-1")
	if err != nil {
		t.Fatal(err)
	}
	audit := accesssqlite.NewRepository(db)
	return &repositoryContractFixture{
		db: db, store: store, dbPath: dbPath, repo: NewRepositoryWithAudit(db, audit),
		now: time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC), payload: payload, identity: identity,
	}
}

func (f *repositoryContractFixture) evidence(t *testing.T, action saved.MutationAction, key string, at time.Time) saved.MutationEvidence {
	t.Helper()
	fingerprint, err := saved.CanonicalFingerprint(struct {
		Action saved.MutationAction `json:"action"`
		Key    string               `json:"key"`
	}{action, key})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := saved.NewMutationEvidence("owner", action, key, fingerprint, "request-"+key, "correlation-"+key, at)
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func (f *repositoryContractFixture) intent(action saved.MutationAction, event string) access.AuditIntent {
	return access.AuditIntent{
		EventID: "saved-exploration:" + event, Source: "analytics.exploration.saved",
		Operation: string(action) + "SavedExploration", Action: "saved_exploration." + string(action) + "d",
		Capability: access.CapabilityResourceEdit, Outcome: "success", MetadataJSON: `{}`,
	}
}

func (f *repositoryContractFixture) wrongIntent() access.AuditIntent {
	return access.AuditIntent{
		EventID: "caller-event", Source: "caller-source", Operation: "caller-operation",
		PrincipalID: "", Action: "caller.action", ResourceKind: "caller_resource", ResourceID: "caller-id",
		Capability: access.CapabilityResourceRead, Outcome: "caller-outcome", RequestID: "caller-request",
		CorrelationID: "caller-correlation", AggregateKey: "caller-aggregate", MetadataJSON: `{"callerSecret":"must-not-persist"}`,
	}
}

func (f *repositoryContractFixture) createInput(project, id, slug string, evidence saved.MutationEvidence) saved.CreateInput {
	identity := f.identity
	identity.ProjectID = projectgraph.ResourceID(project)
	revision, err := saved.NewRevision(saved.RevisionID("revision-"+id), 1, f.now, "owner", f.payload, identity)
	if err != nil {
		panic(err)
	}
	return saved.CreateInput{ProjectID: projectgraph.ResourceID(project), ID: saved.ExplorationID(id), OwnerPrincipalID: "owner", Title: "Orders " + id, Slug: slug,
		Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: f.now, Revision: revision, Evidence: evidence}
}

func (f *repositoryContractFixture) create(t *testing.T, project, id, slug, event string) (saved.CreateInput, saved.MutationResult) {
	t.Helper()
	evidence := f.evidence(t, saved.MutationActionCreate, event, f.now)
	input := f.createInput(project, id, slug, evidence)
	result, err := f.repo.Create(saved.WithAuditIntent(context.Background(), f.intent(saved.MutationActionCreate, event)), input)
	if err != nil {
		t.Fatal(err)
	}
	return input, result
}

func (f *repositoryContractFixture) counts(t *testing.T, project string) repositoryRowCounts {
	t.Helper()
	var counts repositoryRowCounts
	queries := []struct {
		query string
		value *int
	}{
		{`SELECT COUNT(*) FROM saved_explorations WHERE project_id = ?`, &counts.lifecycle},
		{`SELECT COUNT(*) FROM saved_exploration_revisions WHERE project_id = ?`, &counts.revisions},
		{`SELECT COUNT(*) FROM saved_exploration_operations WHERE project_id = ?`, &counts.operations},
		{`SELECT COUNT(*) FROM audit_outbox WHERE aggregate_key LIKE ?`, &counts.outbox},
	}
	for index, query := range queries {
		arg := project
		if index == 3 {
			arg = "saved_exploration:" + project + ":%"
		}
		if err := f.db.QueryRowContext(context.Background(), query.query, arg).Scan(query.value); err != nil {
			t.Fatal(err)
		}
	}
	return counts
}

func TestRepositoryContractProjectIsolationAndExactReads(t *testing.T) {
	f := newRepositoryContractFixture(t)
	salesInput, sales := f.create(t, "project:sales", "exploration-sales", "sales", "sales")
	_, marketing := f.create(t, "project:marketing", "exploration-marketing", "marketing", "marketing")

	if _, err := f.repo.GetLifecycle(context.Background(), saved.ReadInput{ProjectID: "project:sales", ID: marketing.Lifecycle.ID}); !errors.Is(err, saved.ErrNotFound) {
		t.Fatalf("cross-project lifecycle error = %v, want not found", err)
	}
	if _, err := f.repo.GetRevision(context.Background(), saved.RevisionReadInput{ProjectID: "project:sales", ID: marketing.Lifecycle.ID, Revision: marketing.AppliedRevision}); !errors.Is(err, saved.ErrNotFound) {
		t.Fatalf("cross-project revision error = %v, want not found", err)
	}
	rows, err := f.repo.List(context.Background(), saved.ListInput{ProjectID: "project:sales"})
	if err != nil || len(rows) != 1 || rows[0].ID != sales.Lifecycle.ID {
		t.Fatalf("sales list = %#v, %v", rows, err)
	}
	lookup, found, err := f.repo.LookupMutation(context.Background(), saved.MutationLookupInput{ProjectID: "project:sales", ActorID: "owner", Action: saved.MutationActionCreate, IdempotencyKey: "marketing", Fingerprint: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	if err != nil || found || lookup != (saved.MutationReplayMetadata{}) {
		t.Fatalf("cross-project mutation lookup = %#v, found=%t, err=%v", lookup, found, err)
	}

	marketingIdentity, err := projectgraph.NewServingIdentity("project:marketing", "production", "generation-2")
	if err != nil {
		t.Fatal(err)
	}
	next, err := saved.NewRevision("revision-cross-project", 2, f.now.Add(time.Minute), "owner", f.payload, marketingIdentity)
	if err != nil {
		t.Fatal(err)
	}
	updateEvidence := f.evidence(t, saved.MutationActionUpdate, "cross-update", f.now.Add(time.Minute))
	_, err = f.repo.UpdateVersion(saved.WithAuditIntent(context.Background(), f.intent(saved.MutationActionUpdate, "cross-update")), saved.UpdateVersionInput{ProjectID: "project:marketing", ID: sales.Lifecycle.ID, ExpectedRevision: sales.AppliedRevision, Revision: next, Title: "Cross", Slug: "cross", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", UpdatedAt: f.now.Add(time.Minute), Evidence: updateEvidence})
	if !errors.Is(err, saved.ErrNotFound) {
		t.Fatalf("cross-project update error = %v, want not found", err)
	}
	destinationRevision, err := saved.NewRevision("revision-cross-copy", 1, f.now.Add(time.Minute), "owner", f.payload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	duplicateEvidence := f.evidence(t, saved.MutationActionDuplicate, "cross-duplicate", f.now.Add(time.Minute))
	_, err = f.repo.Duplicate(saved.WithAuditIntent(context.Background(), f.intent(saved.MutationActionDuplicate, "cross-duplicate")), saved.DuplicateInput{ProjectID: "project:sales", SourceID: marketing.Lifecycle.ID, ExpectedSourceRevision: marketing.AppliedRevision, Evidence: duplicateEvidence, Destination: saved.CreateInput{ProjectID: "project:sales", ID: "exploration-copy", OwnerPrincipalID: "owner", Title: "Copy", Slug: "copy", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: f.now.Add(time.Minute), Revision: destinationRevision}})
	if !errors.Is(err, saved.ErrNotFound) {
		t.Fatalf("cross-project duplicate error = %v, want not found", err)
	}
	_ = salesInput
}

func TestRepositoryListPageUsesProjectScopedKeyset(t *testing.T) {
	f := newRepositoryContractFixture(t)
	f.create(t, "project:sales", "exploration-a", "sales-a", "list-a")
	f.create(t, "project:sales", "exploration-b", "sales-b", "list-b")
	f.create(t, "project:sales", "exploration-c", "sales-c", "list-c")
	f.create(t, "project:marketing", "exploration-z", "marketing-z", "list-z")

	first, err := f.repo.ListPage(context.Background(), saved.ListInput{ProjectID: "project:sales", Limit: 1})
	if err != nil || len(first.Items) != 1 || first.Items[0].ID != "exploration-a" || first.NextCursor != "exploration-a" {
		t.Fatalf("first repository page = %#v, %v", first, err)
	}
	second, err := f.repo.ListPage(context.Background(), saved.ListInput{ProjectID: "project:sales", Cursor: first.NextCursor, Limit: 1})
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != "exploration-b" || second.NextCursor != "exploration-b" {
		t.Fatalf("second repository page = %#v, %v", second, err)
	}
	if _, err := f.repo.ListPage(context.Background(), saved.ListInput{ProjectID: "project:marketing", Cursor: first.NextCursor, Limit: 1}); err != nil {
		t.Fatalf("marketing keyset page: %v", err)
	}
}

func TestRepositoryListOrdersByImmutableExplorationID(t *testing.T) {
	f := newRepositoryContractFixture(t)
	f.create(t, "project:sales", "exploration-z", "z-order", "order-z")
	f.create(t, "project:sales", "exploration-a", "a-order", "order-a")

	items, err := f.repo.List(context.Background(), saved.ListInput{ProjectID: "project:sales"})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]saved.ExplorationID, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	if len(ids) != 2 || ids[0] != "exploration-a" || ids[1] != "exploration-z" {
		t.Fatalf("list IDs = %#v, want immutable ID order [exploration-a exploration-z]", ids)
	}
}

func TestRepositoryContractStaleCASLeavesDurableRowsUnchanged(t *testing.T) {
	f := newRepositoryContractFixture(t)
	_, created := f.create(t, "project:sales", "exploration-1", "orders", "create")
	before := f.counts(t, "project:sales")
	stale := saved.RevisionToken{RevisionID: "revision-stale", Number: 1, ContentHash: created.AppliedRevision.ContentHash}
	next, err := saved.NewRevision("revision-2", 2, f.now.Add(time.Minute), "owner", f.payload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	updateEvidence := f.evidence(t, saved.MutationActionUpdate, "stale-update", f.now.Add(time.Minute))
	_, err = f.repo.UpdateVersion(saved.WithAuditIntent(context.Background(), f.intent(saved.MutationActionUpdate, "stale-update")), saved.UpdateVersionInput{ProjectID: "project:sales", ID: created.Lifecycle.ID, ExpectedRevision: stale, Revision: next, Title: "Stale", Slug: "stale", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", UpdatedAt: f.now.Add(time.Minute), Evidence: updateEvidence})
	if !errors.Is(err, saved.ErrStaleRevision) {
		t.Fatalf("stale update error = %v, want stale revision", err)
	}
	archiveEvidence := f.evidence(t, saved.MutationActionArchive, "stale-archive", f.now.Add(time.Minute))
	_, err = f.repo.Archive(saved.WithAuditIntent(context.Background(), f.intent(saved.MutationActionArchive, "stale-archive")), saved.ArchiveInput{ProjectID: "project:sales", ID: created.Lifecycle.ID, ExpectedRevision: stale, ArchivedAt: f.now.Add(time.Minute), Evidence: archiveEvidence})
	if !errors.Is(err, saved.ErrStaleRevision) {
		t.Fatalf("stale archive error = %v, want stale revision", err)
	}
	if got := f.counts(t, "project:sales"); got != before {
		t.Fatalf("rows after stale operations = %#v, want %#v", got, before)
	}
}

func TestRepositoryContractExactReplaySnapshotsAndArchive(t *testing.T) {
	f := newRepositoryContractFixture(t)
	createEvidence := f.evidence(t, saved.MutationActionCreate, "create", f.now)
	createEvidence.AdminOverride = true
	createEvidence.AdminReason = "approved migration"
	if err := createEvidence.Validate(); err != nil {
		t.Fatal(err)
	}
	createInput := f.createInput("project:sales", "exploration-1", "orders", createEvidence)
	created, err := f.repo.Create(saved.WithAuditIntent(context.Background(), f.intent(saved.MutationActionCreate, "create")), createInput)
	if err != nil {
		t.Fatal(err)
	}
	next, err := saved.NewRevision("revision-2", 2, f.now.Add(time.Minute), "owner", f.payload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	updateEvidence := f.evidence(t, saved.MutationActionUpdate, "update", f.now.Add(time.Minute))
	updateInput := saved.UpdateVersionInput{ProjectID: "project:sales", ID: created.Lifecycle.ID, ExpectedRevision: created.AppliedRevision, Revision: next, Title: "Orders v2", Slug: "orders-v2", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", UpdatedAt: f.now.Add(time.Minute), Evidence: updateEvidence}
	updated, err := f.repo.UpdateVersion(saved.WithAuditIntent(context.Background(), f.intent(saved.MutationActionUpdate, "update")), updateInput)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ConcurrencyRevision != updateInput.ExpectedRevision {
		t.Fatalf("update concurrency revision = %#v, want %#v", updated.ConcurrencyRevision, updateInput.ExpectedRevision)
	}
	archiveEvidence := f.evidence(t, saved.MutationActionArchive, "archive", f.now.Add(2*time.Minute))
	archiveInput := saved.ArchiveInput{ProjectID: "project:sales", ID: created.Lifecycle.ID, ExpectedRevision: updated.AppliedRevision, ArchivedAt: f.now.Add(2 * time.Minute), Evidence: archiveEvidence}
	archived, err := f.repo.Archive(saved.WithAuditIntent(context.Background(), f.intent(saved.MutationActionArchive, "archive")), archiveInput)
	if err != nil {
		t.Fatal(err)
	}
	if archived.ConcurrencyRevision != archiveInput.ExpectedRevision {
		t.Fatalf("archive concurrency revision = %#v, want %#v", archived.ConcurrencyRevision, archiveInput.ExpectedRevision)
	}
	before := f.counts(t, "project:sales")
	createReplay, err := f.repo.Create(context.Background(), createInput)
	if err != nil || !createReplay.Replayed || createReplay.Lifecycle.Title != created.Lifecycle.Title || createReplay.Revision == nil || createReplay.Revision.Metadata.Number != 1 || !createReplay.Evidence.AdminOverride || createReplay.Evidence.AdminReason != "approved migration" {
		t.Fatalf("create replay = %#v, %v", createReplay, err)
	}
	updateReplay, err := f.repo.UpdateVersion(context.Background(), updateInput)
	if err != nil || !updateReplay.Replayed || updateReplay.Lifecycle.Status != saved.StatusActive || updateReplay.Lifecycle.Title != updated.Lifecycle.Title || updateReplay.Revision == nil || updateReplay.AppliedRevision != updated.AppliedRevision || updateReplay.ConcurrencyRevision != updateInput.ExpectedRevision {
		t.Fatalf("update replay = %#v, %v", updateReplay, err)
	}
	archiveReplay, err := f.repo.Archive(context.Background(), archiveInput)
	if err != nil || !archiveReplay.Replayed || archiveReplay.Revision != nil || archiveReplay.Lifecycle.Status != saved.StatusArchived || archiveReplay.AppliedRevision != archived.AppliedRevision || archiveReplay.ConcurrencyRevision != archiveInput.ExpectedRevision {
		t.Fatalf("archive replay = %#v, %v", archiveReplay, err)
	}
	if got := f.counts(t, "project:sales"); got != before {
		t.Fatalf("rows after exact replays = %#v, want %#v", got, before)
	}
}

func TestRepositoryContractSameKeyDuplicateBytesAndAuditFailure(t *testing.T) {
	f := newRepositoryContractFixture(t)
	_, created := f.create(t, "project:sales", "exploration-source", "source", "create")
	before := f.counts(t, "project:sales")
	changedEvidence := f.evidence(t, saved.MutationActionCreate, "create", f.now)
	changedEvidence.Fingerprint = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	changedInput := f.createInput("project:sales", "exploration-source", "changed", changedEvidence)
	changedInput.Title = "Changed"
	if _, err := f.repo.Create(context.Background(), changedInput); !errors.Is(err, saved.ErrConflict) {
		t.Fatalf("same-key different-fingerprint error = %v, want conflict", err)
	}
	if got := f.counts(t, "project:sales"); got != before {
		t.Fatalf("rows after same-key conflict = %#v, want %#v", got, before)
	}

	destinationRevision, err := saved.NewRevision("revision-copy", 1, f.now.Add(time.Minute), "owner", f.payload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	duplicateEvidence := f.evidence(t, saved.MutationActionDuplicate, "duplicate", f.now.Add(time.Minute))
	duplicate, err := f.repo.Duplicate(saved.WithAuditIntent(context.Background(), f.intent(saved.MutationActionDuplicate, "duplicate")), saved.DuplicateInput{ProjectID: "project:sales", SourceID: created.Lifecycle.ID, ExpectedSourceRevision: created.AppliedRevision, Evidence: duplicateEvidence, Destination: saved.CreateInput{ProjectID: "project:sales", ID: "exploration-copy", OwnerPrincipalID: "owner", Title: "Copy", Slug: "copy", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: f.now.Add(time.Minute), Revision: destinationRevision}})
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.ConcurrencyRevision != created.AppliedRevision {
		t.Fatalf("duplicate concurrency revision = %#v, want %#v", duplicate.ConcurrencyRevision, created.AppliedRevision)
	}
	sourceRevision, err := f.repo.GetRevision(context.Background(), saved.RevisionReadInput{ProjectID: "project:sales", ID: created.Lifecycle.ID, Revision: created.AppliedRevision})
	if err != nil {
		t.Fatal(err)
	}
	destination, err := f.repo.GetRevision(context.Background(), saved.RevisionReadInput{ProjectID: "project:sales", ID: duplicate.Lifecycle.ID, Revision: duplicate.AppliedRevision})
	if err != nil {
		t.Fatal(err)
	}
	if string(sourceRevision.Payload.Canonical()) != string(destination.Payload.Canonical()) || sourceRevision.Metadata.ContentHash != destination.Metadata.ContentHash {
		t.Fatal("duplicate did not copy exact source payload bytes and content hash")
	}

	injectedAuditFailure := errors.New("injected audit failure")
	failing := NewRepositoryWithAudit(f.db, failingAuditRecorder{err: injectedAuditFailure})
	failedEvidence := f.evidence(t, saved.MutationActionUpdate, "failed-update", f.now.Add(2*time.Minute))
	failedRevision, err := saved.NewRevision("revision-failed", 2, f.now.Add(2*time.Minute), "owner", f.payload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failing.UpdateVersion(saved.WithAuditIntent(context.Background(), f.intent(saved.MutationActionUpdate, "failed-update")), saved.UpdateVersionInput{ProjectID: "project:sales", ID: created.Lifecycle.ID, ExpectedRevision: created.AppliedRevision, Revision: failedRevision, Title: "Failed", Slug: "failed", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", UpdatedAt: f.now.Add(2 * time.Minute), Evidence: failedEvidence}); !errors.Is(err, saved.ErrUnavailable) || !errors.Is(err, injectedAuditFailure) {
		t.Fatalf("injected audit update error = %v, want saved.ErrUnavailable and original cause", err)
	}
	if got := f.counts(t, "project:sales"); got.lifecycle != 2 || got.revisions != 2 || got.operations != 2 || got.outbox != 2 {
		t.Fatalf("rows after failing audit update = %#v", got)
	}

	missingEvidence := f.evidence(t, saved.MutationActionCreate, "missing-audit", f.now.Add(2*time.Minute))
	missingRevision, err := saved.NewRevision("revision-missing", 1, f.now.Add(2*time.Minute), "owner", f.payload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	noAudit := NewRepository(f.db)
	if _, err := noAudit.Create(context.Background(), saved.CreateInput{ProjectID: "project:sales", ID: "exploration-missing", OwnerPrincipalID: "owner", Title: "Missing", Slug: "missing", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: f.now.Add(2 * time.Minute), Revision: missingRevision, Evidence: missingEvidence}); !errors.Is(err, saved.ErrUnavailable) {
		t.Fatalf("missing audit error = %v, want unavailable", err)
	}
}

func TestRepositoryContractAuditClassificationComesFromEvidence(t *testing.T) {
	f := newRepositoryContractFixture(t)
	ctx := context.Background()
	readAudit := func(t *testing.T, evidence saved.MutationEvidence) (eventID, source, operation, action, capability, outcome, resourceKind, resourceID, aggregateKey, metadataJSON string) {
		t.Helper()
		eventID = savedExplorationAuditEventID("project:sales", evidence)
		err := f.db.QueryRowContext(ctx, `SELECT event_id, source, operation, action, capability, outcome, resource_kind, resource_id, aggregate_key, metadata_json FROM audit_outbox WHERE event_id = ?`, eventID).Scan(&eventID, &source, &operation, &action, &capability, &outcome, &resourceKind, &resourceID, &aggregateKey, &metadataJSON)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	assertAudit := func(t *testing.T, evidence saved.MutationEvidence, lifecycle saved.Lifecycle, operation, action string, capability access.Capability) {
		t.Helper()
		eventID, source, gotOperation, gotAction, gotCapability, outcome, resourceKind, resourceID, aggregateKey, metadataJSON := readAudit(t, evidence)
		if eventID != savedExplorationAuditEventID(lifecycle.ProjectID, evidence) || source != savedExplorationAuditSource || gotOperation != operation || gotAction != action || gotCapability != capability.String() || outcome != "success" {
			t.Fatalf("audit classification = id=%q source=%q operation=%q action=%q capability=%q outcome=%q", eventID, source, gotOperation, gotAction, gotCapability, outcome)
		}
		if resourceKind != "saved_exploration" || resourceID != lifecycle.ID.String() || aggregateKey != "saved_exploration:"+lifecycle.ProjectID.String()+":"+lifecycle.ID.String() {
			t.Fatalf("audit binding = kind=%q id=%q aggregate=%q", resourceKind, resourceID, aggregateKey)
		}
		assertSavedExplorationAuditMetadata(t, metadataJSON, evidence, lifecycle.CurrentRevision.Token(), string(f.payload.Canonical()))
	}

	createEvidence := f.evidence(t, saved.MutationActionCreate, "audit-create", f.now)
	createEvidence.AdminOverride = true
	createEvidence.AdminReason = "approved migration"
	createInput := f.createInput("project:sales", "exploration-audit", "audit", createEvidence)
	created, err := f.repo.Create(saved.WithAuditIntent(ctx, f.wrongIntent()), createInput)
	if err != nil {
		t.Fatal(err)
	}
	assertAudit(t, createEvidence, created.Lifecycle, "createSavedExploration", "saved_exploration.created", access.CapabilityResourceEdit)

	updateEvidence := f.evidence(t, saved.MutationActionUpdate, "audit-update", f.now.Add(time.Minute))
	next, err := saved.NewRevision("revision-audit-2", 2, f.now.Add(time.Minute), "owner", f.payload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := f.repo.UpdateVersion(saved.WithAuditIntent(ctx, f.wrongIntent()), saved.UpdateVersionInput{
		ProjectID: "project:sales", ID: created.Lifecycle.ID, ExpectedRevision: created.AppliedRevision, Revision: next,
		Title: "Audit v2", Slug: "audit-v2", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales",
		UpdatedAt: f.now.Add(time.Minute), Evidence: updateEvidence,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertAudit(t, updateEvidence, updated.Lifecycle, "updateSavedExploration", "saved_exploration.updated", access.CapabilityResourceEdit)

	duplicateEvidence := f.evidence(t, saved.MutationActionDuplicate, "audit-duplicate", f.now.Add(2*time.Minute))
	destinationRevision, err := saved.NewRevision("revision-audit-copy", 1, f.now.Add(2*time.Minute), "owner", f.payload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	duplicated, err := f.repo.Duplicate(saved.WithAuditIntent(ctx, f.wrongIntent()), saved.DuplicateInput{
		ProjectID: "project:sales", SourceID: created.Lifecycle.ID, ExpectedSourceRevision: updated.AppliedRevision,
		Evidence: duplicateEvidence, Destination: saved.CreateInput{ProjectID: "project:sales", ID: "exploration-audit-copy", OwnerPrincipalID: "owner", Title: "Audit Copy", Slug: "audit-copy", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: f.now.Add(2 * time.Minute), Revision: destinationRevision},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertAudit(t, duplicateEvidence, duplicated.Lifecycle, "duplicateSavedExploration", "saved_exploration.duplicated", access.CapabilityResourceEdit)

	archiveEvidence := f.evidence(t, saved.MutationActionArchive, "audit-archive", f.now.Add(3*time.Minute))
	archived, err := f.repo.Archive(saved.WithAuditIntent(ctx, f.wrongIntent()), saved.ArchiveInput{
		ProjectID: "project:sales", ID: created.Lifecycle.ID, ExpectedRevision: updated.AppliedRevision,
		ArchivedAt: f.now.Add(3 * time.Minute), Evidence: archiveEvidence,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertAudit(t, archiveEvidence, archived.Lifecycle, "archiveSavedExploration", "saved_exploration.archived", access.CapabilityResourceManage)
}

func TestRepositoryContractConcurrentIdempotentCreate(t *testing.T) {
	f := newRepositoryContractFixture(t)
	evidence := f.evidence(t, saved.MutationActionCreate, "concurrent", f.now)
	input := f.createInput("project:sales", "exploration-concurrent", "concurrent", evidence)
	ctx := saved.WithAuditIntent(context.Background(), f.intent(saved.MutationActionCreate, "concurrent"))
	results := make([]saved.MutationResult, 2)
	errs := make([]error, 2)
	var wait sync.WaitGroup
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index], errs[index] = f.repo.Create(ctx, input)
		}(index)
	}
	wait.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("concurrent create %d error = %v", index, err)
		}
	}
	if results[0].Replayed == results[1].Replayed {
		t.Fatalf("concurrent replay flags = %t/%t, want one replay", results[0].Replayed, results[1].Replayed)
	}
	if got := f.counts(t, "project:sales"); got.lifecycle != 1 || got.revisions != 1 || got.operations != 1 || got.outbox != 1 {
		t.Fatalf("concurrent durable counts = %#v", got)
	}
}

func TestRepositoryContractConcurrentUpdateCAS(t *testing.T) {
	f := newRepositoryContractFixture(t)
	_, created := f.create(t, "project:sales", "exploration-concurrent-update", "concurrent-update", "concurrent-update-create")
	before := f.counts(t, "project:sales")

	leftPayload, err := saved.NewExplorationSpecPayload(canonical.ExplorationSpec{
		SchemaVersion: 1,
		ModelID:       "semantic:sales",
		Dimensions:    []canonical.ExplorationDimensionRef{{Field: "orders.status"}},
		Metrics:       []canonical.ExplorationMetricRef{{Field: "order_count"}},
		Filters:       []canonical.ExplorationFilter{}, Sort: []canonical.ExplorationSort{}, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	rightPayload, err := saved.NewExplorationSpecPayload(canonical.ExplorationSpec{
		SchemaVersion: 1,
		ModelID:       "semantic:sales",
		Dimensions:    []canonical.ExplorationDimensionRef{{Field: "orders.region"}},
		Metrics:       []canonical.ExplorationMetricRef{{Field: "order_count"}},
		Filters:       []canonical.ExplorationFilter{}, Sort: []canonical.ExplorationSort{}, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	leftRevision, err := saved.NewRevision("revision-concurrent-left", 2, f.now.Add(time.Minute), "owner", leftPayload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	rightRevision, err := saved.NewRevision("revision-concurrent-right", 2, f.now.Add(2*time.Minute), "owner", rightPayload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	if leftRevision.Token() == rightRevision.Token() || leftRevision.Payload.ContentHash() == rightRevision.Payload.ContentHash() {
		t.Fatal("concurrent update candidates must have different valid revision identities and payloads")
	}
	leftEvidence := f.evidence(t, saved.MutationActionUpdate, "concurrent-update-left", f.now.Add(time.Minute))
	rightEvidence := f.evidence(t, saved.MutationActionUpdate, "concurrent-update-right", f.now.Add(2*time.Minute))
	leftInput := saved.UpdateVersionInput{
		ProjectID: "project:sales", ID: created.Lifecycle.ID, ExpectedRevision: created.AppliedRevision, Revision: leftRevision,
		Title: "Orders left", Slug: "orders-left", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales",
		UpdatedAt: f.now.Add(time.Minute), Evidence: leftEvidence,
	}
	rightInput := saved.UpdateVersionInput{
		ProjectID: "project:sales", ID: created.Lifecycle.ID, ExpectedRevision: created.AppliedRevision, Revision: rightRevision,
		Title: "Orders right", Slug: "orders-right", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales",
		UpdatedAt: f.now.Add(2 * time.Minute), Evidence: rightEvidence,
	}

	// Separate handles are required to let both deferred transactions observe
	// the same current revision before SQLite serializes their writes.
	secondStore, err := platform.Open(context.Background(), f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondStore.Close() })
	secondDB := secondStore.SQLDB()
	secondRepo := NewRepositoryWithAudit(secondDB, accesssqlite.NewRepository(secondDB))

	type attempt struct {
		result saved.MutationResult
		err    error
	}
	attempts := make([]attempt, 2)
	ready := make(chan struct{}, len(attempts))
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(len(attempts))
	go func() {
		defer wait.Done()
		ready <- struct{}{}
		<-start
		attempts[0].result, attempts[0].err = f.repo.UpdateVersion(saved.WithAuditIntent(context.Background(), f.intent(saved.MutationActionUpdate, "concurrent-update-left")), leftInput)
	}()
	go func() {
		defer wait.Done()
		ready <- struct{}{}
		<-start
		attempts[1].result, attempts[1].err = secondRepo.UpdateVersion(saved.WithAuditIntent(context.Background(), f.intent(saved.MutationActionUpdate, "concurrent-update-right")), rightInput)
	}()
	for range attempts {
		<-ready
	}
	close(start)
	wait.Wait()

	successes := 0
	winner := -1
	for index, attempt := range attempts {
		switch {
		case attempt.err == nil:
			if attempt.result.Replayed || attempt.result.Revision == nil {
				t.Fatalf("concurrent update %d unexpectedly replayed or returned no revision: %#v", index, attempt.result)
			}
			successes++
			winner = index
		case errors.Is(attempt.err, saved.ErrStaleRevision), errors.Is(attempt.err, saved.ErrConflict), errors.Is(attempt.err, saved.ErrUnavailable):
			// A loser can observe the committed CAS successor as stale, or
			// fail at the SQLite write boundary while the winner holds the
			// database lock. Both paths must roll back the entire mutation.
		default:
			t.Fatalf("concurrent update %d error = %v, want stale/conflict/unavailable", index, attempt.err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent update successes = %d, want exactly one; attempts = %#v", successes, attempts)
	}

	got := f.counts(t, "project:sales")
	if got.lifecycle != before.lifecycle || got.revisions != before.revisions+1 || got.operations != before.operations+1 || got.outbox != before.outbox+1 {
		t.Fatalf("concurrent update durable counts = %#v, want lifecycle=%d and one successor across revision/operation/audit rows", got, before.lifecycle)
	}
	lifecycle, err := f.repo.GetLifecycle(context.Background(), saved.ReadInput{ProjectID: "project:sales", ID: created.Lifecycle.ID})
	if err != nil {
		t.Fatal(err)
	}
	if lifecycle.CurrentRevision.Token() != attempts[winner].result.AppliedRevision || lifecycle.Title != attempts[winner].result.Lifecycle.Title || lifecycle.Slug != attempts[winner].result.Lifecycle.Slug {
		t.Fatalf("durable lifecycle = %#v, winner result = %#v", lifecycle, attempts[winner].result)
	}
	winningRevision, err := f.repo.GetRevision(context.Background(), saved.RevisionReadInput{ProjectID: "project:sales", ID: created.Lifecycle.ID, Revision: lifecycle.CurrentRevision.Token()})
	if err != nil {
		t.Fatal(err)
	}
	winnerPayload := leftRevision.Payload
	if winner == 1 {
		winnerPayload = rightRevision.Payload
	}
	if string(winningRevision.Payload.Canonical()) != string(winnerPayload.Canonical()) {
		t.Fatalf("durable successor payload does not match winner: got=%s want=%s", winningRevision.Payload.Canonical(), winnerPayload.Canonical())
	}
	winnerEvidence := leftEvidence
	if winner == 1 {
		winnerEvidence = rightEvidence
	}
	var operationRevisionID, operationContentHash string
	var operationRevisionNumber int64
	if err := f.db.QueryRowContext(context.Background(), `SELECT result_revision_id, result_revision_number, result_content_hash FROM saved_exploration_operations WHERE project_id = ? AND actor_id = ? AND operation_kind = ? AND idempotency_key = ?`, "project:sales", winnerEvidence.ActorID, winnerEvidence.Action, winnerEvidence.IdempotencyKey).Scan(&operationRevisionID, &operationRevisionNumber, &operationContentHash); err != nil {
		t.Fatalf("winner operation lookup: %v", err)
	}
	if operationRevisionID != string(attempts[winner].result.AppliedRevision.RevisionID) || operationRevisionNumber != int64(attempts[winner].result.AppliedRevision.Number) || operationContentHash != attempts[winner].result.AppliedRevision.ContentHash {
		t.Fatalf("winner operation revision = %q/%d/%q, want %#v", operationRevisionID, operationRevisionNumber, operationContentHash, attempts[winner].result.AppliedRevision)
	}
	var winnerAuditMetadata string
	if err := f.db.QueryRowContext(context.Background(), `SELECT metadata_json FROM audit_outbox WHERE event_id = ?`, savedExplorationAuditEventID("project:sales", winnerEvidence)).Scan(&winnerAuditMetadata); err != nil {
		t.Fatalf("winner audit intent lookup: %v", err)
	}
	assertSavedExplorationAuditMetadata(t, winnerAuditMetadata, winnerEvidence, attempts[winner].result.AppliedRevision, string(winnerPayload.Canonical()))

	loser := 1 - winner
	loserRevision := leftRevision
	loserEvidence := leftEvidence
	if loser == 1 {
		loserRevision = rightRevision
		loserEvidence = rightEvidence
	}
	assertCount := func(query string, args ...any) int {
		t.Helper()
		var count int
		if err := f.db.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if count := assertCount(`SELECT COUNT(*) FROM saved_exploration_revisions WHERE project_id = ? AND exploration_id = ? AND revision_id = ?`, "project:sales", created.Lifecycle.ID, loserRevision.Metadata.ID); count != 0 {
		t.Fatalf("loser revision rows = %d, want 0", count)
	}
	if count := assertCount(`SELECT COUNT(*) FROM saved_exploration_operations WHERE project_id = ? AND actor_id = ? AND operation_kind = ? AND idempotency_key = ?`, "project:sales", loserEvidence.ActorID, loserEvidence.Action, loserEvidence.IdempotencyKey); count != 0 {
		t.Fatalf("loser operation rows = %d, want 0", count)
	}
	if count := assertCount(`SELECT COUNT(*) FROM audit_outbox WHERE event_id = ?`, savedExplorationAuditEventID("project:sales", loserEvidence)); count != 0 {
		t.Fatalf("loser audit intent rows = %d, want 0", count)
	}
}

func assertSavedExplorationAuditMetadata(t *testing.T, encoded string, evidence saved.MutationEvidence, revision saved.RevisionToken, canonicalSpec string) {
	t.Helper()
	var envelope struct {
		SchemaVersion int    `json:"schemaVersion"`
		Retention     string `json:"retention"`
		PayloadSchema string `json:"payloadSchema"`
		Payload       struct {
			MutationEvidenceVersion uint32               `json:"mutationEvidenceVersion"`
			ActorID                 string               `json:"actorId"`
			Action                  saved.MutationAction `json:"action"`
			IdempotencyKey          string               `json:"idempotencyKey"`
			Fingerprint             string               `json:"fingerprint"`
			RequestID               string               `json:"requestId"`
			CorrelationID           string               `json:"correlationId"`
			AdminOverride           bool                 `json:"adminOverride"`
			AdminReason             string               `json:"adminReason"`
			AppliedRevision         saved.RevisionToken  `json:"appliedRevision"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(encoded), &envelope); err != nil {
		t.Fatalf("decode saved exploration audit metadata: %v", err)
	}
	if envelope.SchemaVersion != 1 || envelope.Retention != "security" || envelope.PayloadSchema != "SavedExplorationMutationAuditPayload" {
		t.Fatalf("saved exploration audit envelope = %#v", envelope)
	}
	payload := envelope.Payload
	if payload.MutationEvidenceVersion != evidence.Version || payload.ActorID != evidence.ActorID || payload.Action != evidence.Action || payload.IdempotencyKey != evidence.IdempotencyKey || payload.Fingerprint != evidence.Fingerprint || payload.RequestID != evidence.RequestID || payload.CorrelationID != evidence.CorrelationID || payload.AdminOverride != evidence.AdminOverride || payload.AdminReason != evidence.AdminReason || payload.AppliedRevision != revision {
		t.Fatalf("saved exploration audit payload = %#v, evidence=%#v revision=%#v", payload, evidence, revision)
	}
	if !evidence.AdminOverride && !strings.Contains(encoded, `"adminReason":""`) {
		t.Fatalf("non-admin audit metadata omitted explicit empty admin reason: %s", encoded)
	}
	if strings.Contains(encoded, "callerSecret") {
		t.Fatalf("caller-supplied audit metadata was retained: %s", encoded)
	}
	if strings.Contains(encoded, canonicalSpec) {
		t.Fatalf("saved exploration audit metadata contains authored spec bytes: %s", encoded)
	}
	for _, forbidden := range []string{"orders.status", "order_count", "semantic:sales"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("saved exploration audit metadata contains authored spec value %q: %s", forbidden, encoded)
		}
	}
}
