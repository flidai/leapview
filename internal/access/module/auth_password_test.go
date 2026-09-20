package module

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/jackc/pgx/v5"
)

func TestLocalPasswordPolicyFailurePreservesSession(t *testing.T) {
	store, auth, created, session := localPasswordAuthFixture(t)
	response := exerciseLocalPassword(t, auth, session, created.Password, "too-short")

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d body=%s", response.Code, http.StatusUnprocessableEntity, response.Body.String())
	}
	if _, err := store.repository.PrincipalForToken(t.Context(), session); err != nil {
		t.Fatalf("policy rejection revoked current session: %v", err)
	}
}

func TestLocalPasswordChangeExpiresCookieAndRequiresFreshSignIn(t *testing.T) {
	store, auth, created, session := localPasswordAuthFixture(t)
	response := exerciseLocalPassword(t, auth, session, created.Password, "replacement-password")

	if response.Code != http.StatusFound || response.Header().Get("Location") != "/login" {
		t.Fatalf("response = %d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	if _, err := store.repository.PrincipalForToken(t.Context(), session); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("changed password retained current session: %v", err)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "lv_session" || cookies[0].MaxAge >= 0 || !cookies[0].HttpOnly {
		t.Fatalf("expired session cookies = %#v", cookies)
	}
}

func TestLogoutAllRevokesBrowserAndDesktopSessionsAndExpiresCookie(t *testing.T) {
	store, auth, created, current := localPasswordAuthFixture(t)
	repository := store.repository
	if _, err := repository.CreateSession(t.Context(), created.Principal.ID, time.Hour); err != nil {
		t.Fatalf("create browser session: %v", err)
	}
	if _, err := repository.CreateDesktopSession(t.Context(), created.Principal.ID, "instance_0123456789abcdef0123456789abcdef", "profile_0123456789abcdef0123456789abcdef", time.Hour); err != nil {
		t.Fatalf("create desktop session: %v", err)
	}
	if _, err := store.pool.Exec(t.Context(), `
		INSERT INTO access.session(id, principal_id, token_fingerprint, verifier, expires_at, created_at)
		SELECT gen_random_uuid(), $1::uuid,
		       decode(md5(value::text) || md5('session-' || value::text), 'hex'),
		       decode(md5('verifier-' || value::text) || md5('secret-' || value::text), 'hex'),
		       clock_timestamp() + interval '1 hour',
		       clock_timestamp() - (value || ' seconds')::interval
		FROM generate_series(1, 1001) AS value`, created.Principal.ID); err != nil {
		t.Fatalf("seed sessions beyond list page: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/auth/logout-all", nil)
	request = request.WithContext(WithPrincipal(request.Context(), Principal{ID: created.Principal.ID}))
	request.AddCookie(&http.Cookie{Name: "lv_session", Value: current})
	response := httptest.NewRecorder()
	auth.LogoutAll(response, request)
	if response.Code != http.StatusFound || response.Header().Get("Location") != "/" {
		t.Fatalf("logout all response = %d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "lv_session" || cookies[0].MaxAge >= 0 || !cookies[0].HttpOnly {
		t.Fatalf("expired session cookies = %#v", cookies)
	}
	sessions, err := repository.ListSessions(t.Context(), created.Principal.ID)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	for _, session := range sessions {
		if session.RevokedAt == "" {
			t.Fatalf("session remained active: %#v", session)
		}
	}
	var active int
	if err := store.pool.QueryRow(t.Context(), `SELECT count(*) FROM access.session WHERE principal_id=$1::uuid AND revoked_at IS NULL`, created.Principal.ID).Scan(&active); err != nil {
		t.Fatalf("count active sessions: %v", err)
	}
	if active != 0 {
		t.Fatalf("active sessions after logout all = %d", active)
	}
}

func localPasswordAuthFixture(t *testing.T) (*accessTestStore, *Auth, access.LocalPasswordReset, string) {
	t.Helper()
	store := testStore(t)
	repository := store.repository
	created, err := repository.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "password@example.com", MustChange: true})
	if err != nil {
		t.Fatalf("create local user: %v", err)
	}
	session, err := repository.CreateSession(t.Context(), created.Principal.ID, time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	auth := mustNewAuth(t, repository, AuthConfig{LocalAuth: true, CSRFKey: strings.Repeat("k", 32)})
	return store, auth, created, session
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
