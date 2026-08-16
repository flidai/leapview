package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	apigenapi "github.com/flidai/leapview/internal/app/api/gen"
)

func TestGetInstanceReturnsConfiguredEnvironment(t *testing.T) {
	store := testStore(t)
	principal := testPrincipal(t, context.Background(), store, "publisher@example.com", "Publisher")
	token, _ := testScopedAPIToken(t, context.Background(), store, access.APITokenInput{PrincipalID: principal.ID, Name: "publisher", Capabilities: []access.Capability{access.CapabilityResourceEdit}})
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(nil, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultEnvironment: "prod"}))
	unauthenticated := httptest.NewRecorder()
	server.Routes().ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/v1/instance", nil))
	if unauthenticated.Code != http.StatusOK {
		t.Fatalf("unauthenticated status = %d", unauthenticated.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/instance", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var response apigenapi.InstanceResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Environment != "prod" || response.Id != "lvinst_test" || response.CanonicalOrigin != "http://localhost:8080" {
		t.Fatalf("instance response = %#v", response)
	}
}
