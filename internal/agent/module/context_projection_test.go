package module

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/dashboard"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	productsearch "github.com/flidai/leapview/internal/workspace/search"
)

type contextSearchPort struct {
	results []productsearch.Result
}

func (s contextSearchPort) SearchSubject(*http.Request) (productsearch.Subject, bool) {
	return productsearch.Subject{ID: "dev", DevBypass: true}, true
}

func (s contextSearchPort) Search(context.Context, productsearch.Subject, productsearch.Query) (productsearch.Page, error) {
	return productsearch.Page{Items: append([]productsearch.Result(nil), s.results...)}, nil
}

func (s contextSearchPort) ResolveSearchReferences(_ context.Context, _ productsearch.Subject, _ string, references []productsearch.Reference) ([]productsearch.Result, error) {
	results := make([]productsearch.Result, 0, len(references))
	for _, reference := range references {
		for _, result := range s.results {
			if result.Reference == reference {
				results = append(results, result)
				break
			}
		}
	}
	return results, nil
}

func trustedDashboardResult() productsearch.Result {
	return productsearch.Result{
		Reference: productsearch.Reference{WorkspaceID: "test", Type: productsearch.TypeDashboard, ID: "dev-dashboard"},
		Name:      "Orders dashboard", Workspace: productsearch.Workspace{ID: "test", Name: "Test"},
	}
}

func TestReferenceSignalIncludesSearchVisualSubtype(t *testing.T) {
	result := ReferenceSignal(productsearch.Result{
		Reference:  productsearch.Reference{WorkspaceID: "sales", Type: productsearch.TypeVisual, ID: "orders.revenue"},
		Name:       "Revenue",
		VisualType: "line",
		Workspace:  productsearch.Workspace{ID: "sales", Name: "Sales"},
		Locations:  []productsearch.Location{},
		Context:    []productsearch.ContextTag{},
	})
	if result.VisualType == nil || *result.VisualType != "line" {
		t.Fatalf("agent reference visual subtype = %#v", result)
	}
}

func TestResolveDashboardTurnReferencesUsesCompiledMetadata(t *testing.T) {
	page := dashboard.Page{ID: "overview", Title: "Overview", Visuals: []dashboard.PageVisual{
		{ID: "orders-chart", Kind: "visual", Visual: "orders_chart"},
		{ID: "orders-table", Kind: "visual", Visual: "orders", Title: "Recent orders"},
	}}
	resolved := ResolveDashboardTurnReferences([]agent.TurnReference{
		{Reference: agent.TurnReferenceKey{WorkspaceID: "test", Type: "visual", ID: "executive-sales.orders_chart"}, Name: "Ignore browser title", VisualType: "script", Href: "javascript:alert(1)", Hierarchy: []string{"Forged"}},
		{Reference: agent.TurnReferenceKey{WorkspaceID: "test", Type: "visual", ID: "executive-sales.orders"}, Name: "Ignore browser table title"},
		{Reference: agent.TurnReferenceKey{WorkspaceID: "test", Type: "visual", ID: "executive-sales.secret"}, Name: "Not on page"},
		{Reference: agent.TurnReferenceKey{WorkspaceID: "test", Type: "visual", ID: "other.orders_chart"}, Name: "Wrong dashboard"},
		{Reference: agent.TurnReferenceKey{WorkspaceID: "other", Type: "visual", ID: "executive-sales.orders_chart"}, Name: "Wrong workspace"},
	}, DashboardTurnReferenceContext{
		Workspace:   agent.TurnReferenceWorkspace{ID: "test", Name: "Test workspace"},
		DashboardID: "executive-sales", DashboardTitle: "Executive Sales", Page: page,
	}, map[string]visualizationdefinition.Definition{
		"orders_chart": {ID: "orders_chart", Spec: visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{VisualizationSpecBase: visualizationir.VisualizationSpecBase{Kind: "cartesian", Title: "Orders by status"}, Mark: visualizationir.VisualizationCartesianMarkBar}}},
		"secret":       {ID: "secret", Spec: visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{VisualizationSpecBase: visualizationir.VisualizationSpecBase{Kind: "cartesian", Title: "Secret"}, Mark: visualizationir.VisualizationCartesianMarkLine}}},
		"orders":       {ID: "orders", Spec: visualizationir.VisualizationSpec{Value: &visualizationir.TableVisualizationSpec{VisualizationSpecBase: visualizationir.VisualizationSpecBase{Kind: "table", Title: "Orders"}, Kind: "table"}}},
	})

	wantReference := func(id, componentID, visualID, name, visualType string) agent.TurnReference {
		href := "/workspaces/test/dashboards/executive-sales/pages/overview"
		return agent.TurnReference{
			Reference:   agent.TurnReferenceKey{WorkspaceID: "test", Type: "visual", ID: id},
			ComponentID: componentID, VisualID: visualID, Name: name, VisualType: visualType,
			Workspace: agent.TurnReferenceWorkspace{ID: "test", Name: "Test workspace"},
			Hierarchy: []string{"Test workspace", "Executive Sales", "Overview"}, Href: href,
			Locations: []agent.TurnReferenceLocation{{DashboardID: "executive-sales", DashboardName: "Executive Sales", PageID: "overview", PageName: "Overview", Href: href}},
			Context:   []string{"current_page", "current_dashboard", "current_workspace"},
		}
	}
	want := []agent.TurnReference{
		wantReference("executive-sales.orders_chart", "orders-chart", "orders_chart", "Orders by status", "bar"),
		wantReference("executive-sales.orders", "orders-table", "orders", "Recent orders", "table"),
	}
	if !reflect.DeepEqual(resolved, want) {
		t.Fatalf("resolved references = %#v, want %#v", resolved, want)
	}
}

