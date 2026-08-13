package observability

import (
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

type Telemetry struct {
	refreshDuration      *prometheus.HistogramVec
	stageDuration        *prometheus.HistogramVec
	refreshInFlight      *prometheus.GaugeVec
	refreshCancellations *prometheus.CounterVec
	cacheOutcomes        *prometheus.CounterVec
	targetOutcomes       *prometheus.CounterVec
	frameRows            *prometheus.HistogramVec
	frameBytes           *prometheus.HistogramVec
	cardinality          *prometheus.HistogramVec
	tileRequests         *prometheus.CounterVec
	tileCacheOutcomes    *prometheus.CounterVec
	tileDuration         *prometheus.HistogramVec
	tileBytes            *prometheus.HistogramVec
	tileFeatures         *prometheus.HistogramVec
	tileFallbacks        *prometheus.CounterVec
	publicDocuments      *prometheus.CounterVec
	publicStreams        *prometheus.GaugeVec
	publicCommands       *prometheus.CounterVec
	publicRateLimits     *prometheus.CounterVec
}

func New(registerer prometheus.Registerer) *Telemetry {
	telemetry := &Telemetry{
		refreshDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "leapview_dashboard_refresh_duration_seconds",
			Help:    "End-to-end dashboard refresh duration in seconds.",
			Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		}, []string{"command", "outcome"}),
		stageDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "leapview_dashboard_refresh_stage_duration_seconds",
			Help:    "Dashboard refresh stage duration in seconds.",
			Buckets: []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		}, []string{"stage", "outcome"}),
		refreshInFlight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "leapview_dashboard_refreshes_in_flight",
			Help: "Dashboard refreshes currently in flight.",
		}, []string{"command"}),
		refreshCancellations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "leapview_dashboard_refresh_cancellations_total",
			Help: "Total dashboard refresh cancellations.",
		}, []string{"command"}),
		cacheOutcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "leapview_dashboard_cache_outcomes_total",
			Help: "Dashboard query cache outcomes.",
		}, []string{"outcome"}),
		targetOutcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "leapview_dashboard_target_outcomes_total",
			Help: "Dashboard refresh target outcomes.",
		}, []string{"kind", "outcome"}),
		frameRows: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "leapview_visualization_frame_rows",
			Help: "Rows delivered in visualization data frames.",
		}, []string{"kind"}),
		frameBytes: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "leapview_visualization_frame_size_bytes",
			Help: "Serialized visualization envelope size in bytes.",
		}, []string{"kind"}),
		cardinality: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "leapview_visualization_cardinality",
			Help: "Reported visualization result cardinality.",
		}, []string{"kind"}),
		tileRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "leapview_spatial_tile_requests_total", Help: "Spatial tile request outcomes.",
		}, []string{"outcome"}),
		tileCacheOutcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "leapview_spatial_tile_cache_outcomes_total", Help: "Spatial child-tile byte cache outcomes.",
		}, []string{"outcome"}),
		tileDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "leapview_spatial_tile_stage_duration_seconds", Help: "Spatial tile query and encoding duration.",
			Buckets: []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		}, []string{"stage", "precision"}),
		tileBytes: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "leapview_spatial_tile_size_bytes", Help: "Uncompressed MVT bytes by precision.",
			Buckets: []float64{0, 1024, 4096, 16384, 65536, 131072, 262144, 524288},
		}, []string{"precision"}),
		tileFeatures: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "leapview_spatial_tile_features", Help: "Encoded feature count by precision.",
			Buckets: []float64{0, 1, 10, 50, 100, 250, 500, 1000, 2500, 5000},
		}, []string{"precision"}),
		tileFallbacks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "leapview_spatial_tile_raw_fallbacks_total", Help: "Tiles served at aggregate precision because the raw revision-wide budget did not fit.",
		}, []string{"precision"}),
		publicDocuments: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "leapview_public_dashboard_documents_total",
			Help: "Public dashboard document load outcomes.",
		}, []string{"presentation", "outcome"}),
		publicStreams: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "leapview_public_dashboard_streams_active",
			Help: "Active anonymous dashboard streams.",
		}, []string{"presentation"}),
		publicCommands: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "leapview_public_dashboard_commands_total",
			Help: "Anonymous dashboard command attempts.",
		}, []string{"command", "outcome"}),
		publicRateLimits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "leapview_public_dashboard_rate_limit_rejections_total",
			Help: "Anonymous dashboard requests rejected by public traffic family.",
		}, []string{"family"}),
	}
	if registerer != nil {
		registerer.MustRegister(
			telemetry.refreshDuration,
			telemetry.stageDuration,
			telemetry.refreshInFlight,
			telemetry.refreshCancellations,
			telemetry.cacheOutcomes,
			telemetry.targetOutcomes,
			telemetry.frameRows,
			telemetry.frameBytes,
			telemetry.cardinality,
			telemetry.tileRequests,
			telemetry.tileCacheOutcomes,
			telemetry.tileDuration,
			telemetry.tileBytes,
			telemetry.tileFeatures,
			telemetry.tileFallbacks,
			telemetry.publicDocuments,
			telemetry.publicStreams,
			telemetry.publicCommands,
			telemetry.publicRateLimits,
		)
	}
	return telemetry
}

