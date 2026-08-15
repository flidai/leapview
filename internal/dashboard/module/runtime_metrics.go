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
	"github.com/flidai/leapview/internal/dashboard/consumer"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	"github.com/flidai/leapview/internal/runtimehost"
)

type runtimeMetrics struct {
	provider                   runtimehost.Provider
	workspaceID                string
	publishedCompilationReader dashboardresolver.PublishedCompilationReader
}

// Catalog is the dashboard-owned catalog contract exposed through the module
// surface for application composition.
type Catalog = dashboard.Catalog

type dashboardRefreshRuntimeKey struct{}

type dashboardRefreshRuntime struct {
	workspaceID    string
	runtime        runtimehost.Runtime
	servingStateID string
	resolutions    *dashboardRefreshResolutionCache
}

type dashboardRefreshResolutionCache struct {
	mu     sync.Mutex
	values map[string]dashboardresolver.Resolved
	errors map[string]error
}

type dynamicRuntimeMetrics struct {
	factory                    func(workspaceID string) runtimehost.Provider
	publishedCompilationReader dashboardresolver.PublishedCompilationReader
	mu                         sync.Mutex
	metrics                    map[string]Metrics
}

type catalogRuntime interface {
	Catalog() dashboard.Catalog
	DefaultDashboardID() string
	ModelIDForDashboard(dashboardID string) string
	Pages(dashboardID string) []dashboard.Page
}

type runtimeResolver interface {
	Resolver() dashboardresolver.Resolver
	SemanticModel(modelID string) (*semanticmodel.Model, bool)
	DefaultFilters(dashboardID string) dashboard.Filters
}

type dashboardRuntime interface {
	QueryDashboardPage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error)
}

type definitionDashboardRuntime interface {
	QueryDashboardPageForDefinition(context.Context, dashboarddefinition.Definition, string, dashboard.Filters) (dashboard.Patch, error)
}

type definitionVisualizationRuntime interface {
	QueryVisualizationForDefinition(context.Context, dashboarddefinition.Definition, string, dashboard.Filters, string) (visualizationir.VisualizationEnvelope, error)
	QueryVisualizationWindowForDefinition(context.Context, dashboarddefinition.Definition, string, dashboard.Filters, visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error)
	NormalizeVisualizationWindowForDefinition(dashboarddefinition.Definition, dashboard.TableRequest) dashboard.TableRequest
}

type definitionMetadataRuntime interface {
	DefaultFiltersForDefinition(dashboarddefinition.Definition) dashboard.Filters
	PagesForDefinition(dashboarddefinition.Definition) []dashboard.Page
	ModelIDForDashboardDefinition(dashboarddefinition.Definition) string
}

type definitionFilterRuntime interface {
	QueryCompiledFilterOptionsForDefinition(context.Context, dashboarddefinition.Definition, dashboardfilter.OptionQuery) (dashboardfilter.OptionResult, error)
}

type definitionConsumerRuntime interface {
	ExecuteConsumersPageForDefinition(context.Context, dashboarddefinition.Definition, consumer.Request, consumer.Publisher) error
}

type filterOptionRuntime interface {
	QueryCompiledFilterOptions(context.Context, string, dashboardfilter.OptionQuery) (dashboardfilter.OptionResult, error)
}

type visualizationRuntime interface {
	NormalizeVisualizationWindow(dashboardID string, request dashboard.TableRequest) dashboard.TableRequest
	QueryVisualization(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, visualID string) (visualizationir.VisualizationEnvelope, error)
	QueryVisualizationWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error)
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

type RuntimeMetricsOptions struct {
	Provider                   runtimehost.Provider
	WorkspaceID                string
	PublishedCompilationReader dashboardresolver.PublishedCompilationReader
}

func NewRuntimeMetrics(options RuntimeMetricsOptions) Metrics {
	return runtimeMetrics{
		provider:                   options.Provider,
		workspaceID:                strings.TrimSpace(options.WorkspaceID),
		publishedCompilationReader: options.PublishedCompilationReader,
	}
}

