package materialize

import (
	"context"
	"database/sql"
	"sync/atomic"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

type semanticConsumerDuckDB struct {
	cacheRuntimeDatabase
	db      *sql.DB
	queries atomic.Int32
}

func (d *semanticConsumerDuckDB) QueryArrow(ctx context.Context, plan semanticquery.Plan, sink arrowquery.Sink) error {
	d.queries.Add(1)
	rows, err := d.db.QueryContext(ctx, plan.SQL, plan.Args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	values := make(semanticquery.Rows, 0)
	for rows.Next() {
		rowValues := make([]any, len(columns))
		pointers := make([]any, len(rowValues))
		for index := range rowValues {
			pointers[index] = &rowValues[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			return err
		}
		row := semanticquery.Row{}
		for index, column := range columns {
			row[column] = rowValues[index]
		}
		values = append(values, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return writeTestRowsArrow(ctx, plan, values, sink)
}

func TestProtectedRuntimeRowsTotalUsesAdmittedMultiRowPopulation(t *testing.T) {
	runtime, governor, _ := protectedConsumerFixtureWithModelModifier(t, true, func(model *semanticmodel.Model) {
		table := model.Tables["orders"]
		table.Dimensions["region"] = semanticmodel.MetricDimension{Field: "orders.region", Table: "orders", Name: "region", Type: "string", Datatype: semanticmodel.DataTypeString}
		table.Dimensions["approved"] = semanticmodel.MetricDimension{Field: "orders.approved", Table: "orders", Name: "approved", Type: "boolean", Datatype: semanticmodel.DataTypeBoolean}
		model.Tables["orders"] = table
		model.Dimensions["region"] = semanticmodel.SemanticDimension{Name: "region", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.region"}}}
		model.Dimensions["approved"] = semanticmodel.SemanticDimension{Name: "approved", Datatype: semanticmodel.DataTypeBoolean, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.approved"}}}
		model.Filters = map[string]semanticmodel.SemanticFilterSpec{"west_only": {Field: "orders.region", Operator: "equals", Value: "west"}}
		metric := model.Metrics["shared"]
		metric.Where = []string{"west_only"}
		model.Metrics["shared"] = metric
		model.AccessPolicy.Dimensions["region"] = []string{"canViewAccount"}
		model.AccessPolicy.Dimensions["approved"] = []string{"canViewAccount"}
	})
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders(id BIGINT, shared BIGINT, region VARCHAR, approved BOOLEAN)",
		"INSERT INTO model.orders VALUES (1, 1, 'west', true), (1, 1, 'east', true), (1, 1, 'west', false), (2, 2, 'west', true)",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	database := &semanticConsumerDuckDB{db: db}
	runtime.db = database
	request := dataquery.Query{
		ModelID: "sales", PrincipalID: "alice", Kind: dataquery.KindSemanticRows, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Metrics: []dataquery.Field{{Field: "shared", Alias: "shared"}},
		Filters: []dataquery.Filter{{Field: "approved", Operator: "equals", Values: []any{true}}}, Sort: []dataquery.Sort{{Field: "orders.id", Direction: "asc"}},
		IncludeTotal: true, Limit: 1, Offset: 1,
	}
	result, err := runtime.ExecuteDataQuery(dataquery.WithGovernor(context.Background(), governor), request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.TotalRowsKnown || result.TotalRows != 1 {
		t.Fatalf("total=%d known=%v, want one named/request-filtered row", result.TotalRows, result.TotalRowsKnown)
	}
	if len(result.Rows) != 0 {
		t.Fatalf("returned rows = %#v, want the second row paginated away", result.Rows)
	}
	if got := database.queries.Load(); got != 2 {
		t.Fatalf("physical executions = %d, want visible rows plus admitted count", got)
	}
	request.Offset = 0
	request.Limit = 0
	request.Filters = nil
	result, err = runtime.ExecuteDataQuery(dataquery.WithGovernor(context.Background(), governor), request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.TotalRowsKnown || result.TotalRows != 2 || len(result.Rows) != 2 {
		t.Fatalf("named population: total=%d known=%v rows=%d, want two rows and total", result.TotalRows, result.TotalRowsKnown, len(result.Rows))
	}
	request.AuthorizationFields = []dataquery.Field{
		{Field: "orders.id", Kind: dataquery.FieldKindDimension},
		{Field: "shared", Kind: dataquery.FieldKindMetric},
	}
	request.Fields, request.Metrics = nil, nil
	result, err = runtime.ExecuteDataQuery(dataquery.WithGovernor(context.Background(), governor), request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.TotalRowsKnown || result.TotalRows != 2 {
		t.Fatalf("count-only named population: total=%d known=%v, want 2", result.TotalRows, result.TotalRowsKnown)
	}
}
