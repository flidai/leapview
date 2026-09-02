package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/http/mcpoauth"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	agentcap "github.com/flidai/leapview/internal/agent"
	apihttpmiddleware "github.com/flidai/leapview/internal/platform/http/middleware"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPRequiresBearerAndSupportsInitializeAndTools(t *testing.T) {
	store := testStore(t)
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{
		Auth: testAuth(store, accessmodule.AuthConfig{DevBypass: true, DevAPIToken: "mcp-secret"}),
	}))
	handler := server.Routes()

	unauthorized := mcpRequest(t, handler, "", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if unauthorized.Code != http.StatusUnauthorized || !strings.Contains(unauthorized.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("unauthorized response = %d headers=%v body=%s", unauthorized.Code, unauthorized.Header(), unauthorized.Body.String())
	}

	initialized := mcpRequest(t, handler, "mcp-secret", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	if initialized.Code != http.StatusOK {
		t.Fatalf("initialize = %d body=%s", initialized.Code, initialized.Body.String())
	}
	var initialize map[string]any
	if err := json.Unmarshal(initialized.Body.Bytes(), &initialize); err != nil {
		t.Fatalf("decode initialize: %v", err)
	}
	result := initialize["result"].(map[string]any)
	if result["protocolVersion"] != "2025-11-25" {
		t.Fatalf("protocol version = %#v", result["protocolVersion"])
	}

	listed := mcpRequest(t, handler, "mcp-secret", "2025-11-25", `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	if listed.Code != http.StatusOK {
		t.Fatalf("tools/list = %d body=%s", listed.Code, listed.Body.String())
	}
	var listResponse struct {
		Result struct {
			Tools []struct {
				Name         string         `json:"name"`
				Description  string         `json:"description"`
				InputSchema  map[string]any `json:"inputSchema"`
				OutputSchema map[string]any `json:"outputSchema"`
				Annotations  struct {
					ReadOnly    bool `json:"readOnlyHint"`
					Destructive bool `json:"destructiveHint"`
					Idempotent  bool `json:"idempotentHint"`
					OpenWorld   bool `json:"openWorldHint"`
				} `json:"annotations"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &listResponse); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	builtIn := map[string]struct {
		description string
		input       map[string]any
		output      map[string]any
		effect      string
	}{}
	for _, definition := range server.routes.agentModule.ToolDefinitions(agentcap.Scope{PrincipalID: "dev", DevAuthBypass: true}) {
		var input, output map[string]any
		if err := json.Unmarshal(definition.InputSchema, &input); err != nil {
			t.Fatalf("decode built-in input schema %s: %v", definition.Name, err)
		}
		if err := json.Unmarshal(definition.OutputSchema, &output); err != nil {
			t.Fatalf("decode built-in output schema %s: %v", definition.Name, err)
		}
		builtIn[definition.Name] = struct {
			description string
			input       map[string]any
			output      map[string]any
			effect      string
		}{definition.Description, input, output, definition.Effect}
	}
	if len(listResponse.Result.Tools) != len(builtIn) {
		t.Fatalf("MCP tool count = %d, built-in count = %d", len(listResponse.Result.Tools), len(builtIn))
	}
	if len(listResponse.Result.Tools) != 22 {
		t.Fatalf("MCP tool count = %d, want 22", len(listResponse.Result.Tools))
	}
	foundVisual := false
	foundNames := map[string]bool{}
	for _, tool := range listResponse.Result.Tools {
		foundNames[tool.Name] = true
		expected, ok := builtIn[tool.Name]
		if !ok {
			t.Fatalf("MCP exposed tool absent from built-in catalog: %s", tool.Name)
		}
		if tool.Description != expected.description || !jsonObjectsEqual(tool.InputSchema, expected.input) || !jsonObjectsEqual(tool.OutputSchema, expected.output) || tool.Annotations.ReadOnly != (expected.effect == "read") {
			t.Fatalf("MCP metadata differs for %s", tool.Name)
		}
		if tool.Annotations.ReadOnly != (expected.effect == "read") || tool.Annotations.Destructive != (expected.effect == "destructive") || tool.Annotations.Idempotent != (expected.effect == "read") || tool.Annotations.OpenWorld {
			t.Fatalf("MCP safety annotations differ for %s: %#v", tool.Name, tool.Annotations)
		}
		if tool.Name == "query_visual" {
			foundVisual = true
			if !tool.Annotations.ReadOnly || tool.InputSchema["type"] != "object" || tool.OutputSchema["type"] != "object" {
				t.Fatalf("query_visual metadata = %#v", tool)
			}
			properties := tool.InputSchema["properties"].(map[string]any)
			if _, ok := properties["semanticModelId"]; !ok {
				t.Fatalf("query_visual schema does not expose semanticModelId: %#v", tool.InputSchema)
			}
			if _, ok := properties["workspace"]; ok {
				t.Fatalf("query_visual schema still exposes legacy workspace: %#v", tool.InputSchema)
			}
		}
	}
	if !foundVisual {
		t.Fatalf("tools/list omitted query_visual: %s", listed.Body.String())
	}
	for _, name := range []string{"catalog_search", "catalog_list", "catalog_get", "query_semantic_model", "query_dashboard_visual", "query_visual", "docs_search", "docs_read", "list_dashboards", "get_dashboard", "get_dashboard_draft", "read_dashboard_source", "edit_dashboard_source", "create_dashboard_draft", "execute_dashboard_command", "fork_dashboard", "preview_dashboard_draft", "export_dashboard_yaml", "set_dashboard_visibility", "add_dashboard_page", "add_dashboard_visual", "assign_dashboard_field"} {
		if !foundNames[name] {
			t.Fatalf("tools/list omitted %s: %s", name, listed.Body.String())
		}
	}
	for _, legacy := range []string{"list_workspaces", "search", "describe_dashboard", "query_dashboard_page"} {
		if foundNames[legacy] {
			t.Fatalf("tools/list exposed legacy tool %s", legacy)
		}
	}

	called := mcpRequest(t, handler, "mcp-secret", "2025-11-25", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"catalog_list","arguments":{}}}`)
	if called.Code != http.StatusOK {
		t.Fatalf("tools/call = %d body=%s", called.Code, called.Body.String())
	}
	var callResponse struct {
		Result struct {
			IsError           bool           `json:"isError"`
			StructuredContent map[string]any `json:"structuredContent"`
			Content           []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(called.Body.Bytes(), &callResponse); err != nil {
		t.Fatalf("decode tools/call: %v", err)
	}
	if callResponse.Result.IsError || len(callResponse.Result.Content) != 1 {
		t.Fatalf("tools/call result = %#v body=%s", callResponse.Result, called.Body.String())
	}
	var textContent map[string]any
	if err := json.Unmarshal([]byte(callResponse.Result.Content[0].Text), &textContent); err != nil {
		t.Fatalf("decode tools/call text: %v", err)
	}
	if !jsonObjectsEqual(callResponse.Result.StructuredContent, textContent) {
		t.Fatalf("structured and text output differ: structured=%#v text=%#v", callResponse.Result.StructuredContent, textContent)
	}

	visual := mcpRequest(t, handler, "mcp-secret", "2025-11-25", `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"query_visual","arguments":{"semanticModelId":"test","visual":{"type":"bar","query":{"type":"aggregate","dimensions":["status"],"metrics":["order_count"],"limit":10},"presentation":{"type":"cartesian"},"dataBudget":{"maxRows":50}}}}}`)
	if visual.Code != http.StatusOK {
		t.Fatalf("query_visual = %d body=%s", visual.Code, visual.Body.String())
	}
	var visualResponse struct {
		Result struct {
			IsError           bool           `json:"isError"`
			StructuredContent map[string]any `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(visual.Body.Bytes(), &visualResponse); err != nil {
		t.Fatalf("decode query_visual: %v", err)
	}
	if visualResponse.Result.IsError ||
		visualResponse.Result.StructuredContent["queryId"] == nil ||
		visualResponse.Result.StructuredContent["fields"] == nil ||
		visualResponse.Result.StructuredContent["completeness"] == nil ||
		visualResponse.Result.StructuredContent["patch"] != nil {
		t.Fatalf("query_visual result = %#v body=%s", visualResponse.Result, visual.Body.String())
	}
}