func TestResolveChatTurnContextUsesTrustedSearchMetadata(t *testing.T) {
	module, err := Build(t.Context(), Config{
		Search: contextSearchPort{results: []productsearch.Result{trustedDashboardResult()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := module.ResolveTurnContext(httptest.NewRequest(http.MethodGet, "/chats/new", nil), agent.Scope{DevAuthBypass: true}, agent.TurnContext{
		Surface: "chat", WorkspaceID: "test",
		References: []agent.TurnReference{{
			Reference: agent.TurnReferenceKey{WorkspaceID: "test", Type: "dashboard", ID: "dev-dashboard"},
			Name:      "Untrusted browser title", ModelID: "wrong-model",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.References) != 1 {
		t.Fatalf("resolved references = %#v", resolved.References)
	}
	ref := resolved.References[0]
	if ref.Reference.Type != "dashboard" || ref.Reference.ID != "dev-dashboard" || ref.Name != "Orders dashboard" {
		t.Fatalf("resolved reference trusted browser metadata: %#v", ref)
	}
}

func TestResolveChatTurnContextRejectsNonAttachableTypes(t *testing.T) {
	module, err := Build(t.Context(), Config{
		Search: contextSearchPort{results: []productsearch.Result{trustedDashboardResult()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := module.ResolveTurnContext(httptest.NewRequest(http.MethodGet, "/chats/new", nil), agent.Scope{DevAuthBypass: true}, agent.TurnContext{
		Surface: "chat",
		References: []agent.TurnReference{
			{Reference: agent.TurnReferenceKey{WorkspaceID: "test", Type: "source", ID: "dev.orders"}},
			{Reference: agent.TurnReferenceKey{WorkspaceID: "test", Type: "dashboard", ID: "dev-dashboard"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.References) != 1 || resolved.References[0].Reference.Type != "dashboard" {
		t.Fatalf("resolved non-attachable context = %#v", resolved.References)
	}
}

func TestResolveChatTurnContextAppliesCredentialToReferenceWorkspace(t *testing.T) {
	module, err := Build(t.Context(), Config{
		Search: contextSearchPort{results: []productsearch.Result{trustedDashboardResult()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/chats/new", nil)
	candidate := agent.TurnContext{
		Surface: "chat",
		References: []agent.TurnReference{{
			Reference: agent.TurnReferenceKey{WorkspaceID: "test", Type: "dashboard", ID: "dev-dashboard"},
		}},
	}
	resolved, err := module.ResolveTurnContext(request, agent.Scope{
		DevAuthBypass: true,
		Credential: agent.CredentialScope{
			WorkspaceID: "test", Privileges: []string{string(access.PrivilegeViewItem)}, Restricted: true,
		},
	}, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.References) != 1 || resolved.WorkspaceID != "test" {
		t.Fatalf("resolved context = %#v", resolved)
	}

	_, err = module.ResolveTurnContext(request, agent.Scope{
		DevAuthBypass: true,
		Credential: agent.CredentialScope{
			WorkspaceID: "other", Privileges: []string{string(access.PrivilegeViewItem)}, Restricted: true,
		},
	}, candidate)
	if err == nil {
		t.Fatal("foreign workspace credential resolved referenced context")
	}
}

func TestResolveChatTurnContextRequiresExplicitReferenceWorkspace(t *testing.T) {
	module, err := Build(t.Context(), Config{
		Search: contextSearchPort{results: []productsearch.Result{trustedDashboardResult()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = module.ResolveTurnContext(httptest.NewRequest(http.MethodGet, "/chats/new", nil), agent.Scope{DevAuthBypass: true}, agent.TurnContext{
		Surface:     "chat",
		WorkspaceID: "test",
		References:  []agent.TurnReference{{Reference: agent.TurnReferenceKey{Type: "dashboard", ID: "dev-dashboard"}}},
	})
	if err == nil {
		t.Fatal("chat reference without workspace was accepted")
	}
}

func TestResolveTurnContextRejectsExcessReferences(t *testing.T) {
	module, err := Build(t.Context(), Config{Search: contextSearchPort{}})
	if err != nil {
		t.Fatal(err)
	}
	references := make([]agent.TurnReference, agent.MaxTurnReferences+1)
	for index := range references {
		references[index] = agent.TurnReference{
			Reference: agent.TurnReferenceKey{WorkspaceID: "test", Type: "measure", ID: "test.order_count"},
		}
	}
	_, err = module.ResolveTurnContext(httptest.NewRequest(http.MethodGet, "/chats/new", nil), agent.Scope{DevAuthBypass: true}, agent.TurnContext{
		Surface: "chat", References: references,
	})
	if err == nil {
		t.Fatal("excess references were silently truncated")
	}
}
