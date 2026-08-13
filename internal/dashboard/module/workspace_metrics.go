package module

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

type multiWorkspaceMetrics struct {
	defaultWorkspaceID string
	workspaces         map[string]Metrics
}

func NewMultiWorkspaceMetrics(defaultWorkspaceID string, workspaces map[string]Metrics) Metrics {
	if len(workspaces) == 0 {
		return nil
	}
	return multiWorkspaceMetrics{defaultWorkspaceID: defaultWorkspaceID, workspaces: workspaces}
}

func (m multiWorkspaceMetrics) MetricsForWorkspace(workspaceID string) (Metrics, bool) {
	metrics, ok := m.workspaces[workspaceID]
	return metrics, ok
}

func (m multiWorkspaceMetrics) defaultMetrics() Metrics {
	if metrics := m.workspaces[m.defaultWorkspaceID]; metrics != nil {
		return metrics
	}
	for _, metrics := range m.workspaces {
		return metrics
	}
	return nil
}

func (m multiWorkspaceMetrics) Catalog() dashboard.Catalog {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.Catalog()
	}
	return dashboard.Catalog{}
}

func (m multiWorkspaceMetrics) DefaultDashboardID() string {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.DefaultDashboardID()
	}
	return ""
}

func (m multiWorkspaceMetrics) ModelIDForDashboard(dashboardID string) string {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.ModelIDForDashboard(dashboardID)
	}
	return ""
}

func (m multiWorkspaceMetrics) Report(dashboardID string) (dashboarddefinition.Definition, *semanticmodel.Model, bool) {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.Report(dashboardID)
	}
	return dashboarddefinition.Definition{}, nil, false
}

func (m multiWorkspaceMetrics) VisualizationDefinition(dashboardID, visualID string) (visualizationdefinition.Definition, bool) {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.VisualizationDefinition(dashboardID, visualID)
	}
	return visualizationdefinition.Definition{}, false
}

func (m multiWorkspaceMetrics) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.SemanticModel(modelID)
	}
	return nil, false
}

func (m multiWorkspaceMetrics) DefaultFilters(dashboardID string) dashboard.Filters {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.DefaultFilters(dashboardID)
	}
	return dashboard.Filters{}.WithDefaults()
}

func (m multiWorkspaceMetrics) NormalizeVisualizationWindow(dashboardID string, request dashboard.TableRequest) dashboard.TableRequest {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.NormalizeVisualizationWindow(dashboardID, request)
	}
	return request.WithDefaults()
}

func (m multiWorkspaceMetrics) QueryDashboard(ctx context.Context, dashboardID string, filters dashboard.Filters) (dashboard.Patch, error) {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.QueryDashboard(ctx, dashboardID, filters)
	}
	return dashboard.EmptyPatch(filters.WithDefaults(), fmt.Errorf("workspace metrics are not configured")), nil
}

func (m multiWorkspaceMetrics) QueryDashboardPage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.QueryDashboardPage(ctx, dashboardID, pageID, filters)
	}
	return dashboard.EmptyPatch(filters.WithDefaults(), fmt.Errorf("workspace metrics are not configured")), nil
}

func (m multiWorkspaceMetrics) QueryDashboardVisualizations(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.QueryDashboardVisualizations(ctx, dashboardID, pageID, filters)
	}
	return dashboard.EmptyPatch(filters.WithDefaults(), fmt.Errorf("workspace metrics are not configured")), nil
}

func (m multiWorkspaceMetrics) QueryVisualization(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, visualID string) (visualizationir.VisualizationEnvelope, error) {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.QueryVisualization(ctx, dashboardID, pageID, filters, visualID)
	}
	return visualizationir.VisualizationEnvelope{}, fmt.Errorf("workspace metrics are not configured")
}

func (m multiWorkspaceMetrics) QueryVisualizationWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.QueryVisualizationWindow(ctx, dashboardID, pageID, filters, request)
	}
	return visualizationir.VisualizationEnvelope{}, fmt.Errorf("workspace metrics are not configured")
}

func (m multiWorkspaceMetrics) QueryVisualizationSpatialWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationSpatialWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.QueryVisualizationSpatialWindow(ctx, dashboardID, pageID, filters, request)
	}
	return visualizationir.VisualizationEnvelope{}, fmt.Errorf("workspace metrics are not configured")
}

