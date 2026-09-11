package agenttool

import (
	"encoding/json"
	"testing"

	"github.com/Yacobolo/toolbelt/apigen/ir"
	runtime "github.com/Yacobolo/toolbelt/apigen/runtime/agenttool"
	"github.com/stretchr/testify/require"
)

func TestBuildCompilesEndpointToolContract(t *testing.T) {
	doc := ir.Document{
		SchemaVersion: ir.CurrentSchemaVersion,
		API:           ir.API{BasePath: "/api/v1"},
		Info:          ir.Info{Title: "Tools", Version: "1"},
		Schemas: map[string]ir.Schema{
			"Item": {
				Type: "object",
				Properties: map[string]ir.SchemaProperty{
					"id":     {Schema: ir.SchemaRef{Type: "string"}},
					"secret": {Schema: ir.SchemaRef{Type: "string"}},
				},
				Required: []string{"id"},
			},
			"ListResponse": {
				Type: "object",
				Properties: map[string]ir.SchemaProperty{
					"items": {Schema: ir.SchemaRef{Type: "array", Items: &ir.SchemaRef{Ref: "Item"}}},
				},
				Required: []string{"items"},
			},
		},
		Endpoints: []ir.Endpoint{{
			Method: "get", Path: "/workspaces/{workspace}/items", OperationID: "listItems", Summary: "List items",
			Parameters: []ir.Parameter{
				{Name: "workspace", In: "path", Required: true, Schema: ir.SchemaRef{Type: "string"}},
				{Name: "limit", In: "query", Schema: ir.SchemaRef{Type: "integer", Format: "int32"}},
				{Name: "Accept", In: "header", Required: true, Schema: ir.SchemaRef{Type: "string", Enum: []string{"application/json", "application/vnd.apache.arrow.file"}}},
			},
			Responses: []ir.Response{{
				StatusCode:  200,
				Description: "ok",
				Contents: []ir.BodyContent{
					{ContentType: "application/vnd.apache.arrow.file", BodyKind: "binary", Schema: &ir.SchemaRef{Type: "string", Format: "binary"}},
					{ContentType: "application/json", BodyKind: "json", Schema: &ir.SchemaRef{Ref: "ListResponse"}},
				},
			}},
			Tool: &ir.Tool{
				Name: "list_items", Effect: "read", Confirmation: "never",
				Input: &ir.ToolInput{Fields: []ir.ToolInputField{
					{Source: "path", Name: "workspace", Mode: "context", ContextKey: "workspace"},
					{Source: "query", Name: "limit", Default: 25},
				}},
				Output: ir.ToolOutput{Mode: "project", Select: []ir.ToolProjection{{Source: "/items", CountAs: "count", Select: []ir.ToolProjection{{Source: "/id"}}}}},
			},
		}},
	}

	contracts, err := Build(doc)
	require.NoError(t, err)
	contract := contracts["list_items"]
	require.Equal(t, "/api/v1/workspaces/{workspace}/items", contract.Path)
	require.Equal(t, runtime.EffectRead, contract.Effect)
	require.Equal(t, "application/json", contract.ResponseContentType)
	require.Equal(t, "context", contract.Bindings[0].Mode)
	require.Equal(t, "workspace", contract.Bindings[0].ContextKey)
	require.Equal(t, "limit", contract.Bindings[1].Argument)
	require.EqualValues(t, 25, contract.Bindings[1].Default)
	require.Equal(t, "omit", contract.Bindings[2].Mode)
	require.Equal(t, "application/json", contract.Bindings[2].Default)
	require.Equal(t, "array", contract.Output.Select[0].Kind)
	require.Equal(t, "value", contract.Output.Select[0].Select[0].Kind)

	var inputSchema map[string]any
	require.NoError(t, json.Unmarshal(contract.InputSchema, &inputSchema))
	properties := inputSchema["properties"].(map[string]any)
	require.NotContains(t, properties, "workspace")
	require.Contains(t, properties, "limit")
	require.NotContains(t, properties, "Accept")
	require.NotContains(t, properties["limit"].(map[string]any), "format")
	require.NotContains(t, properties["limit"].(map[string]any), "default")
	require.Equal(t, false, inputSchema["additionalProperties"])
}

