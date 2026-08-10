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

func decodedObject(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(b, &decoded))
	return decoded
}
