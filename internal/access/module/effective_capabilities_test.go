package module

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestRequestEffectiveCapabilitiesRejectsCrossProjectAuthoringCredential(t *testing.T) {
	module, err := newSurface(surfaceConfig{
		Auth: &Auth{},
		CurrentEffectiveCapabilities: func(context.Context, string) ([]access.Capability, error) {
			return []access.Capability{access.CapabilityResourceRead}, nil
		},
		CurrentProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return projectgraph.ResourceID("project_active"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := access.NewAuthoringScope(
		"instance-prod", projectgraph.ResourceID("project_foreign"),
		[]access.Capability{access.CapabilityResourceRead},
	)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/api/v1/capabilities", nil)
	request = request.WithContext(WithAPICredential(request.Context(), access.APICredential{
		Authoring: &access.AuthoringSession{Scope: scope},
	}))

	_, err = module.RequestEffectiveCapabilities(request.Context(), request, "principal-1")
	if !errors.Is(err, access.ErrAuthoringScopeDenied) {
		t.Fatalf("cross-project authoring credential error = %v, want ErrAuthoringScopeDenied", err)
	}
}

func TestListCurrentEffectiveCapabilitiesAttenuatesAuthoringCredential(t *testing.T) {
	module, err := newSurface(surfaceConfig{
		Auth: &Auth{},
		CurrentPrincipal: func(*http.Request) (Principal, bool) {
			return Principal{ID: "principal-1"}, true
		},
		CurrentEffectiveCapabilities: func(context.Context, string) ([]access.Capability, error) {
			return []access.Capability{access.CapabilityResourceRead, access.CapabilityResourceEdit}, nil
		},
		CurrentProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return projectgraph.ResourceID("project_active"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := access.NewAuthoringScope(
		"instance-prod", projectgraph.ResourceID("project_active"),
		[]access.Capability{access.CapabilityResourceRead},
	)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/me/effective-capabilities", nil)
	request = request.WithContext(WithAPICredential(request.Context(), access.APICredential{
		Authoring: &access.AuthoringSession{Scope: scope},
	}))
	recorder := httptest.NewRecorder()
	module.HTTP().ListCurrentEffectiveCapabilities(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var response struct {
		Capabilities []access.Capability `json:"capabilities"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Capabilities) != 1 || response.Capabilities[0] != access.CapabilityResourceRead {
		t.Fatalf("capabilities = %v, want only authoring scope intersection", response.Capabilities)
	}
}

func TestListCurrentEffectiveCapabilitiesFailsClosedForCrossProjectAuthoringCredential(t *testing.T) {
	module, err := newSurface(surfaceConfig{
		Auth: &Auth{},
		CurrentPrincipal: func(*http.Request) (Principal, bool) {
			return Principal{ID: "principal-1"}, true
		},
		CurrentEffectiveCapabilities: func(context.Context, string) ([]access.Capability, error) {
			return []access.Capability{access.CapabilityResourceRead}, nil
		},
		CurrentProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return projectgraph.ResourceID("project_active"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := access.NewAuthoringScope(
		"instance-prod", projectgraph.ResourceID("project_foreign"),
		[]access.Capability{access.CapabilityResourceRead},
	)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/me/effective-capabilities", nil)
	request = request.WithContext(WithAPICredential(request.Context(), access.APICredential{
		Authoring: &access.AuthoringSession{Scope: scope},
	}))
	recorder := httptest.NewRecorder()
	module.HTTP().ListCurrentEffectiveCapabilities(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want fail-closed %d", recorder.Code, http.StatusInternalServerError)
	}
}
