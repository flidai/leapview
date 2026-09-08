package http

import (
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestCreateDataPolicyRejectsNewCreationWithoutStorageMutation(t *testing.T) {
	repositoryCalled := false
	handler := Handler{
		Repository: func() (access.Repository, error) {
			repositoryCalled = true
			return nil, errors.New("repository must not be opened")
		},
	}
	request := httptest.NewRequest(stdhttp.MethodPost, "/api/v1/data-policies", strings.NewReader(`{"name":"legacy"}`))
	recorder := httptest.NewRecorder()

	handler.CreateDataPolicy(recorder, request)

	if recorder.Code != stdhttp.StatusConflict {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, stdhttp.StatusConflict, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "DATA_POLICY_AUTHORING_RESTRICTED") || !strings.Contains(body, "SemanticModel") || !strings.Contains(body, "activation qualification") {
		t.Fatalf("deprecation response = %s", body)
	}
	if repositoryCalled {
		t.Fatal("deprecated create opened the repository")
	}
}
