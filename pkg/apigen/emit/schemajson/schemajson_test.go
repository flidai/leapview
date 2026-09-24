package schemajson

import (
	"testing"

	"github.com/Yacobolo/toolbelt/apigen/ir"
	"github.com/stretchr/testify/require"
)

func TestRefPreservesPatternsAndPropertyNames(t *testing.T) {
	doc := ir.Document{
		Schemas: map[string]ir.Schema{
			"Values": {Type: "object"},
		},
	}
	got := Ref(doc, ir.SchemaRef{
		Ref:     "Values",
		Pattern: "^[A-Z]+$",
		PropertyNames: &ir.SchemaRef{
			Type:    "string",
			Pattern: "^[a-z_]+$",
		},
		AdditionalProperties: &ir.AdditionalProperties{Schema: &ir.SchemaRef{Type: "string"}},
	})
	require.Equal(t, "object", got["type"])
	require.Equal(t, "^[A-Z]+$", got["pattern"])
	require.Equal(t, "^[a-z_]+$", got["propertyNames"].(map[string]any)["pattern"])
	require.Equal(t, map[string]any{"type": "string"}, got["additionalProperties"])
}

func TestRefPreservesNumericConstantsAndArrayBounds(t *testing.T) {
	constant := 1.0
	minimum := 1
	maximum := 100
	got := Ref(ir.Document{}, ir.SchemaRef{
		Type: "array", MinItems: &minimum, MaxItems: &maximum,
		Items: &ir.SchemaRef{Type: "integer", Const: &constant},
	})
	require.Equal(t, 1, got["minItems"])
	require.Equal(t, 100, got["maxItems"])
	require.Equal(t, float64(1), got["items"].(map[string]any)["const"])
}
