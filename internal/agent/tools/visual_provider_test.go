package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboarddocument "github.com/flidai/leapview/internal/dashboard/document"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

func TestAgentVisualInputRejectsLegacyAndUnknownProperties(t *testing.T) {
	for _, property := range []string{"shape", "options", "rendererOptions", "unexpected"} {
		t.Run(property, func(t *testing.T) {
			_, err := decodeAgentVisualInput([]byte(`{"semanticModelId":"sales","visual":{"type":"histogram","query":{"type":"histogram","field":"revenue","bins":20,"nullPolicy":"omit","approximation":"exact"},"presentation":{"type":"cartesian"}},"` + property + `":{}}`))
			if err == nil || !strings.Contains(err.Error(), property) {
				t.Fatalf("decode error = %v, want closed-contract rejection for %q", err, property)
			}
		})
	}
	schema := string((VisualProvider{}).Definitions(Scope{})[0].InputSchema)
	for _, property := range []string{`"shape"`, `"rendererOptions"`} {
		if strings.Contains(schema, property) {
			t.Fatalf("agent schema still exposes legacy property %s", property)
		}
	}
}

func TestAgentVisualQueryRequiresServingSnapshot(t *testing.T) {
	provider := VisualProvider{Resolve: func(_ context.Context, _ Scope, id projectgraph.ResourceID, _ projectgraph.Kind, _ access.Capability) (projectgraph.ResourceID, error) {
		return id, nil
	}, SemanticModel: func(string, string) (*semanticmodel.Model, bool) { return testAgentModel(), true }}
	result := provider.Run(context.Background(), Scope{ProjectID: "project", PrincipalID: "principal"}, agentcore.ToolCall{
		ID:        "query-without-snapshot",
		Arguments: json.RawMessage(`{"semanticModelId":"orders","visual":{"type":"bar","query":{"type":"aggregate","dimensions":["country"],"metrics":["revenue"],"limit":10},"presentation":{"type":"cartesian"},"dataBudget":{"maxRows":50}}}`),
	})
	content, _ := result.Content.(map[string]any)
	failure, _ := content["error"].(map[string]any)
	if !result.IsError || !strings.Contains(failure["message"].(string), "serving snapshot") {
		t.Fatalf("result = %#v; want serving snapshot failure", result)
	}
}

func TestVisualProviderDecoratesQueryContextWithScope(t *testing.T) {
	type contextKey struct{}
	var authorizedValue string
	provider := VisualProvider{
		Resolve: func(_ context.Context, _ Scope, id projectgraph.ResourceID, _ projectgraph.Kind, _ access.Capability) (projectgraph.ResourceID, error) {
			return id, nil
		},
		QueryContext: func(ctx context.Context, scope Scope) context.Context {
			return context.WithValue(ctx, contextKey{}, scope.PrincipalID)
		},
		SemanticModel: func(string, string) (*semanticmodel.Model, bool) { return testAgentModel(), true },
		Authorize: func(ctx context.Context, _ Scope, _ VisualAuthorizationRequest) (agentcore.ToolResult, bool) {
			authorizedValue, _ = ctx.Value(contextKey{}).(string)
			return apigenAgentToolError("authorization_failed", "stop after context capture"), false
		},
	}

	provider.Run(context.Background(), Scope{ProjectID: "project_demo", PrincipalID: "principal-1"}, agentcore.ToolCall{
		ID:        "call-visual",
		Arguments: json.RawMessage(`{"semanticModelId":"orders","visual":{"type":"bar","query":{"type":"aggregate","dimensions":["country"],"metrics":["revenue"],"limit":10},"presentation":{"type":"cartesian"},"dataBudget":{"maxRows":50}}}`),
	})

	if authorizedValue != "principal-1" {
		t.Fatalf("decorated query context principal = %q, want principal-1", authorizedValue)
	}
}

func TestAgentVisualFieldUsagePreservesSemanticUnitsAndFormats(t *testing.T) {
	model := &semanticmodel.Model{
		Metrics: map[string]semanticmodel.Metric{
			"return_rate": {Label: "Return rate", Unit: "percent", Format: "percent_1"},
		},
	}
	got := agentVisualFieldUsage("sales", "commerce", model, agentVisualFieldRef{Field: "return_rate", Alias: "rate"}, "metric")
	if got.Role != "metric" || got.FieldID != "commerce.return_rate" || got.Label != "Return rate" ||
		got.Alias == nil || *got.Alias != "rate" || got.Unit == nil || *got.Unit != "percent" ||
		got.Format == nil || *got.Format != "percent_1" {
		t.Fatalf("field usage = %#v", got)
	}
}

