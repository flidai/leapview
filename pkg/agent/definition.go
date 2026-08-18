package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type Definition struct {
	Name        string
	Description string

	SystemPrompt string
	Model        Model

	Tools             []ToolDefinition
	Limits            Limits
	ToolOutput        ToolOutputConfig
	Compaction        CompactionConfig
	InitialTranscript []Message

	Events      EventSink
	Clock       Clock
	IDGenerator IDGenerator
}

func (d Definition) withDefaults() Definition {
	d.Limits = defaultLimits(d.Limits)
	d.ToolOutput = defaultToolOutputConfig(d.ToolOutput)
	d.Compaction = defaultCompaction(d.Compaction)
	if d.Events == nil {
		d.Events = noopEventSink{}
	}
	if d.Clock == nil {
		d.Clock = realClock{}
	}
	if d.IDGenerator == nil {
		d.IDGenerator = &sequenceIDGenerator{}
	}
	d.InitialTranscript = cloneMessages(d.InitialTranscript)
	return d
}

func compileTools(tools []ToolDefinition) (map[string]*compiledTool, []ToolSpec, error) {
	registry := make(map[string]*compiledTool, len(tools))
	specs := make([]ToolSpec, 0, len(tools))
	for _, tool := range tools {
		if strings.TrimSpace(tool.Name) == "" {
			return nil, nil, NewError(ErrorCodeInvalidArgument, "tool name is required", nil)
		}
		if _, exists := registry[tool.Name]; exists {
			return nil, nil, NewError(ErrorCodeInvalidArgument, fmt.Sprintf("duplicate tool %q", tool.Name), nil)
		}
		if tool.Handler == nil {
			return nil, nil, NewError(ErrorCodeInvalidArgument, fmt.Sprintf("tool %q handler is required", tool.Name), nil)
		}
		schemaRaw := tool.InputSchema
		if len(schemaRaw) == 0 {
			schemaRaw = json.RawMessage(`{"type":"object"}`)
		}
		if !json.Valid(schemaRaw) {
			return nil, nil, NewError(ErrorCodeInvalidArgument, fmt.Sprintf("tool %q schema is invalid JSON", tool.Name), nil)
		}
		if err := validatePortableToolSchema(tool.Name, schemaRaw); err != nil {
			return nil, nil, err
		}
		schema, err := compileSchema(tool.Name, schemaRaw)
		if err != nil {
			return nil, nil, NewError(ErrorCodeInvalidArgument, fmt.Sprintf("tool %q schema did not compile", tool.Name), err)
		}
		tool.InputSchema = append(json.RawMessage(nil), schemaRaw...)
		var outputSchema *jsonschema.Schema
		if len(tool.OutputSchema) > 0 {
			if !json.Valid(tool.OutputSchema) {
				return nil, nil, NewError(ErrorCodeInvalidArgument, fmt.Sprintf("tool %q output schema is invalid JSON", tool.Name), nil)
			}
			outputSchema, err = compileSchema(tool.Name+"-output", tool.OutputSchema)
			if err != nil {
				return nil, nil, NewError(ErrorCodeInvalidArgument, fmt.Sprintf("tool %q output schema did not compile", tool.Name), err)
			}
			tool.OutputSchema = append(json.RawMessage(nil), tool.OutputSchema...)
		}
		registry[tool.Name] = &compiledTool{def: tool, schema: schema, outputSchema: outputSchema}
		specs = append(specs, ToolSpec{Name: tool.Name, Description: tool.Description, InputSchema: append(json.RawMessage(nil), schemaRaw...)})
	}
	return registry, specs, nil
}

func compileSchema(name string, raw json.RawMessage) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	loc := "tool://" + name + ".schema.json"
	if err := compiler.AddResource(loc, doc); err != nil {
		return nil, err
	}
	return compiler.Compile(loc)
}

func validatePortableToolSchema(name string, raw json.RawMessage) error {
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return NewError(ErrorCodeInvalidArgument, fmt.Sprintf("tool %q schema is invalid JSON", name), err)
	}
	if schema["type"] != "object" {
		return NewError(ErrorCodeInvalidArgument, fmt.Sprintf("tool %q schema root type must be object for provider portability", name), nil)
	}
	if key, path, ok := findNonPortableToolSchemaKeyword(schema, "$"); ok {
		return NewError(ErrorCodeInvalidArgument, fmt.Sprintf("tool %q schema uses %q at %s; use the portable tool schema subset", name, key, path), nil)
	}
	return nil
}

func findNonPortableToolSchemaKeyword(value any, path string) (string, string, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		if items, ok := value.([]any); ok {
			for i, item := range items {
				if key, foundPath, found := findNonPortableToolSchemaKeyword(item, fmt.Sprintf("%s[%d]", path, i)); found {
					return key, foundPath, true
				}
			}
		}
		return "", "", false
	}
	for _, key := range sortedSchemaKeys(object) {
		child := object[key]
		if !portableToolSchemaKeywords[key] {
			return key, path + "." + key, true
		}
		if key == "oneOf" || key == "allOf" {
			branches, ok := child.([]any)
			if !ok || len(branches) == 0 {
				return key, path + "." + key, true
			}
			for index, branch := range branches {
				if _, ok := branch.(map[string]any); !ok {
					return key, fmt.Sprintf("%s.%s[%d]", path, key, index), true
				}
				if foundKey, foundPath, found := findNonPortableToolSchemaKeyword(branch, fmt.Sprintf("%s.%s[%d]", path, key, index)); found {
					return foundKey, foundPath, true
				}
			}
			continue
		}
		if key == "propertyNames" {
			if _, ok := child.(map[string]any); !ok {
				return key, path + "." + key, true
			}
		}
		if key == "pattern" {
			if _, ok := child.(string); !ok {
				return key, path + "." + key, true
			}
			continue
		}
		if key == "properties" {
			properties, _ := child.(map[string]any)
			for _, name := range sortedSchemaKeys(properties) {
				propertySchema := properties[name]
				if foundKey, foundPath, found := findNonPortableToolSchemaKeyword(propertySchema, path+".properties."+name); found {
					return foundKey, foundPath, true
				}
			}
			continue
		}
		if foundKey, foundPath, found := findNonPortableToolSchemaKeyword(child, path+"."+key); found {
			return foundKey, foundPath, true
		}
	}
	return "", "", false
}

func sortedSchemaKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

var portableToolSchemaKeywords = map[string]bool{
	"additionalProperties": true,
	"description":          true,
	"enum":                 true,
	"items":                true,
	"maximum":              true,
	"maxLength":            true,
	"minimum":              true,
	"minLength":            true,
	"oneOf":                true,
	"allOf":                true,
	"properties":           true,
	"propertyNames":        true,
	"pattern":              true,
	"required":             true,
	"type":                 true,
}
