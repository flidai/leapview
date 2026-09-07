package contractprojection

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
)

func canonicalAllowedValues(values []any) ([]CanonicalValue, error) {
	return canonicalAllowedValuesTyped(values, "", true)
}

// canonicalAllowedValuesTyped canonicalizes homogeneous scalar sets. The
// source SemanticModel contract uses set semantics for access grants and
// `in`/`not_in` filters, while preserving ordered range-like values is
// deliberately outside this helper's contract.
func canonicalAllowedValuesTyped(values []any, datatype string, setSemantics bool) ([]CanonicalValue, error) {
	result := make([]CanonicalValue, 0, len(values))
	seen := map[string]struct{}{}
	category := ""
	for _, value := range values {
		canonical, err := canonicalLiteral(datatype, value)
		if err != nil {
			return nil, err
		}
		valueCategory := canonicalValueCategory(canonical)
		if category == "" {
			category = valueCategory
		} else if category != valueCategory {
			return nil, fmt.Errorf("semantic value set mixes %s and %s values", category, valueCategory)
		}
		key := canonicalValueKey(canonical)
		if setSemantics {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
		}
		result = append(result, canonical)
	}
	if setSemantics {
		sort.Slice(result, func(i, j int) bool { return canonicalValueSortKey(result[i]) < canonicalValueSortKey(result[j]) })
	}
	return result, nil
}

func canonicalValueCategory(value CanonicalValue) string {
	switch variant := value.Value.(type) {
	case *projectcontracts.ContractProjectionCanonicalTextValue:
		if variant.Type == "Number" || variant.Type == "Integer" || variant.Type == "Decimal" {
			return "number"
		}
		return "text"
	case *projectcontracts.ContractProjectionCanonicalBooleanValue:
		return "boolean"
	default:
		return "unknown"
	}
}

func canonicalValueSortKey(value CanonicalValue) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return canonicalValueKey(value)
	}
	canonical, err := canonicalizeRFC8785(encoded)
	if err != nil {
		return canonicalValueKey(value)
	}
	return string(canonical)
}

func canonicalLiteral(datatype string, value any) (CanonicalValue, error) {
	typeName := strings.ToLower(datatype)
	semanticType := ""
	switch typeName {
	case "string":
		semanticType = "String"
	case "boolean":
		semanticType = "Boolean"
	case "integer":
		semanticType = "Integer"
	case "decimal":
		semanticType = "Decimal"
	case "date":
		semanticType = "Date"
	case "time":
		semanticType = "Time"
	case "datetime":
		semanticType = "DateTime"
	case "datetimetz":
		semanticType = "Timestamp"
	case "float":
		return CanonicalValue{}, errors.New("approximate Float values are not permitted in contract literals")
	case "opaque":
		return CanonicalValue{}, errors.New("Opaque values have no canonical contract literal representation")
	}
	if semanticType != "" {
		dimensionType := semanticType
		if semanticType == "Timestamp" {
			dimensionType = "DateTimeTz"
		}
		canonical, err := semanticmodel.CoerceSemanticLiteral(value, semanticmodel.MetricDimension{Datatype: semanticmodel.LogicalDataType(dimensionType)})
		if err != nil {
			return CanonicalValue{}, err
		}
		if boolean, ok := canonical.(bool); ok {
			return canonicalBooleanValue(boolean), nil
		}
		text, err := canonicalText(fmt.Sprint(canonical))
		if err != nil {
			return CanonicalValue{}, err
		}
		return canonicalTextValue(semanticType, text), nil
	}
	switch typed := value.(type) {
	case string:
		text, err := canonicalText(typed)
		return canonicalTextValue("String", text), err
	case bool:
		return canonicalBooleanValue(typed), nil
	case json.Number:
		canonical, err := semanticmodel.CoerceSemanticLiteral(typed, semanticmodel.MetricDimension{Datatype: semanticmodel.DataTypeDecimal})
		if err != nil {
			return CanonicalValue{}, err
		}
		return canonicalTextValue("Number", fmt.Sprint(canonical)), nil
	default:
		return CanonicalValue{}, fmt.Errorf("unsupported contract literal %T", value)
	}
}

func canonicalTextValue(typeName, value string) CanonicalValue {
	return CanonicalValue{Value: &projectcontracts.ContractProjectionCanonicalTextValue{Type: typeName, Value: value}}
}

func canonicalBooleanValue(value bool) CanonicalValue {
	return CanonicalValue{Value: &projectcontracts.ContractProjectionCanonicalBooleanValue{Type: "Boolean", Value: value}}
}

func canonicalValueKey(value CanonicalValue) string {
	switch variant := value.Value.(type) {
	case *projectcontracts.ContractProjectionCanonicalTextValue:
		return variant.Type + "\x00" + variant.Value
	case *projectcontracts.ContractProjectionCanonicalBooleanValue:
		return variant.Type + "\x00" + fmt.Sprint(variant.Value)
	default:
		return fmt.Sprintf("%T", value.Value)
	}
}
