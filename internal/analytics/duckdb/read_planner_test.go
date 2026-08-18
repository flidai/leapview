package duckdb

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestPlanModelTableCompilesCSVSQLModelToInlineRelations(t *testing.T) {
	ctx := context.Background()
	db := openPlanningRuntimeDB(t)
	defer db.Close()
	model := planningModel(map[string][]string{
		"orders":   {"order_id", "customer_id", "status"},
		"payments": {"order_id", "payment_value"},
	}, semanticmodel.Table{
		Sources: []string{"orders", "payments"},
		Columns: map[string]semanticmodel.ModelColumn{
			"order_id":    {Datatype: semanticmodel.DataTypeString},
			"customer_id": {Datatype: semanticmodel.DataTypeString},
			"revenue":     {Datatype: semanticmodel.DataTypeFloat},
		},
		Entities: map[string]semanticmodel.ModelEntitySpec{"order_id": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order_id",
		Dimensions: map[string]semanticmodel.MetricDimension{
			"order_id":    {Label: "Order ID", Datatype: semanticmodel.DataTypeString},
			"customer_id": {Label: "Customer ID", Datatype: semanticmodel.DataTypeString},
			"revenue":     {Label: "Revenue", Datatype: semanticmodel.DataTypeFloat},
		},
		Transform: semanticmodel.Transform{SQL: `
			SELECT o.order_id, o.customer_id, SUM(try_cast(p.payment_value AS DOUBLE)) AS revenue
			FROM source.orders o
			JOIN source.payments p USING (order_id)
			WHERE o.status IS NOT NULL
			GROUP BY o.order_id, o.customer_id
		`},
	})
	validateAndBindPlanningManagedRoot(t, model, managedPlanningRoot)

	plan, err := PlanModelTable(ctx, db, model, "orders", model.Tables["orders"])
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != analyticsmaterialize.PlanModeProjectedSourceInline {
		t.Fatalf("mode = %q, want inline", plan.Mode)
	}
	for _, want := range []string{
		"CREATE OR REPLACE TABLE model.orders AS",
		"FROM (SELECT customer_id, order_id FROM read_csv('/managed/revision/orders.csv')) o",
		"JOIN (SELECT order_id, payment_value FROM read_csv('/managed/revision/payments.csv')) p",
	} {
		if !strings.Contains(plan.SQL, want) {
			t.Fatalf("plan SQL = %s, want %q", plan.SQL, want)
		}
	}
	if strings.Contains(plan.SQL, "source.orders") || strings.Contains(plan.SQL, "source.payments") {
		t.Fatalf("plan SQL still contains source refs: %s", plan.SQL)
	}
}

func TestPlanModelTableCompilesDirectSourceFromColumns(t *testing.T) {
	ctx := context.Background()
	db := openPlanningRuntimeDB(t)
	defer db.Close()
	model := planningModel(map[string][]string{
		"orders": {"raw_order_id", "gross_revenue", "status"},
	}, semanticmodel.Table{
		Source:    "orders",
		ModelName: "orders",
		Entities:  map[string]semanticmodel.ModelEntitySpec{"order_id": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order_id",
		Columns: map[string]semanticmodel.ModelColumn{
			"order_id": {SourceField: "raw_order_id", Datatype: semanticmodel.DataTypeString},
			"revenue":  {SourceField: "gross_revenue", Datatype: semanticmodel.DataTypeFloat},
			"status":   {Datatype: semanticmodel.DataTypeString},
		},
		Dimensions: map[string]semanticmodel.MetricDimension{
			"order_id": {Label: "Order ID", Datatype: semanticmodel.DataTypeString},
			"revenue":  {Label: "Revenue", Datatype: semanticmodel.DataTypeFloat},
			"status":   {Label: "Status", Datatype: semanticmodel.DataTypeString},
		},
	})
	validateAndBindPlanningManagedRoot(t, model, managedPlanningRoot)

	plan, err := PlanModelTable(ctx, db, model, "orders", model.Tables["orders"])
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != analyticsmaterialize.PlanModeDirectSourceRead {
		t.Fatalf("mode = %q, want direct", plan.Mode)
	}
	want := "CREATE OR REPLACE TABLE model.orders AS SELECT raw_order_id AS order_id, gross_revenue AS revenue, status FROM read_csv('/managed/revision/orders.csv')"
	if plan.SQL != want {
		t.Fatalf("plan SQL = %q, want %q", plan.SQL, want)
	}
}

func TestPlanModelTableCompilesCountStarToInlineRowPresence(t *testing.T) {
	ctx := context.Background()
	db := openPlanningRuntimeDB(t)
	defer db.Close()
	model := planningModel(map[string][]string{
		"orders": {"order_id", "customer_id"},
	}, semanticmodel.Table{
		Sources:  []string{"orders"},
		Entities: map[string]semanticmodel.ModelEntitySpec{"order_count": {Type: "primary", Fields: []string{"order_count"}}}, GrainEntity: "order_count",
		Dimensions: map[string]semanticmodel.MetricDimension{
			"order_count": {Label: "Order Count", Datatype: semanticmodel.DataTypeInteger},
		},
		Transform: semanticmodel.Transform{SQL: `SELECT COUNT(*) AS order_count FROM source.orders`},
	})
	validateAndBindPlanningManagedRoot(t, model, managedPlanningRoot)

	plan, err := PlanModelTable(ctx, db, model, "orders", model.Tables["orders"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.SQL, "FROM (SELECT 1 AS __leapview_row_present FROM read_csv('/managed/revision/orders.csv'))") {
		t.Fatalf("plan SQL = %s, want row-presence inline relation", plan.SQL)
	}
}

func TestPlanModelTableAliasesUnaliasedInlineSourceRefs(t *testing.T) {
	ctx := context.Background()
	db := openPlanningRuntimeDB(t)
	defer db.Close()
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/orders.csv", []byte("order_id\n1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	model := planningModel(map[string][]string{
		"orders": {"order_id"},
	}, semanticmodel.Table{
		Sources:  []string{"orders"},
		Entities: map[string]semanticmodel.ModelEntitySpec{"order_id": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order_id",
		Dimensions: map[string]semanticmodel.MetricDimension{"order_id": {Label: "Order ID", Datatype: semanticmodel.DataTypeString}},
		Transform:  semanticmodel.Transform{SQL: `SELECT orders.order_id FROM source.orders`},
	})
	validateAndBindPlanningManagedRoot(t, model, dir)

	plan, err := PlanModelTable(ctx, db, model, "orders", model.Tables["orders"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.SQL, "AS orders") {
		t.Fatalf("plan SQL = %s, want alias for unaliased source relation", plan.SQL)
	}
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA model"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, plan.SQL); err != nil {
		t.Fatalf("executing plan SQL failed: %v\nSQL: %s", err, plan.SQL)
	}
}

func TestPlanModelTablePreservesMixedInlineSourceAliases(t *testing.T) {
	ctx := context.Background()
	db := openPlanningRuntimeDB(t)
	defer db.Close()
	model := planningModel(map[string][]string{
		"orders":   {"order_id"},
		"payments": {"order_id", "payment_value"},
	}, semanticmodel.Table{
		Sources: []string{"orders", "payments"},
		Columns: map[string]semanticmodel.ModelColumn{
			"order_id":      {Datatype: semanticmodel.DataTypeString},
			"payment_value": {Datatype: semanticmodel.DataTypeString},
		},
		Entities: map[string]semanticmodel.ModelEntitySpec{"order_id": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order_id",
		Dimensions: map[string]semanticmodel.MetricDimension{
			"order_id":      {Label: "Order ID", Datatype: semanticmodel.DataTypeString},
			"payment_value": {Label: "Payment Value", Datatype: semanticmodel.DataTypeString},
		},
		Transform: semanticmodel.Transform{SQL: `SELECT orders.order_id, p.payment_value FROM source.orders JOIN source.payments p USING (order_id)`},
	})
	validateAndBindPlanningManagedRoot(t, model, managedPlanningRoot)

	plan, err := PlanModelTable(ctx, db, model, "orders", model.Tables["orders"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.SQL, "FROM (SELECT order_id FROM read_csv('/managed/revision/orders.csv')) AS orders") {
		t.Fatalf("plan SQL = %s, want generated alias for orders", plan.SQL)
	}
	if !strings.Contains(plan.SQL, "JOIN (SELECT order_id, payment_value FROM read_csv('/managed/revision/payments.csv')) p") {
		t.Fatalf("plan SQL = %s, want explicit payments alias preserved", plan.SQL)
	}
}

func TestPlanModelTableRejectsQualifiedSourceColumnRefs(t *testing.T) {
	ctx := context.Background()
	db := openPlanningRuntimeDB(t)
	defer db.Close()
	model := planningModel(map[string][]string{
		"orders": {"order_id"},
	}, semanticmodel.Table{
		Sources:  []string{"orders"},
		Entities: map[string]semanticmodel.ModelEntitySpec{"order_id": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order_id",
		Dimensions: map[string]semanticmodel.MetricDimension{"order_id": {Label: "Order ID", Datatype: semanticmodel.DataTypeString}},
		Transform:  semanticmodel.Transform{SQL: `SELECT source.orders.order_id FROM source.orders`},
	})
	validateAndBindPlanningManagedRoot(t, model, managedPlanningRoot)

	_, err := PlanModelTable(ctx, db, model, "orders", model.Tables["orders"])
	if err == nil || !strings.Contains(err.Error(), `column reference "source.orders.order_id" must use a table alias`) {
		t.Fatalf("PlanModelTable error = %v, want qualified source column rejection", err)
	}
}

func TestPlanModelTableFailsClosedWhenInlineExplainOmitsSourceScan(t *testing.T) {
	ctx := context.Background()
	db := openPlanningRuntimeDB(t)
	defer db.Close()
	model := planningModel(map[string][]string{
		"orders": {"order_id", "customer_id"},
	}, semanticmodel.Table{
		Sources: []string{"orders"},
		Columns: map[string]semanticmodel.ModelColumn{
			"order_id":    {Datatype: semanticmodel.DataTypeString},
			"customer_id": {Datatype: semanticmodel.DataTypeString},
		},
		Entities: map[string]semanticmodel.ModelEntitySpec{"order_id": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order_id",
		Dimensions: map[string]semanticmodel.MetricDimension{
			"order_id":    {Label: "Order ID", Datatype: semanticmodel.DataTypeString},
			"customer_id": {Label: "Customer ID", Datatype: semanticmodel.DataTypeString},
		},
		Transform: semanticmodel.Transform{SQL: `SELECT * FROM source.orders WHERE 1=0`},
	})
	validateAndBindPlanningManagedRoot(t, model, managedPlanningRoot)

	_, err := PlanModelTable(ctx, db, model, "orders", model.Tables["orders"])
	if err == nil || !strings.Contains(err.Error(), `SQL plan did not expose projections for source "orders"`) {
		t.Fatalf("PlanModelTable error = %v, want fail-closed missing projection error", err)
	}
}

func TestPlanModelTableFailsClosedForUnusedCTESourceRef(t *testing.T) {
	ctx := context.Background()
	db := openPlanningRuntimeDB(t)
	defer db.Close()
	model := planningModel(map[string][]string{
		"orders":   {"order_id"},
		"payments": {"order_id"},
	}, semanticmodel.Table{
		Sources:  []string{"orders", "payments"},
		Entities: map[string]semanticmodel.ModelEntitySpec{"order_id": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order_id",
		Dimensions: map[string]semanticmodel.MetricDimension{"order_id": {Label: "Order ID", Datatype: semanticmodel.DataTypeString}},
		Transform: semanticmodel.Transform{SQL: `
			WITH unused_payments AS (SELECT order_id FROM source.payments)
			SELECT order_id FROM source.orders
		`},
	})
	validateAndBindPlanningManagedRoot(t, model, managedPlanningRoot)

	_, err := PlanModelTable(ctx, db, model, "orders", model.Tables["orders"])
	if err == nil || !strings.Contains(err.Error(), `SQL plan did not expose projections for source "payments"`) {
		t.Fatalf("PlanModelTable error = %v, want fail-closed unused source error", err)
	}
}

func openPlanningRuntimeDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	return db
}

const managedPlanningRoot = "/managed/revision"

func validateAndBindPlanningManagedRoot(t *testing.T, model *semanticmodel.Model, root string) {
	t.Helper()
	if err := model.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, connection := range model.Connections {
		if connection.Kind != "managed" {
			continue
		}
		connection.Root = filepath.Clean(root)
		model.Connections[name] = connection
	}
}

func planningModel(sourceColumns map[string][]string, table semanticmodel.Table) *semanticmodel.Model {
	sources := map[string]semanticmodel.Source{}
	for name, columns := range sourceColumns {
		schemaColumns := make([]semanticmodel.ColumnSchema, 0, len(columns))
		for index, column := range columns {
			schemaColumns = append(schemaColumns, semanticmodel.ColumnSchema{Name: column, Ordinal: index + 1, PhysicalType: "VARCHAR"})
		}
		sources[name] = semanticmodel.Source{
			Connection:       "local_files",
			Path:             name + ".csv",
			Format:           "csv",
			EffectiveOptions: map[string]any{},
			Schema:           semanticmodel.TableSchema{Columns: schemaColumns},
		}
	}
	if strings.TrimSpace(table.Transform.SQL) != "" {
		table.SQLAnalysisEvidence = &semanticmodel.SQLAnalysisEvidence{Validated: true, SourceRefs: append([]string(nil), table.Sources...), ModelRefs: append([]string(nil), table.ModelDependencies...)}
	}
	return &semanticmodel.Model{
		Name:        "test",
		Connections: map[string]semanticmodel.Connection{"local_files": {Kind: "managed"}},
		Sources:     sources,
		Tables:      map[string]semanticmodel.Table{"orders": table},
		Datasets:    map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Metrics:     map[string]semanticmodel.Metric{},
	}
}
