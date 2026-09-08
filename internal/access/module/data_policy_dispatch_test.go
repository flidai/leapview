package module

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestDispatchAPIGenCreateDataPolicyRejectsNewCreation(t *testing.T) {
	repositoryCalled := false
	module, err := newSurface(surfaceConfig{
		Repository: func() (access.Repository, error) {
			repositoryCalled = true
			return nil, errors.New("repository must not be opened")
		},
	})
	if err != nil {
		t.Fatalf("new access surface: %v", err)
	}

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/data-policies", strings.NewReader(`{"name":"legacy"}`))
	if !module.DispatchAPIGenOperation("createDataPolicy", response, request) {
		t.Fatal("createDataPolicy was not dispatched")
	}
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusConflict, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "DATA_POLICY_AUTHORING_RESTRICTED") {
		t.Fatalf("response = %s", response.Body.String())
	}
	if repositoryCalled {
		t.Fatal("deprecated create opened the repository")
	}

	for _, operation := range []string{"listDataPolicies", "getDataPolicy", "updateDataPolicy", "deleteDataPolicy"} {
		if module.DispatchAPIGenOperation(operation, httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/data-policies", nil)) {
			t.Fatalf("unsupported compatibility operation %q was unexpectedly dispatched", operation)
		}
	}
}
