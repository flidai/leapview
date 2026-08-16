package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringapplication "github.com/flidai/leapview/internal/dashboard/authoring/application"
	authoringcompileradapter "github.com/flidai/leapview/internal/dashboard/authoring/compileradapter"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	authoringsqlite "github.com/flidai/leapview/internal/dashboard/authoring/sqlite"
	"github.com/flidai/leapview/internal/platform"
	projectmodule "github.com/flidai/leapview/internal/project/module"
	projectsqlite "github.com/flidai/leapview/internal/project/sqlite"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/internal/workspace"
)

func TestMCPIntegrationCallsEveryAdvertisedAgentTool(t *testing.T) {
	harness := newStoreBackedHarness(t)
	authoringApp := newIntegrationAuthoringApplication(t, harness.store, harness.runtime)
	auth := NewAuth(testAccessRepository(harness.store), AuthConfig{DevBypass: true})
	server := assembleRuntime(harness.metrics, testStoreOptions(harness.store, assemblyConfig{
		Auth: auth, Authoring: authoringApp, WorkspaceID: harness.workspaceID,
	}))
	harness.handler = server.Routes()
	called := map[string]bool{}

	listed := mcpRequest(t, harness.handler, "dev", "2025-11-25", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	if listed.Code != http.StatusOK {
		t.Fatalf("list MCP tools: status=%d body=%s", listed.Code, listed.Body.String())
	}
	var listResponse struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &listResponse); err != nil {
		t.Fatalf("decode MCP tools: %v", err)
	}

	root := callAgentToolThroughMCP(t, harness.handler, called, "catalog_list", map[string]any{})
	rootItems := integrationArray(t, root, "items")
	if integrationInt(t, root, "count") != 1 || len(rootItems) != 1 {
		t.Fatalf("catalog_list root = %#v, want one workspace", root)
	}
	workspaceItem := integrationObjectValue(t, rootItems[0], "catalog_list workspace")
	workspaceRef := integrationObject(t, workspaceItem, "ref")
	if integrationString(t, workspaceRef, "type") != "workspace" || integrationString(t, workspaceRef, "id") != "sales" {
		t.Fatalf("catalog_list workspace ref = %#v, want sales workspace", workspaceRef)
	}

	search := callAgentToolThroughMCP(t, harness.handler, called, "catalog_search", map[string]any{
		"query": "dashboard",
		"types": []string{"dashboard"},
	})
	searchItems := integrationArray(t, search, "items")
	if integrationInt(t, search, "count") != 1 || len(searchItems) != 1 {
		t.Fatalf("typed catalog_search = %#v, want one dashboard", search)
	}
	searchItem := integrationObjectValue(t, searchItems[0], "catalog_search item")
	if integrationString(t, searchItem, "name") != "Executive Sales" {
		t.Fatalf("catalog_search item = %#v, want Executive Sales", searchItem)
	}

	dashboardRef := map[string]any{"workspaceId": "sales", "type": "dashboard", "id": "executive-sales"}
	get := callAgentToolThroughMCP(t, harness.handler, called, "catalog_get", map[string]any{"ref": dashboardRef})
	getItem := integrationObject(t, get, "item")
	getDetails := integrationObject(t, get, "details")
	if integrationString(t, getItem, "name") != "Executive Sales" || integrationString(t, getDetails, "type") != "dashboard" {
		t.Fatalf("catalog_get dashboard = %#v", get)
	}

	semantic := callAgentToolThroughMCP(t, harness.handler, called, "query_semantic_model", map[string]any{
		"workspace":  "sales",
		"model":      "sales",
		"dimensions": []map[string]any{{"field": "orders.status"}},
		"measures":   []map[string]any{{"field": "order_count"}},
		"limit":      10,
	})
	if integrationString(t, semantic, "queryId") == "" || len(integrationArray(t, semantic, "columns")) != 2 || len(integrationArray(t, semantic, "rows")) == 0 {
		t.Fatalf("query_semantic_model result = %#v", semantic)
	}

	dashboardVisual := callAgentToolThroughMCP(t, harness.handler, called, "query_dashboard_visual", map[string]any{
		"workspace": "sales",
		"dashboard": "executive-sales",
		"page":      "overview",
		"visual":    "total_orders",
		"limit":     10,
	})
	if integrationString(t, dashboardVisual, "visualId") != "total_orders" || len(integrationArray(t, dashboardVisual, "rows")) == 0 {
		t.Fatalf("query_dashboard_visual result = %#v", dashboardVisual)
	}
	if status := integrationObject(t, dashboardVisual, "status"); integrationString(t, status, "kind") != "ready" {
		t.Fatalf("query_dashboard_visual status = %#v", status)
	}

	visual := callAgentToolThroughMCP(t, harness.handler, called, "query_visual", map[string]any{
		"workspace":  "sales",
		"type":       "bar",
		"model":      "sales",
		"dataset":    "orders",
		"dimensions": []map[string]any{{"field": "orders.status"}},
		"measures":   []map[string]any{{"field": "order_count"}},
		"limit":      10,
	})
	if ok, _ := visual["ok"].(bool); !ok || integrationString(t, visual, "queryId") == "" || len(integrationArray(t, visual, "fields")) != 2 {
		t.Fatalf("query_visual result = %#v", visual)
	}
	if modelRef := integrationObject(t, visual, "modelRef"); integrationString(t, modelRef, "id") != "sales" {
		t.Fatalf("query_visual model ref = %#v", modelRef)
	}

	docs := callAgentToolThroughMCP(t, harness.handler, called, "docs_search", map[string]any{
		"query": "semantic models",
		"limit": 1,
	})
	matches := integrationArray(t, docs, "matches")
	if len(matches) != 1 {
		t.Fatalf("docs_search result = %#v, want one match", docs)
	}
	docID := integrationString(t, integrationObjectValue(t, matches[0], "docs_search match"), "id")
	read := callAgentToolThroughMCP(t, harness.handler, called, "docs_read", map[string]any{
		"id": docID, "limit": 5,
	})
	if integrationString(t, read, "id") != docID || !strings.Contains(integrationString(t, read, "content"), "Semantic model") {
		t.Fatalf("docs_read result = %#v", read)
	}

	authoringList := callAgentToolThroughMCP(t, harness.handler, called, "list_dashboards", map[string]any{"workspace": "sales"})
	if integrationInt(t, authoringList, "count") < 1 || len(integrationArray(t, authoringList, "items")) < 1 {
		t.Fatalf("list_dashboards result = %#v", authoringList)
	}
	authoringGet := callAgentToolThroughMCP(t, harness.handler, called, "get_dashboard", map[string]any{
		"workspace": "sales", "dashboard": "executive-sales",
	})
	if integrationString(t, integrationObject(t, authoringGet, "dashboard"), "id") != "executive-sales" {
		t.Fatalf("get_dashboard result = %#v", authoringGet)
	}

	created := callAgentToolThroughMCP(t, harness.handler, called, "create_dashboard_draft", map[string]any{
		"workspace": "sales", "title": "MCP Authoring Integration", "semanticModel": "sales",
		"dashboardId": "mcp-authoring-integration", "slug": "mcp-authoring-integration",
	})
	dashboardID, draftID := integrationDraftIdentity(t, created)
	revision := integrationRevision(t, created)
	if integrationString(t, integrationObject(t, created, "lifecycle"), "title") != "MCP Authoring Integration" {
		t.Fatalf("create_dashboard_draft result = %#v", created)
	}

	draft := callAgentToolThroughMCP(t, harness.handler, called, "get_dashboard_draft", map[string]any{
		"workspace": "sales", "dashboard": dashboardID,
	})
	if integrationString(t, integrationObject(t, draft, "revision"), "id") != integrationString(t, revision, "revisionId") {
		t.Fatalf("initial draft revision = %#v want %#v", draft, revision)
	}

	addedPage := callAgentToolThroughMCP(t, harness.handler, called, "add_dashboard_page", map[string]any{
		"workspace": "sales", "dashboardId": dashboardID, "draftId": draftID, "expectedRevision": revision,
		"pageId": "detail", "title": "Detail",
	})
	revision = integrationRevision(t, addedPage)
	if integrationInt(t, revision, "number") <= 1 {
		t.Fatalf("add_dashboard_page revision did not advance: %#v", addedPage)
	}

	addedVisual := callAgentToolThroughMCP(t, harness.handler, called, "add_dashboard_visual", map[string]any{
		"workspace": "sales", "dashboardId": dashboardID, "draftId": draftID, "expectedRevision": revision,
		"pageId": "detail", "visualId": "orders", "componentId": "orders-card", "type": "bar", "title": "Orders",
	})
	revision = integrationRevision(t, addedVisual)

	assignedDimension := callAgentToolThroughMCP(t, harness.handler, called, "assign_dashboard_field", map[string]any{
		"workspace": "sales", "dashboardId": dashboardID, "draftId": draftID, "expectedRevision": revision,
		"pageId": "detail", "visualId": "orders-card", "fieldId": "orders.status", "role": "dimension",
	})
	revision = integrationRevision(t, assignedDimension)
	assignedMeasure := callAgentToolThroughMCP(t, harness.handler, called, "assign_dashboard_field", map[string]any{
		"workspace": "sales", "dashboardId": dashboardID, "draftId": draftID, "expectedRevision": revision,
		"pageId": "detail", "visualId": "orders-card", "fieldId": "order_count", "role": "measure",
	})
	revision = integrationRevision(t, assignedMeasure)

	draft = callAgentToolThroughMCP(t, harness.handler, called, "get_dashboard_draft", map[string]any{
		"workspace": "sales", "dashboard": dashboardID,
	})
	draftRevision := integrationObject(t, draft, "revision")
	document := integrationObject(t, draftRevision, "document")
	pages := integrationArray(t, document, "Pages")
	if len(pages) != 2 {
		t.Fatalf("draft pages after builder edits = %#v", pages)
	}
	var detailPage map[string]any
	for _, candidate := range pages {
		page := integrationObjectValue(t, candidate, "draft page")
		if integrationString(t, page, "id") == "detail" {
			detailPage = page
			break
		}
	}
	if detailPage == nil {
		t.Fatalf("draft pages omitted detail page = %#v", pages)
	}
	pageVisuals := integrationArray(t, detailPage, "visuals")
	if len(pageVisuals) != 1 || integrationString(t, integrationObjectValue(t, pageVisuals[0], "draft visual"), "visual") != "orders" {
		t.Fatalf("draft visuals after builder edits = %#v", pageVisuals)
	}

	visibility := callAgentToolThroughMCP(t, harness.handler, called, "set_dashboard_visibility", map[string]any{
		"workspace": "sales", "dashboardId": dashboardID, "draftId": draftID, "expectedRevision": revision,
		"visibility": "shared",
	})
	revision = integrationRevision(t, visibility)
	if integrationString(t, integrationObject(t, visibility, "lifecycle"), "visibility") != "shared" {
		t.Fatalf("set_dashboard_visibility result = %#v", visibility)
	}

	preview := callAgentToolThroughMCP(t, harness.handler, called, "preview_dashboard_draft", map[string]any{
		"workspace": "sales", "dashboard": dashboardID, "draftId": draftID, "expectedRevision": revision, "page": "detail",
	})
	if integrationString(t, integrationObject(t, preview, "revision"), "revisionId") != integrationString(t, revision, "revisionId") || len(integrationObject(t, preview, "definition")) == 0 {
		t.Fatalf("preview_dashboard_draft result = %#v", preview)
	}

	published := callAgentToolThroughMCP(t, harness.handler, called, "execute_dashboard_command", map[string]any{
		"workspace": "sales", "dashboardId": dashboardID, "draftId": draftID, "expectedRevision": revision,
		"publish": map[string]any{},
	})
	if integrationString(t, integrationObject(t, published, "lifecycle"), "status") != "published" {
		t.Fatalf("execute_dashboard_command publish result = %#v", published)
	}

	forked := callAgentToolThroughMCP(t, harness.handler, called, "fork_dashboard", map[string]any{
		"sourceKind": "workspace", "sourceWorkspace": "sales", "sourceDashboard": dashboardID,
		"targetWorkspace": "sales", "title": "MCP Forked Dashboard", "slug": "mcp-forked-dashboard",
	})
	forkedID, _ := integrationDraftIdentity(t, forked)
	if forkedID == dashboardID || integrationString(t, integrationObject(t, forked, "lifecycle"), "title") != "MCP Forked Dashboard" {
		t.Fatalf("fork_dashboard result = %#v", forked)
	}

	exported := callAgentToolThroughMCP(t, harness.handler, called, "export_dashboard_yaml", map[string]any{
		"sourceKind": "workspace", "workspace": "sales", "dashboard": dashboardID,
	})
	if !strings.Contains(integrationString(t, exported, "yaml"), "MCP Authoring Integration") {
		t.Fatalf("export_dashboard_yaml result = %#v", exported)
	}

	advertised := make([]string, 0, len(listResponse.Result.Tools))
	for _, tool := range listResponse.Result.Tools {
		advertised = append(advertised, tool.Name)
		if !called[tool.Name] {
			t.Errorf("advertised agent tool %q has no integration call", tool.Name)
		}
	}
	slices.Sort(advertised)
	if len(called) != len(advertised) {
		t.Fatalf("called tools = %#v, advertised tools = %#v", called, advertised)
	}
}

