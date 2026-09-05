package jsonschema

import (
	"encoding/json"
	"testing"

	"github.com/Yacobolo/toolbelt/apigen/ir"
	"github.com/stretchr/testify/require"
)

func TestEmit_PreservesContractAndPropertyMetadata(t *testing.T) {
	doc := ir.Document{
		Info: ir.Info{Title: "LibreDash Signal Contracts", Version: "1.0.0"},
		Contracts: []ir.Contract{{
			Name:       "DashboardEnvelope",
			Schema:     ir.SchemaRef{Ref: "DashboardEnvelope"},
			Kind:       "ui-signal",
			Tags:       []string{"dashboard"},
			Extensions: map[string]any{"x-libredash-surface": "dashboard"},
		}},
		Schemas: map[string]ir.Schema{
			"DashboardEnvelope": {
				Type: "object",
				Properties: map[string]ir.SchemaProperty{
					"visuals": {
						Schema:     ir.SchemaRef{Type: "object", AdditionalProperties: &ir.AdditionalProperties{Schema: &ir.SchemaRef{Ref: "DashboardVisual"}}},
						Extensions: map[string]any{"x-libredash-signal-key": "visuals"},
					},
				},
				Required: []string{"visuals"},
			},
			"DashboardVisual": {Type: "object", Properties: map[string]ir.SchemaProperty{"id": {Schema: ir.SchemaRef{Type: "string"}}}, Required: []string{"id"}},
		},
	}

	b, err := Emit(doc, Options{ID: "https://example.test/contracts.schema.json"})
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(b, &decoded))

	require.Equal(t, "https://json-schema.org/draft/2020-12/schema", decoded["$schema"])
	require.Equal(t, "https://example.test/contracts.schema.json", decoded["$id"])
	require.Equal(t, "LibreDash Signal Contracts", decoded["title"])
	require.Contains(t, string(b), `"x-libredash-surface": "dashboard"`)
	require.Contains(t, string(b), `"x-libredash-signal-key": "visuals"`)
	require.Contains(t, string(b), `"#/$defs/DashboardEnvelope"`)
}

func TestEmit_PreservesPatternAndPropertyNames(t *testing.T) {
	doc := ir.Document{
		Info:      ir.Info{Title: "Constrained", Version: "1"},
		Contracts: []ir.Contract{{Name: "payload", Schema: ir.SchemaRef{Ref: "Payload"}}},
		Schemas: map[string]ir.Schema{
			"Payload": {
				Type: "object",
				Properties: map[string]ir.SchemaProperty{
					"values": {Schema: ir.SchemaRef{
						Type:                 "object",
						AdditionalProperties: &ir.AdditionalProperties{Schema: &ir.SchemaRef{Type: "string"}},
						PropertyNames:        &ir.SchemaRef{Type: "string", Pattern: "^[a-z_]+$"},
						Pattern:              "^[A-Z]+$",
					}},
				},
			},
		},
	}
	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	decoded := decodedObject(t, b)
	defs := decoded["$defs"].(map[string]any)
	payload := defs["Payload"].(map[string]any)
	refSibling := payload["properties"].(map[string]any)["values"].(map[string]any)
	require.Equal(t, "^[A-Z]+$", refSibling["pattern"])
	require.Equal(t, "^[a-z_]+$", refSibling["propertyNames"].(map[string]any)["pattern"])
	require.Equal(t, map[string]any{"type": "string"}, refSibling["additionalProperties"])
}

func TestEmit_PreservesItemBounds(t *testing.T) {
	minItems, maxItems := 1, 4
	doc := ir.Document{
		Info:      ir.Info{Title: "Constrained", Version: "1"},
		Contracts: []ir.Contract{{Name: "payload", Schema: ir.SchemaRef{Ref: "Payload"}}},
		Schemas: map[string]ir.Schema{
			"Payload": {
				Type: "object",
				Properties: map[string]ir.SchemaProperty{
					"values": {Schema: ir.SchemaRef{Type: "array", Items: &ir.SchemaRef{Type: "string"}, MinItems: &minItems, MaxItems: &maxItems, UniqueItems: true}},
				},
			},
		},
	}
	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	decoded := decodedObject(t, b)
	values := decoded["$defs"].(map[string]any)["Payload"].(map[string]any)["properties"].(map[string]any)["values"].(map[string]any)
	require.Equal(t, float64(1), values["minItems"])
	require.Equal(t, float64(4), values["maxItems"])
	require.Equal(t, true, values["uniqueItems"])
}

