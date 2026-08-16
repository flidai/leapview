package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/platform"
)

func TestAPITokenCapabilityAllowlistIsEnforced(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testPrincipal(t, ctx, store, "token-owner@example.com", "Token Owner")
	token, _ := testScopedAPIToken(t, ctx, store, access.APITokenInput{
		PrincipalID:  owner.ID,
		Name:         "resource-use-only",
		Capabilities: []access.Capability{access.CapabilityResourceUse},
	})
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))

	publishesReq := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project:other/releases", strings.NewReader(`{"environment":"prod","generationId":"generation:test","projectDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","artifactDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","connections":[]}`))
	publishesReq.Header.Set("Authorization", "Bearer "+token)
	publishesReq.Header.Set("Accept", "application/json")
	publishesReq.Header.Set("Content-Type", "application/json")
	publishesReq.Header.Set("Idempotency-Key", "denied-release")
	publishesRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(publishesRec, publishesReq)
	if publishesRec.Code != http.StatusNotFound {
		t.Fatalf("project release status = %d, want concealed %d body=%s", publishesRec.Code, http.StatusNotFound, publishesRec.Body.String())
	}

	foreignProjectReq := httptest.NewRequest(http.MethodGet, "/api/v1/projects/project:other", nil)
	foreignProjectReq.Header.Set("Authorization", "Bearer "+token)
	foreignProjectReq.Header.Set("Accept", "application/json")
	foreignProjectRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(foreignProjectRec, foreignProjectReq)
	if foreignProjectRec.Code != http.StatusNotFound {
		t.Fatalf("foreign project status = %d, want concealed %d body=%s", foreignProjectRec.Code, http.StatusNotFound, foreignProjectRec.Body.String())
	}

	effectiveReq := httptest.NewRequest(http.MethodGet, "/api/v1/me/effective-capabilities", nil)
	effectiveReq.Header.Set("Authorization", "Bearer "+token)
	effectiveReq.Header.Set("Accept", "application/json")
	effectiveRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(effectiveRec, effectiveReq)
	if effectiveRec.Code != http.StatusOK {
		t.Fatalf("effective capabilities status = %d, want %d body=%s", effectiveRec.Code, http.StatusOK, effectiveRec.Body.String())
	}
	var effectiveBody struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(effectiveRec.Body.Bytes(), &effectiveBody); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if !hasString(effectiveBody.Capabilities, string(access.CapabilityResourceUse)) {
		t.Fatalf("capabilities = %#v, want resource use", effectiveBody.Capabilities)
	}
	if hasString(effectiveBody.Capabilities, string(access.CapabilityResourceRead)) {
		t.Fatalf("capabilities = %#v, token allowlist leaked resource read", effectiveBody.Capabilities)
	}
	if strings.Contains(effectiveRec.Body.String(), "privileges") {
		t.Fatalf("effective capabilities response still uses privileges vocabulary: %s", effectiveRec.Body.String())
	}

	emptyAllowlistToken, _ := testScopedAPIToken(t, ctx, store, access.APITokenInput{
		PrincipalID:  owner.ID,
		Name:         "empty-allowlist",
		Capabilities: []access.Capability{},
	})
	emptyAllowlistReq := httptest.NewRequest(http.MethodGet, "/api/v1/me/effective-capabilities", nil)
	emptyAllowlistReq.Header.Set("Authorization", "Bearer "+emptyAllowlistToken)
	emptyAllowlistReq.Header.Set("Accept", "application/json")
	emptyAllowlistRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(emptyAllowlistRec, emptyAllowlistReq)
	if emptyAllowlistRec.Code != http.StatusForbidden {
		t.Fatalf("empty allowlist status = %d, want %d body=%s", emptyAllowlistRec.Code, http.StatusForbidden, emptyAllowlistRec.Body.String())
	}
}

