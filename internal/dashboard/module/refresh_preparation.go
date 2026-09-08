package module

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/command"
	"github.com/flidai/leapview/internal/dashboard/publication"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// preparePublicDashboardRefresh prepares only metadata. The caller must resolve
// the publication through its authority, establish PublicationExecutionContext,
// and retain the decorated refresh lease through normal consumer execution.
// It intentionally has no fallback to an unleased runtime or query executor.
func preparePublicDashboardRefresh(ctx context.Context, row publication.Publication) (command.Request, command.PreparedRefresh, error) {
	fail := func(err error) (command.Request, command.PreparedRefresh, error) {
		return command.Request{}, command.PreparedRefresh{}, err
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	pinned, ok := ctx.Value(dashboardRefreshRuntimeKey{}).(dashboardRefreshRuntime)
	if !ok || !pinned.metrics.pinnedProjectMatches(pinned) {
		return fail(fmt.Errorf("dashboard preparation requires a refresh lease"))
	}
	if row.Status() != publication.StatusActive || row.ProjectID != pinned.identity.ProjectID || row.ServingStateID != pinned.servingStateID {
		return fail(fmt.Errorf("publication does not match the leased serving generation"))
	}
	metrics := pinned.metrics
	if metrics.publishedCompilationReader != nil {
		metrics.publishedCompilationReader = preparationCompilationReader{ctx: ctx, reader: metrics.publishedCompilationReader}
	}
	runtime, release, resolved, err := metrics.activeResolvedForDashboardRefresh(ctx, row.Dashboard)
	if err != nil {
		return fail(err)
	}
	defer release()
	page, ok := resolved.Definition.PageOrDefault(row.DefaultPage)
	if !ok || (row.DefaultPage != "" && page.ID != row.DefaultPage) {
		return fail(fmt.Errorf("publication default page is unavailable"))
	}
	var normalize func(dashboard.TableRequest) dashboard.TableRequest
	if resolved.Source.Kind == dashboardresolver.SourceInstance {
		port, ok := runtime.(definitionVisualizationRuntime)
		if !ok {
			return fail(fmt.Errorf("leased runtime cannot prepare published visualization windows"))
		}
		normalize = func(request dashboard.TableRequest) dashboard.TableRequest {
			return port.NormalizeVisualizationWindowForDefinition(resolved.Definition, request)
		}
	} else {
		port, ok := runtime.(visualizationRuntime)
		if !ok {
			return fail(fmt.Errorf("leased runtime cannot prepare visualization windows"))
		}
		normalize = func(request dashboard.TableRequest) dashboard.TableRequest {
			return port.NormalizeVisualizationWindow(row.Dashboard, request)
		}
	}
	// Match PublicDashboardUpdates with an empty URL query, including compiled
	// filter state. The command service remains the sole target-plan algorithm.
	filters := resolved.Definition.NormalizeFiltersForPage(page.ID, resolved.Definition.FiltersFromURLForPage(page.ID, nil))
	state, err := resolved.Definition.FilterStateFromURL(page.ID, nil)
	if err != nil {
		return fail(err)
	}
	filters.CompiledState = &state
	request := command.Request{DashboardID: row.Dashboard, PageID: page.ID, ModelID: resolved.Definition.SemanticModel}
	prepared, err := (command.Service{Metrics: refreshPreparationMetrics{resolved: resolved, normalize: normalize}}).PrepareInitial(request, filters)
	return request, prepared, err
}

// The synchronous resolver passes Background; bind its existing reader to the
// attempt deadline without introducing another resolution path.
type preparationCompilationReader struct {
	ctx    context.Context
	reader dashboardresolver.PublishedCompilationReader
}

func (r preparationCompilationReader) GetPublishedCompilation(_ context.Context, project projectgraph.ResourceID, dashboardID authoring.DashboardID) (authoring.CompiledRevision, error) {
	return r.reader.GetPublishedCompilation(r.ctx, project, dashboardID)
}

// This view implements preparation metadata only; it cannot execute queries.
type refreshPreparationMetrics struct {
	resolved  dashboardresolver.Resolved
	normalize func(dashboard.TableRequest) dashboard.TableRequest
}

func (m refreshPreparationMetrics) Resolver() dashboardresolver.Resolver { return m }
func (m refreshPreparationMetrics) Resolve(id projectgraph.ResourceID) (dashboardresolver.Resolved, error) {
	if id.String() != m.resolved.Definition.ID {
		return dashboardresolver.Resolved{}, dashboardresolver.ErrNotFound
	}
	return m.resolved, nil
}
func (m refreshPreparationMetrics) DefaultFilters(string) dashboard.Filters {
	return m.resolved.Definition.DefaultFilters()
}
func (m refreshPreparationMetrics) NormalizeVisualizationWindow(_ string, request dashboard.TableRequest) dashboard.TableRequest {
	return m.normalize(request)
}
