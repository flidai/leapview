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

func (m *Service) Report(dashboardID string) (dashboarddefinition.Definition, *semanticmodel.Model, bool) {
	return m.reports.Report(dashboardID)
}

func (m *Service) VisualizationDefinition(dashboardID, visualID string) (visualizationdefinition.Definition, bool) {
	return m.reports.VisualizationDefinition(dashboardID, visualID)
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

func (s *ReportService) Report(dashboardID string) (dashboarddefinition.Definition, *semanticmodel.Model, bool) {
	report, ok := s.compiledDashboard(dashboardID)
	if !ok {
		return dashboarddefinition.Definition{}, nil, false
	}
	if report.SemanticModel != "" {
		model, ok := s.workspace.Models[report.SemanticModel]
		if !ok {
			return dashboarddefinition.Definition{}, nil, false
		}
		return *report, model, true
	}
	return dashboarddefinition.Definition{}, nil, false
}

func (s *ReportService) VisualizationDefinition(dashboardID, visualID string) (visualizationdefinition.Definition, bool) {
	dashboard, ok := s.workspace.Dashboards[dashboardID]
	if !ok {
		return visualizationdefinition.Definition{}, false
	}
	definition, ok := dashboard.Visualizations[visualID]
	return definition, ok
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
	report, ok := s.compiledDashboard(dashboardID)
	if !ok {
		return nil, nil, fmt.Errorf("unknown dashboard %q", dashboardID)
	}
	if report.SemanticModel == "" {
		return nil, nil, fmt.Errorf("dashboard %q has no semantic model", dashboardID)
	}
	runtime, ok := runtimes[report.SemanticModel]
	if !ok {
		return nil, nil, fmt.Errorf("unknown semantic model %q", report.SemanticModel)
	}
	return report, runtime, nil
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
