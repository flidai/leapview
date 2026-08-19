package duckdbsql

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseExtractsRelationsFunctionsColumnsAndCTEs(t *testing.T) {
	query, err := Parse(context.Background(), `WITH recent AS (SELECT id, amount FROM source.orders) SELECT sum(amount) FROM recent`)
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := Analyze(query)
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.CTEs) != 1 || analysis.CTEs[0].Name != "recent" {
		t.Fatalf("CTEs = %#v", analysis.CTEs)
	}
	if len(analysis.Relations) != 2 || analysis.Relations[0].Name != "orders" || !analysis.Relations[1].CTE {
		t.Fatalf("relations = %#v", analysis.Relations)
	}
	if len(analysis.Functions) != 1 || analysis.Functions[0].Name != "sum" {
		t.Fatalf("functions = %#v", analysis.Functions)
	}
	if len(analysis.Columns) < 2 || analysis.Columns[0].Names[0] != "id" {
		t.Fatalf("columns = %#v", analysis.Columns)
	}
}

func TestParsePreservesSyntaxDiagnostics(t *testing.T) {
	_, err := Parse(context.Background(), "SELECT (")
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("error = %T %v", err, err)
	}
	if parseErr.Kind != ErrorSyntax || parseErr.DuckDBType != "parser" || parseErr.DuckDBSubtype != "SYNTAX_ERROR" || parseErr.BytePosition != 8 || parseErr.Message == "" {
		t.Fatalf("diagnostic = %#v", parseErr)
	}
}

func TestParseRejectsUnsupportedStatementClass(t *testing.T) {
	_, err := Parse(context.Background(), "CREATE TABLE x(i INTEGER)")
	var parseErr *ParseError
	if !errors.As(err, &parseErr) || parseErr.Kind != ErrorUnsupportedStatement {
		t.Fatalf("error = %#v", err)
	}
}

func TestParserIsolationAndLimits(t *testing.T) {
	p, err := New(Limits{MaxSQLBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Parse(context.Background(), strings.Repeat("x", 9))
	var parseErr *ParseError
	if !errors.As(err, &parseErr) || parseErr.Kind != ErrorLimit {
		t.Fatalf("error = %#v", err)
	}
}

func TestParsePreservesNamedParameterMap(t *testing.T) {
	query, err := Parse(context.Background(), `SELECT $account`)
	if err != nil {
		t.Fatal(err)
	}
	if len(query.Statements) != 1 {
		t.Fatalf("statements = %#v", query.Statements)
	}
	meta := query.Statements[0].Meta()
	if len(meta.NamedParameters) != 1 || meta.NamedParameters[0].Name != "account" || meta.NamedParameters[0].Index != 1 {
		t.Fatalf("named parameters = %#v", meta.NamedParameters)
	}
}

func TestParsePreservesRelationSamplingOptions(t *testing.T) {
	query, err := Parse(context.Background(), `SELECT * FROM tbl USING SAMPLE 10% (reservoir, 377)`)
	if err != nil {
		t.Fatal(err)
	}
	from, ok := query.Statements[0].(*SelectStatement)
	if !ok {
		t.Fatalf("statement = %T", query.Statements[0])
	}
	if !from.HasSample || from.Sample.Kind != ValueObject {
		t.Fatalf("sample select = %#v", from.Sample)
	}
}
