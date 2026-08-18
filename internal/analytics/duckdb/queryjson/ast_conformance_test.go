package queryjson

import (
	"context"
	"fmt"
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

func TestAnalyzeSQLRejectsClosedEnumAndTypeMutations(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "star type",
			payload: `{"error":false,"statements":[{"node":{"type":"SELECT_NODE","select_list":[{"class":"STAR","type":"CONSTANT"}],"from_table":{"type":"EMPTY"}}}]}`,
			want:    "does not allow type",
		},
		{
			name:    "comparison enum",
			payload: `{"error":false,"statements":[{"node":{"type":"SELECT_NODE","select_list":[],"from_table":{"type":"EMPTY"},"where_clause":{"class":"COMPARISON","type":"COMPARE_FUTURE","left":{"class":"CONSTANT","type":"VALUE_CONSTANT","value":{}},"right":{"class":"CONSTANT","type":"VALUE_CONSTANT","value":{}}}}}]}`,
			want:    "does not allow type",
		},
		{
			name:    "join enum",
			payload: `{"error":false,"statements":[{"node":{"type":"SELECT_NODE","select_list":[],"from_table":{"type":"JOIN","left":{"type":"EMPTY"},"right":{"type":"EMPTY"},"join_type":"FUTURE","ref_type":"REGULAR"}}}]}`,
			want:    "join_type",
		},
		{
			name:    "function children type",
			payload: `{"error":false,"statements":[{"node":{"type":"SELECT_NODE","select_list":[{"class":"FUNCTION","type":"FUNCTION","function_name":"count_star","children":{}}],"from_table":{"type":"EMPTY"}}}]}`,
			want:    "children must be an array",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := AnalyzeSQL([]byte(test.payload)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("AnalyzeSQL mutation error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestAnalyzeSQLRejectsAcceptedKeyWrongTypesAndOpaquePayloads(t *testing.T) {
	const root = `{"error":false,"statements":[{"node":%s}]}`
	const selectBase = `{"type":"SELECT_NODE","select_list":[],"from_table":{"type":"EMPTY"}}`
	mutations := []struct {
		name string
		node string
		want string
	}{
		{"aggregate handling", `{"type":"SELECT_NODE","select_list":[],"from_table":{"type":"EMPTY"},"aggregate_handling":1}`, "aggregate_handling"},
		{"group set index", `{"type":"SELECT_NODE","select_list":[{"class":"COLUMN_REF","type":"COLUMN_REF","column_names":["id"]}],"from_table":{"type":"EMPTY"},"group_sets":[[-1]]}`, "group_sets"},
		{"sample", `{"type":"SELECT_NODE","select_list":[],"from_table":{"type":"EMPTY"},"sample":{}}`, "samples"},
		{"window definition name", `{"type":"SELECT_NODE","select_list":[],"from_table":{"type":"EMPTY"},"window_definitions":[{"name":1,"expression":{"class":"STAR","type":"STAR"}}]}`, "window definition name"},
		{"window definition expression", `{"type":"SELECT_NODE","select_list":[],"from_table":{"type":"EMPTY"},"window_definitions":[{"name":"w"}]}`, "window definition expression"},
		{"base table alias", `{"type":"SELECT_NODE","select_list":[],"from_table":{"type":"BASE_TABLE","table_name":"orders","alias":1}}`, "BASE_TABLE alias"},
		{"base table aliases", `{"type":"SELECT_NODE","select_list":[],"from_table":{"type":"BASE_TABLE","table_name":"orders","column_name_alias":["bad-name"]}}`, "column_name_alias"},
		{"order modifier orders", `{"type":"SELECT_NODE","select_list":[{"class":"FUNCTION","type":"FUNCTION","function_name":"count_star","order_bys":{"type":"ORDER_MODIFIER","orders":{}}}],"from_table":{"type":"EMPTY"}}`, "ORDER modifier orders"},
		{"cast type extra", `{"type":"SELECT_NODE","select_list":[{"class":"CAST","type":"OPERATOR_CAST","child":{"class":"CONSTANT","type":"VALUE_CONSTANT","value":1},"cast_type":{"id":"INTEGER","unknown":true}}],"from_table":{"type":"EMPTY"}}`, "unknown DuckDB SQL AST field"},
		{"constant value extra", `{"type":"SELECT_NODE","select_list":[{"class":"CONSTANT","type":"VALUE_CONSTANT","value":{"type":{"id":"INTEGER"},"is_null":false,"value":1,"unknown":true}}],"from_table":{"type":"EMPTY"}}`, "unknown DuckDB SQL AST field"},
		{"constant value type", `{"type":"SELECT_NODE","select_list":[{"class":"CONSTANT","type":"VALUE_CONSTANT","value":{"type":1,"is_null":false,"value":1}}],"from_table":{"type":"EMPTY"}}`, "CONSTANT value type"},
		{"column names type", `{"type":"SELECT_NODE","select_list":[{"class":"COLUMN_REF","type":"COLUMN_REF","column_names":[1]}],"from_table":{"type":"EMPTY"}}`, "column reference names"},
		{"column namespace", `{"type":"SELECT_NODE","select_list":[{"class":"COLUMN_REF","type":"COLUMN_REF","column_names":["raw","orders","id"]}],"from_table":{"type":"EMPTY"}}`, "namespace"},
		{"cast width fractional", `{"type":"SELECT_NODE","select_list":[{"class":"CAST","type":"OPERATOR_CAST","child":{"class":"CONSTANT","type":"VALUE_CONSTANT","value":{"type":{"id":"INTEGER"},"is_null":false,"value":1}},"cast_type":{"id":"DECIMAL","type_info":{"type":"DECIMAL_TYPE_INFO","width":1.5,"scale":2}}}],"from_table":{"type":"EMPTY"}}`, "bounded integer"},
		{"window boundary", `{"type":"SELECT_NODE","select_list":[{"class":"WINDOW","type":"WINDOW_AGGREGATE","function_name":"count","partitions":[],"orders":[],"children":[],"start":1,"end":"CURRENT_ROW_RANGE"}],"from_table":{"type":"EMPTY"}}`, "WINDOW start"},
		{"pivot relation", `{"type":"SELECT_NODE","select_list":[],"from_table":{"type":"PIVOT","source":{"type":"EMPTY"},"aggregates":[],"pivots":[]}}`, "PIVOT relations"},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			payload := []byte(fmt.Sprintf(root, mutation.node))
			if _, err := AnalyzeSQL(payload); err == nil || !strings.Contains(err.Error(), mutation.want) {
				t.Fatalf("AnalyzeSQL mutation error = %v, want %q", err, mutation.want)
			}
		})
	}
	if _, err := AnalyzeSQL([]byte(fmt.Sprintf(root, selectBase))); err != nil {
		t.Fatalf("control AST rejected: %v", err)
	}
}
