package authoring

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestVisualQueryUsesDatasetAndRejectsLegacyTable(t *testing.T) {
	var query VisualQuery
	if err := yaml.Unmarshal([]byte("dataset: orders\nmetrics: [revenue]\n"), &query); err != nil {
		t.Fatalf("dataset query rejected: %v", err)
	}
	if query.Dataset != "orders" {
		t.Fatalf("dataset query target = %q, want orders", query.Dataset)
	}
	if err := yaml.Unmarshal([]byte("table: orders\nmetrics: [revenue]\n"), &query); err == nil {
		t.Fatal("legacy table query accepted")
	}
}

func TestTableQueryUsesDatasetAndRejectsLegacyTable(t *testing.T) {
	var query TableQuery
	if err := yaml.Unmarshal([]byte("dataset: orders\nfields: [orders.order_id]\n"), &query); err != nil {
		t.Fatalf("dataset table query rejected: %v", err)
	}
	if query.Dataset != "orders" {
		t.Fatalf("dataset table query target = %q, want orders", query.Dataset)
	}
	if err := yaml.Unmarshal([]byte("table: orders\nfields: [orders.order_id]\n"), &query); err == nil {
		t.Fatal("legacy table query accepted")
	}
}

func TestDerivedDistributionVisualsRequireRawTableTarget(t *testing.T) {
	t.Parallel()

	tests := map[string]Visual{
		"histogram": {
			Type:  "histogram",
			Query: VisualQuery{Metrics: []FieldRef{{Field: "delivery_days"}}},
		},
		"boxplot": {
			Type: "boxplot",
			Query: VisualQuery{
				Dimensions: []FieldRef{{Field: "orders.delivery_bucket"}},
				Metrics:    []FieldRef{{Field: "delivery_days"}},
			},
		},
	}

	for name, visual := range tests {
		visual := visual
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := validateVisualQueryShape(name, visual)
			if err == nil || !strings.Contains(err.Error(), "requires query.dataset") {
				t.Fatalf("validateVisualQueryShape() error = %v, want missing query.dataset", err)
			}
		})
	}
}
