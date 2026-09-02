package module

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access/desktopauth"
	"github.com/flidai/leapview/internal/platform"
	"github.com/go-chi/chi/v5"
)

const (
	desktopTestInstanceID = "instance_0123456789abcdef0123456789abcdef"
	desktopTestProfileID  = "profile_0123456789abcdef0123456789abcdef"
	desktopTestVerifier   = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFG"
)

func TestDesktopAuthorizationEstablishesHttpOnlySessionWithoutReturningSecret(t *testing.T) {
	fixture := newDesktopAuthTestModule(t)
	module := fixture.module
	router := chi.NewRouter()
	module.MountDesktopAuth(router)

	browserStatus := httptest.NewRecorder()
	browserStatusRequest := httptest.NewRequest(
		http.MethodGet,
		DesktopSessionStatusPath,
		nil,
	)
	browserStatusRequest.AddCookie(fixture.browserSession)
	router.ServeHTTP(browserStatus, browserStatusRequest)
	if browserStatus.Code != http.StatusUnauthorized {
		t.Fatalf(
			"ordinary browser session status = %d, want %d",
			browserStatus.Code,
			http.StatusUnauthorized,
		)
	}

	state := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopq"
	authorize := httptest.NewRecorder()
	authorizeRequest := httptest.NewRequest(http.MethodGet, desktopAuthorizePath(state), nil)
	authorizeRequest.AddCookie(fixture.browserSession)
	router.ServeHTTP(authorize, authorizeRequest)
	if authorize.Code != http.StatusFound {
		t.Fatalf("authorize status = %d, want %d: %s", authorize.Code, http.StatusFound, authorize.Body.String())
	}
	callback, err := url.Parse(authorize.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse desktop callback: %v", err)
	}
	if callback.Host != "127.0.0.1:49152" || callback.Path != "/callback" ||
		callback.Query().Get("state") != state {
		t.Fatalf("callback = %q, want exact loopback callback and state", callback.String())
	}
	code := callback.Query().Get("code")
	if code == "" {
		t.Fatal("desktop authorization code is empty")
	}

	redeemForm := url.Values{
		"client_id":     {desktopauth.DesktopClientID},
		"code":          {code},
		"code_verifier": {desktopTestVerifier},
		"instance_id":   {desktopTestInstanceID},
		"profile_id":    {desktopTestProfileID},
		"redirect_uri":  {"http://127.0.0.1:49152/callback"},
	}
	redeem := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, DesktopRedeemPath, strings.NewReader(redeemForm.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(redeem, request)
	if redeem.Code != http.StatusNoContent {
		t.Fatalf("redeem status = %d, want %d: %s", redeem.Code, http.StatusNoContent, redeem.Body.String())
	}
	if redeem.Body.Len() != 0 {
		t.Fatalf("redeem body = %q, want empty", redeem.Body.String())
	}
	cookies := redeem.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("redeem cookies = %#v, want one session cookie", cookies)
	}
	session := cookies[0]
	if session.Name != fixture.module.auth.SessionCookieName() || session.Value == "" || !session.HttpOnly ||
		!session.Secure || session.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie = %#v, want secure HttpOnly SameSite=Lax cookie", session)
	}
	if strings.Contains(redeem.Header().Get("Location"), session.Value) ||
		strings.Contains(redeem.Body.String(), session.Value) {
		t.Fatal("session secret leaked into desktop redemption response")
	}

	protected := module.Auth().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok || principal.ID != "dev" {
			t.Fatalf("authenticated desktop principal = %#v, %v; want dev", principal, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	authenticated := httptest.NewRecorder()
	authenticatedRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	authenticatedRequest.AddCookie(session)
	protected.ServeHTTP(authenticated, authenticatedRequest)
	if authenticated.Code != http.StatusNoContent {
		t.Fatalf("authenticated status = %d, want %d", authenticated.Code, http.StatusNoContent)
	}
	status := httptest.NewRecorder()
	statusRequest := httptest.NewRequest(http.MethodGet, DesktopSessionStatusPath, nil)
	statusRequest.AddCookie(session)
	router.ServeHTTP(status, statusRequest)
	if status.Code != http.StatusNoContent {
		t.Fatalf("desktop session status = %d, want %d", status.Code, http.StatusNoContent)
	}
	disconnect := httptest.NewRecorder()
	disconnectForm := url.Values{
		"instance_id": {desktopTestInstanceID},
		"profile_id":  {desktopTestProfileID},
	}
	disconnectRequest := httptest.NewRequest(
		http.MethodPost,
		DesktopDisconnectPath,
		strings.NewReader(disconnectForm.Encode()),
	)
	disconnectRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	disconnectRequest.AddCookie(session)
	router.ServeHTTP(disconnect, disconnectRequest)
	if disconnect.Code != http.StatusNoContent {
		t.Fatalf("disconnect status = %d, want %d: %s", disconnect.Code, http.StatusNoContent, disconnect.Body.String())
	}
	cleared := disconnect.Result().Cookies()
	if len(cleared) != 1 || cleared[0].Name != fixture.module.auth.SessionCookieName() || cleared[0].MaxAge != -1 {
		t.Fatalf("disconnect cookies = %#v, want expired session cookie", cleared)
	}
	revokedStatus := httptest.NewRecorder()
	revokedStatusRequest := httptest.NewRequest(http.MethodGet, DesktopSessionStatusPath, nil)
	revokedStatusRequest.AddCookie(session)
	router.ServeHTTP(revokedStatus, revokedStatusRequest)
	if revokedStatus.Code != http.StatusUnauthorized {
		t.Fatalf("revoked desktop status = %d, want %d", revokedStatus.Code, http.StatusUnauthorized)
	}

	replay := httptest.NewRecorder()
	replayRequest := httptest.NewRequest(http.MethodPost, DesktopRedeemPath, strings.NewReader(redeemForm.Encode()))
	replayRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(replay, replayRequest)
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replay status = %d, want %d", replay.Code, http.StatusUnauthorized)
	}
}

