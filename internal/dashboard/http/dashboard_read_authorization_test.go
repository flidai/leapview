package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestDashboardReadAuthorizationFailsClosedWithoutAuthorizer(t *testing.T) {
	h := Handler{}
	err := h.authorizeDashboardRead(httptest.NewRequest(http.MethodGet, "/api/v1/dashboards/dashboard:one", nil), "dashboard:one")
	if !errors.Is(err, ErrDashboardAuthorizationUnavailable) {
		t.Fatalf("error = %v, want unavailable", err)
	}
}

func TestDashboardReadAuthorizationUsesExactDashboardResource(t *testing.T) {
	var gotPrincipal string
	var gotResource access.ResourceRef
	var gotCapability access.Capability
	h := Handler{
		CurrentPrincipalID: func(*http.Request) string { return "principal-1" },
		AuthorizeListResource: func(_ context.Context, principal string, resource access.ResourceRef, capability access.Capability) (bool, error) {
			gotPrincipal, gotResource, gotCapability = principal, resource, capability
			return true, nil
		},
	}
	if err := h.authorizeDashboardRead(httptest.NewRequest(http.MethodGet, "/api/v1/dashboards/dashboard:one", nil), "dashboard:one"); err != nil {
		t.Fatalf("error = %v", err)
	}
	want, err := access.NewResourceRef(projectgraph.ResourceID("dashboard:one"), projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	if gotPrincipal != "principal-1" || gotResource != want || gotCapability != access.CapabilityResourceRead {
		t.Fatalf("authorization call = principal %q resource %#v capability %q", gotPrincipal, gotResource, gotCapability)
	}
}

func TestDashboardReadAuthorizationReturnsForbiddenForDeniedResource(t *testing.T) {
	h := Handler{
		AuthorizeListResource: func(context.Context, string, access.ResourceRef, access.Capability) (bool, error) {
			return false, nil
		},
	}
	err := h.authorizeDashboardRead(httptest.NewRequest(http.MethodGet, "/api/v1/dashboards/dashboard:one", nil), "dashboard:one")
	if !errors.Is(err, access.ErrForbidden) || dashboardReadAuthorizationStatus(err) != http.StatusForbidden {
		t.Fatalf("error = %v status = %d, want forbidden", err, dashboardReadAuthorizationStatus(err))
	}
}
