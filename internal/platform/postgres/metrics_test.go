package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
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

func TestPoolMetricsCollectorObservesStarvationAndCanceledAcquireWithRealPostgres(t *testing.T) {
	harness := postgrestest.Start(t)
	config, err := pgxpool.ParseConfig(harness.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	config.MinConns = 1
	config.MaxConns = 1
	puddlePool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatalf("open one-connection PostgreSQL pool: %v", err)
	}
	defer puddlePool.Close()

	pool := &Pool{pool: puddlePool, acquireTimeout: time.Second}
	first, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatalf("acquire first PostgreSQL connection: %v", err)
	}
	tx, err := first.Begin(t.Context())
	if err != nil {
		first.Release()
		t.Fatalf("begin long PostgreSQL transaction: %v", err)
	}
	if _, err := tx.Exec(t.Context(), "SELECT 1"); err != nil {
		_ = tx.Rollback(t.Context())
		first.Release()
		t.Fatalf("execute in long PostgreSQL transaction: %v", err)
	}

	type acquireResult struct {
		conn    *pgxpool.Conn
		err     error
		elapsed time.Duration
	}
	started := time.Now()
	waiting := make(chan acquireResult, 1)
	acquireCtx, cancelAcquire := context.WithTimeout(t.Context(), time.Second)
	defer cancelAcquire()
	go func() {
		second, acquireErr := pool.Acquire(acquireCtx)
		result := acquireResult{conn: second, err: acquireErr, elapsed: time.Since(started)}
		if second != nil {
			second.Release()
		}
		waiting <- result
	}()

	select {
	case result := <-waiting:
		if result.err == nil {
			t.Fatalf("starving acquire completed before the long transaction released: wait=%s", result.elapsed)
		}
		t.Fatalf("starving acquire failed before the long transaction released: %v", result.err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := tx.Rollback(t.Context()); err != nil {
		first.Release()
		t.Fatalf("rollback long PostgreSQL transaction: %v", err)
	}
	first.Release()
	result := <-waiting
	if result.err != nil {
		t.Fatalf("starving acquire after transaction release: %v", result.err)
	}
	if result.elapsed < 75*time.Millisecond {
		t.Fatalf("starving acquire waited %s, want evidence of held pool slot", result.elapsed)
	}

	// Hold the sole slot while a caller's bounded context expires. This is the
	// failed-acquisition path used by the canceled-acquire alert.
	first, err = pool.Acquire(t.Context())
	if err != nil {
		t.Fatalf("reacquire first PostgreSQL connection: %v", err)
	}
	canceledCtx, cancelCanceled := context.WithTimeout(t.Context(), 50*time.Millisecond)
	_, canceledErr := pool.Acquire(canceledCtx)
	cancelCanceled()
	first.Release()
	if canceledErr == nil {
		t.Fatal("canceled PostgreSQL acquire unexpectedly succeeded")
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(NewPoolMetricsCollector(NamedPool{Name: ControlRuntimePoolName, Pool: pool}))
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather PostgreSQL pool metrics: %v", err)
	}
	if got := gatheredPoolMetric(t, families, "leapview_postgres_pool_max_connections").GetGauge().GetValue(); got != 1 {
		t.Fatalf("max connections = %v, want one", got)
	}
	if got := gatheredPoolMetric(t, families, "leapview_postgres_pool_acquired_connections").GetGauge().GetValue(); got != 0 {
		t.Fatalf("acquired connections = %v, want zero after release", got)
	}
	if got := gatheredPoolMetric(t, families, "leapview_postgres_pool_empty_acquire_count_total").GetCounter().GetValue(); got < 1 {
		t.Fatalf("empty acquire count = %v, want at least one successful waited acquire", got)
	}
	if got := gatheredPoolMetric(t, families, "leapview_postgres_pool_canceled_acquire_count_total").GetCounter().GetValue(); got < 1 {
		t.Fatalf("canceled acquire count = %v, want at least one canceled acquire", got)
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