func TestDesktopRedemptionRollsBackCodeWhenSessionAuditFails(t *testing.T) {
	fixture := newDesktopAuthTestModule(t)
	router := chi.NewRouter()
	fixture.module.MountDesktopAuth(router)

	authorize := httptest.NewRecorder()
	authorizeRequest := httptest.NewRequest(
		http.MethodGet,
		desktopAuthorizePath("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopq"),
		nil,
	)
	authorizeRequest.AddCookie(fixture.browserSession)
	router.ServeHTTP(authorize, authorizeRequest)
	callback, err := url.Parse(authorize.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse desktop callback: %v", err)
	}
	redeemForm := url.Values{
		"client_id":     {desktopauth.DesktopClientID},
		"code":          {callback.Query().Get("code")},
		"code_verifier": {desktopTestVerifier},
		"instance_id":   {desktopTestInstanceID},
		"profile_id":    {desktopTestProfileID},
		"redirect_uri":  {"http://127.0.0.1:49152/callback"},
	}
	if _, err := fixture.database.ExecContext(t.Context(), `
CREATE TRIGGER reject_desktop_session_audit
BEFORE INSERT ON audit_events
WHEN NEW.action = 'desktop_session.created'
BEGIN
  SELECT RAISE(ABORT, 'forced desktop audit failure');
END
`); err != nil {
		t.Fatalf("install audit failure trigger: %v", err)
	}

	failed := httptest.NewRecorder()
	failedRequest := httptest.NewRequest(
		http.MethodPost,
		DesktopRedeemPath,
		strings.NewReader(redeemForm.Encode()),
	)
	failedRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(failed, failedRequest)
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("failed redemption status = %d, want %d", failed.Code, http.StatusInternalServerError)
	}
	if _, err := fixture.database.ExecContext(
		t.Context(),
		"DROP TRIGGER reject_desktop_session_audit",
	); err != nil {
		t.Fatalf("remove audit failure trigger: %v", err)
	}

	retry := httptest.NewRecorder()
	retryRequest := httptest.NewRequest(
		http.MethodPost,
		DesktopRedeemPath,
		strings.NewReader(redeemForm.Encode()),
	)
	retryRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(retry, retryRequest)
	if retry.Code != http.StatusNoContent {
		t.Fatalf(
			"retried redemption status = %d, want %d: %s",
			retry.Code,
			http.StatusNoContent,
			retry.Body.String(),
		)
	}
}