func TestCreateAndResetLocalPrincipalAPI(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	admin := testPlatformPrincipal(t, ctx, store, "access-admin@example.com", "Access Admin")
	token, _ := testScopedAPIToken(t, ctx, store, access.APITokenInput{
		PrincipalID: admin.ID,
		Name:        "access-admin",
	})
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/principals", strings.NewReader(`{"email":"local-user@example.com","displayName":"Local User"}`))
	createReq.Header.Set("Authorization", "Bearer "+token)
	createReq.Header.Set("Accept", "application/json")
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Idempotency-Key", "create-local-user")
	createRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create principal status = %d, want %d body=%s", createRec.Code, http.StatusCreated, createRec.Body.String())
	}
	var created struct {
		Principal         access.Principal `json:"principal"`
		TemporaryPassword string           `json:"temporaryPassword"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Principal.Email != "local-user@example.com" || created.TemporaryPassword == "" {
		t.Fatalf("created response = %#v", created)
	}
	repo := accesssqlite.NewRepository(store.SQLDB())
	if _, credential, err := repo.VerifyLocalPassword(ctx, "local-user@example.com", created.TemporaryPassword); err != nil {
		t.Fatalf("verify created temporary password: %v", err)
	} else if !credential.MustChangePassword {
		t.Fatal("created credential must_change_password = false, want true")
	}

	resetReq := httptest.NewRequest(http.MethodPost, "/api/v1/principals/"+created.Principal.ID+"/password-reset", nil)
	resetReq.Header.Set("Authorization", "Bearer "+token)
	resetReq.Header.Set("Accept", "application/json")
	resetReq.Header.Set("Idempotency-Key", "reset-local-user-password")
	resetRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(resetRec, resetReq)
	if resetRec.Code != http.StatusOK {
		t.Fatalf("reset principal status = %d, want %d body=%s", resetRec.Code, http.StatusOK, resetRec.Body.String())
	}
	var reset struct {
		Principal         access.Principal `json:"principal"`
		TemporaryPassword string           `json:"temporaryPassword"`
	}
	if err := json.Unmarshal(resetRec.Body.Bytes(), &reset); err != nil {
		t.Fatalf("decode reset response: %v", err)
	}
	if reset.Principal.ID != created.Principal.ID || reset.TemporaryPassword == "" || reset.TemporaryPassword == created.TemporaryPassword {
		t.Fatalf("reset response = %#v created password=%q", reset, created.TemporaryPassword)
	}
	if _, credential, err := repo.VerifyLocalPassword(ctx, "local-user@example.com", reset.TemporaryPassword); err != nil {
		t.Fatalf("verify reset temporary password: %v", err)
	} else if !credential.MustChangePassword {
		t.Fatal("reset credential must_change_password = false, want true")
	}
}

func TestCurrentAPITokenRevocationIsScopedToAuthenticatedPrincipal(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testPlatformPrincipal(t, ctx, store, "token-revoke-owner@example.com", "Token Owner")
	foreign := testPlatformPrincipal(t, ctx, store, "token-revoke-foreign@example.com", "Token Foreign")
	authSecret, _ := testScopedAPIToken(t, ctx, store, access.APITokenInput{
		PrincipalID:  owner.ID,
		Name:         "auth",
		Capabilities: []access.Capability{access.CapabilityResourceManage},
	})
	ownerSecret, ownerToken := testScopedAPIToken(t, ctx, store, access.APITokenInput{PrincipalID: owner.ID, Name: "owned"})
	foreignSecret, foreignToken := testScopedAPIToken(t, ctx, store, access.APITokenInput{PrincipalID: foreign.ID, Name: "foreign"})
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))

	for _, id := range []string{foreignToken.ID, "token_missing"} {
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/me/api-tokens/"+id, nil)
		req.Header.Set("Authorization", "Bearer "+authSecret)
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("revoke api token %q status = %d, want %d body=%s", id, rec.Code, http.StatusNotFound, rec.Body.String())
		}
	}
	if _, err := testAccessRepository(store).PrincipalForAPIToken(ctx, foreignSecret); err != nil {
		t.Fatalf("foreign token was revoked by owner: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/me/api-tokens/"+ownerToken.ID, nil)
	req.Header.Set("Authorization", "Bearer "+authSecret)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke owned api token status = %d, want %d body=%s", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	revokedReq := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	revokedReq.Header.Set("Authorization", "Bearer "+ownerSecret)
	revokedReq.Header.Set("Accept", "application/json")
	revokedRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(revokedRec, revokedReq)
	if revokedRec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked api token status = %d, want %d body=%s", revokedRec.Code, http.StatusUnauthorized, revokedRec.Body.String())
	}
}

func TestCurrentAPITokenCreateAndRevokeRecordsAudit(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	repo := testAccessRepository(store)
	owner := testPlatformPrincipal(t, ctx, store, "token-audit-owner@example.com", "Token Audit Owner")
	authSecret, _ := testScopedAPIToken(t, ctx, store, access.APITokenInput{
		PrincipalID:  owner.ID,
		Name:         "auth",
		Capabilities: []access.Capability{access.CapabilityResourceManage, access.CapabilityResourceUse},
	})
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/me/api-tokens", strings.NewReader(`{"name":"audited-api-token","capabilities":["RESOURCE_USE"]}`))
	createReq.Header.Set("Authorization", "Bearer "+authSecret)
	createReq.Header.Set("Accept", "application/json")
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Idempotency-Key", "create-audited-api-token")
	createRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create api token status = %d, want %d body=%s", createRec.Code, http.StatusCreated, createRec.Body.String())
	}
	var created struct {
		APIToken struct {
			ID string `json:"id"`
		} `json:"apiToken"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created api token: %v body=%s", err, createRec.Body.String())
	}
	if created.APIToken.ID == "" {
		t.Fatalf("created api token missing id: %s", createRec.Body.String())
	}
	createdEvents, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{PrincipalID: owner.ID, Action: "api_token.created"})
	if err != nil {
		t.Fatalf("list create audit events: %v", err)
	}
	if len(createdEvents) != 1 || createdEvents[0].ResourceID != created.APIToken.ID || createdEvents[0].PrincipalID != owner.ID {
		t.Fatalf("api_token.created audit = %#v, want target %q actor %q", createdEvents, created.APIToken.ID, owner.ID)
	}

	revokeReq := httptest.NewRequest(http.MethodDelete, "/api/v1/me/api-tokens/"+created.APIToken.ID, nil)
	revokeReq.Header.Set("Authorization", "Bearer "+authSecret)
	revokeReq.Header.Set("Accept", "application/json")
	revokeRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(revokeRec, revokeReq)
	if revokeRec.Code != http.StatusNoContent {
		t.Fatalf("revoke api token status = %d, want %d body=%s", revokeRec.Code, http.StatusNoContent, revokeRec.Body.String())
	}
	revokedEvents, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{PrincipalID: owner.ID, Action: "api_token.revoked"})
	if err != nil {
		t.Fatalf("list revoke audit events: %v", err)
	}
	if len(revokedEvents) != 1 || revokedEvents[0].ResourceID != created.APIToken.ID || revokedEvents[0].PrincipalID != owner.ID {
		t.Fatalf("api_token.revoked audit = %#v, want target %q actor %q", revokedEvents, created.APIToken.ID, owner.ID)
	}
}

