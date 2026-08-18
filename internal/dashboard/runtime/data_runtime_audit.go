package runtime

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type governedDataRuntime struct {
	DataRuntime
	projectID projectgraph.ResourceID
	service   reportdef.DataService
}

func newGovernedDataRuntime(projectID projectgraph.ResourceID, modelID projectgraph.ResourceID, runtime DataRuntime) DataRuntime {
	wrapped := &governedDataRuntime{DataRuntime: runtime, projectID: projectID}
	wrapped.service = reportdef.NewDataQueryService(projectID, modelID.String(), wrapped)
	return wrapped
}

// Planner forwards the activation-owned planner through the governed runtime
// wrapper. Embedding DataRuntime alone does not promote optional capability
// methods from the wrapped dynamic value, so retain this narrow forwarding
// method for Service.Planner and composition adapters.
func (r *governedDataRuntime) Planner() consumer.Planner {
	if r == nil || r.DataRuntime == nil {
		return nil
	}
	port, ok := r.DataRuntime.(DataRuntimePlanner)
	if !ok {
		return nil
	}
	return port.Planner()
}

func (r *governedDataRuntime) bindProject(request dataquery.Query) (dataquery.Query, error) {
	if err := r.projectID.Validate(); err != nil {
		return dataquery.Query{}, fmt.Errorf("governed dashboard project identity: %w", err)
	}
	if request.ProjectID != "" && request.ProjectID != r.projectID {
		return dataquery.Query{}, fmt.Errorf("governed dashboard query project %q does not match runtime project %q", request.ProjectID, r.projectID)
	}
	request.ProjectID = r.projectID
	return request, nil
}

func (r *governedDataRuntime) bindContext(ctx context.Context) (context.Context, error) {
	metadata := dataquery.MetadataFromContext(ctx)
	if metadata.ProjectID != "" && metadata.ProjectID != r.projectID {
		return ctx, fmt.Errorf("governed dashboard metadata project %q does not match runtime project %q", metadata.ProjectID, r.projectID)
	}
	metadata.ProjectID = r.projectID
	return dataquery.WithMetadata(ctx, metadata), nil
}

func (r *governedDataRuntime) Query(ctx context.Context, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	return r.service.Query(ctx, request)
}

func (r *governedDataRuntime) Rows(ctx context.Context, request reportdef.RowQuery) (reportdef.QueryRows, error) {
	return r.service.Rows(ctx, request)
}

func (r *governedDataRuntime) Count(ctx context.Context, request reportdef.CountQuery) (int, error) {
	return r.service.Count(ctx, request)
}

func (r *governedDataRuntime) Histogram(ctx context.Context, request reportdef.RawValueQuery, binCount int) ([]reportdef.HistogramBin, error) {
	return r.service.Histogram(ctx, request, binCount)
}

func (r *governedDataRuntime) Distribution(ctx context.Context, request reportdef.RawValueQuery, sort []reportdef.QuerySort, limit int) (reportdef.QueryRows, error) {
	return r.service.Distribution(ctx, request, sort, limit)
}

func (r *governedDataRuntime) VerifySemantic(ctx context.Context) error {
	verifier, ok := r.DataRuntime.(DataRuntimeSemanticVerifier)
	if !ok {
		return fmt.Errorf("dashboard data runtime does not support semantic verification")
	}
	return verifier.VerifySemantic(ctx)
}

func (r *governedDataRuntime) ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
	bound, err := r.bindProject(request)
	if err != nil {
		return dataquery.Result{}, err
	}
	boundContext, err := r.bindContext(ctx)
	if err != nil {
		return dataquery.Result{}, err
	}
	return dataquery.ExecuteAudited(boundContext, bound, r.DataRuntime.ExecuteDataQuery)
}

