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

func (r browserGuardRepository) IsPlatformAdmin(context.Context, string) (bool, error) {
	return r.admin, r.err
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

func TestAuthenticateInstallsInjectedPrincipalInRequestContext(t *testing.T) {
	principal := Principal{ID: "principal", Kind: access.PrincipalKindUser}
	module := browserGuardModule(nil, principal, true)
	recorder := httptest.NewRecorder()
	module.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current, ok := PrincipalFromContext(r.Context())
		if !ok || current != principal {
			t.Fatalf("request principal = %#v, %t; want %#v", current, ok, principal)
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
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
	module := browserGuardModule(browserGuardRepository{admin: true}, Principal{ID: "dev", DevBypass: true}, true)
	recorder := httptest.NewRecorder()
	module.RequirePlatformAdmin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/system", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestRequirePlatformAdminAllowsDevelopmentBypassWithoutRepository(t *testing.T) {
	module := browserGuardModule(nil, LocalDeveloperPrincipal(), true)
	recorder := httptest.NewRecorder()
	module.RequirePlatformAdmin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/storage", nil))
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
	if _, err := repository.SetPlatformRole(t.Context(), access.PlatformRoleInput{PrincipalID: principal.ID, Email: principal.Email, Role: access.PlatformRoleAdmin}); err != nil {
		t.Fatal(err)
	}
	auth := NewAuth(repository, AuthConfig{})
	module, err := newSurface(surfaceConfig{
		Repository: func() (access.Repository, error) { return repository, nil },
		Auth:       auth,
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

func TestRequirePlatformAdminIgnoresProjectSnapshotWithoutDurableRole(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "access.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repository := accesssqlite.NewRepository(store.SQLDB())
	principal, err := repository.UpsertPrincipal(t.Context(), access.PrincipalInput{ID: "project-admin-only", Email: "project-admin@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	module, err := newSurface(surfaceConfig{
		Repository: func() (access.Repository, error) { return repository, nil },
		CurrentPrincipal: func(*http.Request) (Principal, bool) {
			return Principal{ID: principal.ID, Kind: access.PrincipalKindUser}, true
		},
		CurrentEffectiveCapabilities: func(context.Context, string) ([]access.Capability, error) {
			return []access.Capability{access.CapabilityProjectAdmin}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	module.RequirePlatformAdmin(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("project snapshot grant authorized platform administration")
	})).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/system", nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}

func TestRequestPlatformAdminCredentialAttenuation(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "access.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repository := accesssqlite.NewRepository(store.SQLDB())
	principal, err := repository.SetPlatformRole(t.Context(), access.PlatformRoleInput{PrincipalID: "attenuated-admin", Email: "attenuated@example.test", Role: access.PlatformRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	module, err := newSurface(surfaceConfig{
		Repository: func() (access.Repository, error) { return repository, nil },
		Auth:       NewAuth(repository, AuthConfig{}),
		CurrentPrincipal: func(*http.Request) (Principal, bool) {
			return Principal{ID: principal.ID, Kind: access.PrincipalKindUser}, true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	check := func(name string, credential access.APICredential, want bool) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/admin/system", nil)
		r = r.WithContext(WithAPICredential(r.Context(), credential))
		got, evalErr := module.RequestPlatformAdmin(r.Context(), r, principal.ID)
		if evalErr != nil {
			t.Fatalf("%s evaluation error: %v", name, evalErr)
		}
		if got != want {
			t.Fatalf("%s allowed = %t, want %t", name, got, want)
		}
	}
	check("session", access.APICredential{}, true)
	check("authoring", access.APICredential{Authoring: &access.AuthoringSession{}}, false)
	check("dynamic token", access.APICredential{Token: access.APIToken{ID: "dynamic"}}, true)
	check("empty token", access.APICredential{Token: access.APIToken{ID: "empty", Capabilities: []access.Capability{}}}, false)
	check("narrow token", access.APICredential{Token: access.APIToken{ID: "narrow", Capabilities: []access.Capability{access.CapabilityResourceRead}}}, false)
	check("project admin token", access.APICredential{Token: access.APIToken{ID: "project", Capabilities: []access.Capability{access.CapabilityProjectAdmin}}}, true)
}
