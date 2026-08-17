package authoring

import (
	"strings"
	"testing"
)

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
			if err == nil || !strings.Contains(err.Error(), "requires query.table") {
				t.Fatalf("validateVisualQueryShape() error = %v, want missing query.table", err)
			}
		})
	}
}
