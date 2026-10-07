package materialize

import (
	"context"
	"database/sql"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"testing"
)

func TestExplorationAggregateWindowsCountAuthorizedGroups(t *testing.T) {
	runtime, governor, _ := protectedConsumerFixture(t)
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{"CREATE SCHEMA model", "CREATE TABLE model.orders(id BIGINT, shared BIGINT)", "INSERT INTO model.orders VALUES (1,10),(1,10),(1,20),(2,30)"} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	runtime.db = &semanticConsumerDuckDB{db: db}
	request := dataquery.Query{ModelID: "sales", Kind: dataquery.KindSemanticAggregate, Target: "orders", Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, PrincipalID: "alice", IncludeTotal: true, Offset: 1500, Limit: 100}
	ctx := dataquery.WithGovernor(context.Background(), governor)
	result, err := runtime.ExecuteDataQuery(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 0 || !result.TotalRowsKnown || result.TotalRows != 1 {
		t.Fatalf("deep aggregate window = %#v", result)
	}
	request.Offset = 0
	result, err = runtime.ExecuteDataQuery(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || !result.TotalRowsKnown || result.TotalRows != 1 || result.Rows[0]["id"] != int64(1) {
		t.Fatalf("aggregate result = %#v", result)
	}
}
