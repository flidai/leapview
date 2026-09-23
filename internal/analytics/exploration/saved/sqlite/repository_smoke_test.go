package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	canonical "github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/analytics/exploration/saved"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestRepositorySmoke(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, t.TempDir()+"/platform.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	db := store.SQLDB()
	if _, err := db.ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES ('owner', 'owner@example.test', 'Owner')`); err != nil {
		t.Fatal(err)
	}
	auditStore := accesssqlite.NewRepository(db)
	repo := NewRepositoryWithAudit(db, auditStore)
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	payload, err := saved.NewExplorationSpecPayload(canonical.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", Dimensions: []canonical.ExplorationDimensionRef{{Field: "orders.status"}}, Metrics: []canonical.ExplorationMetricRef{{Field: "order_count"}}, Filters: []canonical.ExplorationFilter{}, Sort: []canonical.ExplorationSort{}, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := newIdentity("project:sales")
	if err != nil {
		t.Fatal(err)
	}
	revision, err := saved.NewRevision("revision-1", 1, now, "owner", payload, identity)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := func(action saved.MutationAction) string {
		value, _ := saved.CanonicalFingerprint(struct{ A saved.MutationAction }{action})
		return value
	}
	evidence, err := saved.NewMutationEvidence("owner", saved.MutationActionCreate, "create-1", fingerprint(saved.MutationActionCreate), "request-1", "correlation-1", now)
	if err != nil {
		t.Fatal(err)
	}
	intent := access.AuditIntent{EventID: "saved-exploration:create-1", Source: "analytics.exploration.saved", Operation: "createSavedExploration", Action: "saved_exploration.created", Capability: access.CapabilityResourceEdit, Outcome: "success", MetadataJSON: `{}`}
	create := saved.CreateInput{ProjectID: "project:sales", ID: "exploration-1", OwnerPrincipalID: "owner", Title: "Orders", Slug: "orders", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: now, Revision: revision, Evidence: evidence}
	result, err := repo.Create(saved.WithAuditIntent(ctx, intent), create)
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision == nil || result.Lifecycle.CurrentRevision.Token() != revision.Token() {
		t.Fatalf("create = %#v", result)
	}
	if _, err := repo.GetLifecycle(ctx, saved.ReadInput{ProjectID: "project:sales", ID: "exploration-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetRevision(ctx, saved.RevisionReadInput{ProjectID: "project:sales", ID: "exploration-1", Revision: revision.Token()}); err != nil {
		t.Fatal(err)
	}
	if replay, err := repo.Create(ctx, create); err != nil || !replay.Replayed {
		t.Fatalf("replay = %#v, %v", replay, err)
	}
	next, err := saved.NewRevision("revision-2", 2, now.Add(time.Minute), "owner", payload, identity)
	if err != nil {
		t.Fatal(err)
	}
	updateEvidence, _ := saved.NewMutationEvidence("owner", saved.MutationActionUpdate, "update-1", fingerprint(saved.MutationActionUpdate), "request-2", "correlation-2", now.Add(time.Minute))
	updated, err := repo.UpdateVersion(saved.WithAuditIntent(ctx, access.AuditIntent{EventID: "saved-exploration:update-1", Source: "analytics.exploration.saved", Operation: "updateSavedExploration", Action: "saved_exploration.updated", Capability: access.CapabilityResourceEdit, Outcome: "success", MetadataJSON: `{}`}), saved.UpdateVersionInput{ProjectID: "project:sales", ID: "exploration-1", ExpectedRevision: revision.Token(), Revision: next, Title: "Orders v2", Slug: "orders-v2", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", UpdatedAt: now.Add(time.Minute), Evidence: updateEvidence})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Lifecycle.Title != "Orders v2" {
		t.Fatalf("updated = %#v", updated)
	}
	lookup, found, err := repo.LookupMutation(ctx, saved.MutationLookupInput{ProjectID: create.ProjectID, ActorID: evidence.ActorID, Action: evidence.Action, IdempotencyKey: evidence.IdempotencyKey, Fingerprint: evidence.Fingerprint})
	if err != nil || !found || lookup.Lifecycle.Title != "Orders" || lookup.AppliedRevision.Number != 1 || lookup.Evidence.Action != saved.MutationActionCreate {
		t.Fatalf("durable create lookup after update = %#v, %v, found=%t", lookup, err, found)
	}
	changedEvidence := evidence
	changedEvidence.Fingerprint = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if _, _, err := repo.LookupMutation(ctx, saved.MutationLookupInput{ProjectID: create.ProjectID, ActorID: evidence.ActorID, Action: evidence.Action, IdempotencyKey: evidence.IdempotencyKey, Fingerprint: changedEvidence.Fingerprint}); !errors.Is(err, saved.ErrConflict) {
		t.Fatalf("same-key different-fingerprint lookup error = %v, want conflict", err)
	}
	if rows, err := repo.List(ctx, saved.ListInput{ProjectID: create.ProjectID}); err != nil || len(rows) != 1 {
		t.Fatalf("active list = %d, %v", len(rows), err)
	}
	destRevision, err := saved.NewRevision("revision-copy", 1, now.Add(2*time.Minute), "owner", payload, identity)
	if err != nil {
		t.Fatal(err)
	}
	dupEvidence, _ := saved.NewMutationEvidence("owner", saved.MutationActionDuplicate, "duplicate-1", fingerprint(saved.MutationActionDuplicate), "request-3", "correlation-3", now.Add(2*time.Minute))
	dup, err := repo.Duplicate(saved.WithAuditIntent(ctx, access.AuditIntent{EventID: "saved-exploration:duplicate-1", Source: "analytics.exploration.saved", Operation: "duplicateSavedExploration", Action: "saved_exploration.duplicated", Capability: access.CapabilityResourceEdit, Outcome: "success", MetadataJSON: `{}`}), saved.DuplicateInput{ProjectID: "project:sales", SourceID: "exploration-1", ExpectedSourceRevision: next.Token(), Evidence: dupEvidence, Destination: saved.CreateInput{ProjectID: "project:sales", ID: "exploration-copy", OwnerPrincipalID: "owner", Title: "Copy", Slug: "copy", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: now.Add(2 * time.Minute), Revision: destRevision}})
	if err != nil {
		t.Fatal(err)
	}
	if dup.Revision == nil {
		t.Fatal("duplicate returned no revision")
	}
	archiveEvidence, _ := saved.NewMutationEvidence("owner", saved.MutationActionArchive, "archive-1", fingerprint(saved.MutationActionArchive), "request-4", "correlation-4", now.Add(3*time.Minute))
	archived, err := repo.Archive(saved.WithAuditIntent(ctx, access.AuditIntent{EventID: "saved-exploration:archive-1", Source: "analytics.exploration.saved", Operation: "archiveSavedExploration", Action: "saved_exploration.archived", Capability: access.CapabilityResourceEdit, Outcome: "success", MetadataJSON: `{}`}), saved.ArchiveInput{ProjectID: "project:sales", ID: "exploration-1", ExpectedRevision: next.Token(), ArchivedAt: now.Add(3 * time.Minute), Evidence: archiveEvidence})
	if err != nil {
		t.Fatal(err)
	}
	if archived.Revision != nil || archived.Lifecycle.Status != saved.StatusArchived {
		t.Fatalf("archive = %#v", archived)
	}
}

func TestRepositoryMetadataProjectionAndAuditRollback(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, t.TempDir()+"/platform.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	db := store.SQLDB()
	if _, err := db.ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES ('owner', 'owner@example.test', 'Owner')`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	payload, err := saved.NewExplorationSpecPayload(canonical.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", Dimensions: []canonical.ExplorationDimensionRef{{Field: "orders.status"}}, Metrics: []canonical.ExplorationMetricRef{{Field: "order_count"}}, Filters: []canonical.ExplorationFilter{}, Sort: []canonical.ExplorationSort{}, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := newIdentity("project:sales")
	if err != nil {
		t.Fatal(err)
	}
	revision, err := saved.NewRevision("revision-1", 1, now, "owner", payload, identity)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, _ := saved.CanonicalFingerprint(struct{ Value string }{"create"})
	evidence, _ := saved.NewMutationEvidence("owner", saved.MutationActionCreate, "create-1", fingerprint, "request-1", "correlation-1", now)
	create := saved.CreateInput{ProjectID: "project:sales", ID: "exploration-1", OwnerPrincipalID: "owner", Title: "Orders", Slug: "orders", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: now, Revision: revision, Evidence: evidence}
	intent := access.AuditIntent{EventID: "saved-exploration:create-1", Source: "analytics.exploration.saved", Operation: "createSavedExploration", Action: "saved_exploration.created", Capability: access.CapabilityResourceEdit, Outcome: "success", MetadataJSON: `{}`}
	repo := NewRepositoryWithAudit(db, accesssqlite.NewRepository(db))
	if _, err := repo.Create(saved.WithAuditIntent(ctx, intent), create); err != nil {
		t.Fatal(err)
	}
	// The payload is valid JSON but intentionally not valid strict canonical
	// ExplorationSpec JSON. Lifecycle projections remain readable because they
	// do not select or decode the payload column.
	if _, err := db.ExecContext(ctx, `DROP TRIGGER saved_exploration_revisions_immutable_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE saved_exploration_revisions SET spec_canonical_json = ? WHERE project_id = ? AND exploration_id = ? AND revision_id = ?`, `{"version":1,"spec":{"modelId":"semantic:sales","unknown":true}}`, "project:sales", "exploration-1", "revision-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetLifecycle(ctx, saved.ReadInput{ProjectID: "project:sales", ID: "exploration-1"}); err != nil {
		t.Fatalf("lifecycle read after payload corruption: %v", err)
	}
	if rows, err := repo.List(ctx, saved.ListInput{ProjectID: "project:sales"}); err != nil || len(rows) != 1 {
		t.Fatalf("list after payload corruption = %d, %v", len(rows), err)
	}
	lookup, found, err := repo.LookupMutation(ctx, saved.MutationLookupInput{ProjectID: create.ProjectID, ActorID: evidence.ActorID, Action: evidence.Action, IdempotencyKey: evidence.IdempotencyKey, Fingerprint: evidence.Fingerprint})
	if err != nil || !found || lookup.Lifecycle.ID != create.ID || lookup.AppliedRevision != revision.Token() {
		t.Fatalf("metadata lookup after payload corruption = %#v, found=%t, err=%v", lookup, found, err)
	}
	if _, err := repo.GetRevision(ctx, saved.RevisionReadInput{ProjectID: "project:sales", ID: "exploration-1", Revision: revision.Token()}); !errors.Is(err, saved.ErrInvalidPayload) {
		t.Fatalf("corrupt exact revision error = %v, want invalid payload", err)
	}

	// A repository without the Access recorder cannot start a fresh mutation.
	noAudit := NewRepository(db)
	second := create
	second.ID = "exploration-2"
	second.Slug = "orders-2"
	second.Evidence, _ = saved.NewMutationEvidence("owner", saved.MutationActionCreate, "create-2", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "request-2", "correlation-2", now)
	if _, err := noAudit.Create(ctx, second); !errors.Is(err, saved.ErrUnavailable) {
		t.Fatalf("missing audit error = %v, want unavailable", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM saved_explorations WHERE project_id = 'project:sales' AND exploration_id = 'exploration-2'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("missing-audit create rows = %d, want 0", count)
	}
	injected := errors.New("audit unavailable")
	next, err := saved.NewRevision("revision-2", 2, now.Add(time.Minute), "owner", payload, identity)
	if err != nil {
		t.Fatal(err)
	}
	updateEvidence, err := saved.NewMutationEvidence("owner", saved.MutationActionUpdate, "update-1", "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "request-3", "correlation-3", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	failedRepo := NewRepositoryWithAudit(db, failingAuditRecorder{err: injected})
	_, err = failedRepo.UpdateVersion(saved.WithAuditIntent(ctx, access.AuditIntent{EventID: "saved-exploration:update-rollback", Source: "analytics.exploration.saved", Operation: "updateSavedExploration", Action: "saved_exploration.updated", Capability: access.CapabilityResourceEdit, Outcome: "success", MetadataJSON: `{}`}), saved.UpdateVersionInput{ProjectID: "project:sales", ID: "exploration-1", ExpectedRevision: revision.Token(), Revision: next, Title: "Orders v2", Slug: "orders-v2", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", UpdatedAt: now.Add(time.Minute), Evidence: updateEvidence})
	if !errors.Is(err, saved.ErrUnavailable) || !errors.Is(err, injected) {
		t.Fatalf("failing audit error = %v, want saved.ErrUnavailable and original cause", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM saved_exploration_revisions WHERE project_id = 'project:sales' AND exploration_id = 'exploration-1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("revision rows after audit rollback = %d, want 1", count)
	}
}

type failingAuditRecorder struct{ err error }

func (r failingAuditRecorder) RecordAuditIntent(context.Context, transaction.Transaction, access.AuditIntent) error {
	return r.err
}

func newIdentity(project string) (projectgraph.ServingIdentity, error) {
	return projectgraph.NewServingIdentity(projectgraph.ResourceID(project), "production", "generation-1")
}
