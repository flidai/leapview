package openapi

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v4"

	"github.com/Yacobolo/toolbelt/apigen/ir"
)

func TestEmitYAML(t *testing.T) {
	t.Helper()

	docIR := ir.Document{
		SchemaVersion: "v4",
		Info:          ir.Info{Title: "test", Version: "1.0.0"},
		Schemas: map[string]ir.Schema{
			"Item": {
				Type: "object",
				Example: map[string]any{
					"id": "item_123",
				},
				Properties: map[string]ir.SchemaProperty{
					"id": {Schema: ir.SchemaRef{Type: "string"}, Example: "item_123"},
				},
			},
			"Envelope": {
				Type: "object",
				Properties: map[string]ir.SchemaProperty{
					"item": {Schema: ir.SchemaRef{Ref: "Item"}},
				},
			},
		},
		Endpoints: []ir.Endpoint{
			{
				Method:      "get",
				Path:        "/items/{id}",
				OperationID: "getItem",
				Parameters: []ir.Parameter{
					{Name: "id", In: "path", Required: true, Schema: ir.SchemaRef{Type: "string"}, Example: "item_123"},
					{Name: "accept", In: "header", Required: true, Schema: ir.SchemaRef{Type: "string", Enum: []string{"application/json", "application/octet-stream"}}},
				},
				Extensions: map[string]any{
					"x-downstream": map[string]any{
						"enabled": true,
						"name":    "get_item",
						"score":   1.5,
						"tags":    []any{"items", "read"},
						"nested":  map[string]any{"nullable": nil, "count": 3},
					},
				},
				Responses: []ir.Response{{
					StatusCode:  200,
					Description: "ok",
					Headers: []ir.Header{{
						Name:        "X-RateLimit-Remaining",
						Description: "Requests left in the current window.",
						Schema:      ir.SchemaRef{Type: "integer", Format: "int32"},
					}},
					Contents: []ir.BodyContent{{
						ContentType: "application/json",
						BodyKind:    "json",
						Schema:      &ir.SchemaRef{Ref: "Item"},
						Example: map[string]any{
							"id": "item_123",
						},
					}},
				}},
			},
		},
	}

	b, err := EmitYAML(docIR, Options{})
	require.NoError(t, err)

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(b)
	require.NoError(t, err)
	require.Equal(t, "3.0.0", doc.OpenAPI)
	require.Equal(t, "getItem", doc.Paths.Value("/items/{id}").Get.OperationID)
	require.Equal(t, "item_123", doc.Paths.Value("/items/{id}").Get.Parameters[0].Value.Example)
	require.Equal(t, []any{"application/json", "application/octet-stream"}, doc.Paths.Value("/items/{id}").Get.Parameters[1].Value.Schema.Value.Enum)
	require.Equal(t, "item_123", doc.Components.Schemas["Item"].Value.Example.(map[string]any)["id"])
	require.Equal(t, true, doc.Paths.Value("/items/{id}").Get.Extensions["x-downstream"].(map[string]any)["enabled"])
	require.Equal(t, []any{"items", "read"}, doc.Paths.Value("/items/{id}").Get.Extensions["x-downstream"].(map[string]any)["tags"])
	require.Equal(t, nil, doc.Paths.Value("/items/{id}").Get.Extensions["x-downstream"].(map[string]any)["nested"].(map[string]any)["nullable"])
	headers := doc.Paths.Value("/items/{id}").Get.Responses.Value("200").Value.Headers
	require.Contains(t, headers, "X-RateLimit-Remaining")
	require.Equal(t, openapi3.Types{"integer"}, *headers["X-RateLimit-Remaining"].Value.Schema.Value.Type)
	require.Equal(t, "item_123", doc.Paths.Value("/items/{id}").Get.Responses.Value("200").Value.Content.Get("application/json").Example.(map[string]any)["id"])

	var root yaml.Node
	require.NoError(t, yaml.Unmarshal(b, &root))
	itemProperty := lookupYAMLMappingNode(&root, "components", "schemas", "Envelope", "properties", "item")
	require.NotNil(t, itemProperty)
	require.False(t, mappingNodeHasKey(itemProperty, "example"))
	require.Contains(t, string(b), "example:")
}

