package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	adminmodule "github.com/flidai/leapview/internal/admin/module"
)

func TestProductAdministrationRejectsWorkspaceScopedManagePlatform(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	repository := testAccessRepository(store)
	principal := testPrincipal(t, ctx, store, "workspace-platform@example.test", "Workspace Platform", "")
	if _, err := repository.UpsertSecurableObject(ctx, access.WorkspaceObject("test"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateGrant(ctx, access.GrantInput{
		Object: access.WorkspaceObject("test"), SubjectType: access.SubjectPrincipal,
		SubjectID: principal.ID, Privilege: access.PrivilegeManagePlatform,
	}); err != nil {
		t.Fatal(err)
	}
	token, _ := testScopedAPIToken(t, ctx, store, access.APITokenInput{
		PrincipalID: principal.ID, WorkspaceID: "test", Name: "workspace-manage-platform",
		Privileges: []access.Privilege{access.PrivilegeManagePlatform},
	})
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	service, err := adminmodule.NewProductService(store.SQLDB(), productAuthorizationBlobs{})
	if err != nil {
		t.Fatal(err)
	}
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test", Product: service}))

	// This credential demonstrates the old ProtectGlobal behavior: a matching
	// workspace grant was enough even though no PlatformObject grant exists.
	broadRequest := httptest.NewRequest(http.MethodGet, "/broad-guard", nil)
	broadRequest.Header.Set("Authorization", "Bearer "+token)
	broadResponse := httptest.NewRecorder()
	server.routes.accessModule.ProtectGlobal(access.PrivilegeManagePlatform, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})(broadResponse, broadRequest)
	if broadResponse.Code != http.StatusNoContent {
		t.Fatalf("workspace-scoped credential did not exercise broad guard: status=%d body=%s", broadResponse.Code, broadResponse.Body.String())
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/instance/settings", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	server.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("workspace-scoped MANAGE_PLATFORM reached product settings: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestProductAdministrationUsesGeneratedRouteDispatch(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	repository := testAccessRepository(store)
	principal := testPrincipal(t, ctx, store, "platform-admin@example.test", "Platform Admin", "")
	if _, err := repository.CreateGrant(ctx, access.GrantInput{
		Object: access.PlatformObject(), SubjectType: access.SubjectPrincipal,
		SubjectID: principal.ID, Privilege: access.PrivilegeManagePlatform,
	}); err != nil {
		t.Fatal(err)
	}
	token, _ := testScopedAPIToken(t, ctx, store, access.APITokenInput{
		PrincipalID: principal.ID, Name: "platform-manage", Privileges: []access.Privilege{access.PrivilegeManagePlatform},
	})
	service, err := adminmodule.NewProductService(store.SQLDB(), productAuthorizationBlobs{})
	if err != nil {
		t.Fatal(err)
	}
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test", Product: service}))

	request := httptest.NewRequest(http.MethodGet, "/api/v1/instance/settings", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	server.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"product-1"` {
		t.Fatalf("generated product route status=%d etag=%q body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}

	patch := httptest.NewRequest(http.MethodPatch, "/api/v1/instance/settings", strings.NewReader(`{"displayName":"Acme Analytics"}`))
	patch.Header.Set("Authorization", "Bearer "+token)
	patch.Header.Set("Content-Type", "application/json")
	patch.Header.Set("If-Match", response.Header().Get("ETag"))
	patch.Header.Set("X-Request-ID", "req_product_patch")
	patched := httptest.NewRecorder()
	server.Routes().ServeHTTP(patched, patch)
	if patched.Code != http.StatusOK || patched.Header().Get("ETag") != `"product-2"` || !strings.Contains(patched.Body.String(), `"displayName":"Acme Analytics"`) {
		t.Fatalf("generated product patch status=%d etag=%q body=%s", patched.Code, patched.Header().Get("ETag"), patched.Body.String())
	}
	var action, metadata string
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT action, metadata_json FROM audit_events WHERE request_id = 'req_product_patch'`).Scan(&action, &metadata); err != nil {
		t.Fatal(err)
	}
	if action != "product.identity.updated" || !strings.Contains(metadata, `"payloadSchema":"ProductIdentityUpdatedAuditPayload"`) || !strings.Contains(metadata, `"fields":["displayName"]`) {
		t.Fatalf("product command audit action=%q metadata=%s", action, metadata)
	}

	stale := httptest.NewRequest(http.MethodPatch, "/api/v1/instance/settings", strings.NewReader(`{"displayName":"Stale"}`))
	stale.Header.Set("Authorization", "Bearer "+token)
	stale.Header.Set("Content-Type", "application/json")
	stale.Header.Set("If-Match", `"invalid"`)
	staleResponse := httptest.NewRecorder()
	server.Routes().ServeHTTP(staleResponse, stale)
	if staleResponse.Code != http.StatusPreconditionFailed || !strings.Contains(staleResponse.Body.String(), `"code":"PRODUCT_IDENTITY_PRECONDITION_FAILED"`) {
		t.Fatalf("generated stale product patch status=%d body=%s", staleResponse.Code, staleResponse.Body.String())
	}
}

type productAuthorizationBlobs struct{}

func (productAuthorizationBlobs) Put(_ context.Context, expected adminmodule.ProductBlob, body io.Reader) (adminmodule.ProductBlob, error) {
	_, err := io.Copy(io.Discard, body)
	return expected, err
}

func (productAuthorizationBlobs) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, adminmodule.ErrProductLogoNotFound
}
