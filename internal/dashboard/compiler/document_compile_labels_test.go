package compiler

import (
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestCompileRecordTablePreservesAuthoredLabelsAndResultAliases(t *testing.T) {
	model := dashboardQueryTestModel()
	table := model.Tables["orders"]
	status := table.Dimensions["status"]
	status.Label = "Order status"
	table.Dimensions["status"] = status
	model.Tables["orders"] = table

	visual := document.DashboardVisual{
		Type: document.DashboardVisualTypeTable,
		Query: document.DashboardQuery{Value: &document.RecordsDashboardQuery{
			Type: "records", Dataset: "orders",
			Fields: []document.DashboardRecordFieldSelection{
				{Reference: &document.DashboardRecordFieldReference{Field: "status", Alias: stringPtr("displayStatus")}},
				{Reference: &document.DashboardRecordFieldReference{Field: "order_id", Alias: stringPtr("identifier")}},
			},
		}},
		Presentation: document.DashboardPresentation{Value: &document.TableDashboardPresentation{Type: "table", RowHeight: 32, ShowHeader: true}},
	}
	compiled, err := (dashboardCompileContext{model: model, modelID: "sales"}).compileVisuals(map[string]document.DashboardVisual{"records": visual})
	if err != nil {
		t.Fatal(err)
	}
	spec := compiled["records"].Spec.Value.(*visualizationir.TableVisualizationSpec)
	want := []struct{ alias, source, label string }{
		{"displayStatus", "orders.status", "Order status"},
		{"identifier", "orders.order_id", "identifier"},
	}
	for i, expected := range want {
		field, column := spec.Datasets[0].Fields[i], spec.Columns[i]
		if field.ID != expected.alias || field.SourceRef == nil || *field.SourceRef != expected.source || field.Label != expected.label {
			t.Fatalf("record field %d = %#v, want alias=%q source=%q label=%q", i, field, expected.alias, expected.source, expected.label)
		}
		if column.Field.Field != expected.alias || column.Label != expected.label {
			t.Fatalf("record column %d = %#v, want alias=%q label=%q", i, column, expected.alias, expected.label)
		}
	}
}

func TestCompilePivotPreservesSemanticLabelsAcrossAliases(t *testing.T) {
	model := dashboardQueryTestModel()
	region := model.Dimensions["state"]
	region.Label = "Customer region"
	model.Dimensions["state"] = region
	date := model.Dimensions["purchaseDate"]
	date.Label = "Order date"
	model.Dimensions["purchaseDate"] = date
	revenue := model.Metrics["revenue"]
	revenue.Label, revenue.Format = "Net revenue", "decimal"
	model.Metrics["revenue"] = revenue

	visual := document.DashboardVisual{
		Type: document.DashboardVisualTypePivot,
		Query: document.DashboardQuery{Value: &document.PivotDashboardQuery{
			Type:    "pivot",
			Rows:    []document.DashboardDimensionSelection{{Reference: &document.DashboardDimensionReference{Dimension: "state", Alias: stringPtr("region")}}},
			Columns: []document.DashboardDimensionSelection{{Reference: &document.DashboardDimensionReference{Dimension: "purchaseDate", Alias: stringPtr("day")}}},
			Metrics: []document.DashboardMetricSelection{{Reference: &document.DashboardMetricReference{Metric: "revenue", Alias: stringPtr("sales")}}},
		}},
		Presentation: document.DashboardPresentation{Value: &document.TableDashboardPresentation{Type: "table", RowHeight: 32, ShowHeader: true}},
	}
	compiled, err := (dashboardCompileContext{model: model, modelID: "sales"}).compileVisuals(map[string]document.DashboardVisual{"pivot": visual})
	if err != nil {
		t.Fatal(err)
	}
	spec := compiled["pivot"].Spec.Value.(*visualizationir.PivotVisualizationSpec)
	want := []struct{ alias, source, label string }{
		{"region", "state", "Customer region"},
		{"day", "purchaseDate", "Order date"},
		{"sales", "revenue", "Net revenue"},
	}
	for i, expected := range want {
		field := spec.Datasets[0].Fields[i]
		if field.ID != expected.alias || field.SourceRef == nil || *field.SourceRef != expected.source || field.Label != expected.label {
			t.Fatalf("pivot field %d = %#v, want alias=%q source=%q label=%q", i, field, expected.alias, expected.source, expected.label)
		}
	}
	metric := spec.Datasets[0].Fields[2]
	if metric.Format == nil {
		t.Fatal("aliased semantic metric lost its decimal format")
	}
	if kind, err := metric.Format.Kind(); err != nil || kind != "number" {
		t.Fatalf("aliased metric format = %q, error=%v; want number", kind, err)
	}
	if spec.Rows[0].Field != "region" || spec.Columns[0].Field != "day" || spec.Metrics[0].Field != "sales" {
		t.Fatalf("pivot channels did not retain result aliases: %#v", spec)
	}
}
