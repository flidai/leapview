package duckdbsql

import (
	"encoding/json"
)

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
