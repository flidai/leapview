package module

import (
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/pkg/arrowresult"
	"github.com/prometheus/client_golang/prometheus"
)

type Collector struct {
	database                                                   *analyticsducklake.Environment
	cache                                                      *resultcache.Pool
	connectionsOpen, connectionsActive, connectionsIdle        *prometheus.Desc
	cacheEntries, cacheBytes, cacheEvictions, cacheStores      *prometheus.Desc
	arrowResults, arrowLeases, arrowBytes, arrowTransientBytes *prometheus.Desc
	connectionAcquisitions, extensionInitializations           *prometheus.Desc
	sourceAcquisitions, scopeContention, commitRetries         *prometheus.Desc
	refreshCleanup, fatalHealth                                *prometheus.Desc
}

func NewCollector(database *analyticsducklake.Environment, cache *resultcache.Pool) *Collector {
	return &Collector{
		database:                 database,
		cache:                    cache,
		connectionsOpen:          prometheus.NewDesc("leapview_duckdb_connections_open", "Open connections to the process-owned DuckDB instance.", nil, nil),
		connectionsActive:        prometheus.NewDesc("leapview_duckdb_connections_active", "Connections currently in use in the process-owned DuckDB instance.", nil, nil),
		connectionsIdle:          prometheus.NewDesc("leapview_duckdb_connections_idle", "Idle connections in the process-owned DuckDB instance.", nil, nil),
		cacheEntries:             prometheus.NewDesc("leapview_query_cache_entries", "Retained governed query-result entries.", nil, nil),
		cacheBytes:               prometheus.NewDesc("leapview_query_cache_bytes", "Conservatively retained Arrow query-result bytes.", nil, nil),
		arrowResults:             prometheus.NewDesc("leapview_arrow_results", "Live owned Arrow analytical results.", nil, nil),
		arrowLeases:              prometheus.NewDesc("leapview_arrow_result_leases", "Active Arrow analytical result leases.", nil, nil),
		arrowBytes:               prometheus.NewDesc("leapview_arrow_result_bytes", "Conservatively retained bytes in live Arrow analytical results.", nil, nil),
		arrowTransientBytes:      prometheus.NewDesc("leapview_arrow_transient_bytes", "Transient bytes retained while materializing owned Arrow analytical results.", nil, nil),
		cacheEvictions:           prometheus.NewDesc("leapview_query_cache_evictions_total", "Query-result cache evictions by limiting constraint.", []string{"constraint"}, nil),
		cacheStores:              prometheus.NewDesc("leapview_query_cache_store_total", "Query-result cache store outcomes.", []string{"outcome"}, nil),
		connectionAcquisitions:   prometheus.NewDesc("leapview_duckdb_connection_acquisitions_total", "Admitted analytical connection acquisitions.", nil, nil),
		extensionInitializations: prometheus.NewDesc("leapview_duckdb_extension_initializations_total", "Approved extension initialization outcomes.", []string{"outcome"}, nil),
		sourceAcquisitions:       prometheus.NewDesc("leapview_duckdb_source_acquisitions_total", "Refresh-only source acquisition outcomes.", []string{"connector", "outcome"}, nil),
		scopeContention:          prometheus.NewDesc("leapview_duckdb_secret_scope_contention_total", "Refresh acquisition waits caused by identical credential scope.", []string{"connector"}, nil),
		commitRetries:            prometheus.NewDesc("leapview_ducklake_commit_retries_total", "Transient DuckLake commit retries.", nil, nil),
		refreshCleanup:           prometheus.NewDesc("leapview_duckdb_refresh_cleanup_total", "Refresh-session cleanup outcomes.", []string{"outcome"}, nil),
		fatalHealth:              prometheus.NewDesc("leapview_duckdb_fatal_health", "Whether analytical cleanup safety is fatally unhealthy.", nil, nil),
	}
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	for _, description := range []*prometheus.Desc{c.connectionsOpen, c.connectionsActive, c.connectionsIdle, c.cacheEntries, c.cacheBytes, c.cacheEvictions, c.cacheStores, c.arrowResults, c.arrowLeases, c.arrowBytes, c.arrowTransientBytes, c.connectionAcquisitions, c.extensionInitializations, c.sourceAcquisitions, c.scopeContention, c.commitRetries, c.refreshCleanup, c.fatalHealth} {
		ch <- description
	}
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	arrowStats := arrowresult.Stats()
	ch <- prometheus.MustNewConstMetric(c.arrowResults, prometheus.GaugeValue, float64(arrowStats.Results))
	ch <- prometheus.MustNewConstMetric(c.arrowLeases, prometheus.GaugeValue, float64(arrowStats.Leases))
	ch <- prometheus.MustNewConstMetric(c.arrowBytes, prometheus.GaugeValue, float64(arrowStats.Bytes))
	ch <- prometheus.MustNewConstMetric(c.arrowTransientBytes, prometheus.GaugeValue, float64(arrowStats.TransientBytes))
	openConnections, activeConnections, idleConnections := 0, 0, 0
	if c.database != nil {
		stats := c.database.ConnectionStats()
		openConnections, activeConnections, idleConnections = stats.OpenConnections, stats.InUse, stats.Idle
	}
	// Production sealed serving intentionally has no process-owned DuckDB
	// environment. Keep the gauge family present with zero values so metrics
	// consumers can distinguish that valid topology from missing telemetry.
	ch <- prometheus.MustNewConstMetric(c.connectionsOpen, prometheus.GaugeValue, float64(openConnections))
	ch <- prometheus.MustNewConstMetric(c.connectionsActive, prometheus.GaugeValue, float64(activeConnections))
	ch <- prometheus.MustNewConstMetric(c.connectionsIdle, prometheus.GaugeValue, float64(idleConnections))
	if c.database != nil {
		analytical := c.database.AnalyticalStats()
		ch <- prometheus.MustNewConstMetric(c.connectionAcquisitions, prometheus.CounterValue, float64(analytical.ConnectionAcquisitions))
		ch <- prometheus.MustNewConstMetric(c.extensionInitializations, prometheus.CounterValue, float64(analytical.ExtensionSuccess), "succeeded")
		ch <- prometheus.MustNewConstMetric(c.extensionInitializations, prometheus.CounterValue, float64(analytical.ExtensionFailures), "failed")
		for connector, outcomes := range analytical.SourceTotals {
			for _, outcome := range []string{"succeeded", "failed"} {
				ch <- prometheus.MustNewConstMetric(c.sourceAcquisitions, prometheus.CounterValue, float64(outcomes[outcome]), connector, outcome)
			}
		}
		for connector, count := range analytical.ScopeContention {
			ch <- prometheus.MustNewConstMetric(c.scopeContention, prometheus.CounterValue, float64(count), connector)
		}
		ch <- prometheus.MustNewConstMetric(c.commitRetries, prometheus.CounterValue, float64(analytical.CommitRetries))
		ch <- prometheus.MustNewConstMetric(c.refreshCleanup, prometheus.CounterValue, float64(analytical.CleanupSuccess), "succeeded")
		ch <- prometheus.MustNewConstMetric(c.refreshCleanup, prometheus.CounterValue, float64(analytical.CleanupFailures), "failed")
		fatal := 0.0
		if analytical.Fatal {
			fatal = 1
		}
		ch <- prometheus.MustNewConstMetric(c.fatalHealth, prometheus.GaugeValue, fatal)
	}
	if c.cache != nil {
		stats := c.cache.Stats()
		ch <- prometheus.MustNewConstMetric(c.cacheEntries, prometheus.GaugeValue, float64(stats.Entries))
		ch <- prometheus.MustNewConstMetric(c.cacheBytes, prometheus.GaugeValue, float64(stats.Bytes))
		for _, constraint := range []resultcache.Constraint{resultcache.ConstraintPartition, resultcache.ConstraintNode} {
			ch <- prometheus.MustNewConstMetric(c.cacheEvictions, prometheus.CounterValue, float64(stats.Evictions[constraint]), string(constraint))
		}
		for _, outcome := range []resultcache.StoreOutcome{resultcache.StoreStored, resultcache.StoreOversized, resultcache.StoreStale, resultcache.StoreClosed} {
			ch <- prometheus.MustNewConstMetric(c.cacheStores, prometheus.CounterValue, float64(stats.Stores[outcome]), string(outcome))
		}
	}
}
