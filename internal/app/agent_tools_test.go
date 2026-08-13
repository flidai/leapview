package app

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	agentcap "github.com/flidai/leapview/internal/agent"
	agentcontracts "github.com/flidai/leapview/internal/agent/contracts"
	agentmodule "github.com/flidai/leapview/internal/agent/module"
	"github.com/flidai/leapview/internal/agent/productdocs"
	agenttools "github.com/flidai/leapview/internal/agent/tools"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	refreshsqlite "github.com/flidai/leapview/internal/refresh/sqlite"
	"github.com/flidai/leapview/internal/workspace"
	agentcore "github.com/flidai/leapview/pkg/agent"
	toon "github.com/toon-format/toon-go"
)

func agentAPIGenToolsForTest(server *appTestHarness, scope agentcap.Scope) []agentcore.ToolDefinition {
	return server.routes.agentModule.APIGenToolProvider().Definitions(agentmodule.ToolsScope(scope))
}

func agentVisualToolsForTest(server *appTestHarness, scope agentcap.Scope) []agentcore.ToolDefinition {
	return agentVisualToolProviderForTest(server).Definitions(agentmodule.ToolsScope(scope))
}

func TestAgentDocsToolsSearchAndReadEmbeddedDocumentation(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{})
	definitions := server.routes.agentModule.DocsToolProvider().Definitions()
	search, err := definitions[0].Handler.Run(context.Background(), agentcore.ToolCall{
		ID: "docs-search", Name: agenttools.DocsSearchToolName,
		Arguments: json.RawMessage(`{"query":"semantic relationships","path":"concepts","limit":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if search.IsError {
		t.Fatalf("docs_search result = %#v", search.Content)
	}
	searchResult, ok := search.Content.(productdocs.SearchResult)
	if !ok || len(searchResult.Matches) != 1 || searchResult.Count != 1 || !searchResult.HasMore || searchResult.NextCursor == "" {
		t.Fatalf("docs_search content = %#v", search.Content)
	}
	nextArguments, err := json.Marshal(productdocs.SearchRequest{
		Query: "semantic relationships", Path: "concepts", Cursor: searchResult.NextCursor, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	next, err := definitions[0].Handler.Run(context.Background(), agentcore.ToolCall{
		ID: "docs-search-next", Name: agenttools.DocsSearchToolName, Arguments: nextArguments,
	})
	if err != nil || next.IsError {
		t.Fatalf("continued docs_search = %#v, error=%v", next.Content, err)
	}
	nextResult, ok := next.Content.(productdocs.SearchResult)
	if !ok || nextResult.Count != 1 || nextResult.Matches[0].ID == searchResult.Matches[0].ID {
		t.Fatalf("continued docs_search content = %#v", next.Content)
	}

	readArguments, err := json.Marshal(productdocs.ReadRequest{ID: searchResult.Matches[0].ID, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	read, err := definitions[1].Handler.Run(context.Background(), agentcore.ToolCall{
		ID: "docs-read", Name: agenttools.DocsReadToolName, Arguments: readArguments,
	})
	if err != nil {
		t.Fatal(err)
	}
	if read.IsError {
		t.Fatalf("docs_read result = %#v", read.Content)
	}
	readResult, ok := read.Content.(productdocs.ReadResult)
	if !ok || readResult.LineStart != 1 || readResult.LineEnd > 3 || !strings.Contains(readResult.Content, "Semantic models") {
		t.Fatalf("docs_read content = %#v", read.Content)
	}
}

func runAgentVisualToolForTest(server *appTestHarness, ctx context.Context, scope agentcap.Scope, call agentcore.ToolCall) agentcore.ToolResult {
	return agentVisualToolProviderForTest(server).Run(ctx, agentmodule.ToolsScope(scope), call)
}

func agentVisualToolProviderForTest(server *appTestHarness) agenttools.VisualProvider {
	return server.routes.agentModule.VisualToolProvider()
}

func TestAPIGenAgentToolsExposeOnlyGovernedQueryOperations(t *testing.T) {
	server := assembleRuntime(manyRowsMetrics{}, assemblyConfig{WorkspaceID: "test"})
	tools := agentAPIGenToolsForTest(server, agentcap.Scope{WorkspaceID: "test", PrincipalID: "principal"})
	names := map[string]agentcore.ToolDefinition{}
	for _, tool := range tools {
		names[tool.Name] = tool
	}

	for _, want := range []string{"query_dashboard_visual", "query_semantic_model"} {
		if _, ok := names[want]; !ok {
			t.Fatalf("missing APIGen agent tool %q in %#v", want, toolNames(tools))
		}
	}
	if len(names) != 2 {
		t.Fatalf("APIGen tools = %#v, want exact query pair", toolNames(tools))
	}

	var schema struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if err := json.Unmarshal(names["query_semantic_model"].InputSchema, &schema); err != nil {
		t.Fatalf("decode query_semantic_model schema: %v", err)
	}
	if _, ok := schema.Properties["workspace"]; !ok || !slices.Contains(schema.Required, "workspace") {
		t.Fatalf("workspace must remain explicit in every model input: %s", names["query_semantic_model"].InputSchema)
	}
	for _, want := range []string{"model", "dimensions", "measures", "limit", "pageToken"} {
		if _, ok := schema.Properties[want]; !ok {
			t.Fatalf("schema missing query parameter %q: %s", want, names["query_semantic_model"].InputSchema)
		}
	}
}

func TestAgentVisualToolIsCustomAgentOnlyTool(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{WorkspaceID: "test"})
	tools := agentVisualToolsForTest(server, agentcap.Scope{WorkspaceID: "test", PrincipalID: "principal", DevAuthBypass: true})
	if len(tools) != 1 || tools[0].Name != agenttools.QueryVisualToolName || tools[0].Handler == nil {
		t.Fatalf("visual tools = %#v", tools)
	}
	schemaText := string(tools[0].InputSchema)
	for _, forbidden := range []string{`"$ref"`, `"$defs"`, `"definitions"`} {
		if strings.Contains(schemaText, forbidden) {
			t.Fatalf("query_visual schema contains non-portable keyword %s: %s", forbidden, schemaText)
		}
	}
	if !strings.Contains(schemaText, `"filters"`) {
		t.Fatalf("query_visual input schema does not expose governed filters: %s", schemaText)
	}
	var outputSchema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(tools[0].OutputSchema, &outputSchema); err != nil {
		t.Fatalf("decode query_visual output schema: %v", err)
	}
	for _, property := range []string{"queryId", "servingSnapshot", "freshness", "fields", "filters", "completeness", "status", "diagnostics", "signal"} {
		if _, ok := outputSchema.Properties[property]; !ok {
			t.Fatalf("query_visual output schema missing %q: %s", property, tools[0].OutputSchema)
		}
	}
	if _, ok := outputSchema.Properties["patch"]; ok {
		t.Fatalf("query_visual canonical output schema exposes renderer patch: %s", tools[0].OutputSchema)
	}
	for _, tool := range agentAPIGenToolsForTest(server, agentcap.Scope{WorkspaceID: "test", PrincipalID: "principal"}) {
		if tool.Name == agenttools.QueryVisualToolName {
			t.Fatalf("query_visual should not be exposed through APIGen tools")
		}
	}
}

func TestAgentVisualToolAcceptsCatalogReferenceIDs(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{WorkspaceID: "test"})
	result := runAgentVisualToolForTest(
		server,
		context.Background(),
		agentcap.Scope{WorkspaceID: "test", PrincipalID: "principal", DevAuthBypass: true},
		agentcore.ToolCall{
			ID:   "catalog-ref-visual",
			Name: agenttools.QueryVisualToolName,
			Arguments: json.RawMessage(`{
				"workspace":"test",
				"type":"bar",
				"model":"test",
				"dataset":"test.orders",
				"dimensions":[{"field":"test.orders.status"}],
				"measures":[{"field":"test.order_count"}]
			}`),
		},
	)
	if result.IsError {
		t.Fatalf("query_visual rejected catalog refs: %#v", result.Content)
	}
}

func TestAgentAPIGenQueryAuditSurface(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(testStore(t), assemblyConfig{WorkspaceID: "test"}))
	var queryTool agentcore.ToolDefinition
	for _, tool := range agentAPIGenToolsForTest(server, agentcap.Scope{WorkspaceID: "test", PrincipalID: "principal", DevAuthBypass: true}) {
		if tool.Name == "query_semantic_model" {
			queryTool = tool
			break
		}
	}
	if queryTool.Handler == nil {
		t.Fatal("query_semantic_model tool not found")
	}

	result, err := queryTool.Handler.Run(context.Background(), agentcore.ToolCall{
		ID:   "call_agent_query",
		Name: "query_semantic_model",
		Arguments: json.RawMessage(`{
			"workspace":"test",
			"model":"test",
			"dimensions":[{"field":"orders.status","alias":"status"}],
			"measures":[{"field":"order_count"}],
			"limit":1
		}`),
	})
	if err != nil {
		t.Fatalf("run query_semantic_model: %v", err)
	}
	if result.IsError {
		t.Fatalf("query_semantic_model returned error: %#v", result.Content)
	}

	events := queryEventsForTest(t, server, queryaudit.Filter{WorkspaceID: "test", Surface: dataquery.SurfaceAgent})
	if len(events) != 1 {
		t.Fatalf("agent query events = %d, want 1: %#v", len(events), events)
	}
	if events[0].Operation != dataquery.OperationAgentQuery || events[0].ObjectType != "agent_tool" || events[0].RequestID != "call_agent_query" {
		t.Fatalf("agent query event = %#v", events[0])
	}
}

func TestAgentVisualToolReturnsChartPatchFromSemanticData(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{WorkspaceID: "test"})
	tool := agentVisualToolsForTest(server, agentcap.Scope{WorkspaceID: "test", PrincipalID: "principal", DevAuthBypass: true})[0]
	result, err := tool.Handler.Run(context.Background(), agentcore.ToolCall{
		ID:   "call_1",
		Name: "query_visual",
		Arguments: json.RawMessage(`{
			"workspace":"test",
			"model":"test",
			"dataset":"orders",
			"title":"Orders by status",
			"type":"bar",
			"dimensions":[{"field":"orders.status"}],
			"measures":[{"field":"order_count"}],
			"filters":[{"field":"orders.status","operator":"not_contains","values":["cancelled"]}],
			"limit":10
		}`),
	})
	if err != nil {
		t.Fatalf("run query_visual: %v", err)
	}
	if result.IsError {
		t.Fatalf("query_visual returned error: %#v", result.Content)
	}
	normalizedJSON, err := json.Marshal(result.Content)
	if err != nil {
		t.Fatalf("normalize query_visual model result: %v", err)
	}
	var normalized any
	if err := json.Unmarshal(normalizedJSON, &normalized); err != nil {
		t.Fatalf("decode normalized query_visual model result: %v", err)
	}
	toonBody, err := toon.Marshal(normalized)
	if err != nil {
		t.Fatalf("encode query_visual model result as TOON: %v", err)
	}
	if _, err := toon.Decode(toonBody); err != nil {
		t.Fatalf("query_visual model result does not round-trip through TOON: %v\n%s", err, toonBody)
	}
	compact, err := json.Marshal(result.Content)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if strings.Contains(string(compact), "delivered") || strings.Contains(string(compact), `"patch"`) || strings.Contains(string(compact), `"data"`) {
		t.Fatalf("model-visible chart result should be compact: %s", compact)
	}
	var compactResult agentcontracts.QueryVisualResult
	if err := json.Unmarshal(compact, &compactResult); err != nil {
		t.Fatalf("decode compact result: %v body=%s", err, compact)
	}
	if compactResult.ID != "agent_visual_call_1" {
		t.Fatalf("chart artifact id = %q, want call-scoped id", compactResult.ID)
	}
	if compactResult.QueryID != "call_1" || compactResult.ServingSnapshot == "" ||
		compactResult.ModelRef.ID != "test" || compactResult.DatasetRef.ID != "test.orders" ||
		compactResult.Completeness.ReturnedRows != 2 || compactResult.Completeness.Status != "complete" ||
		len(compactResult.Fields) != 2 || compactResult.Fields[0].Role != "dimension" ||
		compactResult.Fields[1].Role != "measure" || compactResult.Fields[1].Label != "Orders" ||
		len(compactResult.Filters) != 1 || compactResult.Filters[0].Ref.ID != "test.orders.status" ||
		compactResult.Filters[0].Operator != "not_contains" || compactResult.Status.Kind != "ready" {
		t.Fatalf("compact chart metadata = %#v", compactResult)
	}
	body, err := json.Marshal(result.DisplayContent)
	if err != nil {
		t.Fatalf("marshal display result: %v", err)
	}
	var decoded struct {
		Type  string `json:"type"`
		ID    string `json:"id"`
		Patch struct {
			Visuals map[string]visualizationir.VisualizationEnvelope `json:"visuals"`
		} `json:"patch"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode result: %v body=%s", err, body)
	}
	if !compactResult.Ok || compactResult.Type != "bar" || compactResult.ID != decoded.ID || compactResult.Signal != "visuals."+decoded.ID {
		t.Fatalf("compact result = %#v decoded=%#v", compactResult, decoded)
	}
	visual := decoded.Patch.Visuals[decoded.ID]
	spec, specOK := visual.Spec.Value.(*visualizationir.CartesianVisualizationSpec)
	state, stateOK := visual.DataState.Value.(*visualizationir.InlineVisualizationDataState)
	if decoded.Type != "bar" || visual.VisualID != decoded.ID || !specOK || spec.Title != "Orders by status" || spec.Mark != visualizationir.VisualizationCartesianMarkBar || !stateOK || len(state.Datasets) != 1 || len(state.Datasets[0].Rows) != 2 {
		t.Fatalf("chart result = %#v visual=%#v", decoded, visual)
	}
	if len(state.Datasets[0].Rows[0]) != 2 || state.Datasets[0].Rows[0][0] == nil || state.Datasets[0].Rows[0][1] == nil {
		t.Fatalf("chart data does not use the typed columnar frame: %#v", state.Datasets[0])
	}
	if len(spec.Interactions) != 0 || len(visual.Selection) != 0 {
		t.Fatalf("chart should not include interactivity: %#v", visual)
	}
}

func TestAgentVisualToolAuthorizesAgainstRequestedDataset(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	repo := testAccessRepository(store)
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "principal_agent_dataset", Email: "agent-dataset@example.com", DisplayName: "Agent Dataset"})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}
	if _, err := repo.CreateGrant(ctx, access.GrantInput{
		Object:      access.ItemObject(access.SecurableSemanticModel, "test", "test"),
		SubjectType: access.SubjectPrincipal,
		SubjectID:   principal.ID,
		Privilege:   access.PrivilegeQueryData,
	}); err != nil {
		t.Fatalf("grant semantic model query: %v", err)
	}
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{WorkspaceID: "test"}))
	tool := agentVisualToolsForTest(server, agentcap.Scope{WorkspaceID: "test", PrincipalID: principal.ID})[0]

	result, err := tool.Handler.Run(context.Background(), agentcore.ToolCall{
		ID:   "call_dataset_auth",
		Name: "query_visual",
		Arguments: json.RawMessage(`{
			"workspace":"test",
			"type":"bar",
			"model":"test",
			"dataset":"orders",
			"title":"Orders by status",
			"type":"bar",
			"dimensions":[{"field":"orders.status"}],
			"measures":[{"field":"order_count"}],
			"limit":10
		}`),
	})
	if err != nil {
		t.Fatalf("run query_visual: %v", err)
	}
	if result.IsError {
		t.Fatalf("query_visual returned error for semantic-model grant: %#v", result.Content)
	}
}

