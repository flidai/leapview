package runtime

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
)

type ReportService struct {
	workspace *dashboarddefinition.Workspace
	defaultID string
}

func (m *Service) DefaultDashboardID() string {
	return m.reports.DefaultDashboardID()
}

func (m *Service) ModelIDForDashboard(dashboardID string) string {
	return m.reports.ModelIDForDashboard(dashboardID)
}

// Resolver exposes the workspace-scoped dashboard resolver used by runtime
// query consumers. The service's compiled workspace is the project serving
// source; no caller-supplied workspace ID can alter that scope.
func (m *Service) Resolver() dashboardresolver.Resolver {
	if m == nil {
		return nil
	}
	return m.reports
}

func (m *Service) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	return m.reports.SemanticModel(modelID)
}

func (m *Service) QuerySemantic(ctx context.Context, modelID string, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	query := reportAggregateDataQuery(modelID, request)
	query.WorkspaceID = m.workspaceID()
	result, err := m.ExecuteDataQuery(ctx, query)
	return reportRowsFromDataQuery(result.Rows), err
}

func (m *Service) PreviewSemantic(ctx context.Context, modelID string, request reportdef.RowQuery) (reportdef.QueryRows, error) {
	query := reportRowDataQuery(modelID, request, false)
	query.WorkspaceID = m.workspaceID()
	result, err := m.ExecuteDataQuery(ctx, query)
	return reportRowsFromDataQuery(result.Rows), err
}

func (m *Service) workspaceID() string {
	if m != nil && m.reports != nil && m.reports.workspace != nil {
		return m.reports.workspace.Catalog.Workspace.ID
	}
	return ""
}

func (m *Service) ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
	if strings.TrimSpace(request.WorkspaceID) == "" {
		return dataquery.Result{}, fmt.Errorf("workspace ID is required")
	}
	return dataquery.ExecuteAudited(ctx, request, func(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
		runtime, err := m.semanticRuntime(request.ModelID)
		if err != nil {
			return dataquery.Result{}, err
		}
		m.mu.RLock()
		defer m.mu.RUnlock()
		return runtime.data.ExecuteDataQuery(ctx, request)
	})
}

func (m *Service) ExecuteDataQueryArrow(ctx context.Context, request dataquery.Query, sink arrowquery.Sink) (dataquery.Result, error) {
	if strings.TrimSpace(request.WorkspaceID) == "" {
		return dataquery.Result{}, fmt.Errorf("workspace ID is required")
	}
	return dataquery.ExecuteAudited(ctx, request, func(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
		runtime, err := m.semanticRuntime(request.ModelID)
		if err != nil {
			return dataquery.Result{}, err
		}
		arrowRuntime, ok := runtime.data.(arrowquery.Executor)
		if !ok {
			return dataquery.Result{}, fmt.Errorf("semantic model runtime does not support native Arrow execution")
		}
		m.mu.RLock()
		defer m.mu.RUnlock()
		return arrowRuntime.ExecuteDataQueryArrow(ctx, request, sink)
	})
}

func (m *Service) NormalizeVisualizationWindow(dashboardID string, request dashboard.TableRequest) dashboard.TableRequest {
	return m.reports.NormalizeVisualizationWindow(dashboardID, request)
}

func (m *Service) DefaultFilters(dashboardID string) dashboard.Filters {
	return m.reports.DefaultFilters(dashboardID)
}

func (m *Service) Pages(dashboardID string) []dashboard.Page {
	return m.reports.Pages(dashboardID)
}

func (s *ReportService) DefaultDashboardID() string {
	return s.defaultID
}

func (s *ReportService) ModelIDForDashboard(dashboardID string) string {
	report, ok := s.compiledDashboard(dashboardID)
	if !ok {
		return ""
	}
	if report.SemanticModel != "" {
		return report.SemanticModel
	}
	return ""
}

// Resolve implements the capability-owned resolver contract for the compiled
// project/deployment serving state currently held by this runtime.
func (s *ReportService) Resolve(dashboardID string) (dashboardresolver.Resolved, error) {
	if s == nil || s.workspace == nil {
		return dashboardresolver.Resolved{}, dashboardresolver.ErrNotFound
	}
	report, ok := s.compiledDashboard(dashboardID)
	if !ok || report.SemanticModel == "" {
		return dashboardresolver.Resolved{}, fmt.Errorf("%w: %q", dashboardresolver.ErrNotFound, strings.TrimSpace(dashboardID))
	}
	model, ok := s.workspace.Models[report.SemanticModel]
	if !ok || model == nil {
		return dashboardresolver.Resolved{}, fmt.Errorf("%w: semantic model %q for dashboard %q", dashboardresolver.ErrNotFound, report.SemanticModel, strings.TrimSpace(dashboardID))
	}
	return dashboardresolver.Resolved{
		Definition: *report,
		Model:      model,
		Source: dashboardresolver.SourceMetadata{
			Kind:        dashboardresolver.SourceProject,
			WorkspaceID: s.workspace.Catalog.Workspace.ID,
		},
	}, nil
}

