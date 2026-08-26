package module

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	oidcauth "github.com/flidai/leapview/internal/access/oidc"
)

func TestSecureAuthCookiesUseHostPrefix(t *testing.T) {
	auth := NewAuth(nil, AuthConfig{CookieSecure: true, CSRFKey: strings.Repeat("k", 32)})

	for label, cookie := range map[string]*http.Cookie{
		"session":     auth.sessionCookie("token", authNow()),
		"expired":     auth.expiredSessionCookie(),
		"oidc state":  auth.oidcStateCookie("state", "nonce"),
		"return path": auth.authReturnCookie("/oauth/authorize"),
	} {
		if !strings.HasPrefix(cookie.Name, hostCookiePrefix) {
			t.Fatalf("%s cookie name = %q, want __Host- prefix", label, cookie.Name)
		}
		if !cookie.Secure || cookie.Path != "/" || cookie.Domain != "" {
			t.Fatalf("%s cookie does not satisfy __Host- contract: %#v", label, cookie)
		}
	}
	if auth.csrfCookie != "__Host-lv_csrf" {
		t.Fatalf("CSRF cookie name = %q", auth.csrfCookie)
	}
}

func TestDevelopmentAuthCookiesKeepUnprefixedNames(t *testing.T) {
	auth := NewAuth(nil, AuthConfig{CSRFKey: strings.Repeat("k", 32)})

	if auth.SessionCookieName() != sessionCookieName || auth.csrfCookie != csrfCookieName ||
		auth.oidcCookie != oidcStateCookieName || auth.returnCookie != authReturnCookieName {
		t.Fatalf("development cookie names = %q %q %q %q", auth.SessionCookieName(), auth.csrfCookie, auth.oidcCookie, auth.returnCookie)
	}
}

func TestLocalLoginRejectsStreamedOversizeBody(t *testing.T) {
	auth := NewAuth(nil, AuthConfig{LocalAuth: true, CSRFKey: strings.Repeat("k", 32)})
	request := httptest.NewRequest(http.MethodPost, "/auth/local/login", strings.NewReader("password="+strings.Repeat("a", int(LocalAuthMaxFormBytes))))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.ContentLength = -1
	response := httptest.NewRecorder()

	auth.LocalLogin(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d body=%s", response.Code, http.StatusRequestEntityTooLarge, response.Body.String())
	}
}

func TestOIDCErrorsDoNotExposeProviderDetails(t *testing.T) {
	auth := NewAuth(nil, AuthConfig{DevBypass: true, CSRFKey: strings.Repeat("k", 32)})
	auth.ConfigureOIDCTestClients(map[string]OIDCClient{"azureadv2": rejectingOIDCClient{}})
	stateCookie := auth.OIDCStateCookie("state", "nonce")
	request := httptest.NewRequest(http.MethodGet, "/auth/azureadv2/callback?state=state&code=code", nil)
	request.AddCookie(stateCookie)
	response := httptest.NewRecorder()

	auth.Callback(response, request)

	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "authentication failed") {
		t.Fatalf("response = %d body=%q", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "provider-secret-detail") {
		t.Fatalf("provider detail leaked in response: %q", response.Body.String())
	}
}

type rejectingOIDCClient struct{}

func (rejectingOIDCClient) AuthCodeURL(_, _ string) string { return "https://issuer.example/authorize" }

func (rejectingOIDCClient) Authenticate(context.Context, string, string) (oidcauth.Claims, error) {
	return oidcauth.Claims{}, errors.New("provider-secret-detail")
}
