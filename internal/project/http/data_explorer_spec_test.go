package http

import (
	"net/url"
	"strings"
	"testing"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func TestDataExploreCommandFromQueryAcceptsLegacyAndRejectsMalformedState(t *testing.T) {
	legacy, err := dataExploreCommandFromQuery(url.Values{"model": {"semantic:sales"}, "dataset": {"orders"}})
	if err != nil || legacy.Spec.Limit != int32(dataExplorerDefaultLimit) || legacy.Spec.Dimensions == nil || legacy.Spec.Metrics == nil {
		t.Fatalf("legacy query = %#v, %v", legacy, err)
	}

	tests := []url.Values{
		{"v": {"2"}},
		{"v": {"2"}, "mode": {"explore"}, "state": {`{"schemaVersion":1,"modelId":"semantic:sales","dimensions":[{"field":"orders.status","FIELD":"orders.status"}],"metrics":[],"filters":[],"sort":[],"limit":100}`}},
		{"limit": {"0"}},
		{"limit": {"1001"}},
		{"filter": {`{"field":"status","operator":"equals","values":[],"unexpected":true}`}},
		{"sort": {`{"field":"revenue","direction":"sideways"}`}},
		{"time": {`{"field":"created_at","grain":"month"} trailing`}},
	}
	for _, values := range tests {
		if command, err := dataExploreCommandFromQuery(values); err == nil {
			t.Fatalf("query %#v accepted as %#v", values, command)
		}
	}
}

func TestDataExplorerSemanticExploreRejectsMalformedSpecWithoutExecution(t *testing.T) {
	executor := &browserDataQueryStub{}
	badAlias := "status label"
	_, result := dataExplorerSemanticResult(t.Context(), executor, "project:test", projectsignals.DataExploreCommand{
		Spec: exploration.ExplorationSpec{
			SchemaVersion: 1, ModelID: "semantic-model:sales", DatasetID: projectsignals.Pointer("orders"),
			Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status", Alias: &badAlias}},
			Metrics:    []exploration.ExplorationMetricRef{}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100,
		},
	}, []projectsignals.DataExploreFieldSignal{{ID: "orders.status", Kind: "dimension", Compatible: true}}, nil)
	if result.Error == nil || !strings.Contains(*result.Error, "invalid exploration command") {
		t.Fatalf("malformed command error = %#v, want actionable shape diagnostic", result.Error)
	}
	if executor.calls != 0 {
		t.Fatalf("malformed command executed %d analytical queries, want 0", executor.calls)
	}
}

func TestDataExplorerSemanticExploreRejectsMissingRequiredArraysWithoutExecution(t *testing.T) {
	executor := &browserDataQueryStub{}
	_, result := dataExplorerSemanticResult(t.Context(), executor, "project:test", projectsignals.DataExploreCommand{
		Spec: exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic-model:sales", Limit: 100},
	}, nil, nil)
	if result.Error == nil || !strings.Contains(*result.Error, "required") {
		t.Fatalf("missing arrays error = %#v, want required-array diagnostic", result.Error)
	}
	if executor.calls != 0 {
		t.Fatalf("missing arrays command executed %d analytical queries, want 0", executor.calls)
	}
}

func TestDataExplorerSemanticExploreRejectsPivotWithoutExecution(t *testing.T) {
	executor := &browserDataQueryStub{}
	_, result := dataExplorerSemanticResult(t.Context(), executor, "project:test", projectsignals.DataExploreCommand{
		Spec: exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic-model:sales", Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100, Pivot: &exploration.ExplorationPivotConfig{}},
	}, nil, &semanticmodel.Model{})
	if result.Error == nil || !strings.Contains(*result.Error, "pivot exploration execution is not supported") {
		t.Fatalf("pivot result error = %#v, want explicit unsupported diagnostic", result.Error)
	}
	if executor.calls != 0 {
		t.Fatalf("pivot command executed %d analytical queries, want 0", executor.calls)
	}
}

func TestDataExplorerSemanticExploreRejectsTypedOperatorBeforeExecution(t *testing.T) {
	model := &semanticmodel.Model{
		Tables: map[string]semanticmodel.Table{
			"orders": {ModelName: "orders", Dimensions: map[string]semanticmodel.MetricDimension{
				"status": {Field: "orders.status", Type: "string", Datatype: semanticmodel.DataTypeString},
			}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
	}
	compiled, err := semanticquery.CompileDatasetBindings(model)
	if err != nil {
		t.Fatal(err)
	}
	fields := explorerFields(model, "orders", dataExploreState{Dimensions: []string{"orders.status"}}, compiled)
	executor := &browserDataQueryStub{}
	dataset := "orders"
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic-model:sales", DatasetID: &dataset,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}},
		Metrics:    []exploration.ExplorationMetricRef{},
		Filters: []exploration.ExplorationFilter{{
			Field: "orders.status",
			Expression: exploration.ExplorationFilterExpression{Value: &exploration.ComparisonExplorationFilterExpression{
				Kind: "comparison", Operator: "greater_than",
				Value: exploration.ExplorationFilterValue{Value: &exploration.StringExplorationFilterValue{Kind: "string", Value: "z"}},
			}},
		}},
		Sort: []exploration.ExplorationSort{}, Limit: 100,
	}
	_, result := dataExplorerSemanticResult(t.Context(), executor, "project:test", projectsignals.DataExploreCommand{Spec: spec}, fields, model)
	if result.Error == nil || !strings.Contains(*result.Error, "incompatible") {
		t.Fatalf("typed operator result = %#v, want semantic incompatibility", result.Error)
	}
	if executor.calls != 0 {
		t.Fatalf("typed operator executed %d analytical queries, want 0", executor.calls)
	}
}
