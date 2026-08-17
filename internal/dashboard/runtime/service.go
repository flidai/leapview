package runtime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type DataRuntimeFactory interface {
	OpenDashboardProjectDataRuntimes(ctx context.Context, config ProjectDataRuntimeConfig) (map[projectgraph.ResourceID]DataRuntime, error)
}

type ProjectDataRuntimeConfig struct {
	Definition *ProjectDefinition
	DBDir      string
}

type DataRuntime interface {
	reportdef.DataService
	ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error)
	Refresh(ctx context.Context) error
	Close() error
	LastRefresh() time.Time
}

type DataRuntimeSnapshot interface {
	DuckLakeSnapshotID() int64
}

type DataRuntimeReadConcurrency interface {
	ReadConcurrency() int
}

type DataRuntimeSemanticVerifier interface {
	VerifySemantic(context.Context) error
}

// DataRuntimePlanner exposes the immutable planner compiled for the active
// serving generation. Dashboard optimization must consume this port instead
// of compiling a second planner from the model projection.
type DataRuntimePlanner interface {
	Planner() consumer.Planner
}

type setupRequiredError interface {
	SetupRequired() bool
}

type Service struct {
	mu             sync.RWMutex
	identity       projectgraph.ServingIdentity
	runtimes       map[projectgraph.ResourceID]*modelRuntime
	catalog        *CatalogService
	reports        *ReportService
	queries        *QueryService
	filters        *FilterService
	visualizations *VisualizationDataService
	snapshots      *SnapshotService
	tiles          *spatialTileRegistry
}

type modelRuntime struct {
	model     *semanticmodel.Model
	optimizer *consumer.Optimizer
	data      DataRuntime
	ready     bool
	missing   error
}

// definitionService creates a query service view over an already compiled
// dashboard definition. Instance-managed dashboards are immutable compiler
// output and must execute through the same model/data runtimes as project
// dashboards; rebuilding another runtime or opening another data runtime here
// would violate that boundary. The view only replaces report metadata while
// retaining this service's model runtimes, locks, filters, and visual cache.
// It is intentionally unexported and consumed immediately by the
// ForDefinition methods below; do not return it from a public API or call
// lifecycle methods such as Close on the view, since it aliases the base
// service's data runtimes.
func (m *Service) definitionService(definition dashboarddefinition.Definition) (*Service, error) {
	if m == nil || m.reports == nil {
		return nil, fmt.Errorf("dashboard runtime is unavailable")
	}
	definition.ID = strings.TrimSpace(definition.ID)
	definition.SemanticModel = strings.TrimSpace(definition.SemanticModel)
	if definition.ID == "" || definition.SemanticModel == "" {
		return nil, fmt.Errorf("compiled dashboard requires ID and semantic model")
	}
	dashboardID, err := projectgraph.NewResourceID(definition.ID)
	if err != nil {
		return nil, fmt.Errorf("compiled dashboard id: %w", err)
	}
	modelID, err := projectgraph.NewResourceID(definition.SemanticModel)
	if err != nil {
		return nil, fmt.Errorf("compiled dashboard semantic model: %w", err)
	}
	runtime, ok := m.runtimes[modelID]
	if !ok || runtime == nil {
		return nil, fmt.Errorf("unknown semantic model %q", definition.SemanticModel)
	}
	if runtime.model == nil {
		return nil, fmt.Errorf("semantic model %q does not match compiled dashboard", definition.SemanticModel)
	}
	models := make(map[projectgraph.ResourceID]*semanticmodel.Model, len(m.reports.models)+1)
	for modelID, model := range m.reports.models {
		models[modelID] = model
	}
	models[modelID] = runtime.model
	reports := &ReportService{projectID: m.identity.ProjectID, identity: m.identity, models: models,
		dashboards: map[projectgraph.ResourceID]dashboarddefinition.Definition{dashboardID: definition},
		catalog:    m.reports.catalog, defaultID: definition.ID}
	visualizations := *m.visualizations
	visualizations.reports = reports
	visualizations.runtimes = m.runtimes
	snapshots := *m.snapshots
	snapshots.reports = reports
	snapshots.visualizations = &visualizations
	queries := &QueryService{snapshots: &snapshots, visualizations: &visualizations}
	return &Service{
		// Keep the execution view's lock zero-valued: query paths use the
		// pointer-bearing Snapshot/Visualization services below, which retain
		// the original service lock without copying a live mutex.
		identity: m.identity, runtimes: m.runtimes, catalog: m.catalog, reports: reports,
		queries: queries, filters: m.filters, visualizations: &visualizations,
		snapshots: &snapshots, tiles: m.tiles,
	}, nil
}

