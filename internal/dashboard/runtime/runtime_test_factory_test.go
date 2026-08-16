package runtime

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	analyticsduckdb "github.com/flidai/leapview/internal/analytics/duckdb"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	"github.com/flidai/leapview/internal/workload"
)

type testDataRuntimeFactory struct{}

func (testDataRuntimeFactory) OpenDashboardWorkspaceDataRuntimes(ctx context.Context, config WorkspaceDataRuntimeConfig) (map[string]DataRuntime, error) {
	if config.Definition == nil {
		return nil, fmt.Errorf("workspace definition is required")
	}
	database, err := analyticsducklake.Open(ctx, analyticsducklake.Config{RootDir: filepath.Join(config.DBDir, "ducklake"), MaxConnections: 2})
	if err != nil {
		return nil, err
	}
	controller, err := workload.New(workload.DefaultConfig())
	if err != nil {
		_ = database.Close()
		return nil, err
	}
	refreshLease, err := controller.Acquire(ctx, workload.Request{Class: workload.Refresh, PrincipalID: "test", Operation: "dashboard-test-refresh", EstimatedMemoryBytes: 1})
	if err != nil {
		controller.Close()
		_ = database.Close()
		return nil, err
	}
	runtime, err := analyticsduckdb.OpenWorkspaceMaterializeRuntime(refreshLease.Context(), analyticsduckdb.ProjectRuntimeConfig{
		Models:   config.Definition.Models,
		Database: database,
	})
	refreshLease.Release()
	if err != nil {
		controller.Close()
		_ = database.Close()
		return nil, err
	}
	sharedClose := &testSharedDataRuntimeCloser{runtime: runtime, database: database, controller: controller}
	runtimes := make(map[string]DataRuntime, len(config.Definition.Models))
	for modelID := range config.Definition.Models {
		runtimes[modelID] = testWorkspaceDataRuntime{
			modelID: modelID,
			runtime: runtime,
			close:   sharedClose,
			data:    reportdef.NewDataQueryService(modelID, runtime),
		}
	}
	return runtimes, nil
}

type testSharedDataRuntimeCloser struct {
	once       sync.Once
	runtime    *analyticsduckdb.ProjectRuntime
	database   *analyticsducklake.Environment
	controller *workload.Controller
	err        error
}

func (c *testSharedDataRuntimeCloser) Close() error {
	if c == nil {
		return nil
	}
	c.once.Do(func() {
		c.err = c.runtime.Close()
		if err := c.database.Close(); c.err == nil {
			c.err = err
		}
		c.controller.Close()
	})
	return c.err
}

type testWorkspaceDataRuntime struct {
	modelID string
	runtime *analyticsduckdb.ProjectRuntime
	close   *testSharedDataRuntimeCloser
	data    reportdef.DataService
}

func (r testWorkspaceDataRuntime) Query(ctx context.Context, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	return r.data.Query(r.readContext(ctx), request)
}

func (r testWorkspaceDataRuntime) Rows(ctx context.Context, request reportdef.RowQuery) (reportdef.QueryRows, error) {
	return r.data.Rows(r.readContext(ctx), request)
}

func (r testWorkspaceDataRuntime) Count(ctx context.Context, request reportdef.CountQuery) (int, error) {
	return r.data.Count(r.readContext(ctx), request)
}

func (r testWorkspaceDataRuntime) Histogram(ctx context.Context, request reportdef.RawValueQuery, binCount int) ([]reportdef.HistogramBin, error) {
	return r.data.Histogram(r.readContext(ctx), request, binCount)
}

func (r testWorkspaceDataRuntime) Distribution(ctx context.Context, request reportdef.RawValueQuery, sort []reportdef.QuerySort, limit int) (reportdef.QueryRows, error) {
	return r.data.Distribution(r.readContext(ctx), request, sort, limit)
}

func (r testWorkspaceDataRuntime) ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
	return r.runtime.ExecuteDataQuery(r.readContext(ctx), request)
}

func (r testWorkspaceDataRuntime) ExecuteDataQueryBundle(ctx context.Context, requests []dataquery.BundleRequest) (dataquery.BundleResult, error) {
	return r.runtime.ExecuteDataQueryBundle(r.readContext(ctx), requests)
}

func (r testWorkspaceDataRuntime) Refresh(ctx context.Context) error {
	lease, err := r.close.controller.Acquire(ctx, workload.Request{Class: workload.Refresh, PrincipalID: "test", Operation: "dashboard-test-refresh", EstimatedMemoryBytes: 1})
	if err != nil {
		return err
	}
	defer lease.Release()
	return r.runtime.Refresh(lease.Context())
}

func (r testWorkspaceDataRuntime) RefreshTables(ctx context.Context, tableNames []string) error {
	lease, err := r.close.controller.Acquire(ctx, workload.Request{Class: workload.Refresh, PrincipalID: "test", Operation: "dashboard-test-refresh", EstimatedMemoryBytes: 1})
	if err != nil {
		return err
	}
	defer lease.Release()
	return r.runtime.RefreshModelTables(lease.Context(), r.modelID, tableNames)
}

func (r testWorkspaceDataRuntime) readContext(ctx context.Context) context.Context {
	return workload.WithAdmitter(ctx, r.close.controller)
}

func (r testWorkspaceDataRuntime) Close() error {
	return r.close.Close()
}

func (r testWorkspaceDataRuntime) LastRefresh() time.Time {
	return r.runtime.LastRefresh()
}