// Resolver exposes the project/deployment dashboard source through the shared
// resolver boundary. The workspace is fixed when this metrics value is
// composed; lookup callers provide only a dashboard ID.
func (m runtimeMetrics) Resolver() dashboardresolver.Resolver {
	return runtimeMetricsResolver{metrics: m}
}

type runtimeMetricsResolver struct {
	metrics runtimeMetrics
}

func (r runtimeMetricsResolver) Resolve(dashboardID string) (dashboardresolver.Resolved, error) {
	if r.metrics.provider == nil {
		return dashboardresolver.Resolved{}, fmt.Errorf("runtime provider is not configured")
	}
	lease, err := r.metrics.provider.Acquire(context.Background())
	if err != nil {
		return dashboardresolver.Resolved{}, err
	}
	defer lease.Release()
	runtime := lease.Runtime()
	return r.metrics.resolveOnRuntime(runtime, strings.TrimSpace(string(lease.ServingStateID())), dashboardID)
}

func runtimeResolverPort(runtime runtimehost.Runtime) (runtimeResolver, bool) {
	port, ok := runtime.(runtimeResolver)
	return port, ok
}

func (m runtimeMetrics) resolveOnRuntime(runtime runtimehost.Runtime, servingStateID, dashboardID string) (dashboardresolver.Resolved, error) {
	port, ok := runtimeResolverPort(runtime)
	if !ok {
		return dashboardresolver.Resolved{}, fmt.Errorf("active runtime does not provide dashboard resolver")
	}
	project := dashboardresolver.NewProject(port.Resolver(), m.workspaceID, dashboardresolver.SourceMetadata{ServingStateID: strings.TrimSpace(servingStateID)})
	var published dashboardresolver.Resolver
	if m.publishedCompilationReader != nil {
		published = dashboardresolver.NewPublished(
			dashboardresolver.NewPublishedCompilationResolver(m.workspaceID, strings.TrimSpace(servingStateID), m.publishedCompilationReader, port),
			m.workspaceID,
			dashboardresolver.SourceMetadata{},
		)
	}
	composite, err := dashboardresolver.NewComposite(m.workspaceID, project, published)
	if err != nil {
		return dashboardresolver.Resolved{}, err
	}
	return composite.Resolve(dashboardID)
}

type DynamicRuntimeMetricsOptions struct {
	ProviderFactory            func(workspaceID string) runtimehost.Provider
	PublishedCompilationReader dashboardresolver.PublishedCompilationReader
}

