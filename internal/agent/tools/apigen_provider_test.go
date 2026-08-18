package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

func TestAPIGenDefinitionsUseBoundProjectContext(t *testing.T) {
	var authorizedScope Scope
	var dispatchedPath string
	provider := APIGenProvider{
		Operations: curatedTestAPIGenOperations(),
		Authorize: func(_ context.Context, scope Scope, _ string) (agentcore.ToolResult, bool) {
			authorizedScope = scope
			return agentcore.ToolResult{}, true
		},
		Dispatch: func(_ Scope, _ string, writer http.ResponseWriter, request *http.Request) bool {
			dispatchedPath = request.URL.Path
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusOK)
			return true
		},
	}

	var definition agentcore.ToolDefinition
	for _, candidate := range provider.Definitions(Scope{ProjectID: "project_demo", PrincipalID: "principal-1"}) {
		if candidate.Name == "query_semantic_model" {
			definition = candidate
			break
		}
	}
	if definition.Name == "" {
		t.Fatal("query_semantic_model definition not found")
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if err := json.Unmarshal(definition.InputSchema, &schema); err != nil {
		t.Fatalf("decode input schema: %v", err)
	}
	if _, ok := schema.Properties["project"]; ok || containsString(schema.Required, "project") {
		t.Fatalf("input schema = %s, must not expose project selector", definition.InputSchema)
	}
	limit, _ := schema.Properties["limit"].(map[string]any)
	if limit["maximum"] != float64(maxAgentQueryRows) {
		t.Fatalf("agent query limit maximum = %#v, want %d", limit["maximum"], maxAgentQueryRows)
	}

	result, err := definition.Handler.Run(context.Background(), agentcore.ToolCall{ID: "call-1", Arguments: json.RawMessage(`{"model":"orders"}`)})
	if err != nil {
		t.Fatalf("run tool: %v", err)
	}
	if result.IsError && !strings.Contains(dispatchedPath, "/api/v1/projects/project_demo/") {
		t.Fatalf("tool result = %#v", result)
	}
	if authorizedScope.ProjectID != "project_demo" {
		t.Fatalf("authorized project = %q, want project_demo", authorizedScope.ProjectID)
	}
	if dispatchedPath != "/api/v1/projects/project_demo/semantic-models/orders/query" {
		t.Fatalf("dispatched path = %q", dispatchedPath)
	}
}

func TestAPIGenDefinitionsExposeCompactDashboardVisualOutputSchema(t *testing.T) {
	for _, definition := range (APIGenProvider{Operations: curatedTestAPIGenOperations()}).Definitions(Scope{PrincipalID: "principal-1"}) {
		if definition.Name != "query_dashboard_visual" {
			continue
		}
		if len(definition.OutputSchema) >= 24*1024 {
			t.Fatalf("dashboard visual output schema is %d bytes, want compact schema under 24 KiB", len(definition.OutputSchema))
		}
		var schema map[string]any
		if err := json.Unmarshal(definition.OutputSchema, &schema); err != nil {
			t.Fatalf("decode output schema: %v", err)
		}
		if schema["type"] != "object" {
			t.Fatalf("output schema type = %#v, want object: %s", schema["type"], definition.OutputSchema)
		}
		if schema["additionalProperties"] != false {
			t.Fatalf("output schema is not closed: %s", definition.OutputSchema)
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("output schema properties = %#v", schema["properties"])
		}
		for _, property := range []string{
			"queryId", "servingSnapshot", "visualId", "title", "type", "columns", "rows",
			"appliedFilters", "status", "diagnostics", "completeness", "hasMore", "nextCursor",
		} {
			if _, ok := properties[property]; !ok {
				t.Fatalf("compact output schema missing %q: %s", property, definition.OutputSchema)
			}
		}
		columns, _ := properties["columns"].(map[string]any)
		items, _ := columns["items"].(map[string]any)
		columnProperties, _ := items["properties"].(map[string]any)
		for _, property := range []string{"id", "sourceRef", "label", "role", "dataType", "nullable", "format", "grain"} {
			if _, ok := columnProperties[property]; !ok {
				t.Fatalf("compact column schema missing %q: %#v", property, items)
			}
		}
		for _, property := range []string{"spec", "dataState", "rendererID", "selection"} {
			if _, ok := properties[property]; ok {
				t.Fatalf("compact output schema kept renderer field %q: %s", property, definition.OutputSchema)
			}
		}
		return
	}
	t.Fatal("query_dashboard_visual definition not found")
}

