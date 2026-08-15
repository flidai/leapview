package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	dashboardmodel "github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringsqlite "github.com/flidai/leapview/internal/dashboard/authoring/sqlite"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	"github.com/flidai/leapview/internal/platform"
)

func TestAuthoringLifecyclePersistence(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES (?, ?, ?)`, "principal-1", "owner@example.test", "Owner"); err != nil {
		t.Fatal(err)
	}
	doc := authoring.Dashboard{ID: "dash", Title: "Dash", SemanticModel: "model", Visuals: map[string]authoring.AuthoringVisualization{}, Pages: []dashboardmodel.Page{{ID: "overview"}}}
	prov := authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}
	rev, err := authoring.NewRevision("r1", "dash", 1, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), doc, prov)
	if err != nil {
		t.Fatal(err)
	}
	draft := &authoring.Draft{ID: "draft", DashboardID: "dash", Revision: rev.Token(), Provenance: prov}
	life, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{ProjectID: "w", ID: "dash", OwnerPrincipalID: "principal-1", Slug: "dash", Title: "Dash", SemanticModel: "model", Visibility: authoring.VisibilityPrivate, Draft: draft})
	if err != nil {
		t.Fatal(err)
	}
	r := authoringsqlite.NewRepository(store.SQLDB())
	if _, err := r.Create(ctx, authoring.CreateInput{ProjectID: "w", Lifecycle: life, Revision: rev}); err != nil {
		t.Fatal(err)
	}
	docOther := doc
	docOther.ID = "dash2"
	docOther.Title = "Dash 2"
	revOther, err := authoring.NewRevision("r1", "dash2", 1, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), docOther, prov)
	if err != nil {
		t.Fatal(err)
	}
	lifeOther, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{ProjectID: "w2", ID: "dash2", OwnerPrincipalID: "principal-1", Slug: "dash", Title: "Dash 2", SemanticModel: "model", Visibility: authoring.VisibilityOrganization, Draft: &authoring.Draft{ID: "draft", DashboardID: "dash2", Revision: revOther.Token(), Provenance: prov}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Create(ctx, authoring.CreateInput{ProjectID: "w2", Lifecycle: lifeOther, Revision: revOther}); err != nil {
		t.Fatal(err)
	}
	conflictingLife := lifeOther
	conflictingLife.ProjectID = "w"
	if _, err := r.Create(ctx, authoring.CreateInput{ProjectID: "w", Lifecycle: conflictingLife, Revision: revOther}); !errors.Is(err, authoring.ErrConflict) {
		t.Fatalf("same-project slug error = %v", err)
	}
	if list, err := r.List(ctx, "w"); err != nil || len(list) != 1 {
		t.Fatalf("project list = %#v, err = %v", list, err)
	}
	got, err := r.Get(ctx, "w", "dash")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != life.ID {
		t.Fatalf("got %#v", got)
	}
	doc.Title = "Dash v2"
	rev2, err := authoring.NewRevision("r2", "dash", 2, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), doc, prov)
	if err != nil {
		t.Fatal(err)
	}
	next := life
	next.Slug = "dash-updated"
	next.Title = "Dash v2"
	next.Visibility = authoring.VisibilityOrganization
	next.Draft = &authoring.Draft{ID: draft.ID, DashboardID: "dash", Revision: rev2.Token(), Provenance: authoring.Provenance{Origin: authoring.OriginAgent, ActorID: "agent", Source: &authoring.SourceMetadata{Path: "agent.json"}}}
	appendInput := authoring.AppendDraftInput{ProjectID: "w", DashboardID: "dash", ExpectedDraftRevision: rev.Token(), Revision: rev2, Next: next, Evidence: authoring.CommandEvidence{ID: "cmd-1", Fingerprint: "fp-1", Action: authoring.AuthorizationActionEdit, Provenance: prov, OccurredAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}}
	if _, err := r.AppendDraft(ctx, appendInput); err != nil {
		t.Fatal(err)
	}
	commandResult, found, err := r.LookupCommandResult(ctx, "w", "dash", appendInput.Evidence)
	if err != nil || !found || commandResult.Revision != rev2.Token() {
		t.Fatalf("command lookup = %#v, found=%v, err=%v", commandResult, found, err)
	}
	changedEvidence := appendInput.Evidence
	changedEvidence.Fingerprint = "changed"
	if _, _, err := r.LookupCommandResult(ctx, "w", "dash", changedEvidence); !errors.Is(err, authoring.ErrCommandReuse) {
		t.Fatalf("changed command lookup error = %v", err)
	}
	staleEvidence := appendInput.Evidence
	staleEvidence.ID, staleEvidence.Fingerprint = "cmd-stale", "fp-stale"
	if _, err := r.AppendDraft(ctx, authoring.AppendDraftInput{ProjectID: "w", DashboardID: "dash", ExpectedDraftRevision: rev.Token(), Revision: rev2, Next: next, Evidence: staleEvidence}); !errors.Is(err, authoring.ErrConflict) {
		t.Fatalf("stale append error = %v", err)
	}
	tamperedDoc := doc
	tamperedDoc.Title = "tampered"
	tampered, err := authoring.NewRevision("r2", "dash", 3, time.Date(2026, 1, 2, 1, 0, 0, 0, time.UTC), tamperedDoc, authoring.Provenance{Origin: authoring.OriginAgent, ActorID: "attacker"})
	if err != nil {
		t.Fatal(err)
	}
	tamperedNext := next
	tamperedNext.Draft = &authoring.Draft{ID: draft.ID, DashboardID: "dash", Revision: tampered.Token(), Provenance: tampered.Provenance}
	immutableEvidence := appendInput.Evidence
	immutableEvidence.ID, immutableEvidence.Fingerprint = "cmd-immutable", "fp-immutable"
	if _, err := r.AppendDraft(ctx, authoring.AppendDraftInput{ProjectID: "w", DashboardID: "dash", ExpectedDraftRevision: rev2.Token(), Revision: tampered, Next: tamperedNext, Evidence: immutableEvidence}); !errors.Is(err, authoring.ErrInvalidAuthoring) {
		t.Fatalf("immutable revision reuse error = %v", err)
	}
	unchanged, err := r.Get(ctx, "w", "dash")
	if err != nil || unchanged.Draft == nil || unchanged.Draft.Revision != rev2.Token() || unchanged.Title != "Dash v2" {
		t.Fatalf("immutable conflict changed lifecycle = %#v, err = %v", unchanged, err)
	}
	if replay, err := r.AppendDraft(ctx, appendInput); err != nil || replay.ID != rev2.ID {
		t.Fatalf("replay = %#v, err = %v", replay, err)
	}
	updated, err := r.Get(ctx, "w", "dash")
	if err != nil || updated.Slug != "dash-updated" || updated.Visibility != authoring.VisibilityOrganization || updated.Draft == nil || updated.Draft.Provenance.ActorID != "agent" {
		t.Fatalf("next lifecycle roundtrip = %#v, err = %v", updated, err)
	}
	appendInput.Evidence.Fingerprint = "changed"
	if _, err := r.AppendDraft(ctx, appendInput); !errors.Is(err, authoring.ErrCommandReuse) {
		t.Fatalf("changed command fingerprint error = %v", err)
	}
	compiled2, err := authoring.NewCompiledRevision("w", "dash", rev2.Token(), dashboarddefinition.Definition{ID: "dash", Title: "Dash v2", SemanticModel: "model", Pages: doc.Pages, Visualizations: map[string]visualizationdefinition.Definition{}}, "state-1", time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	published := authoring.Published{Revision: rev2.Token(), Compilation: compiled2.Token(), PublishedAt: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC), Provenance: prov}
	if _, err := r.Publish(ctx, authoring.PublishInput{ProjectID: "w", DashboardID: "dash", ExpectedDraftRevision: rev2.Token(), Published: published, Compilation: compiled2, Evidence: authoring.CommandEvidence{ID: "cmd-2", Fingerprint: "fp-2", Action: authoring.AuthorizationActionPublish, Provenance: prov, OccurredAt: published.PublishedAt}}); err != nil {
		t.Fatal(err)
	}
	gotCompiled, err := r.GetPublishedCompilation(ctx, "w", "dash")
	if err != nil || gotCompiled.DefinitionHash != compiled2.DefinitionHash || gotCompiled.SemanticServingStateID != compiled2.SemanticServingStateID || gotCompiled.Definition.ID != "dash" {
		t.Fatalf("published compilation = %#v, err = %v", gotCompiled, err)
	}
	beforeInvalid, err := r.Get(ctx, "w", "dash")
	if err != nil {
		t.Fatal(err)
	}
	invalidCompiled := compiled2
	invalidCompiled.Definition.Title = "tampered"
	invalidPublish := authoring.PublishInput{ProjectID: "w", DashboardID: "dash", ExpectedDraftRevision: rev2.Token(), Published: published, Compilation: invalidCompiled, Evidence: authoring.CommandEvidence{ID: "cmd-invalid", Fingerprint: "fp-invalid", Action: authoring.AuthorizationActionPublish, Provenance: prov, OccurredAt: published.PublishedAt}}
	if _, err := r.Publish(ctx, invalidPublish); !errors.Is(err, authoring.ErrInvalidAuthoring) {
		t.Fatalf("invalid compiled publish error = %v", err)
	}
	afterInvalid, err := r.Get(ctx, "w", "dash")
	if err != nil || afterInvalid.Status != beforeInvalid.Status || afterInvalid.Published == nil || afterInvalid.Published.Compilation != beforeInvalid.Published.Compilation {
		t.Fatalf("invalid compiled publish changed lifecycle: before=%#v after=%#v err=%v", beforeInvalid, afterInvalid, err)
	}
	var invalidCommandCount int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM dashboard_authoring_commands WHERE project_id = ? AND dashboard_id = ? AND command_id = ?`, "w", "dash", "cmd-invalid").Scan(&invalidCommandCount); err != nil || invalidCommandCount != 0 {
		t.Fatalf("invalid compiled publish command rows = %d, err=%v", invalidCommandCount, err)
	}
	compiled2Revalidated, err := authoring.NewCompiledRevision("w", "dash", rev2.Token(), compiled2.Definition, "state-2", time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	revalidatedPublished := authoring.Published{Revision: rev2.Token(), Compilation: compiled2Revalidated.Token(), PublishedAt: time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC), Provenance: prov}
	if _, err := r.Publish(ctx, authoring.PublishInput{ProjectID: "w", DashboardID: "dash", ExpectedDraftRevision: rev2.Token(), Published: revalidatedPublished, Compilation: compiled2Revalidated, Evidence: authoring.CommandEvidence{ID: "cmd-revalidate", Fingerprint: "fp-revalidate", Action: authoring.AuthorizationActionPublish, Provenance: prov, OccurredAt: revalidatedPublished.PublishedAt}}); err != nil {
		t.Fatal(err)
	}
	if got, err := r.GetPublishedCompilation(ctx, "w", "dash"); err != nil || got.SemanticServingStateID != "state-2" {
		t.Fatalf("revalidated compilation = %#v, err = %v", got, err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `CREATE TRIGGER fail_authoring_command BEFORE INSERT ON dashboard_authoring_commands WHEN NEW.command_id = 'cmd-db-fail' BEGIN SELECT RAISE(ABORT, 'forced command ledger failure'); END`); err != nil {
		t.Fatal(err)
	}
	compiledFailure, err := authoring.NewCompiledRevision("w", "dash", rev2.Token(), compiled2.Definition, "state-db-fail", time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	failurePublished := authoring.Published{Revision: rev2.Token(), Compilation: compiledFailure.Token(), PublishedAt: compiledFailure.CompiledAt, Provenance: prov}
	_, failureErr := r.Publish(ctx, authoring.PublishInput{ProjectID: "w", DashboardID: "dash", ExpectedDraftRevision: rev2.Token(), Published: failurePublished, Compilation: compiledFailure, Evidence: authoring.CommandEvidence{ID: "cmd-db-fail", Fingerprint: "fp-db-fail", Action: authoring.AuthorizationActionPublish, Provenance: prov, OccurredAt: compiledFailure.CompiledAt}})
	if _, err := store.SQLDB().ExecContext(ctx, `DROP TRIGGER fail_authoring_command`); err != nil {
		t.Fatal(err)
	}
	if failureErr == nil {
		t.Fatal("forced command ledger failure unexpectedly succeeded")
	}
	if got, err := r.GetPublishedCompilation(ctx, "w", "dash"); err != nil || got.SemanticServingStateID != "state-2" {
		t.Fatalf("late DB failure changed published compilation = %#v, err = %v", got, err)
	}
	var failedCompiledCount, failedCommandCount int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM dashboard_authoring_compiled_revisions WHERE project_id = ? AND dashboard_id = ? AND semantic_serving_state_id = ?`, "w", "dash", "state-db-fail").Scan(&failedCompiledCount); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM dashboard_authoring_commands WHERE project_id = ? AND dashboard_id = ? AND command_id = ?`, "w", "dash", "cmd-db-fail").Scan(&failedCommandCount); err != nil {
		t.Fatal(err)
	}
	if failedCompiledCount != 0 || failedCommandCount != 0 {
		t.Fatalf("late DB failure left rows: compiled=%d command=%d", failedCompiledCount, failedCommandCount)
	}
	if _, err := r.GetPublishedCompilation(ctx, "w2", "dash"); !errors.Is(err, authoring.ErrNotFound) {
		t.Fatalf("project-isolated compilation lookup error = %v", err)
	}
	var action, provenanceJSON, occurredAt string
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT action, provenance_json, occurred_at FROM dashboard_authoring_commands WHERE project_id = ? AND dashboard_id = ? AND command_id = ?`, "w", "dash", "cmd-2").Scan(&action, &provenanceJSON, &occurredAt); err != nil {
		t.Fatal(err)
	}
	if action != string(authoring.AuthorizationActionPublish) || provenanceJSON == "" || occurredAt != "2026-01-03T00:00:00Z" {
		t.Fatalf("publish command evidence = action %q provenance %q occurred %q", action, provenanceJSON, occurredAt)
	}
	doc.Title = "Dash v3"
	rev3, err := authoring.NewRevision("r3", "dash", 3, time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC), doc, prov)
	if err != nil {
		t.Fatal(err)
	}
	publishedLife, err := r.Get(ctx, "w", "dash")
	if err != nil {
		t.Fatal(err)
	}
	next2 := publishedLife
	next2.Title = rev3.Document.Title
	next2.Draft = &authoring.Draft{ID: publishedLife.Draft.ID, DashboardID: "dash", Revision: rev3.Token(), Provenance: prov}
	edit3Evidence := appendInput.Evidence
	edit3Evidence.ID, edit3Evidence.Fingerprint, edit3Evidence.OccurredAt = "cmd-4", "fp-4", time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC)
	if _, err := r.AppendDraft(ctx, authoring.AppendDraftInput{ProjectID: "w", DashboardID: "dash", ExpectedDraftRevision: rev2.Token(), Revision: rev3, Next: next2, Evidence: edit3Evidence}); err != nil {
		t.Fatal(err)
	}
	compiled3, err := authoring.NewCompiledRevision("w", "dash", rev3.Token(), dashboarddefinition.Definition{ID: "dash", Title: "Dash v3", SemanticModel: "model", Pages: doc.Pages, Visualizations: map[string]visualizationdefinition.Definition{}}, "state-2", time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	published3 := authoring.Published{Revision: rev3.Token(), Compilation: compiled3.Token(), PublishedAt: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), Provenance: prov}
	if _, err := r.Publish(ctx, authoring.PublishInput{ProjectID: "w", DashboardID: "dash", ExpectedDraftRevision: rev3.Token(), Published: published3, Compilation: compiled3, Evidence: authoring.CommandEvidence{ID: "cmd-5", Fingerprint: "fp-5", Action: authoring.AuthorizationActionPublish, Provenance: prov, OccurredAt: published3.PublishedAt}}); err != nil {
		t.Fatal(err)
	}
	if gotRev, err := r.GetRevision(ctx, "w", "dash", "r3"); err != nil || gotRev.Document.Title != "Dash v3" || gotRev.Provenance.ActorID != prov.ActorID {
		t.Fatalf("revision roundtrip = %#v, err = %v", gotRev, err)
	}
	archived, err := r.Archive(ctx, authoring.ArchiveInput{ProjectID: "w", DashboardID: "dash", ExpectedCurrentRevision: rev3.Token(), Evidence: authoring.CommandEvidence{ID: "cmd-3", Fingerprint: "fp-3", Action: authoring.AuthorizationActionArchive, Provenance: prov, OccurredAt: time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC)}})
	if err != nil {
		t.Fatal(err)
	}
	if archived.Status != authoring.LifecycleStatusArchived {
		t.Fatalf("status = %s", archived.Status)
	}
	for _, command := range []struct {
		id     string
		action authoring.AuthorizationAction
		at     string
	}{
		{id: "cmd-1", action: authoring.AuthorizationActionEdit, at: "2026-01-02T00:00:00Z"},
		{id: "cmd-3", action: authoring.AuthorizationActionArchive, at: "2026-01-06T00:00:00Z"},
	} {
		if err := store.SQLDB().QueryRowContext(ctx, `SELECT action, provenance_json, occurred_at FROM dashboard_authoring_commands WHERE project_id = ? AND dashboard_id = ? AND command_id = ?`, "w", "dash", command.id).Scan(&action, &provenanceJSON, &occurredAt); err != nil {
			t.Fatal(err)
		}
		if action != string(command.action) || provenanceJSON == "" || occurredAt != command.at {
			t.Fatalf("command %s evidence = action %q provenance %q occurred %q", command.id, action, provenanceJSON, occurredAt)
		}
	}
}