func TestBuildEmitsUnconstrainedSchemasWithoutEmptyTypes(t *testing.T) {
	unknown := ir.SchemaRef{}
	doc := ir.Document{
		SchemaVersion: ir.CurrentSchemaVersion,
		API:           ir.API{BasePath: "/api/v1"},
		Info:          ir.Info{Title: "Tools", Version: "1"},
		Schemas: map[string]ir.Schema{
			"SemanticFilter": {
				Type: "object",
				Properties: map[string]ir.SchemaProperty{
					"values": {
						Schema: ir.SchemaRef{Type: "array", Items: &unknown},
					},
					"metadata": {
						Schema: ir.SchemaRef{
							Type:                 "object",
							AdditionalProperties: &ir.AdditionalProperties{Schema: &unknown},
						},
					},
					"mode": {
						Schema: ir.SchemaRef{Type: "string", Enum: []string{"all", "any"}},
					},
					"next": {
						Schema: ir.SchemaRef{Ref: "SemanticFilter"},
					},
				},
				Required: []string{"values"},
			},
		},
		Endpoints: []ir.Endpoint{{
			Method: "post", Path: "/search", OperationID: "search", Summary: "Search",
			RequestBody: &ir.RequestBody{
				Required: true,
				Contents: []ir.BodyContent{{
					ContentType: "application/json", BodyKind: "json", Schema: &ir.SchemaRef{Ref: "SemanticFilter"},
				}},
			},
			Responses: []ir.Response{{
				StatusCode: 200, Description: "ok",
				Contents: []ir.BodyContent{{
					ContentType: "application/json", BodyKind: "json", Schema: &ir.SchemaRef{Ref: "SemanticFilter"},
				}},
			}},
			Tool: &ir.Tool{
				Name: "search", Effect: "read", Confirmation: "never",
				Output: ir.ToolOutput{Mode: "raw"},
			},
		}},
	}

	contracts, err := Build(doc)
	require.NoError(t, err)
	contract := contracts["search"]

	var inputSchema map[string]any
	require.NoError(t, json.Unmarshal(contract.InputSchema, &inputSchema))
	inputProperties := inputSchema["properties"].(map[string]any)
	require.Equal(t, map[string]any{}, inputProperties["values"].(map[string]any)["items"])
	require.Equal(t, map[string]any{}, inputProperties["metadata"].(map[string]any)["additionalProperties"])
	require.Equal(t, "string", inputProperties["mode"].(map[string]any)["type"])
	require.Equal(t, []any{"all", "any"}, inputProperties["mode"].(map[string]any)["enum"])
	requireNoEmptySchemaTypes(t, inputSchema)

	var outputSchema map[string]any
	require.NoError(t, json.Unmarshal(contract.OutputSchema, &outputSchema))
	outputProperties := outputSchema["properties"].(map[string]any)
	require.Equal(t, map[string]any{}, outputProperties["values"].(map[string]any)["items"])
	require.Equal(t, map[string]any{}, outputProperties["metadata"].(map[string]any)["additionalProperties"])
	require.Equal(t, "object", outputProperties["next"].(map[string]any)["type"])
	requireNoEmptySchemaTypes(t, outputSchema)
}

