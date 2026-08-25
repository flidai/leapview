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

func localPasswordAuthFixture(t *testing.T) (*accesssqlite.Repository, *Auth, access.LocalPasswordReset, string) {
	t.Helper()
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "access.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repository := accesssqlite.NewRepository(store.SQLDB())
	created, err := repository.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "password@example.com", MustChange: true})
	if err != nil {
		t.Fatalf("create local user: %v", err)
	}
	session, err := repository.CreateSession(t.Context(), created.Principal.ID, time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	auth := NewAuth(repository, AuthConfig{LocalAuth: true, CSRFKey: strings.Repeat("k", 32)})
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
