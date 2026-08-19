package module

import (
	"context"
	"testing"

	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/prometheus/client_golang/prometheus"
)

func TestAnalyticalCollectorUsesBoundedLabels(t *testing.T) {
	database, err := analyticsducklake.Open(context.Background(), analyticsducklake.Config{RootDir: t.TempDir(), MaxConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	cache, err := resultcache.New(resultcache.Limits{RuntimeEntries: 1, RuntimeBytes: 1024, NodeEntries: 1, NodeBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()

	registry := prometheus.NewRegistry()
	registry.MustRegister(NewCollector(database, cache))
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"leapview_arrow_result_leases":     false,
		"leapview_arrow_transient_bytes":   false,
		"leapview_duckdb_connections_open": false,
		"leapview_query_cache_entries":     false,
		"leapview_query_cache_store_total": false,
	}
	for _, family := range families {
		if _, ok := want[family.GetName()]; !ok {
			continue
		}
		want[family.GetName()] = true
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				switch label.GetName() {
				case "workspace", "runtime", "generation", "operation", "request":
					t.Fatalf("unbounded label %q", label.GetName())
				}
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("metric %s missing", name)
		}
	}
}

func TestAnalyticalCollectorExportsZeroProcessConnectionsWithoutProcessEnvironment(t *testing.T) {
	registry := prometheus.NewRegistry()
	registry.MustRegister(NewCollector(nil, nil))
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "leapview_duckdb_connections_open" {
			continue
		}
		if len(family.Metric) != 1 || family.Metric[0].GetGauge().GetValue() != 0 {
			t.Fatalf("connections-open metrics = %#v, want one zero gauge", family.Metric)
		}
		return
	}
	t.Fatal("leapview_duckdb_connections_open metric missing")
}