func TestEmitYAMLIncludesTypedToolMetadata(t *testing.T) {
	doc := ir.Document{
		SchemaVersion: ir.CurrentSchemaVersion,
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "Tools", Version: "1"},
		Endpoints: []ir.Endpoint{{
			Method: "get", Path: "/items", OperationID: "listItems",
			Responses: []ir.Response{{StatusCode: 204, Description: "ok"}},
			Tool:      &ir.Tool{Name: "list_items", Effect: "read", Confirmation: "never", Output: ir.ToolOutput{Mode: "empty"}},
		}},
	}

	content, err := EmitYAML(doc, Options{})
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, yaml.Unmarshal(content, &raw))
	operation := raw["paths"].(map[string]any)["/items"].(map[string]any)["get"].(map[string]any)
	require.Equal(t, map[string]any{
		"name": "list_items", "effect": "read", "confirmation": "never", "output": map[string]any{"mode": "empty"},
	}, operation["x-apigen-tool"])
}

func TestEmitYAMLIncludesTypedCommandMetadata(t *testing.T) {
	doc := ir.Document{
		SchemaVersion: ir.CurrentSchemaVersion,
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "Commands", Version: "1"},
		Endpoints: []ir.Endpoint{{
			Method: "delete", Path: "/workspaces/{workspace}/bindings/{binding}", OperationID: "deleteBinding",
			Parameters: []ir.Parameter{
				{Name: "workspace", In: "path", Required: true, Schema: ir.SchemaRef{Type: "string"}},
				{Name: "binding", In: "path", Required: true, Schema: ir.SchemaRef{Type: "string"}},
			},
			Responses: []ir.Response{{StatusCode: 204, Description: "deleted"}},
			Command: &ir.Command{
				Owner:               "CommandAPI.Access",
				Audit:               ir.AuditPolicy{Required: true, SuccessAction: "binding.deleted", Guarantee: "transactional"},
				AdditionalExposures: []string{"ui"},
				Target:              &ir.OperationTarget{Parameter: "binding", Type: "binding"},
				AuthzMode:           "privilege",
				Privilege:           "MANAGE_GRANTS",
			},
		}},
	}

	content, err := EmitYAML(doc, Options{})
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, yaml.Unmarshal(content, &raw))
	operation := raw["paths"].(map[string]any)["/workspaces/{workspace}/bindings/{binding}"].(map[string]any)["delete"].(map[string]any)
	require.Equal(t, map[string]any{
		"owner":                "CommandAPI.Access",
		"audit":                map[string]any{"required": true, "success_action": "binding.deleted", "guarantee": "transactional"},
		"additional_exposures": []any{"ui"},
		"target":               map[string]any{"parameter": "binding", "type": "binding"},
		"authz_mode":           "privilege",
		"privilege":            "MANAGE_GRANTS",
	}, operation["x-apigen-command"])
}

func TestEmitYAML_EmitsMultipleContentKinds(t *testing.T) {
	t.Helper()

	docIR := ir.Document{
		SchemaVersion: "v4",
		Info:          ir.Info{Title: "test", Version: "1.0.0"},
		Endpoints: []ir.Endpoint{{
			Method:      "get",
			Path:        "/artifact",
			OperationID: "getArtifact",
			Responses: []ir.Response{{
				StatusCode:  200,
				Description: "ok",
				Contents: []ir.BodyContent{
					{ContentType: "application/json", BodyKind: "json", Schema: &ir.SchemaRef{Type: "string"}},
					{ContentType: "application/octet-stream", BodyKind: "binary", Schema: &ir.SchemaRef{Type: "string", Format: "binary"}},
				},
			}},
		}},
	}

	b, err := EmitYAML(docIR, Options{})
	require.NoError(t, err)

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(b)
	require.NoError(t, err)
	content := doc.Paths.Value("/artifact").Get.Responses.Value("200").Value.Content
	require.NotNil(t, content.Get("application/json"))
	require.NotNil(t, content.Get("application/octet-stream"))
	require.Equal(t, "binary", content.Get("application/octet-stream").Schema.Value.Format)
}

