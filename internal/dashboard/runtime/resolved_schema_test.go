package runtime

import (
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestResolveDashboardSchemasUsesDiscoveredMetricTypes(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{"orders": {
			Dimensions: map[string]semanticmodel.MetricDimension{
				"order_id": {Field: "orders.order_id", Table: "orders", Name: "order_id", Datatype: semanticmodel.DataTypeString},
				"revenue":  {Field: "orders.revenue", Table: "orders", Name: "revenue", Type: "number", Datatype: semanticmodel.DataTypeFloat},
			},
		}},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Metrics: map[string]semanticmodel.Metric{
			"revenue":             {Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.revenue"}},
			"order_count":         {Type: "aggregate", Dataset: "orders", Aggregation: "count_distinct", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}},
			"average_order_value": {Type: "ratio", Numerator: "revenue", Denominator: "order_count"},
		},
	}
	spec := visualizationir.VisualizationSpec{Value: &visualizationir.KPIVisualizationSpec{
		VisualizationSpecBase: visualizationir.VisualizationSpecBase{
			Kind: "kpi", Title: "Average order value",
			Datasets:      []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: []visualizationir.VisualizationField{{ID: "value", SourceRef: stringPointer("average_order_value"), Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal, Label: "Average order value"}}}},
			DataBudget:    visualizationir.VisualizationDataBudget{MaxRows: 1, RequiredCompleteness: visualizationir.VisualizationCompletenessComplete},
			Accessibility: visualizationir.VisualizationAccessibility{Title: "Average order value", Description: "Average order value"},
		},
		Kind: "kpi", Value: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "value"},
		Presentation: visualizationir.KPIVisualizationPresentation{Mode: visualizationir.VisualizationKPIModeCompact, Delta: visualizationir.VisualizationKPIDeltaModeAbsolute, FavorableDirection: visualizationir.VisualizationKPIDirectionNeutral, MissingComparison: visualizationir.VisualizationKPIMissingComparisonShowUnavailable},
	}}
	query := visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultScalar, ModelID: "sales", DatasetID: "primary", Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: "orders", Metrics: []visualizationdefinition.FieldBinding{{FieldID: "average_order_value", Alias: "value"}}, Limit: 1}}
	visual, err := visualizationdefinition.New("average_order_value", spec, query)
	if err != nil {
		t.Fatal(err)
	}
	before := visual.SpecRevision
	dashboard := dashboarddefinition.Definition{ID: "dashboard:sales", SemanticModel: "sales", Visualizations: map[string]visualizationdefinition.Definition{"average_order_value": visual}}
	resolved, err := resolveDashboardSchemas(dashboard, model)
	if err != nil {
		t.Fatal(err)
	}
	got := resolved.Visualizations["average_order_value"]
	base, err := got.Spec.Base()
	if err != nil {
		t.Fatal(err)
	}
	if datatype := base.Datasets[0].Fields[0].DataType; datatype != visualizationir.VisualizationDataTypeFloat {
		t.Fatalf("resolved metric datatype = %q, want float", datatype)
	}
	if got.SpecRevision == before {
		t.Fatal("resolved specification revision did not change")
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("resolved visualization definition: %v", err)
	}
}

func stringPointer(value string) *string { return &value }
