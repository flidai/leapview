package duckdbsql

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	maxPlanPayloadBytes = 8 << 20
	maxPlanDepth        = 256
)

// Querier is the narrow database surface needed to obtain a DuckDB JSON
// explain plan. *sql.DB and *sql.Tx both implement it. The package never
// exposes the raw plan payload to callers.
type Querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// Plan is the stable, generic subset of an EXPLAIN (FORMAT json) plan exposed
// by duckdbsql. Plan analysis is evidence about physical scans; it is not a
// replacement for query-AST analysis or an application authorization policy.
type Plan struct {
	Scans []Scan
}

// Scan describes one physical table scan in plan traversal order.
type Scan struct {
	Operator    string
	Catalog     string
	Schema      string
	Table       string
	Projections []string
}

// AnalyzePlan asks DuckDB for a JSON explain plan and returns stable typed
// scan evidence. DuckDB remains responsible for planning and binding; this
// function only decodes the plan representation needed by generic callers.
func AnalyzePlan(ctx context.Context, querier Querier, sqlText string) (Plan, error) {
	if querier == nil {
		return Plan{}, fmt.Errorf("duckdbsql: nil plan querier")
	}
	if strings.TrimSpace(sqlText) == "" {
		return Plan{}, fmt.Errorf("duckdbsql: empty SQL cannot be explained")
	}
	query, err := Parse(ctx, sqlText)
	if err != nil {
		return Plan{}, fmt.Errorf("duckdbsql: validating SQL before EXPLAIN: %w", err)
	}
	if len(query.Statements) != 1 {
		return Plan{}, unsupportedError("EXPLAIN requires exactly one query statement", "multiple_statements")
	}
	switch query.Statements[0].(type) {
	case *SelectStatement, *SetOperationStatement:
		// Both are query statements represented by DuckDB's serialized query
		// protocol. Binding and physical planning remain DuckDB's authority.
	default:
		return Plan{}, unsupportedError("EXPLAIN requires a query statement", "non_query_statement")
	}
	rows, err := querier.QueryContext(ctx, "EXPLAIN (FORMAT json) "+sqlText)
	if err != nil {
		return Plan{}, err
	}
	if rows == nil {
		return Plan{}, fmt.Errorf("duckdbsql: plan querier returned nil rows")
	}
	payload, err := readPlanPayload(rows)
	if err != nil {
		return Plan{}, err
	}
	return decodePlan(payload)
}

func readPlanPayload(rows *sql.Rows) (string, error) {
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return "", fmt.Errorf("duckdbsql: reading plan columns: %w", err)
	}
	if len(columns) == 0 {
		return "", fmt.Errorf("duckdbsql: plan query returned no columns")
	}
	columnIndex := -1
	for index, column := range columns {
		if strings.EqualFold(column, "explain_value") {
			columnIndex = index
			break
		}
	}
	if columnIndex < 0 {
		if len(columns) == 1 {
			columnIndex = 0
		} else {
			return "", fmt.Errorf("duckdbsql: plan query has no explain_value column")
		}
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", fmt.Errorf("duckdbsql: reading plan row: %w", err)
		}
		return "", fmt.Errorf("duckdbsql: plan query returned no rows")
	}
	values := make([]sql.NullString, len(columns))
	destinations := make([]any, len(values))
	for index := range values {
		destinations[index] = &values[index]
	}
	if err := rows.Scan(destinations...); err != nil {
		return "", fmt.Errorf("duckdbsql: scanning plan row: %w", err)
	}
	if rows.Next() {
		return "", fmt.Errorf("duckdbsql: plan query returned multiple rows")
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("duckdbsql: reading plan rows: %w", err)
	}
	if !values[columnIndex].Valid {
		return "", fmt.Errorf("duckdbsql: plan query returned a NULL explain payload")
	}
	if err := rows.Close(); err != nil {
		return "", fmt.Errorf("duckdbsql: closing plan rows: %w", err)
	}
	return values[columnIndex].String, nil
}

