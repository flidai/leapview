package runtime

import (
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
)

func TestAddPivotTotalsPreservesMultipleColumnAndMetricCells(t *testing.T) {
	values := []crossTabValueField{
		{key: "revenue", label: "Revenue", format: "decimal"},
		{key: "orders", label: "Orders", format: "number"},
	}
	columns := []dashboard.TableColumn{
		{Key: "region", Role: "row_header"},
		{Key: "pivot_east__revenue", Role: "metric", Metric: "revenue", ColumnValue: "East"},
		{Key: "pivot_east__orders", Role: "metric", Metric: "orders", ColumnValue: "East"},
		{Key: "pivot_west__revenue", Role: "metric", Metric: "revenue", ColumnValue: "West"},
		{Key: "pivot_west__orders", Role: "metric", Metric: "orders", ColumnValue: "West"},
	}
	rows := []map[string]any{
		{"region": "North", "pivot_east__revenue": 10.0, "pivot_east__orders": 2.0, "pivot_west__revenue": 7.0, "pivot_west__orders": 3.0},
		{"region": "South", "pivot_east__revenue": 4.0, "pivot_east__orders": 1.0, "pivot_west__revenue": 8.0, "pivot_west__orders": 5.0},
	}
	columns, rows = addPivotTotals(columns, rows, tablePlan{
		Rows:   []visualizationdefinition.FieldBinding{{Alias: "region"}},
		Totals: &visualizationdefinition.PivotTotals{Rows: true, Columns: true, Grand: true},
	}, values)
	if len(rows) != 3 {
		t.Fatalf("rows with pivot totals = %d, want 3", len(rows))
	}
	if rows[0]["pivot_total__revenue"] != 17.0 || rows[1]["pivot_total__orders"] != 6.0 {
		t.Fatalf("row totals = %#v", rows)
	}
	if rows[2]["pivot_east__revenue"] != 14.0 || rows[2]["pivot_west__orders"] != 8.0 {
		t.Fatalf("column totals = %#v", rows[2])
	}
	if len(columns) != 7 {
		t.Fatalf("pivot columns = %d, want 7", len(columns))
	}
}