func testAgentVisual(visualType string) dashboarddocument.DashboardVisual {
	if visualType == "histogram" {
		return dashboarddocument.DashboardVisual{Type: dashboarddocument.DashboardVisualTypeHistogram, Query: dashboarddocument.DashboardQuery{Value: &dashboarddocument.HistogramDashboardQuery{Type: "histogram", Field: dashboarddocument.DashboardMetricSelection{String: strPtr("revenue")}, Bins: 20, NullPolicy: dashboarddocument.DashboardHistogramNullPolicyOmit, Approximation: dashboarddocument.DashboardHistogramApproximationExact}}, Presentation: dashboarddocument.DashboardPresentation{Value: &dashboarddocument.CartesianDashboardPresentation{Type: "cartesian"}}}
	}
	return dashboarddocument.DashboardVisual{Type: dashboarddocument.DashboardVisualType(visualType), Query: dashboarddocument.DashboardQuery{Value: &dashboarddocument.AggregateDashboardQuery{Type: "aggregate", Dimensions: []dashboarddocument.DashboardDimensionSelection{{String: strPtr("country")}}, Metrics: []dashboarddocument.DashboardMetricSelection{{String: strPtr("revenue")}}}}, Presentation: dashboarddocument.DashboardPresentation{Value: &dashboarddocument.CartesianDashboardPresentation{Type: "cartesian"}}}
}

func TestAgentVisualDocumentPreservesCanonicalVisualAndSecondaryDatasets(t *testing.T) {
	visual := testAgentVisual("bar")
	secondary := map[string]dashboarddocument.DashboardQuery{"context": visual.Query}
	visual.Datasets = &secondary
	input := agentVisualInput{Visual: visual, Model: "commerce"}
	doc := agentVisualDocument(input, "visual-id", "commerce")
	if !reflect.DeepEqual(doc.Spec.Visuals["visual-id"], visual) {
		t.Fatalf("synthetic document changed canonical visual:\n got %#v\nwant %#v", doc.Spec.Visuals["visual-id"], visual)
	}
	if got := doc.Spec.Visuals["visual-id"].Datasets; got == nil || !reflect.DeepEqual(*got, secondary) {
		t.Fatalf("secondary datasets = %#v, want %#v", got, secondary)
	}
}

func TestAgentVisualQueryUsesCanonicalDefinitionExactlyOnce(t *testing.T) {
	model := testAgentModel()
	input := agentVisualInput{Visual: testAgentVisualWithLimit("bar", 10), Model: "commerce"}
	dashboardDefinition, err := compileAgentVisual(input, model, "visual-id")
	if err != nil {
		t.Fatalf("compileAgentVisual(): %v", err)
	}
	definition := dashboardDefinition.Visualizations["visual-id"]
	wantEnvelope := visualizationir.VisualizationEnvelope{VisualID: "visual-id", RendererID: "canonical"}
	var calls int
	var captured dashboarddefinition.Definition
	var capturedFilters dashboard.Filters
	provider := VisualProvider{QueryDefinition: func(ctx context.Context, _ string, got dashboarddefinition.Definition, pageID, visualID string, filters dashboard.Filters) (visualizationir.VisualizationEnvelope, error) {
		calls++
		captured = got
		capturedFilters = filters
		budget, ok := dataquery.ResultBudgetFromContext(ctx)
		if !ok {
			t.Fatal("canonical runtime context has no independent result budget")
		}
		if err := budget.ConsumeSize(maxVisualRows+1, 1); err == nil {
			t.Fatal("canonical runtime result budget accepted more than max rows")
		}
		if pageID != "page" || visualID != "visual-id" {
			t.Fatalf("runtime route = page %q visual %q", pageID, visualID)
		}
		return wantEnvelope, nil
	}}
	result, err := provider.queryAgentVisual(context.Background(), "sales", input, "visual-id", model, dashboardDefinition, definition)
	if err != nil {
		t.Fatalf("queryAgentVisual(): %v", err)
	}
	if calls != 1 {
		t.Fatalf("canonical runtime calls = %d, want one", calls)
	}
	if !reflect.DeepEqual(captured, dashboardDefinition) {
		t.Fatalf("runtime received altered definition:\n got %#v\nwant %#v", captured, dashboardDefinition)
	}
	if !reflect.DeepEqual(capturedFilters, dashboardDefinition.DefaultFilters()) {
		t.Fatalf("runtime filters = %#v, want compiled defaults %#v", capturedFilters, dashboardDefinition.DefaultFilters())
	}
	if got := result.Patch["visuals"]["visual-id"]; !reflect.DeepEqual(got, wantEnvelope) {
		t.Fatalf("runtime envelope changed:\n got %#v\nwant %#v", got, wantEnvelope)
	}
}

