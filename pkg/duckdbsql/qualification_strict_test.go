package duckdbsql

import (
	"context"
	"errors"
	"testing"
)

func qualificationDocument(node string) []byte {
	return []byte(`{"error":false,"statements":[{"node":` + node + `}]}`)
}

func qualificationSelectNode(extra string) string {
	return `{"type":"SELECT_NODE","select_list":[],"from_table":{"type":"EMPTY"}` + extra + `}`
}

func qualificationParseErrorKind(t *testing.T, input []byte, limits Limits, want ErrorKind) {
	t.Helper()
	query, err := decodeDocument(input, "SELECT 1", limits)
	if err == nil {
		t.Fatalf("decoder unexpectedly accepted input (query=%#v)", query)
	}
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("error %T does not satisfy ParseError: %v", err, err)
	}
	if parseErr.Kind != want {
		t.Fatalf("error kind = %s, want %s (error=%v)", parseErr.Kind, want, err)
	}
	if len(query.Statements) != 0 {
		t.Fatalf("decoder returned partial query after error: %#v", query)
	}
}

func TestQualificationDecoderStrictVariantAndScalarTable(t *testing.T) {
	cases := []struct {
		name      string
		node      string
		wantKind  ErrorKind
		checkKind bool
	}{
		{
			name:      "statement rejects relation-only field",
			node:      qualificationSelectNode(`,"setop_type":"UNION"`),
			wantKind:  ErrorCompatibility,
			checkKind: true,
		},
		{
			name:      "base relation rejects expression-only field",
			node:      qualificationSelectNode(`,"from_table":{"type":"BASE_TABLE","table_name":"orders","function":{}}`),
			wantKind:  ErrorCompatibility,
			checkKind: true,
		},
		{
			name:      "constant rejects relation-only field",
			node:      qualificationSelectNode(`,"select_list":[{"class":"CONSTANT","type":"VALUE_CONSTANT","value":{"type":{"id":"INTEGER"},"value":1},"children":[]}]`),
			wantKind:  ErrorCompatibility,
			checkKind: true,
		},
		{
			name:      "aggregate handling rejects scalar number",
			node:      qualificationSelectNode(`,"aggregate_handling":7`),
			wantKind:  ErrorCompatibility,
			checkKind: true,
		},
		{
			name:      "query location rejects scalar string",
			node:      qualificationSelectNode(`,"from_table":{"type":"EMPTY","query_location":"oops"}`),
			wantKind:  ErrorMalformed,
			checkKind: true,
		},
		{
			name:      "base relation requires table name",
			node:      qualificationSelectNode(`,"from_table":{"type":"BASE_TABLE"}`),
			wantKind:  ErrorMalformed,
			checkKind: true,
		},
		{
			name:      "function expression requires function name",
			node:      qualificationSelectNode(`,"select_list":[{"class":"FUNCTION","type":"FUNCTION"}]`),
			wantKind:  ErrorMalformed,
			checkKind: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := qualificationDocument(tc.node)
			if tc.checkKind {
				qualificationParseErrorKind(t, input, DefaultLimits(), tc.wantKind)
				return
			}
			if query, err := decodeDocument(input, "SELECT 1", DefaultLimits()); err == nil || len(query.Statements) != 0 {
				t.Fatalf("decoder accepted malformed variant: query=%#v err=%v", query, err)
			}
		})
	}
}

func TestQualificationDecoderRejectsTrailingJSON(t *testing.T) {
	seed := qualificationDocument(qualificationSelectNode(""))
	for _, suffix := range []string{" {}", " 42"} {
		t.Run(suffix, func(t *testing.T) {
			input := append(append([]byte(nil), seed...), []byte(suffix)...)
			qualificationParseErrorKind(t, input, DefaultLimits(), ErrorMalformed)
		})
	}
}

