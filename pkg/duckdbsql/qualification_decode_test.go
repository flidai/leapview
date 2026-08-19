package duckdbsql

import (
	"errors"
	"testing"
)

// This is deliberately a tiny hand-written serializer payload. Qualification
// mutates it in memory rather than storing upstream JSON, so the corpus remains
// provenance metadata plus SQL examples and never becomes a second AST ABI.
const qualificationASTSeed = `{"error":false,"statements":[{"node":{"type":"SELECT_NODE","select_list":[{"class":"CONSTANT","type":"VALUE_CONSTANT","value":{"type":{"id":"INTEGER"},"is_null":false,"value":1}}],"from_table":{"type":"EMPTY"}}}]}`

func TestQualificationDecoderMutations(t *testing.T) {
	mutations := map[string]string{
		"unknown root field":       `{"error":false,"future":true,"statements":[]}`,
		"unknown statement field":  `{"error":false,"statements":[{"node":{"type":"SELECT_NODE","future":true,"select_list":[],"from_table":{"type":"EMPTY"}}}]}`,
		"unknown expression field": `{"error":false,"statements":[{"node":{"type":"SELECT_NODE","select_list":[{"class":"CONSTANT","type":"VALUE_CONSTANT","future":true,"value":{"type":{"id":"INTEGER"},"is_null":false,"value":1}}],"from_table":{"type":"EMPTY"}}}]}`,
		"missing root statements":  `{"error":false}`,
		"missing statement node":   `{"error":false,"statements":[{}]}`,
		"missing select list":      `{"error":false,"statements":[{"node":{"type":"SELECT_NODE","from_table":{"type":"EMPTY"}}}]}`,
		"wrong child shape":        `{"error":false,"statements":[{"node":{"type":"SELECT_NODE","select_list":{},"from_table":{"type":"EMPTY"}}}]}`,
		"truncated JSON":           `{"error":false,"statements":[`,
		"unknown node type":        `{"error":false,"statements":[{"node":{"type":"FUTURE_QUERY_NODE"}}]}`,
	}
	if query, err := decodeDocument([]byte(qualificationASTSeed), "SELECT 1", DefaultLimits()); err != nil || len(query.Statements) != 1 {
		t.Fatalf("seed no longer decodes: query=%#v err=%v", query, err)
	}
	for name, input := range mutations {
		name, input := name, input
		t.Run(name, func(t *testing.T) {
			query, err := decodeDocument([]byte(input), "SELECT 1", DefaultLimits())
			if err == nil {
				t.Fatal("mutation unexpectedly decoded")
			}
			if len(query.Statements) != 0 {
				t.Fatalf("decoder returned partial query after error: %#v", query)
			}
			var parseErr *ParseError
			if !errors.As(err, &parseErr) {
				t.Fatalf("decoder error %T does not satisfy ParseError: %v", err, err)
			}
		})
	}
}

func FuzzDecodeDocumentQualification(f *testing.F) {
	f.Add([]byte(qualificationASTSeed))
	f.Add([]byte(`{"error":true,"error_message":"syntax error","error_type":"parser"}`))
	f.Add([]byte(`{"error":false,"statements":[]}`))
	f.Add([]byte(`{"error":false,"statements":[{"node":{"type":"FUTURE_QUERY_NODE"}}]}`))
	f.Fuzz(func(t *testing.T, input []byte) {
		query, err := decodeDocument(input, "SELECT 1", DefaultLimits())
		if err != nil && len(query.Statements) != 0 {
			t.Fatalf("decoder returned partial query after error: %#v", query)
		}
	})
}
