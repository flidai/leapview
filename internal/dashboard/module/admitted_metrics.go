package module

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/workload"
)

type admittedMetrics struct {
	Metrics
	admitter workload.Admitter
}

func WithAdmission(metrics Metrics, admitter workload.Admitter) Metrics {
	if metrics == nil || admitter == nil {
		return nil
	}
	return admittedMetrics{Metrics: metrics, admitter: admitter}
}

// Planner preserves the activation-owned semantic planner through the
// workload admission decorator. Semantic APIs and authorization must observe
// the same compiled planner as the wrapped runtime.
func (m admittedMetrics) Planner(modelID string) (consumer.Planner, bool) {
	provider, ok := m.Metrics.(interface {
		Planner(string) (consumer.Planner, bool)
	})
	if !ok {
		return nil, false
	}
	planner, available := provider.Planner(modelID)
	return planner, available && planner != nil
}

func (m admittedMetrics) readContext(ctx context.Context) context.Context {
	return workload.WithAdmitter(ctx, m.admitter)
}

func (m admittedMetrics) MetricsForProject(projectID projectgraph.ResourceID) (Metrics, bool) {
	provider, ok := m.Metrics.(ProjectMetrics)
	if ok {
		metrics, found := provider.MetricsForProject(projectID)
		if !found || metrics == nil {
			return nil, found
		}
		return admittedMetrics{Metrics: metrics, admitter: m.admitter}, true
	}
	if m.Metrics == nil {
		return nil, false
	}
	if m.Metrics.Catalog().Project.ID != projectID {
		return nil, false
	}
	return m, true
}

func (m admittedMetrics) QueryDashboard(ctx context.Context, dashboardID string, filters dashboard.Filters) (dashboard.Patch, error) {
	return m.QueryDashboardPage(ctx, dashboardID, "", filters)
}

func (m admittedMetrics) QueryCompiledFilterOptions(ctx context.Context, dashboardID string, query dashboardfilter.OptionQuery) (dashboardfilter.OptionResult, error) {
	provider, ok := m.Metrics.(interface {
		QueryCompiledFilterOptions(context.Context, string, dashboardfilter.OptionQuery) (dashboardfilter.OptionResult, error)
	})
	if !ok {
		return dashboardfilter.OptionResult{}, errors.New("compiled filter options are not supported by this runtime")
	}
	return provider.QueryCompiledFilterOptions(m.readContext(ctx), dashboardID, query)
}

func (m admittedMetrics) QueryDashboardPage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	return m.Metrics.QueryDashboardPage(m.readContext(ctx), dashboardID, pageID, filters)
}

func (m admittedMetrics) QueryDashboardVisualizations(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	return m.Metrics.QueryDashboardVisualizations(m.readContext(ctx), dashboardID, pageID, filters)
}

func (m admittedMetrics) QueryVisualization(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, visualID string) (visualizationir.VisualizationEnvelope, error) {
	return m.Metrics.QueryVisualization(m.readContext(ctx), dashboardID, pageID, filters, visualID)
}

// QueryVisualizationForDefinition preserves the canonical compiled-definition
// execution seam through workload admission. Agent-authored visuals must use
// the same admission boundary as dashboard visual queries.
func (m admittedMetrics) QueryVisualizationForDefinition(ctx context.Context, definition dashboarddefinition.Definition, pageID string, filters dashboard.Filters, visualID string) (visualizationir.VisualizationEnvelope, error) {
	provider, ok := m.Metrics.(interface {
		QueryVisualizationForDefinition(context.Context, dashboarddefinition.Definition, string, dashboard.Filters, string) (visualizationir.VisualizationEnvelope, error)
	})
	if !ok {
		return visualizationir.VisualizationEnvelope{}, errors.New("compiled visualization execution is not supported by this runtime")
	}
	return provider.QueryVisualizationForDefinition(m.readContext(ctx), definition, pageID, filters, visualID)
}

// DefaultFiltersForDefinition forwards authored defaults through the
// admission decorator so canonical visual queries use the same initial state
// as dashboard pages.
func (m admittedMetrics) DefaultFiltersForDefinition(definition dashboarddefinition.Definition) dashboard.Filters {
	provider, ok := m.Metrics.(interface {
		DefaultFiltersForDefinition(dashboarddefinition.Definition) dashboard.Filters
	})
	if !ok {
		return dashboard.Filters{}.WithDefaults()
	}
	return provider.DefaultFiltersForDefinition(definition)
}

func (m admittedMetrics) QueryVisualizationWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	return m.Metrics.QueryVisualizationWindow(m.readContext(ctx), dashboardID, pageID, filters, request)
}

func (m admittedMetrics) QueryVisualizationTile(ctx context.Context, dashboardID, visualID, revision string, zoom, x, y int) (dashboardruntime.SpatialTileResult, error) {
	port, ok := m.Metrics.(visualizationTileMetrics)
	if !ok {
		return dashboardruntime.SpatialTileResult{}, errors.New("spatial tile metrics are not configured")
	}
	return port.QueryVisualizationTile(m.readContext(ctx), dashboardID, visualID, revision, zoom, x, y)
}

func (m admittedMetrics) ExpireVisualizationTileStream(streamID string) {
	if expirer, ok := m.Metrics.(interface{ ExpireVisualizationTileStream(string) }); ok {
		expirer.ExpireVisualizationTileStream(streamID)
	}
}

