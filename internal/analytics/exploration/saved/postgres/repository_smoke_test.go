package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/exploration/saved"
)

func TestRepositoryPostgreSQLSmoke(t *testing.T) {
	f := newRepositoryFixture(t)
	input, created := f.create(t, "exploration-smoke", "smoke", "smoke-create")
	if created.Revision == nil || created.Lifecycle.CurrentRevision.Token() != input.Revision.Token() {
		t.Fatalf("create = %#v", created)
	}
	if _, err := f.repo.GetLifecycle(t.Context(), saved.ReadInput{ProjectID: input.ProjectID, ID: input.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.GetRevision(t.Context(), saved.RevisionReadInput{ProjectID: input.ProjectID, ID: input.ID, Revision: input.Revision.Token()}); err != nil {
		t.Fatal(err)
	}
	if replay, err := f.repo.Create(t.Context(), input); err != nil || !replay.Replayed {
		t.Fatalf("replay = %#v, %v", replay, err)
	}

	next, err := saved.NewRevision("revision-smoke-2", 2, f.now.Add(time.Minute), testActorID, f.payload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	updateEvidence := f.evidence(t, saved.MutationActionUpdate, "smoke-update", f.now.Add(time.Minute))
	updated, err := f.repo.UpdateVersion(saved.WithAuditIntent(t.Context(), f.intent()), saved.UpdateVersionInput{
		ProjectID: input.ProjectID, ID: input.ID, ExpectedRevision: input.Revision.Token(), Revision: next,
		Title: "Smoke v2", Slug: "smoke-v2", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales",
		UpdatedAt: f.now.Add(time.Minute), Evidence: updateEvidence,
	})
	if err != nil || updated.Lifecycle.Title != "Smoke v2" {
		t.Fatalf("update = %#v, %v", updated, err)
	}
	lookup, found, err := f.repo.LookupMutation(t.Context(), saved.MutationLookupInput{ProjectID: input.ProjectID, ActorID: updateEvidence.ActorID, Action: updateEvidence.Action, IdempotencyKey: updateEvidence.IdempotencyKey, Fingerprint: updateEvidence.Fingerprint})
	if err != nil || !found || lookup.AppliedRevision != updated.AppliedRevision {
		t.Fatalf("update lookup = %#v, found=%t, err=%v", lookup, found, err)
	}
	changed := updateEvidence
	changed.Fingerprint = "sha256:" + strings.Repeat("c", 64)
	if _, _, err := f.repo.LookupMutation(t.Context(), saved.MutationLookupInput{ProjectID: input.ProjectID, ActorID: changed.ActorID, Action: changed.Action, IdempotencyKey: changed.IdempotencyKey, Fingerprint: changed.Fingerprint}); !errors.Is(err, saved.ErrConflict) {
		t.Fatalf("same-key lookup error = %v, want conflict", err)
	}

	destinationRevision, err := saved.NewRevision("revision-smoke-copy", 1, f.now.Add(2*time.Minute), testActorID, f.payload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	duplicateEvidence := f.evidence(t, saved.MutationActionDuplicate, "smoke-duplicate", f.now.Add(2*time.Minute))
	duplicate, err := f.repo.Duplicate(saved.WithAuditIntent(t.Context(), f.intent()), saved.DuplicateInput{
		ProjectID: input.ProjectID, SourceID: input.ID, ExpectedSourceRevision: updated.AppliedRevision, Evidence: duplicateEvidence,
		Destination: saved.CreateInput{ProjectID: input.ProjectID, ID: "exploration-smoke-copy", OwnerPrincipalID: testActorID, Title: "Copy", Slug: "copy", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: f.now.Add(2 * time.Minute), Revision: destinationRevision},
	})
	if err != nil || duplicate.Revision == nil {
		t.Fatalf("duplicate = %#v, %v", duplicate, err)
	}
	archiveEvidence := f.evidence(t, saved.MutationActionArchive, "smoke-archive", f.now.Add(3*time.Minute))
	archived, err := f.repo.Archive(saved.WithAuditIntent(t.Context(), f.intent()), saved.ArchiveInput{ProjectID: input.ProjectID, ID: input.ID, ExpectedRevision: updated.AppliedRevision, ArchivedAt: f.now.Add(3 * time.Minute), Evidence: archiveEvidence})
	if err != nil || archived.Revision != nil || archived.Lifecycle.Status != saved.StatusArchived {
		t.Fatalf("archive = %#v, %v", archived, err)
	}
}

func TestRepositoryPostgreSQLMetadataProjectionAndAuditRollback(t *testing.T) {
	f := newRepositoryFixture(t)
	input, created := f.create(t, "exploration-metadata", "metadata", "metadata-create")
	if _, err := f.repo.GetLifecycle(t.Context(), saved.ReadInput{ProjectID: input.ProjectID, ID: input.ID}); err != nil {
		t.Fatal(err)
	}
	if rows, err := f.repo.List(t.Context(), saved.ListInput{ProjectID: input.ProjectID}); err != nil || len(rows) != 1 {
		t.Fatalf("metadata list = %d, %v", len(rows), err)
	}
	lookup, found, err := f.repo.LookupMutation(t.Context(), saved.MutationLookupInput{ProjectID: input.ProjectID, ActorID: input.Evidence.ActorID, Action: input.Evidence.Action, IdempotencyKey: input.Evidence.IdempotencyKey, Fingerprint: input.Evidence.Fingerprint})
	if err != nil || !found || lookup.AppliedRevision != created.AppliedRevision {
		t.Fatalf("metadata lookup = %#v, found=%t, err=%v", lookup, found, err)
	}
	if _, err := f.db.Exec(t.Context(), `UPDATE saved_exploration.saved_exploration_revisions SET spec_canonical_json = $1 WHERE project_id = $2 AND exploration_id = $3 AND revision_id = $4`, []byte(`{"version":1,"spec":{"modelId":"semantic:sales","unknown":true}}`), input.ProjectID.String(), input.ID.String(), input.Revision.Metadata.ID.String()); err == nil {
		t.Fatal("immutable native revision update unexpectedly succeeded")
	}
	if _, err := f.repo.GetLifecycle(t.Context(), saved.ReadInput{ProjectID: input.ProjectID, ID: input.ID}); err != nil {
		t.Fatalf("lifecycle after rejected payload update: %v", err)
	}
	// Corrupt only the authored bytes under the disposable database's admin
	// authority. Metadata reads must not decode those bytes before application
	// authorization. The production runtime role cannot disable this trigger.
	if _, err := f.db.Exec(t.Context(), `ALTER TABLE saved_exploration.saved_exploration_revisions DISABLE TRIGGER saved_exploration_revision_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(t.Context(), `UPDATE saved_exploration.saved_exploration_revisions SET spec_canonical_json = $1 WHERE project_id = $2 AND exploration_id = $3`, []byte(`{"version":1,"spec":{"modelId":"semantic:sales","unknown":true}}`), input.ProjectID.String(), input.ID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(t.Context(), `ALTER TABLE saved_exploration.saved_exploration_revisions ENABLE TRIGGER saved_exploration_revision_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.GetLifecycle(t.Context(), saved.ReadInput{ProjectID: input.ProjectID, ID: input.ID}); err != nil {
		t.Fatalf("metadata read decoded corrupted authored bytes: %v", err)
	}
	if page, err := f.repo.ListPage(t.Context(), saved.ListInput{ProjectID: input.ProjectID}); err != nil || len(page.Items) != 1 {
		t.Fatalf("metadata page decoded corrupted authored bytes: %#v, %v", page, err)
	}
	if _, found, err := f.repo.LookupMutation(t.Context(), saved.MutationLookupInput{ProjectID: input.ProjectID, ActorID: input.Evidence.ActorID, Action: input.Evidence.Action, IdempotencyKey: input.Evidence.IdempotencyKey, Fingerprint: input.Evidence.Fingerprint}); err != nil || !found {
		t.Fatalf("metadata replay decoded corrupted authored bytes: found=%t err=%v", found, err)
	}
	if _, err := f.repo.GetRevision(t.Context(), saved.RevisionReadInput{ProjectID: input.ProjectID, ID: input.ID, Revision: created.AppliedRevision}); err == nil {
		t.Fatal("payload read accepted corrupted authored bytes")
	}

	missingEvidence := f.evidence(t, saved.MutationActionCreate, "metadata-missing-audit", f.now)
	missingRevision, err := saved.NewRevision("revision-missing-audit", 1, f.now, testActorID, f.payload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	noAudit := New(f.db, nil)
	if _, err := noAudit.Create(context.Background(), saved.CreateInput{ProjectID: input.ProjectID, ID: "exploration-missing-audit", OwnerPrincipalID: testActorID, Title: "Missing", Slug: "missing", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: f.now, Revision: missingRevision, Evidence: missingEvidence}); !errors.Is(err, saved.ErrUnavailable) {
		t.Fatalf("missing audit error = %v, want unavailable", err)
	}

	next, err := saved.NewRevision("revision-metadata-failed", 2, f.now.Add(time.Minute), testActorID, f.payload, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	failedEvidence := f.evidence(t, saved.MutationActionUpdate, "metadata-failed-update", f.now.Add(time.Minute))
	injected := errors.New("audit unavailable")
	failing := New(f.db, failingAuditAdapter{err: injected})
	_, err = failing.UpdateVersion(saved.WithAuditIntent(t.Context(), access.AuditIntent{}), saved.UpdateVersionInput{ProjectID: input.ProjectID, ID: input.ID, ExpectedRevision: created.AppliedRevision, Revision: next, Title: "Failed", Slug: "failed", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", UpdatedAt: f.now.Add(time.Minute), Evidence: failedEvidence})
	if !errors.Is(err, saved.ErrUnavailable) || !errors.Is(err, injected) {
		t.Fatalf("failing audit error = %v", err)
	}
	var revisions int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM saved_exploration.saved_exploration_revisions WHERE project_id = $1 AND exploration_id = $2`, input.ProjectID.String(), input.ID.String()).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if revisions != 1 {
		t.Fatalf("revision rows after audit rollback = %d, want 1", revisions)
	}
}
