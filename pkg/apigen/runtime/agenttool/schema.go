package agenttool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"unicode/utf8"
)

func validatePortableSchema(value any, encoded json.RawMessage) error {
	if len(bytes.TrimSpace(encoded)) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var schema map[string]any
	if err := decoder.Decode(&schema); err != nil {
		return fmt.Errorf("decode contract schema: %w", err)
	}
	return validateSchemaValue(value, schema, "$")
}

func validateSchemaValue(value any, schema map[string]any, path string) error {
	if enum, ok := schema["enum"].([]any); ok && !schemaEnumContains(enum, value) {
		return fmt.Errorf("%s is not an allowed enum value", path)
	}
	typeName, _ := schema["type"].(string)
	switch typeName {
	case "", "null":
		if typeName == "null" && value != nil {
			return fmt.Errorf("%s must be null", path)
		}
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		properties, _ := schema["properties"].(map[string]any)
		for _, name := range schemaStringArray(schema["required"]) {
			if _, exists := object[name]; !exists {
				return fmt.Errorf("%s.%s is required", path, name)
			}
		}
		for name, child := range object {
			if rawProperty, exists := properties[name]; exists {
				property, ok := rawProperty.(map[string]any)
				if !ok {
					return fmt.Errorf("%s.%s has an invalid contract schema", path, name)
				}
				if err := validateSchemaValue(child, property, path+"."+name); err != nil {
					return err
				}
				continue
			}
			switch additional := schema["additionalProperties"].(type) {
			case bool:
				if !additional {
					return fmt.Errorf("%s.%s is not allowed", path, name)
				}
			case map[string]any:
				if err := validateSchemaValue(child, additional, path+"."+name); err != nil {
					return err
				}
			}
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array", path)
		}
		if itemSchema, ok := schema["items"].(map[string]any); ok {
			for index, item := range items {
				if err := validateSchemaValue(item, itemSchema, fmt.Sprintf("%s[%d]", path, index)); err != nil {
					return err
				}
			}
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", path)
		}
		length := utf8.RuneCountInString(text)
		if minimum, ok := schemaInteger(schema["minLength"]); ok && length < minimum {
			return fmt.Errorf("%s must contain at least %d characters", path, minimum)
		}
		if maximum, ok := schemaInteger(schema["maxLength"]); ok && length > maximum {
			return fmt.Errorf("%s must contain at most %d characters", path, maximum)
		}
	case "integer":
		number, integer, ok := schemaNumber(value)
		if !ok || !integer {
			return fmt.Errorf("%s must be an integer", path)
		}
		if err := validateNumberBounds(path, number, schema); err != nil {
			return err
		}
	case "number":
		number, _, ok := schemaNumber(value)
		if !ok {
			return fmt.Errorf("%s must be a number", path)
		}
		if err := validateNumberBounds(path, number, schema); err != nil {
			return err
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", path)
		}
	default:
		return fmt.Errorf("%s uses unsupported schema type %q", path, typeName)
	}
	return nil
}

func validateNumberBounds(path string, value float64, schema map[string]any) error {
	if minimum, _, ok := schemaNumber(schema["minimum"]); ok && value < minimum {
		return fmt.Errorf("%s must be at least %v", path, minimum)
	}
	if maximum, _, ok := schemaNumber(schema["maximum"]); ok && value > maximum {
		return fmt.Errorf("%s must be at most %v", path, maximum)
	}
	return nil
}

func schemaEnumContains(enum []any, value any) bool {
	for _, allowed := range enum {
		if reflect.DeepEqual(allowed, value) {
			return true
		}
		if left, _, leftOK := schemaNumber(allowed); leftOK {
			if right, _, rightOK := schemaNumber(value); rightOK && left == right {
				return true
			}
		}
	}
	return false
}

func schemaStringArray(value any) []string {
	raw, _ := value.([]any)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func schemaInteger(value any) (int, bool) {
	number, integer, ok := schemaNumber(value)
	return int(number), ok && integer
}

func schemaNumber(value any) (float64, bool, bool) {
	var number float64
	switch value := value.(type) {
	case json.Number:
		parsed, err := value.Float64()
		if err != nil {
			return 0, false, false
		}
		number = parsed
	case float64:
		number = value
	case float32:
		number = float64(value)
	case int:
		number = float64(value)
	case int32:
		number = float64(value)
	case int64:
		number = float64(value)
	case uint:
		number = float64(value)
	case uint32:
		number = float64(value)
	case uint64:
		number = float64(value)
	default:
		return 0, false, false
	}
	return number, math.Trunc(number) == number, true
}