func TestAgentVisualQueryPreservesSecondaryQueriesAndCountsPrimaryRows(t *testing.T) {
	model := testAgentModel()
	visual := testAgentVisualWithLimit("bar", 10)
	secondary := map[string]dashboarddocument.DashboardQuery{"context": visual.Query}
	visual.Datasets = &secondary
	input := agentVisualInput{Visual: visual, Model: "commerce"}
	dashboardDefinition, err := compileAgentVisual(input, model, "visual-id")
	if err != nil {
		t.Fatalf("compileAgentVisual(): %v", err)
	}
	compiled := dashboardDefinition.Visualizations["visual-id"]
	if _, ok := compiled.SecondaryQueries["context"]; !ok {
		t.Fatalf("compiled definition lost secondary query: %#v", compiled.SecondaryQueries)
	}
	wantEnvelope := visualizationir.VisualizationEnvelope{
		VisualID: "visual-id", RendererID: "canonical",
		Spec: compiled.Spec,
		DataState: visualizationir.VisualizationDataState{Value: &visualizationir.InlineVisualizationDataState{
			VisualizationDataStateBase: visualizationir.VisualizationDataStateBase{Kind: "inline"},
			Kind:                       "inline",
			Datasets: []visualizationir.VisualizationInlineDataset{
				{ID: "primary", Rows: [][]any{{"primary"}}},
				{ID: "context", Rows: [][]any{{"secondary"}, {"secondary-2"}}},
			},
		}},
	}
	var captured dashboarddefinition.Definition
	provider := VisualProvider{QueryDefinition: func(_ context.Context, _ string, got dashboarddefinition.Definition, _ string, _ string, _ dashboard.Filters) (visualizationir.VisualizationEnvelope, error) {
		captured = got
		return wantEnvelope, nil
	}}
	result, err := provider.queryAgentVisual(context.Background(), "sales", input, "visual-id", model, dashboardDefinition, compiled)
	if err != nil {
		t.Fatalf("queryAgentVisual(): %v", err)
	}
	if _, ok := captured.Visualizations["visual-id"].SecondaryQueries["context"]; !ok {
		t.Fatalf("runtime definition lost secondary query: %#v", captured.Visualizations["visual-id"].SecondaryQueries)
	}
	compact, err := compactAgentVisualResult("sales", "query-secondary", VisualQueryMetadata{}, model, input, dashboardDefinition, compiled, result)
	if err != nil {
		t.Fatalf("compactAgentVisualResult(): %v", err)
	}
	if got := compact.Completeness.ReturnedRows; got != 1 {
		t.Fatalf("returned rows = %d, want primary-only count 1", got)
	}
	if got := result.Patch["visuals"]["visual-id"]; !reflect.DeepEqual(got, wantEnvelope) {
		t.Fatalf("runtime envelope changed:\n got %#v\nwant %#v", got, wantEnvelope)
	}
}

func TestAgentVisualQueryRequiresCanonicalRuntime(t *testing.T) {
	model := testAgentModel()
	input := agentVisualInput{Visual: testAgentVisualWithLimit("bar", 10), Model: "commerce"}
	dashboardDefinition, err := compileAgentVisual(input, model, "visual-id")
	if err != nil {
		t.Fatalf("compileAgentVisual(): %v", err)
	}
	_, err = (VisualProvider{}).queryAgentVisual(context.Background(), "sales", input, "visual-id", model, dashboardDefinition, dashboardDefinition.Visualizations["visual-id"])
	if err == nil || !strings.Contains(err.Error(), "canonical visualization runtime") {
		t.Fatalf("queryAgentVisual() error = %v, want canonical runtime failure", err)
	}
}

