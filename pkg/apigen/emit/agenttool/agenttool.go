// Package agenttool compiles typed IR endpoint tools into portable runtime descriptors.
package agenttool

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Yacobolo/toolbelt/apigen/ir"
	runtime "github.com/Yacobolo/toolbelt/apigen/runtime/agenttool"
)

// Build compiles all typed endpoint tools keyed by public tool name.
func Build(doc ir.Document) (map[string]runtime.Contract, error) {
	contracts := make(map[string]runtime.Contract)
	for _, endpoint := range doc.Endpoints {
		if endpoint.Tool == nil {
			continue
		}
		contract, err := buildContract(doc, endpoint)
		if err != nil {
			return nil, fmt.Errorf("compile tool %q: %w", endpoint.Tool.Name, err)
		}
		contracts[contract.Name] = contract
	}
	return contracts, nil
}

func buildContract(doc ir.Document, endpoint ir.Endpoint) (runtime.Contract, error) {
	output, outputSchema, responseContentType, err := buildOutput(doc, endpoint)
	if err != nil {
		return runtime.Contract{}, err
	}
	bindings, inputSchema, err := buildInput(doc, endpoint, responseContentType)
	if err != nil {
		return runtime.Contract{}, err
	}
	description := endpoint.Tool.Description
	if description == "" {
		description = endpoint.Summary
	}
	if description == "" {
		description = endpoint.Description
	}
	return runtime.Contract{
		Name:                endpoint.Tool.Name,
		OperationID:         endpoint.OperationID,
		Method:              strings.ToUpper(endpoint.Method),
		Path:                ir.JoinAPIPath(doc.API.BasePath, endpoint.Path),
		Description:         description,
		Effect:              runtime.Effect(endpoint.Tool.Effect),
		Confirmation:        runtime.Confirmation(defaultConfirmation(endpoint.Tool.Effect, endpoint.Tool.Confirmation)),
		Tags:                append([]string(nil), endpoint.Tool.Tags...),
		InputSchema:         inputSchema,
		OutputSchema:        outputSchema,
		ResponseContentType: responseContentType,
		Bindings:            bindings,
		Output:              output,
		Metadata:            cloneMap(endpoint.Tool.Metadata),
	}, nil
}

type inputSource struct {
	Source      string
	Name        string
	Required    bool
	Description string
	Explode     bool
	Schema      ir.SchemaRef
}

func buildInput(doc ir.Document, endpoint ir.Endpoint, responseContentType string) ([]runtime.Binding, json.RawMessage, error) {
	sources := make([]inputSource, 0, len(endpoint.Parameters)+4)
	for _, parameter := range endpoint.Parameters {
		sources = append(sources, inputSource{
			Source: parameter.In, Name: parameter.Name, Required: parameter.Required,
			Description: parameter.Description, Explode: parameter.Explode != nil && *parameter.Explode, Schema: parameter.Schema,
		})
	}
	if endpoint.RequestBody != nil {
		content := endpoint.RequestBody.Contents[0]
		bodyRef := *content.Schema
		if schema, ok := concreteSchema(doc, bodyRef); ok && schema.Type == "object" {
			required := set(schema.Required)
			for _, name := range ir.OrderedPropertyNames(schema) {
				property := schema.Properties[name]
				sources = append(sources, inputSource{Source: "body", Name: name, Required: required[name], Description: property.Description, Schema: property.Schema})
			}
		} else {
			sources = append(sources, inputSource{Source: "body", Name: "$", Required: endpoint.RequestBody.Required, Schema: bodyRef})
		}
	}
	overrides := map[string]ir.ToolInputField{}
	if endpoint.Tool.Input != nil {
		for _, field := range endpoint.Tool.Input.Fields {
			overrides[field.Source+"\x00"+field.Name] = field
		}
	}

	bindings := make([]runtime.Binding, 0, len(sources))
	properties := map[string]any{}
	requiredArguments := []string{}
	for _, source := range sources {
		override := overrides[source.Source+"\x00"+source.Name]
		mode := override.Mode
		if mode == "" {
			mode = "model"
		}
		defaultValue := override.Default
		contextKey := override.ContextKey
		if source.Source == "header" && strings.EqualFold(source.Name, "Accept") && responseContentType != "" {
			mode = "omit"
			defaultValue = responseContentType
			contextKey = ""
		}
		argument := override.Alias
		if argument == "" {
			argument = source.Name
			if argument == "$" {
				argument = "body"
			}
		}
		description := override.Description
		if description == "" {
			description = source.Description
		}
		binding := runtime.Binding{
			Argument: argument, Source: source.Source, WireName: source.Name, Mode: mode,
			ContextKey: contextKey, Description: description, Required: source.Required,
			Default: defaultValue, Explode: source.Explode, Schema: valueSchema(doc, source.Schema),
		}
		if mode != "model" {
			binding.Argument = ""
		}
		bindings = append(bindings, binding)
		if mode == "model" {
			property := schemaRefJSON(doc, source.Schema, map[string]bool{})
			if description != "" {
				property["description"] = description
			}
			properties[argument] = property
			if source.Required && defaultValue == nil {
				requiredArguments = append(requiredArguments, argument)
			}
		}
	}
	sort.Strings(requiredArguments)
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(requiredArguments) > 0 {
		schema["required"] = requiredArguments
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, nil, fmt.Errorf("encode input schema: %w", err)
	}
	return bindings, encoded, nil
}

