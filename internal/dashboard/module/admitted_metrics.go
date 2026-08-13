package module

import (
	"context"
	"errors"
	"time"

	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	"github.com/flidai/leapview/internal/workload"
)

type admittedMetrics struct {
	Metrics
	admitter           workload.Admitter
	defaultWorkspaceID string
}

func WithAdmission(metrics Metrics, admitter workload.Admitter, defaultWorkspaceID string) Metrics {
	if metrics == nil {
		return nil
	}
	return admittedMetrics{Metrics: metrics, admitter: admitter, defaultWorkspaceID: defaultWorkspaceID}
}

func (m admittedMetrics) readContext(ctx context.Context) context.Context {
	return workload.WithAdmitter(ctx, m.admitter)
}

func (m admittedMetrics) MetricsForWorkspace(workspaceID string) (Metrics, bool) {
	provider, ok := m.Metrics.(WorkspaceMetrics)
	if ok {
		metrics, found := provider.MetricsForWorkspace(workspaceID)
		if !found || metrics == nil {
			return nil, found
		}
		return admittedMetrics{Metrics: metrics, admitter: m.admitter, defaultWorkspaceID: workspaceID}, true
	}
	if m.Metrics == nil {
		return nil, false
	}
	if m.defaultWorkspaceID != "" && workspaceID != "" && workspaceID != m.defaultWorkspaceID {
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

func (m admittedMetrics) QueryVisualizationWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	return m.Metrics.QueryVisualizationWindow(m.readContext(ctx), dashboardID, pageID, filters, request)
}

func (m admittedMetrics) QueryVisualizationTile(ctx context.Context, workspaceID, dashboardID, visualID, revision string, zoom, x, y int) (dashboardruntime.SpatialTileResult, error) {
	port, ok := m.Metrics.(visualizationTileMetrics)
	if !ok {
		return dashboardruntime.SpatialTileResult{}, errors.New("spatial tile metrics are not configured")
	}
	return port.QueryVisualizationTile(m.readContext(ctx), workspaceID, dashboardID, visualID, revision, zoom, x, y)
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
	if m.admitter == nil {
		return m.Metrics.ExecuteDataQuery(ctx, request)
	}
	workspaceID := request.WorkspaceID
	if workspaceID == "" {
		workspaceID = m.defaultWorkspaceID
	}
	class := workload.Interactive
	if request.Surface == dataquery.SurfaceAgent {
		class = workload.Background
		if activeClass, activeWorkspace, admitted := workload.Current(ctx); admitted && activeClass == workload.Background {
			workspaceID = activeWorkspace
		}
	}
	operation := request.Operation
	if operation == "" {
		operation = string(request.Kind)
	}
	lease, err := m.admitter.Acquire(ctx, workload.Request{Class: class, WorkspaceID: workspaceID, Operation: operation})
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
	executor, ok := m.Metrics.(arrowquery.Executor)
	if !ok {
		return dataquery.Result{}, errors.New("query metrics do not support native Arrow execution")
	}
	if m.admitter == nil {
		return executor.ExecuteDataQueryArrow(ctx, request, sink)
	}
	workspaceID := request.WorkspaceID
	if workspaceID == "" {
		workspaceID = m.defaultWorkspaceID
	}
	class := workload.Interactive
	if request.Surface == dataquery.SurfaceAgent {
		class = workload.Background
		if activeClass, activeWorkspace, admitted := workload.Current(ctx); admitted && activeClass == workload.Background {
			workspaceID = activeWorkspace
		}
	}
	operation := request.Operation
	if operation == "" {
		operation = string(request.Kind)
	}
	lease, err := m.admitter.Acquire(ctx, workload.Request{Class: class, WorkspaceID: workspaceID, Operation: operation})
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
