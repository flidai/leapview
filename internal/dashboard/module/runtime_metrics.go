package module

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	"github.com/flidai/leapview/internal/runtimehost"
)

type runtimeMetrics struct {
	provider    runtimehost.Provider
	workspaceID string
}

// Catalog is the dashboard-owned catalog contract exposed through the module
// surface for application composition.
type Catalog = dashboard.Catalog

type dashboardRefreshRuntimeKey struct{}

type dashboardRefreshRuntime struct {
	workspaceID string
	runtime     runtimehost.Runtime
}

type dynamicRuntimeMetrics struct {
	defaultID string
	factory   func(workspaceID string) runtimehost.Provider
	mu        sync.Mutex
	metrics   map[string]Metrics
}

type catalogRuntime interface {
	Catalog() dashboard.Catalog
	DefaultDashboardID() string
	ModelIDForDashboard(dashboardID string) string
	Pages(dashboardID string) []dashboard.Page
}

type reportRuntime interface {
	Report(dashboardID string) (dashboarddefinition.Definition, *semanticmodel.Model, bool)
	VisualizationDefinition(dashboardID, visualID string) (visualizationdefinition.Definition, bool)
	SemanticModel(modelID string) (*semanticmodel.Model, bool)
	DefaultFilters(dashboardID string) dashboard.Filters
}

type dashboardRuntime interface {
	QueryDashboardPage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error)
}

type filterOptionRuntime interface {
	QueryCompiledFilterOptions(context.Context, string, dashboardfilter.OptionQuery) (dashboardfilter.OptionResult, error)
}

type visualizationRuntime interface {
	NormalizeVisualizationWindow(dashboardID string, request dashboard.TableRequest) dashboard.TableRequest
	QueryVisualization(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, visualID string) (visualizationir.VisualizationEnvelope, error)
	QueryVisualizationWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error)
	QueryVisualizationSpatialWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationSpatialWindowRequest) (visualizationir.VisualizationEnvelope, error)
}

type spatialTileRuntime interface {
	QueryVisualizationTile(ctx context.Context, dashboardID, visualID, revision string, zoom, x, y int) (dashboardruntime.SpatialTileResult, error)
	QueryPublicVisualizationTile(ctx context.Context, publicID, dashboardID, visualID, revision string, zoom, x, y int) (dashboardruntime.SpatialTileResult, error)
}

type semanticQueryRuntime interface {
	ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error)
	QuerySemantic(ctx context.Context, modelID string, request reportdef.AggregateQuery) (reportdef.QueryRows, error)
	PreviewSemantic(ctx context.Context, modelID string, request reportdef.RowQuery) (reportdef.QueryRows, error)
}

type semanticArrowQueryRuntime interface {
	ExecuteDataQueryArrow(ctx context.Context, request dataquery.Query, sink arrowquery.Sink) (dataquery.Result, error)
}

func SupportsNativeArrow(metrics Metrics) bool {
	_, ok := metrics.(semanticArrowQueryRuntime)
	return ok
}

func NewRuntimeMetrics(provider runtimehost.Provider, workspaceID string) Metrics {
	return runtimeMetrics{provider: provider, workspaceID: workspaceID}
}

func NewDynamicRuntimeMetrics(defaultWorkspaceID string, factory func(workspaceID string) runtimehost.Provider) Metrics {
	return &dynamicRuntimeMetrics{
		defaultID: defaultWorkspaceID,
		factory:   factory,
		metrics:   map[string]Metrics{},
	}
}

func (m *dynamicRuntimeMetrics) RuntimeReady(ctx context.Context, workspaceID string) error {
	metrics, ok := m.MetricsForWorkspace(workspaceID)
	if !ok || metrics == nil {
		return fmt.Errorf("runtime for workspace %q is not configured", workspaceID)
	}
	if readiness, ok := metrics.(runtimeReadiness); ok {
		return readiness.RuntimeReady(ctx, workspaceID)
	}
	return metricsMetadataReady(metrics, workspaceID)
}

