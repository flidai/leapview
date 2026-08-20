// Package analyticsruntime adapts the analytics capability's governed
// project runtime to dashboard-owned data interfaces.
package analyticsruntime

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/catalogstats"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	analyticscontract "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type Options struct {
	Projects                 analyticscontract.ProjectFactory
	ResultLimits             dataquery.ResultLimits
	SnapshotID               int64
	ServingStateID           string
	ProjectID                projectgraph.ResourceID
	Environment              string
	SemanticModelDigest      string
	ArtifactDigest           string
	SourceDataDigest         string
	CandidateID              string
	AuthorizationFingerprint string
	BindingFingerprint       string
	SkipInitialRefresh       bool
}

type Factory struct{ options Options }

func NewFactory(options Options) Factory { return Factory{options: options} }

func (f Factory) OpenDashboardProjectDataRuntimes(ctx context.Context, config dashboardruntime.ProjectDataRuntimeConfig) (map[projectgraph.ResourceID]dashboardruntime.DataRuntime, error) {
	if config.Definition == nil {
		return nil, fmt.Errorf("project definition is required")
	}
	options := f.options
	if options.Projects == nil {
		return nil, fmt.Errorf("analytical project factory is unavailable")
	}
	models := make(map[string]*semanticmodel.Model, len(config.Definition.Models()))
	for id, model := range config.Definition.Models() {
		models[id.String()] = model
	}
	runtime, err := options.Projects.OpenProject(ctx, analyticscontract.ProjectRequest{
		Models: models, SnapshotID: options.SnapshotID,
		RequiredExtensions: requiredProjectExtensions(config.Definition),
		ResultLimits:       options.ResultLimits,
		ServingStateID:     options.ServingStateID, ProjectID: options.ProjectID, Environment: options.Environment,
		SemanticDigest: options.SemanticModelDigest, ArtifactDigest: options.ArtifactDigest, SourceDataDigest: options.SourceDataDigest,
		CandidateID: options.CandidateID, AuthorizationFingerprint: options.AuthorizationFingerprint,
		BindingFingerprint: options.BindingFingerprint,
		SkipInitialRefresh: options.SkipInitialRefresh,
	})
	if err != nil {
		return nil, err
	}
	sharedClose := &sharedCloser{runtime: runtime}
	runtimes := make(map[projectgraph.ResourceID]dashboardruntime.DataRuntime, len(config.Definition.Models()))
	for id := range config.Definition.Models() {
		modelID := id.String()
		runtimes[id] = projectRuntime{modelID: modelID, runtime: runtime, close: sharedClose, data: reportdef.NewDataQueryService(options.ProjectID, modelID, runtime)}
	}
	return runtimes, nil
}

func requiredProjectExtensions(definition *dashboardruntime.ProjectDefinition) []string {
	if definition == nil {
		return nil
	}
	for _, dashboard := range definition.Dashboards() {
		for _, visual := range dashboard.Visualizations {
			if visual.Query.Spatial != nil && visual.Query.Spatial.Tiles != nil {
				return []string{"spatial"}
			}
		}
	}
	return nil
}

type sharedCloser struct {
	once    sync.Once
	runtime analyticscontract.Project
	err     error
}

func (c *sharedCloser) Close() error {
	c.once.Do(func() { c.err = c.runtime.Close() })
	return c.err
}

type projectRuntime struct {
	modelID string
	runtime analyticscontract.Project
	close   *sharedCloser
	data    reportdef.DataService
}

func (r projectRuntime) Query(ctx context.Context, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	return r.data.Query(ctx, request)
}
func (r projectRuntime) Rows(ctx context.Context, request reportdef.RowQuery) (reportdef.QueryRows, error) {
	return r.data.Rows(ctx, request)
}
func (r projectRuntime) Count(ctx context.Context, request reportdef.CountQuery) (int, error) {
	return r.data.Count(ctx, request)
}
func (r projectRuntime) Histogram(ctx context.Context, request reportdef.RawValueQuery, bins int) ([]reportdef.HistogramBin, error) {
	return r.data.Histogram(ctx, request, bins)
}
func (r projectRuntime) Distribution(ctx context.Context, request reportdef.RawValueQuery, sort []reportdef.QuerySort, limit int) (reportdef.QueryRows, error) {
	return r.data.Distribution(ctx, request, sort, limit)
}
func (r projectRuntime) ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
	return r.runtime.ExecuteDataQuery(ctx, request)
}

func (r projectRuntime) CatalogTableStatistics(ctx context.Context) ([]catalogstats.Table, error) {
	reader, ok := r.runtime.(catalogstats.Reader)
	if !ok {
		return nil, fmt.Errorf("analytical project runtime does not expose catalog statistics")
	}
	return reader.CatalogTableStatistics(ctx)
}
func (r projectRuntime) ExecuteDataQueryArrow(ctx context.Context, request dataquery.Query, sink arrowquery.Sink) (dataquery.Result, error) {
	return r.runtime.ExecuteDataQueryArrow(ctx, request, sink)
}
func (r projectRuntime) ExecuteDataQueryBundle(ctx context.Context, requests []dataquery.BundleRequest) (dataquery.BundleResult, error) {
	return r.runtime.ExecuteDataQueryBundle(ctx, requests)
}
func (r projectRuntime) Refresh(ctx context.Context) error { return r.runtime.Refresh(ctx) }
func (r projectRuntime) RefreshTables(ctx context.Context, tables []string) error {
	return r.runtime.RefreshModelTables(ctx, r.modelID, tables)
}
func (r projectRuntime) VerifySemantic(ctx context.Context) error {
	verifier, ok := r.runtime.(interface {
		VerifySemantic(context.Context, string) error
	})
	if !ok {
		return fmt.Errorf("analytical project runtime does not support semantic verification")
	}
	return verifier.VerifySemantic(ctx, r.modelID)
}
func (r projectRuntime) Close() error              { return r.close.Close() }
func (r projectRuntime) LastRefresh() time.Time    { return r.runtime.LastRefresh() }
func (r projectRuntime) DuckLakeSnapshotID() int64 { return r.runtime.DuckLakeSnapshotID() }
func (r projectRuntime) ReadConcurrency() int      { return r.runtime.ReadConcurrency() }

func (r projectRuntime) Planner() consumer.Planner {
	provider, ok := r.runtime.(interface {
		Planner(string) (*semanticquery.Planner, bool)
	})
	if !ok {
		return nil
	}
	planner, ok := provider.Planner(r.modelID)
	if !ok {
		return nil
	}
	return planner
}
