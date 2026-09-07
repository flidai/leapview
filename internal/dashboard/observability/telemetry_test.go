package observability

import (
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/prometheus/client_golang/prometheus"
)

func TestTelemetryObservesAcceptedProgressiveTargetsAndFrames(t *testing.T) {
	registry := prometheus.NewRegistry()
	telemetry := New(registry)
	for _, event := range []struct {
		eventType string
		target    string
	}{
		{eventType: "visual", target: "revenue"},
		{eventType: "table", target: "orders"},
		{eventType: "target_error", target: "visual:broken"},
		{eventType: "target_error", target: "refresh"},
		{eventType: "complete"},
	} {
		telemetry.DashboardRefreshEventObserved(event.eventType, event.target)
	}
	telemetry.VisualizationFrameObserved("inline", 1, 1, 10)
	telemetry.VisualizationFrameObserved("windowed", 1, 1, 20)

	want := map[string]float64{
		"refresh:error":  1,
		"visual:error":   1,
		"visual:success": 2,
	}
	got := targetMetricValues(t, registry)
	if len(got) != len(want) {
		t.Fatalf("target outcome metric series = %#v, want %#v", got, want)
	}
	for labels, count := range want {
		if got[labels] != count {
			t.Fatalf("target outcome %s = %v, want %v (all %#v)", labels, got[labels], count, got)
		}
	}
	for _, name := range []string{"leapview_visualization_frame_rows", "leapview_visualization_frame_size_bytes", "leapview_visualization_cardinality"} {
		if got := histogramSampleCount(t, registry, name); got != 2 {
			t.Fatalf("%s sample count = %d, want 2", name, got)
		}
	}
}

func TestTelemetryUsesBoundedLabelsAndRecordsRefreshLifecycle(t *testing.T) {
	registry := prometheus.NewRegistry()
	telemetry := New(registry)
	telemetry.DashboardRefreshStarted("select")
	telemetry.DashboardRefreshFinished("select", "complete", 2, map[string]float64{
		"endToEnd": 42,
		"planning": 3,
	})
	telemetry.DashboardCacheObserved("hit")
	telemetry.DashboardCacheObserved("coalesced")
	telemetry.DashboardTargetObserved("visual", "success")

	for name, want := range map[string]float64{
		"leapview_dashboard_refreshes_in_flight":         0,
		"leapview_dashboard_refresh_cancellations_total": 2,
		"leapview_dashboard_cache_outcomes_total":        1,
		"leapview_dashboard_target_outcomes_total":       1,
	} {
		if got := metricValue(t, registry, name); got != want {
			t.Fatalf("%s = %v, want %v", name, got, want)
		}
	}

	for name, test := range map[string]struct {
		raw   string
		label func(string) string
	}{
		"command": {raw: "select:dashboard-tenant-123", label: commandLabel},
		"outcome": {raw: "failed-for-user-123", label: outcomeLabel},
		"stage":   {raw: "target:customer-123", label: stageLabel},
		"cache":   {raw: "hit:customer-123", label: cacheLabel},
		"kind":    {raw: "visual:customer-123", label: targetKindLabel},
	} {
		if got := test.label(test.raw); got != "other" {
			t.Fatalf("%s label for %q = %q, want other", name, test.raw, got)
		}
	}
	if got := cacheLabel("coalesced"); got != "coalesced" {
		t.Fatalf("coalesced cache label = %q, want coalesced", got)
	}
	if got := stageLabel("targetWorkSum"); got != "target_work_sum" {
		t.Fatalf("target work sum stage label = %q, want target_work_sum", got)
	}
	if got := stageLabel("targetCriticalPath"); got != "target_critical_path" {
		t.Fatalf("target critical path stage label = %q, want target_critical_path", got)
	}
}

