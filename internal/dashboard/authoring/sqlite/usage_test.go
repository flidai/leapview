package sqlite_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	dashboardmodel "github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringsqlite "github.com/flidai/leapview/internal/dashboard/authoring/sqlite"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/project/graph"
)

func TestCountBySemanticModelCountsActiveDraftsAndPublishedDashboards(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES (?, ?, ?)`, "principal-1", "owner@example.test", "Owner"); err != nil {
		t.Fatal(err)
	}
	repo := authoringsqlite.NewRepository(store.SQLDB())

	alphaPublished := createUsageDashboard(t, ctx, repo, "project-1", "alpha-published", "alpha-published", "alpha", authoring.VisibilityOrganization)
	publishUsageDashboard(t, ctx, repo, alphaPublished)
	createUsageDashboard(t, ctx, repo, "project-1", "alpha-draft", "alpha-draft", "alpha", authoring.VisibilityPrivate)
	createUsageDashboard(t, ctx, repo, "project-1", "beta-published", "beta-published", "beta", authoring.VisibilityPrivate)
	betaPublished := getUsageDashboard(t, ctx, repo, "project-1", "beta-published")
	publishUsageDashboard(t, ctx, repo, betaPublished)
	createUsageDashboard(t, ctx, repo, "project-1", "zeta-draft", "zeta-draft", "zeta", authoring.VisibilityPrivate)
	archived := createUsageDashboard(t, ctx, repo, "project-1", "alpha-archived", "alpha-archived", "alpha", authoring.VisibilityOrganization)
	if _, err := repo.Archive(ctx, authoring.ArchiveInput{
		ProjectID: graph.ResourceID("project-1"), DashboardID: archived.ID, ExpectedCurrentRevision: archived.Draft.Revision,
		Evidence: authoring.CommandEvidence{
			ID: "archive-alpha", Fingerprint: "archive-alpha-fingerprint", Action: authoring.AuthorizationActionArchive,
			Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: "principal-1"}, OccurredAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		},
	}); err != nil {
		t.Fatal(err)
	}

	// A same-named model in another project must not affect project-1.
	createUsageDashboard(t, ctx, repo, "project-2", "alpha-other", "alpha-other", "alpha", authoring.VisibilityOrganization)

	got, err := repo.CountBySemanticModel(ctx, graph.ResourceID("project-1"))
	if err != nil {
		t.Fatal(err)
	}
	want := []authoring.SemanticModelUsage{
		{SemanticModel: "alpha", Private: 1, Organization: 1, Total: 2},
		{SemanticModel: "beta", Private: 1, Organization: 0, Total: 1},
		{SemanticModel: "zeta", Private: 1, Organization: 0, Total: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("usage = %#v, want %#v", got, want)
	}
	for _, usage := range got {
		if err := usage.Validate(); err != nil {
			t.Fatalf("usage %q failed validation: %v", usage.SemanticModel, err)
		}
	}

	other, err := repo.CountBySemanticModel(ctx, graph.ResourceID("project-2"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []authoring.SemanticModelUsage{{SemanticModel: "alpha", Private: 0, Organization: 1, Total: 1}}; !reflect.DeepEqual(other, want) {
		t.Fatalf("project-2 usage = %#v, want %#v", other, want)
	}
	empty, err := repo.CountBySemanticModel(ctx, graph.ResourceID("project-empty"))
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty project usage = %#v, want non-nil empty result", empty)
	}
}

func createUsageDashboard(t *testing.T, ctx context.Context, repo *authoringsqlite.Repository, projectID, dashboardID, slug, semanticModel string, visibility authoring.Visibility) authoring.DashboardLifecycle {
	t.Helper()
	provenance := authoring.Provenance{Origin: authoring.OriginUI, ActorID: "principal-1"}
	document := authoring.Dashboard{ID: graph.ResourceID(dashboardID), Title: dashboardID, SemanticModel: graph.ResourceID(semanticModel), Visuals: map[string]authoring.AuthoringVisualization{}, Pages: []dashboardmodel.Page{{ID: "overview"}}}
	revision, err := authoring.NewRevision(authoring.RevisionID("revision-"+dashboardID), authoring.DashboardID(dashboardID), 1, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), document, provenance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{
		ProjectID: graph.ResourceID(projectID), ID: authoring.DashboardID(dashboardID), OwnerPrincipalID: "principal-1", Slug: slug,
		Title: dashboardID, SemanticModel: graph.ResourceID(semanticModel), Visibility: visibility,
		Draft: &authoring.Draft{ID: authoring.DraftID("draft-" + dashboardID), DashboardID: authoring.DashboardID(dashboardID), Revision: revision.Token(), Provenance: provenance},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := repo.Create(ctx, authoring.CreateInput{ProjectID: graph.ResourceID(projectID), Lifecycle: lifecycle, Revision: revision})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func getUsageDashboard(t *testing.T, ctx context.Context, repo *authoringsqlite.Repository, projectID, dashboardID string) authoring.DashboardLifecycle {
	t.Helper()
	got, err := repo.Get(ctx, graph.ResourceID(projectID), authoring.DashboardID(dashboardID))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func publishUsageDashboard(t *testing.T, ctx context.Context, repo *authoringsqlite.Repository, lifecycle authoring.DashboardLifecycle) {
	t.Helper()
	if lifecycle.Draft == nil {
		t.Fatalf("dashboard %q has no draft", lifecycle.ID)
	}
	provenance := authoring.Provenance{Origin: authoring.OriginUI, ActorID: "principal-1"}
	document := dashboarddefinition.Definition{ID: lifecycle.ID.String(), Title: lifecycle.Title, SemanticModel: lifecycle.SemanticModel.String(), Pages: []dashboardmodel.Page{{ID: "overview"}}, Visualizations: map[string]visualizationdefinition.Definition{}}
	identity, err := graph.NewServingIdentity(lifecycle.ProjectID, "test", "state-"+lifecycle.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := authoring.NewCompiledRevision(lifecycle.ProjectID, lifecycle.ID, lifecycle.Draft.Revision, document, identity, time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	published := authoring.Published{Revision: lifecycle.Draft.Revision, Compilation: compiled.Token(), PublishedAt: time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC), Provenance: provenance}
	if _, err := repo.Publish(ctx, authoring.PublishInput{
		ProjectID: lifecycle.ProjectID, DashboardID: lifecycle.ID, ExpectedDraftRevision: lifecycle.Draft.Revision,
		Published: published, Compilation: compiled,
		Evidence: authoring.CommandEvidence{ID: authoring.CommandID("publish-" + lifecycle.ID.String()), Fingerprint: "publish-fingerprint-" + lifecycle.ID.String(), Action: authoring.AuthorizationActionPublish, Provenance: provenance, OccurredAt: published.PublishedAt},
	}); err != nil {
		t.Fatal(err)
	}
}
