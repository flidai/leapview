package module

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

type multiWorkspaceMetrics struct {
	workspaces map[string]Metrics
}

func NewMultiWorkspaceMetrics(workspaces map[string]Metrics) Metrics {
	if len(workspaces) == 0 {
		return nil
	}
	return multiWorkspaceMetrics{workspaces: workspaces}
}

func (m multiWorkspaceMetrics) MetricsForWorkspace(workspaceID string) (Metrics, bool) {
	if strings.TrimSpace(workspaceID) == "" {
		return nil, false
	}
	metrics, ok := m.workspaces[workspaceID]
	return metrics, ok
}

func (m multiWorkspaceMetrics) ExpireVisualizationTileStream(streamID string) {
	for _, metrics := range m.workspaces {
		if expirer, ok := metrics.(interface{ ExpireVisualizationTileStream(string) }); ok {
			expirer.ExpireVisualizationTileStream(streamID)
		}
	}
}

func (m multiWorkspaceMetrics) QueryVisualizationTile(context.Context, string, string, string, int, int, int) (dashboardruntime.SpatialTileResult, error) {
	return dashboardruntime.SpatialTileResult{}, fmt.Errorf("unbound workspace spatial tile runtime is not configured")
}

func (m multiWorkspaceMetrics) Catalog() dashboard.Catalog {
	return dashboard.Catalog{}
}

func (m multiWorkspaceMetrics) DefaultDashboardID() string {
	return ""
}

func (m multiWorkspaceMetrics) ModelIDForDashboard(dashboardID string) string {
	return ""
}

// Resolver is intentionally unbound for the multi-workspace root. Callers
// must select a workspace through MetricsForWorkspace before resolving a
// dashboard.
func (m multiWorkspaceMetrics) Resolver() dashboardresolver.Resolver { return nil }

func (m multiWorkspaceMetrics) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	return nil, false
}

func (m multiWorkspaceMetrics) DefaultFilters(dashboardID string) dashboard.Filters {
	return dashboard.Filters{}.WithDefaults()
}

func (m multiWorkspaceMetrics) NormalizeVisualizationWindow(dashboardID string, request dashboard.TableRequest) dashboard.TableRequest {
	return request.WithDefaults()
}

func (m multiWorkspaceMetrics) QueryDashboard(ctx context.Context, dashboardID string, filters dashboard.Filters) (dashboard.Patch, error) {
	return dashboard.EmptyPatch(filters.WithDefaults(), fmt.Errorf("workspace metrics are not configured")), nil
}

func (m multiWorkspaceMetrics) QueryDashboardPage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	return dashboard.EmptyPatch(filters.WithDefaults(), fmt.Errorf("workspace metrics are not configured")), nil
}

func (m multiWorkspaceMetrics) QueryDashboardVisualizations(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	return dashboard.EmptyPatch(filters.WithDefaults(), fmt.Errorf("workspace metrics are not configured")), nil
}

func (m multiWorkspaceMetrics) QueryVisualization(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, visualID string) (visualizationir.VisualizationEnvelope, error) {
	return visualizationir.VisualizationEnvelope{}, fmt.Errorf("workspace metrics are not configured")
}

func (m multiWorkspaceMetrics) QueryVisualizationWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	return visualizationir.VisualizationEnvelope{}, fmt.Errorf("workspace metrics are not configured")
}

func (m multiWorkspaceMetrics) QuerySemantic(ctx context.Context, modelID string, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	return nil, fmt.Errorf("workspace metrics are not configured")
}

func (m multiWorkspaceMetrics) ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
	if strings.TrimSpace(request.WorkspaceID) == "" {
		return dataquery.Result{}, fmt.Errorf("workspace ID is required")
	}
	metrics := m.workspaces[request.WorkspaceID]
	if metrics != nil {
		return metrics.ExecuteDataQuery(ctx, request)
	}
	return dataquery.Result{}, fmt.Errorf("workspace metrics are not configured")
}

func (m multiWorkspaceMetrics) ExecuteDataQueryArrow(ctx context.Context, request dataquery.Query, sink arrowquery.Sink) (dataquery.Result, error) {
	if strings.TrimSpace(request.WorkspaceID) == "" {
		return dataquery.Result{}, fmt.Errorf("workspace ID is required")
	}
	metrics := m.workspaces[request.WorkspaceID]
	if executor, ok := metrics.(arrowquery.Executor); ok {
		return executor.ExecuteDataQueryArrow(ctx, request, sink)
	}
	return dataquery.Result{}, fmt.Errorf("workspace metrics do not support native Arrow execution")
}

func (m multiWorkspaceMetrics) PreviewSemantic(ctx context.Context, modelID string, request reportdef.RowQuery) (reportdef.QueryRows, error) {
	return nil, fmt.Errorf("workspace metrics are not configured")
}

func (m multiWorkspaceMetrics) Pages(dashboardID string) []dashboard.Page {
	return nil
}

func (m *dynamicRuntimeMetrics) Catalog() dashboard.Catalog {
	if metrics := m.unboundMetrics(); metrics != nil {
		return metrics.Catalog()
	}
	return dashboard.Catalog{}
}

func (m *dynamicRuntimeMetrics) DefaultDashboardID() string {
	if metrics := m.unboundMetrics(); metrics != nil {
		return metrics.DefaultDashboardID()
	}
	return ""
}

func (m *dynamicRuntimeMetrics) ModelIDForDashboard(dashboardID string) string {
	if metrics := m.unboundMetrics(); metrics != nil {
		return metrics.ModelIDForDashboard(dashboardID)
	}
	return ""
}

// Resolver is intentionally unbound. Workspace composition must happen via
// MetricsForWorkspace before a dashboard ID is resolved.
func (m *dynamicRuntimeMetrics) Resolver() dashboardresolver.Resolver { return nil }

