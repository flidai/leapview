//go:build linux && duckdb_arrow

package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/http/mcpoauth"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/app/api/clienttransport"
	"github.com/flidai/leapview/internal/app/config"
	dashboardcli "github.com/flidai/leapview/internal/dashboard/cli"
	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/flidai/leapview/internal/platform/testing/ssetest"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// These are actual cookie/API routes, the maintained CLI command and transport,
// and the production agent tool catalog reached by the MCP SDK. All four read
// the same restored native serving state. This does not render a browser or
// claim row filtering/masking: this fixture has no such policies.
func assertRestoredJourneyQueryParity(t *testing.T, api restoredJourneyAPI, cfg config.Config, repo access.Repository, db accesspostgres.DBTX, ownerSession, ownerID, outsiderID, outsiderToken string) {
	t.Helper()
	live := httptest.NewTLSServer(api.application.Handler())
	defer live.Close()
	httpClient := live.Client()
	httpClient.Timeout = 30 * time.Second
	defer httpClient.CloseIdleConnections()
	// Production admits the configured canonical host; the disposable TLS
	// listener address is only the wire destination for these retained clients.
	httpClient.Transport = restoredQueryHostTransport{base: httpClient.Transport}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	const queryBody = `{"metrics":[{"field":"revenue"}],"limit":10}`
	reference := api.assertQuery(api.owner, "120", http.StatusOK)
	columns, ok := reference["columns"].([]any)
	if !ok || len(columns) != 1 {
		t.Fatal("reference query omitted its governed column")
	}
	snapshot, ok := reference["servingSnapshot"].(string)
	if !ok || snapshot == "" {
		t.Fatal("reference query omitted its serving snapshot")
	}
	projection := func(result map[string]any) map[string]any {
		return map[string]any{"columns": result["columns"], "rows": result["rows"], "completeness": result["completeness"], "page": result["page"], "servingSnapshot": result["servingSnapshot"]}
	}
	assertSame := func(surface string, result map[string]any) {
		t.Helper()
		if !reflect.DeepEqual(projection(result), projection(reference)) {
			t.Fatalf("%s governed projection differs: got=%v want=%v", surface, projection(result), projection(reference))
		}
	}
	// The public agent contract deliberately renames type to dataType and
	// flattens the page cursor (api/typespec/bi.tsp). Compare that declared
	// semantic projection, while browser/API/CLI retain their own contracts.
	assertAgentSame := func(result map[string]any) {
		t.Helper()
		project := func(value map[string]any, agent bool) map[string]any {
			cols, ok := value["columns"].([]any)
			if !ok || len(cols) != 1 {
				t.Fatal("query projection omitted columns")
			}
			selected := make([]any, len(cols))
			for index, raw := range cols {
				column, ok := raw.(map[string]any)
				if !ok {
					t.Fatal("query projection returned invalid column")
				}
				typeKey := "type"
				if agent {
					typeKey = "dataType"
				}
				if column["name"] == nil || column[typeKey] == nil || column["nullable"] == nil {
					t.Fatal("query projection omitted required column metadata")
				}
				selected[index] = map[string]any{"name": column["name"], "type": column[typeKey], "nullable": column["nullable"], "fieldRef": column["fieldRef"], "label": column["label"], "kind": column["kind"], "unit": column["unit"], "format": column["format"]}
			}
			complete, ok := value["completeness"].(map[string]any)
			if !ok || complete["returnedRows"] == nil || complete["hasMore"] == nil || value["rows"] == nil || value["servingSnapshot"] == nil {
				t.Fatal("query projection omitted required result metadata")
			}
			cursor := value["nextCursor"]
			if !agent {
				page, ok := value["page"].(map[string]any)
				if !ok {
					t.Fatal("API query omitted page metadata")
				}
				cursor = page["nextCursor"]
			}
			return map[string]any{"columns": selected, "rows": value["rows"], "completeness": map[string]any{"returnedRows": complete["returnedRows"], "hasMore": complete["hasMore"]}, "nextCursor": cursor, "servingSnapshot": value["servingSnapshot"]}
		}
		if !reflect.DeepEqual(project(result, true), project(reference, false)) {
			t.Fatalf("agent documented semantic projection differs: got=%v want=%v", project(result, true), project(reference, false))
		}
	}
	decode := func(surface string, data []byte) map[string]any {
		t.Helper()
		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil || result == nil {
			t.Fatalf("decode %s query output: %v", surface, err)
		}
		return result
	}
	assertNoRows := func(surface string, data []byte) {
		t.Helper()
		var result map[string]any
		if json.Unmarshal(data, &result) == nil && result["rows"] != nil {
			t.Fatalf("%s denial exposed query rows", surface)
		}
	}
	cliQuery := func(token string, allowed bool) {
		t.Helper()
		client := *httpClient
		transport := &restoredQueryStatusTransport{base: httpClient.Transport}
		client.Transport = transport
		command := dashboardcli.SemanticModelsCommand(t.Context(), restoredQueryCLIClient{client: &client, snapshot: snapshot})
		command.SetArgs([]string{"query", "semantic-model:restored", "--target", live.URL, "--token", token, "--body-json", queryBody})
		command.SilenceErrors, command.SilenceUsage = true, true
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetErr(&output)
		err := command.ExecuteContext(t.Context())
		if allowed {
			if err != nil {
				t.Fatalf("CLI governed query: %v", err)
			}
			assertSame("CLI", decode("CLI", output.Bytes()))
		} else {
			if err == nil || (transport.status.Load() != http.StatusUnauthorized && transport.status.Load() != http.StatusForbidden) || output.Len() != 0 {
				t.Fatalf("denied CLI query did not reject authority cleanly: %v", err)
			}
		}
	}
	browserSequence := int64(0)
	browserQuery := func(session string, allowed, revoked bool) {
		t.Helper()
		pageRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://localhost/login", nil)
		pageRequest.AddCookie(&http.Cookie{Name: "__Host-lv_session", Value: session})
		pageResponse := httptest.NewRecorder()
		api.application.Handler().ServeHTTP(pageResponse, pageRequest)
		match := regexp.MustCompile(`name="csrf-token" content="([^"]+)"`).FindStringSubmatch(pageResponse.Body.String())
		if len(match) != 2 {
			t.Fatal("browser query setup did not expose CSRF evidence")
		}
		browserSequence++
		body := fmt.Sprintf(`{"dataExplorerCommand":{"mode":"explore","clientId":"restored-query-parity","requestSeq":%d,"explore":{"action":"run","requestSeq":%d,"spec":{"schemaVersion":1,"modelId":"semantic-model:restored","datasetId":"orders","dimensions":[],"metrics":[{"field":"revenue"}],"filters":[],"sort":[],"limit":10}}}}`, browserSequence, browserSequence)
		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://localhost/explore/command?semanticModel=semantic-model:restored", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "text/event-stream")
		request.Header.Set("Origin", "https://localhost")
		request.Header.Set("X-CSRF-Token", match[1])
		request.Header.Set("X-Serving-Snapshot", snapshot)
		request.AddCookie(&http.Cookie{Name: "__Host-lv_session", Value: session})
		for _, cookie := range pageResponse.Result().Cookies() {
			request.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		api.application.Handler().ServeHTTP(response, request)
		if allowed {
			if response.Code != http.StatusOK {
				t.Fatalf("browser-cookie query status=%d body=%s", response.Code, response.Body)
			}
			patches := ssetest.PatchSignals(t, response.Body.String())
			if len(patches) != 1 {
				t.Fatalf("browser command returned %d patches", len(patches))
			}
			data, err := json.Marshal(patches[0]["dataExplorer"])
			if err != nil {
				t.Fatal(err)
			}
			var delivered struct {
				Explore projectsignals.DataExploreSignal `json:"explore"`
			}
			if err := json.Unmarshal(data, &delivered); err != nil {
				t.Fatal(err)
			}
			result := delivered.Explore.Result
			column := columns[0].(map[string]any)
			complete := reference["completeness"].(map[string]any)
			if delivered.Explore.Status.State != "success" || result.Error != nil || len(result.Columns) != 1 || result.Columns[0].Key != column["name"] || len(result.Rows) != 1 || len(result.Rows[0]) != 1 || fmt.Sprint(result.Rows[0][result.Columns[0].Key]) != "120" || float64(result.RowsReturned) != complete["returnedRows"] || result.Truncated != complete["hasMore"] || result.RequestSeq != browserSequence {
				t.Fatalf("browser delivered governed projection differs: status=%v result=%v", delivered.Explore.Status, result)
			}
		} else {
			status := http.StatusForbidden
			if revoked {
				status = http.StatusUnauthorized
			}
			if response.Code != status || strings.TrimSpace(response.Body.String()) != http.StatusText(status) {
				t.Fatalf("browser authority denial status=%d want=%d body=%s", response.Code, status, response.Body)
			}
			assertNoRows("browser-cookie", response.Body.Bytes())
		}
	}
	secret := sha256.Sum256([]byte("leapview:mcp-oauth:" + cfg.CSRFKey))
	issuer := strings.TrimSuffix(cfg.PublicURL, "/")
	oauth, err := mcpoauth.NewPostgres(db, repo, mcpoauth.Config{IssuerURL: issuer, ResourceURL: issuer + "/mcp", Secret: secret[:]})
	if err != nil {
		t.Fatalf("prepare native MCP authority: %v", err)
	}
	ownerOAuth, ownerClient := restoredQueryMCPCredential(t, oauth, issuer, ownerID)
	outsiderOAuth, _ := restoredQueryMCPCredential(t, oauth, issuer, outsiderID)
	connect := func(token string) (*mcpsdk.ClientSession, *restoredQueryStatusTransport) {
		t.Helper()
		client := *httpClient
		transport := &restoredQueryStatusTransport{base: bearerRoundTripper{base: httpClient.Transport, token: token}}
		client.Transport = transport
		sdk := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "restored-query-parity", Version: "1"}, nil)
		session, err := sdk.Connect(t.Context(), &mcpsdk.StreamableClientTransport{Endpoint: live.URL + "/mcp", HTTPClient: &client, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
		if err != nil {
			t.Fatalf("connect restored MCP client: %v", err)
		}
		return session, transport
	}
	ownerAgent, ownerAgentTransport := connect(ownerOAuth.AccessToken)
	outsiderAgent, _ := connect(outsiderOAuth.AccessToken)
	defer ownerAgent.Close()
	defer outsiderAgent.Close()
	agentQuery := func(session *mcpsdk.ClientSession, allowed, revoked bool) {
		t.Helper()
		unauthorizedBefore := ownerAgentTransport.unauthorized.Load()
		result, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "query_semantic_model", Arguments: map[string]any{"model": "semantic-model:restored", "metrics": []map[string]string{{"field": "revenue"}}, "limit": 10}})
		if allowed {
			if err != nil || result == nil || result.IsError || result.StructuredContent == nil {
				t.Fatalf("governed agent query failed: %v", err)
			}
			data, err := json.Marshal(result.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			assertAgentSame(decode("agent", data))
		} else {
			if revoked {
				if err == nil || ownerAgentTransport.unauthorized.Load() <= unauthorizedBefore {
					t.Fatalf("revoked MCP credential did not receive HTTP 401: %v", err)
				}
			} else if err != nil || result == nil || !result.IsError {
				t.Fatalf("outsider agent query did not return an authorization tool error: %v", err)
			} else {
				if len(result.Content) != 1 {
					t.Fatal("outsider agent denial omitted its error envelope")
				}
				content, ok := result.Content[0].(*mcpsdk.TextContent)
				if !ok {
					t.Fatal("outsider agent denial returned unexpected content")
				}
				denial := decode("outsider agent error", []byte(content.Text))
				authorization, ok := denial["error"].(map[string]any)
				if !ok || authorization["code"] != "access_denied" {
					t.Fatalf("outsider agent returned a non-authorization error: %v", denial)
				}
				assertNoRows("outsider agent text", []byte(content.Text))
			}
			if result != nil && result.StructuredContent != nil {
				data, err := json.Marshal(result.StructuredContent)
				if err != nil {
					t.Fatal(err)
				}
				assertNoRows("agent", data)
			}
		}
	}
	outsiderSession, err := repo.CreateSession(t.Context(), outsiderID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.DeleteSession(context.Background(), outsiderSession) }()
	browserQuery(ownerSession, true, false)
	cliQuery(api.owner, true)
	agentQuery(ownerAgent, true, false)
	api.assertQuery(outsiderToken, "", http.StatusForbidden)
	browserQuery(outsiderSession, false, false)
	cliQuery(outsiderToken, false)
	agentQuery(outsiderAgent, false, false)

	// Retain each already successful client and revoke its durable credential;
	// subsequent requests must not return the prior native query result.
	credential, err := repo.CredentialForAPIToken(t.Context(), api.owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RevokeAPIToken(t.Context(), credential.Token.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteSession(t.Context(), ownerSession); err != nil {
		t.Fatal(err)
	}
	values := url.Values{"client_id": {ownerClient}, "token": {ownerOAuth.RefreshToken}}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, issuer+"/oauth/revoke", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	revoked := httptest.NewRecorder()
	oauth.Revoke(revoked, request)
	if revoked.Code != http.StatusOK {
		t.Fatalf("revoke MCP credential status=%d", revoked.Code)
	}
	if _, err := oauth.Authenticate(t.Context(), ownerOAuth.AccessToken); err == nil {
		t.Fatal("revoked OAuth family retained its access token")
	}
	api.assertQuery(api.owner, "", http.StatusUnauthorized)
	browserQuery(ownerSession, false, true)
	cliQuery(api.owner, false)
	agentQuery(ownerAgent, false, true)
	t.Log("restored browser command/API/CLI/agent match their documented governed revenue column, exact total120 and complete one-row result; API/CLI/agent also preserve semantic column metadata and serving snapshot; outsider and retained revoked credentials receive no rows")
}

// Only target/token configuration is injected. The maintained command performs
// encoding and decoding through the same product HTTP transport used by the CLI.
type restoredQueryCLIClient struct {
	client   *http.Client
	snapshot string
}

type restoredQueryHostTransport struct{ base http.RoundTripper }

func (transport restoredQueryHostTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Host = "localhost"
	return transport.base.RoundTrip(request)
}

func (transport restoredQueryHostTransport) CloseIdleConnections() {
	if transport, ok := transport.base.(interface{ CloseIdleConnections() }); ok {
		transport.CloseIdleConnections()
	}
}

// Observe actual resource-server responses instead of interpreting SDK error
// wording; the counter is safe for the SDK's concurrent transport requests.
type restoredQueryStatusTransport struct {
	base         http.RoundTripper
	status       atomic.Int64
	unauthorized atomic.Int64
}

func (transport *restoredQueryStatusTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err == nil {
		transport.status.Store(int64(response.StatusCode))
		if request.Method == http.MethodPost && request.URL.Path == "/mcp" && response.StatusCode == http.StatusUnauthorized {
			transport.unauthorized.Add(1)
		}
	}
	return response, err
}

func (c restoredQueryCLIClient) Resolve(_ context.Context, credentials cliapi.Credentials) (cliapi.Credentials, error) {
	return credentials, nil
}

func (c restoredQueryCLIClient) Environment(_ context.Context, _ cliapi.Credentials, environment string) (string, error) {
	return environment, nil
}

func (c restoredQueryCLIClient) Transport(_ context.Context, credentials cliapi.Credentials) (apigenclient.Transport, error) {
	return clienttransport.Transport{Target: credentials.Target, Token: credentials.Token, Client: c.client, MaxResponseBytes: 1 << 20, PrepareRequest: func(request *http.Request) {
		request.Header.Set("X-LeapView-Invocation-Surface", "cli")
		request.Header.Set("X-Serving-Snapshot", c.snapshot)
	}}, nil
}

func restoredQueryMCPCredential(t *testing.T, service *mcpoauth.Service, issuer, principalID string) (mcpoauth.TokenResponse, string) {
	t.Helper()
	const redirect = "http://127.0.0.1:8399/callback"
	registration := httptest.NewRequestWithContext(t.Context(), http.MethodPost, issuer+"/oauth/register", strings.NewReader(`{"client_name":"Restored query parity","redirect_uris":["`+redirect+`"],"grant_types":["authorization_code","refresh_token"],"response_types":["code"],"token_endpoint_auth_method":"none"}`))
	registration.Header.Set("Content-Type", "application/json")
	registered := httptest.NewRecorder()
	service.Register(registered, registration)
	var client mcpoauth.RegistrationResponse
	if registered.Code != http.StatusCreated || json.Unmarshal(registered.Body.Bytes(), &client) != nil || client.ClientID == "" {
		t.Fatalf("prepare MCP client status=%d", registered.Code)
	}
	verifier := rand.Text() + rand.Text()
	challenge := sha256.Sum256([]byte(verifier))
	values := url.Values{"response_type": {"code"}, "client_id": {client.ClientID}, "redirect_uri": {redirect}, "scope": {"mcp:use offline_access"}, "state": {rand.Text()}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}, "resource": {issuer + "/mcp"}}
	authorized := httptest.NewRecorder()
	service.Authorize(authorized, httptest.NewRequestWithContext(t.Context(), http.MethodPost, issuer+"/oauth/authorize?"+values.Encode(), nil), principalID, true)
	callback, err := url.Parse(authorized.Header().Get("Location"))
	if err != nil || authorized.Code != http.StatusSeeOther || callback.Query().Get("code") == "" || callback.Query().Get("state") != values.Get("state") {
		t.Fatalf("prepare MCP authorization status=%d", authorized.Code)
	}
	values = url.Values{"grant_type": {"authorization_code"}, "client_id": {client.ClientID}, "code": {callback.Query().Get("code")}, "redirect_uri": {redirect}, "code_verifier": {verifier}, "resource": {issuer + "/mcp"}}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, issuer+"/oauth/token", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	service.Token(response, request)
	var token mcpoauth.TokenResponse
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &token) != nil || token.AccessToken == "" || token.RefreshToken == "" {
		t.Fatalf("prepare MCP credential status=%d", response.Code)
	}
	credential, err := service.Authenticate(t.Context(), token.AccessToken)
	if err != nil || credential.Principal.ID != principalID {
		t.Fatalf("MCP credential principal mismatch: %v", err)
	}
	return token, client.ClientID
}
