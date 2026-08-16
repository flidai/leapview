package http

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

func TestProjectIDForRequestTracksFirstBoundProject(t *testing.T) {
	var bound projectgraph.ResourceID
	handler := Handler{ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) {
		return bound, nil
	}}
	if _, err := handler.projectIDForRequest(context.Background()); err == nil {
		t.Fatal("unbound handler unexpectedly returned a project identity")
	}
	bound = "project-first"
	projectID, err := handler.projectIDForRequest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if projectID != bound {
		t.Fatalf("project = %q, want %q", projectID, bound)
	}
}

func TestProjectIDForRequestRejectsInvalidRouteProject(t *testing.T) {
	request := httptest.NewRequest("GET", "/projects/invalid%20project", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("project", "invalid project")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	if _, err := (Handler{}).projectIDForRequest(request.Context()); err == nil {
		t.Fatal("invalid route project was accepted")
	}
}

func TestCommandDashboardIDIsRouteBound(t *testing.T) {
	request := httptest.NewRequest("POST", "/dashboards/dashboard-a/commands/select?dashboard=dashboard-b", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("dashboard", "dashboard-a")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	if _, ok := commandDashboardID(request, dashboard.Signals{}); ok {
		t.Fatal("query dashboard escaped the route scope")
	}
	request = httptest.NewRequest("POST", "/dashboards/dashboard-a/commands/select", nil)
	route = chi.NewRouteContext()
	route.URLParams.Add("dashboard", "dashboard-a")
	signals := dashboard.Signals{}
	signals.Runtime.DashboardID = "dashboard-b"
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	if _, ok := commandDashboardID(request, signals); ok {
		t.Fatal("signal dashboard escaped the route scope")
	}
}

func TestCommandModelScopeIsExact(t *testing.T) {
	request := httptest.NewRequest("POST", "/dashboards/dashboard-a/commands/select?model=model-b", nil)
	if commandModelMatches(request, dashboard.Signals{}, "model-a") {
		t.Fatal("query model escaped the dashboard model scope")
	}
	signals := dashboard.Signals{}
	signals.Runtime.ModelID = "model-b"
	request = httptest.NewRequest("POST", "/dashboards/dashboard-a/commands/select", nil)
	if commandModelMatches(request, signals, "model-a") {
		t.Fatal("signal model escaped the dashboard model scope")
	}
}