func TestSchemaRefJSONPreservesPatternAndPropertyNames(t *testing.T) {
	doc := ir.Document{Schemas: map[string]ir.Schema{"Value": {Type: "string"}}}
	ref := ir.SchemaRef{
		Type:                 "object",
		Pattern:              "^[A-Z]+$",
		PropertyNames:        &ir.SchemaRef{Type: "string", Pattern: "^[a-z_]+$"},
		AdditionalProperties: &ir.AdditionalProperties{Schema: &ir.SchemaRef{Ref: "Value"}},
	}
	got := schemaRefJSON(doc, ref, map[string]bool{})
	require.Equal(t, "^[A-Z]+$", got["pattern"])
	require.Equal(t, "^[a-z_]+$", got["propertyNames"].(map[string]any)["pattern"])
	require.Equal(t, map[string]any{"type": "string"}, got["additionalProperties"])
}

func TestSchemaJSONOmitsEmptyType(t *testing.T) {
	require.NotContains(t, schemaJSON(ir.Document{}, ir.Schema{}, map[string]bool{}), "type")
}

func TestBuildPreservesDiscriminatedOutputSchema(t *testing.T) {
	doc := ir.Document{
		SchemaVersion: ir.CurrentSchemaVersion,
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "Visual API", Version: "1"},
		Schemas: map[string]ir.Schema{
			"Visual":      {Type: "union", OneOf: []ir.SchemaRef{{Ref: "ChartVisual"}, {Ref: "TextVisual"}}, Discriminator: &ir.Discriminator{PropertyName: "shape", Mapping: map[string]string{"chart": "ChartVisual", "text": "TextVisual"}}},
			"VisualBase":  {Type: "object", Properties: map[string]ir.SchemaProperty{"shape": {Schema: ir.SchemaRef{Type: "string"}}}, Required: []string{"shape"}},
			"ChartVisual": {Type: "object", Base: &ir.SchemaRef{Ref: "VisualBase"}, Properties: map[string]ir.SchemaProperty{"shape": {Schema: ir.SchemaRef{Type: "string", Enum: []string{"chart"}}}}, Required: []string{"shape"}},
			"TextVisual":  {Type: "object", Base: &ir.SchemaRef{Ref: "VisualBase"}, Properties: map[string]ir.SchemaProperty{"shape": {Schema: ir.SchemaRef{Type: "string", Enum: []string{"text"}}}}, Required: []string{"shape"}},
			"Page":        {Type: "object", Properties: map[string]ir.SchemaProperty{"visuals": {Schema: ir.SchemaRef{Type: "array", Items: &ir.SchemaRef{Ref: "Visual"}}}}, Required: []string{"visuals"}},
		},
		Endpoints: []ir.Endpoint{{Method: "get", Path: "/visual", OperationID: "getVisual", Responses: []ir.Response{{StatusCode: 200, Description: "ok", Contents: []ir.BodyContent{{ContentType: "application/json", BodyKind: "json", Schema: &ir.SchemaRef{Ref: "Page"}}}}}, Tool: &ir.Tool{Name: "get_visual", Effect: "read", Output: ir.ToolOutput{Mode: "raw"}}}},
	}

	contracts, err := Build(doc)
	require.NoError(t, err)
	require.Contains(t, string(contracts["get_visual"].OutputSchema), `"oneOf"`)
	require.Contains(t, string(contracts["get_visual"].OutputSchema), `"items":{"oneOf"`)
	require.Contains(t, string(contracts["get_visual"].OutputSchema), `"chart"`)
	require.NotContains(t, string(contracts["get_visual"].OutputSchema), `"allOf"`)
	require.Contains(t, string(contracts["get_visual"].OutputSchema), `"additionalProperties":false`)
}

