package module

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/platform"
)

func TestLocalPasswordPolicyFailurePreservesSession(t *testing.T) {
	repository, auth, created, session := localPasswordAuthFixture(t)
	response := exerciseLocalPassword(t, auth, session, created.Password, "too-short")

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d body=%s", response.Code, http.StatusUnprocessableEntity, response.Body.String())
	}
	if _, err := repository.PrincipalForToken(t.Context(), session); err != nil {
		t.Fatalf("policy rejection revoked current session: %v", err)
	}
}

func TestLocalPasswordChangeExpiresCookieAndRequiresFreshSignIn(t *testing.T) {
	repository, auth, created, session := localPasswordAuthFixture(t)
	response := exerciseLocalPassword(t, auth, session, created.Password, "replacement-password")

	if response.Code != http.StatusFound || response.Header().Get("Location") != "/login" {
		t.Fatalf("response = %d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	if _, err := repository.PrincipalForToken(t.Context(), session); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("changed password retained current session: %v", err)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "lv_session" || cookies[0].MaxAge >= 0 || !cookies[0].HttpOnly {
		t.Fatalf("expired session cookies = %#v", cookies)
	}
}

func TestSwitchAccountRevokesSessionAndPreservesReturnTarget(t *testing.T) {
	repository, auth, _, session := localPasswordAuthFixture(t)
	form := url.Values{"return_to": {"/dashboards/dashboard:sales/pages/overview?view=compact"}}
	request := httptest.NewRequest(http.MethodPost, "/auth/switch-account", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: "lv_session", Value: session})
	response := httptest.NewRecorder()

	auth.SwitchAccount(response, request)

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login?error=forbidden&switch=1" {
		t.Fatalf("response = %d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	if _, err := repository.PrincipalForToken(t.Context(), session); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("account switch retained current session: %v", err)
	}
	var expiredSession, returnTarget *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		switch cookie.Name {
		case auth.SessionCookieName():
			expiredSession = cookie
		case auth.returnCookie:
			returnTarget = cookie
		}
	}
	if expiredSession == nil || expiredSession.MaxAge >= 0 {
		t.Fatalf("expired session cookie = %#v", expiredSession)
	}
	if returnTarget == nil {
		t.Fatal("account switch did not preserve an authentication return cookie")
	}
	decoded, err := auth.decodeAuthReturn(returnTarget.Value, authNow())
	if err != nil || decoded != form.Get("return_to") {
		t.Fatalf("return target = %q, %v, want %q", decoded, err, form.Get("return_to"))
	}
}

func TestSwitchAccountRejectsInvalidReturnTargetWithoutRevokingSession(t *testing.T) {
	for _, target := range []string{
		"https://attacker.example/steal",
		"//attacker.example/steal",
		"/healthz",
		"://malformed",
		"/explore?filter=" + strings.Repeat("x", maxAuthReturnTargetBytes),
	} {
		t.Run(target[:min(len(target), 32)], func(t *testing.T) {
			repository, auth, _, session := localPasswordAuthFixture(t)
			form := url.Values{"return_to": {target}}
			request := httptest.NewRequest(http.MethodPost, "/auth/switch-account", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.AddCookie(&http.Cookie{Name: "lv_session", Value: session})
			response := httptest.NewRecorder()

			auth.SwitchAccount(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d body=%s", response.Code, http.StatusBadRequest, response.Body.String())
			}
			if _, err := repository.PrincipalForToken(t.Context(), session); err != nil {
				t.Fatalf("invalid return target revoked current session: %v", err)
			}
			for _, cookie := range response.Result().Cookies() {
				if cookie.Name == auth.returnCookie && cookie.MaxAge >= 0 {
					t.Fatalf("invalid target produced return cookie: %#v", cookie)
				}
			}
		})
	}
}

func TestLocalLoginConsumesPreservedReturnTarget(t *testing.T) {
	_, auth, created, _ := localAuthFixture(t, false)
	target := "/models/model:orders/details?tab=lineage"
	form := url.Values{"email": {created.Principal.Email}, "password": {created.Password}}
	request := httptest.NewRequest(http.MethodPost, "/auth/local/login", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(auth.authReturnCookie(target))
	response := httptest.NewRecorder()

	auth.LocalLogin(response, request)

	if response.Code != http.StatusFound || response.Header().Get("Location") != target {
		t.Fatalf("response = %d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	var cleared bool
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == auth.returnCookie && cookie.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("successful login did not clear the one-shot authentication return cookie")
	}
}

func localPasswordAuthFixture(t *testing.T) (*accesssqlite.Repository, *Auth, access.LocalPasswordReset, string) {
	return localAuthFixture(t, true)
}

func localAuthFixture(t *testing.T, mustChange bool) (*accesssqlite.Repository, *Auth, access.LocalPasswordReset, string) {
	t.Helper()
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "access.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repository := accesssqlite.NewRepository(store.SQLDB())
	created, err := repository.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "password@example.com", MustChange: mustChange})
	if err != nil {
		t.Fatalf("create local user: %v", err)
	}
	session, err := repository.CreateSession(t.Context(), created.Principal.ID, time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	auth := mustNewAuth(t, repository, AuthConfig{LocalAuth: true, CSRFKey: strings.Repeat("k", 32)})
	return repository, auth, created, session
}

func exerciseLocalPassword(t *testing.T, auth *Auth, session, current, replacement string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"currentPassword": {current}, "newPassword": {replacement}}
	request := httptest.NewRequest(http.MethodPost, "/auth/local/password", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: "lv_session", Value: session})
	response := httptest.NewRecorder()
	auth.LocalPassword(response, request)
	return response
}
