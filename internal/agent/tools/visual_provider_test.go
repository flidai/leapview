package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	agentcontracts "github.com/flidai/leapview/internal/agent/contracts"
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

func TestCompileAgentVisualDefaultsBudgetAndQueryLimit(t *testing.T) {
	model := testAgentModel()
	tests := []struct {
		name             string
		budget           *dashboarddocument.DashboardDataBudget
		wantBudget       int64
		wantCompleteness visualizationir.VisualizationCompleteness
	}{
		{name: "agent ceiling", wantBudget: maxVisualRows, wantCompleteness: visualizationir.VisualizationCompletenessPartial},
		{name: "explicit budget", budget: &dashboarddocument.DashboardDataBudget{MaxRows: 7}, wantBudget: 7, wantCompleteness: visualizationir.VisualizationCompletenessComplete},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			visual := testAgentVisual("bar")
			visual.DataBudget = test.budget
			compiled, err := compileAgentVisual(agentVisualInput{Visual: visual, Model: "commerce"}, model, "visual-id")
			if err != nil {
				t.Fatalf("compileAgentVisual(): %v", err)
			}
			definition := compiled.Visualizations["visual-id"]
			base, err := visualizationir.SpecificationBase(definition.Spec)
			if err != nil {
				t.Fatalf("SpecificationBase(): %v", err)
			}
			if base.DataBudget.MaxRows != test.wantBudget {
				t.Fatalf("data budget = %d, want %d", base.DataBudget.MaxRows, test.wantBudget)
			}
			if base.DataBudget.RequiredCompleteness != test.wantCompleteness {
				t.Fatalf("required completeness = %q, want %q", base.DataBudget.RequiredCompleteness, test.wantCompleteness)
			}
			if got := agentDefinitionLimit(definition); got != int(test.wantBudget) {
				t.Fatalf("query limit = %d, want budget %d", got, test.wantBudget)
			}
		})
	}
}

func TestCompileAgentVisualRejectsBudgetAboveAgentCeilingActionably(t *testing.T) {
	visual := testAgentVisual("bar")
	visual.DataBudget = &dashboarddocument.DashboardDataBudget{MaxRows: maxVisualRows + 1}
	_, err := compileAgentVisual(agentVisualInput{Visual: visual, Model: "commerce"}, testAgentModel(), "visual-id")
	if err == nil || !strings.Contains(err.Error(), "visual.dataBudget.maxRows") || !strings.Contains(err.Error(), "50") {
		t.Fatalf("compileAgentVisual() error = %v, want actionable dataBudget.maxRows error naming ceiling 50", err)
	}
}

func TestCompileAgentVisualQueryLimitErrorIsActionable(t *testing.T) {
	visual := testAgentVisual("bar")
	visual.DataBudget = &dashboarddocument.DashboardDataBudget{MaxRows: 7}
	limit := int32(8)
	visual.Query.Value.(*dashboarddocument.AggregateDashboardQuery).Limit = &limit
	_, err := compileAgentVisual(agentVisualInput{Visual: visual, Model: "commerce"}, testAgentModel(), "visual-id")
	if err == nil || !strings.Contains(err.Error(), "visual.query.limit") || !strings.Contains(err.Error(), "visual.dataBudget.maxRows 7") {
		t.Fatalf("compileAgentVisual() error = %v, want actionable query limit and budget details", err)
	}
}