func TestAgentVisualQueryCarriesLeaseCoherentCanonicalExploration(t *testing.T) {
	model := testAgentModel()
	compiled, err := semanticquery.CompileModel(model)
	if err != nil {
		t.Fatalf("compile model: %v", err)
	}
	input := agentVisualInput{Visual: testAgentVisualWithLimit("bar", 10), Model: "commerce"}
	dashboardDefinition, err := compileAgentVisual(input, model, "visual-id")
	if err != nil {
		t.Fatalf("compileAgentVisual(): %v", err)
	}
	definition := dashboardDefinition.Visualizations["visual-id"]
	provider := VisualProvider{
		Resolve: func(_ context.Context, _ Scope, id projectgraph.ResourceID, _ projectgraph.Kind, _ access.Capability) (projectgraph.ResourceID, error) {
			return id, nil
		},
		SemanticModel: func(_ string, _ string) (*semanticmodel.Model, bool) { return model, true },
		QueryMetadata: func(_ context.Context, _, _ string) VisualQueryMetadata {
			return VisualQueryMetadata{ServingSnapshot: "snapshot-1"}
		},
		QueryDefinition: func(_ context.Context, _ string, _ dashboarddefinition.Definition, _ string, visualID string, _ dashboard.Filters) (visualizationir.VisualizationEnvelope, error) {
			return visualizationir.VisualizationEnvelope{VisualID: visualID, Spec: definition.Spec}, nil
		},
		ExplorationModel: func(_ context.Context, projectID, principalID, modelID, snapshot string) (*semanticmodel.Model, *semanticquery.CompiledModel, error) {
			if projectID != "project" || principalID != "principal" || modelID != "commerce" || snapshot != "snapshot-1" {
				t.Fatalf("exploration callback arguments = %q/%q/%q/%q", projectID, principalID, modelID, snapshot)
			}
			return model, compiled, nil
		},
	}
	result := provider.Run(context.Background(), Scope{ProjectID: "project", PrincipalID: "principal"}, agentcore.ToolCall{
		ID:        "visual-call",
		Arguments: json.RawMessage(`{"semanticModelId":"commerce","visual":{"type":"bar","query":{"type":"aggregate","dimensions":[{"dimension":"country","alias":"country"}],"metrics":[{"metric":"revenue","alias":"total_revenue"}],"limit":10},"presentation":{"type":"cartesian"},"dataBudget":{"maxRows":50,"requiredCompleteness":"complete"}}}`),
	})
	if result.IsError {
		t.Fatalf("query_visual failed: %#v", result.Content)
	}
	display, ok := result.DisplayContent.(agentVisualResult)
	if !ok || display.Exploration == nil {
		t.Fatalf("display exploration = %#v", result.DisplayContent)
	}
	spec := display.Exploration
	if spec.ModelID != "commerce" || spec.DatasetID == nil || *spec.DatasetID != "orders" || spec.Limit != 10 {
		t.Fatalf("exploration identity/bound = %#v", spec)
	}
	if len(spec.Dimensions) != 1 || spec.Dimensions[0].Field != "orders.country" || spec.Dimensions[0].Alias == nil || *spec.Dimensions[0].Alias != "country" {
		t.Fatalf("exploration dimensions = %#v", spec.Dimensions)
	}
	if len(spec.Metrics) != 1 || spec.Metrics[0].Field != "revenue" || spec.Metrics[0].Alias == nil || *spec.Metrics[0].Alias != "total_revenue" {
		t.Fatalf("exploration metrics = %#v", spec.Metrics)
	}
}

func TestNormalizeAgentVisualLimitPreservesEffectiveBound(t *testing.T) {
	definition := visualizationdefinition.Definition{Query: visualizationdefinition.QueryBinding{
		Kind:      visualizationdefinition.QueryAggregate,
		Aggregate: &visualizationdefinition.AggregateQueryBinding{Limit: 50},
	}}
	tests := []struct {
		name       string
		queryLimit *int32
		budget     int32
		want       int32
	}{
		{name: "missing limit receives agent cap", budget: 50, want: 50},
		{name: "query limit is narrower", queryLimit: int32Ptr(10), budget: 50, want: 10},
		{name: "declared budget is narrower", queryLimit: int32Ptr(20), budget: 10, want: 10},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			visual := testAgentVisual("bar")
			visual.DataBudget = &dashboarddocument.DashboardDataBudget{MaxRows: test.budget}
			visual.Query.Value.(*dashboarddocument.AggregateDashboardQuery).Limit = test.queryLimit
			normalized, err := normalizeAgentVisualLimit(visual, definition)
			if err != nil {
				t.Fatal(err)
			}
			query := normalized.Query.Value.(*dashboarddocument.AggregateDashboardQuery)
			if query.Limit == nil || *query.Limit != test.want || normalized.DataBudget == nil || normalized.DataBudget.MaxRows != test.want {
				t.Fatalf("normalized bound = %#v/%#v, want %d", query.Limit, normalized.DataBudget, test.want)
			}
			if visual.Query.Value.(*dashboarddocument.AggregateDashboardQuery).Limit != test.queryLimit {
				t.Fatal("normalization mutated authored query")
			}
		})
	}
}

