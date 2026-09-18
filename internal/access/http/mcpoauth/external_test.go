package mcpoauth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access/http/mcpoauth"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

func TestExternalIssuerAuthenticatesAudienceBoundMCPJWT(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	const keyID = "mcp-test-key"
	var issuer string
	issuerServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": issuer, "jwks_uri": issuer + "/jwks",
				"authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token",
			})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: keyID, Algorithm: string(jose.RS256), Use: "sig"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	issuer = issuerServer.URL
	t.Cleanup(issuerServer.Close)

	ctx := context.Background()
	pool := postgrestest.Open(t, accesspostgres.ApplySchema)
	repo, err := accesspostgres.NewAccess(pool, accesspostgres.FingerprintConfig{Key: []byte(strings.Repeat("mcp-oauth-test-key", 2))})
	if err != nil {
		t.Fatalf("open access repository: %v", err)
	}
	external, err := mcpoauth.NewExternal(repo, mcpoauth.ExternalConfig{
		IssuerURL: issuer, ResourceURL: testResource, HTTPClient: issuerServer.Client(),
	})
	if err != nil {
		t.Fatalf("new external resource server: %v", err)
	}

	valid := signExternalToken(t, key, keyID, issuer, testResource, "mcp:use")
	credential, err := external.Authenticate(ctx, valid)
	if err != nil {
		t.Fatalf("authenticate external token: %v", err)
	}
	if credential.Principal.Email != "external@example.com" || credential.Principal.ID == "" || !credential.HasScope(mcpoauth.ScopeMCPUse) {
		t.Fatalf("credential = %#v", credential)
	}

	for name, token := range map[string]string{
		"wrong audience": signExternalToken(t, key, keyID, issuer, "https://other.example/mcp", "mcp:use"),
		"missing scope":  signExternalToken(t, key, keyID, issuer, testResource, "openid profile"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := external.Authenticate(ctx, token); err == nil {
				t.Fatal("Authenticate succeeded")
			}
		})
	}
}

func signExternalToken(t *testing.T, key *rsa.PrivateKey, keyID, issuer, audience, scope string) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: keyID}}, nil)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	now := time.Now()
	token, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer: issuer, Subject: "external-user", Audience: jwt.Audience{audience},
		IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(time.Hour)),
	}).Claims(map[string]any{
		"email": "external@example.com", "name": "External User", "scope": scope,
	}).Serialize()
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return token
}