func TestAgentVisualToolReturnsTablePatchFromSemanticData(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{WorkspaceID: "test"})
	tool := agentVisualToolsForTest(server, agentcap.Scope{WorkspaceID: "test", PrincipalID: "principal", DevAuthBypass: true})[0]
	result, err := tool.Handler.Run(context.Background(), agentcore.ToolCall{
		ID:   "call_1",
		Name: "query_visual",
		Arguments: json.RawMessage(`{
			"workspace":"test",
			"type":"table",
			"model":"test",
			"dataset":"orders",
			"title":"Orders",
			"fields":[{"field":"orders.order_id"},{"field":"orders.status"}],
			"limit":10
		}`),
	})
	if err != nil {
		t.Fatalf("run query_visual: %v", err)
	}
	if result.IsError {
		t.Fatalf("query_visual returned error: %#v", result.Content)
	}
	compact, err := json.Marshal(result.Content)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if strings.Contains(string(compact), "delivered") || strings.Contains(string(compact), `"patch"`) || strings.Contains(string(compact), `"rows"`) {
		t.Fatalf("model-visible table result should be compact: %s", compact)
	}
	var compactResult agentcontracts.QueryVisualResult
	if err := json.Unmarshal(compact, &compactResult); err != nil {
		t.Fatalf("decode compact result: %v body=%s", err, compact)
	}
	if compactResult.ID != "agent_visual_call_1" {
		t.Fatalf("table artifact id = %q, want call-scoped id", compactResult.ID)
	}
	if compactResult.QueryID != "call_1" || compactResult.Completeness.ReturnedRows != 2 ||
		len(compactResult.Fields) != 2 || compactResult.Fields[0].Role != "table_field" ||
		compactResult.Fields[0].DataType == nil || compactResult.Status.Kind != "ready" {
		t.Fatalf("compact table metadata = %#v", compactResult)
	}
	body, err := json.Marshal(result.DisplayContent)
	if err != nil {
		t.Fatalf("marshal display result: %v", err)
	}
	var decoded struct {
		Type  string `json:"type"`
		ID    string `json:"id"`
		Patch struct {
			Visuals map[string]visualizationir.VisualizationEnvelope `json:"visuals"`
		} `json:"patch"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode result: %v body=%s", err, body)
	}
	if !compactResult.Ok || compactResult.Type != "table" || compactResult.ID != decoded.ID || compactResult.Signal != "visuals."+decoded.ID {
		t.Fatalf("compact result = %#v decoded=%#v", compactResult, decoded)
	}
	tabular := decoded.Patch.Visuals[decoded.ID]
	table, specOK := tabular.Spec.Value.(*visualizationir.TableVisualizationSpec)
	state, stateOK := tabular.DataState.Value.(*visualizationir.WindowedVisualizationDataState)
	if decoded.Type != "table" || tabular.VisualID != decoded.ID || !specOK || table.Title != "Orders" || len(table.Columns) != 2 || !stateOK || len(state.Blocks["a"].Rows) != 2 {
		t.Fatalf("table result = %#v envelope=%#v", decoded, tabular)
	}
	if len(table.Interactions) != 0 || len(tabular.Selection) != 0 {
		t.Fatalf("table should not include interactivity: %#v", tabular)
	}
}

func TestAgentVisualToolReturnsAggregateTableFromRowsAndMeasures(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{WorkspaceID: "test"})
	tool := agentVisualToolsForTest(server, agentcap.Scope{WorkspaceID: "test", PrincipalID: "principal", DevAuthBypass: true})[0]
	result, err := tool.Handler.Run(context.Background(), agentcore.ToolCall{
		ID:   "call_1",
		Name: "query_visual",
		Arguments: json.RawMessage(`{
			"workspace":"test",
			"type":"table",
			"model":"test",
			"dataset":"orders",
			"title":"Orders by status",
			"rows":[{"field":"orders.status"}],
			"measures":[{"field":"order_count"}],
			"limit":10
		}`),
	})
	if err != nil {
		t.Fatalf("run query_visual: %v", err)
	}
	if result.IsError {
		t.Fatalf("query_visual returned error: %#v", result.Content)
	}
	body, err := json.Marshal(result.DisplayContent)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var decoded struct {
		ID    string `json:"id"`
		Patch struct {
			Visuals map[string]visualizationir.VisualizationEnvelope `json:"visuals"`
		} `json:"patch"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode result: %v body=%s", err, body)
	}
	envelope := decoded.Patch.Visuals[decoded.ID]
	table, specOK := envelope.Spec.Value.(*visualizationir.TableVisualizationSpec)
	state, stateOK := envelope.DataState.Value.(*visualizationir.WindowedVisualizationDataState)
	if !specOK || len(table.Columns) != 2 || table.Columns[0].Field.Field != "status" || table.Columns[1].Field.Field != "order_count" {
		t.Fatalf("aggregate table columns = %#v", envelope.Spec)
	}
	if !stateOK || len(state.Blocks["a"].Rows) == 0 || len(state.Blocks["a"].Rows[0]) != 2 || state.Blocks["a"].Rows[0][1] == nil {
		t.Fatalf("aggregate table rows missing measure: %#v", envelope.DataState)
	}
}

