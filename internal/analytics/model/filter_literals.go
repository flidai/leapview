package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/analytics/semanticnumeric"
)

// CoerceSemanticLiteral validates and normalizes a canonical filter literal
// against a logical field type. Both model validation and planner adapters use
// this boundary so invalid authored values fail before SQL execution.
func CoerceSemanticLiteral(value any, dimension MetricDimension) (any, error) {
	typeName := strings.ToLower(strings.TrimSpace(string(dimension.Datatype)))
	if typeName == "" {
		typeName = strings.ToLower(strings.TrimSpace(dimension.Type))
	}
	switch typeName {
	case "integer", "int", "bigint":
		return coerceInteger(value)
	case "decimal":
		return coerceDecimal(value)
	case "float", "number", "numeric", "double", "real":
		return coerceFloat(value)
	case "boolean", "bool":
		if boolean, ok := value.(bool); ok {
			return boolean, nil
		}
		return nil, fmt.Errorf("value %v is not boolean", value)
	case "string", "text", "varchar", "char":
		if _, ok := value.(string); !ok {
			return nil, fmt.Errorf("value %v is not a string", value)
		}
		return value, nil
	case "date":
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("value %v is not a date", value)
		}
		if _, err := time.Parse("2006-01-02", text); err != nil {
			return nil, fmt.Errorf("value %q is not a date", text)
		}
		return text, nil
	case "time":
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("value %v is not a time", value)
		}
		if _, err := time.Parse("15:04:05.999999999", text); err != nil {
			return nil, fmt.Errorf("value %q is not a time", text)
		}
		return text, nil
	case "datetime":
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("value %v is not a datetime", value)
		}
		if _, err := time.Parse("2006-01-02T15:04:05.999999999", text); err != nil {
			return nil, fmt.Errorf("value %q is not a timezone-free datetime", text)
		}
		return text, nil
	case "datetimetz", "timestamp":
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("value %v is not a datetime", value)
		}
		if _, err := time.Parse(time.RFC3339Nano, text); err != nil {
			return nil, fmt.Errorf("value %q is not an RFC3339 datetime", text)
		}
		return text, nil
	case "opaque":
		return nil, fmt.Errorf("opaque fields cannot be compared")
	default:
		return value, nil
	}
}

func coerceDecimal(value any) (json.Number, error) {
	switch value.(type) {
	case float32, float64:
		return "", fmt.Errorf("value %v is binary floating point; Decimal literals must use a precision-safe string or integer", value)
	case string, json.Number,
		int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		var token string
		var lexical bool
		switch typed := value.(type) {
		case string:
			token, lexical = typed, true
		case json.Number:
			token, lexical = string(typed), true
		}
		if lexical {
			if err := semanticnumeric.ValidateCanonicalFixedPoint(token); err != nil {
				return "", fmt.Errorf("value %v is not decimal: %w", value, err)
			}
		}
		number, err := semanticnumeric.FromValue(value)
		if err != nil {
			return "", fmt.Errorf("value %v is not decimal: %w", value, err)
		}
		switch normalized := number.Value().(type) {
		case int64:
			return json.Number(fmt.Sprint(normalized)), nil
		case string:
			return json.Number(normalized), nil
		default:
			return "", fmt.Errorf("value %v is not an exact Decimal", value)
		}
	default:
		return "", fmt.Errorf("value %v is not decimal", value)
	}
}

func coerceInteger(value any) (int64, error) {
	switch number := value.(type) {
	case int:
		return int64(number), nil
	case int8:
		return int64(number), nil
	case int16:
		return int64(number), nil
	case int32:
		return int64(number), nil
	case int64:
		return number, nil
	case uint8:
		return int64(number), nil
	case uint16:
		return int64(number), nil
	case uint32:
		return int64(number), nil
	case uint:
		if uint64(number) > uint64(^uint64(0)>>1) {
			return 0, fmt.Errorf("value %v is outside integer range", value)
		}
		return int64(number), nil
	case uint64:
		if number > uint64(^uint64(0)>>1) {
			return 0, fmt.Errorf("value %v is outside integer range", value)
		}
		return int64(number), nil
	case float64:
		if number != float64(int64(number)) {
			return 0, fmt.Errorf("value %v is not an integer", value)
		}
		return int64(number), nil
	case float32:
		converted := float64(number)
		if converted != float64(int64(converted)) {
			return 0, fmt.Errorf("value %v is not an integer", value)
		}
		return int64(converted), nil
	default:
		return 0, fmt.Errorf("value %v is not an integer", value)
	}
}

func coerceFloat(value any) (float64, error) {
	switch number := value.(type) {
	case float64:
		return number, nil
	case float32:
		return float64(number), nil
	case int8:
		return float64(number), nil
	case int16:
		return float64(number), nil
	case int32:
		return float64(number), nil
	case int:
		return float64(number), nil
	case int64:
		return float64(number), nil
	case uint:
		return float64(number), nil
	case uint8:
		return float64(number), nil
	case uint16:
		return float64(number), nil
	case uint32:
		return float64(number), nil
	case uint64:
		return float64(number), nil
	default:
		return 0, fmt.Errorf("value %v is not numeric", value)
	}
}