func TestCacheValidityTelemetryCoversFixedReasonsWithoutIdentityLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	telemetry := New(registry)
	for _, test := range []struct {
		decision dataquery.CacheAdmissionDecision
		reason   dataquery.CacheAdmissionReason
	}{
		{dataquery.CacheAdmissionEligible, dataquery.CacheAdmissionReasonEligible},
		{dataquery.CacheAdmissionBypassed, dataquery.CacheAdmissionReasonQueryNotCacheable},
		{dataquery.CacheAdmissionRejected, dataquery.CacheAdmissionReasonPlanningFailed},
		{dataquery.CacheAdmissionRejected, dataquery.CacheAdmissionReasonCanceled},
		{dataquery.CacheAdmissionBypassed, dataquery.CacheAdmissionReasonDependencyUnavailable},
		{dataquery.CacheAdmissionBypassed, dataquery.CacheAdmissionReasonDependencyInvalid},
		{dataquery.CacheAdmissionBypassed, dataquery.CacheAdmissionReasonPolicyInvalid},
		{dataquery.CacheAdmissionBypassed, dataquery.CacheAdmissionReasonPartitionInvalid},
		{dataquery.CacheAdmissionBypassed, dataquery.CacheAdmissionReasonNonDeterministic},
	} {
		telemetry.DashboardCacheObservationObserved(dataquery.CacheObservation{Phase: dataquery.CacheObservationAdmission, Decision: test.decision, AdmissionReason: test.reason})
	}
	for _, reason := range []dataquery.CacheLookupMissReason{
		dataquery.CacheLookupMissColdStart, dataquery.CacheLookupMissAbsentEntry, dataquery.CacheLookupMissQueryMismatch,
		dataquery.CacheLookupMissInvalidated, dataquery.CacheLookupMissEvicted,
	} {
		telemetry.DashboardCacheObservationObserved(dataquery.CacheObservation{Phase: dataquery.CacheObservationLookup, MissReason: reason, Duration: time.Microsecond})
	}
	for _, source := range []dataquery.CacheHitSource{
		dataquery.CacheHitCurrentGeneration, dataquery.CacheHitSharedGeneration, dataquery.CacheHitCutoverRetained,
	} {
		telemetry.DashboardCacheObservationObserved(dataquery.CacheObservation{Phase: dataquery.CacheObservationLookup, HitSource: source, Duration: time.Microsecond})
		telemetry.DashboardCacheObservationObserved(dataquery.CacheObservation{Phase: dataquery.CacheObservationFinal, Outcome: dataquery.CacheObservationHit, HitSource: source, Duration: time.Millisecond})
	}
	for _, outcome := range []dataquery.CacheObservationOutcome{
		dataquery.CacheObservationHit, dataquery.CacheObservationMiss, dataquery.CacheObservationCoalesced, dataquery.CacheObservationError,
	} {
		telemetry.DashboardCacheObservationObserved(dataquery.CacheObservation{Phase: dataquery.CacheObservationFinal, Outcome: outcome, Duration: time.Millisecond})
	}
	for _, outcome := range []dataquery.CacheStoreOutcome{
		dataquery.CacheStoreStored, dataquery.CacheStoreOversized, dataquery.CacheStoreStale, dataquery.CacheStoreClosed,
	} {
		telemetry.DashboardCacheObservationObserved(dataquery.CacheObservation{Phase: dataquery.CacheObservationStore, StoreOutcome: outcome})
	}
	secret := "principal-query-project-model-secret"
	telemetry.DashboardCacheObservationObserved(dataquery.CacheObservation{
		Phase: dataquery.CacheObservationAdmission, Decision: dataquery.CacheAdmissionDecision(secret), AdmissionReason: dataquery.CacheAdmissionReason(secret),
	})

	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	wantSeries := map[string]int{
		"leapview_dashboard_query_cache_admissions_total": 10,
		"leapview_dashboard_query_cache_misses_total":     5,
		"leapview_dashboard_query_cache_hits_total":       3,
		"leapview_dashboard_query_cache_stores_total":     4,
	}
	for _, family := range families {
		if strings.Contains(family.String(), secret) {
			t.Fatalf("cache metrics exposed identity/query data in %s", family.GetName())
		}
		if want, ok := wantSeries[family.GetName()]; ok {
			if got := len(family.Metric); got != want {
				t.Fatalf("%s series = %d, want %d", family.GetName(), got, want)
			}
			delete(wantSeries, family.GetName())
		}
	}
	if len(wantSeries) != 0 {
		t.Fatalf("missing cache metric families: %#v", wantSeries)
	}
}

func TestSpatialTileTelemetryUsesOnlyBoundedLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	telemetry := New(registry)
	telemetry.SpatialTileObserved("success", "hit", "raw", 0, 0, 128, 12, false)
	telemetry.SpatialTileObserved("success", "coalesced", "aggregated", 8, 2, 4096, 42, true)
	telemetry.SpatialTileObserved("tenant-secret", "user-secret", "visual-secret", 0, 0, 0, 0, false)
	for name, want := range map[string]uint64{
		"leapview_spatial_tile_stage_duration_seconds": 4,
		"leapview_spatial_tile_size_bytes":             2,
		"leapview_spatial_tile_features":               2,
	} {
		if got := histogramSampleCount(t, registry, name); got != want {
			t.Fatalf("%s sample count = %d, want %d", name, got, want)
		}
	}
	if got := spatialPrecisionLabel("visual-user-123"); got != "unknown" {
		t.Fatalf("unbounded precision label = %q", got)
	}
}

func TestPublicTelemetryPreservesMetricContract(t *testing.T) {
	registry := prometheus.NewRegistry()
	telemetry := New(registry)
	telemetry.PublicDocumentObserved("embed", "success")
	finished := telemetry.PublicStreamStarted("public")
	finished()
	telemetry.PublicCommandObserved("select", "accepted")
	telemetry.PublicRateLimitObserved("stream")

	for name, want := range map[string]float64{
		"leapview_public_dashboard_documents_total":             1,
		"leapview_public_dashboard_streams_active":              0,
		"leapview_public_dashboard_commands_total":              1,
		"leapview_public_dashboard_rate_limit_rejections_total": 1,
	} {
		if got := metricValue(t, registry, name); got != want {
			t.Fatalf("%s = %v, want %v", name, got, want)
		}
	}
}

func histogramSampleCount(t *testing.T, registry *prometheus.Registry, name string) uint64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var count uint64
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.Metric {
			count += metric.Histogram.GetSampleCount()
		}
	}
	return count
}

func targetMetricValues(t *testing.T, registry *prometheus.Registry) map[string]float64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]float64{}
	for _, family := range families {
		if family.GetName() != "leapview_dashboard_target_outcomes_total" {
			continue
		}
		for _, metric := range family.Metric {
			labels := map[string]string{}
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
			}
			values[labels["kind"]+":"+labels["outcome"]] = metric.Counter.GetValue()
		}
	}
	return values
}

func metricValue(t *testing.T, registry *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != name || len(family.Metric) == 0 {
			continue
		}
		metric := family.Metric[0]
		if metric.Gauge != nil {
			return metric.Gauge.GetValue()
		}
		if metric.Counter != nil {
			return metric.Counter.GetValue()
		}
	}
	t.Fatalf("metric %q not found", name)
	return 0
}