func TestAgentVisualToolUsesToolCallScopedArtifactIDs(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{WorkspaceID: "test"})
	tool := agentVisualToolsForTest(server, agentcap.Scope{WorkspaceID: "test", PrincipalID: "principal", DevAuthBypass: true})[0]
	args := json.RawMessage(`{
		"workspace":"test",
		"model":"test",
		"dataset":"orders",
		"title":"Orders by status",
		"type":"bar",
		"dimensions":[{"field":"orders.status"}],
		"measures":[{"field":"order_count"}],
		"limit":10
	}`)
	first, err := tool.Handler.Run(context.Background(), agentcore.ToolCall{ID: "call_first", Name: "query_visual", Arguments: args})
	if err != nil {
		t.Fatalf("run first query_visual: %v", err)
	}
	second, err := tool.Handler.Run(context.Background(), agentcore.ToolCall{ID: "call_second", Name: "query_visual", Arguments: args})
	if err != nil {
		t.Fatalf("run second query_visual: %v", err)
	}
	firstBody, _ := json.Marshal(first.Content)
	secondBody, _ := json.Marshal(second.Content)
	var firstCompact, secondCompact struct {
		ID     string `json:"id"`
		Signal string `json:"signal"`
	}
	if err := json.Unmarshal(firstBody, &firstCompact); err != nil {
		t.Fatalf("decode first compact: %v", err)
	}
	if err := json.Unmarshal(secondBody, &secondCompact); err != nil {
		t.Fatalf("decode second compact: %v", err)
	}
	if firstCompact.ID == secondCompact.ID || firstCompact.Signal == secondCompact.Signal {
		t.Fatalf("identical requests reused artifact identity: first=%#v second=%#v", firstCompact, secondCompact)
	}
	if firstCompact.ID != "agent_visual_call_first" || secondCompact.ID != "agent_visual_call_second" {
		t.Fatalf("unexpected call-scoped IDs: first=%#v second=%#v", firstCompact, secondCompact)
	}
}