func TestBuildRawUnionSchemaIncludesInheritedPropertiesInEveryVariant(t *testing.T) {
	doc := ir.Document{
		SchemaVersion: ir.CurrentSchemaVersion,
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "Visual API", Version: "1"},
		Schemas: map[string]ir.Schema{
			"Visual": {Type: "union", OneOf: []ir.SchemaRef{{Ref: "ChartVisual"}, {Ref: "TextVisual"}}},
			"VisualBase": {
				Type: "object",
				Properties: map[string]ir.SchemaProperty{
					"kind":     {Schema: ir.SchemaRef{Type: "string"}},
					"title":    {Schema: ir.SchemaRef{Type: "string"}},
					"subtitle": {Schema: ir.SchemaRef{Type: "string"}},
				},
				PropertyOrder: []string{"kind", "title", "subtitle"},
				Required:      []string{"kind", "title"},
			},
			"ChartVisual": {Type: "object", Base: &ir.SchemaRef{Ref: "VisualBase"}, Properties: map[string]ir.SchemaProperty{"kind": {Schema: ir.SchemaRef{Type: "string", Enum: []string{"chart"}}}}, PropertyOrder: []string{"kind"}, Required: []string{"kind"}},
			"TextVisual":  {Type: "object", Base: &ir.SchemaRef{Ref: "VisualBase"}, Properties: map[string]ir.SchemaProperty{"kind": {Schema: ir.SchemaRef{Type: "string", Enum: []string{"text"}}}}, PropertyOrder: []string{"kind"}, Required: []string{"kind"}},
		},
		Endpoints: []ir.Endpoint{{Method: "get", Path: "/visual", OperationID: "getVisual", Responses: []ir.Response{{StatusCode: 200, Description: "ok", Contents: []ir.BodyContent{{ContentType: "application/json", BodyKind: "json", Schema: &ir.SchemaRef{Ref: "Visual"}}}}}, Tool: &ir.Tool{Name: "get_visual", Effect: "read", Output: ir.ToolOutput{Mode: "raw"}}}},
	}

	contracts, err := Build(doc)
	require.NoError(t, err)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(contracts["get_visual"].OutputSchema, &schema))
	variants := schema["oneOf"].([]any)
	require.Len(t, variants, 2)
	for _, rawVariant := range variants {
		variant := rawVariant.(map[string]any)
		properties := variant["properties"].(map[string]any)
		require.Contains(t, properties, "kind")
		require.Contains(t, properties, "title")
		require.Contains(t, properties, "subtitle")
		require.ElementsMatch(t, []any{"kind", "title"}, variant["required"].([]any))
		require.Equal(t, false, variant["additionalProperties"])
	}
}

func TestBuildCompilesProjectionsThroughDiscriminatedUnions(t *testing.T) {
	doc := ir.Document{
		SchemaVersion: ir.CurrentSchemaVersion,
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "Visual API", Version: "1"},
		Schemas: map[string]ir.Schema{
			"Visual":      {Type: "union", OneOf: []ir.SchemaRef{{Ref: "ChartVisual"}, {Ref: "TextVisual"}}},
			"VisualBase":  {Type: "object", Properties: map[string]ir.SchemaProperty{"title": {Schema: ir.SchemaRef{Type: "string"}}}, Required: []string{"title"}},
			"ChartVisual": {Type: "object", Base: &ir.SchemaRef{Ref: "VisualBase"}},
			"TextVisual":  {Type: "object", Base: &ir.SchemaRef{Ref: "VisualBase"}},
			"Page": {
				Type: "object",
				Properties: map[string]ir.SchemaProperty{
					"primary":       {Schema: ir.SchemaRef{Ref: "Visual"}},
					"visuals":       {Schema: ir.SchemaRef{Type: "array", Items: &ir.SchemaRef{Ref: "Visual"}}},
					"visuals_by_id": {Schema: ir.SchemaRef{Type: "object", AdditionalProperties: &ir.AdditionalProperties{Schema: &ir.SchemaRef{Ref: "Visual"}}}},
				},
				Required: []string{"primary", "visuals", "visuals_by_id"},
			},
		},
		Endpoints: []ir.Endpoint{{
			Method: "get", Path: "/page", OperationID: "getPage",
			Responses: []ir.Response{{StatusCode: 200, Description: "ok", Contents: []ir.BodyContent{{ContentType: "application/json", BodyKind: "json", Schema: &ir.SchemaRef{Ref: "Page"}}}}},
			Tool: &ir.Tool{Name: "get_page", Effect: "read", Output: ir.ToolOutput{Mode: "project", Select: []ir.ToolProjection{
				{Source: "/primary", Select: []ir.ToolProjection{{Source: "/title"}}},
				{Source: "/visuals", Select: []ir.ToolProjection{{Source: "/title"}}},
				{Source: "/visuals_by_id", Select: []ir.ToolProjection{{Source: "/title"}}},
			}}},
		}},
	}

	contracts, err := Build(doc)
	require.NoError(t, err)
	selects := contracts["get_page"].Output.Select
	require.Equal(t, "object", selects[0].Kind)
	require.Equal(t, "array", selects[1].Kind)
	require.Equal(t, "map", selects[2].Kind)
	for _, projection := range selects {
		require.Equal(t, "value", projection.Select[0].Kind)
		require.False(t, projection.Select[0].Optional)
	}
}