func (m multiWorkspaceMetrics) QuerySemantic(ctx context.Context, modelID string, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.QuerySemantic(ctx, modelID, request)
	}
	return nil, fmt.Errorf("workspace metrics are not configured")
}

func (m multiWorkspaceMetrics) ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
	metrics := m.defaultMetrics()
	if request.WorkspaceID != "" {
		metrics = m.workspaces[request.WorkspaceID]
	}
	if metrics != nil {
		return metrics.ExecuteDataQuery(ctx, request)
	}
	return dataquery.Result{}, fmt.Errorf("workspace metrics are not configured")
}

func (m multiWorkspaceMetrics) ExecuteDataQueryArrow(ctx context.Context, request dataquery.Query, sink arrowquery.Sink) (dataquery.Result, error) {
	metrics := m.defaultMetrics()
	if request.WorkspaceID != "" {
		metrics = m.workspaces[request.WorkspaceID]
	}
	if executor, ok := metrics.(arrowquery.Executor); ok {
		return executor.ExecuteDataQueryArrow(ctx, request, sink)
	}
	return dataquery.Result{}, fmt.Errorf("workspace metrics do not support native Arrow execution")
}

func (m multiWorkspaceMetrics) PreviewSemantic(ctx context.Context, modelID string, request reportdef.RowQuery) (reportdef.QueryRows, error) {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.PreviewSemantic(ctx, modelID, request)
	}
	return nil, fmt.Errorf("workspace metrics are not configured")
}

func (m multiWorkspaceMetrics) Pages(dashboardID string) []dashboard.Page {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.Pages(dashboardID)
	}
	return nil
}

func (m *dynamicRuntimeMetrics) Catalog() dashboard.Catalog {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.Catalog()
	}
	return dashboard.Catalog{}
}

func (m *dynamicRuntimeMetrics) DefaultDashboardID() string {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.DefaultDashboardID()
	}
	return ""
}

func (m *dynamicRuntimeMetrics) ModelIDForDashboard(dashboardID string) string {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.ModelIDForDashboard(dashboardID)
	}
	return ""
}

func (m *dynamicRuntimeMetrics) Report(dashboardID string) (dashboarddefinition.Definition, *semanticmodel.Model, bool) {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.Report(dashboardID)
	}
	return dashboarddefinition.Definition{}, nil, false
}

func (m *dynamicRuntimeMetrics) VisualizationDefinition(dashboardID, visualID string) (visualizationdefinition.Definition, bool) {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.VisualizationDefinition(dashboardID, visualID)
	}
	return visualizationdefinition.Definition{}, false
}

func (m *dynamicRuntimeMetrics) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.SemanticModel(modelID)
	}
	return nil, false
}

func (m *dynamicRuntimeMetrics) DefaultFilters(dashboardID string) dashboard.Filters {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.DefaultFilters(dashboardID)
	}
	return dashboard.Filters{}.WithDefaults()
}

func (m *dynamicRuntimeMetrics) NormalizeVisualizationWindow(dashboardID string, request dashboard.TableRequest) dashboard.TableRequest {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.NormalizeVisualizationWindow(dashboardID, request)
	}
	return request.WithDefaults()
}

func (m *dynamicRuntimeMetrics) QueryDashboard(ctx context.Context, dashboardID string, filters dashboard.Filters) (dashboard.Patch, error) {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.QueryDashboard(ctx, dashboardID, filters)
	}
	return dashboard.EmptyPatch(filters.WithDefaults(), fmt.Errorf("workspace metrics are not configured")), nil
}

func (m *dynamicRuntimeMetrics) QueryDashboardPage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.QueryDashboardPage(ctx, dashboardID, pageID, filters)
	}
	return dashboard.EmptyPatch(filters.WithDefaults(), fmt.Errorf("workspace metrics are not configured")), nil
}

func (m *dynamicRuntimeMetrics) QueryDashboardVisualizations(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.QueryDashboardVisualizations(ctx, dashboardID, pageID, filters)
	}
	return dashboard.EmptyPatch(filters.WithDefaults(), fmt.Errorf("workspace metrics are not configured")), nil
}

