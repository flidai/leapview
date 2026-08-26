package recovery

import (
	"context"
	"time"

	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/prometheus/client_golang/prometheus"
)

type MetricsCollector struct {
	repository Repository
	clock      refreshschedule.Clock

	due              *prometheus.Desc
	overdue          *prometheus.Desc
	running          *prometheus.Desc
	failed           *prometheus.Desc
	evidence         *prometheus.Desc
	leaseRecoveries  *prometheus.Desc
	lastSuccessAge   *prometheus.Desc
	restoreDuration  *prometheus.Desc
	readiness        *prometheus.Desc
	recoveryPointAge *prometheus.Desc
	scrapeError      *prometheus.Desc
}

func NewMetricsCollector(repository Repository, clock refreshschedule.Clock) *MetricsCollector {
	return &MetricsCollector{
		repository: repository, clock: clock,
		due:              prometheus.NewDesc("leapview_recovery_qualification_due", "Recovery qualification occurrences due for execution.", nil, nil),
		overdue:          prometheus.NewDesc("leapview_recovery_qualification_overdue", "Recovery qualification occurrences whose evidence is stale.", nil, nil),
		running:          prometheus.NewDesc("leapview_recovery_qualification_running", "Claimed or running recovery qualification occurrences.", nil, nil),
		failed:           prometheus.NewDesc("leapview_recovery_qualification_failed", "Failed or expired recovery qualification occurrences.", nil, nil),
		evidence:         prometheus.NewDesc("leapview_recovery_qualification_evidence", "Recovery qualification evidence publication state.", []string{"state"}, nil),
		leaseRecoveries:  prometheus.NewDesc("leapview_recovery_qualification_lease_recoveries", "Recovery qualification attempts retained as reclaimed after lease expiry.", nil, nil),
		lastSuccessAge:   prometheus.NewDesc("leapview_recovery_qualification_last_success_age_seconds", "Age of the latest successful qualification by operation.", []string{"operation"}, nil),
		restoreDuration:  prometheus.NewDesc("leapview_recovery_qualification_restore_duration_seconds", "Latest successful restore duration by operation.", []string{"operation"}, nil),
		readiness:        prometheus.NewDesc("leapview_recovery_qualification_readiness_duration_seconds", "Latest successful readiness duration by operation.", []string{"operation"}, nil),
		recoveryPointAge: prometheus.NewDesc("leapview_recovery_qualification_recovery_point_age_seconds", "Latest successful recovery-point age by operation.", []string{"operation"}, nil),
		scrapeError:      prometheus.NewDesc("leapview_recovery_qualification_scrape_error", "Whether the latest recovery qualification ledger scrape failed.", nil, nil),
	}
}

func (collector *MetricsCollector) Describe(output chan<- *prometheus.Desc) {
	for _, descriptor := range []*prometheus.Desc{
		collector.due, collector.overdue, collector.running, collector.failed,
		collector.evidence, collector.leaseRecoveries, collector.lastSuccessAge,
		collector.restoreDuration, collector.readiness, collector.recoveryPointAge,
		collector.scrapeError,
	} {
		output <- descriptor
	}
}

func (collector *MetricsCollector) Collect(output chan<- prometheus.Metric) {
	if collector == nil || collector.repository == nil {
		return
	}
	clock := collector.clock
	if clock == nil {
		clock = refreshschedule.RealClock{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	snapshot, err := collector.repository.Status(ctx, clock.Now())
	if err != nil {
		output <- prometheus.MustNewConstMetric(collector.scrapeError, prometheus.GaugeValue, 1)
		return
	}
	output <- prometheus.MustNewConstMetric(collector.due, prometheus.GaugeValue, float64(snapshot.Due))
	output <- prometheus.MustNewConstMetric(collector.overdue, prometheus.GaugeValue, float64(snapshot.Overdue))
	output <- prometheus.MustNewConstMetric(collector.running, prometheus.GaugeValue, float64(snapshot.Running))
	output <- prometheus.MustNewConstMetric(collector.failed, prometheus.GaugeValue, float64(snapshot.Failed))
	output <- prometheus.MustNewConstMetric(collector.evidence, prometheus.GaugeValue, float64(snapshot.EvidencePending), EvidencePending)
	output <- prometheus.MustNewConstMetric(collector.evidence, prometheus.GaugeValue, float64(snapshot.EvidenceFailed), EvidenceFailed)
	output <- prometheus.MustNewConstMetric(collector.leaseRecoveries, prometheus.GaugeValue, float64(snapshot.RecoveredExpiredLeases))
	for _, operation := range snapshot.Operations {
		if operation.LastSuccessAgeSeconds == nil {
			continue
		}
		output <- prometheus.MustNewConstMetric(collector.lastSuccessAge, prometheus.GaugeValue, float64(*operation.LastSuccessAgeSeconds), operation.Operation)
		output <- prometheus.MustNewConstMetric(collector.restoreDuration, prometheus.GaugeValue, float64(*operation.LastRestoreDurationMillis)/1000, operation.Operation)
		output <- prometheus.MustNewConstMetric(collector.readiness, prometheus.GaugeValue, float64(*operation.LastReadinessDurationMillis)/1000, operation.Operation)
		output <- prometheus.MustNewConstMetric(collector.recoveryPointAge, prometheus.GaugeValue, float64(*operation.LastRecoveryPointAgeSeconds), operation.Operation)
	}
	output <- prometheus.MustNewConstMetric(collector.scrapeError, prometheus.GaugeValue, 0)
}
