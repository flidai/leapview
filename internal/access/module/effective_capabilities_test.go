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

func TestAuthorizeAuthoringBootstrapRequestAllowsProjectAdminScopeForResourceOperation(t *testing.T) {
	module, err := newSurface(surfaceConfig{
		Auth: &Auth{},
		Repository: func() (access.Repository, error) {
			return browserGuardRepository{admin: true}, nil
		},
		CurrentPrincipal: func(*http.Request) (Principal, bool) {
			return Principal{ID: "principal-1", Kind: access.PrincipalKindUser}, true
		},
		CurrentEffectiveCapabilities: func(context.Context, string) ([]access.Capability, error) {
			return []access.Capability{access.CapabilityProjectAdmin}, nil
		},
		CurrentProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return projectgraph.ResourceID("project_active"), nil
		},
		AuthoringProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return projectgraph.ResourceID("project_active"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := access.NewAuthoringScope(
		"instance-prod", projectgraph.ResourceID("project_active"),
		[]access.Capability{access.CapabilityProjectAdmin},
	)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project_active/candidate-sync/plan", nil)
	request = request.WithContext(WithPrincipal(request.Context(), Principal{ID: "principal-1", Kind: access.PrincipalKindUser}))
	request = request.WithContext(WithAPICredential(request.Context(), access.APICredential{
		Principal: access.Principal{ID: "principal-1", Kind: access.PrincipalKindUser},
		Authoring: &access.AuthoringSession{
			ID: "authoring-1", Kind: access.AuthoringSessionHumanCLI,
			ClientID: access.AuthoringCLIClientID, PrincipalID: "principal-1", Scope: scope,
		},
	}))
	allowed, err := module.AuthorizeAuthoringBootstrapRequest(request.Context(), request, "project_active", access.CapabilityResourceEdit)
	if err != nil || !allowed {
		t.Fatalf("authoring bootstrap authorization = %t, %v; want true, nil", allowed, err)
	}
}

func TestAuthorizeAuthoringBootstrapRequestAllowsExactCredentialOnFreshTarget(t *testing.T) {
	activeErr := errors.New("no active project")
	snapshotErr := errors.New("no active authorization snapshot")
	module, err := newSurface(surfaceConfig{
		Auth: &Auth{},
		Repository: func() (access.Repository, error) {
			return browserGuardRepository{admin: true}, nil
		},
		CurrentEffectiveCapabilities: func(context.Context, string) ([]access.Capability, error) {
			return nil, snapshotErr
		},
		CurrentProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return "", activeErr
		},
		AuthoringProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return "", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := authoringBootstrapRequest(t, access.AuthoringSessionHumanCLI, "principal-1", "project_fresh", []access.Capability{access.CapabilityResourceEdit})
	allowed, err := module.AuthorizeAuthoringBootstrapRequest(request.Context(), request, "project_fresh", access.CapabilityResourceEdit)
	if err != nil || !allowed {
		t.Fatalf("fresh authoring bootstrap authorization = %t, %v; want true, nil", allowed, err)
	}
}

func TestAuthorizeAuthoringBootstrapRequestFreshTargetRequiresScopedCapability(t *testing.T) {
	module, err := newSurface(surfaceConfig{
		Auth: &Auth{},
		CurrentEffectiveCapabilities: func(context.Context, string) ([]access.Capability, error) {
			return nil, errors.New("snapshot must not be consulted for fresh target")
		},
		AuthoringProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return "", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := authoringBootstrapRequest(t, access.AuthoringSessionHumanCLI, "principal-1", "project_fresh", []access.Capability{access.CapabilityResourceRead})
	allowed, err := module.AuthorizeAuthoringBootstrapRequest(request.Context(), request, "project_fresh", access.CapabilityResourceEdit)
	if err != nil {
		t.Fatalf("fresh capability denial error = %v, want nil", err)
	}
	if allowed {
		t.Fatal("fresh authoring bootstrap accepted a capability absent from credential scope")
	}
}

func TestAuthorizeAuthoringBootstrapRequestRejectsProjectMismatchBeforeDurableLookup(t *testing.T) {
	lookups := 0
	module, err := newSurface(surfaceConfig{
		Auth: &Auth{},
		AuthoringProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			lookups++
			return "", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := authoringBootstrapRequest(t, access.AuthoringSessionHumanCLI, "principal-1", "project_foreign", []access.Capability{access.CapabilityResourceEdit})
	allowed, err := module.AuthorizeAuthoringBootstrapRequest(request.Context(), request, "project_requested", access.CapabilityResourceEdit)
	if err != nil {
		t.Fatalf("project mismatch error = %v, want nil", err)
	}
	if allowed {
		t.Fatal("project-mismatched authoring credential was accepted")
	}
	if lookups != 0 {
		t.Fatalf("durable project lookups = %d, want no lookup for mismatched credential", lookups)
	}
}

func TestAuthorizeAuthoringBootstrapRequestAllowsClaimedProjectBeforeActivation(t *testing.T) {
	module, err := newSurface(surfaceConfig{
		Auth: &Auth{},
		Repository: func() (access.Repository, error) {
			return browserGuardRepository{admin: true}, nil
		},
		CurrentProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return "", errors.New("no active project before activation")
		},
		AuthoringProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return "project_claimed", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := authoringBootstrapRequest(t, access.AuthoringSessionWorkload, "principal-1", "project_claimed", []access.Capability{access.CapabilityResourceEdit})
	request = request.WithContext(WithPrincipal(request.Context(), Principal{ID: "principal-1", Kind: access.PrincipalKindServicePrincipal}))
	allowed, err := module.AuthorizeAuthoringBootstrapRequest(request.Context(), request, "project_claimed", access.CapabilityResourceEdit)
	if err != nil || !allowed {
		t.Fatalf("claimed authoring bootstrap authorization = %t, %v; want true, nil", allowed, err)
	}
}

func TestAuthorizeAuthoringBootstrapRequestRejectsNonAdminOnFreshTarget(t *testing.T) {
	module, err := newSurface(surfaceConfig{
		Auth: &Auth{},
		Repository: func() (access.Repository, error) {
			return browserGuardRepository{admin: false}, nil
		},
		AuthoringProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return "", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := authoringBootstrapRequest(t, access.AuthoringSessionHumanCLI, "principal-1", "project_fresh", []access.Capability{access.CapabilityResourceEdit})
	allowed, err := module.AuthorizeAuthoringBootstrapRequest(request.Context(), request, "project_fresh", access.CapabilityResourceEdit)
	if allowed || err != nil {
		t.Fatalf("non-admin fresh target authorization = %t, %v; want false, nil", allowed, err)
	}
}

func TestAuthorizeAuthoringBootstrapRequestPropagatesDurableResolverErrors(t *testing.T) {
	resolverErr := errors.New("claim store unavailable")
	module, err := newSurface(surfaceConfig{
		Auth: &Auth{},
		AuthoringProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return "", resolverErr
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := authoringBootstrapRequest(t, access.AuthoringSessionHumanCLI, "principal-1", "project_fresh", []access.Capability{access.CapabilityResourceEdit})
	allowed, err := module.AuthorizeAuthoringBootstrapRequest(request.Context(), request, "project_fresh", access.CapabilityResourceEdit)
	if allowed || !errors.Is(err, resolverErr) {
		t.Fatalf("durable resolver authorization = %t, %v; want false and resolver error", allowed, err)
	}
}

func TestAuthorizeAuthoringBootstrapRequestRequiresDurableResolver(t *testing.T) {
	module, err := newSurface(surfaceConfig{Auth: &Auth{}})
	if err != nil {
		t.Fatal(err)
	}
	request := authoringBootstrapRequest(t, access.AuthoringSessionHumanCLI, "principal-1", "project_fresh", []access.Capability{access.CapabilityResourceEdit})
	allowed, err := module.AuthorizeAuthoringBootstrapRequest(request.Context(), request, "project_fresh", access.CapabilityResourceEdit)
	if allowed || err == nil {
		t.Fatalf("missing durable resolver authorization = %t, %v; want false and error", allowed, err)
	}
}

func TestAuthorizeAuthoringBootstrapRequestRejectsMalformedCredentialKind(t *testing.T) {
	module, err := newSurface(surfaceConfig{
		Auth: &Auth{},
		AuthoringProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return "", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := authoringBootstrapRequest(t, access.AuthoringSessionKind("unknown"), "principal-1", "project_fresh", []access.Capability{access.CapabilityResourceEdit})
	allowed, err := module.AuthorizeAuthoringBootstrapRequest(request.Context(), request, "project_fresh", access.CapabilityResourceEdit)
	if err != nil || allowed {
		t.Fatalf("malformed credential authorization = %t, %v; want false, nil", allowed, err)
	}
}

func authoringBootstrapRequest(t *testing.T, kind access.AuthoringSessionKind, principalID, projectID string, capabilities []access.Capability) *http.Request {
	t.Helper()
	scope, err := access.NewAuthoringScope("instance-prod", projectgraph.ResourceID(projectID), capabilities)
	if err != nil {
		t.Fatal(err)
	}
	principalKind := access.PrincipalKindUser
	clientID := access.AuthoringCLIClientID
	if kind == access.AuthoringSessionWorkload {
		principalKind = access.PrincipalKindServicePrincipal
		clientID = principalID
	}
	authoring := &access.AuthoringSession{
		ID: "authoring-1", Kind: kind, ClientID: clientID, PrincipalID: principalID, Scope: scope,
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID+"/candidate-sync/plan", nil)
	request = request.WithContext(WithPrincipal(request.Context(), Principal{ID: principalID, Kind: principalKind}))
	request = request.WithContext(WithAPICredential(request.Context(), access.APICredential{
		Principal: access.Principal{ID: principalID, Kind: principalKind}, Authoring: authoring,
	}))
	return request
}

func TestRequestEffectiveCapabilitiesRejectsExplicitEmptyAPIToken(t *testing.T) {
	module, err := newSurface(surfaceConfig{
		Auth: &Auth{},
		CurrentEffectiveCapabilities: func(context.Context, string) ([]access.Capability, error) {
			return []access.Capability{access.CapabilityResourceRead}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/me/effective-capabilities", nil)
	request = request.WithContext(WithAPICredential(request.Context(), access.APICredential{
		Token: access.APIToken{ID: "token-empty", Capabilities: []access.Capability{}},
	}))

	_, err = module.RequestEffectiveCapabilities(request.Context(), request, "principal-1")
	if !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("explicit empty API token error = %v, want ErrForbidden", err)
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
