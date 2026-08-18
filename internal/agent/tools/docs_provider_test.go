package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	productdocs "github.com/flidai/leapview/internal/agent/productdocs"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

func TestDocsProviderExposesBoundedSearchAndReadTools(t *testing.T) {
	var searchRequest productdocs.SearchRequest
	var readRequest productdocs.ReadRequest
	provider := DocsProvider{Documentation: fakeDocumentation{
		search: func(_ context.Context, request productdocs.SearchRequest) (productdocs.SearchResult, error) {
			searchRequest = request
			return productdocs.SearchResult{Query: request.Query}, nil
		},
		read: func(_ context.Context, request productdocs.ReadRequest) (productdocs.ReadResult, error) {
			readRequest = request
			return productdocs.ReadResult{ID: request.ID}, nil
		},
	}}

	definitions := provider.Definitions()
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		names = append(names, definition.Name)
	}
	if got, want := names, []string{DocsSearchToolName, DocsReadToolName}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Definitions() names = %#v, want %#v", got, want)
	}
	for _, definition := range definitions {
		if definition.Effect != "read" {
			t.Fatalf("tool %q effect = %q, want read", definition.Name, definition.Effect)
		}
		var schema map[string]any
		if err := json.Unmarshal(definition.InputSchema, &schema); err != nil {
			t.Fatalf("decode %s input schema: %v", definition.Name, err)
		}
		if schema["additionalProperties"] != false {
			t.Fatalf("%s input schema is not closed: %s", definition.Name, definition.InputSchema)
		}
	}
	assertToolLimitMaximum(t, definitions[0].InputSchema, productdocs.MaxSearchLimit)
	assertToolLimitMaximum(t, definitions[1].InputSchema, productdocs.MaxReadLimit)

	searchResult, err := definitions[0].Handler.Run(context.Background(), agentcore.ToolCall{
		ID: "call-search", Name: DocsSearchToolName,
		Arguments: json.RawMessage(`{"query":"semantic relationships","path":"guides","cursor":"opaque","limit":4}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if searchResult.IsError {
		t.Fatalf("search result = %#v", searchResult.Content)
	}
	if want := (productdocs.SearchRequest{Query: "semantic relationships", Path: "guides", Cursor: "opaque", Limit: 4}); searchRequest != want {
		t.Fatalf("search request = %#v, want %#v", searchRequest, want)
	}

	readResult, err := definitions[1].Handler.Run(context.Background(), agentcore.ToolCall{
		ID: "call-read", Name: DocsReadToolName,
		Arguments: json.RawMessage(`{"id":"doc:guides/build/semantic-model","offset":101,"limit":50}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if readResult.IsError {
		t.Fatalf("read result = %#v", readResult.Content)
	}
	if want := (productdocs.ReadRequest{ID: "doc:guides/build/semantic-model", Offset: 101, Limit: 50}); readRequest != want {
		t.Fatalf("read request = %#v, want %#v", readRequest, want)
	}
}

func TestDocsProviderGeneratedOutputsMatchRuntimeDTOs(t *testing.T) {
	provider := DocsProvider{Documentation: fakeDocumentation{
		search: func(_ context.Context, request productdocs.SearchRequest) (productdocs.SearchResult, error) {
			return productdocs.SearchResult{
				Query: request.Query,
				Matches: []productdocs.Reference{{
					ID: "doc:guides/semantic-models", Path: "guides/semantic-models", Title: "Semantic models",
					Summary: "Governed semantics.", URL: "/docs/guides/semantic-models", Excerpt: "Metrics and dimensions",
				}},
				Count: 1, HasMore: true, NextCursor: "opaque",
			}, nil
		},
		read: func(_ context.Context, request productdocs.ReadRequest) (productdocs.ReadResult, error) {
			return productdocs.ReadResult{
				ID: request.ID, Path: "guides/semantic-models", Title: "Semantic models", URL: "/docs/guides/semantic-models",
				Content: "1: Semantic models", LineStart: 1, LineEnd: 1, TotalLines: 2, NextOffset: 2, Truncated: true,
			}, nil
		},
	}}
	catalog, err := agentcore.NewToolCatalog(provider.Definitions())
	if err != nil {
		t.Fatalf("compile generated documentation schemas: %v", err)
	}
	for _, call := range []agentcore.ToolCall{
		{ID: "search", Name: DocsSearchToolName, Arguments: json.RawMessage(`{"query":"semantic"}`)},
		{ID: "read", Name: DocsReadToolName, Arguments: json.RawMessage(`{"id":"doc:guides/semantic-models"}`)},
	} {
		result, err := catalog.Execute(context.Background(), call)
		if err != nil || result.IsError {
			t.Fatalf("%s generated contract rejected runtime DTO: result=%#v err=%v", call.Name, result, err)
		}
	}
}

func TestDocsProviderReturnsStructuredToolErrors(t *testing.T) {
	provider := DocsProvider{}
	for _, definition := range provider.Definitions() {
		result, err := definition.Handler.Run(context.Background(), agentcore.ToolCall{
			ID: "call", Name: definition.Name, Arguments: json.RawMessage(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsError {
			t.Fatalf("tool %q result = %#v, want error", definition.Name, result.Content)
		}
	}
}

type fakeDocumentation struct {
	search func(context.Context, productdocs.SearchRequest) (productdocs.SearchResult, error)
	read   func(context.Context, productdocs.ReadRequest) (productdocs.ReadResult, error)
}

func (f fakeDocumentation) Search(ctx context.Context, request productdocs.SearchRequest) (productdocs.SearchResult, error) {
	return f.search(ctx, request)
}

func (f fakeDocumentation) Read(ctx context.Context, request productdocs.ReadRequest) (productdocs.ReadResult, error) {
	return f.read(ctx, request)
}
