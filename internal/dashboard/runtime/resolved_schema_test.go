package runtime

import (
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
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

func TestResolveVisualizationSchemaRecomputesCalculationTypesAndEvaluation(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{"orders": {
			Dimensions: map[string]semanticmodel.MetricDimension{
				"period": {Field: "orders.period", Table: "orders", Name: "period", Datatype: semanticmodel.DataTypeString},
				"amount": {Field: "orders.amount", Table: "orders", Name: "amount", Type: "number", Datatype: semanticmodel.DataTypeFloat},
			},
		}},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Metrics: map[string]semanticmodel.Metric{
			"amount": {Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.amount"}},
		},
	}
	running := visualizationir.VisualizationCalculation{
		ID: "running", Label: "Running", Dataset: "primary", Template: visualizationir.VisualizationCalculationTemplateRunningTotal,
		Source: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "value"}, Axis: visualizationir.VisualizationCalculationAxisRows,
		Reset:   visualizationir.VisualizationCalculationResetNone,
		OrderBy: []visualizationir.VisualizationCalculationOrder{{Field: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "value"}, Direction: visualizationir.VisualizationSortDirectionAscending}},
	}
	difference := visualizationir.VisualizationCalculation{
		ID: "difference", Label: "Difference", Dataset: "primary", Template: visualizationir.VisualizationCalculationTemplateDifference,
		Source: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "running"}, Axis: visualizationir.VisualizationCalculationAxisRows,
		Reset:   visualizationir.VisualizationCalculationResetNone,
		OrderBy: []visualizationir.VisualizationCalculationOrder{{Field: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "running"}, Direction: visualizationir.VisualizationSortDirectionAscending}},
	}
	calculationID := func(value string) *string { return &value }
	base := canonicalBase("cartesian", "Calculated sales", []visualizationir.VisualizationField{
		{ID: "period", SourceRef: stringPointer("period"), Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeString, Label: "Period"},
		{ID: "value", SourceRef: stringPointer("amount"), Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal, Label: "Amount"},
		{ID: "difference", Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal, Nullable: true, Label: "Difference", Provenance: &visualizationir.VisualizationFieldProvenance{Kind: visualizationir.VisualizationFieldProvenanceKindVisualCalculation, CalculationID: calculationID("difference")}},
		{ID: "running", Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal, Nullable: true, Label: "Running", Provenance: &visualizationir.VisualizationFieldProvenance{Kind: visualizationir.VisualizationFieldProvenanceKindVisualCalculation, CalculationID: calculationID("running")}},
	})
	base.Calculations = &[]visualizationir.VisualizationCalculation{difference, running}
	spec := visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{
		VisualizationSpecBase: base, Kind: "cartesian", Mark: visualizationir.VisualizationCartesianMarkLine,
		X: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "period"},
		Y: []visualizationir.VisualizationFieldRef{{Dataset: "primary", Field: "difference"}, {Dataset: "primary", Field: "running"}},
		Presentation: visualizationir.CartesianVisualizationPresentation{VisualizationPresentation: visualizationir.VisualizationPresentation{
			Legend:      visualizationir.VisualizationLegendPositionHidden,
			LabelPolicy: visualizationir.VisualizationLabelPolicy{Density: visualizationir.VisualizationLabelDensityHidden, MaxCharacters: 24, TooltipFallback: true},
		}},
	}}
	visual, err := visualizationdefinition.New("calculated-sales", spec, visualizationdefinition.QueryBinding{
		Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultCategoryValue, ModelID: "sales", DatasetID: "primary",
		Aggregate: &visualizationdefinition.AggregateQueryBinding{
			TableID: "orders", Dimensions: []visualizationdefinition.FieldBinding{{FieldID: "period", Alias: "period"}},
			Metrics: []visualizationdefinition.FieldBinding{{FieldID: "amount", Alias: "value"}}, Limit: 100,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveVisualizationSchema(visual, model)
	if err != nil {
		t.Fatalf("resolveVisualizationSchema(): %v", err)
	}
	resolvedBase, err := resolved.Spec.Base()
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range resolvedBase.Datasets[0].Fields {
		if field.ID == "running" || field.ID == "difference" {
			if field.DataType != visualizationir.VisualizationDataTypeFloat {
				t.Fatalf("resolved %s datatype = %q, want float", field.ID, field.DataType)
			}
		}
	}
	frame, _, err := visualizationruntime.ApplyVisualCalculations(*resolvedBase, "primary", visualizationruntime.Frame{
		Columns: []string{"period", "value"}, Rows: [][]any{{"Q1", 10.0}, {"Q2", 20.0}},
	}, visualizationir.VisualizationCompletenessComplete)
	if err != nil {
		t.Fatalf("ApplyVisualCalculations(): %v", err)
	}
	if got := frame.Rows[1][2]; got != 20.0 {
		t.Fatalf("difference output = %#v, want float 20", got)
	}
	if got := frame.Rows[1][3]; got != 30.0 {
		t.Fatalf("running output = %#v, want float 30", got)
	}
}

func stringPointer(value string) *string { return &value }