func TestMCPGoSDKClientInteroperability(t *testing.T) {
	store := testStore(t)
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{
		Auth: testAuth(store, accessmodule.AuthConfig{DevBypass: true, DevAPIToken: "mcp-secret"}),
	}))
	live := httptest.NewServer(server.Routes())
	defer live.Close()

	httpClient := *live.Client()
	httpClient.Transport = bearerRoundTripper{base: httpClient.Transport, token: "mcp-secret"}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "leapview-interoperability-test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcpsdk.StreamableClientTransport{
		Endpoint:             live.URL + "/mcp",
		HTTPClient:           &httpClient,
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect MCP SDK client: %v", err)
	}
	defer session.Close()

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools through MCP SDK: %v", err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("MCP SDK returned no tools")
	}

	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: "query_visual",
		Arguments: map[string]any{
			"semanticModelId": "test",
			"visual": map[string]any{
				"type": "bar",
				"query": map[string]any{
					"type": "aggregate", "dimensions": []string{"status"}, "metrics": []string{"order_count"}, "limit": 10,
				},
				"presentation": map[string]any{"type": "cartesian"},
				"dataBudget":   map[string]any{"maxRows": 50},
			},
		},
	})
	if err != nil {
		t.Fatalf("call query_visual through MCP SDK: %v", err)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if result.IsError || !ok ||
		structured["queryId"] == nil ||
		structured["fields"] == nil ||
		structured["completeness"] == nil ||
		structured["patch"] != nil {
		t.Fatalf("MCP SDK query_visual result = %#v", result)
	}
}

