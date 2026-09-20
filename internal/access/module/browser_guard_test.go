package module

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
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

func TestLoginErrorCodeRejectsUnknownValues(t *testing.T) {
	for _, test := range []struct {
		query string
		want  string
	}{
		{query: "invalid_credentials", want: "invalid_credentials"},
		{query: "session_expired", want: "session_expired"},
		{query: "forbidden", want: "forbidden"},
		{query: "<script>alert(1)</script>"},
	} {
		request := httptest.NewRequest(http.MethodGet, "/login?error="+url.QueryEscape(test.query), nil)
		if got := loginErrorCode(request); got != test.want {
			t.Fatalf("loginErrorCode(%q) = %q, want %q", test.query, got, test.want)
		}
	}
}

func (r browserGuardRepository) ListGroupIDsForPrincipal(context.Context, string) ([]string, error) {
	return append([]string(nil), r.groups...), r.groupErr
}

func (r browserGuardRepository) IsPlatformAdmin(context.Context, string) (bool, error) {
	return r.admin, r.err
}

func (browserGuardRepository) RecordAuditEvent(context.Context, access.AuditEventInput) error {
	return nil
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

func TestAuthenticateRedirectsUnauthenticatedBrowserNavigationToLogin(t *testing.T) {
	repository := testStore(t).repository
	auth := mustNewAuth(t, repository, AuthConfig{LocalAuth: true})
	module, err := newSurface(surfaceConfig{Repository: func() (access.Repository, error) { return repository, nil }, Auth: auth})
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/dashboards/dashboard:sales", nil)
	request.Header.Set("Accept", "text/html")
	module.Authenticate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("protected handler ran without authentication")
	})).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/login" {
		t.Fatalf("response = %d location %q, want login redirect", recorder.Code, recorder.Header().Get("Location"))
	}
	var returned bool
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == AuthReturnCookieName && cookie.Value != "" {
			returned = true
		}
		if cookie.Name == "lv_session" {
			t.Fatalf("missing session caused unexpected session cookie mutation: %#v", cookie)
		}
	}
	if !returned {
		t.Fatal("login redirect did not retain the requested browser route")
	}
}

func TestAuthenticateKeepsBearerChallengeForAPITokenOnlyNavigation(t *testing.T) {
	repository := testStore(t).repository
	auth := mustNewAuth(t, repository, AuthConfig{APITokenOnly: true})
	module, err := newSurface(surfaceConfig{Repository: func() (access.Repository, error) { return repository, nil }, Auth: auth})
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/dashboards/dashboard:sales", nil)
	request.Header.Set("Accept", "text/html")
	module.Authenticate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("protected handler ran without a bearer credential")
	})).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("Location") != "" {
		t.Fatalf("response = %d location %q, want bearer challenge", recorder.Code, recorder.Header().Get("Location"))
	}
	if got := recorder.Header().Get("WWW-Authenticate"); got != `Bearer realm="leapview"` {
		t.Fatalf("WWW-Authenticate = %q", got)
	}
}

func TestAuthMiddlewareRedirectsAnInvalidSessionToBrandedRecovery(t *testing.T) {
	repository := testStore(t).repository
	auth := mustNewAuth(t, repository, AuthConfig{LocalAuth: true})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/dashboards/dashboard:sales", nil)
	request.AddCookie(&http.Cookie{Name: "lv_session", Value: "expired-session"})
	auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("protected handler ran for an invalid session")
	})).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/login?error=session_expired" {
		t.Fatalf("expired session response = %d location %q", recorder.Code, recorder.Header().Get("Location"))
	}
	var cleared, returned bool
	for _, cookie := range recorder.Result().Cookies() {
		switch cookie.Name {
		case "lv_session":
			cleared = cookie.MaxAge == -1 && cookie.Value == ""
		case AuthReturnCookieName:
			returned = cookie.Value != ""
		}
	}
	if !cleared || !returned {
		t.Fatalf("expired session cookies cleared=%t return-target=%t", cleared, returned)
	}
}

