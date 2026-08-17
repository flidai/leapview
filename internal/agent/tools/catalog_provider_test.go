package tools

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	agentcontracts "github.com/flidai/leapview/internal/agent/contracts"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

type fakeCatalogService struct {
	searchRequest CatalogSearchRequest
	listRequest   CatalogListRequest
	getRequest    CatalogGetRequest
	searchResult  CatalogPage
	listResult    CatalogPage
	getResult     CatalogGetResult
	err           error
}

func (f *fakeCatalogService) Search(_ context.Context, _ Scope, request CatalogSearchRequest) (CatalogPage, error) {
	f.searchRequest = request
	return f.searchResult, f.err
}

func (f *fakeCatalogService) List(_ context.Context, _ Scope, request CatalogListRequest) (CatalogPage, error) {
	f.listRequest = request
	return f.listResult, f.err
}

func (f *fakeCatalogService) Get(_ context.Context, _ Scope, request CatalogGetRequest) (CatalogGetResult, error) {
	f.getRequest = request
	return f.getResult, f.err
}

func TestCatalogProviderDefinesClosedCuratedTools(t *testing.T) {
	definitions := (CatalogProvider{Catalog: &fakeCatalogService{}}).Definitions(Scope{})
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		names = append(names, definition.Name)
		if definition.Effect != "read" {
			t.Fatalf("%s effect = %q, want read", definition.Name, definition.Effect)
		}
		var schema map[string]any
		if err := json.Unmarshal(definition.InputSchema, &schema); err != nil {
			t.Fatalf("%s input schema: %v", definition.Name, err)
		}
		if schema["additionalProperties"] != false {
			t.Fatalf("%s input schema is not closed", definition.Name)
		}
		if definition.Name == CatalogSearchToolName {
			assertToolLimitMaximum(t, definition.InputSchema, MaxCatalogSearchLimit)
		}
		if definition.Name == CatalogListToolName {
			assertToolLimitMaximum(t, definition.InputSchema, MaxCatalogListLimit)
		}
	}
	slices.Sort(names)
	if want := []string{CatalogGetToolName, CatalogListToolName, CatalogSearchToolName}; !slices.Equal(names, want) {
		t.Fatalf("catalog definitions = %#v, want %#v", names, want)
	}
}

func assertToolLimitMaximum(t *testing.T, raw json.RawMessage, want int) {
	t.Helper()
	var schema struct {
		Properties map[string]struct {
			Maximum float64 `json:"maximum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode tool schema: %v", err)
	}
	if got := schema.Properties["limit"].Maximum; got != float64(want) {
		t.Fatalf("schema limit maximum = %v, want %d", got, want)
	}
}

func TestCatalogProviderAppliesDefaultsAndDelegates(t *testing.T) {
	service := &fakeCatalogService{
		searchResult: CatalogPage{
			Items:      []CatalogItem{{Ref: CatalogRef{Kind: CatalogType(agentcontracts.CatalogTypeDashboard), ID: "sales"}, Name: "Sales"}},
			NextCursor: "opaque",
		},
		listResult: CatalogPage{Items: []CatalogItem{}},
		getResult: CatalogGetResult{
			Item:    CatalogItem{Ref: CatalogRef{Kind: CatalogType(agentcontracts.CatalogTypeDashboard), ID: "sales"}, Name: "Sales"},
			Details: map[string]any{"pageCount": 2},
		},
	}
	provider := CatalogProvider{Catalog: service}
	definitions := provider.Definitions(Scope{PrincipalID: "p1"})

	searchResult := runCatalogTool(t, definitions, CatalogSearchToolName, `{"query":"sales"}`)
	if service.searchRequest.Limit != DefaultCatalogSearchLimit {
		t.Fatalf("search limit = %d, want %d", service.searchRequest.Limit, DefaultCatalogSearchLimit)
	}
	page, ok := searchResult.Content.(CatalogPage)
	if !ok || page.Count != 1 || !page.HasMore || page.NextCursor != "opaque" {
		t.Fatalf("search page metadata = %#v", searchResult.Content)
	}

	runCatalogTool(t, definitions, CatalogListToolName, `{}`)
	if service.listRequest.Limit != DefaultCatalogListLimit {
		t.Fatalf("list limit = %d, want %d", service.listRequest.Limit, DefaultCatalogListLimit)
	}

	runCatalogTool(t, definitions, CatalogGetToolName, `{"ref":{"id":"sales","kind":"dashboard"}}`)
	if service.getRequest.Ref.Kind != CatalogType(agentcontracts.CatalogTypeDashboard) {
		t.Fatalf("get ref = %#v", service.getRequest.Ref)
	}
}

func TestCatalogProviderRejectsInvalidArgumentsBeforeCallingService(t *testing.T) {
	service := &fakeCatalogService{}
	definitions := (CatalogProvider{Catalog: service}).Definitions(Scope{})
	for _, test := range []struct {
		name string
		args string
	}{
		{CatalogSearchToolName, `{}`},
		{CatalogSearchToolName, `{"query":"sales","limit":26}`},
		{CatalogListToolName, `{"limit":51}`},
		{CatalogGetToolName, `{"ref":{"id":"warehouse","kind":"invalid"}}`},
		{CatalogGetToolName, `{"ref":{"id":"","kind":"dashboard"}}`},
	} {
		result := runCatalogTool(t, definitions, test.name, test.args)
		if !result.IsError {
			t.Fatalf("%s(%s) error = false, want true", test.name, test.args)
		}
		code := catalogTestErrorCode(result)
		if code != "invalid_arguments" {
			t.Fatalf("%s(%s) content = %#v, want invalid_arguments", test.name, test.args, result.Content)
		}
	}
}

func TestCatalogProviderPreservesStableServiceErrors(t *testing.T) {
	service := &fakeCatalogService{err: &CatalogError{Code: "catalog_not_found", Message: "resource not found"}}
	definitions := (CatalogProvider{Catalog: service}).Definitions(Scope{})
	result := runCatalogTool(t, definitions, CatalogGetToolName, `{"ref":{"id":"revenue","kind":"dashboard"}}`)
	if !result.IsError {
		t.Fatal("error = false, want true")
	}
	if code := catalogTestErrorCode(result); code != "catalog_not_found" {
		t.Fatalf("code = %#v, want catalog_not_found", code)
	}

	service.err = errors.New("database unavailable")
	result = runCatalogTool(t, definitions, CatalogListToolName, `{}`)
	if code := catalogTestErrorCode(result); code != "catalog_list_failed" {
		t.Fatalf("code = %#v, want catalog_list_failed", code)
	}
}

func catalogTestErrorCode(result agentcore.ToolResult) string {
	content, _ := result.Content.(map[string]any)
	payload, _ := content["error"].(map[string]any)
	code, _ := payload["code"].(string)
	return code
}

func runCatalogTool(t *testing.T, definitions []agentcore.ToolDefinition, name, args string) agentcore.ToolResult {
	t.Helper()
	for _, definition := range definitions {
		if definition.Name != name {
			continue
		}
		result, err := definition.Handler.Run(context.Background(), agentcore.ToolCall{ID: "call-1", Arguments: json.RawMessage(args)})
		if err != nil {
			t.Fatalf("%s handler: %v", name, err)
		}
		return result
	}
	t.Fatalf("tool %q not found", name)
	return agentcore.ToolResult{}
}