func (m *dynamicRuntimeMetrics) QueryVisualization(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, visualID string) (visualizationir.VisualizationEnvelope, error) {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.QueryVisualization(ctx, dashboardID, pageID, filters, visualID)
	}
	return visualizationir.VisualizationEnvelope{}, fmt.Errorf("workspace metrics are not configured")
}

func (m *dynamicRuntimeMetrics) QueryVisualizationWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.QueryVisualizationWindow(ctx, dashboardID, pageID, filters, request)
	}
	return visualizationir.VisualizationEnvelope{}, fmt.Errorf("workspace metrics are not configured")
}

func (m *dynamicRuntimeMetrics) QueryVisualizationSpatialWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationSpatialWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.QueryVisualizationSpatialWindow(ctx, dashboardID, pageID, filters, request)
	}
	return visualizationir.VisualizationEnvelope{}, fmt.Errorf("workspace metrics are not configured")
}

func (m *dynamicRuntimeMetrics) QueryVisualizationTile(ctx context.Context, workspaceID, dashboardID, visualID, revision string, zoom, x, y int) (dashboardruntime.SpatialTileResult, error) {
	if metrics, ok := m.MetricsForWorkspace(workspaceID); ok {
		if tiled, ok := metrics.(interface {
			QueryVisualizationTile(context.Context, string, string, string, string, int, int, int) (dashboardruntime.SpatialTileResult, error)
		}); ok {
			return tiled.QueryVisualizationTile(ctx, workspaceID, dashboardID, visualID, revision, zoom, x, y)
		}
	}
	return dashboardruntime.SpatialTileResult{}, fmt.Errorf("workspace spatial tile runtime is not configured")
}

func (m *dynamicRuntimeMetrics) QueryPublicVisualizationTile(ctx context.Context, publicID, dashboardID, visualID, revision string, zoom, x, y int) (dashboardruntime.SpatialTileResult, error) {
	if metrics := m.defaultMetrics(); metrics != nil {
		if tiled, ok := metrics.(interface {
			QueryPublicVisualizationTile(context.Context, string, string, string, string, int, int, int) (dashboardruntime.SpatialTileResult, error)
		}); ok {
			return tiled.QueryPublicVisualizationTile(ctx, publicID, dashboardID, visualID, revision, zoom, x, y)
		}
	}
	return dashboardruntime.SpatialTileResult{}, fmt.Errorf("public spatial tile runtime is not configured")
}

func (m *dynamicRuntimeMetrics) QuerySemantic(ctx context.Context, modelID string, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.QuerySemantic(ctx, modelID, request)
	}
	return nil, fmt.Errorf("workspace metrics are not configured")
}

func (m *dynamicRuntimeMetrics) ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
	workspaceID := request.WorkspaceID
	if workspaceID == "" {
		workspaceID = m.defaultID
	}
	if metrics, ok := m.MetricsForWorkspace(workspaceID); ok {
		if request.WorkspaceID == "" {
			request.WorkspaceID = workspaceID
		}
		return metrics.ExecuteDataQuery(ctx, request)
	}
	return dataquery.Result{}, fmt.Errorf("workspace metrics are not configured")
}

func (m *dynamicRuntimeMetrics) ExecuteDataQueryArrow(ctx context.Context, request dataquery.Query, sink arrowquery.Sink) (dataquery.Result, error) {
	workspaceID := request.WorkspaceID
	if workspaceID == "" {
		workspaceID = m.defaultID
	}
	if metrics, ok := m.MetricsForWorkspace(workspaceID); ok {
		if request.WorkspaceID == "" {
			request.WorkspaceID = workspaceID
		}
		if executor, ok := metrics.(arrowquery.Executor); ok {
			return executor.ExecuteDataQueryArrow(ctx, request, sink)
		}
	}
	return dataquery.Result{}, fmt.Errorf("workspace metrics do not support native Arrow execution")
}

func (m *dynamicRuntimeMetrics) PreviewSemantic(ctx context.Context, modelID string, request reportdef.RowQuery) (reportdef.QueryRows, error) {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.PreviewSemantic(ctx, modelID, request)
	}
	return nil, fmt.Errorf("workspace metrics are not configured")
}

func (m *dynamicRuntimeMetrics) Pages(dashboardID string) []dashboard.Page {
	if metrics := m.defaultMetrics(); metrics != nil {
		return metrics.Pages(dashboardID)
	}
	return nil
}
