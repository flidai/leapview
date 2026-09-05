package mcpoauth_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/http/mcpoauth"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	testIssuer   = "https://leapview.example"
	testResource = "https://leapview.example/mcp"
	testRedirect = "https://client.example/callback"
)

func TestAuthorizationCodePKCERefreshAndRevocation(t *testing.T) {
	ctx := context.Background()
	db := newTestPostgres(t)
	repo := db.repo
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Email: "user@example.com", DisplayName: "MCP User"})
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	service, err := mcpoauth.NewPostgres(db.pool, repo, mcpoauth.Config{
		IssuerURL:   testIssuer,
		ResourceURL: testResource,
		Secret:      []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatalf("new OAuth service: %v", err)
	}

	registered := registerClient(t, service)
	verifier := "abcdefghijklmnopqrstuvwxyz-._~ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])
	authorizeURL := testIssuer + "/oauth/authorize?" + url.Values{
		"response_type":         {"code"},
		"client_id":             {registered.ClientID},
		"redirect_uri":          {testRedirect},
		"scope":                 {"mcp:use offline_access"},
		"state":                 {"client-state"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {testResource},
	}.Encode()

	consentRequest := httptest.NewRequest(http.MethodGet, authorizeURL, nil)
	consent, err := service.Consent(consentRequest)
	if err != nil {
		t.Fatalf("parse consent: %v", err)
	}
	if consent.ClientID != registered.ClientID || consent.ClientName != "Test Client" || consent.Resource != testResource {
		t.Fatalf("consent = %#v", consent)
	}

	authorizeRequest := httptest.NewRequest(http.MethodPost, authorizeURL, nil)
	authorizeResponse := httptest.NewRecorder()
	service.Authorize(authorizeResponse, authorizeRequest, principal.ID, true)
	if authorizeResponse.Code != http.StatusSeeOther {
		t.Fatalf("authorize status = %d body=%s", authorizeResponse.Code, authorizeResponse.Body.String())
	}
	callback, err := url.Parse(authorizeResponse.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse callback: %v", err)
	}
	if callback.Query().Get("state") != "client-state" || callback.Query().Get("code") == "" {
		t.Fatalf("callback = %s", callback)
	}

	token := exchangeCode(t, service, registered.ClientID, callback.Query().Get("code"), verifier)
	if token.AccessToken == "" || token.RefreshToken == "" || token.TokenType != "bearer" || token.ExpiresIn <= 0 {
		t.Fatalf("token response = %#v", token)
	}
	credential, err := service.Authenticate(ctx, token.AccessToken)
	if err != nil {
		t.Fatalf("authenticate access token: %v", err)
	}
	if credential.Principal.ID != principal.ID || credential.Resource != testResource || !credential.HasScope("mcp:use") {
		t.Fatalf("credential = %#v", credential)
	}

	refreshed := refreshToken(t, service, registered.ClientID, token.RefreshToken)
	if refreshed.AccessToken == "" || refreshed.RefreshToken == "" || refreshed.RefreshToken == token.RefreshToken {
		t.Fatalf("refreshed token = %#v", refreshed)
	}
	if _, err := service.Authenticate(ctx, token.AccessToken); err == nil {
		t.Fatal("rotated access token remained valid")
	}
	assertOAuthError(t, http.StatusBadRequest, func(rec *httptest.ResponseRecorder) {
		request := formRequest("/oauth/token", url.Values{
			"grant_type":    {"refresh_token"},
			"client_id":     {registered.ClientID},
			"refresh_token": {token.RefreshToken},
			"resource":      {testResource},
		})
		service.Token(rec, request)
	})

	revokeRequest := formRequest("/oauth/revoke", url.Values{
		"client_id": {registered.ClientID},
		"token":     {refreshed.RefreshToken},
	})
	revokeResponse := httptest.NewRecorder()
	service.Revoke(revokeResponse, revokeRequest)
	if revokeResponse.Code != http.StatusOK {
		t.Fatalf("revoke status = %d body=%s", revokeResponse.Code, revokeResponse.Body.String())
	}
	if _, err := service.Authenticate(ctx, refreshed.AccessToken); err == nil {
		t.Fatal("revoked access token remained valid")
	}
}

func TestPrincipalDisableThenEnableDoesNotReviveMCPOAuthTokens(t *testing.T) {
	ctx := context.Background()
	db := newTestPostgres(t)
	repo := db.repo
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		Email: "disabled-mcp@example.com", DisplayName: "Disabled MCP User",
	})
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	service, err := mcpoauth.NewPostgres(db.pool, repo, mcpoauth.Config{
		IssuerURL: testIssuer, ResourceURL: testResource,
		Secret: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatalf("new OAuth service: %v", err)
	}

	registered := registerClient(t, service)
	verifier := "abcdefghijklmnopqrstuvwxyz-._~ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	challengeBytes := sha256.Sum256([]byte(verifier))
	authorizeURL := testIssuer + "/oauth/authorize?" + url.Values{
		"response_type": {"code"}, "client_id": {registered.ClientID},
		"redirect_uri": {testRedirect}, "scope": {"mcp:use offline_access"},
		"state": {"disable-state"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challengeBytes[:])},
		"code_challenge_method": {"S256"}, "resource": {testResource},
	}.Encode()
	authorizeResponse := httptest.NewRecorder()
	service.Authorize(authorizeResponse, httptest.NewRequest(http.MethodPost, authorizeURL, nil), principal.ID, true)
	if authorizeResponse.Code != http.StatusSeeOther {
		t.Fatalf("authorize status = %d body=%s", authorizeResponse.Code, authorizeResponse.Body.String())
	}
	callback, err := url.Parse(authorizeResponse.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	token := exchangeCode(t, service, registered.ClientID, callback.Query().Get("code"), verifier)
	if token.AccessToken == "" || token.RefreshToken == "" {
		t.Fatalf("token response = %#v", token)
	}
	if _, err := service.Authenticate(ctx, token.AccessToken); err != nil {
		t.Fatalf("authenticate before disable: %v", err)
	}

	if _, err := repo.DisableProvisionedPrincipal(ctx, principal.ID); err != nil {
		t.Fatalf("disable principal: %v", err)
	}
	if _, err := repo.EnablePrincipal(ctx, principal.ID); err != nil {
		t.Fatalf("enable principal: %v", err)
	}
	if _, err := service.Authenticate(ctx, token.AccessToken); err == nil {
		t.Fatal("pre-disable MCP access token revived after enable")
	}
	assertOAuthError(t, http.StatusBadRequest, func(rec *httptest.ResponseRecorder) {
		service.Token(rec, formRequest("/oauth/token", url.Values{
			"grant_type": {"refresh_token"}, "client_id": {registered.ClientID},
			"refresh_token": {token.RefreshToken}, "resource": {testResource},
		}))
	})
	var active int
	if err := db.pool.QueryRow(ctx, `
SELECT COUNT(*)
FROM access.oauth_session
WHERE active = true
  AND request_json->'session'->>'subject' = $1`, principal.ID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatalf("active MCP OAuth sessions after enable = %d, want 0", active)
	}
}