func TestAgentVisualToolRejectsInlineDataAndInteractions(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{WorkspaceID: "test"})
	for _, args := range []string{
		`{"type":"bar","model":"test","dataset":"orders","data":[{"label":"x","value":1}],"measures":[{"field":"order_count"}]}`,
		`{"type":"bar","model":"test","dataset":"orders","filter":{"field":"orders.status","values":["open"]},"measures":[{"field":"order_count"}]}`,
		`{"type":"table","model":"test","dataset":"orders","interaction":{"row_selection":{}},"fields":[{"field":"orders.order_id"}]}`,
	} {
		result := runAgentVisualToolForTest(server, context.Background(), agentcap.Scope{WorkspaceID: "test", PrincipalID: "principal", DevAuthBypass: true}, agentcore.ToolCall{ID: "call_1", Name: "query_visual", Arguments: json.RawMessage(args)})
		if !result.IsError {
			t.Fatalf("query_visual accepted forbidden input %s: %#v", args, result.Content)
		}
	}
}

func TestAPIGenAgentToolsExposeTypeSpecArgumentNamesAndBodyFields(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{WorkspaceID: "test"})
	tools := agentAPIGenToolsForTest(server, agentcap.Scope{WorkspaceID: "test", PrincipalID: "principal"})
	names := map[string]agentcore.ToolDefinition{}
	for _, tool := range tools {
		names[tool.Name] = tool
		schemaText := string(tool.InputSchema)
		for _, forbidden := range []string{`"example"`, `"$ref"`, `"$defs"`, `"oneOf"`, `"anyOf"`, `"allOf"`} {
			if strings.Contains(schemaText, forbidden) {
				t.Fatalf("%s schema contains non-portable keyword %s: %s", tool.Name, forbidden, schemaText)
			}
		}
	}
	for toolName, wantProps := range map[string][]string{
		"query_dashboard_visual": {"workspace", "dashboard", "page", "visual", "limit", "pageToken"},
		"query_semantic_model":   {"workspace", "model", "dimensions", "measures", "filters", "sort", "limit", "pageToken"},
	} {
		var schema struct {
			Properties map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(names[toolName].InputSchema, &schema); err != nil {
			t.Fatalf("decode %s schema: %v", toolName, err)
		}
		for _, want := range wantProps {
			if _, ok := schema.Properties[want]; !ok {
				t.Fatalf("%s schema missing %q: %s", toolName, want, names[toolName].InputSchema)
			}
		}
		for _, forbidden := range []string{"dashboard_id", "model_id", "page_id", "table_id"} {
			if _, ok := schema.Properties[forbidden]; ok {
				t.Fatalf("%s schema exposes rewritten arg %q: %s", toolName, forbidden, names[toolName].InputSchema)
			}
		}
	}
}

func TestAPIGenAgentOperationsDeclareOutputMetadata(t *testing.T) {
	for _, operation := range agentAPIGenOperations() {
		if operation.Tool.Output.Mode == "" || len(operation.Tool.OutputSchema) == 0 {
			t.Fatalf("agent operation %s (%s) has no typed output contract", operation.Contract.OperationID, operation.Tool.Name)
		}
		if operation.Tool.ResponseContentType != "application/json" {
			t.Fatalf("agent operation %s (%s) response content type = %q, want application/json", operation.Contract.OperationID, operation.Tool.Name, operation.Tool.ResponseContentType)
		}
	}
}

func TestAPIGenVisualToolKeepsRESTEnvelopeAndUsesProviderProjection(t *testing.T) {
	for _, operation := range agentAPIGenOperations() {
		if operation.Tool.Name != "query_dashboard_visual" {
			continue
		}
		if operation.Tool.Output.Mode != "raw" || len(operation.Tool.Output.Select) != 0 {
			t.Fatalf("visual REST operation output = %#v, want raw discriminated union", operation.Tool.Output)
		}
		return
	}
	t.Fatal("query_dashboard_visual tool missing")
}