type bearerRoundTripper struct {
	base  http.RoundTripper
	token string
}

func (t bearerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

func TestMCPReturnsValidationFailuresAsToolErrorsAndRejectsOrigins(t *testing.T) {
	store := testStore(t)
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{

		Auth: testAuth(store, accessmodule.AuthConfig{DevBypass: true, DevAPIToken: "mcp-secret"}),
	}))
	handler := server.Routes()

	invalid := mcpRequest(t, handler, "mcp-secret", "2025-11-25", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"query_visual","arguments":{}}}`)
	if invalid.Code != http.StatusOK {
		t.Fatalf("invalid call = %d body=%s", invalid.Code, invalid.Body.String())
	}
	var response struct {
		Result struct {
			IsError           bool           `json:"isError"`
			StructuredContent map[string]any `json:"structuredContent"`
			Content           []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(invalid.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode invalid call: %v", err)
	}
	if !response.Result.IsError || response.Result.StructuredContent != nil || len(response.Result.Content) != 1 || !json.Valid([]byte(response.Result.Content[0].Text)) {
		t.Fatalf("validation result = %#v body=%s", response.Result, invalid.Body.String())
	}

	origin := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	origin.Header.Set("Authorization", "Bearer mcp-secret")
	origin.Header.Set("Content-Type", "application/json")
	origin.Header.Set("Origin", "https://attacker.example")
	originRec := httptest.NewRecorder()
	handler.ServeHTTP(originRec, origin)
	if originRec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want 403 body=%s", originRec.Code, originRec.Body.String())
	}
}

func TestMCPAcceptsOAuthTokensAndRejectsGeneralAPITokens(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	principal := testPrincipal(t, ctx, store, "mcp@example.com", "MCP User")
	repo := testAccessRepository(store)
	apiSecret, _, err := repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{
		PrincipalID:  principal.ID,
		Name:         "rest-api-only",
		Capabilities: []access.Capability{access.CapabilityResourceUse, access.CapabilityResourceRead},
	})
	if err != nil {
		t.Fatalf("create REST API token: %v", err)
	}

	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{
		Auth:     testAuth(store, accessmodule.AuthConfig{APITokenOnly: true}),
		MCPOAuth: MCPOAuthConfig{PublicURL: "https://leapview.example"},
	}))
	handler := server.Routes()
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	apiAttempt := mcpRequest(t, handler, apiSecret, "", initialize)
	if apiAttempt.Code != http.StatusUnauthorized || !strings.Contains(apiAttempt.Header().Get("WWW-Authenticate"), "resource_metadata") {
		t.Fatalf("API token response = %d headers=%v body=%s", apiAttempt.Code, apiAttempt.Header(), apiAttempt.Body.String())
	}
	oauthToken := issueMCPUserToken(t, server, principal.ID)
	if response := mcpRequest(t, handler, oauthToken, "", initialize); response.Code != http.StatusOK {
		t.Fatalf("OAuth token status = %d body=%s", response.Code, response.Body.String())
	}
	restrictedPrincipal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Email: "restricted@example.com", DisplayName: "Restricted"})
	if err != nil {
		t.Fatalf("create restricted principal: %v", err)
	}
	restrictedToken := issueMCPUserToken(t, server, restrictedPrincipal.ID)
	if response := mcpRequest(t, handler, restrictedToken, "", initialize); response.Code != http.StatusOK {
		t.Fatalf("restricted OAuth token initialization status = %d body=%s", response.Code, response.Body.String())
	}

	deniedCatalog := mcpRequest(t, handler, oauthToken, "2025-11-25", `{"jsonrpc":"2.0","id":"denied-project","method":"tools/call","params":{"name":"catalog_get","arguments":{"ref":{"kind":"project","id":"project:other"}}}}`)
	if deniedCatalog.Code != http.StatusOK || !strings.Contains(deniedCatalog.Body.String(), `"isError":true`) {
		t.Fatalf("denied project catalog call = %d body=%s", deniedCatalog.Code, deniedCatalog.Body.String())
	}
	authorizedCatalog := mcpRequest(t, handler, oauthToken, "2025-11-25", `{"jsonrpc":"2.0","id":"authorized-catalog","method":"tools/call","params":{"name":"catalog_list","arguments":{}}}`)
	if authorizedCatalog.Code != http.StatusOK || strings.Contains(authorizedCatalog.Body.String(), `"id":"other"`) {
		t.Fatalf("authorized catalog leaked foreign project: %d body=%s", authorizedCatalog.Code, authorizedCatalog.Body.String())
	}
	cookieOnly := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(initialize))
	cookieOnly.Header.Set("Content-Type", "application/json")
	cookieOnly.Header.Set("Accept", "application/json, text/event-stream")
	cookieOnly.AddCookie(&http.Cookie{Name: "lv_session", Value: "browser-session"})
	cookieOnlyRec := httptest.NewRecorder()
	handler.ServeHTTP(cookieOnlyRec, cookieOnly)
	if cookieOnlyRec.Code != http.StatusUnauthorized {
		t.Fatalf("browser session status = %d, want 401", cookieOnlyRec.Code)
	}
}

func TestMCPOAuthDiscoveryAndBrowserConsent(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	repo := testAccessRepository(store)
	principal := testPrincipal(t, ctx, store, "consent@example.com", "Consent User")
	session, err := repo.CreateSession(ctx, principal.ID, time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{
		Auth:     testAuth(store, accessmodule.AuthConfig{LocalAuth: true, CSRFKey: "0123456789abcdef0123456789abcdef"}),
		MCPOAuth: MCPOAuthConfig{PublicURL: "https://leapview.example"},
	}))
	handler := server.Routes()

	for path, want := range map[string]string{
		"/.well-known/oauth-protected-resource/mcp": `"resource":"https://leapview.example/mcp"`,
		"/.well-known/oauth-authorization-server":   `"client_id_metadata_document_supported":true`,
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), want) {
			t.Fatalf("GET %s = %d body=%s", path, recorder.Code, recorder.Body.String())
		}
	}

	registration := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(`{"client_name":"Claude","redirect_uris":["https://claude.example/callback"],"grant_types":["authorization_code","refresh_token"],"response_types":["code"],"token_endpoint_auth_method":"none","logo_uri":"https://claude.example/logo.png"}`))
	registration.Header.Set("Content-Type", "application/json")
	registered := httptest.NewRecorder()
	handler.ServeHTTP(registered, registration)
	if registered.Code != http.StatusCreated {
		t.Fatalf("register = %d body=%s", registered.Code, registered.Body.String())
	}
	var client mcpoauth.RegistrationResponse
	if err := json.Unmarshal(registered.Body.Bytes(), &client); err != nil {
		t.Fatalf("decode registration: %v", err)
	}

	verifier := strings.Repeat("v", 64)
	challengeBytes := sha256.Sum256([]byte(verifier))
	values := url.Values{
		"response_type": {"code"}, "client_id": {client.ClientID}, "redirect_uri": {"https://claude.example/callback"},
		"scope": {"mcp:use offline_access"}, "state": {"claude-client-state"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challengeBytes[:])}, "code_challenge_method": {"S256"},
		"resource": {"https://leapview.example/mcp"},
	}
	consentRequest := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+values.Encode(), nil)
	consentRequest.AddCookie(&http.Cookie{Name: "lv_session", Value: session})
	consentResponse := httptest.NewRecorder()
	handler.ServeHTTP(consentResponse, consentRequest)
	if consentResponse.Code != http.StatusOK || !strings.Contains(consentResponse.Body.String(), "Claude is requesting permission") {
		t.Fatalf("consent = %d body=%s", consentResponse.Code, consentResponse.Body.String())
	}
	match := regexp.MustCompile(`name="gorilla\.csrf\.Token" value="([^"]+)"`).FindStringSubmatch(consentResponse.Body.String())
	if len(match) != 2 {
		t.Fatalf("consent response missing CSRF token: %s", consentResponse.Body.String())
	}
	values.Set("gorilla.csrf.Token", match[1])
	values.Set("decision", "approve")
	approveRequest := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(values.Encode()))
	approveRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	approveRequest.Header.Set("X-Request-ID", "oauth-consent-request")
	approveRequest.AddCookie(&http.Cookie{Name: "lv_session", Value: session})
	for _, cookie := range consentResponse.Result().Cookies() {
		approveRequest.AddCookie(cookie)
	}
	approveResponse := httptest.NewRecorder()
	handler.ServeHTTP(approveResponse, approveRequest)
	if approveResponse.Code != http.StatusSeeOther || !strings.Contains(approveResponse.Header().Get("Location"), "code=") {
		t.Fatalf("approve = %d location=%s body=%s", approveResponse.Code, approveResponse.Header().Get("Location"), approveResponse.Body.String())
	}
	audits, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{PrincipalID: principal.ID, Action: "mcp_oauth.authorization"})
	if err != nil {
		t.Fatalf("list OAuth audits: %v", err)
	}
	if len(audits) != 1 || audits[0].Status != "success" || audits[0].ResourceID != client.ClientID || audits[0].RequestID != "oauth-consent-request" {
		t.Fatalf("OAuth authorization audit = %#v", audits)
	}
}