func TestEmit_PreservesDiscriminatedComposition(t *testing.T) {
	doc := ir.Document{
		Info: ir.Info{Title: "Visuals", Version: "1"},
		Schemas: map[string]ir.Schema{
			"Visual":      {Type: "union", OneOf: []ir.SchemaRef{{Ref: "ChartVisual"}, {Ref: "TextVisual"}}, Discriminator: &ir.Discriminator{PropertyName: "shape", Mapping: map[string]string{"chart": "ChartVisual", "text": "TextVisual"}}},
			"VisualBase":  {Type: "object", Properties: map[string]ir.SchemaProperty{"shape": {Schema: ir.SchemaRef{Type: "string"}}}, Required: []string{"shape"}},
			"ChartVisual": {Type: "object", Base: &ir.SchemaRef{Ref: "VisualBase"}, Properties: map[string]ir.SchemaProperty{"shape": {Schema: ir.SchemaRef{Type: "string", Enum: []string{"chart"}}}}, Required: []string{"shape"}},
			"TextVisual":  {Type: "object", Base: &ir.SchemaRef{Ref: "VisualBase"}, Properties: map[string]ir.SchemaProperty{"shape": {Schema: ir.SchemaRef{Type: "string", Enum: []string{"text"}}}}, Required: []string{"shape"}},
		},
		Contracts: []ir.Contract{{Name: "visual", Schema: ir.SchemaRef{Ref: "Visual"}}},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	require.Contains(t, string(b), `"oneOf"`)
	require.Contains(t, string(b), `"allOf"`)
	require.Contains(t, string(b), `"chart"`)
	defs := decodedObject(t, b)["$defs"].(map[string]any)
	require.NotContains(t, defs["VisualBase"].(map[string]any), "unevaluatedProperties")
	require.Equal(t, false, defs["ChartVisual"].(map[string]any)["unevaluatedProperties"])
}

func TestEmit_DeterministicUnorderedDocumentPreservesMetadataAndSealing(t *testing.T) {
	// Deliberately populate the schema and property maps in an order that does
	// not match their lexical order. Emit must not expose Go map iteration order
	// in its byte output.
	doc := ir.Document{
		Info: ir.Info{Title: "Visual Contracts", Description: "Contracts for visual payloads"},
		Contracts: []ir.Contract{{
			Name:        "visual",
			Schema:      ir.SchemaRef{Ref: "Envelope"},
			Kind:        "ui-signal",
			Tags:        []string{"dashboard", "visual"},
			Description: "A discriminated visual payload",
			Extensions:  map[string]any{"x-contract-owner": "analytics"},
		}},
		Schemas: unorderedSchemas(),
	}

	want, err := Emit(doc, Options{ID: "https://example.test/visual.schema.json"})
	require.NoError(t, err)
	for i := 0; i < 100; i++ {
		got, err := Emit(doc, Options{ID: "https://example.test/visual.schema.json"})
		require.NoError(t, err)
		require.Equal(t, want, got, "emit iteration %d changed bytes", i)
	}

	decoded := decodedObject(t, want)
	require.Equal(t, "Contracts for visual payloads", decoded["description"])
	contracts := decoded["x-apigen-contracts"].([]any)
	contract := contracts[0].(map[string]any)
	require.Equal(t, "A discriminated visual payload", contract["description"])
	require.Equal(t, "analytics", contract["x-contract-owner"])

	defs := decoded["$defs"].(map[string]any)
	envelope := defs["Envelope"].(map[string]any)
	require.Equal(t, "Envelope schema", envelope["description"])
	require.Equal(t, "analytics", envelope["x-schema-owner"])
	properties := envelope["properties"].(map[string]any)
	visuals := properties["visuals"].(map[string]any)
	require.Equal(t, "Visual payloads", visuals["description"])
	require.Equal(t, "visuals", visuals["x-signal-key"])

	visual := defs["Visual"].(map[string]any)
	require.NotContains(t, visual, "type", "union schemas should be represented by composition")
	require.Len(t, visual["oneOf"], 2)

	base := defs["VisualBase"].(map[string]any)
	require.NotContains(t, base, "unevaluatedProperties", "base schemas must remain open for inheritance")
	chart := defs["ChartVisual"].(map[string]any)
	require.Len(t, chart["allOf"], 1, "derived variants must retain their base composition")
	require.Equal(t, false, chart["unevaluatedProperties"], "derived object schemas must be sealed")
}

func unorderedSchemas() map[string]ir.Schema {
	schemas := make(map[string]ir.Schema, 5)
	// Keep insertion order intentionally different from lexical order.
	schemas["TextVisual"] = ir.Schema{
		Type:       "object",
		Base:       &ir.SchemaRef{Ref: "VisualBase"},
		Properties: map[string]ir.SchemaProperty{"shape": {Schema: ir.SchemaRef{Type: "string", Enum: []string{"text"}}}},
		Required:   []string{"shape"},
	}
	schemas["ChartVisual"] = ir.Schema{
		Type:       "object",
		Base:       &ir.SchemaRef{Ref: "VisualBase"},
		Properties: map[string]ir.SchemaProperty{"shape": {Schema: ir.SchemaRef{Type: "string", Enum: []string{"chart"}}}},
		Required:   []string{"shape"},
	}
	schemas["VisualBase"] = ir.Schema{
		Type:       "object",
		Properties: map[string]ir.SchemaProperty{"shape": {Schema: ir.SchemaRef{Type: "string"}}},
		Required:   []string{"shape"},
	}
	schemas["Visual"] = ir.Schema{
		Type:          "union",
		OneOf:         []ir.SchemaRef{{Ref: "ChartVisual"}, {Ref: "TextVisual"}},
		Discriminator: &ir.Discriminator{PropertyName: "shape", Mapping: map[string]string{"chart": "ChartVisual", "text": "TextVisual"}},
	}
	schemas["Envelope"] = ir.Schema{
		Type:        "object",
		Description: "Envelope schema",
		Extensions:  map[string]any{"x-schema-owner": "analytics"},
		Properties: map[string]ir.SchemaProperty{
			"visuals": {
				Schema:      ir.SchemaRef{Ref: "Visual"},
				Description: "Visual payloads",
				Extensions:  map[string]any{"x-signal-key": "visuals"},
			},
			"status": {Schema: ir.SchemaRef{Type: "string"}},
		},
		Required: []string{"status", "visuals"},
	}
	return schemas
}

func decodedObject(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(b, &decoded))
	return decoded
}