func (m admittedMetrics) QueryPublicVisualizationTile(ctx context.Context, publicID, dashboardID, visualID, revision string, zoom, x, y int) (dashboardruntime.SpatialTileResult, error) {
	port, ok := m.Metrics.(publicVisualizationTileMetrics)
	if !ok {
		return dashboardruntime.SpatialTileResult{}, errors.New("public spatial tile metrics are not configured")
	}
	return port.QueryPublicVisualizationTile(m.readContext(ctx), publicID, dashboardID, visualID, revision, zoom, x, y)
}

func (m admittedMetrics) ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
	ctx = m.readContext(ctx)
	request = request.WithMetadata(dataquery.MetadataFromContext(ctx))
	if err := request.ProjectID.Validate(); err != nil {
		return dataquery.Result{}, fmt.Errorf("project ID: %w", err)
	}
	if m.admitter == nil {
		return dataquery.Result{ExecutionState: dataquery.ExecutionRejected}, errors.New("workload admission is not configured")
	}
	class := workload.Interactive
	if request.Surface == dataquery.SurfaceAgent {
		class = workload.Background
	}
	operation := request.Operation
	if operation == "" {
		operation = string(request.Kind)
	}
	principalID := "system:query"
	if class == workload.Background {
		principalID = "system:dashboard-query"
	}
	lease, err := m.admitter.Acquire(ctx, workload.Request{Class: class, PrincipalID: principalID, Operation: operation, EstimatedMemoryBytes: 64 << 20})
	if err != nil {
		result := dataquery.Result{ExecutionState: executionStateForWorkloadError(ctx, err)}
		var rejection *workload.Rejection
		if errors.As(err, &rejection) {
			result.QueueWaitMS = rejection.QueueWait.Milliseconds()
		}
		return result, err
	}
	defer lease.Release()
	started := time.Now()
	result, err := m.Metrics.ExecuteDataQuery(lease.Context(), request)
	if result.QueueWaitMS == 0 {
		result.QueueWaitMS = lease.QueueWait().Milliseconds()
	}
	if result.ExecutionMS == 0 {
		result.ExecutionMS = elapsedMillis(time.Since(started))
	}
	if result.ExecutionState == "" {
		if err == nil {
			result.ExecutionState = dataquery.ExecutionSucceeded
		} else {
			result.ExecutionState = executionStateForWorkloadError(lease.Context(), err)
		}
	}
	return result, err
}

func (m admittedMetrics) ExecuteDataQueryArrow(ctx context.Context, request dataquery.Query, sink arrowquery.Sink) (dataquery.Result, error) {
	ctx = m.readContext(ctx)
	request = request.WithMetadata(dataquery.MetadataFromContext(ctx))
	if err := request.ProjectID.Validate(); err != nil {
		return dataquery.Result{}, fmt.Errorf("project ID: %w", err)
	}
	executor, ok := m.Metrics.(arrowquery.Executor)
	if !ok {
		return dataquery.Result{}, errors.New("query metrics do not support native Arrow execution")
	}
	if m.admitter == nil {
		return dataquery.Result{ExecutionState: dataquery.ExecutionRejected}, errors.New("workload admission is not configured")
	}
	class := workload.Interactive
	if request.Surface == dataquery.SurfaceAgent {
		class = workload.Background
	}
	operation := request.Operation
	if operation == "" {
		operation = string(request.Kind)
	}
	principalID := "system:query"
	if class == workload.Background {
		principalID = "system:dashboard-query"
	}
	lease, err := m.admitter.Acquire(ctx, workload.Request{Class: class, PrincipalID: principalID, Operation: operation, EstimatedMemoryBytes: 64 << 20})
	if err != nil {
		result := dataquery.Result{ExecutionState: executionStateForWorkloadError(ctx, err)}
		var rejection *workload.Rejection
		if errors.As(err, &rejection) {
			result.QueueWaitMS = rejection.QueueWait.Milliseconds()
		}
		return result, err
	}
	defer lease.Release()
	started := time.Now()
	result, err := executor.ExecuteDataQueryArrow(lease.Context(), request, sink)
	if result.QueueWaitMS == 0 {
		result.QueueWaitMS = lease.QueueWait().Milliseconds()
	}
	if result.ExecutionMS == 0 {
		result.ExecutionMS = elapsedMillis(time.Since(started))
	}
	if result.ExecutionState == "" {
		if err == nil {
			result.ExecutionState = dataquery.ExecutionSucceeded
		} else {
			result.ExecutionState = executionStateForWorkloadError(lease.Context(), err)
		}
	}
	return result, err
}

func (m admittedMetrics) QuerySemantic(ctx context.Context, modelID string, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	return m.Metrics.QuerySemantic(m.readContext(ctx), modelID, request)
}

func (m admittedMetrics) PreviewSemantic(ctx context.Context, modelID string, request reportdef.RowQuery) (reportdef.QueryRows, error) {
	return m.Metrics.PreviewSemantic(m.readContext(ctx), modelID, request)
}

func elapsedMillis(duration time.Duration) int64 {
	if duration <= 0 {
		return 0
	}
	if milliseconds := duration.Milliseconds(); milliseconds > 0 {
		return milliseconds
	}
	return 1
}

func executionStateForWorkloadError(ctx context.Context, err error) string {
	if err == context.DeadlineExceeded || ctx.Err() == context.DeadlineExceeded {
		return dataquery.ExecutionTimeout
	}
	if err == context.Canceled || ctx.Err() == context.Canceled {
		return dataquery.ExecutionCanceled
	}
	if reason, ok := workload.ReasonOf(err); ok && reason == workload.QueueTimeout {
		return dataquery.ExecutionTimeout
	}
	return dataquery.ExecutionRejected
}