func (m *dynamicRuntimeMetrics) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	if metrics := m.unboundMetrics(); metrics != nil {
		return metrics.SemanticModel(modelID)
	}
	return nil, false
}

func (m *dynamicRuntimeMetrics) DefaultFilters(dashboardID string) dashboard.Filters {
	if metrics := m.unboundMetrics(); metrics != nil {
		return metrics.DefaultFilters(dashboardID)
	}
	return dashboard.Filters{}.WithDefaults()
}

func (m *dynamicRuntimeMetrics) NormalizeVisualizationWindow(dashboardID string, request dashboard.TableRequest) dashboard.TableRequest {
	if metrics := m.unboundMetrics(); metrics != nil {
		return metrics.NormalizeVisualizationWindow(dashboardID, request)
	}
	return request.WithDefaults()
}

func (m *dynamicRuntimeMetrics) QueryDashboard(ctx context.Context, dashboardID string, filters dashboard.Filters) (dashboard.Patch, error) {
	if metrics := m.unboundMetrics(); metrics != nil {
		return metrics.QueryDashboard(ctx, dashboardID, filters)
	}
	return dashboard.EmptyPatch(filters.WithDefaults(), fmt.Errorf("workspace metrics are not configured")), nil
}

func (m *dynamicRuntimeMetrics) QueryDashboardPage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	if metrics := m.unboundMetrics(); metrics != nil {
		return metrics.QueryDashboardPage(ctx, dashboardID, pageID, filters)
	}
	return dashboard.EmptyPatch(filters.WithDefaults(), fmt.Errorf("workspace metrics are not configured")), nil
}

func (m *dynamicRuntimeMetrics) QueryDashboardVisualizations(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	if metrics := m.unboundMetrics(); metrics != nil {
		return metrics.QueryDashboardVisualizations(ctx, dashboardID, pageID, filters)
	}
	return dashboard.EmptyPatch(filters.WithDefaults(), fmt.Errorf("workspace metrics are not configured")), nil
}

func (m *dynamicRuntimeMetrics) QueryVisualization(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, visualID string) (visualizationir.VisualizationEnvelope, error) {
	if metrics := m.unboundMetrics(); metrics != nil {
		return metrics.QueryVisualization(ctx, dashboardID, pageID, filters, visualID)
	}
	return visualizationir.VisualizationEnvelope{}, fmt.Errorf("workspace metrics are not configured")
}

func (m *dynamicRuntimeMetrics) QueryVisualizationWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	if metrics := m.unboundMetrics(); metrics != nil {
		return metrics.QueryVisualizationWindow(ctx, dashboardID, pageID, filters, request)
	}
	return visualizationir.VisualizationEnvelope{}, fmt.Errorf("workspace metrics are not configured")
}

func (m *dynamicRuntimeMetrics) QueryVisualizationTile(context.Context, string, string, string, int, int, int) (dashboardruntime.SpatialTileResult, error) {
	return dashboardruntime.SpatialTileResult{}, fmt.Errorf("unbound workspace spatial tile runtime is not configured")
}

func (m *dynamicRuntimeMetrics) ExpireVisualizationTileStream(streamID string) {
	m.mu.Lock()
	metricsByWorkspace := make([]Metrics, 0, len(m.metrics))
	for _, metrics := range m.metrics {
		metricsByWorkspace = append(metricsByWorkspace, metrics)
	}
	m.mu.Unlock()
	for _, metrics := range metricsByWorkspace {
		if expirer, ok := metrics.(interface{ ExpireVisualizationTileStream(string) }); ok {
			expirer.ExpireVisualizationTileStream(streamID)
		}
	}
}

func (m *dynamicRuntimeMetrics) QuerySemantic(ctx context.Context, modelID string, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	if metrics := m.unboundMetrics(); metrics != nil {
		return metrics.QuerySemantic(ctx, modelID, request)
	}
	return nil, fmt.Errorf("workspace metrics are not configured")
}

func (m *dynamicRuntimeMetrics) ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
	if strings.TrimSpace(request.WorkspaceID) == "" {
		return dataquery.Result{}, fmt.Errorf("workspace ID is required")
	}
	if metrics, ok := m.MetricsForWorkspace(request.WorkspaceID); ok {
		return metrics.ExecuteDataQuery(ctx, request)
	}
	return dataquery.Result{}, fmt.Errorf("workspace metrics are not configured")
}

func (m *dynamicRuntimeMetrics) ExecuteDataQueryArrow(ctx context.Context, request dataquery.Query, sink arrowquery.Sink) (dataquery.Result, error) {
	if strings.TrimSpace(request.WorkspaceID) == "" {
		return dataquery.Result{}, fmt.Errorf("workspace ID is required")
	}
	if metrics, ok := m.MetricsForWorkspace(request.WorkspaceID); ok {
		if executor, ok := metrics.(arrowquery.Executor); ok {
			return executor.ExecuteDataQueryArrow(ctx, request, sink)
		}
	}
	return dataquery.Result{}, fmt.Errorf("workspace metrics do not support native Arrow execution")
}

func (m *dynamicRuntimeMetrics) PreviewSemantic(ctx context.Context, modelID string, request reportdef.RowQuery) (reportdef.QueryRows, error) {
	if metrics := m.unboundMetrics(); metrics != nil {
		return metrics.PreviewSemantic(ctx, modelID, request)
	}
	return nil, fmt.Errorf("workspace metrics are not configured")
}

func (m *dynamicRuntimeMetrics) Pages(dashboardID string) []dashboard.Page {
	if metrics := m.unboundMetrics(); metrics != nil {
		return metrics.Pages(dashboardID)
	}
	return nil
}
