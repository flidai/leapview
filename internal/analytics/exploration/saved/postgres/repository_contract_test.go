package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	canonical "github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/analytics/exploration/saved"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type postgresRepositoryRowCounts struct {
	lifecycle, revisions, operations, audit int
}

func (f *repositoryFixture) counts(t *testing.T, project string) postgresRepositoryRowCounts {
	t.Helper()
	var counts postgresRepositoryRowCounts
	queries := []struct {
		query string
		value *int
	}{
		{`SELECT count(*) FROM saved_exploration.saved_explorations WHERE project_id = $1`, &counts.lifecycle},
		{`SELECT count(*) FROM saved_exploration.saved_exploration_revisions WHERE project_id = $1`, &counts.revisions},
		{`SELECT count(*) FROM saved_exploration.saved_exploration_operations WHERE project_id = $1`, &counts.operations},
		{`SELECT count(*) FROM audit.audit_event WHERE scope_id = $1`, &counts.audit},
	}
	for _, query := range queries {
		if err := f.db.QueryRow(t.Context(), query.query, project).Scan(query.value); err != nil {
			t.Fatal(err)
		}
	}
	return counts
}

func (f *repositoryFixture) createProject(t *testing.T, project, id, slug, key string) (saved.CreateInput, saved.MutationResult) {
	t.Helper()
	evidence := f.evidence(t, saved.MutationActionCreate, key, f.now)
	identity := f.identity
	identity.ProjectID = projectgraph.ResourceID(project)
	revision, err := saved.NewRevision(saved.RevisionID("revision-"+id), 1, f.now, testActorID, f.payload, identity)
	if err != nil {
		t.Fatal(err)
	}
	input := saved.CreateInput{ProjectID: projectgraph.ResourceID(project), ID: saved.ExplorationID(id), OwnerPrincipalID: testActorID, Title: "Orders " + id, Slug: slug, Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: f.now, Revision: revision, Evidence: evidence}
	result, err := f.repo.Create(saved.WithAuditIntent(t.Context(), f.intent()), input)
	if err != nil {
		t.Fatal(err)
	}
	return input, result
}