func (r *governedDataRuntime) ExecuteDataQueryBundle(ctx context.Context, requests []dataquery.BundleRequest) (dataquery.BundleResult, error) {
	port, ok := r.DataRuntime.(dataquery.BundleExecutor)
	if !ok {
		return dataquery.BundleResult{}, &dataquery.BundleIncompatibleError{Err: fmt.Errorf("dashboard data runtime does not support aggregate bundles")}
	}
	if len(requests) == 0 {
		return dataquery.BundleResult{}, &dataquery.BundleIncompatibleError{Err: fmt.Errorf("bundle is empty")}
	}
	boundContext, err := r.bindContext(ctx)
	if err != nil {
		return dataquery.BundleResult{}, err
	}
	boundRequests := make([]dataquery.BundleRequest, len(requests))
	ids := make([]string, len(requests))
	for index, request := range requests {
		bound, err := r.bindProject(request.Query)
		if err != nil {
			return dataquery.BundleResult{}, err
		}
		boundRequests[index] = dataquery.BundleRequest{ID: request.ID, Query: bound}
	}
	audit := boundRequests[0].Query
	fieldSet := map[string]bool{}
	metricSet := map[string]bool{}
	for i := range boundRequests {
		ids[i] = boundRequests[i].ID
		for _, field := range boundRequests[i].Query.Fields {
			key := field.Field + "\x00" + field.Alias
			if !fieldSet[key] {
				fieldSet[key] = true
				audit.Fields = append(audit.Fields, field)
			}
		}
		for _, metric := range boundRequests[i].Query.Metrics {
			key := metric.Field + "\x00" + metric.Alias
			if !metricSet[key] {
				metricSet[key] = true
				audit.Metrics = append(audit.Metrics, metric)
			}
		}
	}
	// The first request's fields/metrics were appended again above.
	audit.Fields = dedupeDataQueryFields(audit.Fields)
	audit.Metrics = dedupeDataQueryFields(audit.Metrics)
	sort.Strings(ids)
	audit.ObjectType = "dashboard_refresh_bundle"
	audit.ObjectID = strings.Join(ids, ",")
	audit.Sort = nil
	audit.Offset = 0
	audit.Limit = 0
	var bundle dataquery.BundleResult
	_, err = dataquery.ExecuteAudited(boundContext, audit, func(execCtx context.Context, _ dataquery.Query) (dataquery.Result, error) {
		var executeErr error
		bundle, executeErr = port.ExecuteDataQueryBundle(execCtx, boundRequests)
		summary := dataquery.Result{SQL: bundle.SQL}
		if executeErr == nil {
			summary.ExecutionState = dataquery.ExecutionSucceeded
		}
		for _, result := range bundle.Results {
			summary.RowsReturned += len(result.Rows)
			summary.BytesEstimate += result.BytesEstimate
			summary.PlanningMS = max(summary.PlanningMS, result.PlanningMS)
			summary.ConnectionWaitMS = max(summary.ConnectionWaitMS, result.ConnectionWaitMS)
			summary.DatabaseMS = max(summary.DatabaseMS, result.DatabaseMS)
		}
		summary.CacheOutcome = bundleCacheOutcome(bundle.Results)
		return summary, executeErr
	})
	return bundle, err
}

// bundleCacheOutcome records one representative audit outcome without changing
// per-branch cache observation. The most conservative outcome wins: a miss
// conveys physical database work, coalesced conveys shared work, and hit conveys
// no database work. Cache errors outrank successful outcomes defensively.
func bundleCacheOutcome(results map[string]dataquery.Result) string {
	bestOutcome := ""
	bestRank := 0
	for _, result := range results {
		outcome := result.CacheOutcome
		rank := 0
		switch outcome {
		case dataquery.CacheHit:
			rank = 1
		case dataquery.CacheCoalesced:
			rank = 2
		case dataquery.CacheMiss:
			rank = 3
		case dataquery.CacheError:
			rank = 4
		}
		if rank > bestRank {
			bestRank = rank
			bestOutcome = outcome
		}
	}
	return bestOutcome
}

func dedupeDataQueryFields(fields []dataquery.Field) []dataquery.Field {
	seen := map[string]bool{}
	out := make([]dataquery.Field, 0, len(fields))
	for _, field := range fields {
		key := field.Field + "\x00" + field.Alias
		if !seen[key] {
			seen[key] = true
			out = append(out, field)
		}
	}
	return out
}

func (r *governedDataRuntime) RefreshTables(ctx context.Context, tableNames []string) error {
	port, ok := r.DataRuntime.(interface {
		RefreshTables(context.Context, []string) error
	})
	if !ok {
		return fmt.Errorf("dashboard data runtime does not support model table refresh")
	}
	return port.RefreshTables(ctx, tableNames)
}

func (r *governedDataRuntime) DuckLakeSnapshotID() int64 {
	snapshot, ok := r.DataRuntime.(DataRuntimeSnapshot)
	if !ok {
		return 0
	}
	return snapshot.DuckLakeSnapshotID()
}

func (r *governedDataRuntime) ReadConcurrency() int {
	concurrency, ok := r.DataRuntime.(DataRuntimeReadConcurrency)
	if !ok {
		return 1
	}
	return max(1, concurrency.ReadConcurrency())
}