func (m *dynamicRuntimeMetrics) MetricsForWorkspace(workspaceID string) (Metrics, bool) {
	if workspaceID == "" || m.factory == nil {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if metrics := m.metrics[workspaceID]; metrics != nil {
		return metrics, true
	}
	provider := m.factory(workspaceID)
	if provider == nil {
		return nil, false
	}
	metrics := NewRuntimeMetrics(provider, workspaceID)
	m.metrics[workspaceID] = metrics
	return metrics, true
}

func (m *dynamicRuntimeMetrics) defaultMetrics() Metrics {
	return nil
}

func (m runtimeMetrics) Catalog() dashboard.Catalog {
	runtime, release, err := m.active(context.Background())
	if err != nil {
		title := strings.TrimSpace(m.workspaceID)
		if title == "" {
			title = "Workspace"
		}
		return dashboard.Catalog{
			Workspace: dashboard.CatalogWorkspace{ID: m.workspaceID, Title: title, Description: "No active serving state."},
		}
	}
	defer release()
	port, ok := runtime.(catalogRuntime)
	if !ok {
		return dashboard.Catalog{}
	}
	return port.Catalog()
}

func (m runtimeMetrics) DefaultDashboardID() string {
	runtime, release, err := m.active(context.Background())
	if err != nil {
		return ""
	}
	defer release()
	port, ok := runtime.(catalogRuntime)
	if !ok {
		return ""
	}
	return port.DefaultDashboardID()
}

func (m runtimeMetrics) ModelIDForDashboard(dashboardID string) string {
	runtime, release, err := m.active(context.Background())
	if err != nil {
		return ""
	}
	defer release()
	port, ok := runtime.(catalogRuntime)
	if !ok {
		return ""
	}
	return port.ModelIDForDashboard(dashboardID)
}

func (m runtimeMetrics) Report(dashboardID string) (dashboarddefinition.Definition, *semanticmodel.Model, bool) {
	runtime, release, err := m.active(context.Background())
	if err != nil {
		return dashboarddefinition.Definition{}, nil, false
	}
	defer release()
	port, ok := runtime.(reportRuntime)
	if !ok {
		return dashboarddefinition.Definition{}, nil, false
	}
	return port.Report(dashboardID)
}

func (m runtimeMetrics) VisualizationDefinition(dashboardID, visualID string) (visualizationdefinition.Definition, bool) {
	runtime, release, err := m.active(context.Background())
	if err != nil {
		return visualizationdefinition.Definition{}, false
	}
	defer release()
	port, ok := runtime.(reportRuntime)
	if !ok {
		return visualizationdefinition.Definition{}, false
	}
	return port.VisualizationDefinition(dashboardID, visualID)
}

func (m runtimeMetrics) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	runtime, release, err := m.active(context.Background())
	if err != nil {
		return nil, false
	}
	defer release()
	port, ok := runtime.(reportRuntime)
	if !ok {
		return nil, false
	}
	return port.SemanticModel(modelID)
}

func (m runtimeMetrics) DefaultFilters(dashboardID string) dashboard.Filters {
	runtime, release, err := m.active(context.Background())
	if err != nil {
		return dashboard.Filters{}.WithDefaults()
	}
	defer release()
	port, ok := runtime.(reportRuntime)
	if !ok {
		return dashboard.Filters{}.WithDefaults()
	}
	return port.DefaultFilters(dashboardID)
}

func (m runtimeMetrics) NormalizeVisualizationWindow(dashboardID string, request dashboard.TableRequest) dashboard.TableRequest {
	runtime, release, err := m.active(context.Background())
	if err != nil {
		return request.WithDefaults()
	}
	defer release()
	port, ok := runtime.(visualizationRuntime)
	if !ok {
		return request.WithDefaults()
	}
	return port.NormalizeVisualizationWindow(dashboardID, request)
}

func (m runtimeMetrics) QueryDashboard(ctx context.Context, dashboardID string, filters dashboard.Filters) (dashboard.Patch, error) {
	return m.QueryDashboardPage(ctx, dashboardID, "", filters)
}

func (m runtimeMetrics) QueryCompiledFilterOptions(ctx context.Context, dashboardID string, query dashboardfilter.OptionQuery) (dashboardfilter.OptionResult, error) {
	runtime, release, err := m.active(ctx)
	if err != nil {
		return dashboardfilter.OptionResult{}, err
	}
	defer release()
	port, ok := runtime.(filterOptionRuntime)
	if !ok {
		return dashboardfilter.OptionResult{}, fmt.Errorf("compiled filter options are not supported by this runtime")
	}
	return port.QueryCompiledFilterOptions(ctx, dashboardID, query)
}

func (m runtimeMetrics) QueryDashboardPage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	runtime, release, err := m.active(ctx)
	if err != nil {
		return dashboard.EmptyPatch(filters.WithDefaults(), err), nil
	}
	defer release()
	port, ok := runtime.(dashboardRuntime)
	if !ok {
		err := fmt.Errorf("active runtime does not provide dashboard data")
		return dashboard.EmptyPatch(filters.WithDefaults(), err), nil
	}
	return port.QueryDashboardPage(ctx, dashboardID, pageID, filters)
}

