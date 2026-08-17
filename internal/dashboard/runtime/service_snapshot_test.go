package runtime

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type runtimeAuditRecorder struct {
	queries []dataquery.Query
	results []dataquery.Result
}

func (r *runtimeAuditRecorder) RecordDataQuery(_ context.Context, query dataquery.Query, result dataquery.Result) error {
	r.queries = append(r.queries, query)
	r.results = append(r.results, result)
	return nil
}

type failingBundleDataRuntime struct{ snapshotDataRuntime }

func (f failingBundleDataRuntime) ExecuteDataQueryBundle(context.Context, []dataquery.BundleRequest) (dataquery.BundleResult, error) {
	return dataquery.BundleResult{}, errors.New("bundle failed")
}

type cacheOutcomeBundleDataRuntime struct {
	snapshotDataRuntime
	result dataquery.BundleResult
}

type bindingDataRuntime struct {
	snapshotDataRuntime
	query  dataquery.Query
	bundle []dataquery.BundleRequest
}

func (r *bindingDataRuntime) ExecuteDataQuery(_ context.Context, query dataquery.Query) (dataquery.Result, error) {
	r.query = query
	return dataquery.Result{Status: dataquery.StatusSuccess, ExecutionState: dataquery.ExecutionSucceeded, PlanningMS: 1, DatabaseMS: 1}, nil
}

func (r *bindingDataRuntime) ExecuteDataQueryBundle(_ context.Context, requests []dataquery.BundleRequest) (dataquery.BundleResult, error) {
	r.bundle = append([]dataquery.BundleRequest(nil), requests...)
	return dataquery.BundleResult{Results: map[string]dataquery.Result{
		requests[0].ID: {Status: dataquery.StatusSuccess, ExecutionState: dataquery.ExecutionSucceeded, PlanningMS: 1, DatabaseMS: 1},
	}}, nil
}

func TestGovernedDataRuntimeBindsProjectIdentityAndAudit(t *testing.T) {
	underlying := &bindingDataRuntime{}
	runtime := newGovernedDataRuntime("project_1", "model_1", underlying)
	port := runtime.(DataRuntime)
	recorder := &runtimeAuditRecorder{}
	ctx := dataquery.WithAuditRecorder(context.Background(), recorder)
	request := dataquery.Query{PrincipalID: "principal_1", ModelID: "model_1", Kind: dataquery.KindSemanticAggregate, Metrics: []dataquery.Field{{Field: "order_count"}}}
	if _, err := port.ExecuteDataQuery(ctx, request); err != nil {
		t.Fatal(err)
	}
	if underlying.query.ProjectID != "project_1" || len(recorder.queries) != 1 || recorder.queries[0].ProjectID != "project_1" {
		t.Fatalf("bound query=%#v audit=%#v", underlying.query, recorder.queries)
	}

	request.ProjectID = "other_project"
	if _, err := port.ExecuteDataQuery(ctx, request); err == nil {
		t.Fatal("mismatched project query was accepted")
	}
	if underlying.query.ProjectID != "project_1" {
		t.Fatalf("mismatched query reached runtime: %#v", underlying.query)
	}
	request.ProjectID = ""
	metadataMismatch := dataquery.WithMetadata(context.Background(), dataquery.Metadata{ProjectID: "other_project"})
	if _, err := port.ExecuteDataQuery(metadataMismatch, request); err == nil {
		t.Fatal("mismatched project metadata was accepted")
	}
}

func TestGovernedDataRuntimeBindsEveryBundleBranch(t *testing.T) {
	underlying := &bindingDataRuntime{}
	runtime := newGovernedDataRuntime("project_1", "model_1", underlying)
	port := runtime.(dataquery.BundleExecutor)
	requests := []dataquery.BundleRequest{
		{ID: "one", Query: dataquery.Query{PrincipalID: "principal_1", ModelID: "model_1", Kind: dataquery.KindSemanticAggregate, Metrics: []dataquery.Field{{Field: "one"}}}},
		{ID: "two", Query: dataquery.Query{PrincipalID: "principal_1", ModelID: "model_1", Kind: dataquery.KindSemanticAggregate, Metrics: []dataquery.Field{{Field: "two"}}}},
	}
	if _, err := port.ExecuteDataQueryBundle(context.Background(), requests); err != nil {
		t.Fatal(err)
	}
	if len(underlying.bundle) != 2 {
		t.Fatalf("bundle = %#v", underlying.bundle)
	}
	for _, request := range underlying.bundle {
		if request.Query.ProjectID != "project_1" {
			t.Fatalf("bundle branch query = %#v", request.Query)
		}
	}
	requests[1].Query.ProjectID = "other_project"
	if _, err := port.ExecuteDataQueryBundle(context.Background(), requests); err == nil {
		t.Fatal("mismatched bundle branch was accepted")
	}
}

