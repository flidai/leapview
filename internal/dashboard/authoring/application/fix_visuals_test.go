package application

import (
	"encoding/json"
	"reflect"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/document"
)

func fixVisualFixture() (document.DashboardDocument, *semanticmodel.Model) {
	dimension, metric := "country", "revenue"
	model := &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
		"orders": {ModelName: "orders", GrainEntity: "order", Entities: map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"country"}}}, Dimensions: map[string]semanticmodel.MetricDimension{"country": {Datatype: semanticmodel.DataTypeString}, "revenue": {Datatype: semanticmodel.DataTypeDecimal}}},
		"cash":   {ModelName: "cash", GrainEntity: "scenario", Entities: map[string]semanticmodel.EntityDefinition{"scenario": {Type: "primary", Fields: []string{"scenario"}}}, Dimensions: map[string]semanticmodel.MetricDimension{"scenario": {Datatype: semanticmodel.DataTypeString}}},
	}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}, "cash": {Model: "cash"}}, Dimensions: map[string]semanticmodel.SemanticDimension{
		"country":      {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.country"}}},
		"aaa_scenario": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"cash": {Field: "cash.scenario"}}},
	}, Metrics: map[string]semanticmodel.Metric{"revenue": {Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.revenue"}}}}
	visual := func(empty bool) document.DashboardVisual {
		q := &document.AggregateDashboardQuery{Type: "aggregate"}
		if !empty {
			q.Dimensions = []document.DashboardDimensionSelection{{String: &dimension}}
			q.Metrics = []document.DashboardMetricSelection{{String: &metric}}
		}
		return document.DashboardVisual{Type: document.DashboardVisualTypeFunnel, Query: document.DashboardQuery{Value: q}, Presentation: document.DashboardPresentation{Value: &document.ProportionalDashboardPresentation{Type: "proportional"}}}
	}
	component := func(id string, col int32) document.DashboardPageComponent {
		return document.DashboardPageComponent{Value: &document.VisualDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: id, Type: "visual", Placement: document.DashboardPlacement{Column: col, Row: 1, ColumnSpan: 6, RowSpan: 5}}, Type: "visual", Visual: id}}
	}
	return document.DashboardDocument{APIVersion: document.DashboardApiVersionLeapviewDevV1, Kind: document.DashboardResourceKindDashboard, Metadata: document.DashboardMetadata{ID: "dashboard:sales", Name: "sales"}, Spec: document.DashboardSpec{SemanticModel: "sales", Visuals: map[string]document.DashboardVisual{"ready": visual(false), "empty": visual(true)}, Pages: []document.DashboardPage{{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{component("ready", 1), component("empty", 7)}}}}}, model
}

func TestFixVisualsCompletesCompatibleFieldsWithoutChangingExistingCharts(t *testing.T) {
	doc, model := fixVisualFixture()
	original, _ := json.Marshal(doc)
	fields, err := missingVisualFields(doc, "overview", model)
	if err != nil {
		t.Fatal(err)
	}
	want := []authoring.AssignFieldPayload{{PageID: "overview", VisualID: "empty", FieldID: "revenue", Role: authoring.FieldRoleMetric, ResolvedTable: "orders"}, {PageID: "overview", VisualID: "empty", FieldID: "country", Role: authoring.FieldRoleDimension}}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("fields = %#v, want %#v", fields, want)
	}
	after, _ := json.Marshal(doc)
	if string(after) != string(original) {
		t.Fatal("resolution mutated the current revision")
	}
	fixed, err := authoring.WithAssignedVisualFields(doc, fields)
	if err != nil {
		t.Fatal(err)
	}
	next, err := missingVisualFields(fixed, "overview", model)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 0 {
		t.Fatalf("fix is not idempotent: %#v", next)
	}
}

func TestFixVisualsSkipsChartsWithoutCompatibleFields(t *testing.T) {
	doc, model := fixVisualFixture()
	delete(model.Dimensions, "country")
	fields, err := missingVisualFields(doc, "overview", model)
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 0 {
		t.Fatalf("invented incompatible fields: %#v", fields)
	}
}

