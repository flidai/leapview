// Package schemajson expands APIGen schema references into portable JSON Schema values.
package schemajson

import "github.com/Yacobolo/toolbelt/apigen/ir"

// Ref expands ref and its transitive named dependencies. Recursive references
// become unconstrained values because generated portable schemas have no defs registry.
func Ref(doc ir.Document, ref ir.SchemaRef) map[string]any {
	return schemaRef(doc, ref, map[string]bool{})
}

func schemaRef(doc ir.Document, ref ir.SchemaRef, active map[string]bool) map[string]any {
	if ref.Ref != "" {
		name, ok := ir.NormalizedSchemaRefName(ref)
		if !ok || active[name] {
			return map[string]any{}
		}
		schema, ok := doc.Schemas[name]
		if !ok {
			return map[string]any{}
		}
		active[name] = true
		out := schemaValue(doc, schema, active)
		delete(active, name)
		return out
	}
	out := map[string]any{}
	if ref.Type != "" {
		out["type"] = ref.Type
	}
	if ref.Format != "" {
		out["format"] = ref.Format
	}
	if len(ref.Enum) > 0 {
		out["enum"] = append([]string(nil), ref.Enum...)
	}
	if ref.Minimum != nil {
		out["minimum"] = *ref.Minimum
	}
	if ref.Maximum != nil {
		out["maximum"] = *ref.Maximum
	}
	if ref.MinLength != nil {
		out["minLength"] = *ref.MinLength
	}
	if ref.MaxLength != nil {
		out["maxLength"] = *ref.MaxLength
	}
	if ref.Items != nil {
		out["items"] = schemaRef(doc, *ref.Items, active)
	}
	if ref.AdditionalProperties != nil {
		if ref.AdditionalProperties.Schema != nil {
			out["additionalProperties"] = schemaRef(doc, *ref.AdditionalProperties.Schema, active)
		} else {
			out["additionalProperties"] = ref.AdditionalProperties.Any
		}
	}
	return out
}

func schemaValue(doc ir.Document, schema ir.Schema, active map[string]bool) map[string]any {
	if schema.Type == "object" {
		schema = ir.FlattenObjectSchema(doc, schema)
	}
	out := map[string]any{}
	if schema.Type != "" && schema.Type != "union" {
		out["type"] = schema.Type
	}
	if len(schema.OneOf) > 0 {
		variants := make([]any, 0, len(schema.OneOf))
		for _, variant := range schema.OneOf {
			variants = append(variants, schemaRef(doc, variant, active))
		}
		out["oneOf"] = variants
	}
	if len(schema.Enum) > 0 {
		out["enum"] = append([]string(nil), schema.Enum...)
	}
	if schema.Type == "object" {
		properties := make(map[string]any, len(schema.Properties))
		for _, name := range ir.OrderedPropertyNames(schema) {
			property := schema.Properties[name]
			value := schemaRef(doc, property.Schema, active)
			if property.Description != "" {
				value["description"] = property.Description
			}
			properties[name] = value
		}
		out["properties"] = properties
		out["additionalProperties"] = false
		if len(schema.Required) > 0 {
			out["required"] = append([]string(nil), schema.Required...)
		}
	}
	if schema.Items != nil {
		out["items"] = schemaRef(doc, *schema.Items, active)
	}
	return out
}
