package duckdbsql

import (
	"encoding/json"
)

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
