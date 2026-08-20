package duckdbsql

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestDecodeRejectsUnknownSuccessfulASTFields(t *testing.T) {
	input := []byte(`{"error":false,"statements":[{"node":{"type":"SELECT_NODE","select_list":[],"from_table":{"type":"EMPTY","future":true}}}]}`)
	_, err := decodeDocument(input, "SELECT 1", DefaultLimits())
	var parseErr *ParseError
	if !errors.As(err, &parseErr) || parseErr.Kind != ErrorCompatibility {
		t.Fatalf("error = %#v", err)
	}
}

func TestDecodeRejectsDeepJSONWithinBound(t *testing.T) {
	input := []byte(`{"error":false,"statements":[]}`)
	if _, err := decodeDocument(input, "", Limits{MaxDepth: 1}); err == nil {
		t.Fatal("decode unexpectedly succeeded")
	}
}

func TestDecodePreservesStatementOrder(t *testing.T) {
	input := []byte(`{"error":false,"statements":[{"node":{"type":"SELECT_NODE","select_list":[{"class":"CONSTANT","type":"VALUE_CONSTANT","value":{"type":{"id":"INTEGER"},"is_null":false,"value":1}}],"from_table":{"type":"EMPTY"}}},{"node":{"type":"SELECT_NODE","select_list":[{"class":"CONSTANT","type":"VALUE_CONSTANT","value":{"type":{"id":"INTEGER"},"is_null":false,"value":2}}],"from_table":{"type":"EMPTY"}}}]}`)
	query, err := decodeDocument(input, "SELECT 1; SELECT 2", DefaultLimits())
	if err != nil || len(query.Statements) != 2 {
		t.Fatalf("query=%#v err=%v", query, err)
	}
}

func TestDecodeRejectsOutOfRangeGroupSetIndex(t *testing.T) {
	input := []byte(`{"error":false,"statements":[{"node":{"type":"SELECT_NODE","select_list":[],"from_table":{"type":"EMPTY"},"group_expressions":[],"group_sets":[[0]]}}]}`)
	_, err := decodeDocument(input, "SELECT 1", DefaultLimits())
	var parseErr *ParseError
	if !errors.As(err, &parseErr) || parseErr.Kind != ErrorCompatibility {
		t.Fatalf("error = %#v", err)
	}
}

func TestDecodeRejectsUnknownOrderEnum(t *testing.T) {
	d := decoder{source: "SELECT 1", limits: DefaultLimits()}
	_, err := d.orders(json.RawMessage(`[{"type":"FUTURE","null_order":"ORDER_DEFAULT","expression":{"class":"CONSTANT","type":"VALUE_CONSTANT","value":1}}]`))
	var parseErr *ParseError
	if !errors.As(err, &parseErr) || parseErr.Kind != ErrorCompatibility {
		t.Fatalf("error = %#v", err)
	}
}

func TestClosedSupportingEnumsRejectUnknownValues(t *testing.T) {
	if validCTEMaterialized("FUTURE") || validWindowBoundary("FUTURE") || validWindowExclude("FUTURE") {
		t.Fatal("unknown supporting enum was accepted")
	}
}

func TestDecodeRejectsMalformedOptionalSecurityMetadata(t *testing.T) {
	d := decoder{source: "SELECT 1", limits: DefaultLimits()}
	for _, expression := range []string{
		`{"class":"FUNCTION","type":"FUNCTION","function_name":"trim","catalog":7}`,
		`{"class":"STAR","type":"STAR","columns":"yes"}`,
		`{"class":"WINDOW","type":"WINDOW_ROW_NUMBER","function_name":"row_number","start":7}`,
	} {
		_, err := d.expression(json.RawMessage(expression))
		var parseErr *ParseError
		if !errors.As(err, &parseErr) || parseErr.Kind != ErrorMalformed {
			t.Fatalf("expression %s error = %#v", expression, err)
		}
	}
}
