package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	adminmodule "github.com/flidai/leapview/internal/admin/module"
)

func TestProductAdministrationUsesGeneratedRouteDispatch(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	principal := testPlatformPrincipal(t, ctx, store, "platform-admin@example.test", "Platform Admin")
	token := testAPIToken(t, ctx, store, principal.ID, "platform-manage")
	service, err := adminmodule.NewLegacySQLiteProductService(store.SQLDB(), productAuthorizationBlobs{})
	if err != nil {
		t.Fatal(err)
	}
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, Product: service}))

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