func NewDynamicRuntimeMetrics(options DynamicRuntimeMetricsOptions) Metrics {
	return &dynamicRuntimeMetrics{
		factory:                    options.ProviderFactory,
		publishedCompilationReader: options.PublishedCompilationReader,
		metrics:                    map[string]Metrics{},
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
	if strings.TrimSpace(workspaceID) == "" || m.factory == nil {
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
	metrics := NewRuntimeMetrics(RuntimeMetricsOptions{Provider: provider, WorkspaceID: workspaceID, PublishedCompilationReader: m.publishedCompilationReader})
	m.metrics[workspaceID] = metrics
	return metrics, true
}

// unboundMetrics intentionally never selects a workspace. Callers must first
// resolve a workspace through MetricsForWorkspace.
func (m *dynamicRuntimeMetrics) unboundMetrics() Metrics {
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
	runtime, release, resolved, err := m.activeResolved(context.Background(), dashboardID)
	if err != nil {
		return ""
	}
	defer release()
	if resolved.Source.Kind == dashboardresolver.SourceWorkspace {
		if port, ok := runtime.(definitionMetadataRuntime); ok {
			return port.ModelIDForDashboardDefinition(resolved.Definition)
		}
		return ""
	}
	if port, ok := runtime.(catalogRuntime); ok {
		return port.ModelIDForDashboard(dashboardID)
	}
	return resolved.Definition.SemanticModel
}

func (m runtimeMetrics) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	runtime, release, err := m.active(context.Background())
	if err != nil {
		return nil, false
	}
	defer release()
	port, ok := runtimeResolverPort(runtime)
	if !ok {
		return nil, false
	}
	return port.SemanticModel(modelID)
}

func (m runtimeMetrics) DefaultFilters(dashboardID string) dashboard.Filters {
	runtime, release, resolved, err := m.activeResolved(context.Background(), dashboardID)
	if err != nil {
		return dashboard.Filters{}.WithDefaults()
	}
	defer release()
	if resolved.Source.Kind == dashboardresolver.SourceWorkspace {
		if port, ok := runtime.(definitionMetadataRuntime); ok {
			return port.DefaultFiltersForDefinition(resolved.Definition)
		}
		return dashboard.Filters{}.WithDefaults()
	}
	if port, ok := runtime.(runtimeResolver); ok {
		return port.DefaultFilters(dashboardID)
	}
	return dashboard.Filters{}.WithDefaults()
}

func (m runtimeMetrics) NormalizeVisualizationWindow(dashboardID string, request dashboard.TableRequest) dashboard.TableRequest {
	runtime, release, resolved, err := m.activeResolved(context.Background(), dashboardID)
	if err != nil {
		return request.WithDefaults()
	}
	defer release()
	if resolved.Source.Kind == dashboardresolver.SourceWorkspace {
		if port, ok := runtime.(definitionVisualizationRuntime); ok {
			return port.NormalizeVisualizationWindowForDefinition(resolved.Definition, request)
		}
		return request.WithDefaults()
	}
	port, ok := runtime.(visualizationRuntime)
	if ok && resolved.Source.Kind == dashboardresolver.SourceProject {
		return port.NormalizeVisualizationWindow(dashboardID, request)
	}
	return request.WithDefaults()
}

func (m runtimeMetrics) QueryDashboard(ctx context.Context, dashboardID string, filters dashboard.Filters) (dashboard.Patch, error) {
	return m.QueryDashboardPage(ctx, dashboardID, "", filters)
}

func (m runtimeMetrics) QueryCompiledFilterOptions(ctx context.Context, dashboardID string, query dashboardfilter.OptionQuery) (dashboardfilter.OptionResult, error) {
	runtime, release, resolved, err := m.activeResolvedForDashboardRefresh(ctx, dashboardID)
	if err != nil {
		return dashboardfilter.OptionResult{}, err
	}
	defer release()
	if resolved.Source.Kind == dashboardresolver.SourceWorkspace {
		if port, ok := runtime.(definitionFilterRuntime); ok {
			return port.QueryCompiledFilterOptionsForDefinition(ctx, resolved.Definition, query)
		}
		return dashboardfilter.OptionResult{}, fmt.Errorf("compiled filter options are not supported by this runtime")
	}
	port, ok := runtime.(filterOptionRuntime)
	if !ok || resolved.Source.Kind != dashboardresolver.SourceProject {
		return dashboardfilter.OptionResult{}, fmt.Errorf("compiled filter options are not supported by this runtime")
	}
	return port.QueryCompiledFilterOptions(ctx, dashboardID, query)
}

func (m runtimeMetrics) QueryDashboardPage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	runtime, release, resolved, err := m.activeResolvedForDashboardRefresh(ctx, dashboardID)
	if err != nil {
		return dashboard.EmptyPatch(filters.WithDefaults(), err), nil
	}
	defer release()
	if resolved.Source.Kind == dashboardresolver.SourceWorkspace {
		if port, ok := runtime.(definitionDashboardRuntime); ok {
			return port.QueryDashboardPageForDefinition(ctx, resolved.Definition, pageID, filters)
		}
		err := fmt.Errorf("active runtime does not provide compiled dashboard data")
		return dashboard.EmptyPatch(filters.WithDefaults(), err), nil
	}
	port, ok := runtime.(dashboardRuntime)
	if !ok || resolved.Source.Kind != dashboardresolver.SourceProject {
		err := fmt.Errorf("active runtime does not provide dashboard data")
		return dashboard.EmptyPatch(filters.WithDefaults(), err), nil
	}
	return port.QueryDashboardPage(ctx, dashboardID, pageID, filters)
}

