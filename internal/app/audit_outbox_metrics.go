package app

import (
	"context"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/prometheus/client_golang/prometheus"
)

// auditOutboxCollector performs one bounded aggregate query per scrape. It
// deliberately exposes no event, actor, resource, or metadata labels.
type auditOutboxCollector struct {
	store access.AuditOutboxStore

	intents      *prometheus.Desc
	oldest       *prometheus.Desc
	attempts     *prometheus.Desc
	leases       *prometheus.Desc
	materialized *prometheus.Desc
	capacity     *prometheus.Desc
	remaining    *prometheus.Desc
	scrapeError  *prometheus.Desc
}

func newAuditOutboxCollector(store access.AuditOutboxStore) prometheus.Collector {
	return &auditOutboxCollector{
		store: store,
		intents: prometheus.NewDesc(
			"leapview_audit_outbox_intents",
			"Audit outbox intents by durable state.",
			[]string{"state"}, nil,
		),
		oldest: prometheus.NewDesc(
			"leapview_audit_outbox_oldest_undelivered_age_seconds",
			"Age in seconds of the oldest non-delivered audit intent.",
			nil, nil,
		),
		attempts: prometheus.NewDesc(
			"leapview_audit_outbox_attempts",
			"Aggregate persisted delivery attempts across audit intents.",
			nil, nil,
		),
		leases: prometheus.NewDesc(
			"leapview_audit_outbox_leases",
			"Number of currently leased audit intents.",
			nil, nil,
		),
		materialized: prometheus.NewDesc(
			"leapview_audit_outbox_materialized",
			"Number of audit intents materialized into the final audit ledger.",
			nil, nil,
		),
		capacity: prometheus.NewDesc(
			"leapview_audit_outbox_capacity",
			"Maximum undelivered audit intents accepted by the local outbox.",
			nil, nil,
		),
		remaining: prometheus.NewDesc(
			"leapview_audit_outbox_capacity_remaining",
			"Remaining undelivered audit-intent capacity.",
			nil, nil,
		),
		scrapeError: prometheus.NewDesc(
			"leapview_audit_outbox_scrape_error",
			"Whether the most recent audit outbox aggregate scrape failed.",
			nil, nil,
		),
	}
}

func (collector *auditOutboxCollector) Describe(output chan<- *prometheus.Desc) {
	output <- collector.intents
	output <- collector.oldest
	output <- collector.attempts
	output <- collector.leases
	output <- collector.materialized
	output <- collector.capacity
	output <- collector.remaining
	output <- collector.scrapeError
}

func (collector *auditOutboxCollector) Collect(output chan<- prometheus.Metric) {
	if collector == nil {
		return
	}
	if collector.store == nil {
		output <- prometheus.MustNewConstMetric(collector.scrapeError, prometheus.GaugeValue, 1)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stats, err := collector.store.AuditOutboxStats(ctx, time.Now().UTC())
	if err != nil {
		output <- prometheus.MustNewConstMetric(collector.scrapeError, prometheus.GaugeValue, 1)
		return
	}
	for state, count := range map[string]int64{
		"pending": stats.Pending, "retry": stats.Retry, "leased": stats.Leased,
		"delivered": stats.Delivered, "poison": stats.Poison, "quarantined": stats.Quarantined,
	} {
		output <- prometheus.MustNewConstMetric(collector.intents, prometheus.GaugeValue, float64(count), state)
	}
	output <- prometheus.MustNewConstMetric(collector.oldest, prometheus.GaugeValue, stats.OldestUndeliveredAge.Seconds())
	output <- prometheus.MustNewConstMetric(collector.attempts, prometheus.GaugeValue, float64(stats.AttemptCount))
	output <- prometheus.MustNewConstMetric(collector.leases, prometheus.GaugeValue, float64(stats.Leased))
	output <- prometheus.MustNewConstMetric(collector.materialized, prometheus.GaugeValue, float64(stats.Delivered))
	output <- prometheus.MustNewConstMetric(collector.capacity, prometheus.GaugeValue, float64(stats.Capacity))
	output <- prometheus.MustNewConstMetric(collector.remaining, prometheus.GaugeValue, float64(stats.CapacityRemaining))
	output <- prometheus.MustNewConstMetric(collector.scrapeError, prometheus.GaugeValue, 0)
}
