package module

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func bootstrapProjectPair(t *testing.T, projectID projectgraph.ResourceID, action access.Action) access.PermissionPair {
	t.Helper()
	pair, err := access.NewProjectPermissionPair(action, projectID)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

func typedAuthoringBootstrapRequest(t *testing.T, kind access.AuthoringSessionKind, principalID string, projectID projectgraph.ResourceID, permissions []access.PermissionPair) *http.Request {
	t.Helper()
	scope, err := access.NewAuthoringScope("instance-prod", projectID, permissions)
	if err != nil {
		t.Fatal(err)
	}
	principalKind := access.PrincipalKindUser
	clientID := access.AuthoringCLIClientID
	if kind == access.AuthoringSessionWorkload {
		principalKind = access.PrincipalKindServicePrincipal
		clientID = principalID
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/candidate-sync/plan", nil)
	request.Header.Set("Authorization", "Bearer authoring-secret")
	request = request.WithContext(WithPrincipal(request.Context(), Principal{ID: principalID, Kind: principalKind}))
	request = request.WithContext(WithAPICredential(request.Context(), access.APICredential{
		Principal: access.Principal{ID: principalID, Kind: principalKind},
		Authoring: &access.AuthoringSession{ID: "authoring-1", Kind: kind, ClientID: clientID, PrincipalID: principalID, Scope: scope},
	}))
	return request
}

func TestTypedAuthoringBootstrapRequiresExactPairClaimAndDurableAdmin(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	plan := bootstrapProjectPair(t, projectID, access.ActionDeliveryPlan)
	read := bootstrapProjectPair(t, projectID, access.ActionDeliveryRead)
	for _, tc := range []struct {
		name        string
		permissions []access.PermissionPair
		claim       projectgraph.ResourceID
		admin       bool
		want        bool
	}{
		{name: "exact pair", permissions: []access.PermissionPair{plan}, claim: projectID, admin: true, want: true},
		{name: "other action", permissions: []access.PermissionPair{read}, claim: projectID, admin: true},
		{name: "other claim", permissions: []access.PermissionPair{plan}, claim: "project_other", admin: true},
		{name: "no durable admin", permissions: []access.PermissionPair{plan}, claim: projectID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			module, err := newSurface(surfaceConfig{
				Auth:               &Auth{},
				Repository:         func() (access.Repository, error) { return browserGuardRepository{admin: tc.admin}, nil },
				AuthoringProjectID: func(context.Context) (projectgraph.ResourceID, error) { return tc.claim, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			request := typedAuthoringBootstrapRequest(t, access.AuthoringSessionHumanCLI, "author", projectID, tc.permissions)
			allowed, err := module.AuthorizeTypedAuthoringBootstrapRequest(request.Context(), request, projectID.String(), []access.PermissionPair{plan})
			if err != nil || allowed != tc.want {
				t.Fatalf("allowed=%t error=%v, want allowed=%t", allowed, err, tc.want)
			}
		})
	}
}

func TestTypedAuthoringBootstrapResolverFailureFailsClosed(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	plan := bootstrapProjectPair(t, projectID, access.ActionDeliveryPlan)
	sentinel := errors.New("claim unavailable")
	module, err := newSurface(surfaceConfig{
		Auth:               &Auth{},
		AuthoringProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "", sentinel },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := typedAuthoringBootstrapRequest(t, access.AuthoringSessionHumanCLI, "author", projectID, []access.PermissionPair{plan})
	allowed, err := module.AuthorizeTypedAuthoringBootstrapRequest(request.Context(), request, projectID.String(), []access.PermissionPair{plan})
	if allowed || !errors.Is(err, sentinel) {
		t.Fatalf("allowed=%t error=%v, want resolver error", allowed, err)
	}
}

func TestEffectiveCapabilityProjectionRejectsTypedCredentials(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	plan := bootstrapProjectPair(t, projectID, access.ActionDeliveryPlan)
	module, err := newSurface(surfaceConfig{
		Auth: &Auth{},
		CurrentEffectiveCapabilities: func(context.Context, string) ([]access.Capability, error) {
			return []access.Capability{access.CapabilityProjectAdmin}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := typedAuthoringBootstrapRequest(t, access.AuthoringSessionHumanCLI, "author", projectID, []access.PermissionPair{plan})
	if _, err := module.RequestEffectiveCapabilities(request.Context(), request, "author"); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("typed authoring capability projection error = %v, want forbidden", err)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/me/effective-capabilities", nil)
	request = request.WithContext(WithAPICredential(request.Context(), access.APICredential{Token: access.APIToken{
		ID: "typed", PrincipalID: "author", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{plan},
	}}))
	if _, err := module.RequestEffectiveCapabilities(request.Context(), request, "author"); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("typed API token capability projection error = %v, want forbidden", err)
	}
}
