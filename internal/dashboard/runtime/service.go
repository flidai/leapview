package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
)

type DataRuntimeFactory interface {
	OpenDashboardWorkspaceDataRuntimes(ctx context.Context, config WorkspaceDataRuntimeConfig) (map[string]DataRuntime, error)
}

type WorkspaceDataRuntimeConfig struct {
	Definition *dashboarddefinition.Workspace
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

type setupRequiredError interface {
	SetupRequired() bool
}

type Service struct {
	mu             sync.RWMutex
	runtimes       map[string]*modelRuntime
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

func NewFromDefinition(ctx context.Context, duckDBDir string, factory DataRuntimeFactory, definition *dashboarddefinition.Workspace) (*Service, error) {
	if factory == nil {
		return nil, fmt.Errorf("dashboard data runtime factory is required")
	}
	if definition == nil {
		return nil, fmt.Errorf("workspace definition is required")
	}
	return newFromDefinition(ctx, duckDBDir, factory, definition)
}

func newFromDefinition(ctx context.Context, duckDBDir string, factory DataRuntimeFactory, definition *dashboarddefinition.Workspace) (*Service, error) {
	service := &Service{
		runtimes: map[string]*modelRuntime{}, tiles: newSpatialTileRegistry(),
	}
	service.catalog = NewCatalogService(&service.mu, definition)
	service.reports = &ReportService{
		workspace: definition,
		defaultID: definition.Catalog.Dashboards[0].ID,
	}
	service.filters = &FilterService{}
	service.visualizations = &VisualizationDataService{
		mu:          &service.mu,
		reports:     service.reports,
		runtimes:    service.runtimes,
		filters:     service.filters,
		tiles:       service.tiles,
		workspaceID: definition.Catalog.Workspace.ID,
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
	for modelID, model := range definition.Models {
		optimizer, err := consumer.NewOptimizer(model)
		if err != nil {
			return nil, fmt.Errorf("compile semantic model %q: %w", modelID, err)
		}
		service.runtimes[modelID] = &modelRuntime{model: model, optimizer: optimizer}
	}
	dataRuntimes, err := factory.OpenDashboardWorkspaceDataRuntimes(ctx, WorkspaceDataRuntimeConfig{
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
			return nil, fmt.Errorf("workspace data runtime missing semantic model %q", modelID)
		}
		runtime.data = newGovernedDataRuntime(definition.Catalog.Workspace.ID, modelID, dataRuntime)
		runtime.ready = true
	}
	for modelID := range dataRuntimes {
		if _, ok := service.runtimes[modelID]; !ok {
			return nil, fmt.Errorf("workspace data runtime returned unknown semantic model %q", modelID)
		}
	}
	return service, nil
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
	defer m.mu.RUnlock()
	for modelID, runtime := range m.runtimes {
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