type integrationAuthoringAuthorizer struct{}

func (integrationAuthoringAuthorizer) Authorize(context.Context, authoringservice.AuthorizationRequest) error {
	return nil
}

type integrationAuthoringLease struct {
	runtime    runtimehost.Runtime
	servingID  servingstate.ID
	releaseCnt int
}

func (l *integrationAuthoringLease) Runtime() runtimehost.Runtime { return l.runtime }

func (l *integrationAuthoringLease) ServingStateID() servingstate.ID { return l.servingID }

func (l *integrationAuthoringLease) DuckLakeSnapshotID() int64 { return 0 }

func (l *integrationAuthoringLease) Release() { l.releaseCnt++ }

func newIntegrationAuthoringApplication(t *testing.T, store *platform.Store, runtime runtimehost.Runtime) *authoringapplication.Application {
	t.Helper()
	repository := authoringsqlite.NewRepository(store.SQLDB())
	authorizer := integrationAuthoringAuthorizer{}
	acquireRuntime := func(context.Context) (runtimehost.Lease, error) {
		return &integrationAuthoringLease{runtime: runtime, servingID: "integration-serving-state"}, nil
	}
	compiler, err := authoringcompileradapter.New(authoringcompileradapter.Options{AcquireRuntime: acquireRuntime})
	if err != nil {
		t.Fatalf("build integration authoring compiler: %v", err)
	}
	dashboardNumber, draftNumber, revisionNumber := 0, 0, 0
	newDashboardID := func() (authoring.DashboardID, error) {
		dashboardNumber++
		return authoring.DashboardID(fmt.Sprintf("integration-dashboard-%d", dashboardNumber)), nil
	}
	newDraftID := func() (authoring.DraftID, error) {
		draftNumber++
		return authoring.DraftID(fmt.Sprintf("integration-draft-%d", draftNumber)), nil
	}
	newRevisionID := func() (authoring.RevisionID, error) {
		revisionNumber++
		return authoring.RevisionID(fmt.Sprintf("integration-revision-%d", revisionNumber)), nil
	}
	service, err := authoringservice.NewService(authoringservice.Options{
		Repository: repository, Authorizer: authorizer, Compiler: compiler, Now: func() time.Time { return time.Now().UTC() },
		NewDashboardID: newDashboardID, NewDraftID: newDraftID, NewRevisionID: newRevisionID,
	})
	if err != nil {
		t.Fatalf("build integration authoring service: %v", err)
	}
	application, err := authoringapplication.New(authoringapplication.Options{
		Authoring: service, Repository: repository, Authorizer: authorizer, AcquireRuntime: acquireRuntime,
		ExportDashboard: projectmodule.ExportDashboard,
	})
	if err != nil {
		t.Fatalf("build integration authoring application: %v", err)
	}
	return application
}