func buildOutput(doc ir.Document, endpoint ir.Endpoint) (runtime.Output, json.RawMessage, string, error) {
	authored := endpoint.Tool.Output
	output := runtime.Output{Mode: authored.Mode}
	successRef, responseContentType, hasBody, err := ir.ToolSuccessSchema(endpoint)
	if err != nil {
		return runtime.Output{}, nil, "", err
	}
	if authored.Mode == "project" {
		for _, projection := range authored.Select {
			compiled, err := compileProjection(doc, successRef, projection)
			if err != nil {
				return runtime.Output{}, nil, "", err
			}
			output.Select = append(output.Select, compiled)
		}
		if authored.Cursor != nil {
			_, optional, err := resolvePointer(doc, successRef, authored.Cursor.Source)
			if err != nil {
				return runtime.Output{}, nil, "", err
			}
			output.Cursor = &runtime.Cursor{
				Source: authored.Cursor.Source, Target: defaultString(authored.Cursor.Target, "nextCursor"),
				HasMoreTarget: defaultString(authored.Cursor.HasMoreTarget, "hasMore"), Optional: optional,
			}
		}
	}
	var schema map[string]any
	switch authored.Mode {
	case "project":
		schema = projectedSchema(doc, output)
	case "raw":
		if hasBody {
			schema = schemaRefJSON(doc, successRef, map[string]bool{})
		} else {
			schema = map[string]any{"type": "object", "properties": map[string]any{"status": map[string]any{"type": "integer"}}, "required": []string{"status"}, "additionalProperties": false}
		}
	case "empty":
		schema = map[string]any{"type": "object", "properties": map[string]any{"status": map[string]any{"type": "integer"}}, "required": []string{"status"}, "additionalProperties": false}
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return runtime.Output{}, nil, "", fmt.Errorf("encode output schema: %w", err)
	}
	return output, encoded, responseContentType, nil
}

func compileProjection(doc ir.Document, scope ir.SchemaRef, authored ir.ToolProjection) (runtime.Projection, error) {
	selected, optional, err := resolvePointer(doc, scope, authored.Source)
	if err != nil {
		return runtime.Projection{}, err
	}
	kind, child := projectionKind(doc, selected)
	target := authored.Target
	if target == "" {
		segments := pointerSegments(authored.Source)
		target = segments[len(segments)-1]
	}
	compiled := runtime.Projection{Source: authored.Source, Target: target, Kind: kind, Schema: valueSchema(doc, selected), Optional: optional, CountAs: authored.CountAs}
	for _, nested := range authored.Select {
		childProjection, err := compileProjection(doc, child, nested)
		if err != nil {
			return runtime.Projection{}, err
		}
		compiled.Select = append(compiled.Select, childProjection)
	}
	return compiled, nil
}

