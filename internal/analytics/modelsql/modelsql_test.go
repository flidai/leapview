package modelsql

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/pkg/duckdbsql"
)

func TestAnalyzeAcceptsReviewedModelSQLAndDerivesDependencies(t *testing.T) {
	queries := []string{
		`WITH recent AS (SELECT id, amount FROM source.orders WHERE amount > 0) SELECT id, SUM(amount) AS total FROM recent GROUP BY id`,
		`SELECT CAST(o.purchase_ts AS TIMESTAMP) AS purchase_ts, strftime(CAST(o.purchase_ts AS TIMESTAMP), '%Y-%m') AS purchase_month, COALESCE(round(revenue, 2), CAST(0 AS DECIMAL(38,2))) AS revenue FROM source.orders o`,
		`SELECT CASE WHEN amount > 0 THEN 'positive' ELSE 'zero' END AS bucket, count(*) AS n FROM source.orders GROUP BY bucket`,
		`SELECT lpad(CAST(id AS VARCHAR), 4, '0') AS padded_id FROM source.orders`,
		`SELECT o.id FROM source.orders o JOIN source.payments p USING (id)`,
		`WITH sellers AS (SELECT DISTINCT order_id, seller_id FROM source."olist.order_items"), allocated AS (SELECT order_id, count(*) OVER (PARTITION BY order_id) AS n FROM sellers) SELECT printf('%05d', try_cast(n AS INTEGER)) AS label FROM allocated`,
		`SELECT id FROM source.orders WHERE a IS NOT NULL AND b IS NOT NULL ORDER BY id LIMIT 10`,
		`SELECT -amount AS negative_amount, amount + 1 AS incremented, CAST(amount AS VARCHAR) || '' AS amount_text FROM source.orders`,
		`SELECT dense_rank() OVER (ORDER BY amount) AS position FROM source.orders`,
		`SELECT id FROM source.orders UNION ALL SELECT id FROM source.payments`,
		`SELECT q.id FROM (SELECT id FROM model.orders) q`,
	}
	for _, sqlText := range queries {
		t.Run(sqlText, func(t *testing.T) {
			if _, err := Analyze(context.Background(), sqlText); err != nil {
				t.Fatalf("Analyze() error = %v", err)
			}
		})
	}

	analysis, err := Analyze(context.Background(), `SELECT o.id FROM source.orders o JOIN model.customers c ON c.id = o.customer_id`)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(analysis.SourceRefs, []string{"orders"}) || !reflect.DeepEqual(analysis.ModelRefs, []string{"customers"}) {
		t.Fatalf("dependencies = sources %#v models %#v", analysis.SourceRefs, analysis.ModelRefs)
	}
}

