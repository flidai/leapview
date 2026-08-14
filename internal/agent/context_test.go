package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTurnContextItemsIncludeResolvedWorkspaceReferences(t *testing.T) {
	items := turnContextItems(&TurnContext{
		Surface:     "chat",
		WorkspaceID: "sales",
		References: []TurnReference{{
			Reference: TurnReferenceKey{WorkspaceID: "sales", Type: "measure", ID: "orders.order_count"},
			Name:      "Order count",
			Workspace: TurnReferenceWorkspace{ID: "sales", Name: "Sales"},
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

	for _, want := range []string{`"surface":"chat"`, `"type":"measure"`, "Order count"} {
		if !strings.Contains(input, want) {
			t.Fatalf("contextual input missing %q:\n%s", want, input)
		}
	}
}

func TestTurnContextNormalizationKeepsSameReferenceIDAcrossWorkspaces(t *testing.T) {
	normalized := (TurnContext{
		Surface: "chat",
		References: []TurnReference{
			{Reference: TurnReferenceKey{WorkspaceID: "sales", Type: "field", ID: "orders.revenue"}},
			{Reference: TurnReferenceKey{WorkspaceID: "visuals", Type: "field", ID: "orders.revenue"}},
		},
	}).normalized()

	if got := len(normalized.References); got != 2 {
		t.Fatalf("normalized references = %#v, want two workspace-qualified references", normalized.References)
	}
}

func TestTurnContextItemsIncludeBoundedDataExploration(t *testing.T) {
	items := turnContextItems(&TurnContext{
		Surface: "data", WorkspaceID: " sales ", ModelID: " commerce ", DatasetID: " orders ",
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
	if resolved.WorkspaceID != "sales" || resolved.ModelID != "commerce" || resolved.DatasetID != "orders" {
		t.Fatalf("resolved identity = %#v", resolved)
	}
	if resolved.Exploration == nil || resolved.Exploration.Limit != 1000 || len(resolved.Exploration.Dimensions) != 1 {
		t.Fatalf("resolved exploration = %#v", resolved.Exploration)
	}
	if resolved.Exploration.Filters[0].Operator != "equals" || resolved.Exploration.Sort[0].Direction != "desc" {
		t.Fatalf("normalized exploration = %#v", resolved.Exploration)
	}
}