func TestRepositoryPostgreSQLAuditPrincipalIdentityBoundary(t *testing.T) {
	f := newRepositoryFixture(t)
	readIdentity := func(t *testing.T, eventID string) (principalID, actorID pgtype.Text) {
		t.Helper()
		if err := f.db.QueryRow(t.Context(), `SELECT principal_id::text, actor_id FROM audit.audit_event WHERE audit_id = $1::uuid`, eventID).Scan(&principalID, &actorID); err != nil {
			t.Fatal(err)
		}
		return principalID, actorID
	}

	// A typed Access principal remains in PrincipalID and is also retained as
	// the source actor binding.
	uuidEvidence := f.evidence(t, saved.MutationActionCreate, "principal-uuid", f.now)
	uuidInput := f.createInput(t, "exploration-principal-uuid", "principal-uuid", uuidEvidence, f.now)
	uuidResult, err := f.repo.Create(saved.WithAuditIntent(t.Context(), access.AuditIntent{PrincipalID: testActorID}), uuidInput)
	if err != nil {
		t.Fatal(err)
	}
	principalID, actorID := readIdentity(t, savedExplorationAuditEventID(uuidInput.ProjectID, uuidEvidence))
	if !principalID.Valid || principalID.String != testActorID || !actorID.Valid || actorID.String != testActorID {
		t.Fatalf("UUID audit identity = principal=%#v actor=%#v, want both %q", principalID, actorID, testActorID)
	}

	// Actors from another authority are still durable evidence. They belong in
	// ActorID, while PostgreSQL's typed UUID principal column remains NULL.
	const opaqueActor = "external:owner"
	opaqueFingerprint, err := saved.CanonicalFingerprint(struct {
		Action saved.MutationAction `json:"action"`
		Key    string               `json:"key"`
	}{saved.MutationActionCreate, "principal-opaque"})
	if err != nil {
		t.Fatal(err)
	}
	opaqueEvidence, err := saved.NewMutationEvidence(opaqueActor, saved.MutationActionCreate, "principal-opaque", opaqueFingerprint, "request-principal-opaque", "correlation-principal-opaque", f.now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	opaqueRevision, err := saved.NewRevision("revision-exploration-principal-opaque", 1, f.now.Add(time.Minute), opaqueActor, f.payload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	opaqueInput := saved.CreateInput{ProjectID: "project:sales", ID: "exploration-principal-opaque", OwnerPrincipalID: opaqueActor, Title: "Opaque actor", Slug: "principal-opaque", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: f.now.Add(time.Minute), Revision: opaqueRevision, Evidence: opaqueEvidence}
	opaqueResult, err := f.repo.Create(saved.WithAuditIntent(t.Context(), access.AuditIntent{PrincipalID: opaqueActor}), opaqueInput)
	if err != nil {
		t.Fatal(err)
	}
	principalID, actorID = readIdentity(t, savedExplorationAuditEventID(opaqueInput.ProjectID, opaqueEvidence))
	if principalID.Valid || !actorID.Valid || actorID.String != opaqueActor {
		t.Fatalf("opaque audit identity = principal=%#v actor=%#v, want NULL/%q", principalID, actorID, opaqueActor)
	}
	if uuidResult.Lifecycle.ID == opaqueResult.Lifecycle.ID {
		t.Fatal("UUID and opaque actor creates reused an exploration identity")
	}

	// A caller cannot relabel the typed principal. The mismatch is discovered
	// after the lifecycle, revision, and operation writes, so the transaction
	// must roll all four durable tables back together.
	before := f.counts(t, "project:sales")
	mismatchEvidence := f.evidence(t, saved.MutationActionCreate, "principal-mismatch", f.now.Add(2*time.Minute))
	mismatchInput := f.createInput(t, "exploration-principal-mismatch", "principal-mismatch", mismatchEvidence, f.now.Add(2*time.Minute))
	_, err = f.repo.Create(saved.WithAuditIntent(t.Context(), access.AuditIntent{PrincipalID: "01900000-0000-7000-8000-000000000002"}), mismatchInput)
	if !errors.Is(err, saved.ErrInvalid) {
		t.Fatalf("mismatched principal = %v, want invalid", err)
	}
	if got := f.counts(t, "project:sales"); got != before {
		t.Fatalf("durable rows after mismatched principal = %#v, want %#v", got, before)
	}
}

func TestRepositoryPostgreSQLContractProjectIsolationAndExactReads(t *testing.T) {
	f := newRepositoryFixture(t)
	salesInput, sales := f.create(t, "exploration-sales", "sales", "contract-sales")
	_, marketing := f.createProject(t, "project:marketing", "exploration-marketing", "marketing", "contract-marketing")
	if _, err := f.repo.GetLifecycle(t.Context(), saved.ReadInput{ProjectID: "project:other", ID: sales.Lifecycle.ID}); !errors.Is(err, saved.ErrNotFound) {
		t.Fatalf("cross-project lifecycle = %v, want not found", err)
	}
	if _, err := f.repo.GetRevision(t.Context(), saved.RevisionReadInput{ProjectID: "project:other", ID: sales.Lifecycle.ID, Revision: sales.AppliedRevision}); !errors.Is(err, saved.ErrNotFound) {
		t.Fatalf("cross-project revision = %v, want not found", err)
	}
	rows, err := f.repo.List(t.Context(), saved.ListInput{ProjectID: salesInput.ProjectID})
	if err != nil || len(rows) != 1 || rows[0].ID != sales.Lifecycle.ID {
		t.Fatalf("sales list = %#v, %v", rows, err)
	}
	lookup, found, err := f.repo.LookupMutation(t.Context(), saved.MutationLookupInput{ProjectID: "project:other", ActorID: testActorID, Action: saved.MutationActionCreate, IdempotencyKey: "contract-marketing", Fingerprint: "sha256:" + strings.Repeat("a", 64)})
	if err != nil || found || lookup != (saved.MutationReplayMetadata{}) {
		t.Fatalf("cross-project lookup = %#v, found=%t, err=%v", lookup, found, err)
	}

	marketingIdentity, err := projectgraph.NewServingIdentity("project:marketing", "production", "generation-2")
	if err != nil {
		t.Fatal(err)
	}
	next, err := saved.NewRevision("revision-cross-project", 2, f.now.Add(time.Minute), testActorID, f.payload, marketingIdentity)
	if err != nil {
		t.Fatal(err)
	}
	updateEvidence := f.evidence(t, saved.MutationActionUpdate, "contract-cross-update", f.now.Add(time.Minute))
	_, err = f.repo.UpdateVersion(saved.WithAuditIntent(t.Context(), f.intent()), saved.UpdateVersionInput{ProjectID: "project:marketing", ID: sales.Lifecycle.ID, ExpectedRevision: sales.AppliedRevision, Revision: next, Title: "Cross", Slug: "cross", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", UpdatedAt: f.now.Add(time.Minute), Evidence: updateEvidence})
	if !errors.Is(err, saved.ErrNotFound) {
		t.Fatalf("cross-project update = %v, want not found", err)
	}
	destinationRevision, err := saved.NewRevision("revision-cross-copy", 1, f.now.Add(time.Minute), testActorID, f.payload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	duplicateEvidence := f.evidence(t, saved.MutationActionDuplicate, "contract-cross-duplicate", f.now.Add(time.Minute))
	_, err = f.repo.Duplicate(saved.WithAuditIntent(t.Context(), f.intent()), saved.DuplicateInput{ProjectID: salesInput.ProjectID, SourceID: marketing.Lifecycle.ID, ExpectedSourceRevision: marketing.AppliedRevision, Evidence: duplicateEvidence, Destination: saved.CreateInput{ProjectID: salesInput.ProjectID, ID: "exploration-cross-copy", OwnerPrincipalID: testActorID, Title: "Copy", Slug: "cross-copy", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: f.now.Add(time.Minute), Revision: destinationRevision}})
	if !errors.Is(err, saved.ErrNotFound) {
		t.Fatalf("cross-project duplicate = %v, want not found", err)
	}
}

func TestRepositoryPostgreSQLContractListPageUsesProjectScopedKeyset(t *testing.T) {
	f := newRepositoryFixture(t)
	f.create(t, "exploration-a", "sales-a", "contract-list-a")
	f.create(t, "exploration-b", "sales-b", "contract-list-b")
	f.create(t, "exploration-c", "sales-c", "contract-list-c")
	f.createProject(t, "project:marketing", "exploration-z", "marketing-z", "contract-list-z")
	first, err := f.repo.ListPage(t.Context(), saved.ListInput{ProjectID: "project:sales", Limit: 1})
	if err != nil || len(first.Items) != 1 || first.Items[0].ID != "exploration-a" || first.NextCursor != "exploration-a" {
		t.Fatalf("first page = %#v, %v", first, err)
	}
	second, err := f.repo.ListPage(t.Context(), saved.ListInput{ProjectID: "project:sales", Cursor: first.NextCursor, Limit: 1})
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != "exploration-b" || second.NextCursor != "exploration-b" {
		t.Fatalf("second page = %#v, %v", second, err)
	}
	marketing, err := f.repo.ListPage(t.Context(), saved.ListInput{ProjectID: "project:marketing", Cursor: first.NextCursor, Limit: 1})
	if err != nil || len(marketing.Items) != 1 || marketing.Items[0].ID != "exploration-z" || marketing.NextCursor != "" {
		t.Fatalf("cross-project keyset page = %#v, %v", marketing, err)
	}
	last, err := f.repo.ListPage(t.Context(), saved.ListInput{ProjectID: "project:sales", Cursor: second.NextCursor, Limit: 1})
	if err != nil || len(last.Items) != 1 || last.Items[0].ID != "exploration-c" || last.NextCursor != "" {
		t.Fatalf("final sales page = %#v, %v", last, err)
	}
}

func TestRepositoryPostgreSQLContractListOrdersByImmutableExplorationID(t *testing.T) {
	f := newRepositoryFixture(t)
	f.create(t, "exploration-z", "z-order", "contract-order-z")
	f.create(t, "exploration-a", "a-order", "contract-order-a")
	items, err := f.repo.List(t.Context(), saved.ListInput{ProjectID: "project:sales"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != "exploration-a" || items[1].ID != "exploration-z" {
		t.Fatalf("list IDs = %#v, want immutable ID order", items)
	}
}

func TestRepositoryPostgreSQLContractStaleCASLeavesDurableRowsUnchanged(t *testing.T) {
	f := newRepositoryFixture(t)
	_, created := f.create(t, "exploration-stale", "stale", "contract-stale-create")
	before := f.counts(t, "project:sales")
	stale := created.AppliedRevision
	stale.RevisionID = "revision-stale"
	next, err := saved.NewRevision("revision-stale-next", 2, f.now.Add(time.Minute), testActorID, f.payload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	updateEvidence := f.evidence(t, saved.MutationActionUpdate, "contract-stale-update", f.now.Add(time.Minute))
	_, err = f.repo.UpdateVersion(saved.WithAuditIntent(t.Context(), f.intent()), saved.UpdateVersionInput{ProjectID: "project:sales", ID: created.Lifecycle.ID, ExpectedRevision: stale, Revision: next, Title: "Stale", Slug: "stale-v2", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", UpdatedAt: f.now.Add(time.Minute), Evidence: updateEvidence})
	if !errors.Is(err, saved.ErrStaleRevision) {
		t.Fatalf("stale update = %v, want stale revision", err)
	}
	archiveEvidence := f.evidence(t, saved.MutationActionArchive, "contract-stale-archive", f.now.Add(time.Minute))
	_, err = f.repo.Archive(saved.WithAuditIntent(t.Context(), f.intent()), saved.ArchiveInput{ProjectID: "project:sales", ID: created.Lifecycle.ID, ExpectedRevision: stale, ArchivedAt: f.now.Add(time.Minute), Evidence: archiveEvidence})
	if !errors.Is(err, saved.ErrStaleRevision) {
		t.Fatalf("stale archive = %v, want stale revision", err)
	}
	if got := f.counts(t, "project:sales"); got != before {
		t.Fatalf("rows after stale operations = %#v, want %#v", got, before)
	}
}

func TestRepositoryPostgreSQLContractExactReplaySnapshotsAndArchive(t *testing.T) {
	f := newRepositoryFixture(t)
	createEvidence := f.evidence(t, saved.MutationActionCreate, "contract-replay-create", f.now)
	createEvidence.AdminOverride = true
	createEvidence.AdminReason = "approved migration"
	if err := createEvidence.Validate(); err != nil {
		t.Fatal(err)
	}
	createInput := f.createInput(t, "exploration-replay", "replay", createEvidence, f.now)
	created, err := f.repo.Create(saved.WithAuditIntent(t.Context(), f.intent()), createInput)
	if err != nil {
		t.Fatal(err)
	}
	next, err := saved.NewRevision("revision-replay-2", 2, f.now.Add(time.Minute), testActorID, f.payload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	updateEvidence := f.evidence(t, saved.MutationActionUpdate, "contract-replay-update", f.now.Add(time.Minute))
	updateInput := saved.UpdateVersionInput{ProjectID: "project:sales", ID: created.Lifecycle.ID, ExpectedRevision: created.AppliedRevision, Revision: next, Title: "Replay v2", Slug: "replay-v2", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", UpdatedAt: f.now.Add(time.Minute), Evidence: updateEvidence}
	updated, err := f.repo.UpdateVersion(saved.WithAuditIntent(t.Context(), f.intent()), updateInput)
	if err != nil {
		t.Fatal(err)
	}
	archiveEvidence := f.evidence(t, saved.MutationActionArchive, "contract-replay-archive", f.now.Add(2*time.Minute))
	archiveInput := saved.ArchiveInput{ProjectID: "project:sales", ID: created.Lifecycle.ID, ExpectedRevision: updated.AppliedRevision, ArchivedAt: f.now.Add(2 * time.Minute), Evidence: archiveEvidence}
	archived, err := f.repo.Archive(saved.WithAuditIntent(t.Context(), f.intent()), archiveInput)
	if err != nil {
		t.Fatal(err)
	}
	active, err := f.repo.List(t.Context(), saved.ListInput{ProjectID: "project:sales"})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("default list after archive = %#v, want archived rows excluded", active)
	}
	archivedList, err := f.repo.List(t.Context(), saved.ListInput{ProjectID: "project:sales", IncludeArchived: true})
	if err != nil || len(archivedList) != 1 || archivedList[0].Status != saved.StatusArchived {
		t.Fatalf("include-archived list = %#v, %v", archivedList, err)
	}
	before := f.counts(t, "project:sales")
	createReplay, err := f.repo.Create(context.Background(), createInput)
	if err != nil || !createReplay.Replayed || createReplay.Lifecycle.Title != created.Lifecycle.Title || createReplay.Revision == nil || createReplay.Revision.Metadata.Number != 1 || !createReplay.Evidence.AdminOverride || createReplay.Evidence.AdminReason != createEvidence.AdminReason {
		t.Fatalf("create replay = %#v, %v", createReplay, err)
	}
	updateReplay, err := f.repo.UpdateVersion(context.Background(), updateInput)
	if err != nil || !updateReplay.Replayed || updateReplay.Lifecycle.Title != updated.Lifecycle.Title || updateReplay.Revision == nil || updateReplay.AppliedRevision != updated.AppliedRevision || updateReplay.ConcurrencyRevision != updateInput.ExpectedRevision {
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

func TestRepositoryPostgreSQLContractSameKeyDuplicateBytesAndAuditFailure(t *testing.T) {
	f := newRepositoryFixture(t)
	_, created := f.create(t, "exploration-duplicate-source", "duplicate-source", "contract-duplicate-create")
	before := f.counts(t, "project:sales")
	changedEvidence := f.evidence(t, saved.MutationActionCreate, "contract-duplicate-create", f.now)
	changedEvidence.Fingerprint = "sha256:" + strings.Repeat("a", 64)
	changedInput := f.createInput(t, "exploration-duplicate-source", "changed", changedEvidence, f.now)
	changedInput.Title = "Changed"
	if _, err := f.repo.Create(context.Background(), changedInput); !errors.Is(err, saved.ErrConflict) {
		t.Fatalf("same-key fingerprint conflict = %v", err)
	}
	if got := f.counts(t, "project:sales"); got != before {
		t.Fatalf("rows after same-key conflict = %#v, want %#v", got, before)
	}

	destinationRevision, err := saved.NewRevision("revision-duplicate-copy", 1, f.now.Add(time.Minute), testActorID, f.payload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	duplicateEvidence := f.evidence(t, saved.MutationActionDuplicate, "contract-duplicate", f.now.Add(time.Minute))
	duplicate, err := f.repo.Duplicate(saved.WithAuditIntent(t.Context(), f.intent()), saved.DuplicateInput{ProjectID: "project:sales", SourceID: created.Lifecycle.ID, ExpectedSourceRevision: created.AppliedRevision, Evidence: duplicateEvidence, Destination: saved.CreateInput{ProjectID: "project:sales", ID: "exploration-duplicate-copy", OwnerPrincipalID: testActorID, Title: "Copy", Slug: "duplicate-copy", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: f.now.Add(time.Minute), Revision: destinationRevision}})
	if err != nil {
		t.Fatal(err)
	}
	sourceRevision, err := f.repo.GetRevision(t.Context(), saved.RevisionReadInput{ProjectID: "project:sales", ID: created.Lifecycle.ID, Revision: created.AppliedRevision})
	if err != nil {
		t.Fatal(err)
	}
	destination, err := f.repo.GetRevision(t.Context(), saved.RevisionReadInput{ProjectID: "project:sales", ID: duplicate.Lifecycle.ID, Revision: duplicate.AppliedRevision})
	if err != nil {
		t.Fatal(err)
	}
	if string(sourceRevision.Payload.Canonical()) != string(destination.Payload.Canonical()) || sourceRevision.Metadata.ContentHash != destination.Metadata.ContentHash {
		t.Fatal("duplicate did not preserve exact source bytes and content hash")
	}

	failedRevision, err := saved.NewRevision("revision-duplicate-failed", 2, f.now.Add(2*time.Minute), testActorID, f.payload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	failedEvidence := f.evidence(t, saved.MutationActionUpdate, "contract-duplicate-failed", f.now.Add(2*time.Minute))
	injected := errors.New("injected audit failure")
	failing := New(f.db, failingAuditAdapter{err: injected})
	_, err = failing.UpdateVersion(saved.WithAuditIntent(t.Context(), f.intent()), saved.UpdateVersionInput{ProjectID: "project:sales", ID: created.Lifecycle.ID, ExpectedRevision: created.AppliedRevision, Revision: failedRevision, Title: "Failed", Slug: "failed", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", UpdatedAt: f.now.Add(2 * time.Minute), Evidence: failedEvidence})
	if !errors.Is(err, saved.ErrUnavailable) || !errors.Is(err, injected) {
		t.Fatalf("audit failure = %v", err)
	}
	if got := f.counts(t, "project:sales"); got.lifecycle != 2 || got.revisions != 2 || got.operations != 2 || got.audit != 2 {
		t.Fatalf("rows after audit failure = %#v", got)
	}

	missingEvidence := f.evidence(t, saved.MutationActionCreate, "contract-missing-audit", f.now.Add(2*time.Minute))
	missingRevision, err := saved.NewRevision("revision-duplicate-missing", 1, f.now.Add(2*time.Minute), testActorID, f.payload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(f.db, nil).Create(context.Background(), saved.CreateInput{ProjectID: "project:sales", ID: "exploration-missing-audit-contract", OwnerPrincipalID: testActorID, Title: "Missing", Slug: "missing-contract", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: f.now.Add(2 * time.Minute), Revision: missingRevision, Evidence: missingEvidence}); !errors.Is(err, saved.ErrUnavailable) {
		t.Fatalf("missing audit = %v", err)
	}
}

func TestRepositoryPostgreSQLContractAuditClassificationComesFromEvidence(t *testing.T) {
	f := newRepositoryFixture(t)
	wrong := func() access.AuditIntent {
		return access.AuditIntent{EventID: "caller-event", Source: "caller-source", Operation: "caller-operation", Action: "caller.action", Capability: access.CapabilityResourceRead, Outcome: "caller-outcome", RequestID: "caller-request", CorrelationID: "caller-correlation", AggregateKey: "caller-aggregate", MetadataJSON: `{"callerSecret":"must-not-persist"}`}
	}
	assertAudit := func(t *testing.T, evidence saved.MutationEvidence, lifecycle saved.Lifecycle, operation, action string, capability access.Capability) {
		t.Helper()
		eventID := savedExplorationAuditEventID(lifecycle.ProjectID, evidence)
		var auditID, scope, actor, digest, source, gotOperation, gotAction, gotCapability, outcome, resourceKind, resourceID, aggregateKey, metadata string
		if err := f.db.QueryRow(t.Context(), `SELECT audit_id::text, scope_id, actor_id, request_digest, source, operation, action, capability, outcome, resource_kind, resource_id, aggregate_key, metadata::text FROM audit.audit_event WHERE audit_id = $1::uuid`, eventID).Scan(&auditID, &scope, &actor, &digest, &source, &gotOperation, &gotAction, &gotCapability, &outcome, &resourceKind, &resourceID, &aggregateKey, &metadata); err != nil {
			t.Fatal(err)
		}
		if auditID != eventID || scope != lifecycle.ProjectID.String() || actor != evidence.ActorID || digest != evidence.Fingerprint || source != savedExplorationAuditSource || gotOperation != operation || gotAction != action || gotCapability != capability.String() || outcome != "success" {
			t.Fatalf("audit classification/binding = id=%q scope=%q actor=%q digest=%q source=%q operation=%q action=%q capability=%q outcome=%q", auditID, scope, actor, digest, source, gotOperation, gotAction, gotCapability, outcome)
		}
		if resourceKind != "saved_exploration" || resourceID != lifecycle.ID.String() || aggregateKey != "saved_exploration:"+lifecycle.ProjectID.String()+":"+lifecycle.ID.String() {
			t.Fatalf("audit resource binding = %q/%q/%q", resourceKind, resourceID, aggregateKey)
		}
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
		if err := json.Unmarshal([]byte(metadata), &envelope); err != nil {
			t.Fatal(err)
		}
		payload := envelope.Payload
		if envelope.SchemaVersion != 1 || envelope.Retention != "security" || envelope.PayloadSchema != "SavedExplorationMutationAuditPayload" || payload.MutationEvidenceVersion != evidence.Version || payload.ActorID != evidence.ActorID || payload.Action != evidence.Action || payload.IdempotencyKey != evidence.IdempotencyKey || payload.Fingerprint != evidence.Fingerprint || payload.RequestID != evidence.RequestID || payload.CorrelationID != evidence.CorrelationID || payload.AdminOverride != evidence.AdminOverride || payload.AdminReason != evidence.AdminReason || payload.AppliedRevision != lifecycle.CurrentRevision.Token() {
			t.Fatalf("audit evidence envelope = %#v", envelope)
		}
		if strings.Contains(metadata, "callerSecret") || strings.Contains(metadata, string(f.payload.Canonical())) || strings.Contains(metadata, "orders.status") || strings.Contains(metadata, "order_count") || strings.Contains(metadata, "semantic:sales") {
			t.Fatalf("caller/authored payload leaked into audit metadata: %s", metadata)
		}
	}

	createEvidence := f.evidence(t, saved.MutationActionCreate, "contract-audit-create", f.now)
	createEvidence.AdminOverride = true
	createEvidence.AdminReason = "approved migration"
	createInput := f.createInput(t, "exploration-audit", "audit", createEvidence, f.now)
	created, err := f.repo.Create(saved.WithAuditIntent(t.Context(), wrong()), createInput)
	if err != nil {
		t.Fatal(err)
	}
	assertAudit(t, createEvidence, created.Lifecycle, "createSavedExploration", "saved_exploration.created", access.CapabilityResourceEdit)

	updateEvidence := f.evidence(t, saved.MutationActionUpdate, "contract-audit-update", f.now.Add(time.Minute))
	next, err := saved.NewRevision("revision-audit-contract-2", 2, f.now.Add(time.Minute), testActorID, f.payload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := f.repo.UpdateVersion(saved.WithAuditIntent(t.Context(), wrong()), saved.UpdateVersionInput{ProjectID: "project:sales", ID: created.Lifecycle.ID, ExpectedRevision: created.AppliedRevision, Revision: next, Title: "Audit v2", Slug: "audit-v2", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", UpdatedAt: f.now.Add(time.Minute), Evidence: updateEvidence})
	if err != nil {
		t.Fatal(err)
	}
	assertAudit(t, updateEvidence, updated.Lifecycle, "updateSavedExploration", "saved_exploration.updated", access.CapabilityResourceEdit)

	duplicateEvidence := f.evidence(t, saved.MutationActionDuplicate, "contract-audit-duplicate", f.now.Add(2*time.Minute))
	destinationRevision, err := saved.NewRevision("revision-audit-contract-copy", 1, f.now.Add(2*time.Minute), testActorID, f.payload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	duplicated, err := f.repo.Duplicate(saved.WithAuditIntent(t.Context(), wrong()), saved.DuplicateInput{ProjectID: "project:sales", SourceID: created.Lifecycle.ID, ExpectedSourceRevision: updated.AppliedRevision, Evidence: duplicateEvidence, Destination: saved.CreateInput{ProjectID: "project:sales", ID: "exploration-audit-copy", OwnerPrincipalID: testActorID, Title: "Audit Copy", Slug: "audit-copy", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: f.now.Add(2 * time.Minute), Revision: destinationRevision}})
	if err != nil {
		t.Fatal(err)
	}
	assertAudit(t, duplicateEvidence, duplicated.Lifecycle, "duplicateSavedExploration", "saved_exploration.duplicated", access.CapabilityResourceEdit)

	archiveEvidence := f.evidence(t, saved.MutationActionArchive, "contract-audit-archive", f.now.Add(3*time.Minute))
	archived, err := f.repo.Archive(saved.WithAuditIntent(t.Context(), wrong()), saved.ArchiveInput{ProjectID: "project:sales", ID: created.Lifecycle.ID, ExpectedRevision: updated.AppliedRevision, ArchivedAt: f.now.Add(3 * time.Minute), Evidence: archiveEvidence})
	if err != nil {
		t.Fatal(err)
	}
	assertAudit(t, archiveEvidence, archived.Lifecycle, "archiveSavedExploration", "saved_exploration.archived", access.CapabilityResourceManage)
}

func TestRepositoryPostgreSQLContractConcurrentIdempotentCreate(t *testing.T) {
	f := newRepositoryFixture(t)
	evidence := f.evidence(t, saved.MutationActionCreate, "contract-concurrent-create", f.now)
	input := f.createInput(t, "exploration-concurrent", "concurrent", evidence, f.now)
	ctx := saved.WithAuditIntent(t.Context(), f.intent())
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
			t.Fatalf("concurrent create %d = %v", index, err)
		}
	}
	if results[0].Replayed == results[1].Replayed {
		t.Fatalf("replay flags = %t/%t, want one replay", results[0].Replayed, results[1].Replayed)
	}
	if got := f.counts(t, "project:sales"); got.lifecycle != 1 || got.revisions != 1 || got.operations != 1 || got.audit != 1 {
		t.Fatalf("concurrent durable counts = %#v", got)
	}
}

func TestRepositoryPostgreSQLContractConcurrentUpdateCAS(t *testing.T) {
	f := newRepositoryFixture(t)
	_, created := f.create(t, "exploration-concurrent-update", "concurrent-update", "contract-concurrent-update-create")
	before := f.counts(t, "project:sales")
	leftPayload, err := saved.NewExplorationSpecPayload(canonical.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", Dimensions: []canonical.ExplorationDimensionRef{{Field: "orders.status"}}, Metrics: []canonical.ExplorationMetricRef{{Field: "order_count"}}, Filters: []canonical.ExplorationFilter{}, Sort: []canonical.ExplorationSort{}, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	rightPayload, err := saved.NewExplorationSpecPayload(canonical.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", Dimensions: []canonical.ExplorationDimensionRef{{Field: "orders.region"}}, Metrics: []canonical.ExplorationMetricRef{{Field: "order_count"}}, Filters: []canonical.ExplorationFilter{}, Sort: []canonical.ExplorationSort{}, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	leftRevision, err := saved.NewRevision("revision-concurrent-left", 2, f.now.Add(time.Minute), testActorID, leftPayload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	rightRevision, err := saved.NewRevision("revision-concurrent-right", 2, f.now.Add(2*time.Minute), testActorID, rightPayload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	leftEvidence := f.evidence(t, saved.MutationActionUpdate, "contract-concurrent-left", f.now.Add(time.Minute))
	rightEvidence := f.evidence(t, saved.MutationActionUpdate, "contract-concurrent-right", f.now.Add(2*time.Minute))
	leftInput := saved.UpdateVersionInput{ProjectID: "project:sales", ID: created.Lifecycle.ID, ExpectedRevision: created.AppliedRevision, Revision: leftRevision, Title: "Left", Slug: "left", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", UpdatedAt: f.now.Add(time.Minute), Evidence: leftEvidence}
	rightInput := saved.UpdateVersionInput{ProjectID: "project:sales", ID: created.Lifecycle.ID, ExpectedRevision: created.AppliedRevision, Revision: rightRevision, Title: "Right", Slug: "right", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", UpdatedAt: f.now.Add(2 * time.Minute), Evidence: rightEvidence}
	type attempt struct {
		result saved.MutationResult
		err    error
	}
	attempts := make([]attempt, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	start := make(chan struct{})
	go func() {
		defer wait.Done()
		<-start
		attempts[0].result, attempts[0].err = f.repo.UpdateVersion(saved.WithAuditIntent(t.Context(), f.intent()), leftInput)
	}()
	go func() {
		defer wait.Done()
		<-start
		attempts[1].result, attempts[1].err = f.repo.UpdateVersion(saved.WithAuditIntent(t.Context(), f.intent()), rightInput)
	}()
	// Hold the lifecycle row before starting both mutations. Both statements
	// then establish their READ COMMITTED snapshots before the barrier is
	// released and wait on the same row lock. The first waiter commits the
	// winning revision; the second waiter must classify the newly visible
	// lifecycle as a stale CAS rather than losing the joined revision row from
	// its pre-wait snapshot.
	barrier, err := f.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(t.Context())
	var locked int
	if err := barrier.QueryRow(t.Context(), `
		SELECT 1
		  FROM saved_exploration.saved_explorations
		 WHERE project_id = $1 AND exploration_id = $2
		 FOR UPDATE`, created.Lifecycle.ProjectID.String(), created.Lifecycle.ID.String()).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	close(start)
	if err := waitForConcurrentLifecycleLocks(t.Context(), f.db, 2); err != nil {
		t.Fatalf("concurrent update lock barrier: %v", err)
	}
	if err := barrier.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	wait.Wait()
	winner := -1
	for index, attempt := range attempts {
		if attempt.err == nil {
			if attempt.result.Replayed || attempt.result.Revision == nil {
				t.Fatalf("concurrent update %d replay/no revision: %#v", index, attempt.result)
			}
			if winner != -1 {
				t.Fatal("both concurrent CAS updates succeeded")
			}
			winner = index
			continue
		}
		if !errors.Is(attempt.err, saved.ErrStaleRevision) {
			t.Fatalf("concurrent update %d = %v, want stale revision after the winning transaction", index, attempt.err)
		}
	}
	if winner < 0 {
		t.Fatalf("no concurrent update winner: %#v", attempts)
	}
	if got := f.counts(t, "project:sales"); got.lifecycle != before.lifecycle || got.revisions != before.revisions+1 || got.operations != before.operations+1 || got.audit != before.audit+1 {
		t.Fatalf("concurrent durable counts = %#v, before=%#v", got, before)
	}
	lifecycle, err := f.repo.GetLifecycle(t.Context(), saved.ReadInput{ProjectID: "project:sales", ID: created.Lifecycle.ID})
	if err != nil {
		t.Fatal(err)
	}
	if lifecycle.CurrentRevision.Token() != attempts[winner].result.AppliedRevision || lifecycle.Title != attempts[winner].result.Lifecycle.Title {
		t.Fatalf("durable lifecycle = %#v, winner = %#v", lifecycle, attempts[winner].result)
	}
	loser := 1 - winner
	loserInput := leftInput
	if loser == 1 {
		loserInput = rightInput
	}
	if _, err := f.repo.GetRevision(t.Context(), saved.RevisionReadInput{ProjectID: "project:sales", ID: created.Lifecycle.ID, Revision: loserInput.Revision.Token()}); !errors.Is(err, saved.ErrNotFound) {
		t.Fatalf("loser revision read = %v, want not found", err)
	}
	winnerEvidence := leftEvidence
	loserEvidence := rightEvidence
	if winner == 1 {
		winnerEvidence, loserEvidence = rightEvidence, leftEvidence
	}
	if _, found, err := f.repo.LookupMutation(t.Context(), saved.MutationLookupInput{ProjectID: "project:sales", ActorID: winnerEvidence.ActorID, Action: winnerEvidence.Action, IdempotencyKey: winnerEvidence.IdempotencyKey, Fingerprint: winnerEvidence.Fingerprint}); err != nil || !found {
		t.Fatalf("winner operation lookup = found:%t err:%v", found, err)
	}
	if _, found, err := f.repo.LookupMutation(t.Context(), saved.MutationLookupInput{ProjectID: "project:sales", ActorID: loserEvidence.ActorID, Action: loserEvidence.Action, IdempotencyKey: loserEvidence.IdempotencyKey, Fingerprint: loserEvidence.Fingerprint}); err != nil || found {
		t.Fatalf("loser operation lookup = found:%t err:%v", found, err)
	}
}

func waitForConcurrentLifecycleLocks(ctx context.Context, db *pgxpool.Pool, expected int) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked int
		if err := db.QueryRow(ctx, `
			SELECT count(*)
			  FROM pg_stat_activity
			 WHERE pid <> pg_backend_pid()
			   AND datname = current_database()
			   AND wait_event_type = 'Lock'
			   AND query LIKE '%saved_exploration.saved_explorations%'`).Scan(&blocked); err != nil {
			return err
		}
		if blocked >= expected {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