func (r cacheOutcomeBundleDataRuntime) ExecuteDataQueryBundle(ctx context.Context, _ []dataquery.BundleRequest) (dataquery.BundleResult, error) {
	for _, result := range r.result.Results {
		dataquery.ObserveCacheOutcome(ctx, result.CacheOutcome)
	}
	return r.result, nil
}

func TestGovernedBundleAuditSummarizesCacheOutcomeConservatively(t *testing.T) {
	tests := []struct {
		name     string
		outcomes map[string]string
		want     string
	}{
		{name: "all hit", outcomes: map[string]string{"one": dataquery.CacheHit, "two": dataquery.CacheHit}, want: dataquery.CacheHit},
		{name: "all coalesced", outcomes: map[string]string{"one": dataquery.CacheCoalesced, "two": dataquery.CacheCoalesced}, want: dataquery.CacheCoalesced},
		{name: "all miss", outcomes: map[string]string{"one": dataquery.CacheMiss, "two": dataquery.CacheMiss}, want: dataquery.CacheMiss},
		{name: "mixed hit and coalesced", outcomes: map[string]string{"one": dataquery.CacheHit, "two": dataquery.CacheCoalesced}, want: dataquery.CacheCoalesced},
		{name: "mixed miss coalesced and hit", outcomes: map[string]string{"one": dataquery.CacheHit, "two": dataquery.CacheMiss, "three": dataquery.CacheCoalesced}, want: dataquery.CacheMiss},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := make(map[string]dataquery.Result, len(tt.outcomes))
			requests := make([]dataquery.BundleRequest, 0, len(tt.outcomes))
			for id, outcome := range tt.outcomes {
				results[id] = dataquery.Result{CacheOutcome: outcome}
				requests = append(requests, dataquery.BundleRequest{
					ID: id,
					Query: dataquery.Query{
						PrincipalID: "user",
						ModelID:     "sales",
						Kind:        dataquery.KindSemanticAggregate,
						Metrics:     []dataquery.Field{{Field: id}},
					},
				})
			}
			runtime := newGovernedDataRuntime("sales", "sales", cacheOutcomeBundleDataRuntime{
				result: dataquery.BundleResult{Results: results},
			})
			port := runtime.(dataquery.BundleExecutor)
			recorder := &runtimeAuditRecorder{}
			observed := map[string]int{}
			ctx := dataquery.WithCacheOutcomeObserver(context.Background(), func(outcome string) {
				observed[outcome]++
			})
			ctx = dataquery.WithAuditRecorder(ctx, recorder)

			if _, err := port.ExecuteDataQueryBundle(ctx, requests); err != nil {
				t.Fatalf("ExecuteDataQueryBundle() error = %v", err)
			}
			if len(recorder.results) != 1 {
				t.Fatalf("audit results = %#v, want one bundle summary", recorder.results)
			}
			if got := recorder.results[0].CacheOutcome; got != tt.want {
				t.Fatalf("bundle audit cache outcome = %q, want %q", got, tt.want)
			}
			wantObserved := map[string]int{}
			for _, outcome := range tt.outcomes {
				wantObserved[outcome]++
			}
			if !reflect.DeepEqual(observed, wantObserved) {
				t.Fatalf("per-branch cache events = %#v, want %#v", observed, wantObserved)
			}
		})
	}
}