func NewFromGeneration(ctx context.Context, duckDBDir string, factory DataRuntimeFactory, identity projectgraph.ServingIdentity, definition *ProjectDefinition) (*Service, error) {
	if err := identity.Validate(); err != nil {
		return nil, fmt.Errorf("project serving identity: %w", err)
	}
	if definition == nil || definition.ProjectID() != identity.ProjectID {
		return nil, fmt.Errorf("project generation definition does not match serving identity")
	}
	if factory == nil {
		return nil, fmt.Errorf("dashboard data runtime factory is required")
	}
	service, err := newFromDefinition(ctx, duckDBDir, factory, identity, definition)
	if err != nil {
		return nil, err
	}
	service.identity = identity
	return service, nil
}

// Identity returns the immutable project generation this runtime executes.
func (m *Service) Identity() projectgraph.ServingIdentity {
	if m == nil {
		return projectgraph.ServingIdentity{}
	}
	return m.identity
}

func (m *Service) ProjectIdentity() projectgraph.ResourceID { return m.Identity().ProjectID }

func newFromDefinition(ctx context.Context, duckDBDir string, factory DataRuntimeFactory, identity projectgraph.ServingIdentity, definition *ProjectDefinition) (*Service, error) {
	if err := definition.Validate(); err != nil {
		return nil, err
	}
	service := &Service{
		identity: identity,
		runtimes: map[projectgraph.ResourceID]*modelRuntime{}, tiles: newSpatialTileRegistry(),
	}
	var err error
	service.catalog, err = NewCatalogService(&service.mu, definition)
	if err != nil {
		return nil, err
	}
	service.reports = &ReportService{projectID: definition.ProjectID(), identity: identity,
		models:     make(map[projectgraph.ResourceID]*semanticmodel.Model, len(definition.Models())),
		dashboards: make(map[projectgraph.ResourceID]dashboarddefinition.Definition, len(definition.Dashboards())),
		catalog:    service.catalog.catalog}
	for modelID, model := range definition.Models() {
		service.reports.models[modelID] = model
	}
	for dashboardID, dashboard := range definition.Dashboards() {
		service.reports.dashboards[dashboardID] = dashboard
		if service.reports.defaultID == "" {
			service.reports.defaultID = dashboardID.String()
		}
	}
	service.filters = &FilterService{}
	service.visualizations = &VisualizationDataService{
		mu:       &service.mu,
		reports:  service.reports,
		runtimes: service.runtimes,
		filters:  service.filters,
		tiles:    service.tiles,
	}
	service.snapshots = &SnapshotService{
		mu:             &service.mu,
		reports:        service.reports,
		runtimes:       service.runtimes,
		filters:        service.filters,
		visualizations: service.visualizations,
	}
	service.queries = &QueryService{
		snapshots:      service.snapshots,
		visualizations: service.visualizations,
	}
	for modelID, model := range definition.Models() {
		service.runtimes[modelID] = &modelRuntime{model: model}
	}
	dataRuntimes, err := factory.OpenDashboardProjectDataRuntimes(ctx, ProjectDataRuntimeConfig{
		Definition: definition,
		DBDir:      duckDBDir,
	})
	if err != nil {
		if setupRequired(err) {
			for _, runtime := range service.runtimes {
				runtime.missing = err
			}
			return service, nil
		}
		return nil, err
	}
	for modelID, runtime := range service.runtimes {
		dataRuntime, ok := dataRuntimes[modelID]
		if !ok {
			return nil, fmt.Errorf("project data runtime missing semantic model %q", modelID)
		}
		plannerPort, ok := dataRuntime.(DataRuntimePlanner)
		if !ok || plannerPort.Planner() == nil {
			return nil, fmt.Errorf("semantic model %q runtime does not provide compiled planner", modelID)
		}
		optimizer, err := consumer.NewOptimizerFromPlanner(plannerPort.Planner())
		if err != nil {
			return nil, fmt.Errorf("bind semantic model %q planner: %w", modelID, err)
		}
		runtime.data = newGovernedDataRuntime(identity.ProjectID, modelID, dataRuntime)
		runtime.optimizer = optimizer
		runtime.ready = true
	}
	for modelID := range dataRuntimes {
		if _, ok := service.runtimes[modelID]; !ok {
			return nil, fmt.Errorf("project data runtime returned unknown semantic model %q", modelID)
		}
	}
	return service, nil
}