func TestCompactAgentVisualResultMarksEnvelopeErrorNotOK(t *testing.T) {
	model := testAgentModel()
	input := agentVisualInput{Visual: testAgentVisualWithLimit("bar", 10), Model: "commerce"}
	dashboardDefinition, err := compileAgentVisual(input, model, "visual-id")
	if err != nil {
		t.Fatalf("compileAgentVisual(): %v", err)
	}
	definition := dashboardDefinition.Visualizations["visual-id"]
	envelope := visualizationir.VisualizationEnvelope{
		Spec:   definition.Spec,
		Status: visualizationir.VisualizationStatus{Kind: visualizationir.VisualizationStatusKindError, Message: strPtr("query failed")},
	}
	result := agentVisualResult{Type: "bar", ID: "visual-id", Patch: map[string]map[string]visualizationir.VisualizationEnvelope{"visuals": {"visual-id": envelope}}}
	compact, err := compactAgentVisualResult("sales", "query-id", VisualQueryMetadata{}, model, input, dashboardDefinition, definition, result)
	if err != nil {
		t.Fatalf("compactAgentVisualResult(): %v", err)
	}
	if compact.Ok {
		t.Fatalf("compact result ok = true for error envelope: %#v", compact)
	}
}

func TestCompactAgentVisualModelResultProjectsOnlyBoundedPrimaryInlineRows(t *testing.T) {
	primaryRows := [][]any{{"DE", int64(12)}, {"FR", int64(8)}, {"US", int64(5)}}
	result := compactAgentVisualModelResult(agentcontracts.QueryVisualResult{}, visualizationir.VisualizationEnvelope{
		DataState: visualizationir.VisualizationDataState{Value: &visualizationir.InlineVisualizationDataState{
			Kind: "inline",
			Datasets: []visualizationir.VisualizationInlineDataset{
				{ID: "primary", Columns: []string{"country", "orders"}, Rows: primaryRows},
				{ID: "context", Columns: []string{"ignored"}, Rows: [][]any{{"secondary"}}},
			},
		}},
	}, 2)
	projection, ok := result.(agentVisualModelProjection)
	if !ok {
		t.Fatalf("model result type = %T, want agentVisualModelProjection", result)
	}
	if !reflect.DeepEqual(projection.Columns, []string{"country", "orders"}) {
		t.Fatalf("columns = %#v, want primary columns", projection.Columns)
	}
	if !reflect.DeepEqual(projection.Rows, primaryRows[:2]) {
		t.Fatalf("rows = %#v, want first two primary rows", projection.Rows)
	}
	if got := projection.DataCompleteness; got.ReturnedRows != 2 || got.PrimaryResultRows != 3 || got.Status != "truncated" {
		t.Fatalf("data completeness = %#v, want 2 returned of 3 with truncation", got)
	}
}

func TestCompactAgentVisualModelResultDefaultsNonPositiveRowLimit(t *testing.T) {
	result := compactAgentVisualModelResult(agentcontracts.QueryVisualResult{}, visualizationir.VisualizationEnvelope{
		DataState: visualizationir.VisualizationDataState{Value: &visualizationir.InlineVisualizationDataState{
			Kind:     "inline",
			Datasets: []visualizationir.VisualizationInlineDataset{{ID: "primary", Rows: [][]any{{"o-1"}}}},
		}},
	}, 0)
	projection, ok := result.(agentVisualModelProjection)
	if !ok || len(projection.Rows) != 1 {
		t.Fatalf("model result = %#v, want one row using the default row limit", result)
	}
}

func TestCompactAgentVisualModelResultHidesNonInlineRows(t *testing.T) {
	result := compactAgentVisualModelResult(agentcontracts.QueryVisualResult{}, visualizationir.VisualizationEnvelope{
		DataState: visualizationir.VisualizationDataState{Value: &visualizationir.WindowedVisualizationDataState{
			Kind:   "windowed",
			Blocks: map[string]visualizationir.VisualizationWindowBlock{"first": {ID: "first", Rows: [][]any{{"o-1"}}}},
		}},
	}, maxVisualRows)
	projection, ok := result.(agentVisualModelProjection)
	if !ok {
		t.Fatalf("model result type = %T, want agentVisualModelProjection", result)
	}
	if len(projection.Columns) != 0 || len(projection.Rows) != 0 || projection.DataCompleteness.Status != "unavailable" {
		t.Fatalf("non-inline projection = %#v, want empty data marked unavailable", projection)
	}
}

