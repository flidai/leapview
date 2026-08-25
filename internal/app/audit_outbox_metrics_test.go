package app

import (
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_model/go"
)

func TestAuditOutboxCollectorExposesOnlyAggregateState(t *testing.T) {
	registry := prometheus.NewRegistry()
	registry.MustRegister(newAuditOutboxCollector(auditOutboxStatsStore{stats: access.AuditOutboxStats{
		Pending: 2, Retry: 3, Leased: 4, Delivered: 5, Poison: 6, Quarantined: 7,
		OldestUndeliveredAge: 8 * time.Second,
	}}))
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	intents := metricFamily(t, families, "leapview_audit_outbox_intents")
	if len(intents.Metric) != 6 {
		t.Fatalf("intent state metrics = %d, want 6", len(intents.Metric))
	}
	for _, metric := range intents.Metric {
		if len(metric.Label) != 1 || metric.Label[0].GetName() != "state" {
			t.Fatalf("unexpected audit metric labels: %#v", metric.Label)
		}
	}
	if got := metricFamily(t, families, "leapview_audit_outbox_oldest_undelivered_age_seconds").Metric[0].GetGauge().GetValue(); got != 8 {
		t.Fatalf("oldest age = %v, want 8", got)
	}
	if got := metricFamily(t, families, "leapview_audit_outbox_attempts").Metric[0].GetGauge().GetValue(); got != 0 {
		t.Fatalf("attempts = %v, want 0", got)
	}
	if got := metricFamily(t, families, "leapview_audit_outbox_leases").Metric[0].GetGauge().GetValue(); got != 4 {
		t.Fatalf("leases = %v, want 4", got)
	}
	if got := metricFamily(t, families, "leapview_audit_outbox_materialized").Metric[0].GetGauge().GetValue(); got != 5 {
		t.Fatalf("materialized = %v, want 5", got)
	}
	if got := metricFamily(t, families, "leapview_audit_outbox_capacity").Metric[0].GetGauge().GetValue(); got != 0 {
		t.Fatalf("capacity = %v, want 0 for unconfigured fake", got)
	}
	if got := metricFamily(t, families, "leapview_audit_outbox_scrape_error").Metric[0].GetGauge().GetValue(); got != 0 {
		t.Fatalf("scrape error = %v, want 0", got)
	}
}

func TestAuditOutboxCollectorReportsStoreFailureWithoutPayload(t *testing.T) {
	registry := prometheus.NewRegistry()
	registry.MustRegister(newAuditOutboxCollector(auditOutboxStatsStore{err: errors.New("private event payload")}))
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if got := metricFamily(t, families, "leapview_audit_outbox_scrape_error").Metric[0].GetGauge().GetValue(); got != 1 {
		t.Fatalf("scrape error = %v, want 1", got)
	}
}

func metricFamily(t *testing.T, families []*io_prometheus_client.MetricFamily, name string) *io_prometheus_client.MetricFamily {
	t.Helper()
	for _, family := range families {
		if family.GetName() == name {
			return family
		}
	}
	t.Fatalf("metric family %q not found", name)
	return nil
}