func TestAPIGenAgentToolDispatchesTabularVisualQuery(t *testing.T) {
	server := assembleRuntime(manyRowsMetrics{}, assemblyConfig{WorkspaceID: "test"})
	tools := agentAPIGenToolsForTest(server, agentcap.Scope{WorkspaceID: "test", PrincipalID: "principal"})
	var queryVisual agentcore.ToolDefinition
	for _, tool := range tools {
		if tool.Name == "query_dashboard_visual" {
			queryVisual = tool
			break
		}
	}
	if queryVisual.Handler == nil {
		t.Fatal("query_dashboard_visual tool missing")
	}
	result, err := queryVisual.Handler.Run(context.Background(), agentcore.ToolCall{
		ID:        "call_1",
		Name:      "query_dashboard_visual",
		Arguments: json.RawMessage(`{"workspace":"test","dashboard":"executive-sales","page":"overview","visual":"order_rows","limit":50}`),
	})
	if err != nil {
		t.Fatalf("run tool: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %#v", result.Content)
	}
	body, err := json.Marshal(result.Content)
	if err != nil {
		t.Fatalf("marshal result content: %v", err)
	}
	var table struct {
		QueryID         string `json:"queryId"`
		ServingSnapshot string `json:"servingSnapshot"`
		VisualID        string `json:"visualId"`
		Title           string `json:"title"`
		Type            string `json:"type"`
		Columns         []struct {
			ID       string `json:"id"`
			Label    string `json:"label"`
			Role     string `json:"role"`
			DataType string `json:"dataType"`
		} `json:"columns"`
		Rows         [][]any `json:"rows"`
		Completeness struct {
			ReturnedRows  int    `json:"returnedRows"`
			AvailableRows int    `json:"availableRows"`
			Cardinality   string `json:"cardinality"`
		} `json:"completeness"`
		HasMore    bool   `json:"hasMore"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(body, &table); err != nil {
		t.Fatalf("decode table result: %v\n%s", err, body)
	}
	if table.QueryID != "call_1" || table.ServingSnapshot == "" || table.VisualID != "order_rows" ||
		table.Title == "" || table.Type != "table" || len(table.Columns) == 0 || len(table.Rows) != 50 ||
		table.Completeness.ReturnedRows != 50 || table.Completeness.AvailableRows != 500 ||
		!table.HasMore || table.NextCursor == "" {
		t.Fatalf("compact table result = %#v", table)
	}
	next, err := queryVisual.Handler.Run(context.Background(), agentcore.ToolCall{
		ID:   "call_2",
		Name: "query_dashboard_visual",
		Arguments: json.RawMessage(fmt.Sprintf(
			`{"workspace":"test","dashboard":"executive-sales","page":"overview","visual":"order_rows","limit":50,"pageToken":%q}`,
			table.NextCursor,
		)),
	})
	if err != nil || next.IsError {
		t.Fatalf("continue tool result=%#v err=%v", next, err)
	}
	nextBody, _ := json.Marshal(next.Content)
	var nextPage struct {
		Rows         [][]any `json:"rows"`
		Completeness struct {
			ReturnedRows int `json:"returnedRows"`
		} `json:"completeness"`
		HasMore bool `json:"hasMore"`
	}
	if err := json.Unmarshal(nextBody, &nextPage); err != nil {
		t.Fatalf("decode continued table result: %v body=%s", err, nextBody)
	}
	if len(nextPage.Rows) != 50 || nextPage.Rows[0][0] != "order-50" ||
		nextPage.Completeness.ReturnedRows != 50 || !nextPage.HasMore {
		t.Fatalf("continued compact table result = %#v", nextPage)
	}
	var tableMap map[string]any
	if err := json.Unmarshal(body, &tableMap); err != nil {
		t.Fatalf("decode table map: %v", err)
	}
	for _, forbidden := range []string{"spec", "dataState", "rendererID", "selection", "style", "rendererOptions"} {
		if _, ok := tableMap[forbidden]; ok {
			t.Fatalf("table result kept noisy field %q: %#v", forbidden, tableMap)
		}
	}
}

func TestAPIGenAgentToolFetchesSingleDashboardVisualData(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{WorkspaceID: "test"})
	tools := agentAPIGenToolsForTest(server, agentcap.Scope{WorkspaceID: "test", PrincipalID: "principal"})
	var queryVisual agentcore.ToolDefinition
	for _, tool := range tools {
		if tool.Name == "query_dashboard_visual" {
			queryVisual = tool
			break
		}
	}
	if queryVisual.Handler == nil {
		t.Fatal("query_dashboard_visual tool missing")
	}
	catalog, err := agentcore.NewToolCatalog(tools)
	if err != nil {
		t.Fatalf("compile agent tool catalog: %v", err)
	}
	result, err := catalog.Execute(context.Background(), agentcore.ToolCall{
		ID:   "call_1",
		Name: "query_dashboard_visual",
		Arguments: json.RawMessage(`{
			"workspace":"test",
			"dashboard":"executive-sales",
			"page":"overview",
			"visual":"orders"
		}`),
	})
	if err != nil {
		t.Fatalf("run tool: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %#v", result.Content)
	}
	body, err := json.Marshal(result.Content)
	if err != nil {
		t.Fatalf("marshal result content: %v", err)
	}
	var visual struct {
		QueryID  string `json:"queryId"`
		VisualID string `json:"visualId"`
		Title    string `json:"title"`
		Type     string `json:"type"`
		Mark     string `json:"mark"`
		Columns  []struct {
			ID       string `json:"id"`
			Label    string `json:"label"`
			Role     string `json:"role"`
			DataType string `json:"dataType"`
		} `json:"columns"`
		Rows         [][]any `json:"rows"`
		Completeness struct {
			ReturnedRows int    `json:"returnedRows"`
			Cardinality  string `json:"cardinality"`
		} `json:"completeness"`
		Status struct {
			Kind string `json:"kind"`
		} `json:"status"`
		AppliedFilters struct {
			Controls map[string]any `json:"controls"`
		} `json:"appliedFilters"`
	}
	if err := json.Unmarshal(body, &visual); err != nil {
		t.Fatalf("decode visual result: %v body=%s", err, body)
	}
	if visual.QueryID != "call_1" || visual.VisualID != "orders" || visual.Title != "Orders" ||
		visual.Type != "proportional" || visual.Mark != "donut" || len(visual.Columns) == 0 ||
		len(visual.Rows) != 1 || visual.Completeness.ReturnedRows != 1 ||
		visual.Status.Kind != "ready" || visual.AppliedFilters.Controls == nil {
		t.Fatalf("visual result = %#v", visual)
	}
}

func TestAPIGenAgentSemanticQueryToolInjectsBodyDefaultLimit(t *testing.T) {
	server := assembleRuntime(manySemanticRowsMetrics{}, assemblyConfig{WorkspaceID: "test"})
	tools := agentAPIGenToolsForTest(server, agentcap.Scope{WorkspaceID: "test", PrincipalID: "principal"})
	var querySemantic agentcore.ToolDefinition
	for _, tool := range tools {
		if tool.Name == "query_semantic_model" {
			querySemantic = tool
			break
		}
	}
	if querySemantic.Handler == nil {
		t.Fatal("query_semantic_model tool missing")
	}
	result, err := querySemantic.Handler.Run(context.Background(), agentcore.ToolCall{
		ID:        "call_1",
		Name:      "query_semantic_model",
		Arguments: json.RawMessage(`{"workspace":"test","model":"test","dimensions":[{"field":"orders.status","alias":"status"}],"measures":[{"field":"order_count"}],"sort":[{"field":"status","direction":"asc"}]}`),
	})
	if err != nil {
		t.Fatalf("run tool: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %#v", result.Content)
	}
	body, err := json.Marshal(result.Content)
	if err != nil {
		t.Fatalf("marshal result content: %v", err)
	}
	var decoded struct {
		Columns []struct {
			Name     string `json:"name"`
			Label    string `json:"label"`
			Kind     string `json:"kind"`
			DataType string `json:"dataType"`
			FieldRef struct {
				WorkspaceID string `json:"workspaceId"`
				Type        string `json:"type"`
				ID          string `json:"id"`
			} `json:"fieldRef"`
		} `json:"columns"`
		QueryID         string  `json:"queryId"`
		ServingSnapshot string  `json:"servingSnapshot"`
		Rows            [][]any `json:"rows"`
		Completeness    struct {
			ReturnedRows int  `json:"returnedRows"`
			HasMore      bool `json:"hasMore"`
		} `json:"completeness"`
		HasMore    bool   `json:"hasMore"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode semantic result: %v body=%s", err, body)
	}
	if len(decoded.Columns) == 0 || len(decoded.Rows) != 25 ||
		decoded.Completeness.ReturnedRows != 25 || !decoded.Completeness.HasMore ||
		decoded.QueryID != "call_1" || decoded.ServingSnapshot == "" ||
		!decoded.HasMore || decoded.NextCursor == "" {
		t.Fatalf("semantic default-limited result = %#v", decoded)
	}
	if decoded.Columns[0].Kind != "dimension" || decoded.Columns[0].DataType != "string" ||
		decoded.Columns[0].FieldRef.Type != "field" || decoded.Columns[0].FieldRef.ID != "test.orders.status" ||
		decoded.Columns[1].Kind != "measure" || decoded.Columns[1].FieldRef.Type != "measure" ||
		decoded.Columns[1].FieldRef.ID != "test.order_count" {
		t.Fatalf("semantic columns = %#v", decoded.Columns)
	}
	var decodedMap map[string]any
	if err := json.Unmarshal(body, &decodedMap); err != nil {
		t.Fatalf("decode semantic map: %v", err)
	}
	if _, ok := decodedMap["page"]; ok {
		t.Fatalf("semantic result kept raw page metadata: %#v", decodedMap)
	}
}

