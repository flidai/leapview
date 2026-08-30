package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	connectionadmin "github.com/flidai/leapview/internal/analytics/connectionadmin"
	"github.com/flidai/leapview/internal/analytics/model"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/google/uuid"
)

// TestCredentialedBrowserAndPipelineJourney keeps one deterministic,
// auth-enabled live fixture for the positive browser paths that are otherwise
// easy to accidentally cover only with a dev bearer bypass. The fixture uses
// an in-process HTTP server, a generated local-user verifier, and an opaque
// credential reference; it never stores or sends a provider secret.
func TestCredentialedBrowserAndPipelineJourney(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	repo := accesssqlite.NewRepository(store.SQLDB())
	created, err := repo.CreateLocalUser(ctx, access.LocalUserInput{
		Email: "journey-user@example.test", DisplayName: "Journey User", Password: "journey-password",
	})
	if err != nil {
		t.Fatalf("create local journey user: %v", err)
	}
	if _, err := repo.SetPlatformRole(ctx, access.PlatformRoleInput{
		PrincipalID: created.Principal.ID, Email: created.Principal.Email,
		DisplayName: created.Principal.DisplayName, Role: access.PlatformRoleAdmin,
	}); err != nil {
		t.Fatalf("grant journey user platform role: %v", err)
	}
	auth := testAuth(store, accessmodule.AuthConfig{
		LocalAuth: true, CSRFKey: "credentialed-journey-csrf-key-0123456789abcdef",
	})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{
		Auth:            auth,
		AnalyticsModule: analyticsmodule.NewSurface(nil, nil),
	}))
	server.routes.projectBrowser.Graph = credentialedJourneyGraphReader{}
	// Keep authorization realistic (the local user is not DevBypass) while
	// making the fixture's immutable graph visible to the browser catalog.
	server.routes.projectBrowser.Catalog = credentialedJourneyCatalog{principalID: created.Principal.ID}
	server.routes.projectBrowser.ProjectDefinitionReader = credentialedJourneyDefinitionReader{}
	connection := &credentialedJourneyConnectionAdministration{}
	server.routes.projectBrowser.ConnectionAdministration = connection
	server.routes.projectBrowser.TargetID = "credentialed-journey-target"
	server.routes.projectBrowser.AuthorizeConnectionCreate = func(r *http.Request, projectID projectgraph.ResourceID, capability access.Capability) (bool, error) {
		principal, ok := accessmodule.PrincipalFromContext(r.Context())
		return ok && principal.ID == created.Principal.ID && projectID == "project:test" && capability == access.CapabilityProjectAdmin, nil
	}
	server.routes.projectBrowser.AuthorizePipeline = func(r *http.Request, pipelineID string, capability access.Capability) (bool, error) {
		principal, ok := accessmodule.PrincipalFromContext(r.Context())
		return ok && principal.ID == created.Principal.ID && pipelineID == "pipeline:visuals-refresh" && capability == access.CapabilityResourceUse, nil
	}
	var pipelineCalls []credentialedJourneyPipelineCall
	server.routes.projectBrowser.RunPipeline = func(_ context.Context, pipelineID, principalID, retryOf string) error {
		pipelineCalls = append(pipelineCalls, credentialedJourneyPipelineCall{pipelineID: pipelineID, principalID: principalID, retryOf: retryOf})
		switch len(pipelineCalls) {
		case 1:
			return nil
		case 2:
			return refreshrun.ErrTargetActive
		case 3:
			return errors.New("fixture refresh failed")
		default:
			return nil
		}
	}

	httpServer := httptest.NewServer(server.Routes())
	t.Cleanup(httpServer.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create browser cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	get := func(path string) *http.Response {
		t.Helper()
		response, requestErr := client.Get(httpServer.URL + path)
		if requestErr != nil {
			t.Fatalf("GET %s: %v", path, requestErr)
		}
		return response
	}
	postForm := func(path string, values url.Values) *http.Response {
		t.Helper()
		response, requestErr := client.PostForm(httpServer.URL+path, values)
		if requestErr != nil {
			t.Fatalf("POST %s: %v", path, requestErr)
		}
		return response
	}
	readBody := func(response *http.Response) string {
		t.Helper()
		defer response.Body.Close()
		body, readErr := io.ReadAll(response.Body)
		if readErr != nil {
			t.Fatalf("read response body: %v", readErr)
		}
		return string(body)
	}
	loginPage := func() string {
		t.Helper()
		response := get("/login")
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET /login status = %d", response.StatusCode)
		}
		return readBody(response)
	}
	csrfToken := func(body string) string {
		t.Helper()
		match := regexp.MustCompile(`name="csrf-token" content="([^"]+)"`).FindStringSubmatch(body)
		if len(match) != 2 || strings.TrimSpace(match[1]) == "" {
			t.Fatalf("login page did not expose a CSRF meta token: %s", body)
		}
		return match[1]
	}
	login := func(password string) *http.Response {
		t.Helper()
		token := csrfToken(loginPage())
		return postForm("/auth/local/login", url.Values{
			"email": {created.Principal.Email}, "password": {password}, "gorilla.csrf.Token": {token},
		})
	}

	invalid := login("wrong-password")
	if invalid.StatusCode != http.StatusSeeOther || invalid.Header.Get("Location") != "/login?error=invalid_credentials" {
		t.Fatalf("invalid local login = %d location=%q", invalid.StatusCode, invalid.Header.Get("Location"))
	}
	invalid.Body.Close()

	valid := login("journey-password")
	if valid.StatusCode != http.StatusFound || valid.Header.Get("Location") != "/" {
		t.Fatalf("valid local login = %d location=%q", valid.StatusCode, valid.Header.Get("Location"))
	}
	valid.Body.Close()
	firstSession := browserSessionCookie(t, jar, httpServer.URL)
	if firstSession == "" {
		t.Fatal("valid local login did not establish a session cookie")
	}

	adminPage := get("/admin/profile")
	if adminPage.StatusCode != http.StatusOK {
		t.Fatalf("authenticated /admin = %d location=%q", adminPage.StatusCode, adminPage.Header.Get("Location"))
	}
	readBody(adminPage)

	// Revoke the cookie out of band to model expiry, then require the browser
	// guard to return to the branded recovery surface before re-authentication.
	if err := repo.DeleteSession(ctx, firstSession); err != nil {
		t.Fatalf("expire fixture session: %v", err)
	}
	staleRequest, err := http.NewRequest(http.MethodGet, httpServer.URL+"/sources", nil)
	if err != nil {
		t.Fatal(err)
	}
	staleRequest.AddCookie(&http.Cookie{Name: "lv_session", Value: firstSession})
	staleResponse, err := client.Do(staleRequest)
	if err != nil {
		t.Fatalf("GET /sources with expired session: %v", err)
	}
	if staleResponse.StatusCode != http.StatusFound || staleResponse.Header.Get("Location") != "/login?error=session_expired" {
		t.Fatalf("expired session recovery = %d location=%q", staleResponse.StatusCode, staleResponse.Header.Get("Location"))
	}
	staleResponse.Body.Close()

	renewed := login("journey-password")
	if renewed.StatusCode != http.StatusFound || renewed.Header.Get("Location") != "/sources" {
		t.Fatalf("renewed local login = %d location=%q", renewed.StatusCode, renewed.Header.Get("Location"))
	}
	renewed.Body.Close()
	secondSession := browserSessionCookie(t, jar, httpServer.URL)
	if secondSession == "" || secondSession == firstSession {
		t.Fatalf("renewed session cookie = %q, first = %q", secondSession, firstSession)
	}

	logout := postForm("/auth/logout", url.Values{"gorilla.csrf.Token": {csrfToken(loginPage())}})
	if logout.StatusCode != http.StatusFound || logout.Header.Get("Location") != "/" {
		t.Fatalf("logout = %d location=%q", logout.StatusCode, logout.Header.Get("Location"))
	}
	logout.Body.Close()
	protected := get("/admin/profile")
	if protected.StatusCode != http.StatusUnauthorized {
		t.Fatalf("post-logout /admin = %d location=%q", protected.StatusCode, protected.Header.Get("Location"))
	}
	protected.Body.Close()

	// The connection command carries only the provider-side reference. The
	// recording port proves the assembled browser reaches a credentialed
	// binding path without accepting a secret value.
	if renewed = login("journey-password"); renewed.StatusCode != http.StatusFound {
		t.Fatalf("re-login for pipeline and connection = %d", renewed.StatusCode)
	}
	renewed.Body.Close()
	connectionBody := `{"connectionAdmin":{"action":"create","assetId":"connection:credentialed","connectorKind":"postgres","authenticationMode":"external_bundle","host":"fixture.internal","port":"5432","credentialProjectId":"project:secrets","credentialEnvironment":"dev","secretPath":"/journey/connection","secretKey":"bundle","surface":"list"}}`
	csrf := csrfToken(loginPage())
	connectionResponse := pipelineOrConnectionRequest(t, client, httpServer.URL+"/connections/administration/configuration", connectionBody, "createTargetConnectionBinding", "credentialed-connection-1", csrf)
	connectionMessage := readBody(connectionResponse)
	if connectionResponse.StatusCode != http.StatusOK || !strings.Contains(connectionMessage, "connectionAdmin") {
		t.Fatalf("credentialed connection command = %d body=%s", connectionResponse.StatusCode, connectionMessage)
	}
	if connection.mode != connectionadmin.AuthenticationExternalBundle || connection.reference.SecretPath != "/journey/connection" || connection.reference.SecretKey != "bundle" {
		t.Fatalf("credentialed connection input = %#v body=%s", connection, connectionMessage)
	}

	for index, action := range []string{"run", "run", "run", "retry"} {
		body := `{"pipelineCommand":{"action":"` + action + `","assetId":"pipeline:visuals-refresh","pipelineId":"pipeline:visuals-refresh","runId":"run-failed"}}`
		response := pipelineOrConnectionRequest(t, client, httpServer.URL+"/pipelines/command", body, refreshgen.GenUIActionCreateRefreshRun().OperationID(), "credentialed-pipeline-"+string(rune('1'+index)), csrf)
		message := readBody(response)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("pipeline %s command = %d body=%s", action, response.StatusCode, message)
		}
		switch index {
		case 0, 3:
			if !strings.Contains(message, "Pipeline command accepted") {
				t.Fatalf("pipeline %s success body=%s", action, message)
			}
		case 1, 2:
			if !strings.Contains(message, "Pipeline operation failed") {
				t.Fatalf("pipeline %s failure body=%s", action, message)
			}
		}
	}
	if len(pipelineCalls) != 4 || pipelineCalls[3].retryOf != "run-failed" || pipelineCalls[0].principalID != created.Principal.ID {
		t.Fatalf("pipeline callback calls = %#v", pipelineCalls)
	}
}