func TestCompactAgentVisualModelResultRespectsFormattedByteBudget(t *testing.T) {
	rows := make([][]any, maxVisualRows)
	for index := range rows {
		rows[index] = []any{strings.Repeat("x", 2048)}
	}
	result := compactAgentVisualModelResult(agentcontracts.QueryVisualResult{}, visualizationir.VisualizationEnvelope{
		DataState: visualizationir.VisualizationDataState{Value: &visualizationir.InlineVisualizationDataState{
			Kind:     "inline",
			Datasets: []visualizationir.VisualizationInlineDataset{{ID: "primary", Columns: []string{"value"}, Rows: rows}},
		}},
	}, maxVisualRows)
	projection, ok := result.(agentVisualModelProjection)
	if !ok {
		t.Fatalf("model result type = %T, want bounded projection", result)
	}
	if len(projection.Rows) >= maxVisualRows {
		t.Fatalf("returned rows = %d, want byte-budget truncation below %d", len(projection.Rows), maxVisualRows)
	}
	if projection.DataCompleteness.ReturnedRows != int32(len(projection.Rows)) || projection.DataCompleteness.PrimaryResultRows != maxVisualRows || projection.DataCompleteness.Status != "truncated" {
		t.Fatalf("data completeness = %#v, want accurate byte-budget truncation", projection.DataCompleteness)
	}
	if size := agentVisualModelProjectionBytes(projection); size > maxVisualModelBytes {
		t.Fatalf("formatted model projection size = %d, exceeds %d-byte budget", size, maxVisualModelBytes)
	}
}