func TestRejectsMissingPKCEAndWrongResource(t *testing.T) {
	service := testService(t)
	registered := registerClient(t, service)
	for name, values := range map[string]url.Values{
		"missing PKCE": {
			"response_type": {"code"}, "client_id": {registered.ClientID}, "redirect_uri": {testRedirect},
			"scope": {"mcp:use"}, "resource": {testResource},
		},
		"wrong resource": {
			"response_type": {"code"}, "client_id": {registered.ClientID}, "redirect_uri": {testRedirect},
			"scope": {"mcp:use"}, "resource": {"https://other.example/mcp"},
			"code_challenge": {strings.Repeat("a", 43)}, "code_challenge_method": {"S256"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, testIssuer+"/oauth/authorize?"+values.Encode(), nil)
			if _, err := service.Consent(request); err == nil {
				t.Fatal("Consent succeeded")
			}
		})
	}
}

func TestClientIDMetadataDocumentRegistration(t *testing.T) {
	const clientID = "https://client.example/oauth/client-metadata.json"
	db := newTestPostgres(t)
	repo := db.repo
	metadataClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"client_id":"` + clientID + `","client_name":"CIMD Client","redirect_uris":["` + testRedirect + `"],"grant_types":["authorization_code","refresh_token"],"response_types":["code"],"token_endpoint_auth_method":"none","logo_uri":"https://client.example/logo.png"}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	service, err := mcpoauth.NewPostgres(db.pool, repo, mcpoauth.Config{
		IssuerURL: testIssuer, ResourceURL: testResource,
		Secret: []byte("0123456789abcdef0123456789abcdef"), ClientMetadataHTTPClient: metadataClient,
	})
	if err != nil {
		t.Fatalf("new OAuth service: %v", err)
	}
	challenge := base64.RawURLEncoding.EncodeToString(make([]byte, sha256.Size))
	request := httptest.NewRequest(http.MethodGet, testIssuer+"/oauth/authorize?"+url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {testRedirect},
		"scope": {"mcp:use"}, "resource": {testResource},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"client-state"},
	}.Encode(), nil)
	consent, err := service.Consent(request)
	if err != nil {
		t.Fatalf("resolve CIMD consent: %v", err)
	}
	if consent.ClientID != clientID || consent.ClientName != "CIMD Client" {
		t.Fatalf("consent = %#v", consent)
	}
}