func integrationRevision(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	revision := integrationObject(t, result, "revision")
	for _, key := range []string{"revisionId", "number", "contentHash"} {
		if revision[key] == nil {
			t.Fatalf("revision missing %q: %#v", key, revision)
		}
	}
	return revision
}

func integrationDraftIdentity(t *testing.T, result map[string]any) (dashboardID, draftID string) {
	t.Helper()
	lifecycle := integrationObject(t, result, "lifecycle")
	dashboardID = integrationString(t, lifecycle, "id")
	draft := integrationObject(t, lifecycle, "draft")
	draftID = integrationString(t, draft, "id")
	return dashboardID, draftID
}

func TestMCPIntegrationAgentToolContractMatrix(t *testing.T) {
	harness := newStoreBackedHarness(t)

	t.Run("invalid arguments use one MCP tool error shape", func(t *testing.T) {
		cases := []struct {
			name      string
			arguments map[string]any
		}{
			{name: "catalog_search", arguments: map[string]any{}},
			{name: "catalog_list", arguments: map[string]any{"childTypes": []string{"dashboard"}}},
			{name: "catalog_get", arguments: map[string]any{}},
			{name: "query_semantic_model", arguments: map[string]any{}},
			{name: "query_dashboard_visual", arguments: map[string]any{}},
			{name: "query_visual", arguments: map[string]any{}},
			{name: "docs_search", arguments: map[string]any{}},
			{name: "docs_read", arguments: map[string]any{}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				code := callAgentToolErrorThroughMCP(t, harness.handler, tc.name, tc.arguments)
				if code != "invalid_arguments" && code != "tool_validation_failed" {
					t.Fatalf("%s invalid argument code = %q", tc.name, code)
				}
			})
		}
	})

	t.Run("empty search is a successful empty page", func(t *testing.T) {
		result := callAgentToolThroughMCP(t, harness.handler, map[string]bool{}, "catalog_search", map[string]any{
			"query": "definitely-no-such-leapview-resource-7db4c2",
		})
		if integrationInt(t, result, "count") != 0 || len(integrationArray(t, result, "items")) != 0 || integrationBool(t, result, "hasMore") {
			t.Fatalf("empty catalog search = %#v", result)
		}
	})

	t.Run("unknown resources and query failures are tool errors", func(t *testing.T) {
		cases := []struct {
			name      string
			arguments map[string]any
		}{
			{name: "catalog_get", arguments: map[string]any{"ref": map[string]any{"workspaceId": "sales", "type": "dashboard", "id": "missing-dashboard"}}},
			{name: "query_semantic_model", arguments: map[string]any{"workspace": "sales", "model": "missing-model", "measures": []map[string]any{{"field": "order_count"}}}},
			{name: "query_dashboard_visual", arguments: map[string]any{"workspace": "sales", "dashboard": "executive-sales", "page": "overview", "visual": "missing-visual"}},
			{name: "query_visual", arguments: map[string]any{"workspace": "sales", "type": "bar", "model": "sales", "dataset": "orders", "measures": []map[string]any{{"field": "missing_measure"}}}},
			{name: "docs_read", arguments: map[string]any{"id": "doc:missing/integration-document.md"}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if code := callAgentToolErrorThroughMCP(t, harness.handler, tc.name, tc.arguments); code == "" || code == "invalid_arguments" {
					t.Fatalf("%s runtime failure code = %q", tc.name, code)
				}
			})
		}
	})

	t.Run("catalog and query cursors continue without repeating rows", func(t *testing.T) {
		parent := map[string]any{"workspaceId": "sales", "type": "workspace", "id": "sales"}
		firstCatalog := callAgentToolThroughMCP(t, harness.handler, map[string]bool{}, "catalog_list", map[string]any{
			"parent": parent, "limit": 1,
		})
		if !integrationBool(t, firstCatalog, "hasMore") || integrationString(t, firstCatalog, "nextCursor") == "" {
			t.Fatalf("first catalog page = %#v", firstCatalog)
		}
		secondCatalog := callAgentToolThroughMCP(t, harness.handler, map[string]bool{}, "catalog_list", map[string]any{
			"parent": parent, "limit": 1, "cursor": integrationString(t, firstCatalog, "nextCursor"),
		})
		firstRef := integrationObject(t, integrationObjectValue(t, integrationArray(t, firstCatalog, "items")[0], "first catalog item"), "ref")
		secondRef := integrationObject(t, integrationObjectValue(t, integrationArray(t, secondCatalog, "items")[0], "second catalog item"), "ref")
		if integrationString(t, firstRef, "id") == integrationString(t, secondRef, "id") {
			t.Fatalf("catalog cursor repeated item: first=%#v second=%#v", firstRef, secondRef)
		}

		query := map[string]any{
			"workspace": "sales", "model": "sales",
			"dimensions": []map[string]any{{"field": "orders.status", "alias": "status"}},
			"measures":   []map[string]any{{"field": "order_count"}},
			"sort":       []map[string]any{{"field": "status", "direction": "asc"}},
			"limit":      1,
		}
		firstQuery := callAgentToolThroughMCP(t, harness.handler, map[string]bool{}, "query_semantic_model", query)
		if !integrationBool(t, firstQuery, "hasMore") || integrationString(t, firstQuery, "nextCursor") == "" {
			t.Fatalf("first semantic page = %#v", firstQuery)
		}
		query["pageToken"] = integrationString(t, firstQuery, "nextCursor")
		secondQuery := callAgentToolThroughMCP(t, harness.handler, map[string]bool{}, "query_semantic_model", query)
		firstRows := integrationArray(t, firstQuery, "rows")
		secondRows := integrationArray(t, secondQuery, "rows")
		if len(firstRows) != 1 || len(secondRows) != 1 || jsonObjectsEqualValue(firstRows[0], secondRows[0]) {
			t.Fatalf("semantic cursor pages = first %#v second %#v", firstRows, secondRows)
		}
	})
}

