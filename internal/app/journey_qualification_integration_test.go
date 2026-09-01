package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	connectionadmin "github.com/flidai/leapview/internal/analytics/connectionadmin"
	"github.com/flidai/leapview/internal/analytics/model"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

// TestJourneyQualificationAssembledRouter protects the highest-risk browser
// wiring seams for FAI-492. It deliberately uses the assembled application
// handler, not feature handlers in isolation: an absent route guard, CSRF
// middleware, or production-shaped callback boundary is therefore observable here.
func TestJourneyQualificationAssembledRouter(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	operator := testPlatformPrincipal(t, ctx, store, "operator@example.com", "Operator")
	viewer := testPrincipal(t, ctx, store, "viewer@example.com", "Viewer")
	operatorToken := testAPIToken(t, ctx, store, operator.ID, "journey-operator")
	viewerToken := testAPIToken(t, ctx, store, viewer.ID, "journey-viewer")
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	options := testStoreOptions(store, assemblyConfig{Auth: auth, AnalyticsModule: analyticsmodule.NewSurface(nil, nil)})

	// The real authoring application is required so /dashboards/new and
	// /dashboards/{dashboard}/fork cannot silently stop at a nil test seam.
	audit, err := newAuditRuntime(store.SQLDB())
	if err != nil {
		t.Fatalf("build audit runtime: %v", err)
	}
	authoring, err := dashboardmodule.BuildAuthoring(dashboardmodule.AuthoringConfig{
		Database:            store.SQLDB(),
		AuditIntentRecorder: audit.recorder,
		AuthorizeResource: func(ctx context.Context, principalID string, projectID projectgraph.ResourceID, resource access.ResourceRef, capability access.Capability) (bool, error) {
			return authorizeProjectResources(ctx, options.AccessModule, options.RuntimeHost, principalID, projectID, []access.ResourceRef{resource}, capability)
		},
		AuthorizeProjectCapability: func(ctx context.Context, principalID string, projectID projectgraph.ResourceID, capability access.Capability) (bool, error) {
			return authorizeProjectRole(ctx, options.AccessModule, options.RuntimeHost, principalID, projectID, capability)
		},
		AcquireRuntime: options.RuntimeHost.Acquire,
	})
	if err != nil {
		t.Fatalf("build authoring application: %v", err)
	}
	options.Authoring = authoring
	server := assembleRuntime(fakeMetrics{}, options)
	server.routes.projectBrowser.ProjectDefinitionReader = journeyDefinitionReader{}
	connectionFake := &journeyConnectionAdministration{denyPrincipal: viewer.ID}
	server.routes.projectBrowser.ConnectionAdministration = connectionFake
	server.routes.projectBrowser.Graph = journeyGraphReader{kind: "connection"}
	handler := server.Routes()

	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if method == http.MethodPost {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	if got := request(http.MethodGet, "/dashboards/new", "invalid", "").Code; got != http.StatusUnauthorized {
		t.Fatalf("invalid bearer dashboard create status = %d, want 401", got)
	}
	if got := request(http.MethodGet, "/dashboards/new", viewerToken, "").Code; got != http.StatusForbidden {
		t.Fatalf("viewer dashboard create status = %d, want 403", got)
	}
	createPage := request(http.MethodGet, "/dashboards/new", operatorToken, "")
	if createPage.Code != http.StatusSeeOther || createPage.Header().Get("Location") != "/?create=dashboard" {
		t.Fatalf("operator dashboard create entry = %d location=%q", createPage.Code, createPage.Header().Get("Location"))
	}
	forkPage := request(http.MethodGet, "/dashboards/executive-sales/fork", operatorToken, "")
	if forkPage.Code != http.StatusOK || !strings.Contains(forkPage.Body.String(), `action="/dashboards/executive-sales/fork"`) || !strings.Contains(forkPage.Body.String(), `name="idempotencyKey"`) {
		t.Fatalf("operator dashboard fork page = %d body=%s", forkPage.Code, forkPage.Body.String())
	}
	// Browser POSTs are protected by the assembled CSRF middleware. Bearer
	// requests are intentionally exempt by Auth, so this no-credential request
	// is the deterministic middleware failure injection.
	if got := request(http.MethodPost, "/dashboards/new", "", "title=Sales&semanticModel=test&idempotencyKey=csrf-check").Code; got != http.StatusForbidden {
		t.Fatalf("dashboard create without CSRF status = %d, want 403", got)
	}
	missingID := request(http.MethodPost, "/dashboards/new", operatorToken, "title=Sales&semanticModel=test")
	if missingID.Code != http.StatusBadRequest || !strings.Contains(missingID.Body.String(), "idempotencyKey") {
		t.Fatalf("dashboard create missing idempotency = %d body=%s", missingID.Code, missingID.Body.String())
	}
	first := request(http.MethodPost, "/dashboards/new", operatorToken, "title=Sales&semanticModel=test&slug=sales&idempotencyKey=journey-create-1")
	if first.Code != http.StatusSeeOther || first.Header().Get("Location") == "" {
		t.Fatalf("dashboard create = %d/%q body=%s", first.Code, first.Header().Get("Location"), first.Body.String())
	}

	// Candidate review has a distinct edit guard from owner preview. A viewer
	// is denied before deployment lookup; the lightweight assembled fixture
	// reports its bounded unavailable diagnostic for the operator path.
	if got := request(http.MethodGet, "/candidates/candidate-journey/review", viewerToken, "").Code; got != http.StatusForbidden {
		t.Fatalf("viewer candidate review status = %d, want 403", got)
	}
	if got := request(http.MethodGet, "/candidates/candidate-journey/review", operatorToken, "").Code; got != http.StatusServiceUnavailable {
		t.Fatalf("operator candidate review status = %d, want bounded dependency diagnostic 503, got %d", got, got)
	}

	// Creator commands use the generated UI operation claims and require a
	// request identity. The assembled browser receives a production-shaped
	// definition snapshot and a recording Administration port so a valid
	// connection command reaches the transport callback rather than a nil seam.
	validConnection := `{"connectionAdmin":{"action":"create","assetId":"connection:test","connectorKind":"duckdb","authenticationMode":"none","host":"localhost","surface":"list"}}`
	connectionMissingID := httptest.NewRequest(http.MethodPost, "/connections/administration/configuration", strings.NewReader(validConnection))
	connectionMissingID.Header.Set("Authorization", "Bearer "+operatorToken)
	connectionMissingID.Header.Set("Content-Type", "application/json")
	connectionMissingID.Header.Set(uicommand.HeaderOperationID, "createTargetConnectionBinding")
	missingConnectionRec := httptest.NewRecorder()
	handler.ServeHTTP(missingConnectionRec, connectionMissingID)
	if missingConnectionRec.Code != http.StatusBadRequest || !strings.Contains(missingConnectionRec.Body.String(), "IDEMPOTENCY_KEY_REQUIRED") {
		t.Fatalf("connection command missing request identity = %d body=%s", missingConnectionRec.Code, missingConnectionRec.Body.String())
	}
	viewerConnection := httptest.NewRequest(http.MethodPost, "/connections/administration/configuration", strings.NewReader(validConnection))
	viewerConnection.Header.Set("Authorization", "Bearer "+viewerToken)
	viewerConnection.Header.Set("Content-Type", "application/json")
	viewerConnection.Header.Set(uicommand.HeaderOperationID, "createTargetConnectionBinding")
	viewerConnection.Header.Set("X-Request-ID", "journey-connection-viewer")
	viewerConnection.Header.Set("Idempotency-Key", "ui:journey-connection-viewer")
	viewerConnectionRec := httptest.NewRecorder()
	handler.ServeHTTP(viewerConnectionRec, viewerConnection)
	if viewerConnectionRec.Code != http.StatusOK || !strings.Contains(viewerConnectionRec.Body.String(), "forbidden") {
		t.Fatalf("viewer connection command = %d body=%s", viewerConnectionRec.Code, viewerConnectionRec.Body.String())
	}
	operatorConnection := httptest.NewRequest(http.MethodPost, "/connections/administration/configuration", strings.NewReader(validConnection))
	operatorConnection.Header.Set("Authorization", "Bearer "+operatorToken)
	operatorConnection.Header.Set("Content-Type", "application/json")
	operatorConnection.Header.Set(uicommand.HeaderOperationID, "createTargetConnectionBinding")
	operatorConnection.Header.Set("X-Request-ID", "journey-connection-operator")
	operatorConnection.Header.Set("Idempotency-Key", "ui:journey-connection-operator")
	operatorConnectionRec := httptest.NewRecorder()
	handler.ServeHTTP(operatorConnectionRec, operatorConnection)
	if operatorConnectionRec.Code != http.StatusOK || !strings.Contains(operatorConnectionRec.Body.String(), "connectionAdmin") {
		t.Fatalf("operator connection command = %d body=%s", operatorConnectionRec.Code, operatorConnectionRec.Body.String())
	}
	if connectionFake.creates != 1 {
		t.Fatalf("operator connection callback calls = %d, want 1 body=%s", connectionFake.creates, operatorConnectionRec.Body.String())
	}
	connectionReplay := httptest.NewRequest(http.MethodPost, "/connections/administration/configuration", strings.NewReader(validConnection))
	connectionReplay.Header = operatorConnection.Header.Clone()
	connectionReplayRec := httptest.NewRecorder()
	handler.ServeHTTP(connectionReplayRec, connectionReplay)
	if connectionReplayRec.Code != http.StatusOK || connectionReplayRec.Header().Get("Idempotency-Replayed") != "true" || connectionFake.creates != 1 {
		t.Fatalf("connection replay = %d replay=%q calls=%d body=%s", connectionReplayRec.Code, connectionReplayRec.Header().Get("Idempotency-Replayed"), connectionFake.creates, connectionReplayRec.Body.String())
	}

	validPipeline := `{"pipelineCommand":{"action":"run","pipelineId":"pipeline:visuals-refresh"}}`
	if got := request(http.MethodPost, "/pipelines/command", "invalid", validPipeline).Code; got != http.StatusUnauthorized {
		t.Fatalf("invalid bearer pipeline command status = %d, want 401", got)
	}
	if got := request(http.MethodPost, "/pipelines/command", "", validPipeline).Code; got != http.StatusForbidden {
		t.Fatalf("pipeline command without CSRF status = %d, want 403", got)
	}
	var pipelineCalls int
	graphUnavailableAfterMutation := false
	server.routes.projectBrowser.Graph = journeyGraphReader{kind: "pipeline", unavailable: &graphUnavailableAfterMutation}
	server.routes.projectBrowser.RunPipeline = func(context.Context, string, string, string) error {
		pipelineCalls++
		graphUnavailableAfterMutation = true
		return nil
	}
	operatorPipeline := httptest.NewRequest(http.MethodPost, "/pipelines/command", strings.NewReader(validPipeline))
	operatorPipeline.Header.Set("Authorization", "Bearer "+operatorToken)
	operatorPipeline.Header.Set("Content-Type", "application/json")
	operatorPipeline.Header.Set(uicommand.HeaderOperationID, "createRefreshRun")
	operatorPipeline.Header.Set("X-Request-ID", "journey-pipeline-operator")
	operatorPipeline.Header.Set("Idempotency-Key", "ui:journey-pipeline-operator")
	operatorPipelineRec := httptest.NewRecorder()
	handler.ServeHTTP(operatorPipelineRec, operatorPipeline)
	if operatorPipelineRec.Code != http.StatusOK || !strings.Contains(operatorPipelineRec.Body.String(), "accepted") || !strings.Contains(operatorPipelineRec.Body.String(), "Reload the page") || pipelineCalls != 1 {
		t.Fatalf("operator pipeline command = %d callbacks=%d body=%s", operatorPipelineRec.Code, pipelineCalls, operatorPipelineRec.Body.String())
	}
	pipelineReplay := httptest.NewRequest(http.MethodPost, "/pipelines/command", strings.NewReader(validPipeline))
	pipelineReplay.Header = operatorPipeline.Header.Clone()
	pipelineReplayRec := httptest.NewRecorder()
	handler.ServeHTTP(pipelineReplayRec, pipelineReplay)
	if pipelineReplayRec.Code != http.StatusOK || pipelineReplayRec.Header().Get("Idempotency-Replayed") != "true" || pipelineCalls != 1 {
		t.Fatalf("pipeline replay = %d replay=%q calls=%d body=%s", pipelineReplayRec.Code, pipelineReplayRec.Header().Get("Idempotency-Replayed"), pipelineCalls, pipelineReplayRec.Body.String())
	}
}

// journeyDefinitionReader supplies only typed, credential-free definition
// data needed by the assembled creator command handlers. It is deliberately
// immutable and contains no auth bypass or retry behavior.
type journeyDefinitionReader struct{}

type journeyGraphReader struct {
	kind        string
	unavailable *bool
}

func (r journeyGraphReader) ActiveServingStateGraph(context.Context, projectgraph.ResourceID, string) (servingstate.AssetGraph, bool, error) {
	if r.unavailable != nil && *r.unavailable {
		return servingstate.AssetGraph{}, false, errors.New("injected graph failure")
	}
	asset := servingstate.Asset{ID: "connection:test", ProjectID: "project:test", ServingStateID: "state:journey", SnapshotID: "snapshot:journey", Type: "connection", Key: "test", Title: "Test connection"}
	if r.kind == "pipeline" {
		asset = servingstate.Asset{ID: "pipeline:visuals-refresh", ProjectID: "project:test", ServingStateID: "state:journey", SnapshotID: "snapshot:journey", Type: "pipeline", Key: "visuals-refresh", Title: "Visuals refresh"}
	}
	return servingstate.AssetGraph{Assets: []servingstate.Asset{asset}}, true, nil
}

func (journeyDefinitionReader) ProjectDefinitionSnapshot(context.Context) (projectmanifest.Project, map[string]*semanticquery.CompiledModel, error) {
	return projectmanifest.Project{
		ID:               "project:test",
		Connections:      map[string]model.Connection{"connection:test": {Kind: "duckdb"}},
		RefreshPipelines: map[string]refreshschedule.Definition{"pipeline:visuals-refresh": {}},
	}, nil, nil
}

type journeyConnectionAdministration struct {
	journeyConnectionAdministrationStub
	denyPrincipal string
	creates       int
}

func (f *journeyConnectionAdministration) Create(_ context.Context, actor string, _ connectionadmin.TargetBindingInput) (connectionadmin.TargetBinding, error) {
	if actor == f.denyPrincipal {
		return connectionadmin.TargetBinding{}, connectionadmin.ErrUnauthorizedBinding
	}
	f.creates++
	return connectionadmin.TargetBinding{}, nil
}

var _ connectionadmin.Administration = (*journeyConnectionAdministration)(nil)