// rawPlanNode is deliberately private. DuckDB's JSON shape is an ephemeral,
// version-coupled input protocol; callers receive only Plan and Scan.
type rawPlanNode struct {
	Name      string
	Children  []rawPlanNode
	ExtraInfo map[string]json.RawMessage
}

func decodePlan(payload string) (Plan, error) {
	if len(payload) == 0 {
		return Plan{}, fmt.Errorf("duckdbsql: empty plan payload")
	}
	if len(payload) > maxPlanPayloadBytes {
		return Plan{}, fmt.Errorf("duckdbsql: plan payload exceeds %d bytes", maxPlanPayloadBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(payload)))
	var roots []json.RawMessage
	if err := decoder.Decode(&roots); err != nil {
		return Plan{}, fmt.Errorf("duckdbsql: decoding plan JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return Plan{}, fmt.Errorf("duckdbsql: plan JSON contains trailing values")
	} else if !errors.Is(err, io.EOF) {
		return Plan{}, fmt.Errorf("duckdbsql: decoding trailing plan JSON: %w", err)
	}
	if len(roots) == 0 {
		return Plan{}, fmt.Errorf("duckdbsql: plan JSON contains no roots")
	}
	plan := Plan{Scans: []Scan{}}
	for index, root := range roots {
		node, err := decodePlanNode(root, 0)
		if err != nil {
			return Plan{}, fmt.Errorf("duckdbsql: decoding plan root %d: %w", index, err)
		}
		if err := collectScans(node, &plan); err != nil {
			return Plan{}, fmt.Errorf("duckdbsql: decoding plan root %d: %w", index, err)
		}
	}
	return plan, nil
}

func decodePlanNode(payload json.RawMessage, depth int) (rawPlanNode, error) {
	if depth > maxPlanDepth {
		return rawPlanNode{}, fmt.Errorf("plan nesting exceeds %d levels", maxPlanDepth)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		return rawPlanNode{}, fmt.Errorf("node is not an object: %w", err)
	}
	if object == nil {
		return rawPlanNode{}, fmt.Errorf("node is null")
	}
	for key := range object {
		if key != "name" && key != "children" && key != "extra_info" {
			return rawPlanNode{}, fmt.Errorf("unknown node field %q", key)
		}
	}
	namePayload, ok := object["name"]
	if !ok {
		return rawPlanNode{}, fmt.Errorf("node is missing name")
	}
	var name string
	if err := json.Unmarshal(namePayload, &name); err != nil || strings.TrimSpace(name) == "" {
		return rawPlanNode{}, fmt.Errorf("node name must be a non-empty string")
	}
	node := rawPlanNode{Name: name}
	if childrenPayload, ok := object["children"]; ok {
		if string(bytes.TrimSpace(childrenPayload)) == "null" {
			return rawPlanNode{}, fmt.Errorf("children must be an array, not null")
		}
		if err := json.Unmarshal(childrenPayload, &node.Children); err != nil {
			return rawPlanNode{}, fmt.Errorf("children must be an array: %w", err)
		}
		var childPayloads []json.RawMessage
		if err := json.Unmarshal(childrenPayload, &childPayloads); err != nil {
			return rawPlanNode{}, fmt.Errorf("children must be an array: %w", err)
		}
		node.Children = make([]rawPlanNode, len(childPayloads))
		for index, childPayload := range childPayloads {
			child, err := decodePlanNode(childPayload, depth+1)
			if err != nil {
				return rawPlanNode{}, fmt.Errorf("child %d: %w", index, err)
			}
			node.Children[index] = child
		}
	}
	if extraPayload, ok := object["extra_info"]; ok {
		if err := json.Unmarshal(extraPayload, &node.ExtraInfo); err != nil {
			return rawPlanNode{}, fmt.Errorf("extra_info must be an object: %w", err)
		}
		if node.ExtraInfo == nil {
			return rawPlanNode{}, fmt.Errorf("extra_info must not be null")
		}
	}
	return node, nil
}

func collectScans(node rawPlanNode, plan *Plan) error {
	if tablePayload, ok := node.ExtraInfo["Table"]; ok {
		if string(bytes.TrimSpace(tablePayload)) == "null" {
			return fmt.Errorf("scan Table must be a string, not null")
		}
		var tableText string
		if err := json.Unmarshal(tablePayload, &tableText); err != nil {
			return fmt.Errorf("scan Table must be a string: %w", err)
		}
		if tableText != "" {
			catalog, schema, table, err := splitPlanTableName(tableText)
			if err != nil {
				return fmt.Errorf("scan Table %q: %w", tableText, err)
			}
			projections, err := decodePlanProjections(node.ExtraInfo["Projections"])
			if err != nil {
				return fmt.Errorf("scan Projections: %w", err)
			}
			plan.Scans = append(plan.Scans, Scan{Operator: node.Name, Catalog: catalog, Schema: schema, Table: table, Projections: projections})
		}
	}
	for _, child := range node.Children {
		if err := collectScans(child, plan); err != nil {
			return err
		}
	}
	return nil
}

func decodePlanProjections(payload json.RawMessage) ([]string, error) {
	if len(payload) == 0 || string(payload) == "null" {
		return nil, nil
	}
	if payload[0] == '[' {
		var rawItems []json.RawMessage
		if err := json.Unmarshal(payload, &rawItems); err != nil {
			return nil, fmt.Errorf("Projections must be a string or string array")
		}
		result := make([]string, 0, len(rawItems))
		for index, rawItem := range rawItems {
			if string(bytes.TrimSpace(rawItem)) == "null" {
				return nil, fmt.Errorf("Projections[%d] must be a string", index)
			}
			var item string
			if err := json.Unmarshal(rawItem, &item); err != nil {
				return nil, fmt.Errorf("Projections[%d] must be a string", index)
			}
			if item = strings.TrimSpace(item); item != "" {
				result = append(result, item)
			}
		}
		return result, nil
	}
	var text string
	if err := json.Unmarshal(payload, &text); err != nil {
		return nil, fmt.Errorf("Projections must be a string or string array")
	}
	result := []string{}
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			result = append(result, line)
		}
	}
	return result, nil
}

