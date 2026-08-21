package duckdbsql

import (
	"encoding/json"
)

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
