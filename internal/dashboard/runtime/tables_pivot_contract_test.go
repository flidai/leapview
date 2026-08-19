package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
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

func TestPivotAxisWindowRetainsRowsBeyondInteractiveCellCap(t *testing.T) {
	axis := make([]map[string]any, dashboard.TableInteractiveRowCap+25)
	for index := range axis {
		axis[index] = map[string]any{"row": index}
	}
	dimensions := []visualizationdefinition.FieldBinding{{Alias: "row"}}
	axis = dedupePivotAxisRows(axis, dimensions)
	selected := applyPivotWindow(axis, dashboard.TableInteractiveRowCap+10, 5)
	if len(selected) != 5 {
		t.Fatalf("selected axis rows = %d, want 5", len(selected))
	}
	if got := selected[0]["row"]; got != dashboard.TableInteractiveRowCap+10 {
		t.Fatalf("first selected row = %v, want %d", got, dashboard.TableInteractiveRowCap+10)
	}
}

func TestPivotTotalsUseCompleteColumnAndGrandAggregates(t *testing.T) {
	values := []crossTabValueField{{key: "revenue", label: "Revenue", format: "decimal"}}
	columns := []dashboard.TableColumn{{Key: "pivot_east", Role: "metric", Metric: "revenue", ColumnValue: "East"}}
	rows := []map[string]any{{"region": "North", "pivot_east": 3.0}}
	table := tablePlan{Rows: []visualizationdefinition.FieldBinding{{Alias: "region"}}, ColumnDims: []visualizationdefinition.FieldBinding{{Alias: "quarter"}}, Totals: &visualizationdefinition.PivotTotals{Columns: true, Grand: true}}
	columns, rows = addPivotTotalsExact(columns, rows, table, values,
		[]map[string]any{{"quarter": "East", "revenue": 10.0}},
		[]map[string]any{{"revenue": 30.0}},
		map[string]string{"pivot_east": typedTupleIdentity([]any{"East"})},
	)
	if len(rows) != 2 || rows[1]["pivot_east"] != 10.0 || rows[1]["pivot_grand"] != 30.0 {
		t.Fatalf("exact totals row = %#v", rows)
	}
	if len(columns) != 2 {
		t.Fatalf("exact totals columns = %#v", columns)
	}
}

type pivotWindowDataRuntime struct {
	queries []reportdef.AggregateQuery
}

func (r *pivotWindowDataRuntime) Query(_ context.Context, query reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	r.queries = append(r.queries, query)
	if len(query.Dimensions) == 1 {
		rows := reportdef.QueryRows{}
		for index := query.Offset; index < query.Offset+query.Limit-1; index++ {
			rows = append(rows, reportdef.QueryRow{"row": index})
		}
		return rows, nil
	}
	rows := reportdef.QueryRows{}
	for _, row := range []int{dashboard.TableInteractiveRowCap + 2, dashboard.TableInteractiveRowCap + 3} {
		rows = append(rows,
			reportdef.QueryRow{"row": row, "column": "East", "value": float64(row)},
			reportdef.QueryRow{"row": row, "column": "West", "value": float64(row + 1)},
		)
	}
	return rows, nil
}
func (*pivotWindowDataRuntime) Rows(context.Context, reportdef.RowQuery) (reportdef.QueryRows, error) {
	return nil, nil
}
func (*pivotWindowDataRuntime) Count(context.Context, reportdef.CountQuery) (int, error) {
	return 0, nil
}
func (*pivotWindowDataRuntime) Histogram(context.Context, reportdef.RawValueQuery, int) ([]reportdef.HistogramBin, error) {
	return nil, nil
}
func (*pivotWindowDataRuntime) Distribution(context.Context, reportdef.RawValueQuery, []reportdef.QuerySort, int) (reportdef.QueryRows, error) {
	return nil, nil
}
func (r *pivotWindowDataRuntime) ExecuteDataQuery(context.Context, dataquery.Query) (dataquery.Result, error) {
	return dataquery.Result{}, nil
}
func (r *pivotWindowDataRuntime) Refresh(context.Context) error { return nil }
func (r *pivotWindowDataRuntime) Close() error                  { return nil }
func (r *pivotWindowDataRuntime) LastRefresh() time.Time        { return time.Time{} }

func TestPivotExecutionWindowsRowAxisBeyondCellCap(t *testing.T) {
	fake := &pivotWindowDataRuntime{}
	base := visualizationir.VisualizationSpecBase{Title: "Pivot", Datasets: []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: []visualizationir.VisualizationField{{ID: "row", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeInteger}, {ID: "column", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeString}, {ID: "value", Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal}}}}, DataBudget: visualizationir.VisualizationDataBudget{MaxRows: 1000, RequiredCompleteness: visualizationir.VisualizationCompletenessComplete}, Accessibility: visualizationir.VisualizationAccessibility{Title: "Pivot", Description: "Pivot"}}
	spec := visualizationir.VisualizationSpec{Value: &visualizationir.PivotVisualizationSpec{VisualizationSpecBase: base, Kind: "pivot", Rows: []visualizationir.VisualizationFieldRef{{Dataset: "primary", Field: "row"}}, Columns: []visualizationir.VisualizationFieldRef{{Dataset: "primary", Field: "column"}}, Metrics: []visualizationir.VisualizationFieldRef{{Dataset: "primary", Field: "value"}}, Presentation: visualizationir.GridVisualizationPresentation{RowHeight: 24, ShowHeader: true}}}
	table := tablePlan{Definition: visualizationdefinition.Definition{ID: "pivot", Spec: spec, Query: visualizationdefinition.QueryBinding{DatasetID: "primary"}}, Table: "orders", Rows: []visualizationdefinition.FieldBinding{{FieldID: "row", Alias: "row"}}, ColumnDims: []visualizationdefinition.FieldBinding{{FieldID: "column", Alias: "column"}}, Metrics: []visualizationdefinition.FieldBinding{{FieldID: "value", Alias: "value"}}, Offset: dashboard.TableInteractiveRowCap + 2, Limit: 2}
	runtime := &modelRuntime{model: &semanticmodel.Model{}, data: fake}
	service := &VisualizationDataService{filters: &FilterService{}}
	definition := &dashboarddefinition.Definition{Visualizations: map[string]visualizationdefinition.Definition{}}
	_, rows, incomplete, err := service.crossTabTableRows(context.Background(), runtime, definition, table, dashboard.Filters{}, dashboard.TableRequest{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["row"] != dashboard.TableInteractiveRowCap+2 || incomplete {
		t.Fatalf("rows=%#v incomplete=%v", rows, incomplete)
	}
	if len(fake.queries) != 2 || fake.queries[0].Offset != dashboard.TableInteractiveRowCap+2 || fake.queries[0].Limit != 3 || fake.queries[1].Limit <= 0 {
		t.Fatalf("governed query shapes = %#v", fake.queries)
	}
}

func TestPivotTupleIdentityPreservesTypesAndNulls(t *testing.T) {
	if typedTupleIdentity([]any{int64(1)}) == typedTupleIdentity([]any{float64(1)}) {
		t.Fatal("integer and float tuple identities collided")
	}
	if typedTupleIdentity([]any{nil}) == typedTupleIdentity([]any{"<nil>"}) {
		t.Fatal("null and text tuple identities collided")
	}
}