func TestAnalyzeRejectsUnadmittedCapabilities(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want string
	}{
		{"multiple statements", `SELECT 1; SELECT 2`, "exactly one"},
		{"reader", `SELECT * FROM read_csv('orders.csv')`, "table functions"},
		{"range", `SELECT * FROM range(10)`, "table functions"},
		{"secret access", `SELECT * FROM duckdb_secrets()`, "table functions"},
		{"external SQL bridge", `SELECT * FROM postgres_query('db', 'SELECT 1')`, "table functions"},
		{"attach", `ATTACH 'external.db' AS external`, "unsupported_statement"},
		{"extension management", `INSTALL httpfs`, "unsupported_statement"},
		{"query table", `SELECT query_table('SELECT 1')`, "not allowed"},
		{"qualified function", `SELECT foo.trim(name) FROM source.orders`, "qualified function"},
		{"raw schema", `SELECT * FROM raw.orders`, "not governed"},
		{"main schema", `SELECT * FROM main.orders`, "not governed"},
		{"unqualified relation", `SELECT * FROM orders`, "unqualified relation"},
		{"external catalog", `SELECT * FROM memory.source.orders`, "external catalog"},
		{"recursive CTE", `WITH RECURSIVE q AS (SELECT 1 AS x UNION ALL SELECT x + 1 FROM q) SELECT * FROM q`, "recursive CTEs"},
		{"forward CTE", `WITH a AS (SELECT * FROM b), b AS (SELECT 1 AS x) SELECT * FROM a`, "before its declaration"},
		{"self CTE", `WITH q AS (SELECT * FROM q) SELECT * FROM q`, "not governed"},
		{"sample", `SELECT * FROM source.orders USING SAMPLE 10`, "samples"},
		{"pivot", `PIVOT source.orders ON status USING count(*)`, "unsupported_statement"},
		{"limit percent", `SELECT * FROM source.orders LIMIT 10%`, "LIMIT PERCENT"},
		{"unapproved function nested", `WITH q AS (SELECT random() AS x FROM source.orders) SELECT * FROM q`, "not allowed"},
		{"parameter", `SELECT $1 FROM source.orders`, "not supported"},
		{"qualified star", `SELECT o.* FROM source.orders o`, "extended star"},
		{"star exclude", `SELECT * EXCLUDE (secret) FROM source.orders`, "extended star"},
		{"CTE aliases", `WITH q(id) AS (SELECT id FROM source.orders) SELECT id FROM q`, "CTE column aliases"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Analyze(context.Background(), test.sql)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Analyze() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestAnalyzeRejectsQualifiedSourceColumnReference(t *testing.T) {
	_, err := Analyze(context.Background(), `SELECT source.orders.order_id FROM source.orders`)
	if err == nil || !strings.Contains(err.Error(), `column reference "source.orders.order_id" must use a table alias`) {
		t.Fatalf("Analyze() error = %v", err)
	}
}

func TestRewriteSourcesUsesParserSpansAndPreservesOtherBytes(t *testing.T) {
	sqlText := "-- source.orders\nSELECT o.id, 'source.orders' AS note FROM source.orders o JOIN source.\"olist.order_items\" ON true"
	analysis, err := Analyze(context.Background(), sqlText)
	if err != nil {
		t.Fatal(err)
	}
	got, err := RewriteSources(sqlText, analysis, map[string]string{
		"orders":            "(SELECT id FROM read_csv('orders.csv'))",
		"olist.order_items": "(SELECT * FROM read_parquet('items.parquet'))",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	want := "-- source.orders\nSELECT o.id, 'source.orders' AS note FROM (SELECT id FROM read_csv('orders.csv')) o JOIN (SELECT * FROM read_parquet('items.parquet')) AS \"olist.order_items\" ON true"
	if got != want {
		t.Fatalf("RewriteSources() = %q, want %q", got, want)
	}
}

func TestRewriteSourcesRejectsMissingReplacement(t *testing.T) {
	analysis, err := Analyze(context.Background(), `SELECT * FROM source.orders`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RewriteSources(`SELECT * FROM source.orders`, analysis, nil, true); err == nil || !strings.Contains(err.Error(), `no replacement for source "orders"`) {
		t.Fatalf("RewriteSources() error = %v", err)
	}
}

func TestRewriteModelsUsesParserSpansForRelationsAndColumns(t *testing.T) {
	sqlText := `-- model.orders
SELECT model.orders.id FROM model.orders`
	analysis, err := Analyze(context.Background(), sqlText)
	if err != nil {
		t.Fatal(err)
	}
	got, err := RewriteModels(sqlText, analysis, "_candidate_namespace")
	if err != nil {
		t.Fatal(err)
	}
	want := "-- model.orders\nSELECT _candidate_namespace.orders.id FROM _candidate_namespace.orders"
	if got != want {
		t.Fatalf("RewriteModels() = %q, want %q", got, want)
	}
}

func TestRewriteModelsRewritesQuotedThreePartModelColumns(t *testing.T) {
	sqlText := `SELECT "model"."orders"."id" FROM "model"."orders"`
	analysis, err := Analyze(context.Background(), sqlText)
	if err != nil {
		t.Fatal(err)
	}
	got, err := RewriteModels(sqlText, analysis, "_candidate_namespace")
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT _candidate_namespace.orders.id FROM _candidate_namespace.orders`
	if got != want {
		t.Fatalf("RewriteModels() = %q, want %q", got, want)
	}
}

func TestRewriteModelsRewritesQuotedRelationNameWithPunctuation(t *testing.T) {
	sqlText := `SELECT model."olist.order-items".id FROM model."olist.order-items"`
	analysis, err := Analyze(context.Background(), sqlText)
	if err != nil {
		t.Fatal(err)
	}
	got, err := RewriteModels(sqlText, analysis, "_candidate_namespace")
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT _candidate_namespace."olist.order-items".id FROM _candidate_namespace."olist.order-items"`
	if got != want {
		t.Fatalf("RewriteModels() = %q, want %q", got, want)
	}
}

func TestRewriteModelsRejectsOverlappingParserSpans(t *testing.T) {
	sqlText := `SELECT model.orders.id FROM model.orders`
	analysis, err := Analyze(context.Background(), sqlText)
	if err != nil {
		t.Fatal(err)
	}
	// Duplicate relation evidence must fail closed rather than permitting an
	// ambiguous edit set to mutate authored SQL.
	analysis.ModelRelations = append(analysis.ModelRelations, analysis.ModelRelations[0])
	if _, err := RewriteModels(sqlText, analysis, "_candidate_namespace"); err == nil || !strings.Contains(err.Error(), "duplicate span") {
		t.Fatalf("RewriteModels() error = %v, want duplicate span rejection", err)
	}
}

func TestRewriteModelsRejectsInvalidOrOversizedNamespace(t *testing.T) {
	analysis, err := Analyze(context.Background(), `SELECT * FROM model.orders`)
	if err != nil {
		t.Fatal(err)
	}
	for _, namespace := range []string{"model;drop", strings.Repeat("a", 64)} {
		if _, err := RewriteModels(`SELECT * FROM model.orders`, analysis, namespace); err == nil || !strings.Contains(err.Error(), "relation namespace") {
			t.Fatalf("namespace %q unexpectedly accepted: %v", namespace, err)
		}
	}
}

func TestValidateQueryRejectsTemporalRelationClause(t *testing.T) {
	query := duckdbsql.Query{Statements: []duckdbsql.Statement{&duckdbsql.SelectStatement{
		From: &duckdbsql.BaseTableRelation{Name: "orders", Schema: "source", At: &duckdbsql.AtClause{Unit: "TIMESTAMP"}},
	}}}
	if err := validateQuery(query); err == nil || !strings.Contains(err.Error(), "temporal relation clauses") {
		t.Fatalf("validateQuery() error = %v", err)
	}
}

func TestValidateQueryRejectsInvalidRelationColumnAliases(t *testing.T) {
	tests := []duckdbsql.Relation{
		&duckdbsql.BaseTableRelation{Name: "orders", Schema: "source", ColumnAliases: []string{"bad-name"}},
		&duckdbsql.JoinRelation{JoinType: "INNER", RefType: "REGULAR", UsingColumns: []string{"id", "ID"}},
	}
	for _, relation := range tests {
		query := duckdbsql.Query{Statements: []duckdbsql.Statement{&duckdbsql.SelectStatement{From: relation}}}
		if err := validateQuery(query); err == nil {
			t.Fatalf("validateQuery() accepted relation %#v", relation)
		}
	}
}