func TestServicePrincipalClientCredentials(t *testing.T) {
	const accessTokenTTL = 2 * time.Second

	ctx := context.Background()
	db := newTestPostgres(t)
	repo := db.repo
	principal, err := repo.CreateServicePrincipal(ctx, access.ServicePrincipalInput{DisplayName: "MCP automation"})
	if err != nil {
		t.Fatalf("create service principal: %v", err)
	}
	secret, _, err := repo.CreateServicePrincipalSecret(ctx, principal.ID, access.ServicePrincipalSecretInput{Name: "mcp", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatalf("create service principal secret: %v", err)
	}
	service, err := mcpoauth.NewPostgres(db.pool, repo, mcpoauth.Config{
		IssuerURL: testIssuer, ResourceURL: testResource,
		Secret: []byte("0123456789abcdef0123456789abcdef"), AccessTokenTTL: accessTokenTTL,
	})
	if err != nil {
		t.Fatalf("new OAuth service: %v", err)
	}

	token := tokenRequest(t, service, formRequest("/oauth/token", url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {principal.ID},
		"client_secret": {secret},
		"scope":         {"mcp:use"},
		"resource":      {testResource},
	}))
	if token.RefreshToken != "" {
		t.Fatalf("client credentials refresh token = %q, want empty", token.RefreshToken)
	}
	credential, err := service.Authenticate(ctx, token.AccessToken)
	if err != nil {
		t.Fatalf("authenticate service token: %v", err)
	}
	if credential.Principal.ID != principal.ID || !credential.HasScope(mcpoauth.ScopeMCPUse) {
		t.Fatalf("credential = %#v", credential)
	}
	time.Sleep(accessTokenTTL + 100*time.Millisecond)
	if _, err := service.Authenticate(ctx, token.AccessToken); err == nil {
		t.Fatal("expired service token remained valid")
	}

	assertOAuthError(t, http.StatusUnauthorized, func(rec *httptest.ResponseRecorder) {
		service.Token(rec, formRequest("/oauth/token", url.Values{
			"grant_type": {"client_credentials"}, "client_id": {principal.ID},
			"client_secret": {"wrong"}, "scope": {"mcp:use"}, "resource": {testResource},
		}))
	})
}