// Planner returns the activation-owned planner for one semantic model.
func (m *Service) Planner(modelID string) (consumer.Planner, bool) {
	if m == nil {
		return nil, false
	}
	id, err := projectgraph.NewResourceID(modelID)
	if err != nil {
		return nil, false
	}
	m.mu.RLock()
	runtime, ok := m.runtimes[id]
	m.mu.RUnlock()
	if !ok || runtime == nil || runtime.data == nil {
		return nil, false
	}
	port, ok := runtime.data.(DataRuntimePlanner)
	if !ok {
		return nil, false
	}
	planner := port.Planner()
	return planner, planner != nil
}

func (m *Service) Close() error {
	var closeErr error
	for _, runtime := range m.runtimes {
		if runtime.data == nil {
			continue
		}
		if err := runtime.data.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}

func (m *Service) Verify(ctx context.Context) error {
	if m == nil {
		return fmt.Errorf("dashboard runtime is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.RLock()
	runtimes := make(map[projectgraph.ResourceID]*modelRuntime, len(m.runtimes))
	modelIDs := make([]projectgraph.ResourceID, 0, len(m.runtimes))
	for modelID, runtime := range m.runtimes {
		modelIDs = append(modelIDs, modelID)
		runtimes[modelID] = runtime
	}
	m.mu.RUnlock()
	sort.Slice(modelIDs, func(i, j int) bool { return modelIDs[i] < modelIDs[j] })
	for _, modelID := range modelIDs {
		runtime := runtimes[modelID]
		if runtime == nil || !runtime.ready || runtime.data == nil {
			if runtime != nil && runtime.missing != nil {
				return fmt.Errorf(
					"semantic model %q is unavailable: %w",
					modelID,
					runtime.missing,
				)
			}
			return fmt.Errorf("semantic model %q is unavailable", modelID)
		}
		verifier, ok := runtime.data.(DataRuntimeSemanticVerifier)
		if !ok {
			return fmt.Errorf("semantic model %q does not support semantic verification", modelID)
		}
		if err := verifier.VerifySemantic(ctx); err != nil {
			return fmt.Errorf("semantic model %q verification failed: %w", modelID, err)
		}
	}
	return nil
}

func (m *Service) DuckLakeSnapshotID() int64 {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var snapshotID int64
	for _, runtime := range m.runtimes {
		if runtime == nil || runtime.data == nil {
			continue
		}
		snapshot, ok := runtime.data.(DataRuntimeSnapshot)
		if !ok {
			continue
		}
		current := snapshot.DuckLakeSnapshotID()
		if current == 0 {
			continue
		}
		if snapshotID == 0 {
			snapshotID = current
			continue
		}
		if snapshotID != current {
			return 0
		}
	}
	return snapshotID
}

func (m *Service) DashboardTargetConcurrency() int {
	if m == nil || m.DuckLakeSnapshotID() <= 0 {
		return 1
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	limit := 0
	for _, runtime := range m.runtimes {
		if runtime == nil || !runtime.ready || runtime.data == nil {
			continue
		}
		capability, ok := runtime.data.(DataRuntimeReadConcurrency)
		if !ok || capability.ReadConcurrency() <= 1 {
			return 1
		}
		if limit == 0 || capability.ReadConcurrency() < limit {
			limit = capability.ReadConcurrency()
		}
	}
	return max(1, limit)
}

func setupRequired(err error) bool {
	var setup setupRequiredError
	return errors.As(err, &setup) && setup.SetupRequired()
}
