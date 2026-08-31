package observability

import (
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/prometheus/client_golang/prometheus"
	io_prometheus_client "github.com/prometheus/client_model/go"
)

func TestCacheCutoverReuseMetricUsesRetainedHitSource(t *testing.T) {
	registry := prometheus.NewRegistry()
	telemetry := New(registry)
	telemetry.DashboardCacheObservationObserved(dataquery.CacheObservation{
		Phase:     dataquery.CacheObservationLookup,
		HitSource: dataquery.CacheHitCutoverRetained,
		Duration:  25 * time.Microsecond,
	})
	telemetry.DashboardCacheObservationObserved(dataquery.CacheObservation{
		Phase:     dataquery.CacheObservationFinal,
		Outcome:   dataquery.CacheObservationHit,
		HitSource: dataquery.CacheHitCutoverRetained,
		Duration:  250 * time.Microsecond,
	})

	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	foundRetained := false
	for _, family := range families {
		if family.GetName() != "leapview_dashboard_query_cache_hits_total" {
			continue
		}
		for _, metric := range family.Metric {
			if len(metric.Label) != 1 || metric.Label[0].GetName() != "source" {
				t.Fatalf("cutover hit metric labels = %#v, want one bounded source label", metric.Label)
			}
			if source := metric.Label[0].GetValue(); source != string(dataquery.CacheHitCutoverRetained) {
				t.Fatalf("unexpected cutover hit source series %q", source)
			}
			if got := metric.Counter.GetValue(); got != 1 {
				t.Fatalf("cutover-retained hits = %v, want 1", got)
			}
			foundRetained = true
		}
	}
	if !foundRetained {
		t.Fatal("cutover-retained hit metric is missing")
	}
	if got := cacheCutoverHistogramCount(t, families, "leapview_dashboard_query_cache_lookup_duration_seconds", "outcome", "hit"); got != 1 {
		t.Fatalf("cutover lookup duration samples = %d, want 1", got)
	}
	if got := cacheCutoverHistogramCount(t, families, "leapview_dashboard_query_cache_request_duration_seconds", "outcome", "hit"); got != 1 {
		t.Fatalf("cutover request duration samples = %d, want 1", got)
	}
}

func cacheCutoverHistogramCount(
	t *testing.T,
	families []*io_prometheus_client.MetricFamily,
	name, labelName, labelValue string,
) uint64 {
	t.Helper()
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				if label.GetName() == labelName && label.GetValue() == labelValue {
					return metric.Histogram.GetSampleCount()
				}
			}
		}
	}
	t.Fatalf("histogram %s{%s=%q} not found", name, labelName, labelValue)
	return 0
}
