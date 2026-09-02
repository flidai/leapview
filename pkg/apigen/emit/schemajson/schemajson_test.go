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

func TestRefPreservesItemBounds(t *testing.T) {
	minItems, maxItems := 1, 4
	got := Ref(ir.Document{}, ir.SchemaRef{Type: "array", MinItems: &minItems, MaxItems: &maxItems, UniqueItems: true})
	require.Equal(t, 1, got["minItems"])
	require.Equal(t, 4, got["maxItems"])
	require.Equal(t, true, got["uniqueItems"])
}