func splitPlanTableName(value string) (catalog, schema, table string, err error) {
	parts := make([]string, 0, 3)
	for index := 0; index < len(value); {
		for index < len(value) && (value[index] == ' ' || value[index] == '\t' || value[index] == '\n' || value[index] == '\r') {
			index++
		}
		if index >= len(value) {
			break
		}
		var part strings.Builder
		if value[index] == '"' {
			index++
			closed := false
			for index < len(value) {
				if value[index] == '"' {
					if index+1 < len(value) && value[index+1] == '"' {
						part.WriteByte('"')
						index += 2
						continue
					}
					index++
					closed = true
					break
				}
				part.WriteByte(value[index])
				index++
			}
			if !closed {
				return "", "", "", fmt.Errorf("unterminated quoted table name")
			}
		} else {
			start := index
			for index < len(value) && value[index] != '.' {
				index++
			}
			part.WriteString(strings.TrimSpace(value[start:index]))
		}
		if part.Len() == 0 {
			return "", "", "", fmt.Errorf("empty table-name component")
		}
		parts = append(parts, part.String())
		for index < len(value) && (value[index] == ' ' || value[index] == '\t' || value[index] == '\n' || value[index] == '\r') {
			index++
		}
		if index >= len(value) {
			break
		}
		if value[index] != '.' {
			return "", "", "", fmt.Errorf("table name has unexpected character %q", value[index])
		}
		index++
	}
	if len(parts) == 0 {
		return "", "", "", fmt.Errorf("empty table name")
	}
	switch len(parts) {
	case 1:
		table = parts[0]
	case 2:
		schema, table = parts[0], parts[1]
	default:
		catalog, schema, table = parts[len(parts)-3], parts[len(parts)-2], parts[len(parts)-1]
	}
	return catalog, schema, table, nil
}
