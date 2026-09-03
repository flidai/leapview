package postgres

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	io_prometheus_client "github.com/prometheus/client_model/go"
)

func TestPoolMetricsCollectorExportsNamedPoolStats(t *testing.T) {
	config, err := pgxpool.ParseConfig("postgres://localhost/metrics")
	if err != nil {
		t.Fatal(err)
	}
	config.MinConns = 0
	config.MaxConns = 5
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	registry := prometheus.NewRegistry()
	registry.MustRegister(NewPoolMetricsCollector(
		NamedPool{Name: ControlRuntimePoolName, Pool: &Pool{pool: pool}},
		NamedPool{Name: "", Pool: &Pool{pool: pool}},
		NamedPool{Name: DuckLakeRuntimePoolName, Pool: nil},
	))
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"leapview_postgres_pool_max_connections",
		"leapview_postgres_pool_total_connections",
		"leapview_postgres_pool_acquired_connections",
		"leapview_postgres_pool_idle_connections",
		"leapview_postgres_pool_constructing_connections",
		"leapview_postgres_pool_acquire_count_total",
		"leapview_postgres_pool_acquire_duration_seconds_total",
		"leapview_postgres_pool_empty_acquire_count_total",
		"leapview_postgres_pool_canceled_acquire_count_total",
	} {
		metric := gatheredPoolMetric(t, families, name)
		if len(metric.Label) != 1 || metric.Label[0].GetName() != "pool" || metric.Label[0].GetValue() != ControlRuntimePoolName {
			t.Fatalf("%s labels = %v, want one pool=%q label", name, metric.Label, ControlRuntimePoolName)
		}
	}
	if got := gatheredPoolMetric(t, families, "leapview_postgres_pool_max_connections").GetGauge().GetValue(); got != 5 {
		t.Fatalf("max connections = %v, want 5", got)
	}
}

func TestPoolMetricsCollectorDeduplicatesPoolNames(t *testing.T) {
	config, err := pgxpool.ParseConfig("postgres://localhost/metrics")
	if err != nil {
		t.Fatal(err)
	}
	config.MinConns = 0
	config.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	registry := prometheus.NewRegistry()
	registry.MustRegister(NewPoolMetricsCollector(
		NamedPool{Name: "runtime", Pool: &Pool{pool: pool}},
		NamedPool{Name: " runtime ", Pool: &Pool{pool: pool}},
	))
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	family := gatheredPoolFamily(t, families, "leapview_postgres_pool_total_connections")
	if got := len(family.Metric); got != 1 {
		t.Fatalf("total connection samples = %d, want one after duplicate-name collapse", got)
	}
}

func gatheredPoolFamily(t *testing.T, families []*io_prometheus_client.MetricFamily, name string) *io_prometheus_client.MetricFamily {
	t.Helper()
	for _, family := range families {
		if family.GetName() == name {
			return family
		}
	}
	t.Fatalf("metric family %q not found", name)
	return nil
}

func gatheredPoolMetric(t *testing.T, families []*io_prometheus_client.MetricFamily, name string) *io_prometheus_client.Metric {
	t.Helper()
	family := gatheredPoolFamily(t, families, name)
	if len(family.Metric) != 1 {
		t.Fatalf("metric family %q samples = %d, want one", name, len(family.Metric))
	}
	return family.Metric[0]
}
