package module

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestAuthorizeTypedBootstrapRequestAllowsOnlyConfiguredLocalDevelopmentBearer(t *testing.T) {
	module := browserGuardModule(nil, Principal{ID: "dev", DevBypass: true}, true)
	module.auth = mustNewAuth(t, nil, AuthConfig{DevBypass: true, DevAPIToken: "local-secret"})
	pair, err := access.NewProjectPermissionPair(access.ActionDeliveryPlan, "project_demo")
	if err != nil {
		t.Fatal(err)
	}
	serve := func(token string) (bool, error) {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project_demo/connections/connection_demo/upload-sessions", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		var allowed bool
		var authorizeErr error
		module.Authenticate(http.HandlerFunc(func(_ http.ResponseWriter, authenticated *http.Request) {
			allowed, authorizeErr = module.AuthorizeTypedBootstrapRequest(authenticated.Context(), authenticated, []access.PermissionPair{pair})
		})).ServeHTTP(httptest.NewRecorder(), request)
		return allowed, authorizeErr
	}

	allowed, err := serve("local-secret")
	if err != nil || !allowed {
		t.Fatalf("configured local bearer authorization = %t, %v; want true, nil", allowed, err)
	}
	allowed, err = serve("wrong-secret")
	if err != nil || allowed {
		t.Fatalf("wrong local bearer authorization = %t, %v; want false, nil", allowed, err)
	}
}

func TestAuthorizeTypedBootstrapRequestRejectsLegacyAndOtherProjectPairs(t *testing.T) {
	module := browserGuardModule(browserGuardRepository{admin: true}, Principal{}, false)
	module.auth = &Auth{}
	required, err := access.NewProjectPermissionPair(access.ActionDeliveryPlan, "project_demo")
	if err != nil {
		t.Fatal(err)
	}
	otherProject, err := access.NewProjectPermissionPair(access.ActionDeliveryPlan, "project_other")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		token access.APIToken
		want  bool
	}{
		{name: "exact project action", token: access.APIToken{ID: "typed", PrincipalID: "admin", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{required}}, want: true},
		{name: "other project", token: access.APIToken{ID: "other", PrincipalID: "admin", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{otherProject}}},
		{name: "legacy capability", token: access.APIToken{ID: "legacy", PrincipalID: "admin", Capabilities: []access.Capability{access.CapabilityProjectAdmin}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project_demo/delivery", nil)
			request.Header.Set("Authorization", "Bearer test-secret")
			request = request.WithContext(WithPrincipal(request.Context(), Principal{ID: "admin", Kind: access.PrincipalKindUser}))
			request = request.WithContext(WithAPICredential(request.Context(), access.APICredential{
				Principal: access.Principal{ID: "admin", Kind: access.PrincipalKindUser}, Token: tc.token,
			}))
			allowed, err := module.AuthorizeTypedBootstrapRequest(request.Context(), request, []access.PermissionPair{required})
			if err != nil || allowed != tc.want {
				t.Fatalf("allowed=%t error=%v, want %t", allowed, err, tc.want)
			}
		})
	}
}
