package module

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/platform"
)

type browserGuardRepository struct {
	access.Repository
	admin    bool
	err      error
	groups   []string
	groupErr error
}

func TestPrincipalIsHumanExcludesServicePrincipals(t *testing.T) {
	for _, test := range []struct {
		name      string
		principal Principal
		want      bool
	}{
		{name: "user", principal: Principal{Kind: access.PrincipalKindUser}, want: true},
		{name: "local developer", principal: Principal{DevBypass: true}, want: true},
		{name: "service principal", principal: Principal{Kind: access.PrincipalKindServicePrincipal}, want: false},
		{name: "publication", principal: Principal{Kind: access.PrincipalKindDashboardPublication}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.principal.IsHuman(); got != test.want {
				t.Fatalf("IsHuman() = %t, want %t", got, test.want)
			}
		})
	}
}

func (r browserGuardRepository) ListGroupIDsForPrincipal(context.Context, string) ([]string, error) {
	return append([]string(nil), r.groups...), r.groupErr
}

func browserGuardModule(repo access.Repository, principal Principal, ok bool) *Module {
	var projection func(context.Context, string) ([]access.Capability, error)
	if guard, isGuard := repo.(browserGuardRepository); isGuard {
		projection = func(context.Context, string) ([]access.Capability, error) {
			if guard.err != nil {
				return nil, guard.err
			}
			if guard.admin {
				return []access.Capability{access.CapabilityProjectAdmin}, nil
			}
			return nil, nil
		}
	}
	module, err := newSurface(surfaceConfig{
		Repository:                   func() (access.Repository, error) { return repo, nil },
		CurrentEffectiveCapabilities: projection,
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

func TestRequirePlatformAdminAttenuatesDynamicAndDenyAllTokensAndHonorsRevocation(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "access.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repository := accesssqlite.NewRepository(store.SQLDB())
	principal, err := repository.UpsertPrincipal(t.Context(), access.PrincipalInput{ID: "platform-user", Email: "platform@example.test", DisplayName: "Platform User"})
	if err != nil {
		t.Fatal(err)
	}
	auth := NewAuth(repository, AuthConfig{})
	module, err := newSurface(surfaceConfig{
		Repository: func() (access.Repository, error) { return repository, nil },
		Auth:       auth,
		CurrentEffectiveCapabilities: func(context.Context, string) ([]access.Capability, error) {
			return []access.Capability{access.CapabilityProjectAdmin}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := func(secret string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/admin/system", nil)
		r.Header.Set("Authorization", "Bearer "+secret)
		return r
	}
	call := func(secret string) int {
		recorder := httptest.NewRecorder()
		module.RequirePlatformAdmin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})).ServeHTTP(recorder, request(secret))
		return recorder.Code
	}
	dynamicSecret, dynamicToken, err := repository.CreateAPITokenWithMetadata(t.Context(), access.APITokenInput{PrincipalID: principal.ID, Name: "dynamic", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if got := call(dynamicSecret); got != http.StatusNoContent {
		t.Fatalf("dynamic token status = %d, want 204", got)
	}
	denySecret, _, err := repository.CreateAPITokenWithMetadata(t.Context(), access.APITokenInput{PrincipalID: principal.ID, Name: "deny-all", Capabilities: []access.Capability{}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if got := call(denySecret); got != http.StatusForbidden {
		t.Fatalf("deny-all token status = %d, want 403", got)
	}
	if err := repository.RevokeAPIToken(t.Context(), dynamicToken.ID); err != nil {
		t.Fatal(err)
	}
	if got := call(dynamicSecret); got != http.StatusUnauthorized {
		t.Fatalf("revoked dynamic token status = %d, want 401", got)
	}
}