func TestEmitYAML_EmitsMultipartMetadata(t *testing.T) {
	t.Helper()

	docIR := ir.Document{
		SchemaVersion: "v4",
		Info:          ir.Info{Title: "test", Version: "1.0.0"},
		Schemas: map[string]ir.Schema{
			"Metadata": {Type: "object", Properties: map[string]ir.SchemaProperty{"name": {Schema: ir.SchemaRef{Type: "string"}}}},
		},
		Endpoints: []ir.Endpoint{{
			Method:      "post",
			Path:        "/artifact",
			OperationID: "uploadArtifact",
			RequestBody: &ir.RequestBody{Required: true, Contents: []ir.BodyContent{{
				ContentType: "multipart/form-data",
				BodyKind:    "multipart",
				Parts: []ir.MultipartPart{
					{Name: "metadata", WireName: "metadata", PartKind: "model", Required: true, ContentType: "application/json", BodyKind: "json", Schema: &ir.SchemaRef{Ref: "Metadata"}},
					{Name: "attachments", WireName: "attachments", PartKind: "model", Repeated: true, Required: true, ContentType: "application/octet-stream", BodyKind: "file", Filename: true, Schema: &ir.SchemaRef{Type: "string", Format: "binary"}},
				},
			}}},
			Responses: []ir.Response{{StatusCode: 204, Description: "ok"}},
		}},
	}

	b, err := EmitYAML(docIR, Options{})
	require.NoError(t, err)

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(b)
	require.NoError(t, err)
	media := doc.Paths.Value("/artifact").Post.RequestBody.Value.Content.Get("multipart/form-data")
	require.NotNil(t, media)
	require.Equal(t, []string{"metadata", "attachments"}, media.Schema.Value.Required)
	require.Equal(t, openapi3.Types{"array"}, *media.Schema.Value.Properties["attachments"].Value.Type)
	require.Equal(t, "binary", media.Schema.Value.Properties["attachments"].Value.Items.Value.Format)
	require.Equal(t, "application/json", media.Encoding["metadata"].ContentType)
	require.Equal(t, "application/octet-stream", media.Encoding["attachments"].ContentType)
}

func TestEmitYAML_EmitsMultipartMixedVendorMetadata(t *testing.T) {
	t.Helper()

	docIR := ir.Document{
		SchemaVersion: "v4",
		Info:          ir.Info{Title: "test", Version: "1.0.0"},
		Endpoints: []ir.Endpoint{{
			Method:      "post",
			Path:        "/artifact",
			OperationID: "uploadArtifact",
			RequestBody: &ir.RequestBody{Required: true, Contents: []ir.BodyContent{{
				ContentType: "multipart/mixed",
				BodyKind:    "multipart",
				Parts: []ir.MultipartPart{
					{Name: "part1", PartKind: "tuple", Required: true, ContentType: "application/json", BodyKind: "json", Schema: &ir.SchemaRef{Type: "object"}},
					{Name: "part2", PartKind: "tuple", Required: true, ContentType: "application/octet-stream", BodyKind: "file", Filename: true, Schema: &ir.SchemaRef{Type: "string", Format: "binary"}},
				},
			}}},
			Responses: []ir.Response{{StatusCode: 204, Description: "ok"}},
		}},
	}

	b, err := EmitYAML(docIR, Options{})
	require.NoError(t, err)
	var root yaml.Node
	require.NoError(t, yaml.Unmarshal(b, &root))

	media := lookupYAMLMappingNode(&root, "paths", "/artifact", "post", "requestBody", "content", "multipart/mixed")
	require.NotNil(t, media)
	require.Equal(t, "mixed", yamlScalarValue(lookupYAMLMappingNode(media, "x-apigen-multipart-kind")))
	parts := lookupYAMLMappingNode(media, "x-apigen-multipart-parts")
	require.NotNil(t, parts)
	require.Equal(t, yaml.SequenceNode, parts.Kind)
	require.Len(t, parts.Content, 2)
	require.Equal(t, "part1", yamlScalarValue(lookupYAMLMappingNode(parts.Content[0], "name")))
	require.Equal(t, "application/json", yamlScalarValue(lookupYAMLMappingNode(parts.Content[0], "content_type")))
	require.Equal(t, "json", yamlScalarValue(lookupYAMLMappingNode(parts.Content[0], "body_kind")))
	require.Equal(t, "part2", yamlScalarValue(lookupYAMLMappingNode(parts.Content[1], "name")))
	require.Equal(t, "application/octet-stream", yamlScalarValue(lookupYAMLMappingNode(parts.Content[1], "content_type")))
	require.Equal(t, "file", yamlScalarValue(lookupYAMLMappingNode(parts.Content[1], "body_kind")))
}

func TestEmitYAML_UsesAPIBasePathForVisibleRoutes(t *testing.T) {
	t.Helper()

	docIR := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/v1"},
		Info:          ir.Info{Title: "test", Version: "1.0.0"},
		Endpoints: []ir.Endpoint{
			{
				Method:      "get",
				Path:        "/items/{id}",
				OperationID: "getItem",
				Responses:   []ir.Response{{StatusCode: 200, Description: "ok"}},
			},
		},
	}

	b, err := EmitYAML(docIR, Options{})
	require.NoError(t, err)

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(b)
	require.NoError(t, err)
	require.NotNil(t, doc.Paths.Value("/v1/items/{id}"))
	require.Nil(t, doc.Paths.Value("/items/{id}"))
}

