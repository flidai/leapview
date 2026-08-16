package tools

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/Yacobolo/toolbelt/apigen/runtime/agenttool"
)

func TestAPIGenOperationsUseGeneratedReadOnlyToolContracts(t *testing.T) {
	operations := curatedTestAPIGenOperations()
	if len(operations) != 2 {
		t.Fatalf("BuildAPIGenOperations() count = %d, want 2", len(operations))
	}
	operationsByName := make(map[string]APIGenOperation, len(operations))
	for _, operation := range operations {
		operationsByName[operation.Tool.Name] = operation
		if operation.Tool.Effect != agenttool.EffectRead {
			t.Fatalf("tool %q effect = %q, want read", operation.Tool.Name, operation.Tool.Effect)
		}
		if operation.Tool.OperationID != operation.Contract.OperationID {
			t.Fatalf("tool %q operation = %q, registry operation = %q", operation.Tool.Name, operation.Tool.OperationID, operation.Contract.OperationID)
		}
	}
	for name, operationID := range map[string]string{
		"query_semantic_model":   "querySemanticModel",
		"query_dashboard_visual": "queryDashboardVisualData",
	} {
		operation, ok := operationsByName[name]
		if !ok {
			t.Fatalf("BuildAPIGenOperations() missing generated tool %q", name)
		}
		if operation.Tool.OperationID != operationID {
			t.Fatalf("tool %q operation = %q, want %q", name, operation.Tool.OperationID, operationID)
		}
		if operation.Tool.Effect != agenttool.EffectRead {
			t.Fatalf("tool %q effect = %q, want read", name, operation.Tool.Effect)
		}
	}
	if slices.Contains(APIGenToolNames(operations), "query_dashboard_page") {
		t.Fatalf("APIGenToolNames() = %#v, must not contain query_dashboard_page", APIGenToolNames(operations))
	}
}

