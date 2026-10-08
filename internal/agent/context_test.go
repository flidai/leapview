package agent

import (
	"encoding/json"
	"strings"
	"testing"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
)

func TestTurnContextItemsIncludeResolvedResourceReferences(t *testing.T) {
	items := turnContextItems(&TurnContext{
		Surface: "chat",
		References: []TurnReference{{
			Reference: TurnReferenceKey{Kind: "metric", ID: "orders.order_count"},
			Name:      "Order count",
			Resource:  TurnReferenceResource{ID: "sales", Name: "Sales"},
			ModelID:   "sales",
			DatasetID: "orders",
			FieldID:   "order_count",
		}},
	})
	if len(items) != 1 || items[0].Key != "leapview_context" {
		t.Fatalf("context items = %#v", items)
	}
	payload, err := json.Marshal(items[0].Value)
	if err != nil {
		t.Fatal(err)
	}
	input := string(payload)

	for _, want := range []string{`"surface":"chat"`, `"kind":"metric"`, "Order count"} {
		if !strings.Contains(input, want) {
			t.Fatalf("contextual input missing %q:\n%s", want, input)
		}
	}
}

func TestTurnContextNormalizationKeepsSameReferenceIDAcrossKinds(t *testing.T) {
	normalized := (TurnContext{
		Surface: "chat",
		References: []TurnReference{
			{Reference: TurnReferenceKey{Kind: "field", ID: "orders.revenue"}},
			{Reference: TurnReferenceKey{Kind: "field", ID: "orders.revenue"}},
		},
	}).normalized()

	if got := len(normalized.References); got != 1 {
		t.Fatalf("normalized references = %#v, want one deduplicated reference", normalized.References)
	}
}

func TestTurnContextItemsIncludeBoundedDataExploration(t *testing.T) {
	items := turnContextItems(&TurnContext{
		Surface: "data", ModelID: " commerce ", DatasetID: " orders ",
		Exploration: &exploration.ExplorationSpec{
			SchemaVersion: 1, ModelID: "commerce", Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}},
			Metrics: []exploration.ExplorationMetricRef{{Field: "order_count"}}, Filters: []exploration.ExplorationFilter{{
				Field: "orders.status", Expression: exploration.ExplorationFilterExpression{Value: &exploration.ComparisonExplorationFilterExpression{
					Kind: "comparison", Operator: "equals",
					Value: exploration.ExplorationFilterValue{Value: &exploration.StringExplorationFilterValue{Kind: "string", Value: "delivered"}},
				}},
			}}, Sort: []exploration.ExplorationSort{{Field: "order_count", Direction: exploration.ExplorationSortDirectionDesc}}, Limit: 1000,
		},
	})
	if len(items) != 1 {
		t.Fatalf("context items = %#v", items)
	}
	resolved := items[0].Value.(TurnContext)
	if resolved.ModelID != "commerce" || resolved.DatasetID != "orders" {
		t.Fatalf("resolved identity = %#v", resolved)
	}
	if resolved.Exploration == nil || resolved.Exploration.Limit != 1000 || len(resolved.Exploration.Dimensions) != 1 {
		t.Fatalf("resolved exploration = %#v", resolved.Exploration)
	}
	if _, err := resolved.Exploration.Filters[0].Expression.Kind(); err != nil || resolved.Exploration.Sort[0].Direction != exploration.ExplorationSortDirectionDesc {
		t.Fatalf("normalized exploration = %#v", resolved.Exploration)
	}
}

func TestTurnContextRejectsUnknownExplorationProperties(t *testing.T) {
	var context TurnContext
	err := json.Unmarshal([]byte(`{"surface":"data","exploration":{"schemaVersion":1,"modelId":"commerce","dimensions":[],"metrics":[],"filters":[],"sort":[],"limit":100,"unknown":true}}`), &context)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown exploration property error = %v", err)
	}
}