func resolvePointer(doc ir.Document, scope ir.SchemaRef, pointer string) (ir.SchemaRef, bool, error) {
	return ir.ResolveSchemaPointer(doc, scope, pointer)
}

func projectionKind(doc ir.Document, ref ir.SchemaRef) (string, ir.SchemaRef) {
	return ir.SchemaProjectionKind(doc, ref)
}

func valueSchema(doc ir.Document, ref ir.SchemaRef) runtime.ValueSchema {
	result := runtime.ValueSchema{
		Type: ref.Type, Format: ref.Format, Const: ref.Const, Enum: append([]string(nil), ref.Enum...),
		Minimum: ref.Minimum, Maximum: ref.Maximum, MinLength: ref.MinLength, MaxLength: ref.MaxLength,
		MinItems: ref.MinItems, MaxItems: ref.MaxItems,
		AdditionalProperties: ref.AdditionalProperties != nil,
	}
	if schema, ok := concreteSchema(doc, ref); ok {
		result.Type = schema.Type
		if len(result.Enum) == 0 {
			result.Enum = append([]string(nil), schema.Enum...)
		}
		if schema.Items != nil {
			item := valueSchema(doc, *schema.Items)
			result.Items = &item
		}
		return result
	}
	if ref.Items != nil {
		item := valueSchema(doc, *ref.Items)
		result.Items = &item
	}
	return result
}