func yamlScalarValue(node *yaml.Node) string {
	if node == nil {
		return ""
	}
	return node.Value
}

func lookupYAMLMappingNode(root *yaml.Node, path ...string) *yaml.Node {
	current := root
	if current.Kind == yaml.DocumentNode && len(current.Content) > 0 {
		current = current.Content[0]
	}
	for _, key := range path {
		if current == nil || current.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for i := 0; i < len(current.Content); i += 2 {
			if current.Content[i].Value == key {
				next = current.Content[i+1]
				break
			}
		}
		current = next
	}
	return current
}

func mappingNodeHasKey(node *yaml.Node, key string) bool {
	if node == nil || node.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return true
		}
	}
	return false
}

func TestEmitYAML_EmitsDiscriminatedComposition(t *testing.T) {
	doc := ir.Document{
		SchemaVersion: ir.CurrentSchemaVersion,
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "Visual API", Version: "1"},
		Schemas: map[string]ir.Schema{
			"Visual": {
				Type:          "union",
				OneOf:         []ir.SchemaRef{{Ref: "ChartVisual"}, {Ref: "TextVisual"}},
				Discriminator: &ir.Discriminator{PropertyName: "shape", Mapping: map[string]string{"chart": "ChartVisual", "text": "TextVisual"}},
			},
			"VisualBase":  {Type: "object", Properties: map[string]ir.SchemaProperty{"shape": {Schema: ir.SchemaRef{Type: "string"}}}, Required: []string{"shape"}},
			"ChartVisual": {Type: "object", Base: &ir.SchemaRef{Ref: "VisualBase"}, Properties: map[string]ir.SchemaProperty{"shape": {Schema: ir.SchemaRef{Type: "string", Enum: []string{"chart"}}}}, Required: []string{"shape"}},
			"TextVisual":  {Type: "object", Base: &ir.SchemaRef{Ref: "VisualBase"}, Properties: map[string]ir.SchemaProperty{"shape": {Schema: ir.SchemaRef{Type: "string", Enum: []string{"text"}}}}, Required: []string{"shape"}},
		},
		Endpoints: []ir.Endpoint{{Method: "get", Path: "/visual", OperationID: "getVisual", Responses: []ir.Response{{StatusCode: 200, Description: "ok", Contents: []ir.BodyContent{{ContentType: "application/json", BodyKind: "json", Schema: &ir.SchemaRef{Ref: "Visual"}}}}}}},
	}

	b, err := EmitYAML(doc, Options{})
	require.NoError(t, err)
	content := string(b)
	require.Contains(t, content, "oneOf:\n        - $ref: '#/components/schemas/ChartVisual'")
	require.Contains(t, content, "propertyName: shape")
	require.Contains(t, content, "chart: '#/components/schemas/ChartVisual'")
	require.Contains(t, content, "ChartVisual:\n      type: object\n      allOf:\n        - $ref: '#/components/schemas/VisualBase'")
}

func TestEmitYAML_AddsGeneratedTransportErrorResponses(t *testing.T) {
	doc := ir.Document{
		SchemaVersion: ir.CurrentSchemaVersion,
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "Problem API", Version: "1"},
		Schemas:       map[string]ir.Schema{"Problem": {Type: "object"}},
		TransportErrors: &ir.TransportErrors{
			Schema:      ir.SchemaRef{Ref: "Problem"},
			ContentType: "application/problem+json",
			Failures: map[string]ir.TransportFailure{
				"malformed_body": {StatusCode: 400, Code: "malformed_body", PublicDetail: "Malformed body."},
				"internal":       {StatusCode: 500, Code: "internal", PublicDetail: "Internal error."},
			},
		},
		Endpoints: []ir.Endpoint{{Method: "get", Path: "/", OperationID: "get", Responses: []ir.Response{{StatusCode: 200, Description: "ok"}}}},
	}

	b, err := EmitYAML(doc, Options{})
	require.NoError(t, err)
	content := string(b)
	require.Contains(t, content, "'400':\n          description: Generated transport error")
	require.Contains(t, content, "application/problem+json:")
	require.Contains(t, content, "$ref: '#/components/schemas/Problem'")
	require.Contains(t, content, "'500':\n          description: Generated transport error")
}