func (m runtimeMetrics) QueryDashboardVisualizations(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	return m.QueryDashboardPage(ctx, dashboardID, pageID, filters)
}

func (m runtimeMetrics) QueryVisualization(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, visualID string) (visualizationir.VisualizationEnvelope, error) {
	runtime, release, resolved, err := m.activeResolvedForDashboardRefresh(ctx, dashboardID)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	defer release()
	if resolved.Source.Kind == dashboardresolver.SourceWorkspace {
		if port, ok := runtime.(definitionVisualizationRuntime); ok {
			return port.QueryVisualizationForDefinition(ctx, resolved.Definition, pageID, filters, visualID)
		}
		return visualizationir.VisualizationEnvelope{}, fmt.Errorf("active runtime does not provide compiled visualization data")
	}
	port, ok := runtime.(visualizationRuntime)
	if !ok || resolved.Source.Kind != dashboardresolver.SourceProject {
		return visualizationir.VisualizationEnvelope{}, fmt.Errorf("active runtime does not provide visualization data")
	}
	return port.QueryVisualization(ctx, dashboardID, pageID, filters, visualID)
}

func (m runtimeMetrics) QueryVisualizationWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	runtime, release, resolved, err := m.activeResolvedForDashboardRefresh(ctx, dashboardID)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	defer release()
	if resolved.Source.Kind == dashboardresolver.SourceWorkspace {
		if port, ok := runtime.(definitionVisualizationRuntime); ok {
			return port.QueryVisualizationWindowForDefinition(ctx, resolved.Definition, pageID, filters, request)
		}
		return visualizationir.VisualizationEnvelope{}, fmt.Errorf("active runtime does not provide compiled visualization data")
	}
	port, ok := runtime.(visualizationRuntime)
	if !ok || resolved.Source.Kind != dashboardresolver.SourceProject {
		return visualizationir.VisualizationEnvelope{}, fmt.Errorf("active runtime does not provide visualization data")
	}
	return port.QueryVisualizationWindow(ctx, dashboardID, pageID, filters, request)
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

func (m runtimeMetrics) ExpireVisualizationTileStream(streamID string) {
	runtime, release, err := m.active(context.Background())
	if err != nil {
		return
	}
	defer release()
	if expirer, ok := runtime.(interface{ ExpireVisualizationTileStream(string) }); ok {
		expirer.ExpireVisualizationTileStream(streamID)
	}
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
	runtime, release, servingStateID, err := m.activeWithState(ctx)
	if err != nil {
		return err
	}
	defer release()
	ctx = context.WithValue(ctx, dashboardRefreshRuntimeKey{}, dashboardRefreshRuntime{
		workspaceID: m.workspaceID, runtime: runtime, servingStateID: servingStateID,
		resolutions: &dashboardRefreshResolutionCache{values: map[string]dashboardresolver.Resolved{}, errors: map[string]error{}},
	})
	return run(ctx)
}

func (m runtimeMetrics) activeForDashboardRefresh(ctx context.Context) (runtimehost.Runtime, func(), error) {
	runtime, release, _, err := m.activeResolvedForDashboardRefreshRaw(ctx)
	return runtime, release, err
}