func TestTurnContextRejectsStrictJSONExplorationDuplicates(t *testing.T) {
	for _, payload := range []string{
		`{"surface":"data","exploration":{"schemaVersion":1,"modelId":"commerce","modelId":"other","dimensions":[],"metrics":[],"filters":[],"sort":[],"limit":100}}`,
		`{"surface":"data","exploration":{"schemaVersion":1,"modelId":"commerce","modelID":"other","dimensions":[],"metrics":[],"filters":[],"sort":[],"limit":100}}`,
	} {
		var context TurnContext
		if err := json.Unmarshal([]byte(payload), &context); err == nil {
			t.Fatalf("accepted duplicate exploration property payload: %s", payload)
		}
	}
}

func TestTurnContextRejectsStrictJSONBounds(t *testing.T) {
	deep := `{"surface":"data","filters":` + strings.Repeat("[", 33) + "0" + strings.Repeat("]", 33) + "}"
	var context TurnContext
	if err := json.Unmarshal([]byte(deep), &context); err == nil {
		t.Fatal("accepted excessively deep context")
	}

	large := `{"surface":"data","dashboardTitle":"` + strings.Repeat("x", int(turnContextMaxBytes)) + `"}`
	if err := json.Unmarshal([]byte(large), &context); err == nil {
		t.Fatal("accepted oversized context")
	}
}

func TestTurnContextRejectsClientProjectSelector(t *testing.T) {
	var context TurnContext
	if err := json.Unmarshal([]byte(`{"surface":"dashboard","projectId":"other-project","dashboardId":"sales","pageId":"overview"}`), &context); err == nil {
		t.Fatal("client project selector was accepted")
	}
}

func TestTurnContextIgnoresBrowserReferenceLimit(t *testing.T) {
	for _, limit := range []string{"0", "12", "1000000"} {
		var context TurnContext
		if err := json.Unmarshal([]byte(`{"surface":"chat","referenceLimit":`+limit+`,"references":[{"reference":{"kind":"metric","id":"orders.revenue"}}]}`), &context); err != nil {
			t.Fatal(err)
		}
		if len(context.References) != 1 || context.References[0].Reference.ID != "orders.revenue" {
			t.Fatalf("browser limit changed references: %#v", context.References)
		}
		payload, err := json.Marshal(context)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(payload), "referenceLimit") {
			t.Fatal("browser presentation limit leaked into model context")
		}
	}
	for _, payload := range []string{
		`{"surface":"chat","referenceLimit":12,"projectId":"other-project"}`,
		`{"surface":"chat","referenceLimit":12,"unknown":true}`,
		`{"surface":"chat","referenceLimit":12,"referenceLimit":1000000}`,
	} {
		var context TurnContext
		if err := json.Unmarshal([]byte(payload), &context); err == nil {
			t.Fatalf("accepted invalid browser context: %s", payload)
		}
	}
}

func TestBuilderTurnContextIsIncludedForAgentPrompt(t *testing.T) {
	items := turnContextItems(&TurnContext{Surface: "dashboard_builder", DashboardID: "dashboard_sales", DraftID: "draft_1", DraftRevision: &DraftRevision{RevisionID: "revision_7", Number: 7, ContentHash: "sha256:abc"}})
	if len(items) != 2 {
		t.Fatalf("builder context items = %#v", items)
	}
	context, ok := items[0].Value.(TurnContext)
	if !ok || context.DraftRevision == nil || context.DraftRevision.RevisionID != "revision_7" {
		t.Fatalf("builder prompt context = %#v", items[0].Value)
	}
	policy, _ := items[1].Value.(string)
	if items[1].Key != "leapview_builder_v1_policy" || !strings.Contains(policy, "do not claim the Builder chart failed") {
		t.Fatalf("builder policy context = %#v", items[1])
	}
}

func TestTurnContextItemsRetainBuilderDestination(t *testing.T) {
	items := turnContextItems(&TurnContext{Surface: "builder", DashboardID: "draft", PageID: "bars", References: []TurnReference{{Reference: TurnReferenceKey{Kind: "dashboard", ID: "draft"}}}})
	if len(items) != 1 {
		t.Fatalf("builder context lost: %#v", items)
	}
	resolved := items[0].Value.(TurnContext)
	if resolved.Surface != "builder" || resolved.PageID != "bars" || resolved.DashboardID != "draft" {
		t.Fatalf("wrong builder context: %+v", resolved)
	}
}