func TestCurrentAPITokenCreateRejectsExpiredExpiry(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testPlatformPrincipal(t, ctx, store, "expired-token-owner@example.com", "Expired Token Owner")
	authSecret, _ := testScopedAPIToken(t, ctx, store, access.APITokenInput{
		PrincipalID:  owner.ID,
		Name:         "auth",
		Capabilities: []access.Capability{access.CapabilityResourceManage},
	})
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))

	expiresAt := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/me/api-tokens", strings.NewReader(`{"name":"expired-api-token","capabilities":[],"expiresAt":"`+expiresAt+`"}`))
	req.Header.Set("Authorization", "Bearer "+authSecret)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "reject-expired-api-token")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create expired api token status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestServicePrincipalSecretCreateReturnsExpiry(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	repo := testAccessRepository(store)
	owner := testPlatformPrincipal(t, ctx, store, "sp-secret-owner@example.com", "SP Secret Owner")
	authSecret, _ := testScopedAPIToken(t, ctx, store, access.APITokenInput{
		PrincipalID:  owner.ID,
		Name:         "platform-admin",
		Capabilities: []access.Capability{access.CapabilityProjectAdmin},
	})
	servicePrincipal, err := repo.CreateServicePrincipal(ctx, access.ServicePrincipalInput{ID: "sp_secret_api", DisplayName: "Secret API"})
	if err != nil {
		t.Fatalf("create service principal: %v", err)
	}
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))

	expiresAt := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/service-principals/"+servicePrincipal.ID+"/secrets", strings.NewReader(`{"name":"deploy","expiresAt":"`+expiresAt+`"}`))
	req.Header.Set("Authorization", "Bearer "+authSecret)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "create-service-principal-secret")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create service principal secret status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var body struct {
		Secret       string `json:"secret"`
		ClientSecret struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			ExpiresAt string `json:"expiresAt"`
			Secret    string `json:"secret"`
		} `json:"clientSecret"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode service principal secret response: %v body=%s", err, rec.Body.String())
	}
	if body.Secret == "" || body.ClientSecret.Secret != "" {
		t.Fatalf("secret exposure = top-level %q nested %q", body.Secret, body.ClientSecret.Secret)
	}
	if body.ClientSecret.Name != "deploy" || body.ClientSecret.ExpiresAt != expiresAt {
		t.Fatalf("client secret metadata = %#v, want name deploy expires %s", body.ClientSecret, expiresAt)
	}
	if _, err := repo.PrincipalForServicePrincipalSecret(ctx, servicePrincipal.ID, body.Secret); err != nil {
		t.Fatalf("resolve created service principal secret: %v", err)
	}
}

func TestSecretMintingResponsesDisableHTTPStorage(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	repo := testAccessRepository(store)
	owner := testPlatformPrincipal(t, ctx, store, "secret-cache-owner@example.com", "Secret Cache Owner")
	authSecret, _ := testScopedAPIToken(t, ctx, store, access.APITokenInput{
		PrincipalID:  owner.ID,
		Name:         "platform-admin",
		Capabilities: []access.Capability{access.CapabilityProjectAdmin, access.CapabilityResourceManage, access.CapabilityResourceUse},
	})
	servicePrincipal, err := repo.CreateServicePrincipal(ctx, access.ServicePrincipalInput{ID: "sp_secret_cache", DisplayName: "Secret Cache"})
	if err != nil {
		t.Fatalf("create service principal: %v", err)
	}
	spSecret, _, err := repo.CreateServicePrincipalSecret(ctx, servicePrincipal.ID, access.ServicePrincipalSecretInput{Name: "oauth"})
	if err != nil {
		t.Fatalf("create service principal secret: %v", err)
	}
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))

	for _, tc := range []struct {
		name          string
		req           *http.Request
		wantStatus    int
		secretMarkers []string
	}{
		{
			name:       "api token",
			req:        secretCacheJSONRequest(http.MethodPost, "/api/v1/me/api-tokens", authSecret, `{"name":"deploy","capabilities":["RESOURCE_USE"]}`),
			wantStatus: http.StatusCreated,
			secretMarkers: []string{
				`"token":`,
			},
		},
		{
			name:       "service principal secret",
			req:        secretCacheJSONRequest(http.MethodPost, "/api/v1/service-principals/"+servicePrincipal.ID+"/secrets", authSecret, `{"name":"deploy"}`),
			wantStatus: http.StatusCreated,
			secretMarkers: []string{
				`"secret":`,
			},
		},
		{
			name: "oauth token",
			req: func() *http.Request {
				req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader("grant_type=client_credentials&client_id="+servicePrincipal.ID+"&client_secret="+spSecret+"&scope=mcp%3Ause&resource=http%3A%2F%2Flocalhost%3A8080%2Fmcp"))
				req.Header.Set("Accept", "application/json")
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				return req
			}(),
			wantStatus: http.StatusOK,
			secretMarkers: []string{
				`"access_token":`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			server.Routes().ServeHTTP(rec, tc.req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			for _, marker := range tc.secretMarkers {
				if !strings.Contains(rec.Body.String(), marker) {
					t.Fatalf("response missing secret marker %q: %s", marker, rec.Body.String())
				}
			}
			assertSecretResponseNoStore(t, rec)
		})
	}
}

func TestCurrentSessionRevocationIsScopedToAuthenticatedPrincipal(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	repo := testAccessRepository(store)
	owner := testPlatformPrincipal(t, ctx, store, "session-revoke-owner@example.com", "Session Owner")
	foreign := testPlatformPrincipal(t, ctx, store, "session-revoke-foreign@example.com", "Session Foreign")
	authSecret, _ := testScopedAPIToken(t, ctx, store, access.APITokenInput{
		PrincipalID:  owner.ID,
		Name:         "auth",
		Capabilities: []access.Capability{access.CapabilityResourceUse},
	})
	ownerSessionSecret, err := repo.CreateSession(ctx, owner.ID, time.Hour)
	if err != nil {
		t.Fatalf("create owner session: %v", err)
	}
	foreignSessionSecret, err := repo.CreateSession(ctx, foreign.ID, time.Hour)
	if err != nil {
		t.Fatalf("create foreign session: %v", err)
	}
	ownerSessions, err := repo.ListSessions(ctx, owner.ID)
	if err != nil {
		t.Fatalf("list owner sessions: %v", err)
	}
	foreignSessions, err := repo.ListSessions(ctx, foreign.ID)
	if err != nil {
		t.Fatalf("list foreign sessions: %v", err)
	}
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))

	for _, id := range []string{foreignSessions[0].ID, "session_missing"} {
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/me/sessions/"+id, nil)
		req.Header.Set("Authorization", "Bearer "+authSecret)
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("revoke session %q status = %d, want %d body=%s", id, rec.Code, http.StatusNotFound, rec.Body.String())
		}
	}
	if _, err := repo.PrincipalForToken(ctx, foreignSessionSecret); err != nil {
		t.Fatalf("foreign session was revoked by owner: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/me/sessions/"+ownerSessions[0].ID, nil)
	req.Header.Set("Authorization", "Bearer "+authSecret)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke owned session status = %d, want %d body=%s", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	if _, err := repo.PrincipalForToken(ctx, ownerSessionSecret); err == nil {
		t.Fatal("owner session still resolves after revocation")
	}
}

func testScopedAPIToken(t *testing.T, ctx context.Context, store *platform.Store, input access.APITokenInput) (string, access.APIToken) {
	t.Helper()
	secret, token, err := testAccessRepository(store).CreateAPITokenWithMetadata(ctx, input)
	if err != nil {
		t.Fatalf("create scoped api token: %v", err)
	}
	return secret, token
}

func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func secretCacheJSONRequest(method, path, token, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if method == http.MethodPost && strings.HasPrefix(path, "/api/v1/") {
		req.Header.Set("Idempotency-Key", "secret-cache-"+strings.ReplaceAll(path, "/", "-"))
	}
	return req
}

func assertSecretResponseNoStore(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("Pragma"); got != "no-cache" {
		t.Fatalf("Pragma = %q, want no-cache", got)
	}
}