func (m runtimeMetrics) activeResolvedForDashboardRefreshRaw(ctx context.Context) (runtimehost.Runtime, func(), string, error) {
	if pinned, ok := ctx.Value(dashboardRefreshRuntimeKey{}).(dashboardRefreshRuntime); ok && pinned.workspaceID == m.workspaceID && pinned.runtime != nil {
		return pinned.runtime, func() {}, pinned.servingStateID, nil
	}
	return m.activeWithState(ctx)
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
	if strings.TrimSpace(request.WorkspaceID) == "" {
		return dataquery.Result{}, fmt.Errorf("workspace ID is required")
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
	if strings.TrimSpace(request.WorkspaceID) == "" {
		return dataquery.Result{}, fmt.Errorf("workspace ID is required")
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
	runtime, release, resolved, err := m.activeResolved(context.Background(), dashboardID)
	if err != nil {
		return nil
	}
	defer release()
	if resolved.Source.Kind == dashboardresolver.SourceWorkspace {
		if port, ok := runtime.(definitionMetadataRuntime); ok {
			return port.PagesForDefinition(resolved.Definition)
		}
		return nil
	}
	port, ok := runtime.(catalogRuntime)
	if ok && resolved.Source.Kind == dashboardresolver.SourceProject {
		return port.Pages(dashboardID)
	}
	return nil
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
	reportPort, ok := runtimeResolverPort(activeRuntime)
	if !ok {
		return fmt.Errorf("active runtime does not provide report metadata")
	}
	resolved, err := reportPort.Resolver().Resolve(defaultDashboardID)
	if err != nil {
		return reportMetadataReady(catalogPort, defaultDashboardID, dashboarddefinition.Definition{}, nil, false)
	}
	return reportMetadataReady(catalogPort, defaultDashboardID, resolved.Definition, resolved.Model, true)
}

func (m runtimeMetrics) active(ctx context.Context) (runtimehost.Runtime, func(), error) {
	runtime, release, _, err := m.activeWithState(ctx)
	return runtime, release, err
}

func (m runtimeMetrics) activeWithState(ctx context.Context) (runtimehost.Runtime, func(), string, error) {
	if m.provider == nil {
		return nil, func() {}, "", fmt.Errorf("runtime provider is not configured")
	}
	lease, err := m.provider.Acquire(ctx)
	if err != nil {
		return nil, func() {}, "", err
	}
	return lease.Runtime(), lease.Release, strings.TrimSpace(string(lease.ServingStateID())), nil
}

func (m runtimeMetrics) activeResolved(ctx context.Context, dashboardID string) (runtimehost.Runtime, func(), dashboardresolver.Resolved, error) {
	runtime, release, stateID, err := m.activeWithState(ctx)
	if err != nil {
		return nil, func() {}, dashboardresolver.Resolved{}, err
	}
	resolved, err := m.resolveOnRuntime(runtime, stateID, dashboardID)
	if err != nil {
		release()
		return nil, func() {}, dashboardresolver.Resolved{}, err
	}
	return runtime, release, resolved, nil
}

func (m runtimeMetrics) activeResolvedForDashboardRefresh(ctx context.Context, dashboardID string) (runtimehost.Runtime, func(), dashboardresolver.Resolved, error) {
	if pinned, ok := ctx.Value(dashboardRefreshRuntimeKey{}).(dashboardRefreshRuntime); ok && pinned.workspaceID == m.workspaceID && pinned.runtime != nil && pinned.resolutions != nil {
		id := strings.TrimSpace(dashboardID)
		pinned.resolutions.mu.Lock()
		defer pinned.resolutions.mu.Unlock()
		if resolved, ok := pinned.resolutions.values[id]; ok {
			return pinned.runtime, func() {}, resolved, nil
		}
		if err, ok := pinned.resolutions.errors[id]; ok {
			return nil, func() {}, dashboardresolver.Resolved{}, err
		}
		resolved, err := m.resolveOnRuntime(pinned.runtime, pinned.servingStateID, id)
		if err != nil {
			pinned.resolutions.errors[id] = err
			return nil, func() {}, dashboardresolver.Resolved{}, err
		}
		pinned.resolutions.values[id] = resolved
		return pinned.runtime, func() {}, resolved, nil
	}
	runtime, release, stateID, err := m.activeResolvedForDashboardRefreshRaw(ctx)
	if err != nil {
		return nil, func() {}, dashboardresolver.Resolved{}, err
	}
	resolved, err := m.resolveOnRuntime(runtime, stateID, dashboardID)
	if err != nil {
		if release != nil {
			release()
		}
		return nil, func() {}, dashboardresolver.Resolved{}, err
	}
	return runtime, release, resolved, nil
}