func TestAPIGenAgentSemanticQueryReturnsLastSuccessfulFreshness(t *testing.T) {
	store := testStore(t)
	server := assembleRuntime(manySemanticRowsMetrics{}, testStoreOptions(store, assemblyConfig{WorkspaceID: "test"}))
	if _, err := store.SQLDB().ExecContext(context.Background(), `
		INSERT INTO serving_states (id, workspace_id, status, digest, manifest_json, environment)
		VALUES ('unversioned', 'test', 'active', 'test', '{}', 'dev');
	`); err != nil {
		t.Fatalf("seed freshness serving state: %v", err)
	}
	refreshedAt := time.Date(2026, time.July, 24, 9, 42, 0, 0, time.UTC)
	if err := refreshsqlite.NewRepository(store.SQLDB()).SaveDataVersion(context.Background(), refreshschedule.DataVersion{
		WorkspaceID: "test", Environment: "dev", SemanticModel: "test",
		SnapshotID: 184, ServingStateID: "unversioned", RefreshedAt: refreshedAt,
		Source: refreshschedule.DataVersionSourceRefresh,
	}); err != nil {
		t.Fatalf("save data version: %v", err)
	}
	var querySemantic agentcore.ToolDefinition
	for _, tool := range agentAPIGenToolsForTest(server, agentcap.Scope{
		WorkspaceID: "test", PrincipalID: "principal", DevAuthBypass: true,
	}) {
		if tool.Name == "query_semantic_model" {
			querySemantic = tool
			break
		}
	}
	result, err := querySemantic.Handler.Run(context.Background(), agentcore.ToolCall{
		ID: "freshness_query", Name: "query_semantic_model",
		Arguments: json.RawMessage(`{
			"workspace":"test",
			"model":"test",
			"measures":[{"field":"order_count"}],
			"limit":1
		}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("query result=%#v err=%v", result, err)
	}
	body, _ := json.Marshal(result.Content)
	var decoded struct {
		Freshness struct {
			LastSuccessfulRefreshAt string `json:"lastSuccessfulRefreshAt"`
			SnapshotID              string `json:"snapshotId"`
			ServingStateID          string `json:"servingStateId"`
			Source                  string `json:"source"`
			Status                  string `json:"status"`
		} `json:"freshness"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode result: %v body=%s", err, body)
	}
	if decoded.Freshness.LastSuccessfulRefreshAt != "2026-07-24T09:42:00Z" ||
		decoded.Freshness.SnapshotID != "184" ||
		decoded.Freshness.ServingStateID != "unversioned" ||
		decoded.Freshness.Source != "refresh" ||
		decoded.Freshness.Status != "current" {
		t.Fatalf("freshness = %#v", decoded.Freshness)
	}

	var queryDashboardVisual agentcore.ToolDefinition
	for _, tool := range agentAPIGenToolsForTest(server, agentcap.Scope{
		WorkspaceID: "test", PrincipalID: "principal", DevAuthBypass: true,
	}) {
		if tool.Name == "query_dashboard_visual" {
			queryDashboardVisual = tool
			break
		}
	}
	visualResult, err := queryDashboardVisual.Handler.Run(context.Background(), agentcore.ToolCall{
		ID:   "freshness_visual",
		Name: "query_dashboard_visual",
		Arguments: json.RawMessage(`{
			"workspace":"test",
			"dashboard":"executive-sales",
			"page":"overview",
			"visual":"orders"
		}`),
	})
	if err != nil || visualResult.IsError {
		t.Fatalf("visual result=%#v err=%v", visualResult, err)
	}
	visualBody, _ := json.Marshal(visualResult.Content)
	var visualDecoded struct {
		Freshness struct {
			LastSuccessfulRefreshAt string `json:"lastSuccessfulRefreshAt"`
			SnapshotID              string `json:"snapshotId"`
			ServingStateID          string `json:"servingStateId"`
			Source                  string `json:"source"`
			Status                  string `json:"status"`
		} `json:"freshness"`
	}
	if err := json.Unmarshal(visualBody, &visualDecoded); err != nil {
		t.Fatalf("decode visual result: %v body=%s", err, visualBody)
	}
	if !reflect.DeepEqual(visualDecoded.Freshness, decoded.Freshness) {
		t.Fatalf("visual freshness = %#v, semantic freshness = %#v", visualDecoded.Freshness, decoded.Freshness)
	}

	generatedResult := runAgentVisualToolForTest(
		server,
		context.Background(),
		agentcap.Scope{WorkspaceID: "test", PrincipalID: "principal", DevAuthBypass: true},
		agentcore.ToolCall{
			ID: "freshness_generated_visual", Name: "query_visual",
			Arguments: json.RawMessage(`{
				"workspace":"test",
				"model":"test",
				"dataset":"orders",
				"type":"bar",
				"dimensions":[{"field":"orders.status"}],
				"measures":[{"field":"order_count"}]
			}`),
		},
	)
	if generatedResult.IsError {
		t.Fatalf("generated visual result=%#v", generatedResult.Content)
	}
	generatedBody, _ := json.Marshal(generatedResult.Content)
	var generatedDecoded struct {
		ServingSnapshot string `json:"servingSnapshot"`
		Freshness       struct {
			LastSuccessfulRefreshAt string `json:"lastSuccessfulRefreshAt"`
			SnapshotID              string `json:"snapshotId"`
			ServingStateID          string `json:"servingStateId"`
			Source                  string `json:"source"`
			Status                  string `json:"status"`
		} `json:"freshness"`
	}
	if err := json.Unmarshal(generatedBody, &generatedDecoded); err != nil {
		t.Fatalf("decode generated visual result: %v body=%s", err, generatedBody)
	}
	if generatedDecoded.ServingSnapshot != "unversioned" || !reflect.DeepEqual(generatedDecoded.Freshness, decoded.Freshness) {
		t.Fatalf("generated visual freshness = %#v, semantic freshness = %#v", generatedDecoded, decoded.Freshness)
	}
}

func TestAPIGenAgentSemanticQueryPreservesNullCells(t *testing.T) {
	server := assembleRuntime(nullSemanticRowsMetrics{}, assemblyConfig{WorkspaceID: "test"})
	var querySemantic agentcore.ToolDefinition
	for _, tool := range agentAPIGenToolsForTest(server, agentcap.Scope{
		WorkspaceID: "test", PrincipalID: "principal", DevAuthBypass: true,
	}) {
		if tool.Name == "query_semantic_model" {
			querySemantic = tool
			break
		}
	}
	result, err := querySemantic.Handler.Run(context.Background(), agentcore.ToolCall{
		ID: "null_query", Name: "query_semantic_model",
		Arguments: json.RawMessage(`{
			"workspace":"test",
			"model":"test",
			"dimensions":[{"field":"orders.status"}],
			"measures":[{"field":"order_count"}],
			"limit":2
		}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("query result=%#v err=%v", result, err)
	}
	body, _ := json.Marshal(result.Content)
	var decoded struct {
		Rows [][]any `json:"rows"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode result: %v body=%s", err, body)
	}
	if len(decoded.Rows) != 2 || decoded.Rows[0][0] != nil || decoded.Rows[1][0] != "" {
		t.Fatalf("null and empty string collapsed: %#v", decoded.Rows)
	}
}

func TestAPIGenAgentToolEnforcesCredentialPrivilegeAllowlistAndWorkspace(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	principal := testPrincipal(t, ctx, store, "agent-token@example.com", "Agent Token", access.RoleOwner)
	agentOnlyToken := access.APIToken{WorkspaceID: "test", Privileges: []access.Privilege{access.PrivilegeUseAgent}}
	queryToken := access.APIToken{WorkspaceID: "test", Privileges: []access.Privilege{access.PrivilegeUseAgent, access.PrivilegeQueryData}}
	foreignToken := access.APIToken{WorkspaceID: "other", Privileges: []access.Privilege{access.PrivilegeQueryData}}
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{AccessRepo: testAccessRepository(store), WorkspaceID: "test"}))

	run := func(token access.APIToken) agentcore.ToolResult {
		scope := agentcap.Scope{
			WorkspaceID: "test",
			PrincipalID: principal.ID,
			Credential: agentcap.CredentialScope{
				WorkspaceID: token.WorkspaceID,
				Privileges:  testPrivilegeStrings(token.Privileges),
				Restricted:  token.Privileges != nil,
			},
		}
		tools := agentAPIGenToolsForTest(server, scope)
		for _, tool := range tools {
			if tool.Name == "query_semantic_model" {
				result, err := tool.Handler.Run(ctx, agentcore.ToolCall{ID: "call_1", Name: "query_semantic_model", Arguments: json.RawMessage(`{"workspace":"test","model":"test","measures":[{"field":"order_count"}]}`)})
				if err != nil {
					t.Fatalf("run query_semantic_model: %v", err)
				}
				return result
			}
		}
		t.Fatal("query_semantic_model tool missing")
		return agentcore.ToolResult{}
	}

	if result := run(agentOnlyToken); !result.IsError {
		t.Fatalf("agent-only token unexpectedly called query tool: %#v", result.Content)
	}
	if result := run(foreignToken); !result.IsError {
		t.Fatalf("foreign workspace token unexpectedly called query tool: %#v", result.Content)
	}
	if result := run(queryToken); result.IsError {
		t.Fatalf("query token was rejected: %#v", result.Content)
	}
}

func TestRuntimeAgentToolsMatchPolicyRegistry(t *testing.T) {
	server := assembleRuntime(manyRowsMetrics{}, assemblyConfig{WorkspaceID: "test"})
	scope := agentcap.Scope{WorkspaceID: "test", PrincipalID: "principal"}
	runtimeTools := server.routes.agentModule.ToolDefinitions(scope)
	if got, want := sortedToolNames(runtimeTools), agenttools.ToolNames(agentAPIGenOperations()); !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime tools = %#v, policy registry = %#v", got, want)
	}
}

func TestAdminAgentInspectionExposesExactCuratedCatalog(t *testing.T) {
	store := testStore(t)
	metrics := manyRowsMetrics{}
	service := agentcap.NewService(testAgentRepository(store), agentcap.Config{APIKey: "key", Model: "test-model"})
	server := assembleRuntime(metrics, testStoreOptions(store, assemblyConfig{Agent: service, WorkspaceID: "test"}))
	details, err := server.routes.agentModule.HTTP().AdminDetails(context.Background())
	if err != nil {
		t.Fatalf("admin agent details: %v", err)
	}
	names := make([]string, 0, len(details.Tools))
	for _, tool := range details.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	if want := agenttools.ToolNames(agentAPIGenOperations()); !reflect.DeepEqual(names, want) {
		t.Fatalf("admin tools = %#v, want %#v", names, want)
	}
}

func toolNames(tools []agentcore.ToolDefinition) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func sortedToolNames(tools []agentcore.ToolDefinition) []string {
	names := toolNames(tools)
	sort.Strings(names)
	return names
}

type fakeAssetCatalogReader struct {
	catalog workspace.AssetCatalog
	ok      bool
	err     error
}

func (f fakeAssetCatalogReader) ActiveAssetCatalog(_ context.Context, _ workspace.WorkspaceID, _ string) (workspace.AssetCatalog, bool, error) {
	if f.err != nil {
		return workspace.AssetCatalog{}, false, f.err
	}
	ok := f.ok
	if !ok && (len(f.catalog.Assets) > 0 || len(f.catalog.Edges) > 0) {
		ok = true
	}
	return f.catalog, ok, nil
}

func testAgentAssetCatalog(t *testing.T) workspace.AssetCatalog {
	t.Helper()
	workspaceID := workspace.WorkspaceID("test")
	servingStateID := workspace.ServingStateID("deploy_a")
	dashboard, err := workspace.NewAsset(workspaceID, servingStateID, workspace.AssetTypeDashboard, "executive-sales", "", "Executive Sales", "", "dashboard.v1", map[string]any{"semantic_model": "olist"})
	if err != nil {
		t.Fatalf("dashboard asset: %v", err)
	}
	measure, err := workspace.NewAsset(workspaceID, servingStateID, workspace.AssetTypeMeasure, "olist.revenue", "", "Revenue", "", "measure.v1", map[string]any{"table": "orders"})
	if err != nil {
		t.Fatalf("measure asset: %v", err)
	}
	visual, err := workspace.NewAsset(workspaceID, servingStateID, workspace.AssetTypeVisual, "executive-sales.revenue", dashboard.ID, "Revenue", "", "visual.v1", map[string]any{"query_kind": "aggregate"})
	if err != nil {
		t.Fatalf("visual asset: %v", err)
	}
	graph := workspace.AssetGraph{
		Assets: []workspace.Asset{dashboard, measure, visual},
		Edges: []workspace.AssetEdge{
			workspace.NewAssetEdge(workspaceID, servingStateID, dashboard.ID, visual.ID, workspace.AssetEdgeContains),
			workspace.NewAssetEdge(workspaceID, servingStateID, visual.ID, measure.ID, workspace.AssetEdgeUsesMeasure),
		},
	}
	catalog, err := workspace.DecodeAssetCatalog(graph)
	if err != nil {
		t.Fatalf("decode asset catalog: %v", err)
	}
	return catalog
}

func testAgentAssetCatalogFromProvider(t *testing.T, provider workspaceAssetGraphProvider) workspace.AssetCatalog {
	t.Helper()
	assets, edges, ok := provider.WorkspaceAssets("test", "deploy_a")
	if !ok {
		t.Fatal("workspace assets unavailable")
	}
	catalog, err := workspace.DecodeAssetCatalog(workspace.AssetGraph{Assets: assets, Edges: edges})
	if err != nil {
		t.Fatalf("decode asset catalog: %v", err)
	}
	return catalog
}

func stringSliceHas(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func testPrivilegeStrings(values []access.Privilege) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, string(value))
	}
	return out
}