func issueMCPUserToken(t *testing.T, server *appTestHarness, principalID string) string {
	t.Helper()
	registrationBody := `{"client_name":"Test MCP Client","redirect_uris":["https://client.example/callback"],"grant_types":["authorization_code","refresh_token"],"response_types":["code"],"token_endpoint_auth_method":"none"}`
	registrationRequest := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(registrationBody))
	registrationRequest.Header.Set("Content-Type", "application/json")
	registrationResponse := httptest.NewRecorder()
	server.routes.accessModule.OAuthService().Register(registrationResponse, registrationRequest)
	if registrationResponse.Code != http.StatusCreated {
		t.Fatalf("register OAuth client = %d body=%s", registrationResponse.Code, registrationResponse.Body.String())
	}
	var registration mcpoauth.RegistrationResponse
	if err := json.Unmarshal(registrationResponse.Body.Bytes(), &registration); err != nil {
		t.Fatalf("decode OAuth registration: %v", err)
	}
	verifier := "abcdefghijklmnopqrstuvwxyz-._~ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	challengeBytes := sha256.Sum256([]byte(verifier))
	values := url.Values{
		"response_type": {"code"}, "client_id": {registration.ClientID},
		"redirect_uri": {"https://client.example/callback"}, "scope": {"mcp:use offline_access"},
		"state": {"client-state"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challengeBytes[:])},
		"code_challenge_method": {"S256"}, "resource": {"https://leapview.example/mcp"},
	}
	authorizeRequest := httptest.NewRequest(http.MethodPost, "/oauth/authorize?"+values.Encode(), nil)
	authorizeResponse := httptest.NewRecorder()
	server.routes.accessModule.OAuthService().Authorize(authorizeResponse, authorizeRequest, principalID, true)
	callback, err := url.Parse(authorizeResponse.Header().Get("Location"))
	if err != nil || callback.Query().Get("code") == "" {
		t.Fatalf("OAuth callback = %q err=%v body=%s", authorizeResponse.Header().Get("Location"), err, authorizeResponse.Body.String())
	}
	tokenValues := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {registration.ClientID},
		"code": {callback.Query().Get("code")}, "redirect_uri": {"https://client.example/callback"},
		"code_verifier": {verifier}, "resource": {"https://leapview.example/mcp"},
	}
	tokenRequest := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(tokenValues.Encode()))
	tokenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenResponse := httptest.NewRecorder()
	server.routes.accessModule.OAuthService().Token(tokenResponse, tokenRequest)
	if tokenResponse.Code != http.StatusOK {
		t.Fatalf("exchange OAuth code = %d body=%s", tokenResponse.Code, tokenResponse.Body.String())
	}
	var token mcpoauth.TokenResponse
	if err := json.Unmarshal(tokenResponse.Body.Bytes(), &token); err != nil || token.AccessToken == "" {
		t.Fatalf("decode OAuth token: %#v err=%v", token, err)
	}
	return token.AccessToken
}

