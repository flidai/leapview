package materialize

import (
	"context"
	"database/sql"
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/analytics/exploration/lowering"
)

func TestExplorationRecordsPreservesDuplicateAuthorizedRows(t *testing.T) {
	runtime, governor, _ := protectedConsumerFixture(t)
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders(id BIGINT, shared BIGINT)",
		"INSERT INTO model.orders VALUES (1, 10), (1, 20), (1, 30), (2, 40)",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	runtime.db = &semanticConsumerDuckDB{db: db}
	mode, dataset := exploration.ExplorationQueryModeRecords, "orders"
	spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "sales", DatasetID: &dataset, Mode: &mode,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.id"}}, Metrics: []exploration.ExplorationMetricRef{},
		Filters: []exploration.ExplorationFilter{{Field: "orders.id", Expression: exploration.ExplorationFilterExpression{Value: &exploration.ComparisonExplorationFilterExpression{Kind: "comparison", Operator: "equals", Value: exploration.ExplorationFilterValue{Value: &exploration.IntegerExplorationFilterValue{Kind: "integer", Value: "1"}}}}}},
		Sort:    []exploration.ExplorationSort{{Field: "orders.id", Direction: exploration.ExplorationSortDirectionAsc}}, Limit: 100,
	}
	request, err := lowering.QueryForModel(spec, runtime.model)
	if err != nil {
		t.Fatal(err)
	}
	request.PrincipalID = "alice"
	request.IncludeTotal = true
	ctx := dataquery.WithGovernor(context.Background(), governor)
	result, err := runtime.ExecuteDataQuery(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 3 || !result.TotalRowsKnown || result.TotalRows != 3 {
		t.Fatalf("records collapsed duplicates or lost governance: %#v", result)
	}
	for _, row := range result.Rows {
		if row["id"] != int64(1) {
			t.Fatalf("unexpected record: %#v", row)
		}
	}
	request.Offset, request.Limit = 1, 1
	window, err := runtime.ExecuteDataQuery(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Rows) != 1 || !window.TotalRowsKnown || window.TotalRows != 3 || window.Rows[0]["id"] != int64(1) {
		t.Fatalf("records window = %#v", window)
	}
}