func (s *ReportService) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	model, ok := s.workspace.Models[modelID]
	return model, ok
}

func (s *ReportService) NormalizeVisualizationWindow(dashboardID string, request dashboard.TableRequest) dashboard.TableRequest {
	report, ok := s.compiledDashboard(dashboardID)
	if !ok {
		return request.WithDefaults()
	}
	defaults := dashboard.TableRequest{Block: "all", Start: 0, Count: dashboard.TableChunkSize}
	if table, ok := report.Visualizations["orders"]; ok && table.Query.Kind == visualizationdefinition.QueryDetail {
		defaults.Table = "orders"
		defaults.Sort = defaultTableSort(table)
	} else {
		for _, name := range sortedKeys(report.Visualizations) {
			table := report.Visualizations[name]
			if table.Query.Kind != visualizationdefinition.QueryDetail {
				continue
			}
			defaults.Table = name
			defaults.Sort = defaultTableSort(table)
			break
		}
	}
	if defaults.Table == "" {
		defaults = dashboard.DefaultTableRequest()
	}
	if request.Table == "" {
		request.Table = defaults.Table
	}
	if request.Block == "" {
		request.Block = defaults.Block
	}
	if request.Block != "all" && request.Block != "a" && request.Block != "b" && request.Block != "c" {
		request.Block = defaults.Block
	}
	if request.Count <= 0 {
		request.Count = defaults.Count
	}
	if request.Count > dashboard.TableMaxRequestCount {
		request.Count = dashboard.TableMaxRequestCount
	}
	if request.Start < 0 {
		request.Start = 0
	}
	if request.Sort.Key == "" {
		request.Sort = defaults.Sort
	}
	if request.Sort.Direction != "asc" && request.Sort.Direction != "desc" {
		if defaults.Sort.Direction != "" {
			request.Sort.Direction = defaults.Sort.Direction
		} else {
			request.Sort.Direction = "desc"
		}
	}
	return request
}

func (s *ReportService) DefaultFilters(dashboardID string) dashboard.Filters {
	report, ok := s.compiledDashboard(dashboardID)
	if !ok {
		return dashboard.Filters{}.WithDefaults()
	}
	return report.DefaultFilters()
}

func (s *ReportService) Pages(dashboardID string) []dashboard.Page {
	report, ok := s.compiledDashboard(dashboardID)
	if !ok {
		return nil
	}
	pages := make([]dashboard.Page, len(report.Pages))
	for i, page := range report.Pages {
		pages[i] = page.WithDefaults()
	}
	return pages
}

func (s *ReportService) reportRuntime(dashboardID string, runtimes map[string]*modelRuntime) (*dashboarddefinition.Definition, *modelRuntime, error) {
	resolved, err := s.Resolve(dashboardID)
	if err != nil {
		return nil, nil, fmt.Errorf("unknown dashboard %q: %w", dashboardID, err)
	}
	runtime, ok := runtimes[resolved.Definition.SemanticModel]
	if !ok {
		return nil, nil, fmt.Errorf("unknown semantic model %q", resolved.Definition.SemanticModel)
	}
	return &resolved.Definition, runtime, nil
}

func (s *ReportService) compiledDashboard(dashboardID string) (*dashboarddefinition.Definition, bool) {
	definition, ok := s.workspace.Dashboards[dashboardID]
	if !ok {
		return nil, false
	}
	return &definition, true
}

func defaultTableSort(definition visualizationdefinition.Definition) dashboard.TableSort {
	if definition.Query.Detail == nil || len(definition.Query.Detail.DefaultSort) == 0 {
		return dashboard.TableSort{}
	}
	sort := definition.Query.Detail.DefaultSort[0]
	return dashboard.TableSort{Key: sort.FieldID, Direction: sort.Direction}
}

func (m *Service) semanticRuntime(modelID string) (*modelRuntime, error) {
	runtime, ok := m.runtimes[modelID]
	if !ok {
		return nil, fmt.Errorf("unknown semantic model %q", modelID)
	}
	if !runtime.ready {
		return nil, runtime.missing
	}
	return runtime, nil
}

func sortedKeys[T any](items map[string]T) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
