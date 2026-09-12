package openai

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	agentcontracts "github.com/flidai/leapview/internal/agent/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestCompactJSONSchemaShrinksQueryVisualSchema(t *testing.T) {
	original := json.RawMessage(agentcontracts.QueryVisualInputSchemaJSON)
	compacted := compactJSONSchema(original)
	t.Logf("query_visual schema bytes: %d -> %d", len(original), len(compacted))
	if len(compacted) >= len(original)/2 {
		t.Fatalf("compacted schema is %d bytes, original is %d", len(compacted), len(original))
	}
	var document map[string]any
	if err := json.Unmarshal(compacted, &document); err != nil {
		t.Fatalf("decode compacted schema: %v", err)
	}
	defs, ok := document["$defs"].(map[string]any)
	if !ok || len(defs) == 0 {
		t.Fatalf("compacted schema has no local definitions")
	}
	if !bytes.Contains(compacted, []byte(`"$ref":"#/$defs/`)) {
		t.Fatalf("compacted schema has no local references")
	}
	var originalValue, compactedValue any
	if err := json.Unmarshal(original, &originalValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(compacted, &compactedValue); err != nil {
		t.Fatal(err)
	}
	if expanded := expandLocalRefs(t, compactedValue); !reflect.DeepEqual(originalValue, expanded) {
		t.Fatal("expanding local references did not reproduce the original schema")
	}
	compileSchemaForTest(t, original)
	compileSchemaForTest(t, compacted)
}

func TestCompactJSONSchemaPreservesValidation(t *testing.T) {
	original := json.RawMessage(`{"type":"object","properties":{"left":{"type":"object","properties":{"id":{"type":"string","minLength":3}},"required":["id"],"additionalProperties":false},"right":{"type":"object","properties":{"id":{"type":"string","minLength":3}},"required":["id"],"additionalProperties":false}},"required":["left","right"],"additionalProperties":false}`)
	compacted := compactJSONSchema(original)
	valid := json.RawMessage(`{"left":{"id":"abc"},"right":{"id":"xyz"}}`)
	invalid := json.RawMessage(`{"left":{"id":"a"},"right":{"id":"xyz"}}`)
	originalSchema := compileSchemaForTest(t, original)
	compactedSchema := compileSchemaForTest(t, compacted)
	var validInstance, invalidInstance any
	if err := json.Unmarshal(valid, &validInstance); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(invalid, &invalidInstance); err != nil {
		t.Fatal(err)
	}
	if err := originalSchema.Validate(validInstance); err != nil {
		t.Fatalf("original rejects valid instance: %v", err)
	}
	if err := compactedSchema.Validate(validInstance); err != nil {
		t.Fatalf("compacted rejects valid instance: %v", err)
	}
	if originalSchema.Validate(invalidInstance) == nil || compactedSchema.Validate(invalidInstance) == nil {
		t.Fatal("both schemas must reject the invalid instance")
	}
}

func compileSchemaForTest(t *testing.T, raw json.RawMessage) *jsonschema.Schema {
	t.Helper()
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("tool://test.schema.json", document); err != nil {
		t.Fatalf("add schema resource: %v", err)
	}
	schema, err := compiler.Compile("tool://test.schema.json")
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return schema
}

func expandLocalRefs(t *testing.T, value any) any {
	t.Helper()
	root, ok := value.(map[string]any)
	if !ok {
		t.Fatal("schema root is not an object")
	}
	defs, _ := root["$defs"].(map[string]any)
	delete(root, "$defs")
	return expandLocalRefNode(t, root, defs, map[string]bool{})
}

func expandLocalRefNode(t *testing.T, value any, defs map[string]any, stack map[string]bool) any {
	t.Helper()
	switch current := value.(type) {
	case map[string]any:
		if ref, ok := current["$ref"].(string); ok && strings.HasPrefix(ref, "#/$defs/") {
			name := strings.TrimPrefix(ref, "#/$defs/")
			if stack[name] {
				t.Fatalf("cyclic local reference %q", name)
			}
			definition, ok := defs[name]
			if !ok {
				t.Fatalf("missing local definition %q", name)
			}
			nextStack := make(map[string]bool, len(stack)+1)
			for key, active := range stack {
				nextStack[key] = active
			}
			nextStack[name] = true
			return expandLocalRefNode(t, definition, defs, nextStack)
		}
		out := make(map[string]any, len(current))
		for key, child := range current {
			out[key] = expandLocalRefNode(t, child, defs, stack)
		}
		return out
	case []any:
		out := make([]any, len(current))
		for index, child := range current {
			out[index] = expandLocalRefNode(t, child, defs, stack)
		}
		return out
	default:
		return value
	}
}
