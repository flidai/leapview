package module

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/flidai/leapview/internal/dashboard/catalog"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	dashboardhttp "github.com/flidai/leapview/internal/dashboard/http"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	semanticapi "github.com/flidai/leapview/internal/dashboard/semanticapi"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// listMetrics is intentionally small: list endpoints only need the catalog
// and the transport metrics contract, but the dashboard handler's shared
// interface also includes its query methods.
type listMetrics struct{}

func (listMetrics) ExecuteConsumersPage(context.Context, consumer.Request, consumer.Publisher) error {
	return nil
}
func (listMetrics) Catalog() catalog.Catalog {
	return catalog.Catalog{
		Dashboards: []catalog.Dashboard{{ID: "dashboard:one", Title: "One"}},
		Models:     []catalog.Model{{ID: "semantic_model:one", Title: "One"}},
	}
}
func (listMetrics) DefaultDashboardID() string              { return "dashboard:one" }
func (listMetrics) Resolver() dashboardresolver.Resolver    { return nil }
func (listMetrics) DefaultFilters(string) dashboard.Filters { return dashboard.Filters{} }
func (listMetrics) ModelIDForDashboard(string) string       { return "semantic_model:one" }
func (listMetrics) NormalizeVisualizationWindow(_ string, request dashboard.TableRequest) dashboard.TableRequest {
	return request
}
func (listMetrics) Pages(string) []dashboard.Page { return nil }
func (listMetrics) QueryDashboardPage(context.Context, string, string, dashboard.Filters) (dashboard.Patch, error) {
	return dashboard.Patch{}, nil
}
func (listMetrics) QueryVisualizationWindow(context.Context, string, string, dashboard.Filters, visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	return visualizationir.VisualizationEnvelope{}, nil
}
func (listMetrics) ExecuteDataQuery(context.Context, dataquery.Query) (dataquery.Result, error) {
	return dataquery.Result{}, nil
}
func (listMetrics) SemanticModel(string) (*semanticmodel.Model, bool) { return nil, false }

func TestGeneratedListEndpointsResolveModuleServingSnapshot(t *testing.T) {
	const servingSnapshot = "state-current"
	resolved := 0
	module := &Module{
		snapshot: func(context.Context) (string, error) {
			resolved++
			return servingSnapshot, nil
		},
		handler: dashboardhttp.Handler{
			Metrics:            listMetrics{},
			CurrentPrincipalID: func(*http.Request) string { return "principal-1" },
			AuthorizeListResource: func(context.Context, string, access.ResourceRef, access.Capability) (bool, error) {
				return true, nil
			},
		},
		semantic: semanticapi.Handler{
			Metrics:            listMetrics{},
			ResolveProjectID:   func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
			CurrentPrincipalID: func(*http.Request) string { return "principal-1" },
			AuthorizeListResource: func(context.Context, string, projectgraph.ResourceID, access.ResourceRef, access.Capability) (bool, error) {
				return true, nil
			},
		},
	}

	t.Run("dashboards", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/dashboards", nil)
		request.Header.Set("X-Serving-Snapshot", "state-attacker-controlled")
		recorder := httptest.NewRecorder()
		dashboardAPIGenHandler{module: module}.ListDashboards(recorder, request, dashboardgen.GenListDashboardsParams{})
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if got := request.Header.Get("X-Serving-Snapshot"); got != servingSnapshot {
			t.Fatalf("serving snapshot = %q, want %q", got, servingSnapshot)
		}
	})

	t.Run("semantic models", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/semantic-models", nil)
		request.Header.Set("X-Serving-Snapshot", "state-attacker-controlled")
		recorder := httptest.NewRecorder()
		dashboardAPIGenHandler{module: module}.ListSemanticModels(recorder, request, dashboardgen.GenListSemanticModelsParams{})
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if got := request.Header.Get("X-Serving-Snapshot"); got != servingSnapshot {
			t.Fatalf("serving snapshot = %q, want %q", got, servingSnapshot)
		}
	})

	if resolved != 2 {
		t.Fatalf("serving snapshot resolver calls = %d, want 2", resolved)
	}
}

func TestServingSnapshotIsResolvedByDashboardModule(t *testing.T) {
	module := &Module{snapshot: func(_ context.Context) (string, error) {
		return "state-current", nil
	}}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/sales/semantic-models/orders/query", nil)
	request.Header.Set("X-Serving-Snapshot", "state-attacker-controlled")

	module.setServingSnapshot(request, "sales")

	if got := request.Header.Get("X-Serving-Snapshot"); got != "state-current" {
		t.Fatalf("serving snapshot = %q, want module-owned state-current", got)
	}
}

func TestBuildConstructsOwnedSemanticAPIHandler(t *testing.T) {
	module, err := Build(t.Context(), Config{Semantic: SemanticConfig{
		CurrentPrincipalID: func(*http.Request) string { return "principal-1" },
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := module.SemanticAPI().CurrentPrincipalID(httptest.NewRequest(http.MethodGet, "/", nil)); got != "principal-1" {
		t.Fatalf("principal = %q", got)
	}
}
