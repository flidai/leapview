package http

import (
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestTypedBrowserCatalogFiltersExactReadPairs(t *testing.T) {
	projectID := projectgraph.ResourceID("project:test")
	dashboardA, err := access.NewResourceRef("dashboard:a", projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	dashboardPair, err := access.NewExactPermissionPair(access.ActionDashboardRead, projectID, dashboardA)
	if err != nil {
		t.Fatal(err)
	}
	credential := &access.APICredential{Token: access.APIToken{
		ID: "typed", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{dashboardPair},
	}}
	page := filterCatalogPageForCredential(projectcatalog.Page{Items: []projectcatalog.Result{
		{Ref: projectcatalog.Ref{ID: "dashboard:a", Kind: projectgraph.KindDashboard}},
		{Ref: projectcatalog.Ref{ID: "dashboard:b", Kind: projectgraph.KindDashboard}},
		{Ref: projectcatalog.Ref{ID: "semantic:sales", Kind: projectgraph.KindSemanticModel}},
		{Ref: projectcatalog.Ref{ID: projectID, Kind: projectgraph.KindProjectNamespace}},
	}}, credential, projectID)
	if len(page.Items) != 1 || page.Items[0].Ref.ID != "dashboard:a" {
		t.Fatalf("filtered page = %#v, want only dashboard:a", page.Items)
	}

	sessionPage := projectcatalog.Page{Items: []projectcatalog.Result{{Ref: projectcatalog.Ref{ID: "dashboard:b", Kind: projectgraph.KindDashboard}}}}
	if got := filterCatalogPageForCredential(sessionPage, nil, projectID); len(got.Items) != 1 {
		t.Fatalf("session page = %#v, want durable catalog result retained", got.Items)
	}
}
