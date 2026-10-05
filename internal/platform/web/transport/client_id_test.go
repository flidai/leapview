package transport

import (
	"context"
	"errors"
	"github.com/flidai/leapview/pkg/pagestream"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnsureClientIDKeepsValidCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: ClientIDCookieName, Value: "existing-client"})
	recorder := httptest.NewRecorder()

	clientID, err := (ClientIDCookies{}).Ensure(recorder, req)
	if err != nil || clientID != "existing-client" {
		t.Fatalf("client id = %q, error = %v", clientID, err)
	}
	if cookies := recorder.Result().Cookies(); len(cookies) != 1 || cookies[0].Value != clientID {
		t.Fatalf("refreshed cookies: %#v", cookies)
	}
}

func TestEnsureClientIDRefreshesExistingCookieOnHTTPS(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "https://leapview.example/updates", nil)
	r.AddCookie(&http.Cookie{Name: ClientIDCookieName, Value: "existing-client"})
	w := httptest.NewRecorder()
	id, err := (ClientIDCookies{}).Ensure(w, r)
	cookies := w.Result().Cookies()
	if err != nil || id != "existing-client" || len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("id=%q error=%v cookies=%v; existing identity must get current attributes", id, err, cookies)
	}
}

func TestEnsureClientIDReplacesInvalidCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: ClientIDCookieName, Value: "invalid:client"})
	recorder := httptest.NewRecorder()

	clientID, err := (ClientIDCookies{}).Ensure(recorder, req)
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
		policy  ClientIDCookies
	}{
		{name: "direct TLS", request: httptest.NewRequest(http.MethodGet, "https://leapview.example/updates", nil)},
		{name: "configured HTTPS proxy", policy: ClientIDCookies{Secure: true}, request: func() *http.Request {
			request := httptest.NewRequest(http.MethodGet, "http://leapview.internal/updates", nil)
			request.Header.Set("X-Forwarded-Proto", "https")
			return request
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			clientID, err := test.policy.Ensure(recorder, test.request)
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

	if _, err := (ClientIDCookies{}).Ensure(recorder, request); err != nil {
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

	clientID, err := (ClientIDCookies{}).Ensure(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
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

func TestClientIDCookiePolicyIgnoresForwardedHeaders(t *testing.T) {
	for _, secure := range []bool{false, true} {
		for _, proto := range []string{"", "http", "https", "https, http"} {
			for _, existing := range []bool{false, true} {
				r := httptest.NewRequest("GET", "http://127.0.0.1/updates", nil)
				r.Header.Set("X-Forwarded-Proto", proto)
				if existing {
					r.AddCookie(&http.Cookie{Name: ClientIDCookieName, Value: "existing-client"})
				}
				w := httptest.NewRecorder()
				id, err := (ClientIDCookies{Secure: secure}).Ensure(w, r)
				cookies := w.Result().Cookies()
				if err != nil || len(cookies) != 1 {
					t.Fatalf("cookie issuance: %v %v", cookies, err)
				}
				c := cookies[0]
				if c.Secure != secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Domain != "" || c.Path != "/" || c.MaxAge != 0 || !c.Expires.IsZero() || c.Value != id || (existing && id != "existing-client") {
					t.Fatalf("secure=%v forwarded=%q existing=%v: %#v", secure, proto, existing, c)
				}
			}
		}
	}
}

func TestCookieIsIssuedBeforeSSEHeaders(t *testing.T) {
	policy := ClientIDCookies{Secure: true}
	handlers := map[string]http.HandlerFunc{
		"once": func(w http.ResponseWriter, r *http.Request) {
			_ = policy.PatchOnce(w, r, pagestream.SignalPatch{"ready": true})
		},
		"wait": func(w http.ResponseWriter, r *http.Request) {
			policy.PatchAndWait(w, r, pagestream.SignalPatch{"ready": true})
		},
		"watch": func(w http.ResponseWriter, r *http.Request) {
			policy.PatchAndWatch(w, r, pagestream.SignalPatch{"ready": true}, nil, nil)
		},
	}
	for name, handler := range handlers {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			r := httptest.NewRequest("GET", "/updates", nil).WithContext(ctx)
			r.AddCookie(&http.Cookie{Name: ClientIDCookieName, Value: "retained-client"})
			w := httptest.NewRecorder()
			handler(w, r)
			// Result observes the headers frozen at the first write, not later mutations.
			response := w.Result()
			cookies := response.Cookies()
			if len(cookies) != 1 || !cookies[0].Secure || cookies[0].Value != "retained-client" || response.Header.Get("Content-Type") != "text/event-stream" {
				t.Fatalf("headers = %v", response.Header)
			}
		})
	}
}
