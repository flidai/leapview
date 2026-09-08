package observability

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestPrewarmTelemetryBoundsLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	telemetry := New(registry)
	for _, outcome := range []string{"attempted", "completed", "skipped", "failed", "canceled"} {
		telemetry.DashboardPrewarmObserved(outcome, "executed")
	}
	telemetry.DashboardPrewarmObserved("principal:secret", "SELECT confidential generation-123")
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, family := range families {
		if family.GetName() != "leapview_dashboard_prewarm_outcomes_total" {
			continue
		}
		for _, metric := range family.Metric {
			if len(metric.Label) != 2 {
				t.Fatal("unexpected metric labels")
			}
			for _, label := range metric.Label {
				if strings.Contains(label.GetValue(), "secret") || strings.Contains(label.GetValue(), "SELECT") || strings.Contains(label.GetValue(), "generation") {
					t.Fatal("sensitive label leaked")
				}
				if label.GetValue() == "other" {
					seen = true
				}
			}
		}
	}
	if !seen {
		t.Fatal("unknown labels were not collapsed")
	}
}
