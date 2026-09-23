package http

import (
	"net/http"
	"net/http/httptest"
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

func TestDataExplorerSemanticExploreRejectsUnprojectedOperandsWithoutExecution(t *testing.T) {
	model := &semanticmodel.Model{
		Tables: map[string]semanticmodel.Table{
			"orders": {Dimensions: map[string]semanticmodel.MetricDimension{
				"status":     {Field: "orders.status", Type: "string", Datatype: semanticmodel.DataTypeString},
				"created_at": {Field: "orders.created_at", Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ},
			}},
		},
		Metrics:  map[string]semanticmodel.Metric{"order_count": {Type: "aggregate"}},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
	}

	baseSpec := func() exploration.ExplorationSpec {
		dataset := "orders"
		return exploration.ExplorationSpec{
			SchemaVersion: 1, ModelID: "semantic-model:sales", DatasetID: &dataset,
			Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}},
			Metrics:    []exploration.ExplorationMetricRef{{Field: "order_count"}},
			Filters:    []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100,
		}
	}
	baseFields := []projectsignals.DataExploreFieldSignal{
		{ID: "orders.status", Kind: "dimension", Compatible: true},
		{ID: "order_count", Kind: "metric", Compatible: true},
	}
	tests := []struct {
		name   string
		spec   func() exploration.ExplorationSpec
		fields []projectsignals.DataExploreFieldSignal
		want   string
	}{
		{
			name: "removed dimension",
			spec: func() exploration.ExplorationSpec {
				spec := baseSpec()
				spec.Dimensions = []exploration.ExplorationDimensionRef{{Field: "orders.created_at"}}
				return spec
			},
			fields: baseFields,
			want:   "dimension 1 field \"orders.created_at\" is unavailable",
		},
		{
			name:   "incompatible metric",
			spec:   baseSpec,
			fields: []projectsignals.DataExploreFieldSignal{{ID: "orders.status", Kind: "dimension", Compatible: true}, {ID: "order_count", Kind: "metric", Compatible: false}},
			want:   "metric 1 field \"order_count\" is incompatible",
		},
		{
			name: "filter wrong kind",
			spec: func() exploration.ExplorationSpec {
				spec := baseSpec()
				spec.Dimensions = []exploration.ExplorationDimensionRef{}
				spec.Metrics = []exploration.ExplorationMetricRef{}
				spec.Filters = []exploration.ExplorationFilter{{
					Field:      "orders.status",
					Expression: exploration.ExplorationFilterExpression{Value: &exploration.UnfilteredExplorationFilterExpression{Kind: "unfiltered"}},
				}}
				return spec
			},
			fields: []projectsignals.DataExploreFieldSignal{{ID: "orders.status", Kind: "metric", Compatible: true}},
			want:   "filter 1 field \"orders.status\" has kind \"metric\", want \"dimension\"",
		},
		{
			name: "removed time field",
			spec: func() exploration.ExplorationSpec {
				spec := baseSpec()
				spec.Time = &exploration.ExplorationTimeSelection{Field: "orders.created_at", Grain: exploration.ExplorationTimeGrainDay}
				return spec
			},
			fields: baseFields,
			want:   "time field \"orders.created_at\" is unavailable",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := &browserDataQueryStub{}
			_, result := dataExplorerSemanticResult(t.Context(), executor, "project:test", projectsignals.DataExploreCommand{Spec: test.spec()}, test.fields, model)
			if result.Error == nil || !strings.Contains(*result.Error, test.want) {
				t.Fatalf("result error = %#v, want %q", result.Error, test.want)
			}
			if executor.calls != 0 {
				t.Fatalf("rejected %s executed %d analytical queries, want 0", test.name, executor.calls)
			}
		})
	}
}

func TestDataExplorerSemanticExplorePreservesTimeSortAndAlias(t *testing.T) {
	model := &semanticmodel.Model{
		Tables: map[string]semanticmodel.Table{
			"orders": {Dimensions: map[string]semanticmodel.MetricDimension{
				"created_at": {Field: "orders.created_at", Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ},
			}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
	}
	alias := "order_day"
	dataset := "orders"
	executor := &browserDataQueryStub{}
	_, result := dataExplorerSemanticResult(t.Context(), executor, "project:test", projectsignals.DataExploreCommand{Spec: exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic-model:sales", DatasetID: &dataset,
		Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{}, Filters: []exploration.ExplorationFilter{},
		Time: &exploration.ExplorationTimeSelection{Field: "orders.created_at", Grain: exploration.ExplorationTimeGrainDay, Alias: &alias},
		Sort: []exploration.ExplorationSort{{Field: alias, Direction: exploration.ExplorationSortDirectionAsc}}, Limit: 100,
	}}, []projectsignals.DataExploreFieldSignal{{ID: "orders.created_at", Kind: "dimension", Compatible: true}}, model)
	if result.Error != nil {
		t.Fatalf("time-only exploration result error = %q", *result.Error)
	}
	if executor.calls != 1 {
		t.Fatalf("time-only exploration executed %d queries, want 1", executor.calls)
	}
	if len(executor.query.Sort) != 1 || executor.query.Sort[0].Field != alias || executor.query.Sort[0].Direction != "asc" {
		t.Fatalf("query sort = %#v, want time alias sort", executor.query.Sort)
	}
	if executor.query.Time.Field != "orders.created_at" || executor.query.Time.Alias != alias {
		t.Fatalf("query time = %#v, want authored time alias", executor.query.Time)
	}
}

func TestDataExplorerSignalsForCommandRejectsUnavailableModelOrDataset(t *testing.T) {
	tests := []struct {
		name      string
		spec      exploration.ExplorationSpec
		wantModel string
		wantData  string
	}{
		{
			name: "model",
			spec: exploration.ExplorationSpec{
				ModelID: "semantic:missing", DatasetID: projectsignals.Optional("orders"),
				Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}},
			},
			wantModel: "semantic:missing", wantData: "orders",
		},
		{
			name: "dataset",
			spec: exploration.ExplorationSpec{
				ModelID: "semantic:sales", DatasetID: projectsignals.Optional("missing"),
				Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}},
			},
			wantModel: "semantic:sales", wantData: "missing",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h, executor := newDataExplorerURLTestHandler(t)
			explore := testExplorationCommand(test.spec)
			command := projectsignals.DataExplorerCommand{Mode: projectsignals.Pointer("explore"), Explore: &explore}
			recorder := httptest.NewRecorder()
			_, explorer, ok := h.dataExplorerSignalsForCommand(recorder, httptest.NewRequest(http.MethodGet, "/explore/command", nil), command)
			if !ok {
				t.Fatalf("interactive command failed at HTTP boundary: status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if explorer.Explore.Result.Error == nil {
				t.Fatalf("unavailable %s produced no exploration error: %#v", test.name, explorer.Explore)
			}
			if explorer.Explore.Command.Spec.ModelID != test.wantModel || projectsignals.ValueOrZero(explorer.Explore.Command.Spec.DatasetID) != test.wantData {
				t.Fatalf("unavailable %s was replaced: %#v", test.name, explorer.Explore.Command.Spec)
			}
			if executor.calls != 0 {
				t.Fatalf("unavailable %s executed %d analytical queries, want 0", test.name, executor.calls)
			}
		})
	}
}
