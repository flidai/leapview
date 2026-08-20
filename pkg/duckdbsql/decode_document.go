package duckdbsql

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