func (t *Telemetry) SpatialTileObserved(outcome, cache, precision string, queryMS, encodingMS int64, encodedBytes, features int, fallback bool) {
	if t == nil {
		return
	}
	if outcome != "success" {
		outcome = "error"
	}
	precision = spatialPrecisionLabel(precision)
	t.tileRequests.WithLabelValues(outcome).Inc()
	if outcome == "success" {
		t.tileCacheOutcomes.WithLabelValues(cacheLabel(cache)).Inc()
		t.tileDuration.WithLabelValues("query", precision).Observe(float64(max(queryMS, 0)) / 1000)
		t.tileDuration.WithLabelValues("encoding", precision).Observe(float64(max(encodingMS, 0)) / 1000)
		t.tileBytes.WithLabelValues(precision).Observe(float64(max(encodedBytes, 0)))
		t.tileFeatures.WithLabelValues(precision).Observe(float64(max(features, 0)))
		if fallback {
			t.tileFallbacks.WithLabelValues(precision).Inc()
		}
	}
}

func (t *Telemetry) DashboardRefreshStarted(command string) {
	if t != nil {
		t.refreshInFlight.WithLabelValues(commandLabel(command)).Inc()
	}
}

func (t *Telemetry) DashboardRefreshFinished(commandValue, outcomeValue string, cancellationCount int, stageTimings map[string]float64) {
	if t == nil {
		return
	}
	command := commandLabel(commandValue)
	outcome := outcomeLabel(outcomeValue)
	t.refreshInFlight.WithLabelValues(command).Dec()
	if cancellationCount > 0 {
		t.refreshCancellations.WithLabelValues(command).Add(float64(cancellationCount))
	}
	for stage, milliseconds := range stageTimings {
		if milliseconds < 0 {
			continue
		}
		stage = stageLabel(stage)
		t.stageDuration.WithLabelValues(stage, outcome).Observe(milliseconds / 1000)
		if stage == "end_to_end" {
			t.refreshDuration.WithLabelValues(command, outcome).Observe(milliseconds / 1000)
		}
	}
}

func (t *Telemetry) DashboardCacheObserved(outcome string) {
	if t != nil {
		t.cacheOutcomes.WithLabelValues(cacheLabel(outcome)).Inc()
	}
}

func (t *Telemetry) DashboardTargetObserved(kind, outcome string) {
	if t != nil {
		t.targetOutcomes.WithLabelValues(targetKindLabel(kind), targetOutcomeLabel(outcome)).Inc()
	}
}