func TestMCPUsesAPIRateAndBodyLimits(t *testing.T) {
	store := testStore(t)
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{

		Auth: testAuth(store, accessmodule.AuthConfig{DevBypass: true, DevAPIToken: "mcp-secret"}),
		RateLimits: apihttpmiddleware.RateLimitConfig{
			Enabled:   true,
			APILimit:  1,
			APIWindow: time.Minute,
		},
		RequestBodyLimit: apihttpmiddleware.RequestBodyLimitConfig{Enabled: true, MaxBytes: 512},
	}))
	handler := server.Routes()
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	if first := mcpRequest(t, handler, "mcp-secret", "", initialize); first.Code != http.StatusOK {
		t.Fatalf("first MCP request = %d body=%s", first.Code, first.Body.String())
	}
	if second := mcpRequest(t, handler, "mcp-secret", "", initialize); second.Code != http.StatusTooManyRequests {
		t.Fatalf("second MCP request = %d, want 429 body=%s", second.Code, second.Body.String())
	}

	bodyStore := testStore(t)
	bodyLimited := assembleRuntime(fakeMetrics{}, testStoreOptions(bodyStore, assemblyConfig{

		Auth:             testAuth(bodyStore, accessmodule.AuthConfig{DevBypass: true, DevAPIToken: "mcp-secret"}),
		RequestBodyLimit: apihttpmiddleware.RequestBodyLimitConfig{Enabled: true, MaxBytes: 16},
	}))
	oversized := mcpRequest(t, bodyLimited.Routes(), "mcp-secret", "", initialize)
	if oversized.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized MCP request = %d, want 413 body=%s", oversized.Code, oversized.Body.String())
	}
}

func mcpRequest(t *testing.T, handler http.Handler, token, protocolVersion, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if protocolVersion != "" {
		req.Header.Set("Mcp-Protocol-Version", protocolVersion)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func jsonObjectsEqual(left, right map[string]any) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}
