package duckdbsql

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

func checkKeys(obj map[string]json.RawMessage, allowed ...string) error {
	set := map[string]struct{}{}
	for _, k := range allowed {
		set[k] = struct{}{}
	}
	for k := range obj {
		if _, ok := set[k]; !ok {
			return compatibilityError("unknown DuckDB SQL AST field " + strconv.Quote(k))
		}
	}
	return nil
}

func checkGeneratedSchema(family, discriminator string, obj map[string]json.RawMessage) error {
	var schemas map[string]serializedNodeSchema
	switch family {
	case "statement":
		schemas = generatedStatementSchemas
	case "relation":
		schemas = generatedRelationSchemas
	case "expression":
		schemas = generatedExpressionSchemas
	case "modifier":
		schemas = generatedModifierSchemas
	case "supporting":
		schemas = generatedSupportingSchemas
	default:
		return compatibilityError("unknown generated schema family " + family)
	}
	schema, ok := schemas[discriminator]
	if !ok {
		return compatibilityError("unknown DuckDB SQL AST discriminator " + strconv.Quote(discriminator))
	}
	if err := checkKeys(obj, schema.AllowedFields...); err != nil {
		return err
	}
	for _, key := range schema.RequiredFields {
		if _, ok := obj[key]; !ok {
			return malformedError("DuckDB SQL AST is missing required field "+strconv.Quote(key), nil)
		}
	}
	return nil
}
func rawArray(raw json.RawMessage) ([]json.RawMessage, error) {
	if len(raw) == 0 || isNull(raw) {
		return nil, fmt.Errorf("array expected")
	}
	var a []json.RawMessage
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	return a, nil
}
func requiredString(obj map[string]json.RawMessage, key string) (string, error) {
	v, ok := obj[key]
	if !ok {
		return "", fmt.Errorf("missing %s", key)
	}
	return stringValue(v)
}
func optionalString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || isNull(raw) {
		return "", nil
	}
	value, err := stringValue(raw)
	if err != nil {
		return "", malformedError("optional string field must be a string", err)
	}
	return value, nil
}
func optionalStringValue(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || isNull(raw) {
		return "", false
	}
	v, e := stringValue(raw)
	return v, e == nil
}
func stringValue(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", err
	}
	return s, nil
}
func stringsValue(raw json.RawMessage) ([]string, error) {
	a, e := rawArray(raw)
	if e != nil {
		return nil, e
	}
	out := make([]string, 0, len(a))
	for _, v := range a {
		s, e := stringValue(v)
		if e != nil {
			return nil, e
		}
		out = append(out, s)
	}
	return out, nil
}

func (d *decoder) stringsValue(raw json.RawMessage) ([]string, error) {
	a, err := rawArray(raw)
	if err != nil {
		return nil, err
	}
	if len(a) > d.limits.MaxArrayItems {
		return nil, limitError("string array item count exceeds parser limit")
	}
	out := make([]string, 0, len(a))
	for _, v := range a {
		s, err := stringValue(v)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}
func boolValue(raw json.RawMessage) (bool, error) {
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, err
	}
	return b, nil
}
func optionalBool(raw json.RawMessage) (bool, error) {
	if len(raw) == 0 || isNull(raw) {
		return false, nil
	}
	value, err := boolValue(raw)
	if err != nil {
		return false, malformedError("optional boolean field must be a boolean", err)
	}
	return value, nil
}
func optionalInt64(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 || isNull(raw) {
		return 0, nil
	}
	value, err := int64Value(raw)
	if err != nil {
		return 0, malformedError("optional integer field must be an integer", err)
	}
	return value, nil
}
func intValue(raw json.RawMessage) (int, error) {
	i, e := int64Value(raw)
	if e != nil {
		return 0, e
	}
	if strconv.IntSize == 32 && (i > int64(^uint32(0)>>1) || i < -int64(^uint32(0)>>1)-1) {
		return 0, fmt.Errorf("integer overflows platform int")
	}
	if strconv.IntSize == 64 && (i > int64(^uint64(0)>>1) || i < -int64(^uint64(0)>>1)-1) {
		return 0, fmt.Errorf("integer overflows platform int")
	}
	return int(i), nil
}
func int64Value(raw json.RawMessage) (int64, error) {
	var n json.Number
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&n); err != nil {
		return 0, err
	}
	i, e := strconv.ParseInt(n.String(), 10, 64)
	return i, e
}
func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

