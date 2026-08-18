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
		`SELECT lpad(CAST(id AS VARCHAR), 4, '0') AS padded_id FROM source.orders`,
		`SELECT o.id FROM source.orders o JOIN source.payments p USING (id)`,
		`WITH sellers AS (SELECT DISTINCT order_id, seller_id FROM source."olist.order_items"), allocated AS (SELECT order_id, count(*) OVER (PARTITION BY order_id) AS n FROM sellers) SELECT printf('%05d', try_cast(n AS INTEGER)) AS label FROM allocated`,
		`SELECT id FROM source.orders WHERE a IS NOT NULL AND b IS NOT NULL`,
		`SELECT id FROM source.orders UNION ALL SELECT id FROM source.payments`,
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
		`SELECT foo.trim(name) FROM source.orders`,
		`SELECT * FROM raw.orders`,
		`SELECT * FROM main.orders`,
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
	for _, test := range []struct {
		payload string
		want    string
	}{
		{`{"error":false,"statements":[{"node":{"type":"SELECT_NODE","from_table":{"type":"EMPTY"}}}]}`, "missing"},
		{`{"error":false,"statements":[{"node":{"type":"SELECT_NODE","select_list":[],"from_table":{"type":"BASE_TABLE","schema_name":"source","table_name":"orders","unknown":true}}}]}`, "unknown DuckDB SQL AST field"},
		{`{"error":false,"statements":[{"node":{"type":"SELECT_NODE","select_list":[],"from_table":{"type":"JOIN","left":{"type":"EMPTY"},"right":{"type":"EMPTY"},"using_columns":[1]}}}]}`, "using_columns"},
		{`{"error":false,"statements":[{"node":{"type":"SET_OPERATION_NODE","setop_type":"EXCEPT","left":{"type":"SELECT_NODE","select_list":[],"from_table":{"type":"EMPTY"}},"right":{"type":"SELECT_NODE","select_list":[],"from_table":{"type":"EMPTY"}}}}]}`, "set operation"},
	} {
		if _, err := AnalyzeSQL([]byte(test.payload)); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("AnalyzeSQL malformed payload error = %v", err)
		}
	}
}
