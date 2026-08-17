package compiler

import (
	"reflect"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
)

func TestValidateAndNormalizeDashboardNormalizesCloneWithoutMutatingAuthoredInput(t *testing.T) {
	authored := dashboardValidationNormalizationFixture()
	before, err := authored.Clone()
	if err != nil {
		t.Fatalf("Clone() error = %v", err)
	}
	model := dashboardValidationNormalizationModel()

	normalized, err := ValidateAndNormalizeDashboard(authored, map[string]*semanticmodel.Model{"model": model})
	if err != nil {
		t.Fatalf("ValidateAndNormalizeDashboard() error = %v", err)
	}
	if reflect.DeepEqual(normalized, authored) {
		t.Fatal("normalized dashboard did not contain compiler-owned table fields")
	}
	if got := normalized.Visuals["orders"].Tabular.DataColumns; len(got) != 1 || got[0].Alias != "status" {
		t.Fatalf("normalized data columns = %#v", got)
	}
	if got := normalized.Visuals["orders"].Tabular.Columns; len(got) != 1 || got[0].Key != "status" {
		t.Fatalf("normalized table columns = %#v", got)
	}
	if !reflect.DeepEqual(*authored, before) {
		t.Fatalf("ValidateAndNormalizeDashboard mutated authored input:\nbefore=%#v\nafter=%#v", before, *authored)
	}
}

func TestValidateAndNormalizeDashboardNormalizationIsDeterministic(t *testing.T) {
	authored := dashboardValidationNormalizationFixture()
	model := dashboardValidationNormalizationModel()

	first, err := ValidateAndNormalizeDashboard(authored, map[string]*semanticmodel.Model{"model": model})
	if err != nil {
		t.Fatalf("first ValidateAndNormalizeDashboard() error = %v", err)
	}
	second, err := ValidateAndNormalizeDashboard(authored, map[string]*semanticmodel.Model{"model": model})
	if err != nil {
		t.Fatalf("second ValidateAndNormalizeDashboard() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated normalization differs:\nfirst=%#v\nsecond=%#v", first, second)
	}
	firstVisualizations, err := CompileVisualizationDefinitions(first, model)
	if err != nil {
		t.Fatalf("first compilation error = %v", err)
	}
	secondVisualizations, err := CompileVisualizationDefinitions(second, model)
	if err != nil {
		t.Fatalf("second compilation error = %v", err)
	}
	if !reflect.DeepEqual(firstVisualizations, secondVisualizations) {
		t.Fatalf("repeated compilation differs:\nfirst=%#v\nsecond=%#v", firstVisualizations, secondVisualizations)
	}
}

func dashboardValidationNormalizationFixture() *dashboardauthoring.Dashboard {
	return &dashboardauthoring.Dashboard{
		ID: "orders", Title: "Orders", SemanticModel: "model",
		FilterDefinitions: map[string]dashboardfilter.Definition{
			"status": {
				Label: "Status", Field: "orders.status",
				Predicates: []dashboardfilter.PredicatePolicy{{Kind: dashboardfilter.ExpressionSet, Operators: []dashboardfilter.Operator{dashboardfilter.OperatorIn}}},
				Options:    dashboardfilter.OptionSource{Kind: dashboardfilter.OptionSourceStatic, Values: []dashboardfilter.Option{{Value: dashboardfilter.Value{Kind: dashboardfilter.ValueString, Value: "new"}, Label: "New"}}},
			},
		},
		Visuals: dashboardauthoring.TabularVisualizations("table", map[string]dashboardauthoring.TableVisual{
			"orders": {Title: "Orders", Query: dashboardauthoring.TableQuery{Dataset: "orders", Fields: []string{"orders.status"}, Metrics: []dashboardauthoring.FieldRef{{Field: "order_count", Alias: "order_count"}}}},
		}),
		Pages: []dashboard.Page{{ID: "overview", Title: "Overview", Visuals: []dashboard.PageVisual{{ID: "orders", Kind: "visual", Visual: "orders", Placement: dashboard.PagePlacement{Col: 1, Row: 1, ColSpan: 4, RowSpan: 4}}}}},
	}
}

func dashboardValidationNormalizationModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name:     "model",
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Tables: map[string]semanticmodel.Table{"orders": {
			ModelName:   "orders",
			GrainEntity: "status",
			Entities: map[string]semanticmodel.ModelEntitySpec{
				"status": {Type: "primary", Fields: []string{"status"}},
			},
			Dimensions: map[string]semanticmodel.MetricDimension{
				"status": {Field: "orders.status", Type: "string", Datatype: semanticmodel.DataTypeString},
			},
		}},
		Metrics: map[string]semanticmodel.Metric{"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.status"}, Empty: "zero"}},
	}
}
