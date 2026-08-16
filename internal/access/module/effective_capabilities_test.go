package module

import (
	"context"
	"errors"
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
