// Package analyticsruntime adapts the analytics capability's governed
// workspace runtime to dashboard-owned data interfaces.
package analyticsruntime

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	analyticscontract "github.com/flidai/leapview/internal/analytics/runtime"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
)

type Options struct {
	Workspaces               analyticscontract.WorkspaceFactory
	ResultLimits             dataquery.ResultLimits
	SnapshotID               int64
	ServingStateID           string
	WorkspaceID              string
	Environment              string
	SemanticModelDigest      string
	ArtifactDigest           string
	SourceDataDigest         string
	CandidateID              string
	AuthorizationFingerprint string
	BindingFingerprint       string
}

type Factory struct{ options Options }

func NewFactory(options Options) Factory { return Factory{options: options} }

func (f Factory) OpenDashboardWorkspaceDataRuntimes(ctx context.Context, config dashboardruntime.WorkspaceDataRuntimeConfig) (map[string]dashboardruntime.DataRuntime, error) {
	if config.Definition == nil {
		return nil, fmt.Errorf("workspace definition is required")
	}
	options := f.options
	if options.Workspaces == nil {
		return nil, fmt.Errorf("analytical workspace factory is unavailable")
	}
	runtime, err := options.Workspaces.OpenWorkspace(ctx, analyticscontract.WorkspaceRequest{
		Models: config.Definition.Models, SnapshotID: options.SnapshotID,
		RequiredExtensions: requiredWorkspaceExtensions(config.Definition),
		ResultLimits:       options.ResultLimits,
		ServingStateID:     options.ServingStateID, WorkspaceID: options.WorkspaceID, Environment: options.Environment,
		SemanticDigest: options.SemanticModelDigest, ArtifactDigest: options.ArtifactDigest, SourceDataDigest: options.SourceDataDigest,
		CandidateID: options.CandidateID, AuthorizationFingerprint: options.AuthorizationFingerprint,
		BindingFingerprint: options.BindingFingerprint,
	})
	if err != nil {
		return nil, err
	}
	sharedClose := &sharedCloser{runtime: runtime}
	runtimes := make(map[string]dashboardruntime.DataRuntime, len(config.Definition.Models))
	for modelID := range config.Definition.Models {
		runtimes[modelID] = workspaceRuntime{modelID: modelID, runtime: runtime, close: sharedClose, data: reportdef.NewDataQueryService(modelID, runtime)}
	}
	return runtimes, nil
}

func requiredWorkspaceExtensions(definition *dashboarddefinition.Workspace) []string {
	for _, dashboard := range definition.Dashboards {
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
	runtime analyticscontract.Workspace
	err     error
}

func (c *sharedCloser) Close() error {
	c.once.Do(func() { c.err = c.runtime.Close() })
	return c.err
}

type workspaceRuntime struct {
	modelID string
	runtime analyticscontract.Workspace
	close   *sharedCloser
	data    reportdef.DataService
}

func (r workspaceRuntime) Query(ctx context.Context, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	return r.data.Query(ctx, request)
}
func (r workspaceRuntime) Rows(ctx context.Context, request reportdef.RowQuery) (reportdef.QueryRows, error) {
	return r.data.Rows(ctx, request)
}
func (r workspaceRuntime) Count(ctx context.Context, request reportdef.CountQuery) (int, error) {
	return r.data.Count(ctx, request)
}
func (r workspaceRuntime) Histogram(ctx context.Context, request reportdef.RawValueQuery, bins int) ([]reportdef.HistogramBin, error) {
	return r.data.Histogram(ctx, request, bins)
}
func (r workspaceRuntime) Distribution(ctx context.Context, request reportdef.RawValueQuery, sort []reportdef.QuerySort, limit int) (reportdef.QueryRows, error) {
	return r.data.Distribution(ctx, request, sort, limit)
}
func (r workspaceRuntime) ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
	return r.runtime.ExecuteDataQuery(ctx, request)
}
func (r workspaceRuntime) ExecuteDataQueryArrow(ctx context.Context, request dataquery.Query, sink arrowquery.Sink) (dataquery.Result, error) {
	return r.runtime.ExecuteDataQueryArrow(ctx, request, sink)
}
func (r workspaceRuntime) ExecuteDataQueryBundle(ctx context.Context, requests []dataquery.BundleRequest) (dataquery.BundleResult, error) {
	return r.runtime.ExecuteDataQueryBundle(ctx, requests)
}
func (r workspaceRuntime) Refresh(ctx context.Context) error { return r.runtime.Refresh(ctx) }
func (r workspaceRuntime) RefreshTables(ctx context.Context, tables []string) error {
	return r.runtime.RefreshModelTables(ctx, r.modelID, tables)
}
func (r workspaceRuntime) Close() error              { return r.close.Close() }
func (r workspaceRuntime) LastRefresh() time.Time    { return r.runtime.LastRefresh() }
func (r workspaceRuntime) DuckLakeSnapshotID() int64 { return r.runtime.DuckLakeSnapshotID() }
func (r workspaceRuntime) ReadConcurrency() int      { return r.runtime.ReadConcurrency() }