func TestApplicationCompositionCatalogListsPersistedWorkspaces(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	application, err := Build(ctx, agentToolIntegrationConfig(home))
	if err != nil {
		t.Fatalf("build application: %v", err)
	}
	t.Cleanup(func() { _ = application.Shutdown(context.Background()) })

	store, err := platform.Open(ctx, filepath.Join(home, "leapview.db"))
	if err != nil {
		t.Fatalf("open application store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repository := projectsqlite.NewRepository(store.SQLDB())
	if err := repository.Ensure(ctx, workspace.EnsureInput{ID: "sales", Title: "Sales Workspace"}); err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}

	root := callAgentToolThroughMCP(t, application.Handler(), map[string]bool{}, "catalog_list", map[string]any{})
	items := integrationArray(t, root, "items")
	if integrationInt(t, root, "count") != 1 || len(items) != 1 {
		t.Fatalf("production-composition catalog_list = %#v, want persisted workspace", root)
	}
	item := integrationObjectValue(t, items[0], "catalog_list workspace")
	if integrationString(t, integrationObject(t, item, "ref"), "id") != "sales" {
		t.Fatalf("production-composition catalog_list item = %#v", item)
	}
}

func callAgentToolThroughMCP(t *testing.T, handler http.Handler, called map[string]bool, name string, arguments map[string]any) map[string]any {
	t.Helper()
	called[name] = true
	envelope := callAgentToolMCPEnvelope(t, handler, name, arguments)
	if envelope.Error != nil || envelope.Result.IsError || envelope.Result.StructuredContent == nil {
		t.Fatalf("call %s failed: %#v", name, envelope)
	}
	if len(envelope.Result.Content) != 1 {
		t.Fatalf("call %s content blocks = %d, want 1", name, len(envelope.Result.Content))
	}
	var textContent map[string]any
	if err := json.Unmarshal([]byte(envelope.Result.Content[0].Text), &textContent); err != nil {
		t.Fatalf("decode %s text content: %v", name, err)
	}
	if !jsonObjectsEqual(envelope.Result.StructuredContent, textContent) {
		t.Fatalf("%s structured and text content differ", name)
	}
	return envelope.Result.StructuredContent
}

type agentToolMCPEnvelope struct {
	Result struct {
		IsError           bool           `json:"isError"`
		StructuredContent map[string]any `json:"structuredContent"`
		Content           []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
	Error any `json:"error"`
}

func callAgentToolMCPEnvelope(t *testing.T, handler http.Handler, name string, arguments map[string]any) agentToolMCPEnvelope {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      name,
		"method":  "tools/call",
		"params": map[string]any{
			"name": name, "arguments": arguments,
		},
	})
	if err != nil {
		t.Fatalf("encode %s call: %v", name, err)
	}
	response := mcpRequest(t, handler, "dev", "2025-11-25", string(body))
	if response.Code != http.StatusOK {
		t.Fatalf("call %s: status=%d body=%s", name, response.Code, response.Body.String())
	}
	var envelope agentToolMCPEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode %s response: %v body=%s", name, err, response.Body.String())
	}
	return envelope
}