func TestFixVisualsCompletesPartialAndPivotQueries(t *testing.T) {
	for _, kind := range []document.DashboardVisualType{document.DashboardVisualTypePie, document.DashboardVisualTypeKpi, document.DashboardVisualTypePivot, document.DashboardVisualTypeTable} {
		t.Run(string(kind), func(t *testing.T) {
			doc, model := fixVisualFixture()
			model.Dimensions["region"] = semanticmodel.SemanticDimension{Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.country"}}}
			visual := doc.Spec.Visuals["empty"]
			visual.Type = kind
			switch kind {
			case document.DashboardVisualTypeKpi:
				visual.Presentation = document.DashboardPresentation{Value: &document.KPIDashboardPresentation{Type: "kpi"}}
			case document.DashboardVisualTypePivot:
				visual.Query = document.DashboardQuery{Value: &document.PivotDashboardQuery{Type: "pivot"}}
				visual.Presentation = document.DashboardPresentation{Value: &document.TableDashboardPresentation{Type: "table", RowHeight: 32, ShowHeader: true}}
			case document.DashboardVisualTypeTable:
				visual.Query = document.DashboardQuery{Value: &document.RecordsDashboardQuery{Type: "records", Dataset: "pending_dataset"}}
				visual.Presentation = document.DashboardPresentation{Value: &document.TableDashboardPresentation{Type: "table", RowHeight: 32, ShowHeader: true}}
			}
			doc.Spec.Visuals["empty"] = visual
			fields, err := missingVisualFields(doc, "overview", model)
			if err != nil {
				t.Fatal(err)
			}
			if len(fields) == 0 {
				t.Fatal("chart remains unfinished")
			}
			fixed, err := authoring.WithAssignedVisualFields(doc, fields)
			if err != nil {
				t.Fatal(err)
			}
			if !previewableVisual(fixed, "empty", model) {
				t.Fatal("resolved chart cannot render")
			}
		})
	}
}

func TestFixVisualsPrefersCategoricalBreakdownAndPreservesMetricAlias(t *testing.T) {
	doc, model := fixVisualFixture()
	table := model.Tables["orders"]
	table.Dimensions["finance_month"] = semanticmodel.MetricDimension{Type: "string", Datatype: semanticmodel.DataTypeString}
	model.Tables["orders"] = table
	model.Dimensions["finance_month"] = semanticmodel.SemanticDimension{Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.finance_month"}}}
	day := "finance_month"
	doc.Spec.Visuals["ready"].Query.Value.(*document.AggregateDashboardQuery).Dimensions = []document.DashboardDimensionSelection{{String: &day}}
	alias := "existing_revenue"
	query := doc.Spec.Visuals["empty"].Query.Value.(*document.AggregateDashboardQuery)
	query.Metrics = []document.DashboardMetricSelection{{Reference: &document.DashboardMetricReference{Metric: "revenue", Alias: &alias}}}
	fields, err := missingVisualFields(doc, "overview", model)
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 1 || fields[0].FieldID != "country" {
		t.Fatalf("expected missing category, got %#v", fields)
	}
	fixed, err := authoring.WithAssignedVisualFields(doc, fields)
	if err != nil {
		t.Fatal(err)
	}
	saved := fixed.Spec.Visuals["empty"].Query.Value.(*document.AggregateDashboardQuery)
	if saved.Metrics[0].Reference == nil || *saved.Metrics[0].Reference.Alias != alias {
		t.Fatal("existing measure alias changed")
	}
}

func TestFixVisualsCompletesScatterAxesAndIdentity(t *testing.T) {
	doc, model := fixVisualFixture()
	table := model.Tables["orders"]
	table.Dimensions["budget"] = semanticmodel.MetricDimension{Datatype: semanticmodel.DataTypeDecimal}
	model.Tables["orders"] = table
	model.Metrics["budget"] = semanticmodel.Metric{Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.budget"}}
	visual := doc.Spec.Visuals["empty"]
	visual.Type = document.DashboardVisualTypeScatter
	visual.Presentation = document.DashboardPresentation{Value: &document.PointDashboardPresentation{Type: "point", Identity: []string{"pending_identity"}, X: "pending_x", Y: "pending_y"}}
	doc.Spec.Visuals["empty"] = visual
	fields, err := missingVisualFields(doc, "overview", model)
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 3 {
		t.Fatalf("scatter remains incomplete: %#v", fields)
	}
	fixed, err := authoring.WithAssignedVisualFields(doc, fields)
	if err != nil {
		t.Fatal(err)
	}
	point := fixed.Spec.Visuals["empty"].Presentation.Value.(*document.PointDashboardPresentation)
	if point.X != "revenue" || point.Y != "budget" || !reflect.DeepEqual(point.Identity, []string{"country"}) {
		t.Fatalf("unresolved scatter presentation: %#v", point)
	}
	if !previewableVisual(fixed, "empty", model) {
		t.Fatal("completed scatter cannot render")
	}
}

func TestFixVisualsCompletesEmptyCartesianCharts(t *testing.T) {
	for _, kind := range []document.DashboardVisualType{document.DashboardVisualTypeBar, document.DashboardVisualTypeColumn, document.DashboardVisualTypeLine, document.DashboardVisualTypeArea, document.DashboardVisualTypeCombo} {
		t.Run(string(kind), func(t *testing.T) {
			doc, model := fixVisualFixture()
			visual := doc.Spec.Visuals["empty"]
			visual.Type = kind
			visual.Presentation = document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian"}}
			doc.Spec.Visuals["empty"] = visual
			fields, err := missingVisualFields(doc, "overview", model)
			if err != nil {
				t.Fatal(err)
			}
			fixed, err := authoring.WithAssignedVisualFields(doc, fields)
			if err != nil {
				t.Fatal(err)
			}
			if !previewableVisual(fixed, "empty", model) {
				t.Fatalf("%s remains unrenderable with fields %+v", kind, fields)
			}
		})
	}
}