func TestCompactAgentVisualModelResultKeepsRowsWhenFinalMetadataExceedsBudget(t *testing.T) {
	// The second row should fit only before the result is marked truncated.
	// A third oversized row makes the final status differ from the trial status.
	rows := [][]any{{"small"}, {""}, {strings.Repeat("z", maxVisualModelBytes)}}
	trial := agentVisualModelProjection{Columns: []string{"value"}, Rows: rows[:2], DataCompleteness: agentVisualModelDataCompleteness{PrimaryResultRows: 3, Status: "complete"}}
	low, high := 0, maxVisualModelBytes
	for low < high {
		middle := low + (high-low+1)/2
		trial.Rows[1][0] = strings.Repeat("x", middle)
		if agentVisualModelProjectionBytes(trial) <= maxVisualModelBytes {
			low = middle
		} else {
			high = middle - 1
		}
	}
	rows[1][0] = strings.Repeat("x", low)
	trial.Rows = rows[:2]
	if agentVisualModelProjectionBytes(trial) > maxVisualModelBytes {
		t.Fatal("trial projection unexpectedly exceeds budget")
	}
	trial.DataCompleteness.Status = "truncated"
	trial.DataCompleteness.ReturnedRows = 2
	if agentVisualModelProjectionBytes(trial) <= maxVisualModelBytes {
		t.Fatal("test input did not reach the final-metadata boundary")
	}
	result := compactAgentVisualModelResult(agentcontracts.QueryVisualResult{}, visualizationir.VisualizationEnvelope{
		DataState: visualizationir.VisualizationDataState{Value: &visualizationir.InlineVisualizationDataState{
			Kind: "inline", Datasets: []visualizationir.VisualizationInlineDataset{{ID: "primary", Columns: []string{"value"}, Rows: rows}},
		}},
	}, maxVisualRows)
	projection, ok := result.(agentVisualModelProjection)
	if !ok || len(projection.Rows) == 0 || projection.DataCompleteness.Status != "truncated" {
		t.Fatalf("model result = %#v; want a bounded projection retaining values", result)
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
	got := agentVisualFieldUsage("sales", "commerce", model, agentVisualFieldRef{Field: "return_rate", Alias: "rate"}, "metric", "orders")
	if got.Role != "metric" || got.FieldID != "commerce.return_rate" || got.Label != "Return rate" ||
		got.Alias == nil || *got.Alias != "rate" || got.Unit == nil || *got.Unit != "percent" ||
		got.Format == nil || *got.Format != "percent_1" {
		t.Fatalf("field usage = %#v", got)
	}
}

func TestAgentVisualCanonicalAggregateExposesExplorerDimensionBinding(t *testing.T) {
	model := testAgentModel()
	compiled, err := compileAgentVisual(agentVisualInput{Visual: testAgentVisual("bar"), Model: "commerce"}, model, "visual-id")
	if err != nil {
		t.Fatalf("compileAgentVisual(): %v", err)
	}
	definition := compiled.Visualizations["visual-id"]
	fields := agentVisualFieldUsages("sales", "commerce", model, definition)
	if len(fields) != 2 {
		t.Fatalf("field usages = %#v, want canonical aggregate dimension and metric", fields)
	}
	if fields[0].FieldID != "commerce.country" || fields[0].ExplorerFieldID == nil || *fields[0].ExplorerFieldID != "orders.country" {
		t.Fatalf("semantic dimension explorer binding = %#v, want orders.country", fields[0])
	}
	if fields[1].Role != "metric" || fields[1].ExplorerFieldID != nil {
		t.Fatalf("metric explorer mapping = %#v, want none", fields[1])
	}

	// Explorer field IDs are qualified by the semantic dataset alias, even
	// when that dataset is backed by a differently named physical model.
	aliasModel := testAgentModel()
	aliasModel.Datasets["order_facts"] = semanticmodel.SemanticDatasetSpec{Model: "orders"}
	aliasDimension := aliasModel.Dimensions["country"]
	aliasDimension.Bindings["order_facts"] = semanticmodel.DimensionBinding{Field: "orders.country"}
	aliasModel.Dimensions["country"] = aliasDimension
	aliasUsage := agentVisualFieldUsage("sales", "commerce", aliasModel, agentVisualFieldRef{Field: "country", Alias: "country"}, "dimension", "order_facts")
	if aliasUsage.ExplorerFieldID == nil || *aliasUsage.ExplorerFieldID != "order_facts.country" {
		t.Fatalf("aliased semantic dimension explorer binding = %#v, want order_facts.country", aliasUsage.ExplorerFieldID)
	}

	// A conformed dimension is replayable in Explorer when its authored join
	// is the same unambiguous, grain-preserving path Explorer will use.
	joinedModel := testAgentModel()
	joinedModel.Datasets["customers"] = semanticmodel.SemanticDatasetSpec{Model: "customers"}
	joinedModel.Tables["customers"] = semanticmodel.Table{ModelName: "customers", Dimensions: map[string]semanticmodel.MetricDimension{
		"country": {Field: "customers.country", Type: "string", Datatype: semanticmodel.DataTypeString},
	}}
	joinedModel.Relationships = []semanticmodel.Relationship{{ID: "orders_customers", FromDataset: "orders", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one"}}
	joinedModel.Dimensions["customer_country"] = semanticmodel.SemanticDimension{Bindings: map[string]semanticmodel.DimensionBinding{
		"orders": {Field: "customers.country", Path: []string{"orders_customers"}},
	}}
	joined := agentVisualFieldUsage("sales", "commerce", joinedModel, agentVisualFieldRef{Field: "customer_country"}, "dimension", "orders")
	if joined.ExplorerFieldID == nil || *joined.ExplorerFieldID != "customers.country" {
		t.Fatalf("joined semantic dimension explorer binding = %#v, want customers.country", joined.ExplorerFieldID)
	}
	joinedModel.Datasets["order_facts"] = semanticmodel.SemanticDatasetSpec{Model: "orders"}
	joinedModel.Datasets["customer_lookup"] = semanticmodel.SemanticDatasetSpec{Model: "customers"}
	joinedModel.Tables["order_facts"] = joinedModel.Tables["orders"]
	joinedModel.Tables["customer_lookup"] = semanticmodel.Table{ModelName: "customers", Dimensions: map[string]semanticmodel.MetricDimension{
		"country": {Field: "customer_lookup.country", Type: "string", Datatype: semanticmodel.DataTypeString},
	}}
	joinedModel.Relationships = []semanticmodel.Relationship{{ID: "facts_lookup", FromDataset: "order_facts", FromFields: []string{"customer_id"}, ToDataset: "customer_lookup", ToFields: []string{"customer_id"}, Cardinality: "many_to_one"}}
	joinedModel.Dimensions["customer_country"] = semanticmodel.SemanticDimension{Bindings: map[string]semanticmodel.DimensionBinding{
		"order_facts": {Field: "customer_lookup.country", Path: []string{"facts_lookup"}},
	}}
	joinedAlias := agentVisualFieldUsage("sales", "commerce", joinedModel, agentVisualFieldRef{Field: "customer_country"}, "dimension", "order_facts")
	if joinedAlias.ExplorerFieldID == nil || *joinedAlias.ExplorerFieldID != "customer_lookup.country" {
		t.Fatalf("aliased joined semantic dimension explorer binding = %#v, want customer_lookup.country", joinedAlias.ExplorerFieldID)
	}

	model.Dimensions["country"].Bindings["orders"] = semanticmodel.DimensionBinding{Field: "orders.country", Path: []string{"orders_customers"}}
	unsafe := agentVisualFieldUsage("sales", "commerce", model, agentVisualFieldRef{Field: "country", Alias: "country"}, "dimension", "orders")
	if unsafe.ExplorerFieldID != nil {
		t.Fatalf("non-root dimension binding exposed to Explorer: %#v", unsafe.ExplorerFieldID)
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
		if err := budget.ConsumeSize(agentDefinitionLimit(definition)+1, 1); err != nil {
			t.Fatalf("canonical runtime result budget rejected one sentinel row: %v", err)
		}
		if err := budget.ConsumeSize(1, 1); err == nil {
			t.Fatal("canonical runtime result budget accepted more than one sentinel row")
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

func TestAgentVisualRunTrimsRecordsSentinelToBudget(t *testing.T) {
	for _, rowLimit := range []int{maxVisualRows, 7} {
		t.Run(fmt.Sprintf("budget_%d", rowLimit), func(t *testing.T) {
			model := testAgentModel()
			rows := make([][]any, rowLimit+1)
			for i := range rows {
				rows[i] = []any{fmt.Sprintf("order-%d", i)}
			}
			provider := VisualProvider{
				Resolve: func(_ context.Context, _ Scope, id projectgraph.ResourceID, _ projectgraph.Kind, _ access.Capability) (projectgraph.ResourceID, error) {
					return id, nil
				},
				SemanticModel: func(string, string) (*semanticmodel.Model, bool) { return model, true },
				QueryMetadata: func(context.Context, string, string) VisualQueryMetadata {
					return VisualQueryMetadata{ServingSnapshot: "snapshot-1"}
				},
				QueryDefinition: func(ctx context.Context, _ string, definition dashboarddefinition.Definition, _, visualID string, _ dashboard.Filters) (visualizationir.VisualizationEnvelope, error) {
					if got := definition.Visualizations[visualID].Query.Detail.Limit; got != int64(rowLimit) {
						t.Fatalf("records query limit = %d, want %d", got, rowLimit)
					}
					budget, ok := dataquery.ResultBudgetFromContext(ctx)
					if !ok {
						t.Fatal("canonical runtime context has no independent result budget")
					}
					if err := budget.ConsumeSize(rowLimit+1, 1); err != nil {
						t.Fatalf("canonical runtime result budget rejected sentinel row: %v", err)
					}
					if err := budget.ConsumeSize(1, 1); err == nil {
						t.Fatal("canonical runtime result budget accepted more than one sentinel row")
					}
					return visualizationir.VisualizationEnvelope{
						VisualID: visualID,
						Spec:     definition.Visualizations[visualID].Spec,
						DataState: visualizationir.VisualizationDataState{Value: &visualizationir.InlineVisualizationDataState{
							VisualizationDataStateBase: visualizationir.VisualizationDataStateBase{Kind: "inline"},
							Kind:                       "inline",
							Datasets:                   []visualizationir.VisualizationInlineDataset{{ID: "primary", Rows: rows}},
						}},
						Status: visualizationir.VisualizationStatus{Kind: visualizationir.VisualizationStatusKindReady},
					}, nil
				},
			}
			result := provider.Run(context.Background(), Scope{ProjectID: "project", PrincipalID: "principal"}, agentcore.ToolCall{
				ID:        "records-51",
				Arguments: json.RawMessage(fmt.Sprintf(`{"semanticModelId":"orders","visual":{"type":"table","query":{"type":"records","dataset":"orders","fields":[{"field":"order_id"}]},"dataBudget":{"maxRows":%d},"presentation":{"type":"table","rowHeight":32,"showHeader":true,"striped":false}}}`, rowLimit)),
			})
			if result.IsError {
				t.Fatalf("Run() failed: %#v", result.Content)
			}
			content, ok := result.Content.(agentcontracts.QueryVisualResult)
			if !ok {
				t.Fatalf("content type = %T, want QueryVisualResult", result.Content)
			}
			if !content.Ok || content.Completeness.ReturnedRows != int32(rowLimit) || content.Completeness.Status != "limit_reached" {
				t.Fatalf("compact completeness = %#v, ok=%t; want %d rows and limit_reached", content.Completeness, content.Ok, rowLimit)
			}
			modelContent, ok := result.ModelContent.(agentVisualModelProjection)
			if !ok {
				t.Fatalf("model content type = %T, want bounded visual projection", result.ModelContent)
			}
			if len(modelContent.Rows) != rowLimit || modelContent.DataCompleteness.PrimaryResultRows != int32(rowLimit) || modelContent.DataCompleteness.Status != "limit_reached" {
				t.Fatalf("model projection completeness = %#v with %d rows, want %d rows and limit_reached", modelContent.DataCompleteness, len(modelContent.Rows), rowLimit)
			}
			display, ok := result.DisplayContent.(agentVisualResult)
			if !ok {
				t.Fatalf("display content type = %T, want agentVisualResult", result.DisplayContent)
			}
			envelope := display.Patch["visuals"][display.ID]
			primaryRows := agentVisualReturnedRows(envelope)
			if primaryRows != rowLimit {
				t.Fatalf("displayed primary rows = %d, want %d", primaryRows, rowLimit)
			}
			if len(envelope.Diagnostics) == 0 || envelope.Diagnostics[len(envelope.Diagnostics)-1].Code != "agent_row_limit_reached" {
				t.Fatalf("diagnostics = %#v, want agent row limit warning", envelope.Diagnostics)
			}
		})
	}
}

func TestTrimAgentVisualEnvelopeRowsCapsWindowedBlocks(t *testing.T) {
	rows := func(count int) [][]any {
		out := make([][]any, count)
		for i := range out {
			out[i] = []any{i}
		}
		return out
	}
	envelope := visualizationir.VisualizationEnvelope{DataState: visualizationir.VisualizationDataState{Value: &visualizationir.WindowedVisualizationDataState{
		Kind: "windowed",
		Blocks: map[string]visualizationir.VisualizationWindowBlock{
			"first":  {ID: "first", Start: 0, Rows: rows(30)},
			"second": {ID: "second", Start: 30, Rows: rows(30)},
		},
	}}}
	if !trimAgentVisualEnvelopeRows(&envelope, maxVisualRows) {
		t.Fatal("trimAgentVisualEnvelopeRows() = false, want a windowed cap")
	}
	if got := agentVisualReturnedRows(envelope); got != maxVisualRows {
		t.Fatalf("windowed returned rows = %d, want %d", got, maxVisualRows)
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
