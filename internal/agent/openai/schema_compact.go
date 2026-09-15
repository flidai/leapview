package openai

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// OpenAI receives tool schemas on every model request. Some generated schemas
// repeat large discriminated branches many times; local definitions preserve
// their validation semantics while avoiding that wire-size multiplication.
const minCompactSchemaBytes = 64

func compactJSONSchema(raw json.RawMessage) json.RawMessage {
	var root map[string]any
	if err := decodeSchemaJSON(raw, &root); err != nil || root == nil {
		return append(json.RawMessage(nil), raw...)
	}
	if hasReferenceScopeMarker(root) {
		return append(json.RawMessage(nil), raw...)
	}
	// Existing definitions may have provider-specific references. Leave those
	// documents untouched rather than risk changing their resolution scope.
	if _, exists := root["$defs"]; exists {
		return append(json.RawMessage(nil), raw...)
	}

	counts := make(map[string]int)
	countSchemaObjects(root, counts)
	candidates := make(map[string]string)
	keys := make([]string, 0, len(counts))
	for key, count := range counts {
		if count >= 2 && len(key) >= minCompactSchemaBytes {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return append(json.RawMessage(nil), raw...)
	}
	for index, key := range keys {
		// Hashes make names stable across map traversal and easy to diagnose.
		digest := sha256.Sum256([]byte(key))
		candidates[key] = fmt.Sprintf("compact_%d_%s", index, hex.EncodeToString(digest[:4]))
	}

	defs := make(map[string]any, len(candidates))
	for _, key := range keys {
		var value any
		if err := decodeSchemaJSON([]byte(key), &value); err != nil {
			return append(json.RawMessage(nil), raw...)
		}
		defs[candidates[key]] = compactSchemaValue(value, candidates, key)
	}
	compacted := compactSchemaValue(root, candidates, "")
	compactedRoot, ok := compacted.(map[string]any)
	if !ok {
		return append(json.RawMessage(nil), raw...)
	}
	compactedRoot["$defs"] = defs
	out, err := json.Marshal(compactedRoot)
	if err != nil {
		return append(json.RawMessage(nil), raw...)
	}
	if len(out) >= len(raw) {
		return append(json.RawMessage(nil), raw...)
	}
	return out
}

func decodeSchemaJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(target)
}

func countSchemaObjects(value any, counts map[string]int) {
	countSchemaNode(value, counts)
}

func countSchemaNode(value any, counts map[string]int) {
	object, ok := value.(map[string]any)
	if !ok {
		return
	}
	encoded, err := json.Marshal(object)
	if err == nil {
		counts[string(encoded)]++
	}
	for key, child := range object {
		walkSchemaChild(key, child, func(nested any) { countSchemaNode(nested, counts) })
	}
}

func compactSchemaValue(value any, candidates map[string]string, definitionKey string) any {
	object, ok := value.(map[string]any)
	if !ok {
		return value
	}
	encoded, err := json.Marshal(object)
	if err == nil {
		if name, found := candidates[string(encoded)]; found && string(encoded) != definitionKey {
			return map[string]any{"$ref": "#/$defs/" + name}
		}
	}
	out := make(map[string]any, len(object))
	for key, child := range object {
		out[key] = compactSchemaChild(key, child, candidates, definitionKey)
	}
	return out
}

var schemaChildKeywords = map[string]struct{}{
	"properties": {}, "patternProperties": {}, "$defs": {}, "definitions": {},
	"dependentSchemas": {}, "items": {}, "additionalItems": {}, "additionalProperties": {},
	"contains": {}, "propertyNames": {}, "unevaluatedItems": {}, "unevaluatedProperties": {},
	"not": {}, "if": {}, "then": {}, "else": {}, "prefixItems": {},
	"oneOf": {}, "anyOf": {}, "allOf": {},
}

func compactSchemaChild(key string, value any, candidates map[string]string, definitionKey string) any {
	if _, ok := schemaChildKeywords[key]; !ok {
		return value
	}
	if object, ok := value.(map[string]any); ok {
		if key == "properties" || key == "patternProperties" || key == "$defs" || key == "definitions" || key == "dependentSchemas" {
			out := make(map[string]any, len(object))
			for name, child := range object {
				out[name] = compactSchemaValue(child, candidates, definitionKey)
			}
			return out
		}
		return compactSchemaValue(object, candidates, definitionKey)
	}
	if array, ok := value.([]any); ok {
		out := make([]any, len(array))
		for index, child := range array {
			out[index] = compactSchemaValue(child, candidates, definitionKey)
		}
		return out
	}
	return value
}

func walkSchemaChild(key string, value any, visit func(any)) {
	if _, ok := schemaChildKeywords[key]; !ok {
		return
	}
	switch current := value.(type) {
	case map[string]any:
		if key == "properties" || key == "patternProperties" || key == "$defs" || key == "definitions" || key == "dependentSchemas" {
			for _, child := range current {
				visit(child)
			}
			return
		}
		visit(current)
	case []any:
		for _, child := range current {
			visit(child)
		}
	}
}

func hasReferenceScopeMarker(value any) bool {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			switch key {
			case "$id", "$anchor", "$ref", "$dynamicRef", "$dynamicAnchor":
				return true
			}
			if hasReferenceScopeMarker(child) {
				return true
			}
		}
	case []any:
		for _, child := range current {
			if hasReferenceScopeMarker(child) {
				return true
			}
		}
	}
	return false
}
