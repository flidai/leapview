package authoring

import (
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestSwitchToKPIDropsUnsupportedDimensionBindings(t *testing.T) {
	target := defaultCanonicalVisual("kpi", "Revenue")
	query := canonicalVisualSwitchQuery(target.Query, document.DashboardVisualTypeKpi, &VisualTypeFieldBindings{Dimensions: []string{"country"}, Metrics: []string{"revenue", "orders"}})
	aggregate := query.Value.(*document.AggregateDashboardQuery)
	if len(aggregate.Dimensions) != 0 {
		t.Fatalf("KPI retained dimensions: %v", aggregate.Dimensions)
	}
	if len(aggregate.Metrics) != 1 {
		t.Fatalf("KPI metrics = %d", len(aggregate.Metrics))
	}
}
