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
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboarddocument "github.com/flidai/leapview/internal/dashboard/document"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
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
			ModelName: "orders", Source: "orders", GrainEntity: "order_id",
			Entities: map[string]semanticmodel.ModelEntitySpec{"order_id": {Type: "primary", Fields: []string{"order_id"}}},
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