func TestAPIGenDefinitionsExposeSemanticQueryMetadataSchema(t *testing.T) {
	for _, definition := range (APIGenProvider{Operations: curatedTestAPIGenOperations()}).Definitions(Scope{PrincipalID: "principal-1"}) {
		if definition.Name != "query_semantic_model" {
			continue
		}
		var schema map[string]any
		if err := json.Unmarshal(definition.OutputSchema, &schema); err != nil {
			t.Fatalf("decode output schema: %v", err)
		}
		if schema["additionalProperties"] != false {
			t.Fatalf("semantic output schema is not closed: %s", definition.OutputSchema)
		}
		properties, _ := schema["properties"].(map[string]any)
		for _, name := range []string{"queryId", "servingSnapshot", "freshness", "completeness", "columns", "rows", "hasMore"} {
			if _, ok := properties[name]; !ok {
				t.Fatalf("semantic output schema missing %q: %s", name, definition.OutputSchema)
			}
		}
		completeness, _ := properties["completeness"].(map[string]any)
		if completeness["additionalProperties"] != false {
			t.Fatalf("completeness schema is not closed: %#v", completeness)
		}
		completenessProperties, _ := completeness["properties"].(map[string]any)
		for _, name := range []string{"returnedRows", "hasMore"} {
			if _, ok := completenessProperties[name]; !ok {
				t.Fatalf("completeness schema missing %q: %#v", name, completeness)
			}
		}
		columns, _ := properties["columns"].(map[string]any)
		columnItems, _ := columns["items"].(map[string]any)
		if columnItems["additionalProperties"] != false {
			t.Fatalf("column schema is not closed: %#v", columnItems)
		}
		columnProperties, _ := columnItems["properties"].(map[string]any)
		for _, name := range []string{"name", "nullable", "fieldRef", "label", "kind", "dataType", "unit", "format"} {
			if _, ok := columnProperties[name]; !ok {
				t.Fatalf("column schema missing %q: %#v", name, columnItems)
			}
		}
		return
	}
	t.Fatal("query_semantic_model definition not found")
}

func TestCuratedQueryArgumentsAcceptCatalogReferenceIDs(t *testing.T) {
	semantic := normalizeCuratedQueryArguments("query_semantic_model", json.RawMessage(`{
		"model":"sales",
		"dimensions":[{"field":"sales.orders.status"}],
		"metrics":[{"field":"sales.order_count"}],
		"filters":[{"dataset":"sales.orders","field":"sales.orders.state","groups":[{"filters":[{"field":"sales.orders.city"}]}]}]
	}`))
	var semanticInput map[string]any
	if err := json.Unmarshal(semantic, &semanticInput); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(semanticInput)
	for _, want := range []string{`"field":"orders.status"`, `"field":"order_count"`, `"dataset":"orders"`, `"field":"orders.city"`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("normalized semantic arguments missing %s: %s", want, encoded)
		}
	}

	visual := normalizeCuratedQueryArguments("query_dashboard_visual", json.RawMessage(`{
		"dashboard":"executive-sales",
		"page":"executive-sales.overview",
		"visual":"executive-sales.revenue_kpi"
	}`))
	if string(visual) != `{"dashboard":"executive-sales","page":"overview","visual":"revenue_kpi"}` {
		t.Fatalf("normalized dashboard arguments = %s", visual)
	}
}

func TestVisualDefinitionUsesBoundProjectContext(t *testing.T) {
	var authorizedScope Scope
	provider := VisualProvider{
		Resolve: func(_ context.Context, _ Scope, id projectgraph.ResourceID, _ projectgraph.Kind, _ access.Capability) (projectgraph.ResourceID, error) {
			return id, nil
		},
		Authorize: func(_ context.Context, scope Scope, _ VisualAuthorizationRequest) (agentcore.ToolResult, bool) {
			authorizedScope = scope
			return apigenAgentToolError("authorization_failed", "stop after scope capture"), false
		},
	}
	definition := provider.Definitions(Scope{ProjectID: "project_demo", PrincipalID: "principal-1"})[0]
	var schema struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if err := json.Unmarshal(definition.InputSchema, &schema); err != nil {
		t.Fatalf("decode input schema: %v", err)
	}
	if _, ok := schema.Properties["project"]; ok || containsString(schema.Required, "project") {
		t.Fatalf("visual schema = %s, must not expose project selector", definition.InputSchema)
	}
	_, err := definition.Handler.Run(context.Background(), agentcore.ToolCall{
		ID:        "call-visual",
		Arguments: json.RawMessage(`{"type":"bar","semanticModelId":"orders","dataset":"orders"}`),
	})
	if err != nil {
		t.Fatalf("run tool: %v", err)
	}
	if authorizedScope.ProjectID != "project_demo" {
		t.Fatalf("authorized project = %q, want project_demo", authorizedScope.ProjectID)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
