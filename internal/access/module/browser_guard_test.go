package module

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

type browserGuardRepository struct {
	access.Repository
	admin    bool
	err      error
	groups   []string
	groupErr error
}

func (r browserGuardRepository) IsPlatformAdmin(context.Context, string) (bool, error) {
	return r.admin, r.err
}

func (r browserGuardRepository) ListGroupIDsForPrincipal(context.Context, string) ([]string, error) {
	return append([]string(nil), r.groups...), r.groupErr
}

func browserGuardModule(repo access.Repository, principal Principal, ok bool) *Module {
	module, err := newSurface(surfaceConfig{
		Repository: func() (access.Repository, error) { return repo, nil },
		CurrentPrincipal: func(*http.Request) (Principal, bool) {
			return principal, ok
		},
	})
	if err != nil {
		panic(err)
	}
	return module
}

func TestAuthenticateRejectsMissingPrincipal(t *testing.T) {
	module := browserGuardModule(nil, Principal{}, false)
	recorder := httptest.NewRecorder()
	module.Authenticate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("authenticated handler ran")
	})).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestRequirePlatformAdminRejectsNonAdmin(t *testing.T) {
	repo := browserGuardRepository{}
	module := browserGuardModule(repo, Principal{ID: "principal"}, true)
	recorder := httptest.NewRecorder()
	module.RequirePlatformAdmin(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("platform handler ran")
	})).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/system", nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}

func TestRequirePlatformAdminFailsClosedWhenRepositoryUnavailable(t *testing.T) {
	module := browserGuardModule(nil, Principal{ID: "principal"}, true)
	recorder := httptest.NewRecorder()
	module.RequirePlatformAdmin(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("platform handler ran")
	})).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/system", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}

func TestRequirePlatformAdminFailsClosedWhenRoleCheckErrors(t *testing.T) {
	repo := browserGuardRepository{err: errors.New("role lookup failed")}
	module := browserGuardModule(repo, Principal{ID: "principal"}, true)
	recorder := httptest.NewRecorder()
	module.RequirePlatformAdmin(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("platform handler ran")
	})).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/system", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}

func TestRequirePlatformAdminAllowsDevelopmentBypass(t *testing.T) {
	module := browserGuardModule(nil, Principal{ID: "dev", DevBypass: true}, true)
	recorder := httptest.NewRecorder()
	module.RequirePlatformAdmin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/system", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}
