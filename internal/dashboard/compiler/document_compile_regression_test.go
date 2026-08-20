package compiler

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestPendingMetricRegression(t *testing.T) {
	model := dashboardQueryTestModel()

	t.Run("histogram", func(t *testing.T) {
		metric := "pending_metric"
		query := document.HistogramDashboardQuery{Field: document.DashboardMetricSelection{String: &metric}}
		_, err := lowerCanonicalHistogram(query, model, "model")
		if err == nil {
			t.Fatal("expected error for pending_metric, got nil")
		}
		if !strings.Contains(err.Error(), "histogram requires a metric") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("distribution", func(t *testing.T) {
		metric := "pending_metric"
		query := document.DistributionDashboardQuery{Field: document.DashboardMetricSelection{String: &metric}}
		_, err := lowerCanonicalDistribution(query, model, "model")
		if err == nil {
			t.Fatal("expected error for pending_metric, got nil")
		}
		if !strings.Contains(err.Error(), "distribution requires a metric") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})
}
