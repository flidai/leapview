package compiler

import (
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestCompiledKPIFieldRetainsSemanticPresentation(t *testing.T) {
	model := &semanticmodel.Model{Metrics: map[string]semanticmodel.Metric{
		"revenue": {Label: "Revenue", Aggregation: "sum", Unit: "R$", Format: "currency"},
	}}
	authored := dashboardauthoring.Visual{Type: "kpi", Query: dashboardauthoring.VisualQuery{
		Metrics: []dashboardauthoring.FieldRef{{Field: "revenue"}},
	}}

	spec, err := compileBuiltInVisualizationSpec("revenue", authored, model)
	if err != nil {
		t.Fatalf("compileBuiltInVisualizationSpec() error = %v", err)
	}
	kpi, ok := spec.Value.(*visualizationir.KPIVisualizationSpec)
	if !ok {
		t.Fatalf("spec = %T, want KPIVisualizationSpec", spec.Value)
	}
	value := kpi.Datasets[0].Fields[1]
	if value.Label != "Revenue" || value.SourceRef == nil || *value.SourceRef != "revenue" {
		t.Fatalf("value semantic identity = %#v, want Revenue sourced from revenue", value)
	}
	if value.Format == nil {
		t.Fatal("value format is nil, want BRL currency")
	}
	format, ok := value.Format.Value.(*visualizationir.CurrencyVisualizationFormat)
	if !ok || format.Currency != "BRL" {
		t.Fatalf("value format = %#v, want BRL currency", value.Format)
	}
}

func TestCompiledCategoricalFieldRetainsStringType(t *testing.T) {
	model := &semanticmodel.Model{
		Tables: map[string]semanticmodel.Table{
			"orders": {Dimensions: map[string]semanticmodel.MetricDimension{"month": {Label: "Month", Type: "string"}}},
		},
		Metrics: map[string]semanticmodel.Metric{"revenue": {Aggregation: "sum", Format: "currency"}},
	}
	authored := dashboardauthoring.Visual{Type: "line", Query: dashboardauthoring.VisualQuery{
		Dimensions: []dashboardauthoring.FieldRef{{Field: "orders.month"}}, Metrics: []dashboardauthoring.FieldRef{{Field: "revenue"}},
	}}
	spec, err := compileBuiltInVisualizationSpec("revenue", authored, model)
	if err != nil {
		t.Fatal(err)
	}
	chart := spec.Value.(*visualizationir.CartesianVisualizationSpec)
	if got := chart.Datasets[0].Fields[0].DataType; got != visualizationir.VisualizationDataTypeString {
		t.Fatalf("category data type = %q, want string", got)
	}
}

func TestCompiledGaugeRetainsTruthfulDomainAndTarget(t *testing.T) {
	minimum, maximum, target := 0.0, 5.0, 4.5
	authored := dashboardauthoring.Visual{
		Type: "gauge",
		Presentation: dashboardauthoring.VisualPresentation{
			Minimum: &minimum,
			Maximum: &maximum,
			Target:  &target,
		},
		Query: dashboardauthoring.VisualQuery{
			Metrics: []dashboardauthoring.FieldRef{{Field: "review_score"}},
		},
	}

	spec, err := compileBuiltInVisualizationSpec("review", authored, nil)
	if err != nil {
		t.Fatalf("compileBuiltInVisualizationSpec() error = %v", err)
	}
	gauge, ok := spec.Value.(*visualizationir.PolarVisualizationSpec)
	if !ok {
		t.Fatalf("spec = %T, want PolarVisualizationSpec", spec.Value)
	}
	if gauge.Presentation.Minimum == nil || *gauge.Presentation.Minimum != minimum {
		t.Fatalf("minimum = %v, want %v", gauge.Presentation.Minimum, minimum)
	}
	if gauge.Presentation.Maximum == nil || *gauge.Presentation.Maximum != maximum {
		t.Fatalf("maximum = %v, want %v", gauge.Presentation.Maximum, maximum)
	}
	if gauge.Presentation.Target == nil || *gauge.Presentation.Target != target {
		t.Fatalf("target = %v, want %v", gauge.Presentation.Target, target)
	}
}

func TestCompiledMultiMeasureValueDoesNotClaimOneMeasureFormat(t *testing.T) {
	model := &semanticmodel.Model{
		Tables: map[string]semanticmodel.Table{"orders": {Dimensions: map[string]semanticmodel.MetricDimension{"month": {Type: "string"}}}},
		Metrics: map[string]semanticmodel.Metric{
			"revenue": {Aggregation: "sum", Format: "currency"},
			"orders":  {Aggregation: "count", Format: "integer"},
		},
	}
	authored := dashboardauthoring.Visual{Type: "combo", Query: dashboardauthoring.VisualQuery{
		Dimensions: []dashboardauthoring.FieldRef{{Field: "orders.month"}},
		Metrics:    []dashboardauthoring.FieldRef{{Field: "revenue"}, {Field: "orders"}},
	}}
	spec, err := compileBuiltInVisualizationSpec("summary", authored, model)
	if err != nil {
		t.Fatal(err)
	}
	chart := spec.Value.(*visualizationir.CartesianVisualizationSpec)
	value := chart.Datasets[0].Fields[2]
	if value.ID != "value" || value.SourceRef != nil || value.Format != nil {
		t.Fatalf("heterogeneous value field = %#v, want renderer-neutral unformatted value", value)
	}
}

func TestCompiledDimensionFormatPreservesSemanticScalarTypes(t *testing.T) {
	t.Parallel()
	for semanticType, want := range map[string]string{
		"string": "", "number": "decimal", "boolean": "boolean", "date": "date", "timestamp": "timestamp",
	} {
		if got := compiledDimensionFormat(semanticType); got != want {
			t.Errorf("compiledDimensionFormat(%q) = %q, want %q", semanticType, got, want)
		}
	}
}

func TestCompiledHierarchyRejectsReservedFrameAliases(t *testing.T) {
	t.Parallel()
	authored := dashboardauthoring.Visual{Title: "Hierarchy", Type: "tree", Query: dashboardauthoring.VisualQuery{
		Dimensions: []dashboardauthoring.FieldRef{{Field: "orders.category", Alias: "node"}},
		Metrics:    []dashboardauthoring.FieldRef{{Field: "order_count", Alias: "value"}},
	}}
	_, err := compileBuiltInVisualizationSpec("hierarchy", authored, nil)
	if err == nil || !strings.Contains(err.Error(), `alias "node" conflicts with a reserved frame field`) {
		t.Fatalf("compileBuiltInVisualizationSpec() error = %v", err)
	}
}

func TestCompiledHierarchyFrameBudgetAccountsForMaterializedAncestors(t *testing.T) {
	t.Parallel()

	authored := dashboardauthoring.Visual{Title: "Hierarchy", Type: "treemap", Query: dashboardauthoring.VisualQuery{
		Dimensions: []dashboardauthoring.FieldRef{{Field: "orders.category", Alias: "category"}, {Field: "orders.status", Alias: "status"}},
		Metrics:    []dashboardauthoring.FieldRef{{Field: "order_count", Alias: "order_count"}},
		Limit:      80,
	}}
	spec, err := compileBuiltInVisualizationSpec("hierarchy", authored, nil)
	if err != nil {
		t.Fatal(err)
	}
	base, err := visualizationir.SpecificationBase(spec)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := base.DataBudget.MaxRows, int64(160); got != want {
		t.Fatalf("hierarchy frame budget = %d, want %d", got, want)
	}
}

func TestCompiledMultiMeasureFrameBudgetAccountsForNormalizedSeriesRows(t *testing.T) {
	t.Parallel()

	authored := dashboardauthoring.Visual{Title: "Revenue and orders", Type: "combo", Query: dashboardauthoring.VisualQuery{
		Dimensions: []dashboardauthoring.FieldRef{{Field: "orders.month", Alias: "month"}},
		Metrics: []dashboardauthoring.FieldRef{
			{Field: "revenue", Alias: "revenue"},
			{Field: "order_count", Alias: "order_count"},
		},
		Limit: 30,
	}}
	spec, err := compileBuiltInVisualizationSpec("combo", authored, nil)
	if err != nil {
		t.Fatal(err)
	}
	base, err := visualizationir.SpecificationBase(spec)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := base.DataBudget.MaxRows, int64(60); got != want {
		t.Fatalf("multi-metric frame budget = %d, want %d", got, want)
	}
}

func TestCompiledPhysicalFieldFormatUsesMeasureSemanticsWhenModelTypeIsUnknown(t *testing.T) {
	model := &semanticmodel.Model{Metrics: map[string]semanticmodel.Metric{
		"revenue": {Input: &semanticmodel.MetricInput{Field: "orders.revenue"}, Aggregation: "sum", Format: "currency"},
	}}
	if got := compiledPhysicalFieldFormat(model, "orders.revenue", ""); got != "currency" {
		t.Fatalf("compiledPhysicalFieldFormat = %q, want currency", got)
	}
}

func TestCompiledPhysicalFieldFormatDoesNotTreatCountIdentityAsNumeric(t *testing.T) {
	model := &semanticmodel.Model{Metrics: map[string]semanticmodel.Metric{
		"orders": {Input: &semanticmodel.MetricInput{Field: "orders.order_id"}, Aggregation: "count_distinct"},
	}}
	if got := compiledPhysicalFieldFormat(model, "orders.order_id", ""); got != "" {
		t.Fatalf("compiledPhysicalFieldFormat = %q, want string default", got)
	}
}
