//go:build duckdb_arrow

package gates

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/release"
)

func TestEvaluateExecutesAllChecksAgainstDuckDBCandidateRelations(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE SCHEMA "model"`,
		`CREATE TABLE "model"."customers" (id BIGINT PRIMARY KEY)`,
		`CREATE TABLE "model"."orders" (id BIGINT, customer_id BIGINT, state VARCHAR, updated_at TIMESTAMP)`,
		`INSERT INTO "model"."customers" VALUES (1), (2)`,
		`INSERT INTO "model"."orders" VALUES (10, 1, 'open', TIMESTAMP '2026-08-18 10:00:00'), (11, 2, 'closed', TIMESTAMP '2026-08-18 10:00:00')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	query := func(ctx context.Context, plan semanticquery.Plan) (semanticquery.Rows, error) {
		if len(plan.Columns) != 1 {
			t.Fatalf("gate plan columns = %v, want one declared result column", plan.Columns)
		}
		rows, err := db.QueryContext(ctx, plan.SQL, plan.Args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		columns, err := rows.Columns()
		if err != nil {
			return nil, err
		}
		if len(columns) != 1 || columns[0] != plan.Columns[0] {
			t.Fatalf("gate query columns = %v, plan columns = %v", columns, plan.Columns)
		}
		result := semanticquery.Rows{}
		for rows.Next() {
			values := make([]any, len(columns))
			scan := make([]any, len(values))
			for i := range values {
				scan[i] = &values[i]
			}
			if err := rows.Scan(scan...); err != nil {
				return nil, err
			}
			row := semanticquery.Row{}
			for i, column := range columns {
				row[column] = values[i]
			}
			result = append(result, row)
		}
		return result, rows.Err()
	}
	freshnessAt := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	input := Input{
		CandidateID: "candidate-duckdb", SourceDigest: testDigest, BindingGeneration: testBinding,
		RuntimeVersion: "runtime-1", DuckDBVersion: "duckdb-1", Now: freshnessAt.Add(time.Minute), Query: query,
		Sources: []SourceInput{{ID: "orders", Source: semanticmodel.Source{SchemaMode: "inferred", Freshness: &semanticmodel.SourceFreshnessSpec{Basis: "field", Field: "updated_at", ErrorAfter: &semanticmodel.FreshnessDurationSpec{Amount: 1, Unit: "hour"}}}, Observed: []semanticmodel.ColumnSchema{{Name: "id", PhysicalType: "BIGINT", Ordinal: 1}, {Name: "customer_id", PhysicalType: "BIGINT", Ordinal: 2}, {Name: "state", PhysicalType: "VARCHAR", Ordinal: 3}, {Name: "updated_at", PhysicalType: "TIMESTAMP", Ordinal: 4}}, FreshnessObserved: freshnessAt}},
		Models: []ModelInput{
			{ID: "customers", Model: semanticmodel.Table{ModelName: "customers"}},
			{ID: "orders", Model: semanticmodel.Table{ModelName: "orders", Checks: []semanticmodel.ModelCheck{
				{Type: "non_null", Field: "customer_id", Severity: "error"},
				{Type: "unique", Fields: []string{"id"}, Severity: "error"},
				{Type: "accepted_values", Field: "state", Values: []string{"closed", "open"}, Severity: "error"},
				{Type: "relationship", Field: "customer_id", To: "customers.id", Severity: "error"},
				{Type: "row_count", Minimum: int64Ptr(2), Maximum: int64Ptr(2), Severity: "error"},
			}}},
		},
	}
	evidence, err := Evaluate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Sources[0].FreshnessOutcome != release.GateSuccess || len(evidence.Checks) != 5 {
		t.Fatalf("unexpected gate evidence: %#v", evidence)
	}
	for _, check := range evidence.Checks {
		if check.Outcome != release.GateSuccess || check.Queries == 0 {
			t.Fatalf("check did not execute successfully: %#v", check)
		}
	}
}

func int64Ptr(value int64) *int64 { return &value }

func TestSourceAndModelChecksObserveTheirOwnRelations(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE SCHEMA "source"`,
		`CREATE SCHEMA "model"`,
		`CREATE TABLE "source"."orders" (id BIGINT, state VARCHAR)`,
		`INSERT INTO "source"."orders" VALUES (1, 'valid'), (1, 'invalid'), (NULL, 'valid')`,
		`CREATE TABLE "model"."orders" AS SELECT DISTINCT id, state FROM "source"."orders" WHERE id IS NOT NULL AND state = 'valid'`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	query := func(ctx context.Context, plan semanticquery.Plan) (semanticquery.Rows, error) {
		rows, err := db.QueryContext(ctx, plan.SQL, plan.Args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		result := semanticquery.Rows{}
		for rows.Next() {
			var value any
			if err := rows.Scan(&value); err != nil {
				return nil, err
			}
			result = append(result, semanticquery.Row{plan.Columns[0]: value})
		}
		return result, rows.Err()
	}
	checks := []semanticmodel.ModelCheck{
		{ID: "id_present", Type: "non_null", Field: "id", Severity: "error"},
		{ID: "id_unique", Type: "unique", Fields: []string{"id"}, Severity: "error"},
		{ID: "state_valid", Type: "accepted_values", Field: "state", Values: []string{"valid"}, Severity: "error"},
	}
	sourceEvidence, err := EvaluateSourceChecks(context.Background(), "source:orders", `"source"."orders"`, checks, nil, Bounds{}, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(sourceEvidence) != len(checks) {
		t.Fatalf("source evidence count = %d", len(sourceEvidence))
	}
	for _, check := range sourceEvidence {
		if check.Outcome != release.GateBlocking {
			t.Fatalf("source rule %q outcome = %s", check.Identity, check.Outcome)
		}
	}
	evidence, err := Evaluate(context.Background(), Input{
		CandidateID: "candidate-output", SourceDigest: testDigest, BindingGeneration: testBinding,
		RuntimeVersion: "runtime-1", DuckDBVersion: "duckdb-1", Query: query,
		Models: []ModelInput{{ID: "orders", Model: semanticmodel.Table{ModelName: "orders", Checks: checks}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.Checks) != len(checks) {
		t.Fatalf("model evidence count = %d", len(evidence.Checks))
	}
	for _, check := range evidence.Checks {
		if check.Outcome != release.GateSuccess {
			t.Fatalf("model rule %q outcome = %s", check.Identity, check.Outcome)
		}
	}
}
