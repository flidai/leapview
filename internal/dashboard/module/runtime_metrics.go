package module

import (
	"context"
	"fmt"
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
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
)

type runtimeMetrics struct {
	provider                   runtimehost.Provider
	projectID                  projectgraph.ResourceID
	publishedCompilationReader dashboardresolver.PublishedCompilationReader
}

// Catalog is the dashboard-owned catalog contract exposed through the module
// surface for application composition.
type Catalog = dashboard.Catalog

type dashboardRefreshRuntimeKey struct{}

type dashboardRefreshRuntime struct {
	projectID      projectgraph.ResourceID
	identity       projectgraph.ServingIdentity
	runtime        runtimehost.Runtime
	servingStateID string
	resolutions    *dashboardRefreshResolutionCache
}

type dashboardRefreshResolutionCache struct {
	mu     sync.Mutex
	values map[string]dashboardresolver.Resolved
	errors map[string]error
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
	SemanticModelByID(modelID projectgraph.ResourceID) (*semanticmodel.Model, bool)
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

type semanticPlannerRuntime interface {
	// Planner returns the narrow dashboard consumer port exposed by an active
	// runtime. Runtime metrics keeps the concrete planner return type for the
	// semantic explain APIs below, so Planner adapts that port at this boundary.
	Planner(modelID string) (consumer.Planner, bool)
}

func SupportsNativeArrow(metrics Metrics) bool {
	_, ok := metrics.(semanticArrowQueryRuntime)
	return ok
}

type RuntimeMetricsOptions struct {
	Provider                   runtimehost.Provider
	ProjectID                  projectgraph.ResourceID
	PublishedCompilationReader dashboardresolver.PublishedCompilationReader
}

func NewRuntimeMetrics(options RuntimeMetricsOptions) Metrics {
	return runtimeMetrics{
		provider:                   options.Provider,
		projectID:                  options.ProjectID,
		publishedCompilationReader: options.PublishedCompilationReader,
	}
}

// Resolver exposes the project/deployment dashboard source through the shared
// resolver boundary. The project is taken from the serving lease, rather than
// from startup composition, because a fresh install can bind its first
// project only when the first generation is activated.
func (m runtimeMetrics) Resolver() dashboardresolver.Resolver {
	return runtimeMetricsResolver{metrics: m}
}

type runtimeMetricsResolver struct {
	metrics runtimeMetrics
}

func (r runtimeMetricsResolver) Resolve(dashboardID projectgraph.ResourceID) (dashboardresolver.Resolved, error) {
	if r.metrics.provider == nil {
		return dashboardresolver.Resolved{}, fmt.Errorf("runtime provider is not configured")
	}
	lease, err := r.metrics.provider.Acquire(context.Background())
	if err != nil {
		return dashboardresolver.Resolved{}, err
	}
	defer lease.Release()
	runtime := lease.Runtime()
	identity, err := r.metrics.identityForLease(lease)
	if err != nil {
		return dashboardresolver.Resolved{}, err
	}
	return r.metrics.resolveOnRuntime(runtime, identity, dashboardID)
}

func runtimeResolverPort(runtime runtimehost.Runtime) (runtimeResolver, bool) {
	port, ok := runtime.(runtimeResolver)
	return port, ok
}

func (m runtimeMetrics) resolveOnRuntime(runtime runtimehost.Runtime, identity projectgraph.ServingIdentity, dashboardID projectgraph.ResourceID) (dashboardresolver.Resolved, error) {
	port, ok := runtimeResolverPort(runtime)
	if !ok {
		return dashboardresolver.Resolved{}, fmt.Errorf("active runtime does not provide dashboard resolver")
	}
	if err := identity.Validate(); err != nil {
		return dashboardresolver.Resolved{}, fmt.Errorf("active runtime serving identity is invalid: %w", err)
	}
	if configured := m.projectID; configured != "" && configured != identity.ProjectID {
		return dashboardresolver.Resolved{}, fmt.Errorf("active runtime project %q does not match configured project %q", identity.ProjectID, configured)
	}
	project, err := dashboardresolver.NewProject(port.Resolver(), identity, dashboardresolver.SourceMetadata{})
	if err != nil {
		return dashboardresolver.Resolved{}, err
	}
	var published dashboardresolver.Resolver
	if m.publishedCompilationReader != nil {
		compiled, err := dashboardresolver.NewPublishedCompilationResolver(identity, m.publishedCompilationReader, port)
		if err != nil {
			return dashboardresolver.Resolved{}, err
		}
		published, err = dashboardresolver.NewPublished(compiled, identity, dashboardresolver.SourceMetadata{})
		if err != nil {
			return dashboardresolver.Resolved{}, err
		}
	}
	composite, err := dashboardresolver.NewComposite(identity.ProjectID, project, published)
	if err != nil {
		return dashboardresolver.Resolved{}, err
	}
	return composite.Resolve(dashboardID)
}

func (m runtimeMetrics) identityForLease(lease runtimehost.Lease) (projectgraph.ServingIdentity, error) {
	if lease == nil {
		return projectgraph.ServingIdentity{}, fmt.Errorf("runtime lease is unavailable")
	}
	identity := lease.Identity()
	if err := identity.Validate(); err != nil {
		return projectgraph.ServingIdentity{}, fmt.Errorf("active runtime serving identity is invalid: %w", err)
	}
	if configured := m.projectID; configured != "" && configured != identity.ProjectID {
		return projectgraph.ServingIdentity{}, fmt.Errorf("active runtime project %q does not match configured project %q", identity.ProjectID, configured)
	}
	return identity, nil
}

type DynamicRuntimeMetricsOptions struct {
	Provider                   runtimehost.Provider
	ProjectID                  projectgraph.ResourceID
	PublishedCompilationReader dashboardresolver.PublishedCompilationReader
}

func NewDynamicRuntimeMetrics(options DynamicRuntimeMetricsOptions) Metrics {
	return NewRuntimeMetrics(RuntimeMetricsOptions{Provider: options.Provider, ProjectID: options.ProjectID, PublishedCompilationReader: options.PublishedCompilationReader})
}

func (m runtimeMetrics) Catalog() dashboard.Catalog {
	runtime, release, err := m.active(context.Background())
	if err != nil {
		return dashboard.Catalog{}
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
	if resolved.Source.Kind == dashboardresolver.SourceInstance {
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

func (m runtimeMetrics) Planner(modelID string) (consumer.Planner, bool) {
	runtime, release, err := m.active(context.Background())
	if err != nil {
		return nil, false
	}
	defer release()
	port, ok := runtime.(semanticPlannerRuntime)
	if !ok {
		return nil, false
	}
	plannerPort, ok := port.Planner(modelID)
	if !ok {
		return nil, false
	}
	return plannerPort, plannerPort != nil
}

func concretePlanner(value consumer.Planner) (*semanticquery.Planner, bool) {
	planner, ok := value.(*semanticquery.Planner)
	return planner, ok && planner != nil
}

func (m runtimeMetrics) DefaultFilters(dashboardID string) dashboard.Filters {
	runtime, release, resolved, err := m.activeResolved(context.Background(), dashboardID)
	if err != nil {
		return dashboard.Filters{}.WithDefaults()
	}
	defer release()
	if resolved.Source.Kind == dashboardresolver.SourceInstance {
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

// DefaultFiltersForDefinition returns the authored filter defaults embedded in
// a compiled dashboard definition. Agent-created visuals use this seam after
// their synthetic document has been compiled, ensuring the canonical runtime
// receives the same default state as an ordinary dashboard page.
func (m runtimeMetrics) DefaultFiltersForDefinition(definition dashboarddefinition.Definition) dashboard.Filters {
	runtime, release, err := m.active(context.Background())
	if err != nil {
		return dashboard.Filters{}.WithDefaults()
	}
	defer release()
	if port, ok := runtime.(definitionMetadataRuntime); ok {
		return port.DefaultFiltersForDefinition(definition)
	}
	return dashboard.Filters{}.WithDefaults()
}

func (m runtimeMetrics) NormalizeVisualizationWindow(dashboardID string, request dashboard.TableRequest) dashboard.TableRequest {
	runtime, release, resolved, err := m.activeResolved(context.Background(), dashboardID)
	if err != nil {
		return request.WithDefaults()
	}
	defer release()
	if resolved.Source.Kind == dashboardresolver.SourceInstance {
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
	if resolved.Source.Kind == dashboardresolver.SourceInstance {
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
	if resolved.Source.Kind == dashboardresolver.SourceInstance {
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
	if resolved.Source.Kind == dashboardresolver.SourceInstance {
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

// QueryVisualizationForDefinition executes a caller-supplied immutable
// dashboard definition through the active runtime's canonical visual query
// service. Agent-created visuals use this narrow port so they cannot rebuild
// report queries or bypass the serving generation.
func (m runtimeMetrics) QueryVisualizationForDefinition(ctx context.Context, definition dashboarddefinition.Definition, pageID string, filters dashboard.Filters, visualID string) (visualizationir.VisualizationEnvelope, error) {
	runtime, release, err := m.active(ctx)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	defer release()
	port, ok := runtime.(definitionVisualizationRuntime)
	if !ok {
		return visualizationir.VisualizationEnvelope{}, fmt.Errorf("active runtime does not provide compiled visualization data")
	}
	return port.QueryVisualizationForDefinition(ctx, definition, pageID, filters, visualID)
}

// QueryCompiledFilterOptionsForDefinition executes distinct options against a
// caller-supplied immutable definition. Builder previews use this seam so a
// draft filter can never fall back to the published dashboard resolver.
func (m runtimeMetrics) QueryCompiledFilterOptionsForDefinition(ctx context.Context, definition dashboarddefinition.Definition, query dashboardfilter.OptionQuery) (dashboardfilter.OptionResult, error) {
	runtime, release, err := m.active(ctx)
	if err != nil {
		return dashboardfilter.OptionResult{}, err
	}
	defer release()
	port, ok := runtime.(definitionFilterRuntime)
	if !ok {
		return dashboardfilter.OptionResult{}, fmt.Errorf("active runtime does not provide compiled filter options")
	}
	return port.QueryCompiledFilterOptionsForDefinition(ctx, definition, query)
}

func (m runtimeMetrics) QueryVisualizationWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	runtime, release, resolved, err := m.activeResolvedForDashboardRefresh(ctx, dashboardID)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	defer release()
	if resolved.Source.Kind == dashboardresolver.SourceInstance {
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

func (m runtimeMetrics) QueryVisualizationTile(ctx context.Context, dashboardID, visualID, revision string, zoom, x, y int) (dashboardruntime.SpatialTileResult, error) {
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
	if pinned, ok := ctx.Value(dashboardRefreshRuntimeKey{}).(dashboardRefreshRuntime); ok && m.pinnedProjectMatches(pinned) {
		return run(ctx)
	}
	runtime, release, identity, err := m.activeWithState(ctx)
	if err != nil {
		return err
	}
	defer release()
	ctx = context.WithValue(ctx, dashboardRefreshRuntimeKey{}, dashboardRefreshRuntime{
		projectID: identity.ProjectID, identity: identity, runtime: runtime, servingStateID: identity.GenerationID,
		resolutions: &dashboardRefreshResolutionCache{values: map[string]dashboardresolver.Resolved{}, errors: map[string]error{}},
	})
	return run(ctx)
}

func (m runtimeMetrics) activeForDashboardRefresh(ctx context.Context) (runtimehost.Runtime, func(), error) {
	runtime, release, _, _, err := m.activeResolvedForDashboardRefreshRaw(ctx)
	return runtime, release, err
}

func (m runtimeMetrics) activeResolvedForDashboardRefreshRaw(ctx context.Context) (runtimehost.Runtime, func(), string, projectgraph.ServingIdentity, error) {
	if pinned, ok := ctx.Value(dashboardRefreshRuntimeKey{}).(dashboardRefreshRuntime); ok && m.pinnedProjectMatches(pinned) {
		return pinned.runtime, func() {}, pinned.servingStateID, pinned.identity, nil
	}
	runtime, release, identity, err := m.activeWithState(ctx)
	return runtime, release, identity.GenerationID, identity, err
}

func (m runtimeMetrics) pinnedProjectMatches(pinned dashboardRefreshRuntime) bool {
	if pinned.runtime == nil || pinned.projectID == "" {
		return false
	}
	configured := m.projectID
	return configured == "" || configured == pinned.identity.ProjectID
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
	if err := request.ProjectID.Validate(); err != nil {
		return dataquery.Result{}, fmt.Errorf("project ID: %w", err)
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
	if err := request.ProjectID.Validate(); err != nil {
		return dataquery.Result{}, fmt.Errorf("project ID: %w", err)
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
	value, ok := m.Planner(modelID)
	planner, concrete := concretePlanner(value)
	if !ok || !concrete {
		return semanticquery.Plan{}, fmt.Errorf("compiled semantic planner for model %q is unavailable", modelID)
	}
	return planner.Plan(reportdef.SemanticAggregateRequest(request))
}

func (m runtimeMetrics) ExplainSemanticPreview(modelID string, request reportdef.RowQuery) (semanticquery.Plan, error) {
	value, ok := m.Planner(modelID)
	planner, concrete := concretePlanner(value)
	if !ok || !concrete {
		return semanticquery.Plan{}, fmt.Errorf("compiled semantic planner for model %q is unavailable", modelID)
	}
	return planner.PlanRows(reportdef.SemanticRowRequest(request))
}

func (m runtimeMetrics) Pages(dashboardID string) []dashboard.Page {
	runtime, release, resolved, err := m.activeResolved(context.Background(), dashboardID)
	if err != nil {
		return nil
	}
	defer release()
	if resolved.Source.Kind == dashboardresolver.SourceInstance {
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

func (m runtimeMetrics) RuntimeReady(ctx context.Context, projectID projectgraph.ResourceID) error {
	if err := projectID.Validate(); err != nil {
		return fmt.Errorf("project ID: %w", err)
	}
	if m.projectID != "" && m.projectID != projectID {
		return fmt.Errorf("configured project %q does not match requested project %q", m.projectID, projectID)
	}
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
	if catalog.Project.ID != "" && catalog.Project.ID != projectID {
		return fmt.Errorf("catalog project = %q, want %q", catalog.Project.ID, projectID)
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
	defaultDashboardResourceID, err := projectgraph.NewResourceID(defaultDashboardID)
	if err != nil {
		return fmt.Errorf("default dashboard ID: %w", err)
	}
	resolved, err := reportPort.Resolver().Resolve(defaultDashboardResourceID)
	if err != nil {
		return reportMetadataReady(catalogPort, defaultDashboardID, dashboarddefinition.Definition{}, nil, false)
	}
	return reportMetadataReady(catalogPort, defaultDashboardID, resolved.Definition, resolved.Model, true)
}

func (m runtimeMetrics) active(ctx context.Context) (runtimehost.Runtime, func(), error) {
	runtime, release, _, err := m.activeWithState(ctx)
	return runtime, release, err
}

func (m runtimeMetrics) activeWithState(ctx context.Context) (runtimehost.Runtime, func(), projectgraph.ServingIdentity, error) {
	if m.provider == nil {
		return nil, func() {}, projectgraph.ServingIdentity{}, fmt.Errorf("runtime provider is not configured")
	}
	lease, err := m.provider.Acquire(ctx)
	if err != nil {
		return nil, func() {}, projectgraph.ServingIdentity{}, err
	}
	identity, err := m.identityForLease(lease)
	if err != nil {
		lease.Release()
		return nil, func() {}, projectgraph.ServingIdentity{}, err
	}
	return lease.Runtime(), lease.Release, identity, nil
}

func (m runtimeMetrics) activeResolved(ctx context.Context, dashboardID string) (runtimehost.Runtime, func(), dashboardresolver.Resolved, error) {
	runtime, release, identity, err := m.activeWithState(ctx)
	if err != nil {
		return nil, func() {}, dashboardresolver.Resolved{}, err
	}
	dashboardResourceID, err := projectgraph.NewResourceID(dashboardID)
	if err != nil {
		release()
		return nil, func() {}, dashboardresolver.Resolved{}, err
	}
	resolved, err := m.resolveOnRuntime(runtime, identity, dashboardResourceID)
	if err != nil {
		release()
		return nil, func() {}, dashboardresolver.Resolved{}, err
	}
	return runtime, release, resolved, nil
}

func (m runtimeMetrics) activeResolvedForDashboardRefresh(ctx context.Context, dashboardID string) (runtimehost.Runtime, func(), dashboardresolver.Resolved, error) {
	if pinned, ok := ctx.Value(dashboardRefreshRuntimeKey{}).(dashboardRefreshRuntime); ok && m.pinnedProjectMatches(pinned) && pinned.resolutions != nil {
		id := dashboardID
		dashboardResourceID, err := projectgraph.NewResourceID(id)
		if err != nil {
			return nil, func() {}, dashboardresolver.Resolved{}, err
		}
		pinned.resolutions.mu.Lock()
		defer pinned.resolutions.mu.Unlock()
		if resolved, ok := pinned.resolutions.values[id]; ok {
			return pinned.runtime, func() {}, resolved, nil
		}
		if err, ok := pinned.resolutions.errors[id]; ok {
			return nil, func() {}, dashboardresolver.Resolved{}, err
		}
		resolved, err := m.resolveOnRuntime(pinned.runtime, pinned.identity, dashboardResourceID)
		if err != nil {
			pinned.resolutions.errors[id] = err
			return nil, func() {}, dashboardresolver.Resolved{}, err
		}
		pinned.resolutions.values[id] = resolved
		return pinned.runtime, func() {}, resolved, nil
	}
	runtime, release, _, identity, err := m.activeResolvedForDashboardRefreshRaw(ctx)
	if err != nil {
		return nil, func() {}, dashboardresolver.Resolved{}, err
	}
	dashboardResourceID, err := projectgraph.NewResourceID(dashboardID)
	if err != nil {
		if release != nil {
			release()
		}
		return nil, func() {}, dashboardresolver.Resolved{}, err
	}
	resolved, err := m.resolveOnRuntime(runtime, identity, dashboardResourceID)
	if err != nil {
		if release != nil {
			release()
		}
		return nil, func() {}, dashboardresolver.Resolved{}, err
	}
	return runtime, release, resolved, nil
}
