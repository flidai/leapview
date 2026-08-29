package product

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	"github.com/go-chi/chi/v5"
)

func TestHandlerRequiresETagAndReturnsRedactedStatus(t *testing.T) {
	handler := testHandler(t, Status{
		Authentication: AuthenticationStatus{
			BrowserEnabled: true, Local: Availability{Available: true, Enabled: true},
			OIDC:  NamedAvailability{Available: true, Enabled: true, Provider: "corporate"},
			Azure: Availability{Available: true}, SCIM: Availability{Available: true}, ManagedBy: "deployment",
		},
		API:    APIStatus{BearerCredentials: Availability{Available: true, Enabled: true}, ServicePrincipals: Availability{Available: true, Enabled: true}, OAuth: Availability{Available: true, Enabled: true}, MCP: Availability{Available: true, Enabled: true}},
		System: SystemStatus{InstanceID: "lvinst_1", Environment: "prod", CanonicalOrigin: "https://example.test", Build: buildinfo.Identity{Version: "1.2.3"}, StorageBackend: "s3"},
	})

	get := httptest.NewRecorder()
	handler.GetSettings(get, httptest.NewRequest(http.MethodGet, "/api/v1/instance/settings", nil))
	if get.Code != http.StatusOK || get.Header().Get("ETag") != `"product-1"` {
		t.Fatalf("get settings status=%d etag=%q body=%s", get.Code, get.Header().Get("ETag"), get.Body.String())
	}
	var initial SettingsResponse
	if err := json.Unmarshal(get.Body.Bytes(), &initial); err != nil {
		t.Fatalf("decode initial settings: %v body=%s", err, get.Body.String())
	}
	if _, err := time.Parse(time.RFC3339Nano, initial.UpdatedAt); err != nil {
		t.Fatalf("initial updatedAt = %q, want RFC3339: %v", initial.UpdatedAt, err)
	}

	missing := httptest.NewRecorder()
	missingRequest := httptest.NewRequest(http.MethodPatch, "/api/v1/instance/settings", strings.NewReader(`{"displayName":"Acme"}`))
	missingRequest.Header.Set("Content-Type", "application/json")
	handler.PatchSettings(missing, missingRequest)
	if missing.Code != http.StatusPreconditionFailed {
		t.Fatalf("missing If-Match status=%d body=%s", missing.Code, missing.Body.String())
	}

	patch := httptest.NewRequest(http.MethodPatch, "/api/v1/instance/settings", strings.NewReader(`{"displayName":"Acme"}`))
	patch.Header.Set("Content-Type", "application/json")
	patch.Header.Set("If-Match", get.Header().Get("ETag"))
	patch.Header.Set("X-Request-ID", "req_patch")
	patched := httptest.NewRecorder()
	handler.PatchSettings(patched, patch)
	if patched.Code != http.StatusOK || patched.Header().Get("ETag") != `"product-2"` || !strings.Contains(patched.Body.String(), `"displayName":"Acme"`) {
		t.Fatalf("patch status=%d etag=%q body=%s", patched.Code, patched.Header().Get("ETag"), patched.Body.String())
	}
	var updated SettingsResponse
	if err := json.Unmarshal(patched.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode updated settings: %v body=%s", err, patched.Body.String())
	}
	if _, err := time.Parse(time.RFC3339Nano, updated.UpdatedAt); err != nil {
		t.Fatalf("updated updatedAt = %q, want RFC3339: %v", updated.UpdatedAt, err)
	}

	auth := httptest.NewRecorder()
	handler.GetAuthentication(auth, httptest.NewRequest(http.MethodGet, "/api/v1/instance/authentication", nil))
	for _, forbidden := range []string{"client-secret", "issuer.example", "tenant-id", "callback"} {
		if strings.Contains(auth.Body.String(), forbidden) {
			t.Fatalf("authentication status leaked %q: %s", forbidden, auth.Body.String())
		}
	}
	var status map[string]any
	if err := json.Unmarshal(auth.Body.Bytes(), &status); err != nil || status["managedBy"] != "deployment" {
		t.Fatalf("authentication status = %#v err=%v", status, err)
	}
}

func TestHandlerUploadsAndServesImmutableLogo(t *testing.T) {
	handler := testHandler(t, Status{})
	logo := testPNG(t, 16, 8)
	upload := httptest.NewRequest(http.MethodPut, "/api/v1/instance/logo", bytes.NewReader(logo))
	upload.Header.Set("If-Match", `"product-1"`)
	upload.Header.Set("Content-Type", "image/png")
	uploaded := httptest.NewRecorder()
	handler.UploadLogo(uploaded, upload)
	if uploaded.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", uploaded.Code, uploaded.Body.String())
	}
	var body SettingsResponse
	if err := json.Unmarshal(uploaded.Body.Bytes(), &body); err != nil || body.Logo == nil {
		t.Fatalf("upload response=%#v err=%v", body, err)
	}
	request := httptest.NewRequest(http.MethodGet, body.Logo.URL, nil)
	request = request.WithContext(request.Context())
	route := httptest.NewRecorder()
	request = withDigest(request, body.Logo.SHA256)
	handler.GetLogo(route, request)
	if route.Code != http.StatusOK || route.Header().Get("Cache-Control") != "private, max-age=31536000, immutable" || !bytes.Equal(route.Body.Bytes(), logo) {
		t.Fatalf("logo status=%d headers=%v bytes=%d", route.Code, route.Header(), route.Body.Len())
	}
}

func testHandler(t *testing.T, status Status) *Handler {
	t.Helper()
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service, err := NewLegacySQLite(store.SQLDB(), &memoryBlobs{values: map[string][]byte{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO principals (id, email, display_name) VALUES ('principal_admin', 'admin@example.test', 'Admin')`); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(HTTPConfig{Service: service, Status: status, CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: "principal_admin"}, true }})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func withDigest(request *http.Request, digest string) *http.Request {
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("digest", digest)
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
}