func (t *Telemetry) DashboardRefreshEventObserved(eventType, target string) {
	if t == nil {
		return
	}
	switch eventType {
	case "filter_options":
		t.DashboardTargetObserved("filter_options", "success")
	case "visual", "table":
		t.DashboardTargetObserved("visual", "success")
	case "table_count_error":
		t.DashboardTargetObserved("visual_count", "error")
	case "target_error":
		kind := target
		if prefix, _, ok := strings.Cut(kind, ":"); ok {
			kind = prefix
		}
		t.DashboardTargetObserved(kind, "error")
	}
}

func (t *Telemetry) VisualizationFrameObserved(kind string, rows, cardinality, encodedBytes int) {
	if t == nil {
		return
	}
	t.frameRows.WithLabelValues(kind).Observe(float64(max(rows, 0)))
	t.cardinality.WithLabelValues(kind).Observe(float64(max(cardinality, 0)))
	t.frameBytes.WithLabelValues(kind).Observe(float64(max(encodedBytes, 0)))
}

func (t *Telemetry) PublicDocumentObserved(presentation, outcome string) {
	if t == nil {
		return
	}
	if presentation != "embed" {
		presentation = "public"
	}
	if outcome != "success" {
		outcome = "not_found"
	}
	t.publicDocuments.WithLabelValues(presentation, outcome).Inc()
}

func (t *Telemetry) PublicStreamStarted(presentation string) func() {
	if t == nil {
		return func() {}
	}
	if presentation != "embed" {
		presentation = "public"
	}
	t.publicStreams.WithLabelValues(presentation).Inc()
	return func() { t.publicStreams.WithLabelValues(presentation).Dec() }
}

func (t *Telemetry) PublicCommandObserved(command, outcome string) {
	if t == nil {
		return
	}
	command = commandLabel(command)
	if outcome != "accepted" {
		outcome = "rejected"
	}
	t.publicCommands.WithLabelValues(command, outcome).Inc()
}

func (t *Telemetry) PublicRateLimitObserved(family string) {
	if t == nil {
		return
	}
	switch family {
	case "page", "command", "stream":
	default:
		family = "unknown"
	}
	t.publicRateLimits.WithLabelValues(family).Inc()
}

func commandLabel(value string) string {
	switch normalizedLabel(value) {
	case "initial", "filter_change", "navigate", "select", "clear_selection", "visual_window", "refresh_materializations":
		return normalizedLabel(value)
	default:
		return "other"
	}
}

func outcomeLabel(value string) string {
	switch normalizedLabel(value) {
	case "complete", "partial", "error", "canceled":
		return normalizedLabel(value)
	default:
		return "other"
	}
}

func stageLabel(value string) string {
	switch normalizedLabel(value) {
	case "end_to_end", "target_work_sum", "target_critical_path", "admission_wait", "connection_wait", "planning", "database", "execution":
		return normalizedLabel(value)
	default:
		return "other"
	}
}

func cacheLabel(value string) string {
	switch normalizedLabel(value) {
	case "hit", "miss", "coalesced", "disabled", "error":
		return normalizedLabel(value)
	default:
		return "other"
	}
}

func targetKindLabel(value string) string {
	switch normalizedLabel(value) {
	case "filter_options", "visual", "visual_count", "refresh":
		return normalizedLabel(value)
	default:
		return "other"
	}
}

func targetOutcomeLabel(value string) string {
	switch normalizedLabel(value) {
	case "success", "error", "canceled":
		return normalizedLabel(value)
	default:
		return "other"
	}
}

func spatialPrecisionLabel(value string) string {
	switch normalizedLabel(value) {
	case "raw", "aggregated":
		return normalizedLabel(value)
	default:
		return "unknown"
	}
}

func normalizedLabel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")
	switch value {
	case "endtoend":
		return "end_to_end"
	case "admissionwait":
		return "admission_wait"
	case "connectionwait":
		return "connection_wait"
	case "targetworksum":
		return "target_work_sum"
	case "targetcriticalpath":
		return "target_critical_path"
	default:
		return value
	}
}