func TestDesktopAuthorizationRejectsOversizedOrMalformedRedemption(t *testing.T) {
	module := newDesktopAuthTestModule(t).module
	router := chi.NewRouter()
	module.MountDesktopAuth(router)

	tests := map[string]*http.Request{
		"oversized": httptest.NewRequest(
			http.MethodPost,
			DesktopRedeemPath,
			io.LimitReader(strings.NewReader(strings.Repeat("x", DesktopAuthMaxFormBytes+1)), DesktopAuthMaxFormBytes+1),
		),
		"json": httptest.NewRequest(http.MethodPost, DesktopRedeemPath, strings.NewReader(`{"code":"secret"}`)),
	}
	tests["oversized"].Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tests["json"].Header.Set("Content-Type", "application/json")
	for name, request := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestDesktopAuthorizationRejectsDuplicateOrUnknownParameters(t *testing.T) {
	fixture := newDesktopAuthTestModule(t)
	module := fixture.module
	router := chi.NewRouter()
	module.MountDesktopAuth(router)

	for name, path := range map[string]string{
		"duplicate": desktopAuthorizePath("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopq") + "&state=ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopq",
		"unknown":   desktopAuthorizePath("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopq") + "&native_capability=filesystem",
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.AddCookie(fixture.browserSession)
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestDesktopAuthorizationSurvivesExistingBrowserLoginReturn(t *testing.T) {
	auth := NewAuth(nil, AuthConfig{
		CSRFKey:      "0123456789abcdef0123456789abcdef",
		CookieSecure: true,
	})
	target := desktopAuthorizePath("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopq")
	request := httptest.NewRequest(http.MethodGet, target, nil)
	if got := authenticationReturnTarget(request); got != target {
		t.Fatalf("authentication return target = %q, want %q", got, target)
	}
	cookie := auth.AuthReturnCookie(target)
	callback := httptest.NewRequest(http.MethodGet, "/auth/provider/callback", nil)
	callback.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	if got := auth.AuthenticationRedirectTarget(recorder, callback, "/"); got != target {
		t.Fatalf("decoded authentication return target = %q, want %q", got, target)
	}
}

type desktopAuthTestFixture struct {
	module         *Module
	browserSession *http.Cookie
	database       *sql.DB
}

func newDesktopAuthTestModule(t *testing.T) desktopAuthTestFixture {
	t.Helper()
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	module, err := Build(t.Context(), Config{
		Database: store.SQLDB(), LegacySQLite: true,
		InstanceID: desktopTestInstanceID,
		PublicURL:  "https://analytics.company.com",
		Auth: AuthConfig{
			DevBypass:    true,
			CSRFKey:      "0123456789abcdef0123456789abcdef",
			CookieSecure: true,
		},
	})
	if err != nil {
		t.Fatalf("build access module: %v", err)
	}
	if err := module.SeedLocalDeveloperPlatformAdmin(t.Context()); err != nil {
		t.Fatalf("seed development principal: %v", err)
	}
	token, err := module.repositoryValue().CreateSession(t.Context(), "dev", time.Hour)
	if err != nil {
		t.Fatalf("create browser session: %v", err)
	}
	module.auth.devBypass = false
	return desktopAuthTestFixture{
		module:         module,
		browserSession: &http.Cookie{Name: module.auth.SessionCookieName(), Value: token},
		database:       store.SQLDB(),
	}
}

func desktopAuthorizePath(state string) string {
	digest := sha256.Sum256([]byte(desktopTestVerifier))
	return DesktopAuthorizePath + "?" + url.Values{
		"client_id":             {desktopauth.DesktopClientID},
		"response_type":         {"code"},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(digest[:])},
		"code_challenge_method": {"S256"},
		"state":                 {state},
		"instance_id":           {desktopTestInstanceID},
		"profile_id":            {desktopTestProfileID},
		"redirect_uri":          {"http://127.0.0.1:49152/callback"},
		"return_path":           {"/workspaces"},
	}.Encode()
}