func registerClient(t *testing.T, service *mcpoauth.Service) mcpoauth.RegistrationResponse {
	t.Helper()
	body := `{"client_name":"Test Client","redirect_uris":["` + testRedirect + `"],"grant_types":["authorization_code","refresh_token"],"response_types":["code"],"token_endpoint_auth_method":"none","logo_uri":"https://client.example/logo.png"}`
	request := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	service.Register(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("register status = %d body=%s", response.Code, response.Body.String())
	}
	var registered mcpoauth.RegistrationResponse
	if err := json.Unmarshal(response.Body.Bytes(), &registered); err != nil {
		t.Fatalf("decode registration: %v", err)
	}
	if registered.ClientID == "" || registered.TokenEndpointAuthMethod != "none" {
		t.Fatalf("registration = %#v", registered)
	}
	return registered
}

func exchangeCode(t *testing.T, service *mcpoauth.Service, clientID, code, verifier string) mcpoauth.TokenResponse {
	t.Helper()
	request := formRequest("/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"redirect_uri":  {testRedirect},
		"code_verifier": {verifier},
		"resource":      {testResource},
	})
	return tokenRequest(t, service, request)
}

func refreshToken(t *testing.T, service *mcpoauth.Service, clientID, refreshToken string) mcpoauth.TokenResponse {
	t.Helper()
	request := formRequest("/oauth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {refreshToken},
		"resource":      {testResource},
	})
	return tokenRequest(t, service, request)
}

func tokenRequest(t *testing.T, service *mcpoauth.Service, request *http.Request) mcpoauth.TokenResponse {
	t.Helper()
	response := httptest.NewRecorder()
	service.Token(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("token status = %d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("token cache headers = %v, want no-store/no-cache", response.Header())
	}
	var token mcpoauth.TokenResponse
	if err := json.Unmarshal(response.Body.Bytes(), &token); err != nil {
		t.Fatalf("decode token: %v", err)
	}
	return token
}

func formRequest(path string, values url.Values) *http.Request {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return request
}

func assertOAuthError(t *testing.T, status int, run func(*httptest.ResponseRecorder)) {
	t.Helper()
	recorder := httptest.NewRecorder()
	run(recorder)
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, status, recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || body["error"] == nil {
		t.Fatalf("OAuth error body = %s err=%v", recorder.Body.String(), err)
	}
}

func TestAdversarialProviderMetadataBodyIsBoundedAndSecretsAreNotReturned(t *testing.T) {
	db := newTestPostgres(t)
	repo := db.repo
	metadataClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(strings.Repeat("secret-provider-body", 100000)))}, nil
	})}
	service, err := mcpoauth.NewPostgres(db.pool, repo, mcpoauth.Config{IssuerURL: testIssuer, ResourceURL: testResource, Secret: []byte("0123456789abcdef0123456789abcdef"), ClientMetadataHTTPClient: metadataClient})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?client_id="+url.QueryEscape("https://client.example/metadata.json")+"&redirect_uri=https%3A%2F%2Fclient.example%2Fcb&response_type=code&scope="+mcpoauth.ScopeMCPUse+"&state=s&code_challenge=x&code_challenge_method=S256", nil)
	rec := httptest.NewRecorder()
	service.Authorize(rec, req, "", false)
	if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "secret-provider-body") {
		t.Fatalf("provider failure leaked or succeeded: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func testService(t *testing.T) *mcpoauth.Service {
	t.Helper()
	db := newTestPostgres(t)
	service, err := mcpoauth.NewPostgres(db.pool, db.repo, mcpoauth.Config{
		IssuerURL: testIssuer, ResourceURL: testResource,
		Secret: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatalf("new OAuth service: %v", err)
	}
	return service
}

type testPostgres struct {
	pool *pgxpool.Pool
	repo *accesspostgres.Repository
}

func newTestPostgres(t *testing.T) testPostgres {
	t.Helper()
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "mcpoauth_service")
	pool, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatalf("open PostgreSQL pool: %v", err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin PostgreSQL schema transaction: %v", err)
	}
	if err := accesspostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("apply access schema: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("commit access schema: %v", err)
	}
	repo, err := accesspostgres.NewAccess(pool, accesspostgres.FingerprintConfig{Key: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatalf("construct access repository: %v", err)
	}
	return testPostgres{pool: pool, repo: repo}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }
