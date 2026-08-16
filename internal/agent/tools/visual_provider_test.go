package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

func TestAgentVisualShapeUsesVisualTypeDefaults(t *testing.T) {
	tests := map[string]string{
		"histogram":   "binned_measure",
		"candlestick": "ohlc",
		"boxplot":     "distribution",
		"heatmap":     "matrix",
		"sankey":      "graph",
		"map":         "geo",
		"sunburst":    "hierarchy",
		"kpi":         "single_value",
	}
	for visualType, want := range tests {
		t.Run(visualType, func(t *testing.T) {
			if got := agentVisualShape(agentVisualInput{Type: visualType}); got != want {
				t.Fatalf("shape = %q, want %q", got, want)
			}
		})
	}
}

func TestAgentVisualInputRejectsLegacyAndUnknownProperties(t *testing.T) {
	for _, property := range []string{"shape", "options", "rendererOptions", "unexpected"} {
		t.Run(property, func(t *testing.T) {
			_, err := decodeAgentVisualInput([]byte(`{"type":"histogram","semanticModelId":"sales","dataset":"orders","` + property + `":{}}`))
			if err == nil || !strings.Contains(err.Error(), property) {
				t.Fatalf("decode error = %v, want closed-contract rejection for %q", err, property)
			}
		})
	}
	schema := string((VisualProvider{}).Definitions(Scope{})[0].InputSchema)
	for _, property := range []string{`"shape"`, `"options"`, `"rendererOptions"`} {
		if strings.Contains(schema, property) {
			t.Fatalf("agent schema still exposes legacy property %s", property)
		}
	}
}

func TestAgentVisualQueryRequiresServingSnapshot(t *testing.T) {
	provider := VisualProvider{Resolve: func(_ context.Context, _ Scope, id projectgraph.ResourceID, _ projectgraph.Kind, _ access.Capability) (projectgraph.ResourceID, error) {
		return id, nil
	}}
	result := provider.Run(context.Background(), Scope{ProjectID: "project", PrincipalID: "principal"}, agentcore.ToolCall{
		ID:        "query-without-snapshot",
		Arguments: json.RawMessage(`{"type":"bar","semanticModelId":"orders","dataset":"orders"}`),
	})
	content, _ := result.Content.(map[string]any)
	failure, _ := content["error"].(map[string]any)
	if !result.IsError || !strings.Contains(failure["message"].(string), "serving snapshot") {
		t.Fatalf("result = %#v; want serving snapshot failure", result)
	}
}

func TestAgentVisualInputAcceptsAndNormalizesGovernedFilters(t *testing.T) {
	input, err := decodeAgentVisualInput([]byte(`{
		"type":"bar",
		"semanticModelId":"commerce",
		"dataset":"commerce.orders",
		"dimensions":[{"field":"commerce.orders.country"}],
		"measures":[{"field":"commerce.revenue"}],
		"filters":[{
			"field":"commerce.orders.country",
			"fact":"commerce.orders",
			"operator":"in",
			"values":["DK","SE"],
			"groups":[{"filters":[{"field":"commerce.orders.status","operator":"not_contains","values":["cancelled"]}]}]
		}]
	}`))
	if err != nil {
		t.Fatalf("decodeAgentVisualInput(): %v", err)
	}
	if input.Dataset != "orders" || len(input.Filters) != 1 {
		t.Fatalf("normalized input = %#v", input)
	}
	want := agentVisualFilter{
		Field: "orders.country", Fact: "orders", Operator: "in", Values: []string{"DK", "SE"},
		Groups: []agentVisualFilterGroup{{Filters: []agentVisualFilter{{
			Field: "orders.status", Operator: "not_contains", Values: []string{"cancelled"},
		}}}},
	}
	if !reflect.DeepEqual(input.Filters[0], want) {
		t.Fatalf("normalized filter = %#v, want %#v", input.Filters[0], want)
	}
}

func TestAgentVisualInputAcceptsGroupOnlyFilters(t *testing.T) {
	input, err := decodeAgentVisualInput([]byte(`{
		"type":"bar",
		"semanticModelId":"commerce",
		"dataset":"orders",
		"dimensions":[{"field":"orders.country"}],
		"measures":[{"field":"revenue"}],
		"filters":[{"groups":[{"filters":[{"field":"orders.country","operator":"equals","values":["DK"]}]}]}]
	}`))
	if err != nil {
		t.Fatalf("decodeAgentVisualInput(): %v", err)
	}
	if len(input.Filters) != 1 || len(input.Filters[0].Groups) != 1 {
		t.Fatalf("group-only filters = %#v", input.Filters)
	}
}

func TestAgentVisualQueriesApplyGovernedFilters(t *testing.T) {
	var captured reportdef.AggregateQuery
	provider := VisualProvider{
		Resolve: func(_ context.Context, _ Scope, id projectgraph.ResourceID, _ projectgraph.Kind, _ access.Capability) (projectgraph.ResourceID, error) {
			return id, nil
		},
		AggregateRows: func(_ context.Context, _, _ string, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
			captured = request
			return reportdef.QueryRows{{"label": "DK", "value": 3}}, nil
		},
	}
	input := agentVisualInput{
		Type: "bar", Dataset: "orders", Model: "commerce",
		Dimensions: []agentVisualFieldRef{{Field: "orders.country"}},
		Measures:   []agentVisualFieldRef{{Field: "revenue"}},
		Filters: []agentVisualFilter{{
			Field: "orders.country", Operator: "equals", Values: []string{"DK"},
		}},
		Limit: 10,
	}
	_, err := provider.agentChartData(context.Background(), "sales", input, "category_value", &semanticmodel.Model{})
	if err != nil {
		t.Fatalf("agentChartData(): %v", err)
	}
	want := []reportdef.QueryFilter{{Field: "orders.country", Operator: "equals", Values: []any{"DK"}}}
	if !reflect.DeepEqual(captured.Filters, want) {
		t.Fatalf("query filters = %#v, want %#v", captured.Filters, want)
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
		Authorize: func(ctx context.Context, _ Scope, _ VisualAuthorizationRequest) (agentcore.ToolResult, bool) {
			authorizedValue, _ = ctx.Value(contextKey{}).(string)
			return apigenAgentToolError("authorization_failed", "stop after context capture"), false
		},
	}

	provider.Run(context.Background(), Scope{ProjectID: "project_demo", PrincipalID: "principal-1"}, agentcore.ToolCall{
		ID:        "call-visual",
		Arguments: json.RawMessage(`{"type":"bar","semanticModelId":"orders","dataset":"orders"}`),
	})

	if authorizedValue != "principal-1" {
		t.Fatalf("decorated query context principal = %q, want principal-1", authorizedValue)
	}
}

func TestAgentVisualFieldUsagePreservesSemanticUnitsAndFormats(t *testing.T) {
	model := &semanticmodel.Model{
		Measures: map[string]semanticmodel.MetricMeasure{
			"return_rate": {Label: "Return rate", Unit: "percent", Format: "percent_1"},
		},
	}
	got := agentVisualFieldUsage("sales", "commerce", model, agentVisualFieldRef{Field: "return_rate", Alias: "rate"}, "measure")
	if got.Role != "measure" || got.FieldID != "commerce.return_rate" || got.Label != "Return rate" ||
		got.Alias == nil || *got.Alias != "rate" || got.Unit == nil || *got.Unit != "percent" ||
		got.Format == nil || *got.Format != "percent_1" {
		t.Fatalf("field usage = %#v", got)
	}
}

func TestAgentHistogramProducesBinnedPayload(t *testing.T) {
	provider := VisualProvider{
		Histogram: func(context.Context, string, string, reportdef.RawValueQuery, int) ([]reportdef.HistogramBin, error) {
			return []reportdef.HistogramBin{{Bucket: 0, Count: 4, Start: 10, End: 20}}, nil
		},
	}
	input := agentVisualInput{
		Type: "histogram", Dataset: "orders", Model: "sales",
		Measures: []agentVisualFieldRef{{Field: "revenue"}}, Presentation: agentVisualPresentation{HistogramBins: 12},
	}
	data, err := provider.agentChartData(context.Background(), "sales", input, agentVisualShape(input), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 1 || data[0]["binStart"] != float64(10) || data[0]["binEnd"] != float64(20) || data[0]["value"] != 4 {
		t.Fatalf("histogram data = %#v", data)
	}
}