func TestNormalizeAgentVisualFilterTargetsMapsOnlyAgentWrapperScope(t *testing.T) {
	input := agentVisualInput{Filters: []dashboarddocument.DashboardFilter{{
		ID: "country-filter", Targets: stringSlicePtr([]string{"page/visual"}),
	}}}
	normalized, err := normalizeAgentVisualFilterTargets(input, "agent_visual_call")
	if err != nil {
		t.Fatalf("normalize agent target: %v", err)
	}
	if got := *normalized.Filters[0].Targets; !reflect.DeepEqual(got, []string{"agent_visual_call"}) {
		t.Fatalf("normalized targets = %#v, want generated visual identity", got)
	}
	if got := *input.Filters[0].Targets; !reflect.DeepEqual(got, []string{"page/visual"}) {
		t.Fatalf("normalization mutated input targets = %#v", got)
	}
	if _, err := normalizeAgentVisualFilterTargets(input, "agent_visual_call"); err != nil {
		t.Fatalf("wrapper target should be accepted: %v", err)
	}
	input.Filters[0].Targets = stringSlicePtr([]string{"agent_visual_call"})
	normalized, err = normalizeAgentVisualFilterTargets(input, "agent_visual_call")
	if err != nil || !reflect.DeepEqual(*normalized.Filters[0].Targets, []string{"agent_visual_call"}) {
		t.Fatalf("exact generated target = %#v, err %v", normalized.Filters[0].Targets, err)
	}
	for _, target := range []string{"old-authored-visual", "page/other-visual", ""} {
		input.Filters[0].Targets = stringSlicePtr([]string{target})
		if _, err := normalizeAgentVisualFilterTargets(input, "agent_visual_call"); err == nil {
			t.Fatalf("target %q was accepted; unknown scopes must fail closed", target)
		}
	}
}

func TestAgentVisualQueryNormalizesScopedFilterBeforeCompileAndHandoff(t *testing.T) {
	model := testAgentModel()
	compiled, err := semanticquery.CompileModel(model)
	if err != nil {
		t.Fatalf("compile model: %v", err)
	}
	visual := testAgentVisualWithLimit("bar", 10)
	filter := dashboarddocument.DashboardFilter{
		ID: "country-filter", Label: "Country", Dimension: "country",
		Control: dashboarddocument.DashboardFilterControl{Value: &dashboarddocument.MultiSelectDashboardFilterControl{Type: "multiSelect"}},
		Default: &dashboarddocument.DashboardFilterExpression{Value: &dashboarddocument.SetDashboardFilterExpression{
			DashboardFilterExpressionBase: dashboarddocument.DashboardFilterExpressionBase{Type: "set"},
			Type:                          "set", Operator: dashboarddocument.DashboardFilterOperatorIn,
			Values: []dashboarddocument.DashboardFilterValue{{Value: &dashboarddocument.StringDashboardFilterValue{
				DashboardFilterValueBase: dashboarddocument.DashboardFilterValueBase{Type: "string"}, Type: "string", Value: "DE",
			}}},
		}},
		Targets: stringSlicePtr([]string{"page/visual"}),
	}
	arguments, err := json.Marshal(struct {
		SemanticModelID string                              `json:"semanticModelId"`
		Visual          dashboarddocument.DashboardVisual   `json:"visual"`
		Filters         []dashboarddocument.DashboardFilter `json:"filters"`
	}{SemanticModelID: "commerce", Visual: visual, Filters: []dashboarddocument.DashboardFilter{filter}})
	if err != nil {
		t.Fatalf("marshal query_visual arguments: %v", err)
	}
	var capturedTarget []string
	provider := VisualProvider{
		Resolve: func(_ context.Context, _ Scope, id projectgraph.ResourceID, _ projectgraph.Kind, _ access.Capability) (projectgraph.ResourceID, error) {
			return id, nil
		},
		SemanticModel: func(string, string) (*semanticmodel.Model, bool) { return model, true },
		QueryMetadata: func(_ context.Context, _, _ string) VisualQueryMetadata {
			return VisualQueryMetadata{ServingSnapshot: "snapshot-1"}
		},
		QueryDefinition: func(_ context.Context, _ string, definition dashboarddefinition.Definition, _ string, visualID string, _ dashboard.Filters) (visualizationir.VisualizationEnvelope, error) {
			binding, ok := definition.FilterBindings["country-filter"]
			if !ok {
				t.Fatal("compiled definition omitted scoped filter")
			}
			capturedTarget = append([]string(nil), binding.TargetPolicy.Include...)
			return visualizationir.VisualizationEnvelope{VisualID: visualID, Spec: definition.Visualizations[visualID].Spec}, nil
		},
		ExplorationModel: func(_ context.Context, projectID, principalID, modelID, snapshot string) (*semanticmodel.Model, *semanticquery.CompiledModel, error) {
			if projectID != "project" || principalID != "principal" || modelID != "commerce" || snapshot != "snapshot-1" {
				t.Fatalf("exploration callback identity = %q/%q/%q/%q", projectID, principalID, modelID, snapshot)
			}
			return model, compiled, nil
		},
	}
	result := provider.Run(context.Background(), Scope{ProjectID: "project", PrincipalID: "principal"}, agentcore.ToolCall{ID: "visual-call", Arguments: arguments})
	if result.IsError {
		t.Fatalf("query_visual rejected supported wrapper target: %#v", result.Content)
	}
	if !reflect.DeepEqual(capturedTarget, []string{"agent_visual_visual-call"}) {
		t.Fatalf("compiled filter target = %#v, want generated visual identity", capturedTarget)
	}
	display, ok := result.DisplayContent.(agentVisualResult)
	if !ok || display.Exploration == nil {
		t.Fatalf("display handoff = %#v, want canonical exploration", result.DisplayContent)
	}
	if len(display.Exploration.Filters) != 1 || display.Exploration.Filters[0].Field != "orders.country" {
		t.Fatalf("handoff filters = %#v, want scoped country filter", display.Exploration.Filters)
	}
}

