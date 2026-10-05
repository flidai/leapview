package transport

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnsureClientIDKeepsValidCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: ClientIDCookieName, Value: "existing-client"})
	recorder := httptest.NewRecorder()

	clientID, err := EnsureClientID(recorder, req)
	if err != nil || clientID != "existing-client" {
		t.Fatalf("client id = %q, error = %v", clientID, err)
	}
	if cookies := recorder.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("unexpected replacement cookies: %#v", cookies)
	}
}

func TestEnsureClientIDReplacesInvalidCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: ClientIDCookieName, Value: "invalid:client"})
	recorder := httptest.NewRecorder()

	clientID, err := EnsureClientID(recorder, req)
	if err != nil || len(clientID) != 32 {
		t.Fatalf("client id = %q, error = %v", clientID, err)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Value != clientID || cookies[0].Path != "/" || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Secure {
		t.Fatalf("client id cookie = %#v", cookies)
	}
}

func TestEnsureClientIDMarksCookieSecureForDirectAndProxiedHTTPS(t *testing.T) {
	for _, test := range []struct {
		name    string
		request *http.Request
	}{
		{name: "direct TLS", request: httptest.NewRequest(http.MethodGet, "https://leapview.example/updates", nil)},
		{name: "trusted HTTPS proxy", request: func() *http.Request {
			request := httptest.NewRequest(http.MethodGet, "http://leapview.internal/updates", nil)
			request.Header.Set("X-Forwarded-Proto", "https")
			return request
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			clientID, err := EnsureClientID(recorder, test.request)
			if err != nil {
				t.Fatalf("ensure client id: %v", err)
			}

			cookies := recorder.Result().Cookies()
			if len(cookies) != 1 || cookies[0].Name != ClientIDCookieName || cookies[0].Value != clientID || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].Path != "/" || cookies[0].SameSite != http.SameSiteLaxMode {
				t.Fatalf("client id cookie = %#v", cookies)
			}
		})
	}
}

func TestEnsureClientIDDoesNotAlterUnrelatedCookies(t *testing.T) {
	recorder := httptest.NewRecorder()
	http.SetCookie(recorder, &http.Cookie{Name: "unrelated", Value: "preserved", Path: "/settings", SameSite: http.SameSiteStrictMode})
	request := httptest.NewRequest(http.MethodGet, "https://leapview.example/updates", nil)

	if _, err := EnsureClientID(recorder, request); err != nil {
		t.Fatalf("ensure client id: %v", err)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 2 || cookies[0].Name != "unrelated" || cookies[0].Value != "preserved" || cookies[0].Path != "/settings" || cookies[0].SameSite != http.SameSiteStrictMode || cookies[1].Name != ClientIDCookieName {
		t.Fatalf("cookies = %#v", cookies)
	}
}

func TestEnsureClientIDReturnsEntropyFailure(t *testing.T) {
	previous := readClientIDRandom
	readClientIDRandom = func([]byte) (int, error) { return 0, errors.New("entropy unavailable") }
	defer func() { readClientIDRandom = previous }()

	clientID, err := EnsureClientID(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if err == nil || clientID != "" {
		t.Fatalf("client id = %q, error = %v", clientID, err)
	}
}

func TestClientIDFromRequestPrefersValidSuppliedThenCookieAndFailsClosed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: ClientIDCookieName, Value: "cookie-client"})

	if got := ClientIDFromRequest(req, "supplied-client"); got != "supplied-client" {
		t.Fatalf("supplied client id = %q", got)
	}
	if got := ClientIDFromRequest(req, "invalid:client"); got != "cookie-client" {
		t.Fatalf("cookie client id = %q", got)
	}
	if got := ClientIDFromRequest(httptest.NewRequest(http.MethodGet, "/", nil), ""); got != "" {
		t.Fatalf("missing client id = %q, want empty", got)
	}
}
