package module

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
)

type runtimeReadiness interface {
	RuntimeReady(context.Context, string) error
}

func MetricsMetadataReady(metrics queryruntime.Metrics, workspaceID string) error {
	return metricsMetadataReady(metrics, workspaceID)
}

func metricsMetadataReady(metrics queryruntime.Metrics, workspaceID string) error {
	if metrics == nil || workspaceID == "" {
		return fmt.Errorf("workspace metrics and workspace ID are required")
	}
	catalog := metrics.Catalog()
	if catalog.Workspace.ID != workspaceID {
		return fmt.Errorf("catalog workspace = %q, want %q", catalog.Workspace.ID, workspaceID)
	}
	if len(catalog.Models) == 0 && len(catalog.Dashboards) == 0 {
		return fmt.Errorf("runtime catalog is empty")
	}
	if len(catalog.Dashboards) == 0 {
		return nil
	}
	dashboardID := metrics.DefaultDashboardID()
	if dashboardID == "" {
		return fmt.Errorf("default dashboard is not configured")
	}
	report, model, ok := metrics.Report(dashboardID)
	return reportMetadataReady(metrics, dashboardID, report, model, ok)
}

func reportMetadataReady(metrics interface {
	Pages(string) []dashboard.Page
}, dashboardID string, report dashboarddefinition.Definition, model any, ok bool) error {
	if !ok {
		return fmt.Errorf("default dashboard %q is not available", dashboardID)
	}
	if report.ID == "" {
		return fmt.Errorf("default dashboard %q has no report id", dashboardID)
	}
	if model == nil {
		return fmt.Errorf("default dashboard %q has no semantic model", dashboardID)
	}
	if len(metrics.Pages(dashboardID)) == 0 {
		return fmt.Errorf("default dashboard %q has no pages", dashboardID)
	}
	return nil
}

func (m *Module) RuntimeReady(ctx context.Context, workspaceID string) error {
	if m == nil || m.runtimeMetrics == nil {
		return fmt.Errorf("runtime is not configured")
	}
	if readiness, ok := m.runtimeMetrics.(runtimeReadiness); ok {
		return readiness.RuntimeReady(ctx, workspaceID)
	}
	metrics, ok := metricsForWorkspace(m.runtimeMetrics, workspaceID)
	if !ok || metrics == nil {
		return fmt.Errorf("runtime for workspace %q is not configured", workspaceID)
	}
	return MetricsMetadataReady(metrics, workspaceID)
}

func metricsForWorkspace(metrics queryruntime.Metrics, workspaceID string) (queryruntime.Metrics, bool) {
	if workspaceID == "" {
		return nil, false
	}
	if provider, ok := metrics.(queryruntime.WorkspaceMetrics); ok {
		return provider.MetricsForWorkspace(workspaceID)
	}
	catalog := metrics.Catalog()
	return metrics, catalog.Workspace.ID == workspaceID
}