type credentialedJourneyPipelineCall struct {
	pipelineID  string
	principalID string
	retryOf     string
}

type credentialedJourneyConnectionAdministration struct {
	journeyConnectionAdministrationStub
	mode      connectionadmin.AuthenticationMode
	reference connectionadmin.CredentialReference
}

func (a *credentialedJourneyConnectionAdministration) Create(_ context.Context, _ string, input connectionadmin.TargetBindingInput) (connectionadmin.TargetBinding, error) {
	a.mode, a.reference = input.AuthenticationMode, input.CredentialReference
	return connectionadmin.TargetBinding{}, nil
}

var _ connectionadmin.Administration = (*credentialedJourneyConnectionAdministration)(nil)

type credentialedJourneyGraphReader struct{}

func (credentialedJourneyGraphReader) ActiveServingStateGraph(context.Context, projectgraph.ResourceID, string) (servingstate.AssetGraph, bool, error) {
	return servingstate.AssetGraph{Assets: []servingstate.Asset{
		{ID: "connection:credentialed", ProjectID: "project:test", ServingStateID: "state:journey", SnapshotID: "snapshot:journey", Type: "connection", Key: "credentialed", Title: "Credentialed connection"},
		{ID: "pipeline:visuals-refresh", ProjectID: "project:test", ServingStateID: "state:journey", SnapshotID: "snapshot:journey", Type: "pipeline", Key: "visuals-refresh", Title: "Visuals refresh"},
	}}, true, nil
}

