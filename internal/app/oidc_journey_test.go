package app

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/flidai/leapview/internal/access/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// This uses a disposable TLS issuer and the real OIDC discovery, code exchange,
// signature/claim verifier, application callback and PostgreSQL session store.
// Only the issuer's TLS trust is injected; no authentication result is mocked.
func TestOIDCBrowserJourneyCreatesIsolatedDurableSessions(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "journey"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	type authorization struct{ nonce, subject, fault string }
	var mu sync.Mutex
	codes := map[string]authorization{}
	var issuer string
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "jwks_uri": issuer + "/jwks", "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "journey", Algorithm: "RS256", Use: "sig"}}})
		case "/token":
			if err := r.ParseForm(); err != nil {
				http.Error(w, "invalid form", 400)
				return
			}
			id, secret, ok := r.BasicAuth()
			if !ok || id != "journey-client" || secret != "journey-secret" || r.Form.Get("grant_type") != "authorization_code" {
				http.Error(w, "invalid client", 401)
				return
			}
			mu.Lock()
			entry, ok := codes[r.Form.Get("code")]
			delete(codes, r.Form.Get("code"))
			mu.Unlock()
			if !ok {
				http.Error(w, `{"error":"invalid_grant"}`, 400)
				return
			}
			audience := "journey-client"
			if entry.fault == "nonce" {
				entry.nonce = "wrong-nonce"
			}
			if entry.fault == "audience" {
				audience = "another-client"
			}
			token, err := jwt.Signed(signer).Claims(jwt.Claims{Issuer: issuer, Subject: entry.subject, Audience: jwt.Audience{audience}, Expiry: jwt.NewNumericDate(time.Now().Add(time.Minute))}).Claims(map[string]any{"nonce": entry.nonce, "email": entry.subject + "@example.test", "name": entry.subject}).Serialize()
			if err != nil {
				http.Error(w, "signing failed", 500)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fixture-access", "token_type": "Bearer", "id_token": token})
		default:
			http.NotFound(w, r)
		}
	}))
	issuer = provider.URL
	t.Cleanup(provider.Close)
	store := testStore(t)
	server := httptest.NewUnstartedServer(nil)
	t.Cleanup(server.Close)
	callback := "http://" + server.Listener.Addr().String() + "/auth/journey/callback"
	client, err := oidc.New(t.Context(), oidc.Config{ID: "journey", IssuerURL: issuer, ClientID: "journey-client", ClientSecret: "journey-secret", RedirectURL: callback, HTTPClient: provider.Client()})
	if err != nil {
		t.Fatal(err)
	}
	auth := testAuth(store, accessmodule.AuthConfig{LocalAuth: true, CSRFKey: rand.Text() + rand.Text()})
	auth.ConfigureOIDCTestClients(map[string]accessmodule.OIDCClient{"journey": client})
	application := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))
	server.Config.Handler = application.Routes()
	server.Start()
	var sequence int
	login := func(t *testing.T, subject, fault string) string {
		t.Helper()
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		browser := &http.Client{Jar: jar, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		begin, err := browser.Get(server.URL + "/auth/journey")
		if err != nil {
			t.Fatal(err)
		}
		begin.Body.Close()
		if begin.StatusCode != http.StatusFound {
			t.Fatalf("begin status=%d", begin.StatusCode)
		}
		redirect, err := url.Parse(begin.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		q := redirect.Query()
		if redirect.Scheme+"://"+redirect.Host != issuer || redirect.Path != "/authorize" || q.Get("redirect_uri") != callback || q.Get("client_id") != "journey-client" || q.Get("state") == "" || q.Get("nonce") == "" {
			t.Fatalf("invalid authorization redirect: %s", redirect)
		}
		sequence++
		code := fmt.Sprintf("code-%d", sequence)
		mu.Lock()
		codes[code] = authorization{nonce: q.Get("nonce"), subject: subject, fault: fault}
		mu.Unlock()
		state := q.Get("state")
		if fault == "state" {
			state = "wrong-state"
		}
		returnURL := callback + "?" + url.Values{"code": {code}, "state": {state}}.Encode()
		response, err := browser.Get(returnURL)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		token := browserSessionCookie(t, jar, server.URL)
		if fault != "" {
			if response.StatusCode != http.StatusUnauthorized || token != "" {
				t.Fatalf("%s response status=%d establishedSession=%v", fault, response.StatusCode, token != "")
			}
			return ""
		}
		if response.StatusCode != http.StatusFound || response.Header.Get("Location") != "/" || token == "" {
			t.Fatalf("successful callback status=%d location=%q establishedSession=%v", response.StatusCode, response.Header.Get("Location"), token != "")
		}
		principal, err := store.fixture.Graph.Access.PrincipalForToken(t.Context(), token)
		if err != nil || principal.Email != subject+"@example.test" {
			t.Fatalf("durable principal email=%q error=%v", principal.Email, err)
		}
		protected, err := browser.Get(server.URL + "/admin/profile")
		if err != nil {
			t.Fatal(err)
		}
		protected.Body.Close()
		if protected.StatusCode != http.StatusOK {
			t.Fatalf("OIDC browser session cannot reach its profile: status=%d", protected.StatusCode)
		}
		// The consumed state cookie prevents replay even before the issuer rejects
		// an already exchanged authorization code.
		replay, err := browser.Get(returnURL)
		if err != nil {
			t.Fatal(err)
		}
		replay.Body.Close()
		if replay.StatusCode != http.StatusUnauthorized {
			t.Fatalf("replay status=%d", replay.StatusCode)
		}
		return token
	}
	for _, fault := range []string{"state", "nonce", "audience"} {
		t.Run("reject_"+fault, func(t *testing.T) { login(t, "denied", fault) })
	}
	alice, bob := login(t, "alice", ""), login(t, "bob", "")
	if alice == bob {
		t.Fatal("distinct principals shared a session")
	}
	if err := store.fixture.Graph.Access.DeleteSession(t.Context(), alice); err != nil {
		t.Fatal(err)
	}
	if _, err := store.fixture.Graph.Access.PrincipalForToken(t.Context(), alice); err == nil {
		t.Fatal("revoked OIDC session retained authority")
	}
	if principal, err := store.fixture.Graph.Access.PrincipalForToken(t.Context(), bob); err != nil || principal.Email != "bob@example.test" {
		t.Fatalf("other principal lost authority: email=%q error=%v", principal.Email, err)
	}
}