func TestAPIGenQueryProjectBindingsRemainServerBound(t *testing.T) {
	for _, operation := range curatedTestAPIGenOperations() {
		found := false
		for _, binding := range operation.Tool.Bindings {
			if binding.Source != "path" || binding.WireName != "project" {
				continue
			}
			found = true
			if binding.Mode != "context" || binding.Argument != "" || binding.ContextKey != "project" || !binding.Required {
				t.Fatalf("tool %q project binding = %#v, want required server context binding", operation.Tool.Name, binding)
			}
		}
		if !found {
			t.Fatalf("tool %q has no project path binding", operation.Tool.Name)
		}
		var schema struct {
			Properties map[string]struct {
				MinLength int `json:"minLength"`
			} `json:"properties"`
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(operation.Tool.InputSchema, &schema); err != nil {
			t.Fatalf("decode tool %q input schema: %v", operation.Tool.Name, err)
		}
		if _, ok := schema.Properties["project"]; ok || slices.Contains(schema.Required, "project") {
			t.Fatalf("tool %q input schema = %s, must not expose project selector", operation.Tool.Name, operation.Tool.InputSchema)
		}
	}
}

func TestToolNamesAreTheCuratedSurface(t *testing.T) {
	operations := curatedTestAPIGenOperations()
	want := []string{
		"add_dashboard_page",
		"add_dashboard_visual",
		"assign_dashboard_field",
		"catalog_get",
		"catalog_list",
		"catalog_search",
		"create_dashboard_draft",
		"docs_read",
		"docs_search",
		"execute_dashboard_command",
		"export_dashboard_yaml",
		"fork_dashboard",
		"get_dashboard",
		"get_dashboard_draft",
		"list_dashboards",
		"preview_dashboard_draft",
		"query_dashboard_visual",
		"query_semantic_model",
		"query_visual",
		"set_dashboard_visibility",
	}
	if got := ToolNames(operations); !slices.Equal(got, want) {
		t.Fatalf("ToolNames() = %#v, want %#v", got, want)
	}
}

func TestAnnotationsForEffectUseSafeWorstCaseHints(t *testing.T) {
	for _, test := range []struct {
		effect      string
		readOnly    bool
		destructive bool
		idempotent  bool
	}{
		{effect: "read", readOnly: true, idempotent: true},
		{effect: "write"},
		{effect: "destructive", destructive: true},
	} {
		got := AnnotationsForEffect(test.effect)
		if got.ReadOnlyHint != test.readOnly || got.DestructiveHint != test.destructive || got.IdempotentHint != test.idempotent || got.OpenWorldHint {
			t.Fatalf("AnnotationsForEffect(%q) = %#v", test.effect, got)
		}
	}
}

func TestReferenceCatalogComesFromCanonicalProviderDefinitions(t *testing.T) {
	operations := curatedTestAPIGenOperations()
	reference, err := ReferenceCatalog(operations)
	if err != nil {
		t.Fatalf("ReferenceCatalog(): %v", err)
	}
	if len(reference) != len(ToolNames(operations)) {
		t.Fatalf("ReferenceCatalog() count = %d, want %d", len(reference), len(ToolNames(operations)))
	}
	definitions := (ProviderSet{APIGen: APIGenProvider{Operations: operations}}).referenceDefinitions(Scope{})
	if len(definitions) != len(reference) {
		t.Fatalf("ProviderSet definitions = %d, reference = %d", len(definitions), len(reference))
	}
	wantDefaults := map[string]map[string]any{
		"add_dashboard_page": {}, "add_dashboard_visual": {}, "assign_dashboard_field": {},
		"catalog_get": {}, "catalog_list": {"limit": 25}, "catalog_search": {"limit": 10},
		"create_dashboard_draft": {},
		"docs_read":              {"limit": 200, "offset": 1}, "docs_search": {"limit": 8},
		"execute_dashboard_command": {}, "export_dashboard_yaml": {}, "fork_dashboard": {},
		"get_dashboard": {}, "get_dashboard_draft": {}, "list_dashboards": {}, "preview_dashboard_draft": {},
		"query_dashboard_visual": {"limit": 50}, "query_semantic_model": {"limit": 25}, "query_visual": {"limit": 50},
		"set_dashboard_visibility": {},
	}
	wantEffects := map[string]string{
		"add_dashboard_page": "write", "add_dashboard_visual": "write", "assign_dashboard_field": "write",
		"catalog_get": "read", "catalog_list": "read", "catalog_search": "read", "create_dashboard_draft": "write",
		"docs_read": "read", "docs_search": "read", "execute_dashboard_command": "destructive", "export_dashboard_yaml": "read",
		"fork_dashboard": "write", "get_dashboard": "read", "get_dashboard_draft": "read", "list_dashboards": "read",
		"preview_dashboard_draft": "read", "query_dashboard_visual": "read", "query_semantic_model": "read", "query_visual": "read",
		"set_dashboard_visibility": "write",
	}
	for index, tool := range reference {
		definition := definitions[index]
		if tool.Name != definition.Name {
			t.Fatalf("reference[%d].Name = %q, definition = %q", index, tool.Name, definition.Name)
		}
		if !json.Valid(tool.InputSchema) || !json.Valid(tool.OutputSchema) {
			t.Fatalf("tool %q has invalid generated schemas", tool.Name)
		}
		if string(tool.InputSchema) != string(definition.InputSchema) || string(tool.OutputSchema) != string(definition.OutputSchema) {
			t.Fatalf("tool %q reference schemas drifted from provider definitions", tool.Name)
		}
		if tool.Effect != wantEffects[tool.Name] || tool.Annotations.ReadOnlyHint != (tool.Effect == "read") || tool.Annotations.DestructiveHint != (tool.Effect == "destructive") || tool.Annotations.IdempotentHint != (tool.Effect == "read") || tool.Annotations.OpenWorldHint {
			t.Fatalf("tool %q annotations = %#v", tool.Name, tool.Annotations)
		}
		if tool.AuthzMode == "" || tool.OperationID == "" || (tool.AuthzMode == "privilege" && tool.Privilege == "") || (tool.AuthzMode == "authenticated" && tool.Privilege != "") {
			t.Fatalf("tool %q metadata = %#v", tool.Name, tool)
		}
		gotDefaults, _ := json.Marshal(tool.Defaults)
		expectedDefaults, _ := json.Marshal(wantDefaults[tool.Name])
		if string(gotDefaults) != string(expectedDefaults) {
			t.Fatalf("tool %q defaults = %#v, want %#v", tool.Name, tool.Defaults, wantDefaults[tool.Name])
		}
	}
}

func TestCanonicalProviderSchemasDoNotVaryByProjectContext(t *testing.T) {
	operations := curatedTestAPIGenOperations()
	providers := ProviderSet{APIGen: APIGenProvider{Operations: operations}}
	global := providers.Definitions(Scope{})
	project := providers.Definitions(Scope{ProjectID: "project_demo"})
	if len(global) != len(project) {
		t.Fatalf("global definitions = %d, project definitions = %d", len(global), len(project))
	}
	for index := range global {
		if global[index].Name != project[index].Name {
			t.Fatalf("definition[%d] names = %q and %q", index, global[index].Name, project[index].Name)
		}
		if string(global[index].InputSchema) != string(project[index].InputSchema) {
			t.Fatalf("tool %q input schema varies by project context", global[index].Name)
		}
		if string(global[index].OutputSchema) != string(project[index].OutputSchema) {
			t.Fatalf("tool %q output schema varies by project context:\nglobal=%s\nproject=%s", global[index].Name, global[index].OutputSchema, project[index].OutputSchema)
		}
	}
}

func curatedTestAPIGenOperations() []APIGenOperation {
	contracts := map[string]OperationContract{
		"querySemanticModel": {
			OperationID: "querySemanticModel", Method: "POST", Path: "/api/v1/projects/{project}/semantic-models/{model}/query",
			Protected: true, AuthzMode: "privilege",
			Extensions: map[string]any{"x-authz": map[string]any{"mode": "privilege", "privilege": "RESOURCE_USE"}},
		},
		"queryDashboardVisualData": {
			OperationID: "queryDashboardVisualData", Method: "POST", Path: "/api/v1/projects/{project}/dashboards/query-visual",
			Protected: true, AuthzMode: "privilege",
			Extensions: map[string]any{"x-authz": map[string]any{"mode": "privilege", "privilege": "RESOURCE_USE"}},
		},
	}
	input := json.RawMessage(`{"type":"object","properties":{"model":{"type":"string","minLength":1},"limit":{"type":"integer"}},"required":["model"],"additionalProperties":false}`)
	semanticOutput := json.RawMessage(`{
		"type":"object",
		"additionalProperties":false,
		"properties":{
			"queryId":{"type":"string"},
			"servingSnapshot":{"type":"string"},
			"freshness":{"type":"object","additionalProperties":false,"properties":{}},
			"completeness":{"type":"object","additionalProperties":false,"properties":{"returnedRows":{"type":"integer"},"hasMore":{"type":"boolean"}}},
			"columns":{"type":"array","items":{"type":"object","additionalProperties":false,"properties":{"name":{"type":"string"},"nullable":{"type":"boolean"},"fieldRef":{"type":"string"},"label":{"type":"string"},"kind":{"type":"string"},"dataType":{"type":"string"},"unit":{"type":"string"},"format":{"type":"string"}}}},
			"rows":{"type":"array"},
			"hasMore":{"type":"boolean"}
		}
	}`)
	output := json.RawMessage(`{"type":"object","additionalProperties":true}`)
	tools := map[string]agenttool.Contract{
		"query_semantic_model": {
			Name: "query_semantic_model", OperationID: "querySemanticModel", Method: "POST",
			Path: "/api/v1/projects/{project}/semantic-models/{model}/query", Effect: agenttool.EffectRead,
			InputSchema: input, OutputSchema: semanticOutput,
			Bindings: []agenttool.Binding{
				{Source: "path", WireName: "project", Mode: "context", ContextKey: "project", Required: true, Schema: agenttool.ValueSchema{Type: "string"}},
				{Argument: "model", Source: "path", WireName: "model", Mode: "model", Required: true, Schema: agenttool.ValueSchema{Type: "string"}},
				{Argument: "limit", Source: "body", WireName: "limit", Mode: "model", Default: 25, Schema: agenttool.ValueSchema{Type: "integer"}},
			},
		},
		"query_dashboard_visual": {
			Name: "query_dashboard_visual", OperationID: "queryDashboardVisualData", Method: "POST",
			Path: "/api/v1/projects/{project}/dashboards/query-visual", Effect: agenttool.EffectRead,
			InputSchema: input, OutputSchema: output,
			Bindings: []agenttool.Binding{
				{Source: "path", WireName: "project", Mode: "context", ContextKey: "project", Required: true, Schema: agenttool.ValueSchema{Type: "string"}},
				{Argument: "limit", Source: "body", WireName: "limit", Mode: "model", Default: 50, Schema: agenttool.ValueSchema{Type: "integer"}},
			},
		},
	}
	return BuildAPIGenOperations(contracts, tools)
}