// credentialedJourneyCatalog is an explicit auth fixture, not a development
// bypass: it grants this test principal read access to the two graph assets
// that the live browser commands need to resolve.
type credentialedJourneyCatalog struct {
	principalID string
}

func (c credentialedJourneyCatalog) List(_ context.Context, request projectcatalog.ListRequest) (projectcatalog.Page, error) {
	if request.PrincipalID != c.principalID || request.DevAuthBypass {
		return projectcatalog.Page{}, projectcatalog.ErrNotFound
	}
	items := make([]projectcatalog.Result, 0, len(request.Kinds))
	for _, kind := range request.Kinds {
		if result, ok := credentialedJourneyCatalogResult(kind); ok {
			items = append(items, result)
		}
	}
	return projectcatalog.Page{Items: items}, nil
}

func (c credentialedJourneyCatalog) Resolve(_ context.Context, principalID string, ref projectcatalog.Ref, _ access.Capability, devAuthBypass bool) (projectcatalog.Result, error) {
	if principalID != c.principalID || devAuthBypass {
		return projectcatalog.Result{}, projectcatalog.ErrNotFound
	}
	result, ok := credentialedJourneyCatalogResult(ref.Kind)
	if !ok || result.Ref.ID != ref.ID {
		return projectcatalog.Result{}, projectcatalog.ErrNotFound
	}
	return result, nil
}

