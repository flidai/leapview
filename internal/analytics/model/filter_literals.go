package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/semanticvalue"
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
		return canonicalSemanticValue(semanticvalue.TypeInteger, value)
	case "decimal":
		return canonicalSemanticValue(semanticvalue.TypeDecimal, value)
	case "float", "number", "numeric", "double", "real":
		return coerceFloat(value)
	case "boolean", "bool":
		return canonicalSemanticValue(semanticvalue.TypeBoolean, value)
	case "string", "text", "varchar", "char":
		return canonicalSemanticValue(semanticvalue.TypeString, value)
	case "date":
		return canonicalSemanticValue(semanticvalue.TypeDate, value)
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
		return canonicalSemanticValue(semanticvalue.TypeTimestamp, value)
	case "opaque":
		return nil, fmt.Errorf("opaque fields cannot be compared")
	default:
		return value, nil
	}
}

func canonicalSemanticValue(typeName semanticvalue.Type, value any) (any, error) {
	canonical, err := semanticvalue.Canonicalize(typeName, value)
	if err != nil {
		switch typeName {
		case semanticvalue.TypeString:
			return nil, fmt.Errorf("value %v is not a string: %w", value, err)
		case semanticvalue.TypeBoolean:
			return nil, fmt.Errorf("value %v is not boolean: %w", value, err)
		case semanticvalue.TypeInteger:
			return nil, fmt.Errorf("value %v is not an integer: %w", value, err)
		case semanticvalue.TypeDecimal:
			return nil, fmt.Errorf("value %v is not decimal: %w", value, err)
		case semanticvalue.TypeDate:
			return nil, fmt.Errorf("value %v is not a date: %w", value, err)
		case semanticvalue.TypeTimestamp:
			return nil, fmt.Errorf("value %v is not an RFC3339 datetime: %w", value, err)
		default:
			return nil, err
		}
	}
	return canonical.Native(), nil
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
	case json.Number:
		parsed, err := number.Float64()
		if err != nil {
			return 0, fmt.Errorf("value %v is not numeric", value)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("value %v is not numeric", value)
	}
}
