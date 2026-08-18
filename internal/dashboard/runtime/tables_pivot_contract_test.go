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
	if len(columns) != 9 {
		t.Fatalf("pivot columns = %d, want 9", len(columns))
	}
}

func TestPivotWindowAppliesAfterGroupingWithoutInteractiveInflation(t *testing.T) {
	rows := []map[string]any{{"id": "a"}, {"id": "b"}, {"id": "c"}, {"id": "d"}}
	window := applyPivotWindow(rows, 1, 2)
	if len(window) != 2 || window[0]["id"] != "b" || window[1]["id"] != "c" {
		t.Fatalf("pivot window = %#v", window)
	}
	if got := applyPivotWindow(rows, 9, 1); len(got) != 0 {
		t.Fatalf("out-of-range pivot window = %#v", got)
	}
}

func TestGrandPivotTotalIsNotImplicitColumnTotal(t *testing.T) {
	values := []crossTabValueField{{key: "revenue", label: "Revenue", format: "decimal"}}
	columns := []dashboard.TableColumn{{Key: "pivot_east", Role: "metric", Metric: "revenue", ColumnValue: "East"}}
	rows := []map[string]any{{"pivot_east": 3.0}, {"pivot_east": 4.0}}
	columns, rows = addPivotTotals(columns, rows, tablePlan{Totals: &visualizationdefinition.PivotTotals{Grand: true}}, values)
	if len(rows) != 3 || rows[2]["pivot_grand"] != 7.0 {
		t.Fatalf("grand-only total rows = %#v", rows)
	}
	if len(columns) != 2 || columns[1].Key != "pivot_grand" {
		t.Fatalf("grand-only total columns = %#v", columns)
	}
}
