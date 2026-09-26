package module

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
	agenttools "github.com/flidai/leapview/internal/agent/tools"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type credentialCatalogFake struct {
	page agenttools.CatalogPage
}

func (f credentialCatalogFake) Search(context.Context, agenttools.Scope, agenttools.CatalogSearchRequest) (agenttools.CatalogPage, error) {
	return f.page, nil
}

func (f credentialCatalogFake) List(context.Context, agenttools.Scope, agenttools.CatalogListRequest) (agenttools.CatalogPage, error) {
	return f.page, nil
}

func (f credentialCatalogFake) Get(_ context.Context, _ agenttools.Scope, request agenttools.CatalogGetRequest) (agenttools.CatalogGetResult, error) {
	return agenttools.CatalogGetResult{Item: agenttools.CatalogItem{Ref: request.Ref}}, nil
}

func TestCredentialCatalogFiltersItemsAndCountsByExactTypedPair(t *testing.T) {
	allowed := mustCatalogPermission(t, access.ActionDashboardRead, "dashboard:allowed", projectgraph.KindDashboard)
	scope := agenttools.Scope{ProjectID: "project:analytics", PrincipalID: "principal", Credential: agenttools.CredentialScope{
		Restricted: true, PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{allowed},
	}}
	base := credentialCatalogFake{page: agenttools.CatalogPage{Items: []agenttools.CatalogItem{
		{Ref: agenttools.CatalogRef{ID: "dashboard:allowed", Kind: agenttools.CatalogType(projectgraph.KindDashboard)}},
		{Ref: agenttools.CatalogRef{ID: "dashboard:hidden", Kind: agenttools.CatalogType(projectgraph.KindDashboard)}},
	}, Count: 2}}
	page, err := (credentialCatalog{base: base}).Search(t.Context(), scope, agenttools.CatalogSearchRequest{Query: "dashboard"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Count != 1 || len(page.Items) != 1 || page.Items[0].Ref.ID != "dashboard:allowed" {
		t.Fatalf("filtered page = %#v", page)
	}
}

func TestCredentialCatalogHidesWrongTargetFromGetAndParentList(t *testing.T) {
	allowed := mustCatalogPermission(t, access.ActionDashboardRead, "dashboard:allowed", projectgraph.KindDashboard)
	scope := agenttools.Scope{ProjectID: "project:analytics", PrincipalID: "principal", Credential: agenttools.CredentialScope{
		Restricted: true, PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{allowed},
	}}
	catalog := credentialCatalog{base: credentialCatalogFake{}}
	hidden := agenttools.CatalogRef{ID: "dashboard:hidden", Kind: agenttools.CatalogType(projectgraph.KindDashboard)}
	if _, err := catalog.Get(t.Context(), scope, agenttools.CatalogGetRequest{Ref: hidden}); err == nil {
		t.Fatal("typed credential resolved a catalog item outside its exact target")
	}
	if _, err := catalog.List(t.Context(), scope, agenttools.CatalogListRequest{Parent: &hidden}); err == nil {
		t.Fatal("typed credential listed children of a parent outside its exact target")
	}
}

func mustCatalogPermission(t *testing.T, action access.Action, id projectgraph.ResourceID, kind projectgraph.Kind) access.PermissionPair {
	t.Helper()
	resource, err := access.NewResourceRef(id, kind)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := access.NewExactPermissionPair(action, "project:analytics", resource)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}
