package module

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type runtimeReadiness interface {
	RuntimeReady(context.Context, projectgraph.ResourceID) error
}

func MetricsMetadataReady(metrics queryruntime.Metrics, projectID projectgraph.ResourceID) error {
	return metricsMetadataReady(metrics, projectID)
}

func metricsMetadataReady(metrics queryruntime.Metrics, projectID projectgraph.ResourceID) error {
	if metrics == nil {
		return fmt.Errorf("project metrics are required")
	}
	if err := projectID.Validate(); err != nil {
		return fmt.Errorf("project ID: %w", err)
	}
	catalog := metrics.Catalog()
	if catalog.Project.ID != projectID {
		return fmt.Errorf("catalog project = %q, want %q", catalog.Project.ID, projectID)
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
	if metrics.Resolver() == nil {
		return reportMetadataReady(metrics, dashboardID, dashboarddefinition.Definition{}, nil, false)
	}
	dashboardResourceID, err := projectgraph.NewResourceID(dashboardID)
	if err != nil {
		return fmt.Errorf("default dashboard ID: %w", err)
	}
	resolved, err := metrics.Resolver().Resolve(dashboardResourceID)
	if err != nil {
		return reportMetadataReady(metrics, dashboardID, dashboarddefinition.Definition{}, nil, false)
	}
	return reportMetadataReady(metrics, dashboardID, resolved.Definition, resolved.Model, true)
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

func (m *Module) RuntimeReady(ctx context.Context, projectID projectgraph.ResourceID) error {
	if m == nil || m.runtimeMetrics == nil {
		return fmt.Errorf("runtime is not configured")
	}
	if readiness, ok := m.runtimeMetrics.(runtimeReadiness); ok {
		return readiness.RuntimeReady(ctx, projectID)
	}
	metrics, ok := metricsForProject(m.runtimeMetrics, projectID)
	if !ok || metrics == nil {
		return fmt.Errorf("runtime for project %q is not configured", projectID)
	}
	return MetricsMetadataReady(metrics, projectID)
}

func metricsForProject(metrics queryruntime.Metrics, projectID projectgraph.ResourceID) (queryruntime.Metrics, bool) {
	if metrics == nil || projectID.Validate() != nil {
		return nil, false
	}
	if provider, ok := metrics.(queryruntime.ProjectMetrics); ok {
		return provider.MetricsForProject(projectID)
	}
	catalog := metrics.Catalog()
	return metrics, catalog.Project.ID == projectID
}