func TestBuildCompilesCountProjectionForUnionArraysWithDifferentItemSchemas(t *testing.T) {
	doc := ir.Document{
		SchemaVersion: ir.CurrentSchemaVersion,
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "Results", Version: "1"},
		Schemas: map[string]ir.Schema{
			"Result": {Type: "union", OneOf: []ir.SchemaRef{{Ref: "Strings"}, {Ref: "Numbers"}}},
			"Strings": {Type: "object", Properties: map[string]ir.SchemaProperty{
				"data": {Schema: ir.SchemaRef{Type: "array", Items: &ir.SchemaRef{Type: "string"}}},
			}, Required: []string{"data"}},
			"Numbers": {Type: "object", Properties: map[string]ir.SchemaProperty{
				"data": {Schema: ir.SchemaRef{Type: "array", Items: &ir.SchemaRef{Type: "integer", Format: "int32"}}},
			}, Required: []string{"data"}},
			"Page": {Type: "object", Properties: map[string]ir.SchemaProperty{
				"results": {Schema: ir.SchemaRef{Type: "object", AdditionalProperties: &ir.AdditionalProperties{Schema: &ir.SchemaRef{Ref: "Result"}}}},
			}, Required: []string{"results"}},
		},
		Endpoints: []ir.Endpoint{{
			Method: "get", Path: "/results", OperationID: "getResults",
			Responses: []ir.Response{{StatusCode: 200, Description: "ok", Contents: []ir.BodyContent{{ContentType: "application/json", BodyKind: "json", Schema: &ir.SchemaRef{Ref: "Page"}}}}},
			Tool:      &ir.Tool{Name: "get_results", Effect: "read", Output: ir.ToolOutput{Mode: "project", Select: []ir.ToolProjection{{Source: "/results", Select: []ir.ToolProjection{{Source: "/data", CountAs: "count"}}}}}},
		}},
	}

	contracts, err := Build(doc)
	require.NoError(t, err)
	outer := contracts["get_results"].Output.Select[0]
	require.Equal(t, "map", outer.Kind)
	projection := outer.Select[0]
	require.Equal(t, "array", projection.Kind)
	require.Equal(t, "array", projection.Schema.Type)
	require.Nil(t, projection.Schema.Items)

	var schema map[string]any
	require.NoError(t, json.Unmarshal(contracts["get_results"].OutputSchema, &schema))
	results := schema["properties"].(map[string]any)["results"].(map[string]any)
	properties := results["additionalProperties"].(map[string]any)["properties"].(map[string]any)
	require.Equal(t, map[string]any{"type": "array"}, properties["data"])
	require.Equal(t, map[string]any{"type": "integer"}, properties["count"])
}

func requireNoEmptySchemaTypes(t *testing.T, value any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "type" {
				require.NotEqual(t, "", child)
			}
			requireNoEmptySchemaTypes(t, child)
		}
	case []any:
		for _, child := range typed {
			requireNoEmptySchemaTypes(t, child)
		}
	}
}