func TestGovernedBundleAuditDoesNotReportSucceededExecutionOnError(t *testing.T) {
	runtime := newGovernedDataRuntime("sales", "sales", failingBundleDataRuntime{})
	port, ok := runtime.(dataquery.BundleExecutor)
	if !ok {
		t.Fatal("governed runtime does not expose bundle execution")
	}
	recorder := &runtimeAuditRecorder{}
	ctx := dataquery.WithAuditRecorder(context.Background(), recorder)
	requests := []dataquery.BundleRequest{
		{ID: "one", Query: dataquery.Query{PrincipalID: "user", ModelID: "sales", Kind: dataquery.KindSemanticAggregate, Metrics: []dataquery.Field{{Field: "one"}}}},
		{ID: "two", Query: dataquery.Query{PrincipalID: "user", ModelID: "sales", Kind: dataquery.KindSemanticAggregate, Metrics: []dataquery.Field{{Field: "two"}}}},
	}
	if _, err := port.ExecuteDataQueryBundle(ctx, requests); err == nil {
		t.Fatal("bundle error = nil")
	}
	if len(recorder.results) != 1 {
		t.Fatalf("audit results = %#v", recorder.results)
	}
	result := recorder.results[0]
	if result.Status != dataquery.StatusError || result.ExecutionState != dataquery.ExecutionFailed {
		t.Fatalf("audit result status=%q execution=%q, want error/failed", result.Status, result.ExecutionState)
	}
}

func TestServiceDuckLakeSnapshotIDRequiresOneWorkspaceSnapshot(t *testing.T) {
	service := &Service{
		runtimes: map[projectgraph.ResourceID]*modelRuntime{
			"orders":   {data: snapshotDataRuntime{snapshotID: 42}},
			"products": {data: snapshotDataRuntime{snapshotID: 42}},
		},
	}
	if got := service.DuckLakeSnapshotID(); got != 42 {
		t.Fatalf("DuckLakeSnapshotID = %d, want 42", got)
	}

	service.runtimes["products"].data = snapshotDataRuntime{snapshotID: 43}
	if got := service.DuckLakeSnapshotID(); got != 0 {
		t.Fatalf("DuckLakeSnapshotID with mixed snapshots = %d, want 0", got)
	}
}

func TestServiceAdvertisesConcurrencyOnlyForPinnedSnapshotReaders(t *testing.T) {
	service := &Service{
		runtimes: map[projectgraph.ResourceID]*modelRuntime{
			"orders": {ready: true, data: snapshotDataRuntime{snapshotID: 42, readConcurrency: 3}},
		},
	}
	if got := service.DashboardTargetConcurrency(); got != 3 {
		t.Fatalf("DashboardTargetConcurrency = %d, want 3", got)
	}
	service.runtimes["orders"].data = snapshotDataRuntime{readConcurrency: 3}
	if got := service.DashboardTargetConcurrency(); got != 1 {
		t.Fatalf("mutable DashboardTargetConcurrency = %d, want 1", got)
	}
}

func TestGovernedDataRuntimeForwardsDuckLakeSnapshotID(t *testing.T) {
	runtime := newGovernedDataRuntime("sales", "sales", snapshotDataRuntime{snapshotID: 42})
	snapshot, ok := runtime.(DataRuntimeSnapshot)
	if !ok {
		t.Fatalf("governed runtime does not expose DuckLake snapshot")
	}
	if got := snapshot.DuckLakeSnapshotID(); got != 42 {
		t.Fatalf("DuckLakeSnapshotID = %d, want 42", got)
	}
}

type snapshotDataRuntime struct {
	snapshotID      int64
	readConcurrency int
}

func (r snapshotDataRuntime) Query(context.Context, reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	return nil, nil
}

func (r snapshotDataRuntime) Rows(context.Context, reportdef.RowQuery) (reportdef.QueryRows, error) {
	return nil, nil
}

func (r snapshotDataRuntime) Count(context.Context, reportdef.CountQuery) (int, error) {
	return 0, nil
}

func (r snapshotDataRuntime) Histogram(context.Context, reportdef.RawValueQuery, int) ([]reportdef.HistogramBin, error) {
	return nil, nil
}

func (r snapshotDataRuntime) Distribution(context.Context, reportdef.RawValueQuery, []reportdef.QuerySort, int) (reportdef.QueryRows, error) {
	return nil, nil
}

func (r snapshotDataRuntime) ExecuteDataQuery(context.Context, dataquery.Query) (dataquery.Result, error) {
	return dataquery.Result{}, nil
}

func (r snapshotDataRuntime) Refresh(context.Context) error {
	return nil
}

func (r snapshotDataRuntime) Close() error {
	return nil
}

func (r snapshotDataRuntime) LastRefresh() time.Time {
	return time.Time{}
}

func (r snapshotDataRuntime) DuckLakeSnapshotID() int64 {
	return r.snapshotID
}

func (r snapshotDataRuntime) ReadConcurrency() int {
	return max(1, r.readConcurrency)
}