func TestQualificationDecoderBoundsSupportingArrays(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxArrayItems = 1
	cases := []struct {
		name  string
		input []byte
	}{
		{
			name:  "modifier array",
			input: qualificationDocument(qualificationSelectNode(`,"modifiers":[{"type":"DISTINCT_MODIFIER"},{"type":"DISTINCT_MODIFIER"}]`)),
		},
		{
			name:  "expression-list rows",
			input: qualificationDocument(qualificationSelectNode(`,"from_table":{"type":"EXPRESSION_LIST","values":[[],[]]}`)),
		},
		{
			name:  "pivot columns",
			input: qualificationDocument(qualificationSelectNode(`,"from_table":{"type":"PIVOT","pivots":[{},{}]}`)),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			qualificationParseErrorKind(t, tc.input, limits, ErrorLimit)
		})
	}
}

func TestQualificationDecoderIntegerBoundsAndDiagnostics(t *testing.T) {
	tooLarge := qualificationDocument(qualificationSelectNode(`,"from_table":{"type":"EMPTY","query_location":9223372036854775808}`))
	qualificationParseErrorKind(t, tooLarge, DefaultLimits(), ErrorMalformed)
	tooLargePosition := []byte(`{"error":true,"error_message":"bad syntax","error_type":"parser","error_subtype":"token","error_position":9223372036854775808}`)
	qualificationParseErrorKind(t, tooLargePosition, DefaultLimits(), ErrorMalformed)

	diagnostic := []byte(`{"error":true,"error_message":"bad syntax","error_type":"parser","error_subtype":"token","error_position":17,"diagnostic_context":{"line":2},"future_diagnostic_field":"ignored"}`)
	query, err := decodeDocument(diagnostic, "SELECT 1", DefaultLimits())
	if err == nil || len(query.Statements) != 0 {
		t.Fatalf("diagnostic payload unexpectedly accepted: query=%#v err=%v", query, err)
	}
	var parseErr *ParseError
	if !errors.As(err, &parseErr) || parseErr.Kind != ErrorSyntax {
		t.Fatalf("diagnostic error = %#v", err)
	}
	if parseErr.Message != "bad syntax" || parseErr.DuckDBType != "parser" || parseErr.DuckDBSubtype != "token" || parseErr.BytePosition != 17 {
		t.Fatalf("diagnostic fields were not preserved: %#v", parseErr)
	}
}

func TestQualificationDecoderCTENodeWalk(t *testing.T) {
	input := qualificationDocument(`{"type":"CTE_NODE","cte_name":"wrapped","query":{"node":{"type":"SELECT_NODE","select_list":[],"from_table":{"type":"EMPTY"}}},"child":{"type":"SELECT_NODE","select_list":[],"from_table":{"type":"EMPTY"}}}`)
	query, err := decodeDocument(input, "SELECT 1", DefaultLimits())
	if err != nil || len(query.Statements) != 1 {
		t.Fatalf("CTE_NODE decode failed: query=%#v err=%v", query, err)
	}
	node, ok := query.Statements[0].(*CTENodeStatement)
	if !ok || node.Name != "wrapped" || node.Query == nil || node.Child == nil {
		t.Fatalf("decoded CTE_NODE = %#v", query.Statements[0])
	}
	statements := 0
	if err := Walk(query, WalkCallbacks{Statement: func(Statement) error { statements++; return nil }}); err != nil {
		t.Fatal(err)
	}
	if statements != 3 {
		t.Fatalf("Walk visited %d statements, want CTE wrapper plus query and child", statements)
	}
}

func TestPinnedDuckDBModifierAST(t *testing.T) {
	query, err := Parse(context.Background(), `SELECT DISTINCT ON (id) id FROM tbl LIMIT 10%`)
	if err != nil || len(query.Statements) != 1 {
		t.Fatalf("pinned modifier query failed: query=%#v err=%v", query, err)
	}
	selectNode, ok := query.Statements[0].(*SelectStatement)
	if !ok {
		t.Fatalf("statement = %T, want *SelectStatement", query.Statements[0])
	}
	var distinct, percent bool
	for _, modifier := range selectNode.Modifiers {
		switch modifier := modifier.(type) {
		case *DistinctModifier:
			distinct = len(modifier.DistinctOnTargets) == 1
		case *LimitPercentModifier:
			percent = modifier.Limit != nil
		}
	}
	if !distinct || !percent {
		t.Fatalf("modifiers = %#v, want DISTINCT ON and LIMIT_PERCENT", selectNode.Modifiers)
	}
}
