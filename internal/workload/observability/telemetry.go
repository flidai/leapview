package observability

import (
	"sync"

	"github.com/flidai/leapview/internal/workload"
	"github.com/prometheus/client_golang/prometheus"
)

type Observer struct {
	running    *prometheus.GaugeVec
	queued     *prometheus.GaugeVec
	memory     *prometheus.GaugeVec
	borrowed   *prometheus.GaugeVec
	admissions *prometheus.CounterVec
	queueWait  *prometheus.HistogramVec
	execution  *prometheus.HistogramVec
	mu         sync.Mutex
}

func New(registerer prometheus.Registerer) *Observer {
	observer := &Observer{
		running:    prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "leapview_workload_running", Help: "Currently running workload operations."}, []string{"class"}),
		queued:     prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "leapview_workload_queued", Help: "Currently queued workload operations."}, []string{"class"}),
		memory:     prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "leapview_workload_memory_bytes", Help: "Currently reserved workload memory."}, []string{"class"}),
		borrowed:   prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "leapview_workload_borrowed", Help: "Capacity currently borrowed above each class reservation."}, []string{"class"}),
		admissions: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "leapview_workload_admissions_total", Help: "Workload admission outcomes."}, []string{"class", "outcome", "reason"}),
		queueWait:  prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "leapview_workload_queue_wait_seconds", Help: "Time spent waiting for workload admission.", Buckets: prometheus.ExponentialBuckets(0.001, 2, 17)}, []string{"class"}),
		execution:  prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "leapview_workload_execution_duration_seconds", Help: "Admitted workload execution duration.", Buckets: prometheus.ExponentialBuckets(0.005, 2, 18)}, []string{"class"}),
	}
	if registerer != nil {
		registerer.MustRegister(observer.running, observer.queued, observer.memory, observer.borrowed, observer.admissions, observer.queueWait, observer.execution)
	}
	return observer
}

func (o *Observer) ObserveWorkload(stats workload.Stats) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for class, classStats := range stats.Classes {
		className := string(class)
		o.running.WithLabelValues(className).Set(float64(classStats.Running))
		o.queued.WithLabelValues(className).Set(float64(classStats.Queued))
		o.memory.WithLabelValues(className).Set(float64(classStats.MemoryBytes))
		o.borrowed.WithLabelValues(className).Set(float64(classStats.Borrowed))
	}
}

func (o *Observer) ObserveAdmission(event workload.AdmissionEvent) {
	if o == nil {
		return
	}
	outcome := event.Outcome
	switch outcome {
	case "admitted", "rejected", "completed", "timeout", "canceled":
	default:
		outcome = "other"
	}
	reason := string(event.Reason)
	if reason == "" {
		reason = "none"
	}
	class := string(event.Class)
	o.admissions.WithLabelValues(class, outcome, reason).Inc()
	if outcome == "admitted" || outcome == "rejected" {
		o.queueWait.WithLabelValues(class).Observe(event.QueueWait.Seconds())
	}
	if outcome == "completed" || outcome == "timeout" || outcome == "canceled" {
		o.execution.WithLabelValues(class).Observe(event.Execution.Seconds())
	}
}
