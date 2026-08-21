// Package jsonschema emits JSON Schema artifacts from APIGen data contracts.
package jsonschema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/Yacobolo/toolbelt/apigen/emit/contractutil"
	"github.com/Yacobolo/toolbelt/apigen/ir"
)

// Options configures JSON Schema emission.
type Options struct {
	ID    string
	Title string
}

// Emit renders a draft 2020-12 JSON Schema document for doc.Contracts.
func Emit(doc ir.Document, opts Options) ([]byte, error) {
	defs := map[string]any{}
	baseSchemas := referencedBaseSchemas(doc)
	for _, name := range contractutil.DependencyNames(doc) {
		schema, ok := doc.Schemas[name]
		if !ok {
			return nil, fmt.Errorf("contract schema %q is missing", name)
		}
		_, isBase := baseSchemas[name]
		defs[name] = schemaObject(schema, isBase)
	}
	roots := make([]any, 0, len(doc.Contracts))
	contracts := make([]any, 0, len(doc.Contracts))
	for _, contract := range doc.Contracts {
		roots = append(roots, schemaRefObject(contract.Schema))
		entry := map[string]any{
			"name":   contract.Name,
			"schema": schemaRefObject(contract.Schema),
		}
		if contract.Kind != "" {
			entry["kind"] = contract.Kind
		}
		if contract.Description != "" {
			entry["description"] = contract.Description
		}
		if len(contract.Tags) > 0 {
			entry["tags"] = append([]string(nil), contract.Tags...)
		}
		copyExtensions(entry, contract.Extensions)
		contracts = append(contracts, entry)
	}
	out := map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$defs":   defs,
	}
	if opts.ID != "" {
		out["$id"] = opts.ID
	}
	if opts.Title != "" {
		out["title"] = opts.Title
	} else if doc.Info.Title != "" {
		out["title"] = doc.Info.Title
	}
	if doc.Info.Description != "" {
		out["description"] = doc.Info.Description
	}
	if len(roots) > 0 {
		out["anyOf"] = roots
		out["x-apigen-contracts"] = contracts
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func schemaObject(schema ir.Schema, isBase bool) map[string]any {
	out := map[string]any{}
	schemaType := schema.Type
	if schemaType == "" {
		schemaType = "object"
	}
	if schemaType != "union" {
		out["type"] = schemaType
	}
	if schema.Base != nil {
		out["allOf"] = []any{schemaRefObject(*schema.Base)}
	}
	if len(schema.OneOf) > 0 {
		variants := make([]any, 0, len(schema.OneOf))
		for _, variant := range schema.OneOf {
			variants = append(variants, schemaRefObject(variant))
		}
		out["oneOf"] = variants
	}
	if schema.Description != "" {
		out["description"] = schema.Description
	}
	if schema.Example != nil {
		out["example"] = schema.Example
	}
	copyExtensions(out, schema.Extensions)
	if len(schema.Enum) > 0 {
		out["enum"] = append([]string(nil), schema.Enum...)
	}
	if schema.Items != nil {
		out["items"] = schemaRefObject(*schema.Items)
	}
	if len(schema.Properties) > 0 {
		properties := map[string]any{}
		for _, name := range contractutil.OrderedProperties(schema) {
			properties[name] = schemaPropertyObject(schema.Properties[name])
		}
		out["properties"] = properties
	}
	if len(schema.Required) > 0 {
		required := append([]string(nil), schema.Required...)
		sort.Strings(required)
		out["required"] = required
	}
	if schemaType == "object" && !isBase && (len(schema.Properties) > 0 || schema.Base != nil) {
		out["unevaluatedProperties"] = false
	}
	return out
}

func referencedBaseSchemas(doc ir.Document) map[string]struct{} {
	bases := make(map[string]struct{})
	for _, schema := range doc.Schemas {
		if schema.Base == nil {
			continue
		}
		if name, ok := ir.NormalizedSchemaRefName(*schema.Base); ok {
			bases[name] = struct{}{}
		}
	}
	return bases
}

func schemaPropertyObject(property ir.SchemaProperty) map[string]any {
	out := schemaRefObject(property.Schema)
	if property.Description != "" {
		out["description"] = property.Description
	}
	if property.Example != nil {
		out["example"] = property.Example
	}
	copyExtensions(out, property.Extensions)
	return out
}

func schemaRefObject(ref ir.SchemaRef) map[string]any {
	if ref.Ref != "" && ref.Minimum == nil && ref.Maximum == nil && ref.MinLength == nil && ref.MaxLength == nil && ref.MinProperties == nil && ref.Pattern == "" && ref.PropertyNames == nil {
		if name, ok := ir.NormalizedSchemaRefName(ref); ok {
			return map[string]any{"$ref": "#/$defs/" + name}
		}
	}
	out := map[string]any{}
	if ref.Ref != "" {
		if name, ok := ir.NormalizedSchemaRefName(ref); ok {
			out["$ref"] = "#/$defs/" + name
		}
	}
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
	if ref.MinProperties != nil {
		out["minProperties"] = *ref.MinProperties
	}
	if ref.Pattern != "" {
		out["pattern"] = ref.Pattern
	}
	if ref.Items != nil {
		out["items"] = schemaRefObject(*ref.Items)
	}
	if ref.AdditionalProperties != nil {
		if ref.AdditionalProperties.Any {
			out["additionalProperties"] = true
		} else if ref.AdditionalProperties.Schema != nil {
			out["additionalProperties"] = schemaRefObject(*ref.AdditionalProperties.Schema)
		}
	}
	if ref.PropertyNames != nil {
		out["propertyNames"] = schemaRefObject(*ref.PropertyNames)
	}
	return out
}

func copyExtensions(out map[string]any, extensions map[string]any) {
	for key, value := range extensions {
		out[key] = value
	}
}
