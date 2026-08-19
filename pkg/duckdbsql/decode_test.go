package duckdbsql

import (
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
