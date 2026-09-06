package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	queryauthz "github.com/flidai/leapview/internal/dashboard/queryauthz"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

type apiFilterMetrics struct {
	fakeMetrics
	definition dashboarddefinition.Definition
	queries    int
}

func (m *apiFilterMetrics) Resolver() dashboardresolver.Resolver {
	return apiFilterResolver{resolved: dashboardresolver.Resolved{Definition: m.definition}}
}

func (m *apiFilterMetrics) Pages(string) []dashboard.Page {
	return m.definition.Pages
}

func (m *apiFilterMetrics) QueryCompiledFilterOptions(context.Context, string, dashboardfilter.OptionQuery) (dashboardfilter.OptionResult, error) {
	m.queries++
	return dashboardfilter.OptionResult{Complete: true}, nil
}

type apiFilterResolver struct {
	resolved dashboardresolver.Resolved
}

func (r apiFilterResolver) Resolve(id projectgraph.ResourceID) (dashboardresolver.Resolved, error) {
	if id.String() != r.resolved.Definition.ID {
		return dashboardresolver.Resolved{}, dashboardresolver.ErrNotFound
	}
	return r.resolved, nil
}

type apiFilterAuthorizingMetrics struct {
	*apiFilterMetrics
	err   error
	calls int
}

func (m *apiFilterAuthorizingMetrics) AuthorizeSemanticField(context.Context, string, string, string) error {
	m.calls++
	return m.err
}

func newAPIFilterMetrics() *apiFilterMetrics {
	return &apiFilterMetrics{definition: dashboarddefinition.Definition{
		ID:            "dash",
		SemanticModel: "model",
		FilterDefinitions: map[string]dashboardfilter.Definition{
			"state": {
				Label:     "State",
				Field:     "orders.state",
				Dataset:   "orders",
				ValueKind: dashboardfilter.ValueString,
				Options: dashboardfilter.OptionSource{
					Kind: dashboardfilter.OptionSourceStatic,
					Values: []dashboardfilter.Option{{
						Value: dashboardfilter.Value{Kind: dashboardfilter.ValueString, Value: "secret-state"},
						Label: "Secret state",
					}},
				},
			},
		},
		Pages: []dashboard.Page{{
			ID: "overview",
			FilterBindings: map[string]dashboardfilter.Binding{
				"state": {Key: "state", ID: "state", Filter: "state", Scope: dashboardfilter.ScopePage, PageID: "overview"},
			},
		}},
	}}
}

func apiFilterRequest(method, path string) *http.Request {
	request := httptest.NewRequest(method, path, nil)
	request.Header.Set("X-Serving-Snapshot", "generation-1")
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("dashboard", "dash")
	routeContext.URLParams.Add("page", "overview")
	routeContext.URLParams.Add("filter", "state")
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
}

func TestDashboardFilterAPIRequiresSemanticFieldAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		path   string
		handle func(Handler, http.ResponseWriter, *http.Request)
	}{
		{name: "filter metadata", method: http.MethodGet, path: "/api/v1/dashboards/dash/pages/overview/filters/state", handle: func(h Handler, w http.ResponseWriter, r *http.Request) { h.GetDashboardFilter(w, r) }},
		{name: "filter values", method: http.MethodPost, path: "/api/v1/dashboards/dash/pages/overview/filters/state/values", handle: func(h Handler, w http.ResponseWriter, r *http.Request) { h.ListDashboardFilterOptions(w, r) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, auth := range []struct {
				name       string
				err        error
				wantStatus int
			}{
				{name: "allowed", wantStatus: http.StatusOK},
				{name: "denied", err: errors.New("denied"), wantStatus: http.StatusNotFound},
				{name: "missing", wantStatus: http.StatusServiceUnavailable},
				{name: "missing authority", err: queryauthz.ErrSemanticConsumerAuthorityUnavailable, wantStatus: http.StatusServiceUnavailable},
			} {
				t.Run(auth.name, func(t *testing.T) {
					base := newAPIFilterMetrics()
					var metrics Metrics = base
					var authorizer *apiFilterAuthorizingMetrics
					if auth.name != "missing" {
						authorizer = &apiFilterAuthorizingMetrics{apiFilterMetrics: base, err: auth.err}
						metrics = authorizer
					}
					recorder := httptest.NewRecorder()
					tc.handle(Handler{Metrics: metrics}, recorder, apiFilterRequest(tc.method, tc.path))
					if recorder.Code != auth.wantStatus {
						t.Fatalf("status = %d, want %d; body=%s", recorder.Code, auth.wantStatus, recorder.Body.String())
					}
					body := recorder.Body.String()
					if auth.name == "allowed" {
						if !strings.Contains(body, "secret-state") || !strings.Contains(body, "Secret state") {
							t.Fatalf("authorized response omitted static option: %s", body)
						}
					} else if strings.Contains(body, "secret-state") || strings.Contains(body, "Secret state") {
						t.Fatalf("unauthorized response disclosed static option: %s", body)
					}
					if base.queries != 0 {
						t.Fatalf("static option path executed dynamic query %d times", base.queries)
					}
					if authorizer != nil && authorizer.calls != 1 {
						t.Fatalf("semantic field authorization calls = %d, want 1", authorizer.calls)
					}
				})
			}
		})
	}
}
