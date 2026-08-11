package agenttool

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildRequestBindsContextDefaultsAndBody(t *testing.T) {
	contract := Contract{
		Name:   "create_item",
		Method: http.MethodPost,
		Path:   "/api/v1/workspaces/{workspace}/items",
		Bindings: []Binding{
			{Source: "path", WireName: "workspace", Mode: "context", ContextKey: "workspace", Required: true, Schema: ValueSchema{Type: "string"}},
			{Argument: "limit", Source: "query", WireName: "limit", Mode: "model", Default: float64(25), Schema: ValueSchema{Type: "integer"}},
			{Argument: "title", Source: "body", WireName: "title", Mode: "model", Required: true, Schema: ValueSchema{Type: "string"}},
		},
	}

	request, err := BuildRequest(contract, json.RawMessage(`{"title":"Quarterly report"}`), Context{"workspace": "sales / north"})
	require.NoError(t, err)
	require.Equal(t, http.MethodPost, request.Method)
	require.Equal(t, "/api/v1/workspaces/sales%20%2F%20north/items", request.URL.EscapedPath())
	require.Equal(t, "25", request.URL.Query().Get("limit"))
	require.Equal(t, "application/json", request.Header.Get("Content-Type"))
	body, err := io.ReadAll(request.Body)
	require.NoError(t, err)
	require.JSONEq(t, `{"title":"Quarterly report"}`, string(body))
}

func TestBuildRequestNegotiatesGeneratedJSONResponseMedia(t *testing.T) {
	contract := Contract{
		Name: "get_export", Method: http.MethodGet, Path: "/export",
		ResponseContentType: "application/json",
	}

	request, err := BuildRequest(contract, json.RawMessage(`{}`), nil)
	require.NoError(t, err)
	require.Equal(t, "application/json", request.Header.Get("Accept"))
}

func TestBuildRequestOverridesAcceptBindingWithContractedResponseMedia(t *testing.T) {
	contract := Contract{
		Name: "get_export", Method: http.MethodGet, Path: "/export",
		ResponseContentType: "application/json",
		Bindings:            []Binding{{Argument: "accept", Source: "header", WireName: "Accept", Mode: "model", Schema: ValueSchema{Type: "string"}}},
	}

	request, err := BuildRequest(contract, json.RawMessage(`{"accept":"application/vnd.apache.arrow.file"}`), nil)
	require.NoError(t, err)
	require.Equal(t, "application/json", request.Header.Get("Accept"))
}

func TestBuildRequestRejectsUnknownArgumentsAndMissingContext(t *testing.T) {
	contract := Contract{
		Name:   "get_item",
		Method: http.MethodGet,
		Path:   "/workspaces/{workspace}/items/{id}",
		Bindings: []Binding{
			{Source: "path", WireName: "workspace", Mode: "context", ContextKey: "workspace", Required: true, Schema: ValueSchema{Type: "string"}},
			{Argument: "id", Source: "path", WireName: "id", Mode: "model", Required: true, Schema: ValueSchema{Type: "string"}},
		},
	}

	_, err := BuildRequest(contract, json.RawMessage(`{"id":"one","extra":true}`), Context{"workspace": "sales"})
	require.ErrorContains(t, err, "unknown argument")
	require.Equal(t, ErrorCodeInvalidArguments, err.(*Error).Code)

	_, err = BuildRequest(contract, json.RawMessage(`{"id":"one"}`), nil)
	require.ErrorContains(t, err, "missing context")
	require.Equal(t, ErrorCodeMissingContext, err.(*Error).Code)
}

func TestBuildRequestValidatesNestedSchemasRangesAndTrailingJSON(t *testing.T) {
	contract := Contract{
		Name: "query", Method: http.MethodPost, Path: "/query",
		InputSchema: json.RawMessage(`{
		  "type":"object",
		  "properties":{"options":{"type":"object","properties":{"limit":{"type":"integer","minimum":1,"maximum":100}},"required":["limit"],"additionalProperties":false}},
		  "required":["options"],"additionalProperties":false
		}`),
		Bindings: []Binding{{Argument: "options", Source: "body", WireName: "options", Mode: "model", Required: true, Schema: ValueSchema{Type: "object"}}},
	}

	_, err := BuildRequest(contract, json.RawMessage(`{"options":{"limit":0}}`), nil)
	require.ErrorContains(t, err, "must be at least")
	require.Equal(t, ErrorCodeInvalidArguments, err.(*Error).Code)
	_, err = BuildRequest(contract, json.RawMessage(`{"options":{"limit":2,"extra":true}}`), nil)
	require.ErrorContains(t, err, "is not allowed")
	_, err = BuildRequest(contract, json.RawMessage(`{"options":{"limit":2}} {}`), nil)
	require.ErrorContains(t, err, "trailing JSON")
}

func TestProjectResponseProjectsArraysMapsCountsAndCursor(t *testing.T) {
	contract := Contract{
		Name: "query_page",
		Output: Output{
			Mode: "project",
			Select: []Projection{{
				Source: "/visuals", Target: "visuals", Kind: "map",
				Select: []Projection{
					{Source: "/title", Target: "title", Kind: "value"},
					{Source: "/data", Target: "data", Kind: "array", CountAs: "count", Select: []Projection{{Source: "/value", Target: "value", Kind: "value"}}},
				},
			}},
			Cursor: &Cursor{Source: "/page/nextCursor", Target: "nextCursor", HasMoreTarget: "hasMore"},
		},
	}
	response := &http.Response{
		StatusCode: 200,
		Body: io.NopCloser(strings.NewReader(`{
          "visuals": {
            "sales": {"title":"Sales","secret":"drop","data":[{"value":1,"raw":9},{"value":2,"raw":8}]}
          },
          "page": {"nextCursor":"next-1"},
          "internal":"drop"
        }`)),
	}

	result, err := ProjectResponse(contract, response)
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Equal(t, map[string]any{
		"visuals": map[string]any{
			"sales": map[string]any{
				"title": "Sales",
				"data":  []any{map[string]any{"value": float64(1)}, map[string]any{"value": float64(2)}},
				"count": 2,
			},
		},
		"nextCursor": "next-1",
		"hasMore":    true,
	}, result.Content)
}

func TestProjectResponsePreservesHTTPFailures(t *testing.T) {
	response := &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(`{"code":"forbidden"}`))}
	result, err := ProjectResponse(Contract{Name: "list_items", Output: Output{Mode: "raw"}}, response)
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Equal(t, map[string]any{"status": 403, "body": map[string]any{"code": "forbidden"}}, result.Content)
}

func TestProjectResponseRejectsOutputThatViolatesProjectedSchema(t *testing.T) {
	contract := Contract{
		Name:         "get_item",
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"],"additionalProperties":false}`),
		Output:       Output{Mode: "project", Select: []Projection{{Source: "/count", Target: "count", Kind: "value"}}},
	}
	response := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"count":"many"}`))}

	_, err := ProjectResponse(contract, response)
	require.ErrorContains(t, err, "output does not match schema")
	require.Equal(t, ErrorCodeInvalidResponse, err.(*Error).Code)
}

func TestProjectResponseOmitsOptionalNullValues(t *testing.T) {
	contract := Contract{
		Name:         "get_item",
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}},"additionalProperties":false}`),
		Output:       Output{Mode: "project", Select: []Projection{{Source: "/title", Target: "title", Kind: "value", Optional: true}}},
	}
	response := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"title":null}`))}

	result, err := ProjectResponse(contract, response)
	require.NoError(t, err)
	require.Equal(t, map[string]any{}, result.Content)
}
