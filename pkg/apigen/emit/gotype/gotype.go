// Package gotype maps APIGen schema references to their Go wire types.
package gotype

import (
	"strings"

	"github.com/Yacobolo/toolbelt/apigen/ir"
)

// Ref resolves a schema reference recursively. named maps a normalized schema
// name to the name used by the calling emitter.
func Ref(ref ir.SchemaRef, named func(string) string) string {
	if ref.Ref != "" {
		name, ok := ir.NormalizedSchemaRefName(ref)
		if !ok {
			return "any"
		}
		return named(name)
	}

	if ref.AdditionalProperties != nil {
		if ref.AdditionalProperties.Schema != nil {
			return "map[string]" + Ref(*ref.AdditionalProperties.Schema, named)
		}
		return "map[string]any"
	}

	switch strings.ToLower(strings.TrimSpace(ref.Type)) {
	case "boolean":
		return "bool"
	case "integer":
		if strings.EqualFold(strings.TrimSpace(ref.Format), "int64") {
			return "int64"
		}
		return "int32"
	case "number":
		if strings.EqualFold(strings.TrimSpace(ref.Format), "float") || strings.EqualFold(strings.TrimSpace(ref.Format), "float32") {
			return "float32"
		}
		return "float64"
	case "array":
		if ref.Items != nil {
			return "[]" + Ref(*ref.Items, named)
		}
		return "[]any"
	case "object":
		return "map[string]any"
	case "string":
		return "string"
	default:
		return "any"
	}
}

// Schema resolves a named schema's underlying Go type.
func Schema(schema ir.Schema, named func(string) string) string {
	if schema.Type == "array" {
		if schema.Items != nil {
			return "[]" + Ref(*schema.Items, named)
		}
		return "[]any"
	}
	return Ref(ir.SchemaRef{Type: schema.Type}, named)
}