func stringsContains(s, needle string) bool {
	for i := 0; i+len(needle) <= len(s); i++ {
		if s[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
func validAggregateHandling(v string) bool {
	return generatedEnumContains("AggregateHandling", v)
}
func validSetOperation(v string) bool {
	// NONE is a valid DuckDB enum value but is not a serializable query
	// set-operation node, so it remains intentionally excluded at this layer.
	return v != "NONE" && generatedEnumContains("SetOperationType", v)
}

func validCTEMaterialized(value string) bool {
	return generatedEnumContains("CTEMaterialize", value)
}

func validOrderType(value string) bool {
	return value != "INVALID" && generatedEnumContains("OrderType", value)
}

func validOrderNullType(value string) bool {
	return value != "INVALID" && generatedEnumContains("OrderByNullType", value)
}

func validWindowBoundary(value string) bool {
	return value == "" || (value != "INVALID" && generatedEnumContains("WindowBoundary", value))
}

func validWindowExclude(value string) bool {
	return value == "" || generatedEnumContains("WindowExcludeMode", value)
}

var supportedExpressionTypesByClass = map[string]map[string]struct{}{
	"CONSTANT":             {"VALUE_CONSTANT": {}, "VALUE_NULL": {}, "VALUE_DEFAULT": {}},
	"COLUMN_REF":           {"COLUMN_REF": {}},
	"STAR":                 {"STAR": {}, "TABLE_STAR": {}},
	"FUNCTION":             {"FUNCTION": {}, "AGGREGATE": {}, "GROUPING_FUNCTION": {}, "FUNCTION_REF": {}},
	"OPERATOR":             {"OPERATOR_NOT": {}, "OPERATOR_IS_NULL": {}, "OPERATOR_IS_NOT_NULL": {}, "OPERATOR_UNPACK": {}, "OPERATOR_NULLIF": {}, "OPERATOR_COALESCE": {}, "ARRAY_EXTRACT": {}, "ARRAY_SLICE": {}, "STRUCT_EXTRACT": {}, "ARRAY_CONSTRUCTOR": {}, "ARROW": {}, "OPERATOR_TRY": {}, "OPERATOR_CAST": {}},
	"CONJUNCTION":          {"CONJUNCTION_AND": {}, "CONJUNCTION_OR": {}},
	"COMPARISON":           {"COMPARE_EQUAL": {}, "COMPARE_NOTEQUAL": {}, "COMPARE_LESSTHAN": {}, "COMPARE_GREATERTHAN": {}, "COMPARE_LESSTHANOREQUALTO": {}, "COMPARE_GREATERTHANOREQUALTO": {}, "COMPARE_IN": {}, "COMPARE_NOT_IN": {}, "COMPARE_DISTINCT_FROM": {}, "COMPARE_BETWEEN": {}, "COMPARE_NOT_BETWEEN": {}, "COMPARE_NOT_DISTINCT_FROM": {}},
	"CAST":                 {"OPERATOR_CAST": {}, "CAST": {}},
	"CASE":                 {"CASE_EXPR": {}},
	"WINDOW":               {"WINDOW_AGGREGATE": {}, "WINDOW_RANK": {}, "WINDOW_RANK_DENSE": {}, "WINDOW_NTILE": {}, "WINDOW_PERCENT_RANK": {}, "WINDOW_CUME_DIST": {}, "WINDOW_ROW_NUMBER": {}, "WINDOW_FIRST_VALUE": {}, "WINDOW_LAST_VALUE": {}, "WINDOW_LEAD": {}, "WINDOW_LAG": {}, "WINDOW_NTH_VALUE": {}, "WINDOW_FILL": {}},
	"SUBQUERY":             {"SUBQUERY": {}},
	"BETWEEN":              {"COMPARE_BETWEEN": {}, "COMPARE_NOT_BETWEEN": {}},
	"COLLATE":              {"COLLATE": {}},
	"DEFAULT":              {"VALUE_DEFAULT": {}},
	"LAMBDA":               {"LAMBDA": {}},
	"LAMBDA_REF":           {"LAMBDA_REF": {}},
	"PARAMETER":            {"VALUE_PARAMETER": {}},
	"POSITIONAL_REFERENCE": {"POSITIONAL_REFERENCE": {}},
	"TYPE":                 {"TYPE": {}},
}

func validExpressionType(class, typ string) bool {
	if !generatedEnumContains("ExpressionClass", class) || !generatedEnumContains("ExpressionType", typ) {
		return false
	}
	allowed, ok := supportedExpressionTypesByClass[class]
	if !ok {
		return false
	}
	_, ok = allowed[typ]
	return ok
}
func knownLogicalType(v string) bool {
	if generatedEnumContains("LogicalTypeId", v) {
		return true
	}
	// JSON and extension-defined logical IDs are not core LogicalTypeId enum
	// members. The pinned runtime catalog is the source of truth for those
	// extension types (the parser explicitly loads json before serialization).
	for _, typ := range generatedInventory.Types {
		if typ.LogicalType == v || typ.TypeName == v {
			return true
		}
	}
	return false
}