type manyEdgesMetrics struct {
	fakeMetrics
}

type manySemanticRowsMetrics struct {
	fakeMetrics
}

type nullSemanticRowsMetrics struct {
	fakeMetrics
}

func (m nullSemanticRowsMetrics) ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
	if request.Kind != dataquery.KindSemanticAggregate {
		return m.fakeMetrics.ExecuteDataQuery(ctx, request)
	}
	return dataquery.Result{
		Columns: dataquery.ColumnsFromNames([]string{"status"}),
		Rows: []dataquery.Row{
			{"status": nil},
			{"status": ""},
		},
	}, nil
}

func (manySemanticRowsMetrics) QuerySemantic(_ context.Context, _ string, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	rows := make(reportdef.QueryRows, 0, request.Limit)
	for i := 0; i < request.Limit; i++ {
		rows = append(rows, reportdef.QueryRow{"status": "s" + strconv.Itoa(i), "order_count": i})
	}
	return rows, nil
}

func (m manySemanticRowsMetrics) ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
	if request.Kind != dataquery.KindSemanticAggregate {
		return m.fakeMetrics.ExecuteDataQuery(ctx, request)
	}
	rows, err := m.QuerySemantic(ctx, request.ModelID, reportdef.AggregateQuery{Limit: request.Limit})
	if err != nil {
		return dataquery.Result{}, err
	}
	out := make([]dataquery.Row, 0, len(rows))
	for _, row := range rows {
		out = append(out, dataquery.Row(row))
	}
	return dataquery.Result{Columns: dataquery.ColumnsFromNames([]string{"status", "order_count"}), Rows: out}, nil
}

func (manyEdgesMetrics) WorkspaceAssets(workspaceID, servingStateID string) ([]workspace.Asset, []workspace.AssetEdge, bool) {
	root, err := workspace.NewAsset(workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID), workspace.AssetTypeCatalog, "catalog", "", "Catalog", "", "catalog.v1", map[string]any{})
	if err != nil {
		return nil, nil, false
	}
	assets := []workspace.Asset{root}
	edges := make([]workspace.AssetEdge, 0, 30)
	for i := 0; i < 30; i++ {
		key := "dashboard-" + strconv.Itoa(i)
		child, err := workspace.NewAsset(workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID), workspace.AssetTypeDashboard, key, root.ID, "Dashboard", "", "dashboard.v1", map[string]any{"index": i})
		if err != nil {
			return nil, nil, false
		}
		assets = append(assets, child)
		edges = append(edges, workspace.NewAssetEdge(workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID), root.ID, child.ID, workspace.AssetEdgeContains))
	}
	return assets, edges, true
}
