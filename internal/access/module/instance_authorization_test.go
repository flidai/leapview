package module

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestInitialProjectClaimRequiresExactInstanceClaimCredential(t *testing.T) {
	module := browserGuardModule(browserGuardRepository{admin: true}, Principal{ID: "admin"}, true)
	contract := APIGenOperationContract{
		OperationID: "bootstrapProjectClaim", Method: http.MethodPost, Path: "/api/v1/instance/project-claim",
		Protected: true, AuthzMode: "privilege", Action: string(access.ActionInstanceProjectClaim),
		Resolver:   string(access.TypedOperationResolverInstance),
		Extensions: map[string]any{apiGenObjectScopeExtension: "instance"},
	}
	authorizer, err := module.APIGenAuthorizer(nil, map[string]APIGenOperationContract{contract.OperationID: contract}, APIGenResourceResolvers{
		Instance: func(*http.Request) string { return "instance_demo" },
	})
	if err != nil {
		t.Fatal(err)
	}
	protected, ok := authorizer.Protect(contract.OperationID, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	if !ok {
		t.Fatal("claim operation was not protected")
	}
	serve := func(credential *access.APICredential) int {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, contract.Path, nil)
		if credential != nil {
			r.Header.Set("Authorization", "Bearer test-token")
			r = r.WithContext(WithAPICredential(r.Context(), *credential))
		}
		w := httptest.NewRecorder()
		protected.ServeHTTP(w, r)
		return w.Code
	}
	if got := serve(nil); got != http.StatusUnauthorized {
		t.Fatalf("platform-admin session status = %d, want 401", got)
	}
	other, err := access.NewInstancePermissionPair(access.ActionInstanceProjectClaim, "instance_other")
	if err != nil {
		t.Fatal(err)
	}
	credential := &access.APICredential{Principal: access.Principal{ID: "admin"}, Token: access.APIToken{
		ID: "claim_1", PrincipalID: "admin", PermissionProfile: access.PermissionCatalogProfile,
		Permissions: []access.PermissionPair{other},
	}}
	if got := serve(credential); got != http.StatusForbidden {
		t.Fatalf("wrong-instance claim status = %d, want 403", got)
	}
	claim, err := access.NewInstancePermissionPair(access.ActionInstanceProjectClaim, "instance_demo")
	if err != nil {
		t.Fatal(err)
	}
	credential.Token.Permissions = []access.PermissionPair{claim}
	if got := serve(credential); got != http.StatusNoContent {
		t.Fatalf("exact claim status = %d, want 204", got)
	}
	credential.Token.PrincipalID = "other"
	if got := serve(credential); got != http.StatusForbidden {
		t.Fatalf("different token owner status = %d, want 403", got)
	}
}

func TestProjectClaimPublisherExchangeRequiresBearerClaimCredential(t *testing.T) {
	module := browserGuardModule(browserGuardRepository{admin: true}, Principal{ID: "admin"}, true)
	contract := APIGenOperationContract{
		OperationID: "exchangeProjectClaimPublisher", Method: http.MethodPost,
		Path:      "/api/v1/projects/{project}/project-claim-publisher/exchange",
		Protected: true, AuthzMode: "privilege", Action: string(access.ActionInstanceProjectClaim),
		Resolver:   string(access.TypedOperationResolverInstance),
		Extensions: map[string]any{apiGenObjectScopeExtension: "instance"},
	}
	authorizer, err := module.APIGenAuthorizer(nil, map[string]APIGenOperationContract{contract.OperationID: contract}, APIGenResourceResolvers{
		Instance: func(*http.Request) string { return "instance_demo" },
	})
	if err != nil {
		t.Fatal(err)
	}
	protected, ok := authorizer.Protect(contract.OperationID, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	if !ok {
		t.Fatal("exchange operation was not protected")
	}
	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, contract.Path, nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("platform-admin session exchange status = %d, want 401", recorder.Code)
	}
}
