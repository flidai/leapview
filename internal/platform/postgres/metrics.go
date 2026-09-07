package postgres

import (
	"sort"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

// NamedPool identifies one application-owned PostgreSQL pool for telemetry.
// Names are deliberately supplied by composition from a fixed, low-cardinality
// set (for example, control_runtime or ducklake_maintenance), never from a
// request, tenant, or database identity.
type NamedPool struct {
	Name string
	Pool *Pool
}

// Canonical serving-pool names used by production composition. Keeping these
// values centralized prevents accidental metric-label cardinality drift.
const (
	ControlRuntimePoolName      = "control_runtime"
	ControlMaintenancePoolName  = "control_maintenance"
	ControlReadonlyPoolName     = "control_readonly"
	DuckLakeRuntimePoolName     = "ducklake_runtime"
	DuckLakeMaintenancePoolName = "ducklake_maintenance"
)

// PoolMetricsCollector exports bounded pgxpool statistics for each named
// application pool. PostgreSQL server internals (pg_stat_*) remain owned by an
// external postgres_exporter or managed service and are intentionally not
// queried here.
type PoolMetricsCollector struct {
	pools []NamedPool

	maxConns          *prometheus.Desc
	totalConns        *prometheus.Desc
	acquiredConns     *prometheus.Desc
	idleConns         *prometheus.Desc
	constructingConns *prometheus.Desc
	acquireCount      *prometheus.Desc
	acquireDuration   *prometheus.Desc
	emptyAcquire      *prometheus.Desc
	canceledAcquire   *prometheus.Desc
}

// NewPoolMetricsCollector constructs a collector for the supplied pools. Empty
// names and nil pools are ignored; duplicate names are collapsed to the first
// occurrence so one collector cannot emit duplicate Prometheus samples.
func NewPoolMetricsCollector(pools ...NamedPool) *PoolMetricsCollector {
	byName := make(map[string]*Pool, len(pools))
	for _, named := range pools {
		name := strings.TrimSpace(named.Name)
		if name == "" || named.Pool == nil {
			continue
		}
		if _, exists := byName[name]; !exists {
			byName[name] = named.Pool
		}
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	normalized := make([]NamedPool, 0, len(names))
	for _, name := range names {
		normalized = append(normalized, NamedPool{Name: name, Pool: byName[name]})
	}
	return &PoolMetricsCollector{
		pools: normalized,
		maxConns: prometheus.NewDesc(
			"leapview_postgres_pool_max_connections",
			"Maximum configured connections in the application PostgreSQL pool.",
			[]string{"pool"}, nil,
		),
		totalConns: prometheus.NewDesc(
			"leapview_postgres_pool_total_connections",
			"Current total connections in the application PostgreSQL pool.",
			[]string{"pool"}, nil,
		),
		acquiredConns: prometheus.NewDesc(
			"leapview_postgres_pool_acquired_connections",
			"Current acquired connections in the application PostgreSQL pool.",
			[]string{"pool"}, nil,
		),
		idleConns: prometheus.NewDesc(
			"leapview_postgres_pool_idle_connections",
			"Current idle connections in the application PostgreSQL pool.",
			[]string{"pool"}, nil,
		),
		constructingConns: prometheus.NewDesc(
			"leapview_postgres_pool_constructing_connections",
			"Current connections being constructed in the application PostgreSQL pool.",
			[]string{"pool"}, nil,
		),
		acquireCount: prometheus.NewDesc(
			"leapview_postgres_pool_acquire_count_total",
			"Cumulative successful connection acquisitions from the application PostgreSQL pool.",
			[]string{"pool"}, nil,
		),
		acquireDuration: prometheus.NewDesc(
			"leapview_postgres_pool_acquire_duration_seconds_total",
			"Cumulative time spent on successful connection acquisitions from the application PostgreSQL pool.",
			[]string{"pool"}, nil,
		),
		emptyAcquire: prometheus.NewDesc(
			"leapview_postgres_pool_empty_acquire_count_total",
			"Cumulative successful acquisitions that waited for an empty application PostgreSQL pool.",
			[]string{"pool"}, nil,
		),
		canceledAcquire: prometheus.NewDesc(
			"leapview_postgres_pool_canceled_acquire_count_total",
			"Cumulative canceled connection acquisitions from the application PostgreSQL pool.",
			[]string{"pool"}, nil,
		),
	}
}

func (c *PoolMetricsCollector) Describe(output chan<- *prometheus.Desc) {
	if c == nil {
		return
	}
	for _, descriptor := range []*prometheus.Desc{
		c.maxConns,
		c.totalConns,
		c.acquiredConns,
		c.idleConns,
		c.constructingConns,
		c.acquireCount,
		c.acquireDuration,
		c.emptyAcquire,
		c.canceledAcquire,
	} {
		output <- descriptor
	}
}

func (c *PoolMetricsCollector) Collect(output chan<- prometheus.Metric) {
	if c == nil {
		return
	}
	for _, named := range c.pools {
		stats := named.Pool.Stats()
		if stats == nil {
			continue
		}
		label := named.Name
		output <- prometheus.MustNewConstMetric(c.maxConns, prometheus.GaugeValue, float64(stats.MaxConns()), label)
		output <- prometheus.MustNewConstMetric(c.totalConns, prometheus.GaugeValue, float64(stats.TotalConns()), label)
		output <- prometheus.MustNewConstMetric(c.acquiredConns, prometheus.GaugeValue, float64(stats.AcquiredConns()), label)
		output <- prometheus.MustNewConstMetric(c.idleConns, prometheus.GaugeValue, float64(stats.IdleConns()), label)
		output <- prometheus.MustNewConstMetric(c.constructingConns, prometheus.GaugeValue, float64(stats.ConstructingConns()), label)
		output <- prometheus.MustNewConstMetric(c.acquireCount, prometheus.CounterValue, float64(stats.AcquireCount()), label)
		output <- prometheus.MustNewConstMetric(c.acquireDuration, prometheus.CounterValue, stats.AcquireDuration().Seconds(), label)
		output <- prometheus.MustNewConstMetric(c.emptyAcquire, prometheus.CounterValue, float64(stats.EmptyAcquireCount()), label)
		output <- prometheus.MustNewConstMetric(c.canceledAcquire, prometheus.CounterValue, float64(stats.CanceledAcquireCount()), label)
	}
}
