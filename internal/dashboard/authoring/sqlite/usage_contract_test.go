package sqlite_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringsqlite "github.com/flidai/leapview/internal/dashboard/authoring/sqlite"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/project/graph"
)

func canonicalUsageInput(t *testing.T, project, dashboardID, revisionID, semanticModel string, visibility authoring.Visibility) authoring.CreateInput {
	t.Helper()
	documentValue := canonicalSQLiteDocument(dashboardID)
	documentValue.Spec.SemanticModel = semanticModel
	documentValue.Metadata.DisplayName = stringPointer("Sales " + dashboardID)
	provenance := authoring.Provenance{Origin: authoring.OriginUI, ActorID: "owner"}
	revision, err := authoring.NewRevision(authoring.RevisionID(revisionID), authoring.DashboardID(dashboardID), 1, time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC), documentValue, provenance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{ProjectID: graph.ResourceID(project), ID: authoring.DashboardID(dashboardID), OwnerPrincipalID: "owner", Slug: strings.ToLower(strings.ReplaceAll(dashboardID, ":", "-")), Title: "Sales " + dashboardID, SemanticModel: graph.ResourceID(semanticModel), Visibility: visibility, Draft: &authoring.Draft{ID: authoring.DraftID("draft-" + dashboardID), DashboardID: authoring.DashboardID(dashboardID), Revision: revision.Token(), Provenance: provenance}})
	if err != nil {
		t.Fatal(err)
	}
	return authoring.CreateInput{ProjectID: graph.ResourceID(project), Lifecycle: lifecycle, Revision: revision}
}

func canonicalPublishUsage(t *testing.T, ctx context.Context, repository *authoringsqlite.Repository, lifecycle authoring.DashboardLifecycle) {
	t.Helper()
	identity := servingIdentity(t, lifecycle.ProjectID.String(), "test", "generation-"+strings.ReplaceAll(lifecycle.ID.String(), ":", "-"))
	definition := dashboarddefinition.Definition{ID: lifecycle.ID.String(), Title: lifecycle.Title, SemanticModel: lifecycle.SemanticModel.String()}
	compiled, err := authoring.NewCompiledRevision(lifecycle.ProjectID, lifecycle.ID, lifecycle.Draft.Revision, definition, identity, time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	provenance := authoring.Provenance{Origin: authoring.OriginUI, ActorID: "owner"}
	if _, err := repository.Publish(ctx, authoring.PublishInput{ProjectID: lifecycle.ProjectID, DashboardID: lifecycle.ID, ExpectedDraftRevision: lifecycle.Draft.Revision, Published: authoring.Published{Revision: lifecycle.Draft.Revision, Compilation: compiled.Token(), PublishedAt: compiled.CompiledAt, Provenance: provenance}, Compilation: compiled, Evidence: authoring.CommandEvidence{ID: authoring.CommandID("publish-" + lifecycle.ID.String()), Fingerprint: "fingerprint-" + lifecycle.ID.String(), Action: authoring.AuthorizationActionPublish, Provenance: provenance, OccurredAt: compiled.CompiledAt}}); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalSQLiteCountBySemanticModelCountsActiveDraftsAndPublishedDashboards(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, t.TempDir()+"/usage.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES ('owner', 'owner@example.test', 'Owner')`); err != nil {
		t.Fatal(err)
	}
	repository := authoringsqlite.NewRepository(store.SQLDB())
	alphaPublished := canonicalUsageInput(t, "project:one", "dashboard:alpha-published", "revision-alpha-published", "model:alpha", authoring.VisibilityOrganization)
	if _, err := repository.Create(ctx, alphaPublished); err != nil {
		t.Fatal(err)
	}
	alphaPublishedLifecycle, err := repository.Get(ctx, "project:one", "dashboard:alpha-published")
	if err != nil {
		t.Fatal(err)
	}
	canonicalPublishUsage(t, ctx, repository, alphaPublishedLifecycle)
	for _, input := range []authoring.CreateInput{
		canonicalUsageInput(t, "project:one", "dashboard:alpha-draft", "revision-alpha-draft", "model:alpha", authoring.VisibilityPrivate),
		canonicalUsageInput(t, "project:one", "dashboard:beta-published", "revision-beta-published", "model:beta", authoring.VisibilityPrivate),
		canonicalUsageInput(t, "project:one", "dashboard:zeta-draft", "revision-zeta-draft", "model:zeta", authoring.VisibilityPrivate),
	} {
		if _, err := repository.Create(ctx, input); err != nil {
			t.Fatal(err)
		}
		if input.Lifecycle.ID == "dashboard:beta-published" {
			canonicalPublishUsage(t, ctx, repository, input.Lifecycle)
		}
	}
	archivedInput := canonicalUsageInput(t, "project:one", "dashboard:alpha-archived", "revision-alpha-archived", "model:alpha", authoring.VisibilityOrganization)
	archived, err := repository.Create(ctx, archivedInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Archive(ctx, authoring.ArchiveInput{ProjectID: archived.ProjectID, DashboardID: archived.ID, ExpectedCurrentRevision: archived.Draft.Revision, Evidence: authoring.CommandEvidence{ID: "archive-alpha", Fingerprint: "archive-alpha", Action: authoring.AuthorizationActionArchive, Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: "owner"}, OccurredAt: time.Date(2026, 8, 18, 13, 0, 0, 0, time.UTC)}}); err != nil {
		t.Fatal(err)
	}
	otherProject := canonicalUsageInput(t, "project:two", "dashboard:alpha-other", "revision-alpha-other", "model:alpha", authoring.VisibilityOrganization)
	if _, err := repository.Create(ctx, otherProject); err != nil {
		t.Fatal(err)
	}
	usage, err := repository.CountBySemanticModel(ctx, "project:one")
	if err != nil {
		t.Fatal(err)
	}
	want := []authoring.SemanticModelUsage{{SemanticModel: "model:alpha", Private: 1, Organization: 1, Total: 2}, {SemanticModel: "model:beta", Private: 1, Organization: 0, Total: 1}, {SemanticModel: "model:zeta", Private: 1, Organization: 0, Total: 1}}
	if !reflect.DeepEqual(usage, want) {
		t.Fatalf("usage=%#v want=%#v", usage, want)
	}
	empty, err := repository.CountBySemanticModel(ctx, "project:empty")
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty project usage=%#v", empty)
	}
}
