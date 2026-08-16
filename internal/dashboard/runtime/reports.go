package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/catalog"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type ReportService struct {
	projectID  projectgraph.ResourceID
	identity   projectgraph.ServingIdentity
	models     map[projectgraph.ResourceID]*semanticmodel.Model
	dashboards map[projectgraph.ResourceID]dashboarddefinition.Definition
	catalog    catalog.Catalog
	defaultID  string
}

func (m *Service) DefaultDashboardID() string {
	return m.reports.DefaultDashboardID()
}

func (m *Service) ModelIDForDashboard(dashboardID string) string {
	return m.reports.ModelIDForDashboard(dashboardID)
}

// Resolver exposes the project-scoped dashboard resolver used by runtime query
// consumers. The service's immutable project definition is the serving source.
func (m *Service) Resolver() dashboardresolver.Resolver {
	if m == nil {
		return nil
	}
	return m.reports
}

func (m *Service) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	parsedModelID, err := projectgraph.NewResourceID(modelID)
	if err != nil {
		return nil, false
	}
	return m.SemanticModelByID(parsedModelID)
}

func (m *Service) SemanticModelByID(modelID projectgraph.ResourceID) (*semanticmodel.Model, bool) {
	if err := modelID.Validate(); err != nil || m == nil || m.reports == nil {
		return nil, false
	}
	return m.reports.SemanticModelByID(modelID)
}

// SemanticModelProjection returns a detached semantic-model projection from
// this runtime generation. Preview and other compiler consumers can inspect
// the exact model selected by a leased runtime without acquiring another
// runtime or retaining a mutable pointer into the base serving state.
func (m *Service) SemanticModelProjection(modelID projectgraph.ResourceID) (*semanticmodel.Model, bool) {
	if err := modelID.Validate(); err != nil {
		return nil, false
	}
	model, ok := m.SemanticModelByID(modelID)
	if !ok || model == nil {
		return nil, false
	}
	encoded, err := json.Marshal(model)
	if err != nil {
		return nil, false
	}
	var detached semanticmodel.Model
	if err := json.Unmarshal(encoded, &detached); err != nil {
		return nil, false
	}
	return &detached, true
}

func (m *Service) QuerySemantic(ctx context.Context, modelID string, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	query := reportAggregateDataQuery(modelID, request)
	result, err := m.ExecuteDataQuery(ctx, query)
	return reportRowsFromDataQuery(result.Rows), err
}

func (m *Service) PreviewSemantic(ctx context.Context, modelID string, request reportdef.RowQuery) (reportdef.QueryRows, error) {
	query := reportRowDataQuery(modelID, request, false)
	result, err := m.ExecuteDataQuery(ctx, query)
	return reportRowsFromDataQuery(result.Rows), err
}

func (m *Service) ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
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
func (s *ReportService) Resolve(dashboardID projectgraph.ResourceID) (dashboardresolver.Resolved, error) {
	if s == nil {
		return dashboardresolver.Resolved{}, dashboardresolver.ErrNotFound
	}
	if err := dashboardID.Validate(); err != nil {
		return dashboardresolver.Resolved{}, dashboardresolver.ErrNotFound
	}
	report, ok := s.compiledDashboard(dashboardID.String())
	if !ok || report.SemanticModel == "" {
		return dashboardresolver.Resolved{}, fmt.Errorf("%w: %q", dashboardresolver.ErrNotFound, dashboardID)
	}
	modelID, err := projectgraph.NewResourceID(report.SemanticModel)
	if err != nil {
		return dashboardresolver.Resolved{}, fmt.Errorf("%w: semantic model %q for dashboard %q", dashboardresolver.ErrNotFound, report.SemanticModel, dashboardID)
	}
	model, ok := s.models[modelID]
	if !ok || model == nil {
		return dashboardresolver.Resolved{}, fmt.Errorf("%w: semantic model %q for dashboard %q", dashboardresolver.ErrNotFound, report.SemanticModel, dashboardID)
	}
	return dashboardresolver.Resolved{
		Definition:      *report,
		Model:           model,
		SemanticModelID: modelID,
		Source:          dashboardresolver.SourceMetadata{Kind: dashboardresolver.SourceProject, Identity: s.identity},
	}, nil
}

func (s *ReportService) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	parsedModelID, err := projectgraph.NewResourceID(modelID)
	if err != nil {
		return nil, false
	}
	return s.SemanticModelByID(parsedModelID)
}

func (s *ReportService) SemanticModelByID(modelID projectgraph.ResourceID) (*semanticmodel.Model, bool) {
	if err := modelID.Validate(); err != nil || s == nil {
		return nil, false
	}
	model, ok := s.models[modelID]
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

func (s *ReportService) reportRuntime(dashboardID string, runtimes map[projectgraph.ResourceID]*modelRuntime) (*dashboarddefinition.Definition, *modelRuntime, error) {
	resourceID, err := projectgraph.NewResourceID(strings.TrimSpace(dashboardID))
	if err != nil {
		return nil, nil, fmt.Errorf("unknown dashboard %q: %w", dashboardID, err)
	}
	resolved, err := s.Resolve(resourceID)
	if err != nil {
		return nil, nil, fmt.Errorf("unknown dashboard %q: %w", dashboardID, err)
	}
	modelID, err := projectgraph.NewResourceID(resolved.Definition.SemanticModel)
	if err != nil {
		return nil, nil, fmt.Errorf("unknown semantic model %q", resolved.Definition.SemanticModel)
	}
	runtime, ok := runtimes[modelID]
	if !ok {
		return nil, nil, fmt.Errorf("unknown semantic model %q", resolved.Definition.SemanticModel)
	}
	return &resolved.Definition, runtime, nil
}

func (s *ReportService) compiledDashboard(dashboardID string) (*dashboarddefinition.Definition, bool) {
	dashboardResourceID, err := projectgraph.NewResourceID(strings.TrimSpace(dashboardID))
	if err != nil {
		return nil, false
	}
	definition, ok := s.dashboards[dashboardResourceID]
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
	resourceID, err := projectgraph.NewResourceID(strings.TrimSpace(modelID))
	if err != nil {
		return nil, fmt.Errorf("unknown semantic model %q", modelID)
	}
	runtime, ok := m.runtimes[resourceID]
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
