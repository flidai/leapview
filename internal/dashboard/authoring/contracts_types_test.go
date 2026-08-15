package authoring

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestVisualQueryRejectsInlineMeasure(t *testing.T) {
	var query VisualQuery
	err := yaml.Unmarshal([]byte("measures:\n  revenue:\n    expr: SUM(orders.revenue)\n"), &query)
	if err == nil || !strings.Contains(err.Error(), "inline dashboard measures are not supported") {
		t.Fatalf("Unmarshal() error = %v", err)
	}
}
