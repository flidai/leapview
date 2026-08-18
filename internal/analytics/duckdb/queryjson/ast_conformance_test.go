package queryjson

import (
	"context"
	"strings"
	"testing"
)

func TestAnalyzeSQLTextPinnedAcceptedFixture(t *testing.T) {
	queries := []string{
		`WITH recent AS (SELECT id, amount FROM source.orders WHERE amount > 0) SELECT id, SUM(amount) AS total FROM recent GROUP BY id`,
		`SELECT CAST(o.purchase_ts AS TIMESTAMP) AS purchase_ts, strftime(CAST(o.purchase_ts AS TIMESTAMP), '%Y-%m') AS purchase_month, COALESCE(round(revenue, 2), CAST(0 AS DECIMAL(38,2))) AS revenue FROM source.orders o`,
		`SELECT CASE WHEN amount > 0 THEN 'positive' ELSE 'zero' END AS bucket, count(*) AS n FROM source.orders GROUP BY bucket`,
	}
	got, err := AnalyzeSQLText(context.Background(), queries[0])
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceRefs == nil || len(got.SourceRefs) != 1 || got.SourceRefs[0] != "orders" {
		t.Fatalf("source refs = %#v", got.SourceRefs)
	}
	if len(got.ModelRefs) != 0 || len(got.CTEs) != 1 || got.CTEs[0] != "recent" {
		t.Fatalf("normalized refs = %#v", got)
	}
	for _, sqlText := range queries[1:] {
		if _, err := AnalyzeSQLText(context.Background(), sqlText); err != nil {
			t.Errorf("accepted fixture %q: %v", sqlText, err)
		}
	}
}

func TestAnalyzeSQLTextRejectsUnsafeAndUnknownConstructs(t *testing.T) {
	for _, sqlText := range []string{
		`SELECT * FROM read_csv('orders.csv')`,
		`SELECT * FROM range(10)`,
		`SELECT query_table('SELECT 1')`,
		`SELECT * FROM raw.orders`,
		`SELECT * FROM main.orders`,
		`SELECT * FROM source.orders UNION ALL SELECT * FROM source.orders`,
	} {
		if _, err := AnalyzeSQLText(context.Background(), sqlText); err == nil {
			t.Errorf("AnalyzeSQLText(%q) succeeded", sqlText)
		}
	}
}

func TestAnalyzeSQLTextScopesCTEsWithoutRecursionOrForwardReferences(t *testing.T) {
	for _, sqlText := range []string{
		`WITH q AS (SELECT * FROM q) SELECT * FROM q`,
		`WITH a AS (SELECT * FROM b), b AS (SELECT 1 AS x) SELECT * FROM a`,
	} {
		if _, err := AnalyzeSQLText(context.Background(), sqlText); err == nil {
			t.Errorf("AnalyzeSQLText(%q) succeeded", sqlText)
		}
	}
	if _, err := AnalyzeSQLText(context.Background(), `WITH q AS (SELECT 1 AS x) SELECT * FROM (WITH q AS (SELECT 2 AS x) SELECT * FROM q)`); err != nil {
		t.Fatalf("nested CTE shadowing rejected: %v", err)
	}
}

func TestAnalyzeSQLRejectsMalformedPinnedAST(t *testing.T) {
	for _, payload := range []string{
		`{"error":false,"statements":[{"node":{"type":"SELECT_NODE","from_table":{"type":"EMPTY"}}}]}`,
		`{"error":false,"statements":[{"node":{"type":"SELECT_NODE","select_list":[],"from_table":{"type":"BASE_TABLE","schema_name":"source","table_name":"orders","unknown":true}}}]}`,
	} {
		if _, err := AnalyzeSQL([]byte(payload)); err == nil || !strings.Contains(err.Error(), "DuckDB SQL AST") && !strings.Contains(err.Error(), "missing") {
			t.Errorf("AnalyzeSQL malformed payload error = %v", err)
		}
	}
}
