package duckdbsql

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

type decoder struct {
	source string
	limits Limits
	nodes  int
}

func decodeDocument(input []byte, source string, limits Limits) (Query, error) {
	if len(input) > limits.MaxJSONBytes {
		return Query{}, limitError(fmt.Sprintf("serialized SQL exceeds %d bytes", limits.MaxJSONBytes))
	}
	if err := scanJSONLimits(input, limits); err != nil {
		return Query{}, err
	}
	d := decoder{source: source, limits: limits}
	root, err := d.object(input)
	if err != nil {
		return Query{}, err
	}
	errorRaw, ok := root["error"]
	if !ok {
		return Query{}, malformedError("DuckDB SQL JSON is missing error", nil)
	}
	errorValue, err := boolValue(errorRaw)
	if err != nil {
		return Query{}, malformedError("DuckDB SQL JSON error must be boolean", err)
	}
	if errorValue {
		message, _ := optionalStringValue(root["error_message"])
		typ, _ := optionalStringValue(root["error_type"])
		subtype, _ := optionalStringValue(root["error_subtype"])
		position := -1
		if _, present := root["error_position"]; present {
			position, err = intValue(root["error_position"])
			if err != nil || position < 0 {
				return Query{}, malformedError("DuckDB SQL error position must be a non-negative integer", err)
			}
		}
		if position < 0 {
			if _, present := root["position"]; present {
				position, err = intValue(root["position"])
				if err != nil || position < 0 {
					return Query{}, malformedError("DuckDB SQL position must be a non-negative integer", err)
				}
			}
		}
		if typ == "not implemented" || stringsContains(message, "Only SELECT statements") {
			return Query{}, unsupportedError(message, typ)
		}
		if message == "" {
			message = "unknown DuckDB parser error"
		}
		return Query{}, syntaxError(message, typ, subtype, position)
	}
	if err := checkKeys(root, "error", "statements"); err != nil {
		return Query{}, err
	}
	statementsRaw, ok := root["statements"]
	if !ok {
		return Query{}, malformedError("DuckDB SQL JSON is missing statements", nil)
	}
	statements, err := rawArray(statementsRaw)
	if err != nil {
		return Query{}, malformedError("DuckDB SQL JSON statements must be an array", err)
	}
	if len(statements) > limits.MaxArrayItems {
		return Query{}, limitError("statement count exceeds parser limit")
	}
	result := Query{Statements: make([]Statement, 0, len(statements))}
	for _, statement := range statements {
		obj, err := d.object(statement)
		if err != nil {
			return Query{}, err
		}
		if err := checkKeys(obj, "node", "named_param_map"); err != nil {
			return Query{}, err
		}
		node, ok := obj["node"]
		if !ok {
			return Query{}, malformedError("DuckDB SQL statement is missing node", nil)
		}
		parsed, err := d.statement(node)
		if err != nil {
			return Query{}, err
		}
		if raw, present := obj["named_param_map"]; present {
			params, e := d.namedParameters(raw)
			if e != nil {
				return Query{}, e
			}
			attachNamedParameters(parsed, params)
		}
		result.Statements = append(result.Statements, parsed)
	}
	return result, nil
}

func scanJSONLimits(input []byte, limits Limits) error {
	depth, maxDepth := 0, 0
	inString, escaped := false, false
	for _, c := range input {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			continue
		}
		switch c {
		case '{', '[':
			depth++
			if depth > maxDepth {
				maxDepth = depth
			}
		case '}', ']':
			depth--
			if depth < 0 {
				return malformedError("unbalanced JSON delimiters", nil)
			}
		}
	}
	if inString || depth != 0 {
		return malformedError("truncated JSON", nil)
	}
	if maxDepth > limits.MaxDepth {
		return limitError(fmt.Sprintf("JSON depth exceeds %d", limits.MaxDepth))
	}
	return nil
}

func (d *decoder) count() error {
	d.nodes++
	if d.nodes > d.limits.MaxNodes {
		return limitError(fmt.Sprintf("AST node count exceeds %d", d.limits.MaxNodes))
	}
	return nil
}

func (d *decoder) object(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if err := d.count(); err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&obj); err != nil || obj == nil {
		if err == nil {
			err = fmt.Errorf("object expected")
		}
		return nil, malformedError("DuckDB SQL JSON object expected", err)
	}
	if len(obj) > d.limits.MaxArrayItems {
		return nil, limitError("JSON object field count exceeds parser limit")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, malformedError("trailing JSON data", err)
	}
	return obj, nil
}