func TestAuthenticateRedirectsExpiredBrowserNavigationAndDoesNotReplayCommands(t *testing.T) {
	repository := testStore(t).repository
	auth := mustNewAuth(t, repository, AuthConfig{LocalAuth: true})
	module, err := newSurface(surfaceConfig{Repository: func() (access.Repository, error) { return repository, nil }, Auth: auth})
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name, method string
		wantStatus   int
		wantLocation string
	}{
		{name: "navigation", method: http.MethodGet, wantStatus: http.StatusFound, wantLocation: "/login?error=session_expired"},
		{name: "command", method: http.MethodPost, wantStatus: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(test.method, "/models/model:orders/details", nil)
			request.AddCookie(&http.Cookie{Name: "lv_session", Value: "expired-session"})
			module.Authenticate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("protected handler ran for expired session")
			})).ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus || recorder.Header().Get("Location") != test.wantLocation {
				t.Fatalf("response = %d location %q", recorder.Code, recorder.Header().Get("Location"))
			}
		})
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
	if body := recorder.Body.String(); !strings.Contains(body, "administration page") || !strings.Contains(body, "Return to Insights") {
		t.Fatalf("forbidden administration recovery body = %q", body)
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

func TestRequirePlatformAdminAttenuatesScopedTokensAndHonorsRevocation(t *testing.T) {
	repository := testStore(t).repository
	principal, err := repository.UpsertPrincipal(t.Context(), access.PrincipalInput{Email: "platform@example.test", DisplayName: "Platform User"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SetPlatformRole(t.Context(), access.PlatformRoleInput{PrincipalID: principal.ID, Email: principal.Email, Role: access.PlatformRoleAdmin}); err != nil {
		t.Fatal(err)
	}
	auth := mustNewAuth(t, repository, AuthConfig{})
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
	platformSecret, platformToken, err := repository.CreateAPITokenWithMetadata(t.Context(), access.APITokenInput{PrincipalID: principal.ID, Name: "platform", Capabilities: []access.Capability{access.CapabilityPlatformAdmin}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("create platform token: %v", err)
	}
	if got := call(platformSecret); got != http.StatusNoContent {
		t.Fatalf("platform token status = %d, want 204", got)
	}
	denySecret, _, err := repository.CreateAPITokenWithMetadata(t.Context(), access.APITokenInput{PrincipalID: principal.ID, Name: "deny-all", Capabilities: []access.Capability{}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if got := call(denySecret); got != http.StatusForbidden {
		t.Fatalf("deny-all token status = %d, want 403", got)
	}
	if err := repository.RevokeAPIToken(t.Context(), platformToken.ID); err != nil {
		t.Fatal(err)
	}
	if got := call(platformSecret); got != http.StatusUnauthorized {
		t.Fatalf("revoked platform token status = %d, want 401", got)
	}
}

func TestRequirePlatformAdminIgnoresProjectSnapshotWithoutDurableRole(t *testing.T) {
	repository := testStore(t).repository
	principal, err := repository.UpsertPrincipal(t.Context(), access.PrincipalInput{Email: "project-admin@example.test"})
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
	repository := testStore(t).repository
	principal, err := repository.SetPlatformRole(t.Context(), access.PlatformRoleInput{Email: "attenuated@example.test", Role: access.PlatformRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	module, err := newSurface(surfaceConfig{
		Repository: func() (access.Repository, error) { return repository, nil },
		Auth:       mustNewAuth(t, repository, AuthConfig{}),
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
	check("session", access.APICredential{}, false)
	check("authoring", access.APICredential{Authoring: &access.AuthoringSession{}}, false)
	check("legacy nil token", access.APICredential{Token: access.APIToken{ID: "dynamic"}}, false)
	check("empty token", access.APICredential{Token: access.APIToken{ID: "empty", Capabilities: []access.Capability{}}}, false)
	check("narrow token", access.APICredential{Token: access.APIToken{ID: "narrow", Capabilities: []access.Capability{access.CapabilityResourceRead}}}, false)
	check("project admin token", access.APICredential{Token: access.APIToken{ID: "project", Capabilities: []access.Capability{access.CapabilityProjectAdmin}}}, false)
	check("platform admin token", access.APICredential{Token: access.APIToken{ID: "platform", Capabilities: []access.Capability{access.CapabilityPlatformAdmin}}}, true)

	typedCredential := access.APICredential{
		Principal: access.Principal{ID: principal.ID},
		Token: access.APIToken{
			ID:                "typed",
			PrincipalID:       principal.ID,
			PermissionProfile: access.PermissionCatalogProfile,
		},
	}
	typedRequest := httptest.NewRequest(http.MethodGet, "/api/v1/access/principals", nil)
	typedRequest = typedRequest.WithContext(WithAPICredential(typedRequest.Context(), typedCredential))
	typedContext := withInstanceAuthorization(typedRequest.Context(), instanceAuthorization{
		OperationID: "createPrincipal",
		PrincipalID: principal.ID,
		InstanceID:  "instance_test",
		Action:      access.ActionPlatformAccessManage,
	})
	typedRequest = typedRequest.WithContext(typedContext)
	if allowed, typedErr := module.RequestPlatformAdmin(typedRequest.Context(), typedRequest, principal.ID); typedErr != nil || !allowed {
		t.Fatalf("validated typed instance decision allowed = %t, error = %v", allowed, typedErr)
	}
	if allowed, mismatchErr := module.RequestPlatformAdmin(typedRequest.Context(), typedRequest, "another-principal"); mismatchErr != nil || allowed {
		t.Fatalf("mismatched typed instance decision allowed = %t, error = %v", allowed, mismatchErr)
	}
}