func callAgentToolErrorThroughMCP(t *testing.T, handler http.Handler, name string, arguments map[string]any) string {
	t.Helper()
	envelope := callAgentToolMCPEnvelope(t, handler, name, arguments)
	if envelope.Error != nil || !envelope.Result.IsError || envelope.Result.StructuredContent != nil || len(envelope.Result.Content) != 1 {
		t.Fatalf("%s error envelope = %#v", name, envelope)
	}
	var content struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(envelope.Result.Content[0].Text), &content); err != nil {
		t.Fatalf("decode %s tool error: %v", name, err)
	}
	if content.Error.Code == "" || content.Error.Message == "" {
		t.Fatalf("%s tool error content = %#v", name, content)
	}
	return content.Error.Code
}

func integrationObject(t *testing.T, value map[string]any, key string) map[string]any {
	t.Helper()
	return integrationObjectValue(t, value[key], key)
}

func integrationObjectValue(t *testing.T, value any, label string) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v, want object", label, value)
	}
	return object
}

func integrationArray(t *testing.T, value map[string]any, key string) []any {
	t.Helper()
	items, ok := value[key].([]any)
	if !ok {
		t.Fatalf("%s = %#v, want array in %#v", key, value[key], value)
	}
	return items
}

func integrationString(t *testing.T, value map[string]any, key string) string {
	t.Helper()
	text, ok := value[key].(string)
	if !ok {
		t.Fatalf("%s = %#v, want string in %#v", key, value[key], value)
	}
	return text
}