func (m runtimeMetrics) QueryDashboardVisualizations(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	return m.QueryDashboardPage(ctx, dashboardID, pageID, filters)
}

func (m runtimeMetrics) QueryVisualization(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, visualID string) (visualizationir.VisualizationEnvelope, error) {
	runtime, release, err := m.activeForDashboardRefresh(ctx)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	defer release()
	port, ok := runtime.(visualizationRuntime)
	if !ok {
		return visualizationir.VisualizationEnvelope{}, fmt.Errorf("active runtime does not provide visualization data")
	}
	return port.QueryVisualization(ctx, dashboardID, pageID, filters, visualID)
}

func (m runtimeMetrics) QueryVisualizationWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	runtime, release, err := m.activeForDashboardRefresh(ctx)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	defer release()
	port, ok := runtime.(visualizationRuntime)
	if !ok {
		return visualizationir.VisualizationEnvelope{}, fmt.Errorf("active runtime does not provide visualization data")
	}
	return port.QueryVisualizationWindow(ctx, dashboardID, pageID, filters, request)
}

func (m runtimeMetrics) QueryVisualizationSpatialWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationSpatialWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	runtime, release, err := m.activeForDashboardRefresh(ctx)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	defer release()
	port, ok := runtime.(visualizationRuntime)
	if !ok {
		return visualizationir.VisualizationEnvelope{}, fmt.Errorf("active runtime does not provide visualization data")
	}
	return port.QueryVisualizationSpatialWindow(ctx, dashboardID, pageID, filters, request)
}

func (m runtimeMetrics) QueryVisualizationTile(ctx context.Context, workspaceID, dashboardID, visualID, revision string, zoom, x, y int) (dashboardruntime.SpatialTileResult, error) {
	runtime, release, err := m.active(ctx)
	if err != nil {
		return dashboardruntime.SpatialTileResult{}, err
	}
	defer release()
	port, ok := runtime.(spatialTileRuntime)
	if !ok {
		return dashboardruntime.SpatialTileResult{}, fmt.Errorf("active runtime does not provide spatial tiles")
	}
	return port.QueryVisualizationTile(ctx, dashboardID, visualID, revision, zoom, x, y)
}

func (m runtimeMetrics) QueryPublicVisualizationTile(ctx context.Context, publicID, dashboardID, visualID, revision string, zoom, x, y int) (dashboardruntime.SpatialTileResult, error) {
	runtime, release, err := m.active(ctx)
	if err != nil {
		return dashboardruntime.SpatialTileResult{}, err
	}
	defer release()
	port, ok := runtime.(spatialTileRuntime)
	if !ok {
		return dashboardruntime.SpatialTileResult{}, fmt.Errorf("active runtime does not provide public spatial tiles")
	}
	return port.QueryPublicVisualizationTile(ctx, publicID, dashboardID, visualID, revision, zoom, x, y)
}

func (m runtimeMetrics) WithDashboardRefreshLease(ctx context.Context, run func(context.Context) error) error {
	if run == nil {
		return fmt.Errorf("dashboard refresh lease callback is required")
	}
	if pinned, ok := ctx.Value(dashboardRefreshRuntimeKey{}).(dashboardRefreshRuntime); ok && pinned.workspaceID == m.workspaceID && pinned.runtime != nil {
		return run(ctx)
	}
	runtime, release, err := m.active(ctx)
	if err != nil {
		return err
	}
	defer release()
	ctx = context.WithValue(ctx, dashboardRefreshRuntimeKey{}, dashboardRefreshRuntime{workspaceID: m.workspaceID, runtime: runtime})
	return run(ctx)
}

func (m runtimeMetrics) activeForDashboardRefresh(ctx context.Context) (runtimehost.Runtime, func(), error) {
	if pinned, ok := ctx.Value(dashboardRefreshRuntimeKey{}).(dashboardRefreshRuntime); ok && pinned.workspaceID == m.workspaceID && pinned.runtime != nil {
		return pinned.runtime, func() {}, nil
	}
	return m.active(ctx)
}