func TestAgentVisualExplorationPreservesAliasesTimeAndScopedFilters(t *testing.T) {
	model := testAgentModel()
	orders := model.Tables["orders"]
	orders.Dimensions["ordered_at"] = semanticmodel.MetricDimension{Field: "orders.ordered_at", Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ}
	model.Tables["orders"] = orders
	model.Dimensions["order_date"] = semanticmodel.SemanticDimension{Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ, NativeGrain: "day", Grains: []string{"day", "month"}, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.ordered_at"}}}
	compiled, err := semanticquery.CompileModel(model)
	if err != nil {
		t.Fatalf("compile model: %v", err)
	}
	state := "country"
	month := "order_date"
	revenue := "revenue"
	visual := dashboarddocument.DashboardVisual{
		Type: dashboarddocument.DashboardVisualTypeLine,
		Query: dashboarddocument.DashboardQuery{Value: &dashboarddocument.AggregateDashboardQuery{
			Type: "aggregate",
			Dimensions: []dashboarddocument.DashboardDimensionSelection{
				{Reference: &dashboarddocument.DashboardDimensionReference{Dimension: state, Alias: strPtr("state_label")}},
				{Reference: &dashboarddocument.DashboardDimensionReference{Dimension: month, Alias: strPtr("month_label"), Grain: dashboardTimeGrainPtr(dashboarddocument.DashboardTimeGrainMonth)}},
			},
			Metrics: []dashboarddocument.DashboardMetricSelection{{Reference: &dashboarddocument.DashboardMetricReference{Metric: revenue, Alias: strPtr("gross_revenue")}}},
			Limit:   int32Ptr(25),
		}},
		Presentation: dashboarddocument.DashboardPresentation{Value: &dashboarddocument.CartesianDashboardPresentation{Type: "cartesian"}},
		DataBudget:   &dashboarddocument.DashboardDataBudget{MaxRows: 25},
	}
	filter := dashboarddocument.DashboardFilter{
		ID: "country-filter", Label: "Country", Dimension: state, Targets: stringSlicePtr([]string{"page/visual"}),
		Control: dashboarddocument.DashboardFilterControl{Value: &dashboarddocument.MultiSelectDashboardFilterControl{Type: "multiSelect"}},
		Default: &dashboarddocument.DashboardFilterExpression{Value: &dashboarddocument.SetDashboardFilterExpression{
			DashboardFilterExpressionBase: dashboarddocument.DashboardFilterExpressionBase{Type: "set"}, Type: "set", Operator: dashboarddocument.DashboardFilterOperatorIn,
			Values: []dashboarddocument.DashboardFilterValue{{Value: &dashboarddocument.StringDashboardFilterValue{DashboardFilterValueBase: dashboarddocument.DashboardFilterValueBase{Type: "string"}, Type: "string", Value: "DE"}}},
		}},
	}
	unrelated := filter
	unrelated.ID = "other-country-filter"
	unrelated.Targets = stringSlicePtr([]string{"page/other-visual"})
	input := agentVisualInput{Model: "commerce", Visual: visual}
	definition, err := compileAgentVisual(input, model, "visual-id")
	if err != nil {
		t.Fatalf("compile visual: %v", err)
	}
	input.Filters = []dashboarddocument.DashboardFilter{filter, unrelated}
	spec, err := agentVisualExploration(input, model, model, compiled, definition.Visualizations["visual-id"])
	if err != nil {
		t.Fatalf("agentVisualExploration: %v", err)
	}
	if spec.Limit != 25 || len(spec.Dimensions) != 2 || spec.Dimensions[0].Alias == nil || *spec.Dimensions[0].Alias != "state_label" || spec.Time == nil || spec.Time.Grain != "month" {
		t.Fatalf("canonical selections/time = %#v", spec)
	}
	if len(spec.Filters) != 1 || spec.Filters[0].Field != "orders.country" {
		t.Fatalf("canonical scoped filters = %#v", spec.Filters)
	}
}

func TestAgentVisualQueryOmitsHandoffWhenCompiledModelMismatches(t *testing.T) {
	model := testAgentModel()
	compiled, err := semanticquery.CompileModel(model)
	if err != nil {
		t.Fatalf("compile model: %v", err)
	}
	other := testAgentModel()
	other.Metrics["revenue"] = semanticmodel.Metric{Type: "aggregate", Dataset: "orders", Aggregation: "avg", Input: &semanticmodel.MetricInput{Field: "orders.revenue"}}
	input := agentVisualInput{Visual: testAgentVisualWithLimit("bar", 10), Model: "commerce"}
	dashboardDefinition, err := compileAgentVisual(input, model, "visual-id")
	if err != nil {
		t.Fatalf("compileAgentVisual(): %v", err)
	}
	definition := dashboardDefinition.Visualizations["visual-id"]
	provider := VisualProvider{
		Resolve: func(_ context.Context, _ Scope, id projectgraph.ResourceID, _ projectgraph.Kind, _ access.Capability) (projectgraph.ResourceID, error) {
			return id, nil
		},
		SemanticModel: func(_ string, _ string) (*semanticmodel.Model, bool) { return model, true },
		QueryMetadata: func(_ context.Context, _, _ string) VisualQueryMetadata {
			return VisualQueryMetadata{ServingSnapshot: "snapshot-1"}
		},
		QueryDefinition: func(_ context.Context, _ string, _ dashboarddefinition.Definition, _ string, visualID string, _ dashboard.Filters) (visualizationir.VisualizationEnvelope, error) {
			return visualizationir.VisualizationEnvelope{VisualID: visualID, Spec: definition.Spec}, nil
		},
		ExplorationModel: func(context.Context, string, string, string, string) (*semanticmodel.Model, *semanticquery.CompiledModel, error) {
			return other, compiled, nil
		},
	}
	result := provider.Run(context.Background(), Scope{ProjectID: "project", PrincipalID: "principal"}, agentcore.ToolCall{
		ID:        "visual-call",
		Arguments: json.RawMessage(`{"semanticModelId":"commerce","visual":{"type":"bar","query":{"type":"aggregate","dimensions":["country"],"metrics":["revenue"],"limit":10},"presentation":{"type":"cartesian"},"dataBudget":{"maxRows":50,"requiredCompleteness":"complete"}}}`),
	})
	if result.IsError {
		t.Fatalf("query_visual failed: %#v", result.Content)
	}
	display, ok := result.DisplayContent.(agentVisualResult)
	if !ok || display.Exploration != nil {
		t.Fatalf("mismatched handoff = %#v, want visual without exploration", result.DisplayContent)
	}
}

func TestAgentVisualExplorationRejectsPartialCompleteness(t *testing.T) {
	model := testAgentModel()
	compiled, err := semanticquery.CompileModel(model)
	if err != nil {
		t.Fatalf("compile model: %v", err)
	}
	visual := testAgentVisualWithLimit("bar", 10)
	partial := visualizationir.VisualizationCompletenessPartial
	visual.DataBudget.RequiredCompleteness = &partial
	input := agentVisualInput{Model: "commerce", Visual: visual}
	dashboardDefinition, err := compileAgentVisual(input, model, "visual-id")
	if err != nil {
		t.Fatalf("compile visual: %v", err)
	}
	if _, err := agentVisualExploration(input, model, model, compiled, dashboardDefinition.Visualizations["visual-id"]); err == nil || !strings.Contains(err.Error(), "completeness") {
		t.Fatalf("partial visual handoff error = %v", err)
	}
}

func TestAgentVisualFilterUsagesReportAppliedDefaults(t *testing.T) {
	definition := dashboarddefinition.Definition{
		FilterDefinitions: map[string]dashboardfilter.Definition{
			"status": {Field: "status", Dataset: "orders", ValueKind: dashboardfilter.ValueString, Predicates: []dashboardfilter.PredicatePolicy{{Kind: dashboardfilter.ExpressionSet, Operators: []dashboardfilter.Operator{dashboardfilter.OperatorEquals, dashboardfilter.OperatorIn}}}},
		},
		FilterBindings: map[string]dashboardfilter.Binding{
			"status": {Key: "status", Filter: "status", ValueKind: dashboardfilter.ValueString, Default: dashboardfilter.Expression{Kind: dashboardfilter.ExpressionSet, Operator: dashboardfilter.OperatorIn, Values: []dashboardfilter.Value{{Kind: dashboardfilter.ValueString, Value: "paid"}}}},
		},
		FilterOrder: []string{"status"},
	}
	filters := definition.DefaultFilters()
	got, err := agentVisualFilterUsages("project", "commerce", definition, filters)
	if err != nil {
		t.Fatalf("agentVisualFilterUsages(): %v", err)
	}
	if len(got) != 1 || got[0].FieldID != "commerce.status" {
		t.Fatalf("filter usages = %#v, want applied in/paid metadata", got)
	}
	encoded, err := json.Marshal(got[0].Expression)
	if err != nil || !strings.Contains(string(encoded), `"type":"set"`) || !strings.Contains(string(encoded), `"operator":"in"`) || !strings.Contains(string(encoded), `"value":"paid"`) {
		t.Fatalf("filter expression = %s, want canonical applied in/paid metadata", encoded)
	}
	filters.CompiledState.AppliedControls["status"] = dashboardfilter.AppliedState{Expression: dashboardfilter.Expression{Kind: dashboardfilter.ExpressionUnfiltered}}
	got, err = agentVisualFilterUsages("project", "commerce", definition, filters)
	if err != nil {
		t.Fatalf("agentVisualFilterUsages(unfiltered): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("unfiltered metadata = %#v, want omitted", got)
	}
}

func TestDashboardFilterExpressionPreservesRangeAndRelativeValues(t *testing.T) {
	tests := []struct {
		name       string
		expression dashboardfilter.Expression
		want       []string
	}{
		{
			name: "range",
			expression: dashboardfilter.Expression{Kind: dashboardfilter.ExpressionRange,
				Lower: &dashboardfilter.Bound{Value: dashboardfilter.Value{Kind: dashboardfilter.ValueInteger, Value: int64(10)}, Inclusive: true},
				Upper: &dashboardfilter.Bound{Value: dashboardfilter.Value{Kind: dashboardfilter.ValueDecimal, Value: "19.5"}, Inclusive: false}},
			want: []string{`"type":"range"`, `"inclusive":true`, `"inclusive":false`, `"type":"integer"`, `"value":"10"`, `"type":"decimal"`, `"value":"19.5"`},
		},
		{
			name:       "relative",
			expression: dashboardfilter.Expression{Kind: dashboardfilter.ExpressionRelativePeriod, Direction: dashboardfilter.DirectionPrevious, Count: 2, Unit: dashboardfilter.UnitQuarter, IncludeCurrent: true, Anchor: dashboardfilter.AnchorFixed, AnchorValue: &dashboardfilter.Value{Kind: dashboardfilter.ValueDate, Value: "2026-01-01"}},
			want:       []string{`"type":"relativePeriod"`, `"direction":"previous"`, `"count":2`, `"unit":"quarter"`, `"includeCurrent":true`, `"anchor":"fixed"`, `"type":"date"`, `"value":"2026-01-01"`},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			converted, err := dashboardFilterExpression(test.expression)
			if err != nil {
				t.Fatalf("dashboardFilterExpression(): %v", err)
			}
			encoded, err := json.Marshal(converted)
			if err != nil {
				t.Fatal(err)
			}
			for _, fragment := range test.want {
				if !strings.Contains(string(encoded), fragment) {
					t.Errorf("expression %s missing %s", encoded, fragment)
				}
			}
		})
	}
	if _, err := dashboardRelativeUnit(dashboardfilter.RelativeUnit("fortnight")); err == nil {
		t.Fatal("unknown relative unit was accepted")
	}
	if _, err := dashboardRelativeAnchor(dashboardfilter.RelativeAnchor("nearest")); err == nil {
		t.Fatal("unknown relative anchor was accepted")
	}
}

func testAgentVisualWithLimit(visualType string, limit int32) dashboarddocument.DashboardVisual {
	visual := testAgentVisual(visualType)
	visual.DataBudget = &dashboarddocument.DashboardDataBudget{MaxRows: 50}
	query, ok := visual.Query.Value.(*dashboarddocument.AggregateDashboardQuery)
	if ok {
		query.Limit = &limit
	}
	return visual
}

func testAgentModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name: "commerce", Sources: map[string]semanticmodel.Source{"orders": {Path: "orders.csv"}},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Tables: map[string]semanticmodel.Table{"orders": {
			ModelName: "orders", Execution: semanticmodel.ExecutionDefinition{Source: "orders"}, GrainEntity: "order_id",
			Entities: map[string]semanticmodel.EntityDefinition{"order_id": {Type: "primary", Fields: []string{"order_id"}}},
			Dimensions: map[string]semanticmodel.MetricDimension{
				"country":  {Field: "orders.country", Type: "string", Datatype: semanticmodel.DataTypeString},
				"order_id": {Field: "orders.order_id", Type: "string", Datatype: semanticmodel.DataTypeString},
				"revenue":  {Field: "orders.revenue", Type: "number", Datatype: semanticmodel.DataTypeDecimal},
			},
		}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"country": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.country"}}},
		},
		Metrics: map[string]semanticmodel.Metric{"revenue": {Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.revenue"}, Empty: "zero"}},
	}
}

func strPtr(value string) *string { return &value }

func int32Ptr(value int32) *int32 { return &value }

func stringSlicePtr(value []string) *[]string { return &value }

func dashboardTimeGrainPtr(value dashboarddocument.DashboardTimeGrain) *dashboarddocument.DashboardTimeGrain {
	return &value
}
