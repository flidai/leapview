package project

import (
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestSemanticModelAssetPayloadProjectsCompiledDefinition(t *testing.T) {
	model := &semanticmodel.Model{
		Name:        "sales",
		Title:       "Sales",
		Description: "Sales model",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				PrimaryKey: "order_id",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"status": {Label: "Status", Type: "string"},
				},
			},
			"customers": {PrimaryKey: "customer_id"},
		},
		Measures: map[string]semanticmodel.MetricMeasure{
			"order_count": {Fact: "orders", Aggregation: "count"},
		},
		Relationships: []semanticmodel.Relationship{{
			ID: "orders_customer", From: "orders.customer_id", To: "customers.customer_id", Cardinality: "many_to_one",
		}},
	}

	payload := SemanticModelAssetPayload(model)
	tables, ok := payload["Tables"].(map[string]any)
	if !ok || len(tables) != 2 {
		t.Fatalf("tables = %#v, want two projected tables", payload["Tables"])
	}
	measures, ok := payload["Measures"].(map[string]any)
	if !ok || len(measures) != 1 {
		t.Fatalf("measures = %#v, want one projected measure", payload["Measures"])
	}
	relationships, ok := payload["Relationships"].([]any)
	if !ok || len(relationships) != 1 {
		t.Fatalf("relationships = %#v, want one projected relationship", payload["Relationships"])
	}
	orders, ok := tables["orders"].(map[string]any)
	if !ok || orders["PrimaryKey"] != "order_id" {
		t.Fatalf("orders table = %#v, want primary key", tables["orders"])
	}
}