func (d *decoder) statement(raw json.RawMessage) (Statement, error) {
	obj, err := d.object(raw)
	if err != nil {
		return nil, err
	}
	typ, err := requiredString(obj, "type")
	if err != nil {
		return nil, compatibilityError("query node type is missing")
	}
	if err := checkGeneratedSchema("statement", typ, obj); err != nil {
		return nil, err
	}
	meta := NodeMeta{Type: typ}
	switch typ {
	case "SELECT_NODE":
		if err := checkKeys(obj, "type", "modifiers", "cte_map", "select_list", "from_table", "where_clause", "group_expressions", "group_sets", "aggregate_handling", "having", "sample", "qualify"); err != nil {
			return nil, err
		}
		s := &SelectStatement{NodeMeta: meta}
		if _, ok := obj["select_list"]; !ok {
			return nil, malformedError("SELECT_NODE is missing select_list", nil)
		}
		if _, ok := obj["from_table"]; !ok {
			return nil, malformedError("SELECT_NODE is missing from_table", nil)
		}
		if raw, ok := obj["modifiers"]; ok {
			s.Modifiers, err = d.modifiers(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["cte_map"]; ok {
			s.CTEs, err = d.ctes(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["select_list"]; ok {
			s.SelectList, err = d.expressions(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["from_table"]; ok {
			s.From, err = d.relation(raw)
			if err != nil {
				return nil, err
			}
		}
		for key, target := range map[string]*Expression{"where_clause": &s.Where, "having": &s.Having, "qualify": &s.Qualify} {
			if raw, ok := obj[key]; ok && !isNull(raw) {
				*target, err = d.expression(raw)
				if err != nil {
					return nil, err
				}
			}
		}
		if raw, ok := obj["group_expressions"]; ok {
			s.GroupExpressions, err = d.expressions(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["group_sets"]; ok {
			s.GroupSets, err = d.groupSets(raw)
			if err != nil {
				return nil, err
			}
			for _, groupSet := range s.GroupSets {
				for _, index := range groupSet {
					if index >= len(s.GroupExpressions) {
						return nil, compatibilityError("group set index exceeds group expression count")
					}
				}
			}
		}
		if _, ok := obj["aggregate_handling"]; ok {
			s.AggregateHandling, err = requiredString(obj, "aggregate_handling")
			if err != nil {
				return nil, compatibilityError("aggregate handling is not a string")
			}
			if !validAggregateHandling(s.AggregateHandling) {
				return nil, compatibilityError("unknown aggregate handling")
			}
		}
		if raw, ok := obj["sample"]; ok && !isNull(raw) {
			s.Sample, err = d.value(raw)
			if err != nil {
				return nil, err
			}
			s.HasSample = true
		}
		return s, nil
	case "SET_OPERATION_NODE":
		if err := checkKeys(obj, "type", "modifiers", "cte_map", "setop_type", "left", "right", "setop_all", "children"); err != nil {
			return nil, err
		}
		s := &SetOperationStatement{NodeMeta: meta}
		s.SetOpType, err = requiredString(obj, "setop_type")
		if err != nil {
			return nil, compatibilityError("set operation type is missing")
		}
		if !validSetOperation(s.SetOpType) {
			return nil, compatibilityError("unknown set operation type " + s.SetOpType)
		}
		if raw, ok := obj["setop_all"]; ok {
			s.SetOpAll, err = boolValue(raw)
			if err != nil {
				return nil, malformedError("setop_all must be boolean", err)
			}
		}
		if raw, ok := obj["left"]; ok {
			s.Left, err = d.statement(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["right"]; ok {
			s.Right, err = d.statement(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["children"]; ok {
			arr, e := rawArray(raw)
			if e != nil {
				return nil, malformedError("set operation children must be an array", e)
			}
			for _, child := range arr {
				n, e := d.statement(child)
				if e != nil {
					return nil, e
				}
				s.Children = append(s.Children, n)
			}
		}
		if raw, ok := obj["modifiers"]; ok {
			s.Modifiers, err = d.modifiers(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["cte_map"]; ok {
			s.CTEs, err = d.ctes(raw)
			if err != nil {
				return nil, err
			}
		}
		return s, nil
	case "RECURSIVE_CTE_NODE":
		if err := checkKeys(obj, "type", "modifiers", "cte_map", "cte_name", "union_all", "left", "right", "aliases", "key_targets"); err != nil {
			return nil, err
		}
		r := &RecursiveCTEStatement{NodeMeta: meta}
		if raw, ok := obj["modifiers"]; ok {
			r.Modifiers, err = d.modifiers(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["cte_map"]; ok {
			r.CTEs, err = d.ctes(raw)
			if err != nil {
				return nil, err
			}
		}
		r.Name, err = requiredString(obj, "cte_name")
		if err != nil {
			return nil, err
		}
		if raw, ok := obj["union_all"]; ok {
			r.UnionAll, err = boolValue(raw)
			if err != nil {
				return nil, malformedError("recursive CTE union_all must be boolean", err)
			}
		}
		r.Left, err = d.statement(obj["left"])
		if err != nil {
			return nil, err
		}
		r.Right, err = d.statement(obj["right"])
		if err != nil {
			return nil, err
		}
		if raw, ok := obj["aliases"]; ok {
			r.Aliases, err = d.stringsValue(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["key_targets"]; ok {
			r.KeyTargets, err = d.expressions(raw)
			if err != nil {
				return nil, err
			}
		}
		return r, nil
	case "CTE_NODE":
		if err := checkKeys(obj, "type", "modifiers", "cte_map", "cte_name", "query", "child", "aliases", "materialized"); err != nil {
			return nil, err
		}
		r := &CTENodeStatement{NodeMeta: meta}
		if raw, ok := obj["modifiers"]; ok {
			r.Modifiers, err = d.modifiers(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["cte_map"]; ok {
			r.CTEs, err = d.ctes(raw)
			if err != nil {
				return nil, err
			}
		}
		r.Name, err = requiredString(obj, "cte_name")
		if err != nil {
			return nil, err
		}
		if _, ok := obj["materialized"]; ok {
			r.Materialized, err = requiredString(obj, "materialized")
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["aliases"]; ok {
			r.Aliases, err = d.stringsValue(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["query"]; ok {
			r.Query, err = d.statementEnvelope(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["child"]; ok {
			r.Child, err = d.statement(raw)
			if err != nil {
				return nil, err
			}
		}
		return r, nil
	case "UPDATE_QUERY_NODE", "DELETE_QUERY_NODE", "INSERT_QUERY_NODE", "MERGE_QUERY_NODE", "COPY_QUERY_NODE":
		return nil, unsupportedError("statement class "+typ+" is not part of the query contract", typ)
	default:
		return nil, compatibilityError("unknown DuckDB query node " + typ)
	}
}

func (d *decoder) ctes(raw json.RawMessage) ([]CTE, error) {
	obj, err := d.object(raw)
	if err != nil {
		return nil, err
	}
	if err := checkKeys(obj, "map"); err != nil {
		return nil, err
	}
	if err := checkGeneratedSchema("supporting", "CommonTableExpressionMap", obj); err != nil {
		return nil, err
	}
	entries, err := rawArray(obj["map"])
	if err != nil {
		return nil, malformedError("CTE map must be an array", err)
	}
	if len(entries) > d.limits.MaxArrayItems {
		return nil, limitError("CTE count exceeds parser limit")
	}
	result := make([]CTE, 0, len(entries))
	for _, entryRaw := range entries {
		entry, err := d.object(entryRaw)
		if err != nil {
			return nil, err
		}
		if err := checkKeys(entry, "key", "value"); err != nil {
			return nil, err
		}
		name, err := requiredString(entry, "key")
		if err != nil {
			return nil, malformedError("CTE key must be a string", err)
		}
		value, err := d.object(entry["value"])
		if err != nil {
			return nil, err
		}
		if err := checkGeneratedSchema("supporting", "CommonTableExpressionInfo", value); err != nil {
			return nil, err
		}
		cte := CTE{Name: name}
		if _, ok := value["materialized"]; ok {
			cte.Materialized, err = requiredString(value, "materialized")
			if err != nil {
				return nil, malformedError("CTE materialized must be a string", err)
			}
			if !validCTEMaterialized(cte.Materialized) {
				return nil, compatibilityError("unknown CTE materialized mode " + cte.Materialized)
			}
		}
		if raw, ok := value["aliases"]; ok {
			cte.Aliases, err = d.stringsValue(raw)
			if err != nil {
				return nil, malformedError("CTE aliases must be strings", err)
			}
		}
		if raw, ok := value["key_targets"]; ok {
			cte.KeyTargets, err = d.expressions(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := value["query"]; ok && !isNull(raw) {
			cte.Query, err = d.statementEnvelope(raw)
			if err != nil {
				return nil, err
			}
		}
		result = append(result, cte)
	}
	return result, nil
}

func (d *decoder) relation(raw json.RawMessage) (Relation, error) {
	obj, err := d.object(raw)
	if err != nil {
		return nil, err
	}
	typ, err := requiredString(obj, "type")
	if err != nil {
		return nil, compatibilityError("table reference type is missing")
	}
	generatedType := typ
	if generatedType == "EMPTY" {
		generatedType = "EMPTY_FROM"
	}
	if err := checkGeneratedSchema("relation", generatedType, obj); err != nil {
		return nil, err
	}
	meta, err := d.meta(obj, "", typ)
	if err != nil {
		return nil, err
	}
	if raw, ok := obj["sample"]; ok && !isNull(raw) {
		meta.Sample, err = d.value(raw)
		if err != nil {
			return nil, err
		}
		meta.HasSample = true
	}
	if meta.Span.Start >= 0 && meta.Span.End == meta.Span.Start {
		meta.Span.End = relationExtent(d.source, meta.Span.Start)
	}
	switch typ {
	case "BASE_TABLE":
		if err := checkKeys(obj, "type", "alias", "sample", "query_location", "query_location_length", "schema_name", "table_name", "column_name_alias", "catalog_name", "at_clause", "qualified_name"); err != nil {
			return nil, err
		}
		if _, ok := obj["table_name"]; !ok {
			return nil, malformedError("BASE_TABLE is missing table_name", nil)
		}
		r := &BaseTableRelation{NodeMeta: meta}
		r.Schema, err = optionalString(obj["schema_name"])
		if err != nil {
			return nil, err
		}
		r.Name, err = optionalString(obj["table_name"])
		if err != nil {
			return nil, err
		}
		r.Catalog, err = optionalString(obj["catalog_name"])
		if err != nil {
			return nil, err
		}
		if raw, ok := obj["column_name_alias"]; ok {
			r.ColumnAliases, err = d.stringsValue(raw)
			if err != nil {
				return nil, malformedError("column aliases must be strings", err)
			}
		}
		if raw, ok := obj["qualified_name"]; ok {
			r.QualifiedName, err = d.stringsValue(raw)
			if err != nil && !isNull(raw) {
				return nil, err
			}
		}
		if raw, ok := obj["at_clause"]; ok && !isNull(raw) {
			r.At, err = d.atClause(raw)
			if err != nil {
				return nil, err
			}
		}
		return r, nil
	case "EMPTY":
		if err := checkKeys(obj, "type", "alias", "sample", "query_location", "query_location_length"); err != nil {
			return nil, err
		}
		return &EmptyRelation{NodeMeta: meta}, nil
	case "JOIN":
		if err := checkKeys(obj, "type", "alias", "sample", "query_location", "query_location_length", "left", "right", "condition", "join_type", "ref_type", "using_columns", "delim_flipped", "duplicate_eliminated_columns", "is_implicit", "ranking_expression", "nearest_count", "nearest_order_type", "nearest_approx"); err != nil {
			return nil, err
		}
		r := &JoinRelation{NodeMeta: meta}
		r.Left, err = d.relation(obj["left"])
		if err != nil {
			return nil, err
		}
		r.Right, err = d.relation(obj["right"])
		if err != nil {
			return nil, err
		}
		r.JoinType, err = optionalString(obj["join_type"])
		if err != nil {
			return nil, err
		}
		r.RefType, err = optionalString(obj["ref_type"])
		if err != nil {
			return nil, err
		}
		if raw, ok := obj["condition"]; ok && !isNull(raw) {
			r.Condition, err = d.expression(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["using_columns"]; ok {
			r.UsingColumns, err = d.stringsValue(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["duplicate_eliminated_columns"]; ok {
			r.DuplicateEliminatedColumns, err = d.expressions(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["ranking_expression"]; ok && !isNull(raw) {
			r.RankingExpression, err = d.expression(raw)
			if err != nil {
				return nil, err
			}
		}
		r.DelimFlipped, err = optionalBool(obj["delim_flipped"])
		if err != nil {
			return nil, err
		}
		r.IsImplicit, err = optionalBool(obj["is_implicit"])
		if err != nil {
			return nil, err
		}
		r.NearestCount, err = optionalInt64(obj["nearest_count"])
		if err != nil {
			return nil, err
		}
		r.NearestOrderType, err = optionalString(obj["nearest_order_type"])
		if err != nil {
			return nil, err
		}
		r.NearestApprox, err = optionalBool(obj["nearest_approx"])
		if err != nil {
			return nil, err
		}
		return r, nil
	case "SUBQUERY":
		if err := checkKeys(obj, "type", "alias", "sample", "query_location", "query_location_length", "subquery", "column_name_alias"); err != nil {
			return nil, err
		}
		r := &SubqueryRelation{NodeMeta: meta}
		r.Query, err = d.statementEnvelope(obj["subquery"])
		if err != nil {
			return nil, err
		}
		if raw, ok := obj["column_name_alias"]; ok {
			r.ColumnAliases, err = d.stringsValue(raw)
			if err != nil {
				return nil, err
			}
		}
		return r, nil
	case "TABLE_FUNCTION":
		if err := checkKeys(obj, "type", "alias", "sample", "query_location", "query_location_length", "function", "column_name_alias", "with_ordinality"); err != nil {
			return nil, err
		}
		r := &TableFunctionRelation{NodeMeta: meta}
		r.Function, err = d.expression(obj["function"])
		if err != nil {
			return nil, err
		}
		if raw, ok := obj["column_name_alias"]; ok {
			r.ColumnAliases, err = d.stringsValue(raw)
			if err != nil {
				return nil, err
			}
		}
		r.WithOrdinality, err = optionalString(obj["with_ordinality"])
		if err != nil {
			return nil, err
		}
		return r, nil
	case "EXPRESSION_LIST":
		if err := checkKeys(obj, "type", "alias", "sample", "query_location", "query_location_length", "expected_names", "expected_types", "values"); err != nil {
			return nil, err
		}
		r := &ExpressionListRelation{NodeMeta: meta}
		if raw, ok := obj["expected_names"]; ok {
			r.ExpectedNames, err = d.stringsValue(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["expected_types"]; ok {
			r.ExpectedTypes, err = d.logicalTypes(raw)
			if err != nil {
				return nil, err
			}
		}
		r.Values, err = d.expressionRows(obj["values"])
		return r, err
	case "PIVOT":
		if err := checkKeys(obj, "type", "alias", "sample", "query_location", "query_location_length", "source", "aggregates", "unpivot_names", "pivots", "groups", "column_name_alias", "include_nulls"); err != nil {
			return nil, err
		}
		r := &PivotRelation{NodeMeta: meta}
		if raw, ok := obj["source"]; ok && !isNull(raw) {
			r.Source, err = d.relation(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["aggregates"]; ok {
			r.Aggregates, err = d.expressions(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["unpivot_names"]; ok {
			r.UnpivotNames, err = d.stringsValue(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["groups"]; ok {
			r.Groups, err = d.stringsValue(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["column_name_alias"]; ok {
			r.ColumnAliases, err = d.stringsValue(raw)
			if err != nil {
				return nil, err
			}
		}
		r.IncludeNulls, err = optionalBool(obj["include_nulls"])
		if err != nil {
			return nil, err
		}
		if raw, ok := obj["pivots"]; ok {
			r.Pivots, err = d.pivots(raw)
			if err != nil {
				return nil, err
			}
		}
		return r, nil
	case "SHOW_REF":
		if err := checkKeys(obj, "type", "alias", "sample", "query_location", "query_location_length", "table_name", "query", "show_type", "catalog_name", "schema_name"); err != nil {
			return nil, err
		}
		r := &ShowRelation{NodeMeta: meta}
		r.Name, err = optionalString(obj["table_name"])
		if err != nil {
			return nil, err
		}
		r.Schema, err = optionalString(obj["schema_name"])
		if err != nil {
			return nil, err
		}
		r.Catalog, err = optionalString(obj["catalog_name"])
		if err != nil {
			return nil, err
		}
		r.ShowType, err = optionalString(obj["show_type"])
		if err != nil {
			return nil, err
		}
		if raw, ok := obj["query"]; ok && !isNull(raw) {
			r.Query, err = d.statementEnvelope(raw)
		}
		return r, err
	case "COLUMN_DATA":
		if err := checkKeys(obj, "type", "alias", "sample", "query_location", "query_location_length", "expected_names", "collection"); err != nil {
			return nil, err
		}
		r := &ColumnDataRelation{NodeMeta: meta}
		if raw, ok := obj["expected_names"]; ok {
			r.ExpectedNames, err = d.stringsValue(raw)
		}
		return r, err
	default:
		return nil, compatibilityError("unknown DuckDB table reference " + typ)
	}
}

func (d *decoder) pivots(raw json.RawMessage) ([]PivotColumn, error) {
	arr, err := rawArray(raw)
	if err != nil {
		return nil, malformedError("pivots must be an array", err)
	}
	if len(arr) > d.limits.MaxArrayItems {
		return nil, limitError("pivot column count exceeds parser limit")
	}
	result := make([]PivotColumn, 0, len(arr))
	for _, columnRaw := range arr {
		column, e := d.object(columnRaw)
		if e != nil {
			return nil, e
		}
		if e = checkKeys(column, "pivot_expressions", "unpivot_names", "entries", "pivot_enum"); e != nil {
			return nil, e
		}
		value := PivotColumn{}
		if expressions, ok := column["pivot_expressions"]; ok {
			value.PivotExpressions, e = d.expressions(expressions)
			if e != nil {
				return nil, e
			}
		}
		if names, ok := column["unpivot_names"]; ok {
			value.UnpivotNames, e = d.stringsValue(names)
			if e != nil {
				return nil, e
			}
		}
		if enum, ok := column["pivot_enum"]; ok {
			value.PivotEnum, e = optionalString(enum)
			if e != nil {
				return nil, e
			}
		}
		if entries, ok := column["entries"]; ok {
			value.Entries, e = d.pivotEntries(entries)
			if e != nil {
				return nil, e
			}
		}
		result = append(result, value)
	}
	return result, nil
}

func (d *decoder) pivotEntries(raw json.RawMessage) ([]PivotColumnEntry, error) {
	arr, err := rawArray(raw)
	if err != nil {
		return nil, malformedError("pivot entries must be an array", err)
	}
	if len(arr) > d.limits.MaxArrayItems {
		return nil, limitError("pivot entry count exceeds parser limit")
	}
	result := make([]PivotColumnEntry, 0, len(arr))
	for _, entryRaw := range arr {
		entry, e := d.object(entryRaw)
		if e != nil {
			return nil, e
		}
		if e = checkKeys(entry, "values", "star_expr", "alias"); e != nil {
			return nil, e
		}
		value := PivotColumnEntry{}
		if values, ok := entry["values"]; ok {
			value.Values, e = d.values(values)
			if e != nil {
				return nil, e
			}
		}
		if expr, ok := entry["star_expr"]; ok && !isNull(expr) {
			value.StarExpression, e = d.expression(expr)
			if e != nil {
				return nil, e
			}
		}
		if alias, ok := entry["alias"]; ok {
			value.Alias, e = optionalString(alias)
			if e != nil {
				return nil, e
			}
		}
		result = append(result, value)
	}
	return result, nil
}

func (d *decoder) expression(raw json.RawMessage) (Expression, error) {
	obj, err := d.object(raw)
	if err != nil {
		return nil, err
	}
	class, err := requiredString(obj, "class")
	if err != nil {
		return nil, compatibilityError("expression class is missing")
	}
	typ, err := requiredString(obj, "type")
	if err != nil {
		return nil, compatibilityError("expression type is missing")
	}
	if err := checkGeneratedSchema("expression", class, obj); err != nil {
		return nil, err
	}
	if !validExpressionType(class, typ) {
		return nil, compatibilityError("expression class " + class + " does not allow type " + typ)
	}
	meta, err := d.meta(obj, class, typ)
	if err != nil {
		return nil, err
	}
	switch class {
	case "CONSTANT":
		if err := checkKeys(obj, "class", "type", "alias", "query_location", "query_location_length", "value"); err != nil {
			return nil, err
		}
		e := &ConstantExpression{NodeMeta: meta}
		e.Value, err = d.value(obj["value"])
		if err != nil {
			return nil, err
		}
		return e, nil
	case "COLUMN_REF":
		if err := checkKeys(obj, "class", "type", "alias", "query_location", "query_location_length", "column_names"); err != nil {
			return nil, err
		}
		e := &ColumnExpression{NodeMeta: meta}
		e.Names, err = d.stringsValue(obj["column_names"])
		return e, err
	case "STAR":
		if err := checkKeys(obj, "class", "type", "alias", "query_location", "query_location_length", "relation_name", "exclude_list", "replace_list", "columns", "unpacked", "expr", "qualified_exclude_list", "rename_list"); err != nil {
			return nil, err
		}
		e := &StarExpression{NodeMeta: meta}
		e.RelationName, err = optionalString(obj["relation_name"])
		if err != nil {
			return nil, err
		}
		if raw, ok := obj["exclude_list"]; ok {
			e.ExcludeList, err = d.stringsValue(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["replace_list"]; ok {
			e.ReplaceList, err = d.namedExpressions(raw)
			if err != nil {
				return nil, err
			}
		}
		e.Columns, err = optionalBool(obj["columns"])
		if err != nil {
			return nil, err
		}
		e.Unpacked, err = optionalBool(obj["unpacked"])
		if err != nil {
			return nil, err
		}
		if raw, ok := obj["expr"]; ok && !isNull(raw) {
			e.Expression, err = d.expression(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["qualified_exclude_list"]; ok {
			e.QualifiedExcludeList, err = d.stringsValue(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["rename_list"]; ok {
			e.RenameList, err = d.namedExpressions(raw)
			if err != nil {
				return nil, err
			}
		}
		return e, nil
	case "FUNCTION":
		if err := checkKeys(obj, "class", "type", "alias", "query_location", "query_location_length", "function_name", "schema", "catalog", "children", "filter", "order_bys", "distinct", "is_operator", "export_state"); err != nil {
			return nil, err
		}
		e := &FunctionExpression{NodeMeta: meta}
		e.Name, err = requiredString(obj, "function_name")
		if err != nil {
			return nil, err
		}
		e.Schema, err = optionalString(obj["schema"])
		if err != nil {
			return nil, err
		}
		e.Catalog, err = optionalString(obj["catalog"])
		if err != nil {
			return nil, err
		}
		if raw, ok := obj["children"]; ok {
			e.Children, err = d.expressions(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["filter"]; ok && !isNull(raw) {
			e.Filter, err = d.expression(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["order_bys"]; ok && !isNull(raw) {
			e.OrderBys, err = d.orderModifier(raw)
			if err != nil {
				return nil, err
			}
		}
		e.Distinct, err = optionalBool(obj["distinct"])
		if err != nil {
			return nil, err
		}
		e.IsOperator, err = optionalBool(obj["is_operator"])
		if err != nil {
			return nil, err
		}
		e.ExportState, err = optionalBool(obj["export_state"])
		if err != nil {
			return nil, err
		}
		return e, nil
	case "OPERATOR", "CONJUNCTION":
		if err := checkKeys(obj, "class", "type", "alias", "query_location", "query_location_length", "children"); err != nil {
			return nil, err
		}
		e := &OperatorExpression{NodeMeta: meta}
		e.Children, err = d.expressions(obj["children"])
		return e, err
	case "COMPARISON":
		if err := checkKeys(obj, "class", "type", "alias", "query_location", "query_location_length", "left", "right"); err != nil {
			return nil, err
		}
		e := &ComparisonExpression{NodeMeta: meta}
		e.Left, err = d.expression(obj["left"])
		if err != nil {
			return nil, err
		}
		e.Right, err = d.expression(obj["right"])
		return e, err
	case "CAST":
		if err := checkKeys(obj, "class", "type", "alias", "query_location", "query_location_length", "child", "cast_type", "try_cast"); err != nil {
			return nil, err
		}
		e := &CastExpression{NodeMeta: meta}
		e.Child, err = d.expression(obj["child"])
		if err != nil {
			return nil, err
		}
		e.CastType, err = d.logicalTypeValue(obj["cast_type"])
		if err != nil {
			return nil, err
		}
		e.TryCast, err = optionalBool(obj["try_cast"])
		if err != nil {
			return nil, err
		}
		return e, nil
	case "CASE":
		if err := checkKeys(obj, "class", "type", "alias", "query_location", "query_location_length", "case_checks", "else_expr"); err != nil {
			return nil, err
		}
		e := &CaseExpression{NodeMeta: meta}
		e.Checks, err = d.caseChecks(obj["case_checks"])
		if err != nil {
			return nil, err
		}
		if raw, ok := obj["else_expr"]; ok && !isNull(raw) {
			e.Else, err = d.expression(raw)
		}
		return e, err
	case "WINDOW":
		if err := checkKeys(obj, "class", "type", "alias", "query_location", "query_location_length", "function_name", "schema", "catalog", "partitions", "orders", "start", "end", "start_expr", "end_expr", "offset_expr", "default_expr", "children", "filter_expr", "filter", "ignore_nulls", "exclude_clause", "distinct", "arg_orders"); err != nil {
			return nil, err
		}
		e := &WindowExpression{NodeMeta: meta}
		e.FunctionName, err = optionalString(obj["function_name"])
		if err != nil {
			return nil, err
		}
		e.Schema, err = optionalString(obj["schema"])
		if err != nil {
			return nil, err
		}
		e.Catalog, err = optionalString(obj["catalog"])
		if err != nil {
			return nil, err
		}
		if raw, ok := obj["partitions"]; ok {
			e.Partitions, err = d.expressions(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["orders"]; ok {
			e.Orders, err = d.orders(raw)
			if err != nil {
				return nil, err
			}
		}
		e.Start, err = optionalString(obj["start"])
		if err != nil {
			return nil, err
		}
		e.End, err = optionalString(obj["end"])
		if err != nil {
			return nil, err
		}
		if !validWindowBoundary(e.Start) || !validWindowBoundary(e.End) {
			return nil, compatibilityError("unknown window boundary")
		}
		if raw, ok := obj["start_expr"]; ok && !isNull(raw) {
			e.StartExpression, err = d.expression(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["end_expr"]; ok && !isNull(raw) {
			e.EndExpression, err = d.expression(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["offset_expr"]; ok && !isNull(raw) {
			e.OffsetExpression, err = d.expression(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["default_expr"]; ok && !isNull(raw) {
			e.DefaultExpression, err = d.expression(raw)
			if err != nil {
				return nil, err
			}
		}
		if raw, ok := obj["children"]; ok {
			e.Children, err = d.expressions(raw)
			if err != nil {
				return nil, err
			}
		}
		filterRaw, filterOK := obj["filter_expr"]
		if !filterOK {
			filterRaw, filterOK = obj["filter"]
		}
		if filterOK && !isNull(filterRaw) {
			e.FilterExpression, err = d.expression(filterRaw)
			if err != nil {
				return nil, err
			}
			e.Filter = e.FilterExpression
		}
		e.Distinct, err = optionalBool(obj["distinct"])
		if err != nil {
			return nil, err
		}
		e.IgnoreNulls, err = optionalBool(obj["ignore_nulls"])
		if err != nil {
			return nil, err
		}
		e.ExcludeClause, err = optionalString(obj["exclude_clause"])
		if err != nil {
			return nil, err
		}
		if !validWindowExclude(e.ExcludeClause) {
			return nil, compatibilityError("unknown window exclude mode " + e.ExcludeClause)
		}
		if raw, ok := obj["arg_orders"]; ok {
			e.ArgOrders, err = d.orders(raw)
		}
		return e, err
	case "SUBQUERY":
		if err := checkKeys(obj, "class", "type", "alias", "query_location", "query_location_length", "subquery_type", "subquery", "child", "comparison_type"); err != nil {
			return nil, err
		}
		e := &SubqueryExpression{NodeMeta: meta}
		e.SubqueryType, err = optionalString(obj["subquery_type"])
		if err != nil {
			return nil, err
		}
		e.ComparisonType, err = optionalString(obj["comparison_type"])
		if err != nil {
			return nil, err
		}
		e.Query, err = d.statementEnvelope(obj["subquery"])
		if err != nil {
			return nil, err
		}
		if raw, ok := obj["child"]; ok && !isNull(raw) {
			e.Child, err = d.expression(raw)
		}
		return e, err
	case "BETWEEN":
		if err := checkKeys(obj, "class", "type", "alias", "query_location", "query_location_length", "input", "lower", "upper"); err != nil {
			return nil, err
		}
		e := &BetweenExpression{NodeMeta: meta}
		e.Input, err = d.expression(obj["input"])
		if err != nil {
			return nil, err
		}
		e.Lower, err = d.expression(obj["lower"])
		if err != nil {
			return nil, err
		}
		e.Upper, err = d.expression(obj["upper"])
		return e, err
	case "COLLATE":
		if err := checkKeys(obj, "class", "type", "alias", "query_location", "query_location_length", "child", "collation"); err != nil {
			return nil, err
		}
		e := &CollateExpression{NodeMeta: meta}
		e.Child, err = d.expression(obj["child"])
		if err != nil {
			return nil, err
		}
		e.Collation, err = optionalString(obj["collation"])
		if err != nil {
			return nil, err
		}
		return e, nil
	case "DEFAULT":
		if err := checkKeys(obj, "class", "type", "alias", "query_location", "query_location_length"); err != nil {
			return nil, err
		}
		return &DefaultExpression{NodeMeta: meta}, nil
	case "LAMBDA":
		if err := checkKeys(obj, "class", "type", "alias", "query_location", "query_location_length", "lhs", "expr", "syntax_type"); err != nil {
			return nil, err
		}
		e := &LambdaExpression{NodeMeta: meta}
		e.LHS, err = d.expression(obj["lhs"])
		if err != nil {
			return nil, err
		}
		e.Expr, err = d.expression(obj["expr"])
		if err != nil {
			return nil, err
		}
		e.SyntaxType, err = optionalString(obj["syntax_type"])
		if err != nil {
			return nil, err
		}
		return e, nil
	case "LAMBDA_REF":
		if err := checkKeys(obj, "class", "type", "alias", "query_location", "query_location_length", "lambda_idx", "column_name"); err != nil {
			return nil, err
		}
		e := &LambdaRefExpression{NodeMeta: meta}
		e.LambdaIndex, err = optionalInt64(obj["lambda_idx"])
		if err != nil {
			return nil, err
		}
		e.ColumnName, err = optionalString(obj["column_name"])
		if err != nil {
			return nil, err
		}
		return e, nil
	case "PARAMETER":
		if err := checkKeys(obj, "class", "type", "alias", "query_location", "query_location_length", "identifier"); err != nil {
			return nil, err
		}
		e := &ParameterExpression{NodeMeta: meta}
		e.Identifier, err = optionalString(obj["identifier"])
		if err != nil {
			return nil, err
		}
		return e, nil
	case "POSITIONAL_REFERENCE":
		if err := checkKeys(obj, "class", "type", "alias", "query_location", "query_location_length", "index"); err != nil {
			return nil, err
		}
		e := &PositionalReferenceExpression{NodeMeta: meta}
		e.Index, err = optionalInt64(obj["index"])
		if err != nil {
			return nil, err
		}
		return e, nil
	case "TYPE":
		if err := checkKeys(obj, "class", "type", "alias", "query_location", "query_location_length", "catalog", "schema", "type_name", "children"); err != nil {
			return nil, err
		}
		e := &TypeExpression{NodeMeta: meta}
		e.Catalog, err = optionalString(obj["catalog"])
		if err != nil {
			return nil, err
		}
		e.Schema, err = optionalString(obj["schema"])
		if err != nil {
			return nil, err
		}
		e.Name, err = optionalString(obj["type_name"])
		if err != nil {
			return nil, err
		}
		if raw, ok := obj["children"]; ok {
			e.Children, err = d.expressions(raw)
		}
		return e, err
	default:
		return nil, compatibilityError("unknown DuckDB expression class " + class)
	}
}

func (d *decoder) meta(obj map[string]json.RawMessage, class, typ string) (NodeMeta, error) {
	m := NodeMeta{Class: class, Type: typ}
	var err error
	m.Alias, err = optionalString(obj["alias"])
	if err != nil {
		return NodeMeta{}, malformedError(class+" alias must be a string", err)
	}
	m.Span.Start = -1
	if raw, ok := obj["query_location"]; ok && !isNull(raw) {
		m.Span.Start, err = intValue(raw)
		if err != nil || m.Span.Start < 0 {
			return NodeMeta{}, malformedError(class+" query_location must be a non-negative integer", err)
		}
	}
	m.Span.End = m.Span.Start
	if raw, ok := obj["query_location_length"]; ok && !isNull(raw) {
		length, e := intValue(raw)
		if e != nil || length < 0 {
			return NodeMeta{}, malformedError(class+" query_location_length must be a non-negative integer", e)
		}
		if m.Span.Start >= 0 {
			m.Span.End = m.Span.Start + length
		}
	}
	return m, nil
}

func (d *decoder) statementEnvelope(raw json.RawMessage) (Statement, error) {
	obj, err := d.object(raw)
	if err != nil {
		return nil, err
	}
	if err := checkKeys(obj, "node", "named_param_map"); err != nil {
		return nil, err
	}
	node, ok := obj["node"]
	if !ok {
		return nil, malformedError("query wrapper is missing node", nil)
	}
	statement, err := d.statement(node)
	if err != nil {
		return nil, err
	}
	if raw, present := obj["named_param_map"]; present {
		params, e := d.namedParameters(raw)
		if e != nil {
			return nil, e
		}
		attachNamedParameters(statement, params)
	}
	return statement, nil
}

func (d *decoder) namedParameters(raw json.RawMessage) ([]NamedParameter, error) {
	entries, err := rawArray(raw)
	if err != nil {
		return nil, malformedError("named_param_map must be an array", err)
	}
	if len(entries) > d.limits.MaxArrayItems {
		return nil, limitError("named parameter count exceeds parser limit")
	}
	result := make([]NamedParameter, 0, len(entries))
	for _, entryRaw := range entries {
		entry, e := d.object(entryRaw)
		if e != nil {
			return nil, e
		}
		if e = checkKeys(entry, "key", "value"); e != nil {
			return nil, e
		}
		name, e := requiredString(entry, "key")
		if e != nil {
			return nil, malformedError("named parameter key must be a string", e)
		}
		index, e := int64Value(entry["value"])
		if e != nil || index < 0 {
			return nil, malformedError("named parameter value must be a non-negative integer", e)
		}
		result = append(result, NamedParameter{Name: name, Index: index})
	}
	return result, nil
}

func attachNamedParameters(statement Statement, params []NamedParameter) {
	switch value := statement.(type) {
	case *SelectStatement:
		value.NodeMeta.NamedParameters = params
	case *SetOperationStatement:
		value.NodeMeta.NamedParameters = params
	case *RecursiveCTEStatement:
		value.NodeMeta.NamedParameters = params
	case *CTENodeStatement:
		value.NodeMeta.NamedParameters = params
	}
}

func relationExtent(sqlText string, start int) int {
	if start < 0 || start >= len(sqlText) {
		return start
	}
	i := start
	read := func() bool {
		if i >= len(sqlText) {
			return false
		}
		if sqlText[i] == '"' {
			i++
			for i < len(sqlText) {
				if sqlText[i] == '"' {
					i++
					if i < len(sqlText) && sqlText[i] == '"' {
						i++
						continue
					}
					return true
				}
				i++
			}
			return false
		}
		if !((sqlText[i] >= 'A' && sqlText[i] <= 'Z') || (sqlText[i] >= 'a' && sqlText[i] <= 'z') || sqlText[i] == '_') {
			return false
		}
		i++
		for i < len(sqlText) && ((sqlText[i] >= 'A' && sqlText[i] <= 'Z') || (sqlText[i] >= 'a' && sqlText[i] <= 'z') || (sqlText[i] >= '0' && sqlText[i] <= '9') || sqlText[i] == '_' || sqlText[i] == '$') {
			i++
		}
		return true
	}
	if !read() {
		return start
	}
	for {
		j := i
		for j < len(sqlText) && (sqlText[j] == ' ' || sqlText[j] == '\t' || sqlText[j] == '\r' || sqlText[j] == '\n') {
			j++
		}
		if j >= len(sqlText) || sqlText[j] != '.' {
			break
		}
		i = j + 1
		if !read() {
			break
		}
	}
	return i
}

func (d *decoder) atClause(raw json.RawMessage) (*AtClause, error) {
	obj, err := d.object(raw)
	if err != nil {
		return nil, err
	}
	if err := checkGeneratedSchema("supporting", "AtClause", obj); err != nil {
		return nil, err
	}
	unit, err := requiredString(obj, "unit")
	if err != nil {
		return nil, malformedError("AT clause unit must be a string", err)
	}
	clause := &AtClause{Unit: unit}
	if expr, ok := obj["expr"]; ok && !isNull(expr) {
		clause.Expression, err = d.expression(expr)
		if err != nil {
			return nil, err
		}
	}
	return clause, nil
}

func (d *decoder) expressions(raw json.RawMessage) ([]Expression, error) {
	arr, err := rawArray(raw)
	if err != nil {
		return nil, malformedError("expressions must be an array", err)
	}
	if len(arr) > d.limits.MaxArrayItems {
		return nil, limitError("expression count exceeds parser limit")
	}
	out := make([]Expression, 0, len(arr))
	for _, v := range arr {
		e, er := d.expression(v)
		if er != nil {
			return nil, er
		}
		out = append(out, e)
	}
	return out, nil
}
func (d *decoder) expressionRows(raw json.RawMessage) ([][]Expression, error) {
	arr, err := rawArray(raw)
	if err != nil {
		return nil, err
	}
	if len(arr) > d.limits.MaxArrayItems {
		return nil, limitError("expression row count exceeds parser limit")
	}
	out := make([][]Expression, 0, len(arr))
	for _, row := range arr {
		v, er := d.expressions(row)
		if er != nil {
			return nil, er
		}
		out = append(out, v)
	}
	return out, nil
}
func (d *decoder) namedExpressions(raw json.RawMessage) ([]NamedExpression, error) {
	obj, err := d.object(raw)
	if err == nil {
		_ = obj
	}
	arr, err := rawArray(raw)
	if err != nil {
		return nil, err
	}
	if len(arr) > d.limits.MaxArrayItems {
		return nil, limitError("named expression count exceeds parser limit")
	}
	out := make([]NamedExpression, 0, len(arr))
	for _, v := range arr {
		m, er := d.object(v)
		if er != nil {
			return nil, er
		}
		if er = checkKeys(m, "key", "value"); er != nil {
			return nil, er
		}
		name, er := requiredString(m, "key")
		if er != nil {
			return nil, er
		}
		e, er := d.expression(m["value"])
		if er != nil {
			return nil, er
		}
		out = append(out, NamedExpression{Name: name, Expression: e})
	}
	return out, nil
}
func (d *decoder) caseChecks(raw json.RawMessage) ([]CaseCheck, error) {
	arr, err := rawArray(raw)
	if err != nil {
		return nil, err
	}
	if len(arr) > d.limits.MaxArrayItems {
		return nil, limitError("CASE check count exceeds parser limit")
	}
	out := make([]CaseCheck, 0, len(arr))
	for _, v := range arr {
		m, er := d.object(v)
		if er != nil {
			return nil, er
		}
		if er = checkKeys(m, "when_expr", "then_expr"); er != nil {
			return nil, er
		}
		w, er := d.expression(m["when_expr"])
		if er != nil {
			return nil, er
		}
		if er = checkGeneratedSchema("supporting", "CaseCheck", m); er != nil {
			return nil, er
		}
		t, er := d.expression(m["then_expr"])
		if er != nil {
			return nil, er
		}
		out = append(out, CaseCheck{When: w, Then: t})
	}
	return out, nil
}
func (d *decoder) modifiers(raw json.RawMessage) ([]Modifier, error) {
	arr, err := rawArray(raw)
	if err != nil {
		return nil, err
	}
	if len(arr) > d.limits.MaxArrayItems {
		return nil, limitError("modifier count exceeds parser limit")
	}
	out := make([]Modifier, 0, len(arr))
	for _, v := range arr {
		m, er := d.object(v)
		if er != nil {
			return nil, er
		}
		typ, er := requiredString(m, "type")
		if er != nil {
			return nil, er
		}
		if er = checkGeneratedSchema("modifier", typ, m); er != nil {
			return nil, er
		}
		switch typ {
		case "DISTINCT_MODIFIER":
			modifier := &DistinctModifier{}
			if raw, ok := m["distinct_on_targets"]; ok {
				modifier.DistinctOnTargets, er = d.expressions(raw)
				if er != nil {
					return nil, er
				}
			}
			out = append(out, modifier)
		case "ORDER_MODIFIER":
			if er = checkKeys(m, "type", "orders"); er != nil {
				return nil, er
			}
			orders, er := d.orders(m["orders"])
			if er != nil {
				return nil, er
			}
			out = append(out, &OrderModifier{Orders: orders})
		case "LIMIT_MODIFIER":
			if er = checkKeys(m, "type", "limit", "offset"); er != nil {
				return nil, er
			}
			l := &LimitModifier{}
			l.Limit, er = d.expression(m["limit"])
			if er != nil {
				return nil, er
			}
			if raw, ok := m["offset"]; ok && !isNull(raw) {
				l.Offset, er = d.expression(raw)
				if er != nil {
					return nil, er
				}
			}
			out = append(out, l)
		case "LIMIT_PERCENT_MODIFIER":
			l := &LimitPercentModifier{}
			if raw, ok := m["limit"]; ok && !isNull(raw) {
				l.Limit, er = d.expression(raw)
				if er != nil {
					return nil, er
				}
			}
			if raw, ok := m["offset"]; ok && !isNull(raw) {
				l.Offset, er = d.expression(raw)
				if er != nil {
					return nil, er
				}
			}
			out = append(out, l)
		default:
			return nil, compatibilityError("unknown result modifier " + typ)
		}
	}
	return out, nil
}
func (d *decoder) orders(raw json.RawMessage) ([]Order, error) {
	arr, err := rawArray(raw)
	if err != nil {
		return nil, err
	}
	if len(arr) > d.limits.MaxArrayItems {
		return nil, limitError("order count exceeds parser limit")
	}
	out := make([]Order, 0, len(arr))
	for _, v := range arr {
		m, er := d.object(v)
		if er != nil {
			return nil, er
		}
		if er = checkKeys(m, "type", "null_order", "expression"); er != nil {
			return nil, er
		}
		if er = checkGeneratedSchema("supporting", "OrderByNode", m); er != nil {
			return nil, er
		}
		o := Order{Type: "ORDER_DEFAULT", NullOrder: "ORDER_DEFAULT"}
		if raw, ok := m["type"]; ok {
			o.Type, er = optionalString(raw)
			if er != nil || !validOrderType(o.Type) {
				return nil, compatibilityError("unknown order type")
			}
		}
		if raw, ok := m["null_order"]; ok {
			o.NullOrder, er = optionalString(raw)
			if er != nil || !validOrderNullType(o.NullOrder) {
				return nil, compatibilityError("unknown order null type")
			}
		}
		if raw, ok := m["expression"]; ok && !isNull(raw) {
			o.Expression, er = d.expression(raw)
			if er != nil {
				return nil, er
			}
		} else {
			return nil, malformedError("order expression is required", nil)
		}
		out = append(out, o)
	}
	return out, nil
}
func (d *decoder) orderModifier(raw json.RawMessage) ([]Order, error) {
	m, err := d.object(raw)
	if err != nil {
		return nil, err
	}
	if err = checkGeneratedSchema("modifier", "ORDER_MODIFIER", m); err != nil {
		return nil, err
	}
	if orders, ok := m["orders"]; ok {
		return d.orders(orders)
	}
	return nil, nil
}
func (d *decoder) groupSets(raw json.RawMessage) ([][]int, error) {
	arr, err := rawArray(raw)
	if err != nil {
		return nil, err
	}
	if len(arr) > d.limits.MaxArrayItems {
		return nil, limitError("group set count exceeds parser limit")
	}
	out := make([][]int, 0, len(arr))
	for _, v := range arr {
		row, er := rawArray(v)
		if er != nil {
			return nil, er
		}
		if len(row) > d.limits.MaxArrayItems {
			return nil, limitError("group set width exceeds parser limit")
		}
		vals := make([]int, 0, len(row))
		for _, x := range row {
			i, er := intValue(x)
			if er != nil || i < 0 {
				return nil, malformedError("group set index must be non-negative integer", er)
			}
			vals = append(vals, i)
		}
		out = append(out, vals)
	}
	return out, nil
}
func (d *decoder) value(raw json.RawMessage) (Value, error) {
	if len(raw) == 0 || isNull(raw) {
		return Value{Kind: ValueNull}, nil
	}
	if raw[0] == '{' {
		m, err := d.object(raw)
		if err != nil {
			return Value{}, err
		}
		// Serialized constants carry is_null and/or value alongside their type.
		// Other supporting objects (for example DECIMAL_TYPE_INFO) also have a
		// discriminator named type and must remain typed Value objects rather
		// than being decoded as constants.
		_, hasNullMarker := m["is_null"]
		_, hasConstantValue := m["value"]
		if hasNullMarker || hasConstantValue {
			return d.constantValue(m)
		}
		if len(m) > d.limits.MaxArrayItems {
			return Value{}, limitError("value object field count exceeds parser limit")
		}
		out := Value{Kind: ValueObject}
		for k, v := range m {
			x, er := d.value(v)
			if er != nil {
				return Value{}, er
			}
			out.Object = append(out.Object, Field{Name: k, Value: x})
		}
		return out, nil
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return Value{}, malformedError("invalid value", err)
	}
	switch x := v.(type) {
	case nil:
		return Value{Kind: ValueNull}, nil
	case bool:
		return Value{Kind: ValueBool, Bool: x}, nil
	case string:
		return Value{Kind: ValueString, String: x}, nil
	case json.Number:
		return Value{Kind: ValueNumber, Number: x.String()}, nil
	case []any:
		if len(x) > d.limits.MaxArrayItems {
			return Value{}, limitError("value array item count exceeds parser limit")
		}
		out := Value{Kind: ValueArray}
		for _, e := range x {
			b, _ := json.Marshal(e)
			q, er := d.value(b)
			if er != nil {
				return Value{}, er
			}
			out.Array = append(out.Array, q)
		}
		return out, nil
	default:
		return Value{}, compatibilityError("unsupported DuckDB value")
	}
}
func (d *decoder) constantValue(m map[string]json.RawMessage) (Value, error) {
	if err := checkKeys(m, "type", "is_null", "value"); err != nil {
		return Value{}, err
	}
	raw, ok := m["value"]
	if !ok || isNull(raw) {
		return Value{Kind: ValueNull}, nil
	}
	return d.value(raw)
}
func (d *decoder) logicalTypeValue(raw json.RawMessage) (LogicalType, error) {
	m, err := d.object(raw)
	if err != nil {
		return LogicalType{}, err
	}
	if err = checkKeys(m, "id", "type_modifiers", "type_info"); err != nil {
		return LogicalType{}, err
	}
	id, err := requiredString(m, "id")
	if err != nil {
		return LogicalType{}, err
	}
	if !knownLogicalType(id) {
		return LogicalType{}, compatibilityError("unknown logical type " + id)
	}
	l := LogicalType{ID: id}
	if raw, ok := m["type_modifiers"]; ok {
		arr, e := rawArray(raw)
		if e != nil {
			return LogicalType{}, e
		}
		if len(arr) > d.limits.MaxArrayItems {
			return LogicalType{}, limitError("logical type modifier count exceeds parser limit")
		}
		for _, v := range arr {
			i, e := int64Value(v)
			if e != nil {
				return LogicalType{}, e
			}
			l.Modifiers = append(l.Modifiers, i)
		}
	}
	if raw, ok := m["type_info"]; ok && !isNull(raw) {
		l.Info, err = d.value(raw)
		if err != nil {
			return LogicalType{}, err
		}
	}
	return l, nil
}
func (d *decoder) logicalTypes(raw json.RawMessage) ([]LogicalType, error) {
	arr, err := rawArray(raw)
	if err != nil {
		return nil, err
	}
	if len(arr) > d.limits.MaxArrayItems {
		return nil, limitError("logical type count exceeds parser limit")
	}
	out := make([]LogicalType, 0, len(arr))
	for _, v := range arr {
		x, e := d.logicalTypeValue(v)
		if e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, nil
}

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
func (v Value) objectField(name string) (Value, bool) {
	for _, f := range v.Object {
		if f.Name == name {
			return f.Value, true
		}
	}
	return Value{}, false
}
func (d *decoder) values(raw json.RawMessage) ([]Value, error) {
	a, e := rawArray(raw)
	if e != nil {
		return nil, e
	}
	if len(a) > d.limits.MaxArrayItems {
		return nil, limitError("value count exceeds parser limit")
	}
	out := make([]Value, 0, len(a))
	for _, v := range a {
		x, e := d.value(v)
		if e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, nil
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