func schemaRefJSON(doc ir.Document, ref ir.SchemaRef, seen map[string]bool) map[string]any {
	out := map[string]any{}
	if ref.Ref != "" {
		name, _ := ir.NormalizedSchemaRefName(ref)
		if seen[name] {
			return map[string]any{"type": "object"}
		}
		seen[name] = true
		schema, _ := ir.ResolveSchema(doc, ref)
		out = schemaJSON(doc, schema, seen)
		delete(seen, name)
	} else if ref.Type != "" {
		out["type"] = ref.Type
	}
	if ref.Const != nil {
		out["const"] = *ref.Const
	}
	if len(ref.Enum) > 0 {
		out["enum"] = ref.Enum
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
	if ref.MinItems != nil {
		out["minItems"] = *ref.MinItems
	}
	if ref.MaxItems != nil {
		out["maxItems"] = *ref.MaxItems
	}
	if ref.MinProperties != nil {
		out["minProperties"] = *ref.MinProperties
	}
	if ref.Pattern != "" {
		out["pattern"] = ref.Pattern
	}
	if ref.Items != nil {
		out["items"] = schemaRefJSON(doc, *ref.Items, seen)
	}
	if ref.AdditionalProperties != nil {
		if ref.AdditionalProperties.Schema != nil {
			out["additionalProperties"] = schemaRefJSON(doc, *ref.AdditionalProperties.Schema, seen)
		} else {
			out["additionalProperties"] = ref.AdditionalProperties.Any
		}
	}
	if ref.PropertyNames != nil {
		out["propertyNames"] = schemaRefJSON(doc, *ref.PropertyNames, seen)
	}
	return out
}

func schemaJSON(doc ir.Document, schema ir.Schema, seen map[string]bool) map[string]any {
	out := map[string]any{}
	if schema.Type != "" && schema.Type != "union" {
		out["type"] = schema.Type
	}
	if schema.Base != nil && schema.Type != "object" {
		out["allOf"] = []any{schemaRefJSON(doc, *schema.Base, seen)}
	}
	if len(schema.OneOf) > 0 {
		variants := make([]any, 0, len(schema.OneOf))
		for _, variant := range schema.OneOf {
			variants = append(variants, schemaRefJSON(doc, variant, seen))
		}
		out["oneOf"] = variants
	}
	if schema.Description != "" {
		out["description"] = schema.Description
	}
	if len(schema.Enum) > 0 {
		out["enum"] = schema.Enum
	}
	if schema.Type == "object" {
		schema = ir.FlattenObjectSchema(doc, schema)
		properties := map[string]any{}
		for _, name := range ir.OrderedPropertyNames(schema) {
			property := schema.Properties[name]
			value := schemaRefJSON(doc, property.Schema, seen)
			if property.Description != "" {
				value["description"] = property.Description
			}
			properties[name] = value
		}
		out["properties"] = properties
		out["additionalProperties"] = false
		if len(schema.Required) > 0 {
			out["required"] = schema.Required
		}
	}
	if schema.Items != nil {
		out["items"] = schemaRefJSON(doc, *schema.Items, seen)
	}
	return out
}

func projectedSchema(_ ir.Document, output runtime.Output) map[string]any {
	properties := map[string]any{}
	required := []string{}
	for _, projection := range output.Select {
		properties[projection.Target] = projectionSchema(projection)
		if !projection.Optional {
			required = append(required, projection.Target)
		}
		if projection.CountAs != "" {
			properties[projection.CountAs] = map[string]any{"type": "integer"}
			required = append(required, projection.CountAs)
		}
	}
	if output.Cursor != nil {
		properties[output.Cursor.Target] = map[string]any{"type": "string"}
		properties[output.Cursor.HasMoreTarget] = map[string]any{"type": "boolean"}
		required = append(required, output.Cursor.HasMoreTarget)
	}
	sort.Strings(required)
	out := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func projectionSchema(projection runtime.Projection) map[string]any {
	if len(projection.Select) == 0 {
		return valueSchemaJSON(projection.Schema)
	}
	child := projectedSchema(ir.Document{}, runtime.Output{Select: projection.Select})
	switch projection.Kind {
	case "array":
		return map[string]any{"type": "array", "items": child}
	case "map":
		return map[string]any{"type": "object", "additionalProperties": child}
	default:
		return child
	}
}

func valueSchemaJSON(schema runtime.ValueSchema) map[string]any {
	out := map[string]any{}
	if schema.Type != "" {
		out["type"] = schema.Type
	}
	if schema.Const != nil {
		out["const"] = *schema.Const
	}
	if len(schema.Enum) > 0 {
		out["enum"] = schema.Enum
	}
	if schema.Minimum != nil {
		out["minimum"] = *schema.Minimum
	}
	if schema.Maximum != nil {
		out["maximum"] = *schema.Maximum
	}
	if schema.MinLength != nil {
		out["minLength"] = *schema.MinLength
	}
	if schema.MaxLength != nil {
		out["maxLength"] = *schema.MaxLength
	}
	if schema.MinItems != nil {
		out["minItems"] = *schema.MinItems
	}
	if schema.MaxItems != nil {
		out["maxItems"] = *schema.MaxItems
	}
	if schema.Items != nil {
		out["items"] = valueSchemaJSON(*schema.Items)
	}
	if schema.AdditionalProperties {
		out["additionalProperties"] = true
	}
	return out
}

func concreteSchema(doc ir.Document, ref ir.SchemaRef) (ir.Schema, bool) {
	if ref.Ref != "" {
		return ir.ResolveSchema(doc, ref)
	}
	if ref.Type == "" {
		return ir.Schema{}, false
	}
	return ir.Schema{Type: ref.Type, Items: ref.Items}, true
}

func pointerSegments(pointer string) []string {
	segments, _ := ir.JSONPointerSegments(pointer)
	return segments
}

func set(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func defaultConfirmation(effect, confirmation string) string {
	if confirmation != "" {
		return confirmation
	}
	if effect == "read" {
		return "never"
	}
	if effect == "destructive" {
		return "always"
	}
	return "policy"
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	data, _ := json.Marshal(input)
	var output map[string]any
	_ = json.Unmarshal(data, &output)
	return output
}