func (m runtimeMetrics) QuerySemantic(ctx context.Context, modelID string, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	runtime, release, err := m.active(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	port, ok := runtime.(semanticQueryRuntime)
	if !ok {
		return nil, fmt.Errorf("active runtime does not provide semantic query data")
	}
	return port.QuerySemantic(ctx, modelID, request)
}

func (m runtimeMetrics) ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
	runtime, release, err := m.active(ctx)
	if err != nil {
		return dataquery.Result{}, err
	}
	defer release()
	port, ok := runtime.(semanticQueryRuntime)
	if !ok {
		return dataquery.Result{}, fmt.Errorf("active runtime does not provide semantic query data")
	}
	if request.WorkspaceID == "" {
		request.WorkspaceID = m.workspaceID
	}
	return port.ExecuteDataQuery(ctx, request)
}

func (m runtimeMetrics) ExecuteDataQueryArrow(ctx context.Context, request dataquery.Query, sink arrowquery.Sink) (dataquery.Result, error) {
	runtime, release, err := m.active(ctx)
	if err != nil {
		return dataquery.Result{}, err
	}
	defer release()
	port, ok := runtime.(semanticArrowQueryRuntime)
	if !ok {
		return dataquery.Result{}, fmt.Errorf("active runtime does not provide native Arrow query data")
	}
	if request.WorkspaceID == "" {
		request.WorkspaceID = m.workspaceID
	}
	return port.ExecuteDataQueryArrow(ctx, request, sink)
}

func (m runtimeMetrics) PreviewSemantic(ctx context.Context, modelID string, request reportdef.RowQuery) (reportdef.QueryRows, error) {
	runtime, release, err := m.active(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	port, ok := runtime.(semanticQueryRuntime)
	if !ok {
		return nil, fmt.Errorf("active runtime does not provide semantic query data")
	}
	return port.PreviewSemantic(ctx, modelID, request)
}

func (m runtimeMetrics) ExplainSemanticQuery(modelID string, request reportdef.AggregateQuery) (semanticquery.Plan, error) {
	model, ok := m.SemanticModel(modelID)
	if !ok {
		return semanticquery.Plan{}, fmt.Errorf("unknown semantic model %q", modelID)
	}
	return semanticquery.NewPlanner(model).Plan(reportdef.SemanticAggregateRequest(request))
}

func (m runtimeMetrics) ExplainSemanticPreview(modelID string, request reportdef.RowQuery) (semanticquery.Plan, error) {
	model, ok := m.SemanticModel(modelID)
	if !ok {
		return semanticquery.Plan{}, fmt.Errorf("unknown semantic model %q", modelID)
	}
	return semanticquery.NewPlanner(model).PlanRows(reportdef.SemanticRowRequest(request))
}

func (m runtimeMetrics) Pages(dashboardID string) []dashboard.Page {
	runtime, release, err := m.active(context.Background())
	if err != nil {
		return nil
	}
	defer release()
	port, ok := runtime.(catalogRuntime)
	if !ok {
		return nil
	}
	return port.Pages(dashboardID)
}

func (m runtimeMetrics) RuntimeReady(ctx context.Context, workspaceID string) error {
	activeRuntime, release, err := m.active(ctx)
	if err != nil {
		return err
	}
	defer release()
	catalogPort, ok := activeRuntime.(catalogRuntime)
	if !ok {
		return fmt.Errorf("active runtime does not provide catalog metadata")
	}
	catalog := catalogPort.Catalog()
	if workspaceID != "" && catalog.Workspace.ID != "" && catalog.Workspace.ID != workspaceID {
		return fmt.Errorf("catalog workspace = %q, want %q", catalog.Workspace.ID, workspaceID)
	}
	if len(catalog.Models) == 0 && len(catalog.Dashboards) == 0 {
		return fmt.Errorf("runtime catalog is empty")
	}
	if len(catalog.Dashboards) == 0 {
		return nil
	}
	defaultDashboardID := catalogPort.DefaultDashboardID()
	if defaultDashboardID == "" {
		return fmt.Errorf("default dashboard is not configured")
	}
	reportPort, ok := activeRuntime.(reportRuntime)
	if !ok {
		return fmt.Errorf("active runtime does not provide report metadata")
	}
	report, model, ok := reportPort.Report(defaultDashboardID)
	return reportMetadataReady(catalogPort, defaultDashboardID, report, model, ok)
}

func (m runtimeMetrics) active(ctx context.Context) (runtimehost.Runtime, func(), error) {
	if m.provider == nil {
		return nil, func() {}, fmt.Errorf("runtime provider is not configured")
	}
	lease, err := m.provider.Acquire(ctx)
	if err != nil {
		return nil, func() {}, err
	}
	return lease.Runtime(), lease.Release, nil
}
