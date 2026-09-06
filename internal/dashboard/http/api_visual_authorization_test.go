package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

type apiVisualMetrics struct {
	fakeMetrics
	definition dashboarddefinition.Definition
	model      *semanticmodel.Model
}

func (m *apiVisualMetrics) Resolver() dashboardresolver.Resolver {
	return apiVisualResolver{resolved: dashboardresolver.Resolved{Definition: m.definition, Model: m.model}}
}

func (m *apiVisualMetrics) Pages(string) []dashboard.Page {
	return m.definition.Pages
}

type apiVisualAuthorizingMetrics struct {
	*apiVisualMetrics
	err   error
	calls int
}

func (m *apiVisualAuthorizingMetrics) AuthorizeSemanticModelProjection(context.Context, string) error {
	m.calls++
	return m.err
}

type apiVisualResolver struct {
	resolved dashboardresolver.Resolved
}

func (r apiVisualResolver) Resolve(id projectgraph.ResourceID) (dashboardresolver.Resolved, error) {
	if id.String() != r.resolved.Definition.ID {
		return dashboardresolver.Resolved{}, dashboardresolver.ErrNotFound
	}
	return r.resolved, nil
}

func newAPIVisualMetrics(protected bool) *apiVisualMetrics {
	model := &semanticmodel.Model{Name: "model"}
	if protected {
		model.AccessGrants = map[string]semanticmodel.SemanticAccessGrantSpec{"protected": {}}
	}
	return &apiVisualMetrics{
		model: model,
		definition: dashboarddefinition.Definition{
			ID: "dash", SemanticModel: "model",
			Pages: []dashboard.Page{{ID: "overview", Visuals: []dashboard.PageVisual{{ID: "visual", Kind: "visual", Visual: "visual"}}}},
			Visualizations: map[string]visualizationdefinition.Definition{
				"visual": {ID: "visual", Spec: visualizationir.VisualizationSpec{Value: &visualizationir.KPIVisualizationSpec{
					VisualizationSpecBase: visualizationir.VisualizationSpecBase{Title: "secret-spec"},
					Kind:                  "kpi", Value: visualizationir.VisualizationFieldRef{Dataset: "orders", Field: "secret-member"},
				}}},
			},
		},
	}
}

func apiVisualRequest() *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/dashboards/dash/pages/overview/visuals/visual", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("dashboard", "dash")
	routeContext.URLParams.Add("page", "overview")
	routeContext.URLParams.Add("visual", "visual")
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
}

func TestDashboardVisualAPIRequiresWholeSemanticModelProjectionAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "allowed", wantStatus: http.StatusOK},
		{name: "denied", err: errors.New("denied"), wantStatus: http.StatusNotFound},
		{name: "missing", wantStatus: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := newAPIVisualMetrics(true)
			var metrics Metrics = base
			var authorizer *apiVisualAuthorizingMetrics
			if tc.name != "missing" {
				authorizer = &apiVisualAuthorizingMetrics{apiVisualMetrics: base, err: tc.err}
				metrics = authorizer
			}
			recorder := httptest.NewRecorder()
			(Handler{Metrics: metrics}).GetDashboardVisual(recorder, apiVisualRequest())
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			if tc.name == "allowed" {
				if !strings.Contains(recorder.Body.String(), "secret-spec") || !strings.Contains(recorder.Body.String(), "secret-member") {
					t.Fatalf("authorized visual spec omitted expected metadata: %s", recorder.Body.String())
				}
			} else if strings.Contains(recorder.Body.String(), "secret-spec") || strings.Contains(recorder.Body.String(), "secret-member") {
				t.Fatalf("unauthorized visual response disclosed spec metadata: %s", recorder.Body.String())
			}
			if authorizer != nil && authorizer.calls != 1 {
				t.Fatalf("projection authorization calls = %d, want 1", authorizer.calls)
			}
		})
	}
}

func TestDashboardVisualAPILeavesUnprotectedModelUnchanged(t *testing.T) {
	recorder := httptest.NewRecorder()
	base := newAPIVisualMetrics(false)
	(Handler{Metrics: &apiVisualAuthorizingMetrics{apiVisualMetrics: base}}).GetDashboardVisual(recorder, apiVisualRequest())
	if recorder.Code != http.StatusOK {
		t.Fatalf("unprotected status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
}