func integrationInt(t *testing.T, value map[string]any, key string) int {
	t.Helper()
	number, ok := value[key].(float64)
	if !ok {
		t.Fatalf("%s = %#v, want number in %#v", key, value[key], value)
	}
	return int(number)
}

func integrationBool(t *testing.T, value map[string]any, key string) bool {
	t.Helper()
	boolean, ok := value[key].(bool)
	if !ok {
		t.Fatalf("%s = %#v, want boolean in %#v", key, value[key], value)
	}
	return boolean
}

func jsonObjectsEqualValue(left, right any) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
}

func agentToolIntegrationConfig(home string) config.Config {
	return config.Config{
		HomeDir:                     home,
		ManagedDataBackend:          "local",
		ManagedDataDir:              filepath.Join(home, "managed-data"),
		ManagedDataMaxFiles:         100,
		ManagedDataMaxFileBytes:     1 << 20,
		ManagedDataMaxRevisionBytes: 10 << 20,
		ManagedDataUploadSessionTTL: time.Hour,
		ManagedDataGCInterval:       time.Hour,
		ManagedDataGCGracePeriod:    time.Hour,
		ManagedDataMinFreeBytes:     1,
		DuckDBNodeMemoryMaxBytes:    256 << 20,
		DuckDBNodeTempMaxBytes:      1 << 30,
		DuckDBNodeMaxThreads:        2,
		QueryResultMaxRows:          10_000,
		QueryResultMaxBytes:         32 << 20,
		QueryCacheRuntimeMaxEntries: 16,
		QueryCacheRuntimeMaxBytes:   4 << 20,
		QueryCacheNodeMaxEntries:    64,
		QueryCacheNodeMaxBytes:      16 << 20,
	}
}
