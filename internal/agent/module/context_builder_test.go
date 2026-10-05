package module

import (
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestBuilderContextCredentialRequiresExactDashboardUpdate(t *testing.T) {
	projectID := projectgraph.ResourceID("project_1")
	dashboardID := projectgraph.ResourceID("dashboard_1")
	resource, err := access.NewResourceRef(dashboardID, projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	read, err := access.NewExactPermissionPair(access.ActionDashboardRead, projectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	update, err := access.NewExactPermissionPair(access.ActionDashboardUpdate, projectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	scope := agent.Scope{Credential: agent.CredentialScope{Restricted: true, PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{read}}}
	allowsEdit := func(id projectgraph.ResourceID) bool {
		return CredentialAllowsResource(contextModuleScope(scope, projectID.String()), id, projectgraph.KindDashboard, access.CapabilityResourceEdit)
	}
	if allowsEdit(dashboardID) {
		t.Fatal("dashboard read credential admitted builder edit")
	}
	scope.Credential.Permissions = []access.PermissionPair{update}
	if !allowsEdit(dashboardID) || allowsEdit("dashboard_other") {
		t.Fatal("builder edit did not require the exact dashboard update pair")
	}
}

func TestResolvedBuilderTurnContextUsesAuthorizedDraftRevision(t *testing.T) {
	pageID := "overview"
	builder := uisignals.DashboardBuilderSignal{
		DashboardID: "dashboard_sales", DraftID: "draft_1", Title: "Sales",
		Revision:       uisignals.DashboardBuilderRevisionSignal{ID: "revision_7", Number: 7, ContentHash: "sha256:abc"},
		SelectedPageID: &pageID,
		Pages:          []uisignals.DashboardBuilderPageSignal{{ID: pageID, Title: "Overview"}},
	}
	candidate := agent.TurnContext{DashboardID: "dashboard_sales", DraftID: "draft_1", PageID: pageID,
		DraftRevision: &agent.DraftRevision{RevisionID: "forged", Number: 99, ContentHash: "forged"}}
	got, err := resolvedBuilderTurnContext(candidate, builder)
	if err != nil {
		t.Fatal(err)
	}
	if got.Surface != "dashboard_builder" || got.DraftID != "draft_1" || got.DraftRevision == nil || got.DraftRevision.RevisionID != "revision_7" || got.DraftRevision.Number != 7 {
		t.Fatalf("resolved context = %#v", got)
	}
	for _, invalid := range []agent.TurnContext{
		{DashboardID: "dashboard_sales", DraftID: "other_draft", PageID: pageID},
		{DashboardID: "dashboard_sales", DraftID: "draft_1", PageID: "missing_page"},
	} {
		if _, err := resolvedBuilderTurnContext(invalid, builder); err == nil {
			t.Errorf("accepted stale builder context: %#v", invalid)
		}
	}
}
