package module

import (
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/pkg/arrowresult"
	"github.com/prometheus/client_golang/prometheus"
	io_prometheus_client "github.com/prometheus/client_model/go"
)

func TestCacheCutoverRolloutMetricsExposeLifecycleState(t *testing.T) {
	cache, err := resultcache.New(resultcache.Limits{
		RuntimeEntries: 1,
		RuntimeBytes:   1 << 20,
		NodeEntries:    4,
		NodeBytes:      4 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()

	dormant, err := cache.OpenSharedScope(resultcache.ScopeID{RuntimeID: "rollout-dormant"})
	if err != nil {
		t.Fatal(err)
	}
	storeCacheCutoverMetricResult(t, dormant, "retained", "retained")
	if err := dormant.Close(); err != nil {
		t.Fatal(err)
	}
	reactivated, err := cache.OpenSharedScope(resultcache.ScopeID{RuntimeID: "rollout-dormant"})
	if err != nil {
		t.Fatal(err)
	}
	lease, _, hit, observation, err := reactivated.LookupArrowObserved("retained", resultcache.QueryFamily{1})
	if err != nil || !hit || observation.HitSource != resultcache.HitCutoverRetained {
		t.Fatalf("reactivated lookup hit=%v observation=%#v err=%v", hit, observation, err)
	}
	lease.Release()
	if err := reactivated.Close(); err != nil {
		t.Fatal(err)
	}

	active, err := cache.OpenSharedScope(resultcache.ScopeID{RuntimeID: "rollout-active"})
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	storeCacheCutoverMetricResult(t, active, "first", "first")
	storeCacheCutoverMetricResult(t, active, "second", "second")
	active.Invalidate()

	registry := prometheus.NewRegistry()
	registry.MustRegister(NewCollector(nil, cache))
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, expectation := range []struct {
		name   string
		labels map[string]string
		want   float64
	}{
		{name: "leapview_query_result_cache_scopes", labels: map[string]string{"state": "active"}, want: 1},
		{name: "leapview_query_result_cache_scopes", labels: map[string]string{"state": "dormant"}, want: 1},
		{name: "leapview_query_result_cache_entries", want: 1},
		{name: "leapview_query_result_cache_arrow_holds", want: 1},
		{name: "leapview_cache_invalidations_total", labels: map[string]string{"cache": "stable_result"}, want: 1},
		{name: "leapview_cache_invalidated_entries_total", labels: map[string]string{"cache": "stable_result"}, want: 1},
		{name: "leapview_cache_evicted_entries_total", labels: map[string]string{"cache": "stable_result", "constraint": "runtime"}, want: 1},
		{name: "leapview_query_result_cache_scope_transitions_total", labels: map[string]string{"transition": "created"}, want: 2},
		{name: "leapview_query_result_cache_scope_transitions_total", labels: map[string]string{"transition": "dormant"}, want: 2},
		{name: "leapview_query_result_cache_scope_transitions_total", labels: map[string]string{"transition": "reactivated"}, want: 1},
	} {
		if got := cacheCutoverMetricValue(t, families, expectation.name, expectation.labels); got != expectation.want {
			t.Fatalf("%s%v = %v, want %v", expectation.name, expectation.labels, got, expectation.want)
		}
	}
	if got := cacheCutoverMetricValue(t, families, "leapview_query_result_cache_bytes", nil); got <= 0 {
		t.Fatalf("retained stable bytes = %v, want positive", got)
	}
}

func storeCacheCutoverMetricResult(t *testing.T, scope *resultcache.Scope, key, value string) {
	t.Helper()
	result := cacheCutoverMetricArrowResult(t, value)
	if outcome := scope.StoreArrowObserved(key, resultcache.QueryFamily{1}, scope.Generation(), result, resultcache.Metadata{}); outcome != resultcache.StoreStored {
		result.Release()
		t.Fatalf("store cache cutover metric result = %q, want %q", outcome, resultcache.StoreStored)
	}
	result.Release()
}

func cacheCutoverMetricArrowResult(t *testing.T, value string) *arrowresult.Result {
	t.Helper()
	builder := array.NewStringBuilder(memory.DefaultAllocator)
	builder.Append(value)
	values := builder.NewArray()
	builder.Release()
	record := array.NewRecordBatch(
		arrow.NewSchema([]arrow.Field{{Name: "value", Type: arrow.BinaryTypes.String}}, nil),
		[]arrow.Array{values},
		1,
	)
	values.Release()
	collector := arrowresult.NewBuilder()
	if err := collector.WriteSchema(record.Schema()); err != nil {
		record.Release()
		t.Fatal(err)
	}
	if err := collector.WriteRecord(record); err != nil {
		record.Release()
		t.Fatal(err)
	}
	record.Release()
	result, err := collector.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func cacheCutoverMetricValue(
	t *testing.T,
	families []*io_prometheus_client.MetricFamily,
	name string,
	wantLabels map[string]string,
) float64 {
	t.Helper()
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.Metric {
			labels := make(map[string]string, len(metric.Label))
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
			}
			if !cacheCutoverMetricLabelsEqual(labels, wantLabels) {
				continue
			}
			if metric.Gauge != nil {
				return metric.Gauge.GetValue()
			}
			if metric.Counter != nil {
				return metric.Counter.GetValue()
			}
			t.Fatalf("metric %s%v is not a gauge or counter", name, wantLabels)
		}
	}
	t.Fatalf("metric %s%v not found", name, wantLabels)
	return 0
}

func cacheCutoverMetricLabelsEqual(got, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for name, value := range want {
		if got[name] != value {
			return false
		}
	}
	return true
}