func credentialedJourneyCatalogResult(kind projectgraph.Kind) (projectcatalog.Result, bool) {
	switch kind {
	case projectgraph.KindConnection:
		return projectcatalog.Result{Ref: projectcatalog.Ref{ID: "connection:credentialed", Kind: kind}, Name: "credentialed", DisplayName: "Credentialed connection"}, true
	case projectgraph.KindPipeline:
		return projectcatalog.Result{Ref: projectcatalog.Ref{ID: "pipeline:visuals-refresh", Kind: kind}, Name: "visuals-refresh", DisplayName: "Visuals refresh"}, true
	default:
		return projectcatalog.Result{}, false
	}
}

type credentialedJourneyDefinitionReader struct{}

func (credentialedJourneyDefinitionReader) ProjectDefinitionSnapshot(context.Context) (projectmanifest.Project, map[string]*semanticquery.CompiledModel, error) {
	return projectmanifest.Project{ID: "project:test", Connections: map[string]model.Connection{"connection:credentialed": {Kind: "postgres"}}, RefreshPipelines: map[string]refreshschedule.Definition{"pipeline:visuals-refresh": {}}}, nil, nil
}

func pipelineOrConnectionRequest(t *testing.T, client *http.Client, endpoint, body, operation, requestID, csrf string) *http.Response {
	t.Helper()
	requestUUID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("generate browser request id for %s: %v", requestID, err)
	}
	idempotencyUUID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("generate browser idempotency key for %s: %v", requestID, err)
	}
	request, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(uicommand.HeaderOperationID, operation)
	request.Header.Set("X-Request-ID", requestUUID.String())
	request.Header.Set("Idempotency-Key", idempotencyUUID.String())
	request.Header.Set("X-CSRF-Token", csrf)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST %s: %v", endpoint, err)
	}
	return response
}

func browserSessionCookie(t *testing.T, jar http.CookieJar, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range jar.Cookies(parsed) {
		if cookie.Name == "lv_session" {
			return cookie.Value
		}
	}
	return ""
}
