package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTurnContextItemsIncludeResolvedResourceReferences(t *testing.T) {
	items := turnContextItems(&TurnContext{
		Surface:   "chat",
		ProjectID: "sales",
		References: []TurnReference{{
			Reference: TurnReferenceKey{Kind: "measure", ID: "orders.order_count"},
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

	for _, want := range []string{`"surface":"chat"`, `"kind":"measure"`, "Order count"} {
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

	if got := len(normalized.References); got != 2 {
		t.Fatalf("normalized references = %#v, want one deduplicated reference", normalized.References)
	}
}

func TestTurnContextItemsIncludeBoundedDataExploration(t *testing.T) {
	items := turnContextItems(&TurnContext{
		Surface: "data", ProjectID: " sales ", ModelID: " commerce ", DatasetID: " orders ",
		Exploration: &DataExploration{
			Dimensions: []string{"orders.status", "orders.status", ""}, Measures: []string{"order_count"},
			Filters: []DataExplorationFilter{{Field: " orders.status ", Operator: " EQUALS ", Values: []string{"delivered"}}},
			Sort:    []DataExplorationSort{{Field: "order_count", Direction: "DESC"}}, Limit: 5000,
		},
	})
	if len(items) != 1 {
		t.Fatalf("context items = %#v", items)
	}
	resolved := items[0].Value.(TurnContext)
	if resolved.ProjectID != "sales" || resolved.ModelID != "commerce" || resolved.DatasetID != "orders" {
		t.Fatalf("resolved identity = %#v", resolved)
	}
	if resolved.Exploration == nil || resolved.Exploration.Limit != 1000 || len(resolved.Exploration.Dimensions) != 1 {
		t.Fatalf("resolved exploration = %#v", resolved.Exploration)
	}
	if resolved.Exploration.Filters[0].Operator != "equals" || resolved.Exploration.Sort[0].Direction != "desc" {
		t.Fatalf("normalized exploration = %#v", resolved.Exploration)
	}
}
