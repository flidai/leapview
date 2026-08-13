package materialize

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/analytics/arrowresult"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/resultcache"
)

type bundleStage string

const (
	bundleStageGovern           bundleStage = "govern_validate"
	bundleStageCache            bundleStage = "cache_resolve"
	bundleStagePlan             bundleStage = "plan"
	bundleStageExecute          bundleStage = "admit_execute"
	bundleStageSplitStoreDecode bundleStage = "arrow_split_store_decode"
	bundleStageTransformObserve bundleStage = "transform_observe"
)

type bundleStageObserverContextKey struct{}

func withBundleStageObserver(ctx context.Context, observer func(bundleStage)) context.Context {
	return context.WithValue(ctx, bundleStageObserverContextKey{}, observer)
}

func enterBundleStage(ctx context.Context, stage bundleStage) error {
	if observer, ok := ctx.Value(bundleStageObserverContextKey{}).(func(bundleStage)); ok && observer != nil {
		observer(stage)
	}
	return ctx.Err()
}

type governedBundle struct {
	requests   []dataquery.BundleRequest
	branches   []dataquery.BundleRequest
	transforms map[string]dataquery.ResultTransformer
}

type bundleCacheSlot struct {
	key        string
	generation uint64
}

type bundleFlightSlot struct {
	ID         string `json:"id"`
	Key        string `json:"key"`
	Generation uint64 `json:"generation"`
}

type resolvedBundle struct {
	governedBundle
	misses []dataquery.BundleRequest
	result dataquery.BundleResult
	slots  map[string]bundleCacheSlot
}

type plannedBundle struct {
	resolved   resolvedBundle
	plan       semanticquery.BundlePlan
	planningMS int64
	flightKey  string
}

type bundleExecution struct {
	decoded map[string]semanticquery.Rows
	bytes   map[string]int64
	summary dataquery.Result
}

// ExecuteDataQueryBundle authorizes every branch before compiling one
// single-fact GROUPING SETS statement. The deliberately short orchestration
// method gives each stage one failure boundary and typed state, while a bundle
// miss is still admitted and observed as exactly one physical query.
func (r *Runtime) ExecuteDataQueryBundle(ctx context.Context, requests []dataquery.BundleRequest) (dataquery.BundleResult, error) {
	if r == nil || r.db == nil {
		return dataquery.BundleResult{}, fmt.Errorf("materialization runtime is not initialized")
	}
	if len(requests) < 2 {
		return dataquery.BundleResult{}, &dataquery.BundleIncompatibleError{Err: fmt.Errorf("bundle requires at least two branches")}
	}
	if _, ok := r.db.(arrowDatabase); !ok {
		return dataquery.BundleResult{}, fmt.Errorf("analytical database does not support native Arrow execution")
	}
	ctx = dataquery.WithResultBudget(ctx, r.queryResultLimits())
	governed, err := r.governAndValidateBundle(ctx, requests)
	if err != nil {
		return dataquery.BundleResult{}, err
	}
	resolved, err := r.resolveBundleCache(ctx, governed)
	if err != nil {
		return dataquery.BundleResult{}, err
	}
	if len(resolved.misses) <= 1 {
		return r.executeDegenerateBundle(ctx, resolved)
	}
	planned, err := r.planBundle(ctx, resolved)
	if err != nil {
		return dataquery.BundleResult{}, err
	}
	execution, shared, err := r.executePlannedBundle(ctx, planned)
	if err != nil {
		return finishBundle(ctx, planned.resolved, err)
	}
	return finishExecutedBundle(ctx, planned, execution, shared)
}

func (r *Runtime) governAndValidateBundle(ctx context.Context, requests []dataquery.BundleRequest) (governedBundle, error) {
	if err := enterBundleStage(ctx, bundleStageGovern); err != nil {
		return governedBundle{}, err
	}
	out := governedBundle{requests: append([]dataquery.BundleRequest(nil), requests...), branches: make([]dataquery.BundleRequest, 0, len(requests)), transforms: make(map[string]dataquery.ResultTransformer, len(requests))}
	for _, branch := range requests {
		if err := ctx.Err(); err != nil {
			return governedBundle{}, err
		}
		request := branch.Query.WithMetadata(dataquery.MetadataFromContext(ctx))
		if request.ModelID == "" {
			request.ModelID = r.modelID
		}
		if request.ModelID != r.modelID || request.Kind != dataquery.KindSemanticAggregate {
			return governedBundle{}, &dataquery.BundleIncompatibleError{Err: fmt.Errorf("branch %q is not an aggregate for model %q", branch.ID, r.modelID)}
		}
		if governor, ok := dataquery.GovernorFromContext(ctx); ok && !dataquery.GovernanceApplied(ctx) {
			var err error
			request, out.transforms[branch.ID], err = governor.GovernDataQuery(ctx, request)
			if err != nil {
				return governedBundle{}, &dataquery.BundleBranchError{ID: branch.ID, Err: err}
			}
		}
		if err := request.Validate(); err != nil {
			return governedBundle{}, &dataquery.BundleBranchError{ID: branch.ID, Err: err}
		}
		if !dashboardQueryResultCacheable(request) {
			return governedBundle{}, &dataquery.BundleIncompatibleError{Err: fmt.Errorf("branch %q is not a cache-governed dashboard query", branch.ID)}
		}
		out.branches = append(out.branches, dataquery.BundleRequest{ID: branch.ID, Query: request})
	}
	return out, nil
}

func (r *Runtime) resolveBundleCache(ctx context.Context, governed governedBundle) (resolvedBundle, error) {
	if err := enterBundleStage(ctx, bundleStageCache); err != nil {
		return resolvedBundle{}, err
	}
	out := resolvedBundle{governedBundle: governed, result: dataquery.BundleResult{Results: make(map[string]dataquery.Result, len(governed.requests))}, slots: make(map[string]bundleCacheSlot, len(governed.branches))}
	for _, branch := range governed.branches {
		cached, key, generation, hit, err := r.queryCache.lookupArrow(ctx, branch.Query)
		if err != nil {
			return resolvedBundle{}, &dataquery.BundleBranchError{ID: branch.ID, Err: err}
		}
		if hit {
			dataquery.ObserveCacheOutcome(ctx, dataquery.CacheHit)
			out.result.Results[branch.ID] = cached
			continue
		}
		out.slots[branch.ID] = bundleCacheSlot{key: key, generation: generation}
		out.misses = append(out.misses, branch)
	}
	return out, nil
}

func (r *Runtime) executeDegenerateBundle(ctx context.Context, resolved resolvedBundle) (dataquery.BundleResult, error) {
	if len(resolved.misses) == 0 {
		return finishBundle(ctx, resolved, nil)
	}
	branch := resolved.misses[0]
	branchResult, err := r.ExecuteDataQuery(dataquery.WithGovernanceApplied(ctx), branch.Query)
	if err != nil {
		_ = applyBundleTransforms(resolved, err)
		return dataquery.BundleResult{}, &dataquery.BundleBranchError{ID: branch.ID, Err: err}
	}
	if err := enterBundleStage(ctx, bundleStageTransformObserve); err != nil {
		_ = applyBundleTransforms(resolved, err)
		return dataquery.BundleResult{}, err
	}
	resolved.result.Results[branch.ID] = branchResult
	resolved.result.SQL = branchResult.SQL
	return finishBundleAfterStage(resolved, nil)
}

func (r *Runtime) planBundle(ctx context.Context, resolved resolvedBundle) (plannedBundle, error) {
	if err := enterBundleStage(ctx, bundleStagePlan); err != nil {
		return plannedBundle{}, err
	}
	semanticRequests := make([]semanticquery.BundleRequest, len(resolved.misses))
	for index, branch := range resolved.misses {
		request := branch.Query
		semanticRequests[index] = semanticquery.BundleRequest{ID: branch.ID, Request: semanticquery.Request{Table: request.Target, Dimensions: dataQueryFields(request.Fields), Measures: dataQueryFields(request.Measures), Time: semanticquery.Time{Field: request.Time.Field, Grain: request.Time.Grain, Alias: request.Time.Alias}, Filters: dataQueryFilters(request.Filters), Sort: dataQuerySorts(request.Sort), ColumnMasks: dataQueryColumnMasks(request.ColumnMasks), Limit: request.Limit, Offset: request.Offset}}
	}
	started := time.Now()
	plan, err := r.queryPlanner().PlanBundle(semanticRequests)
	planningMS := elapsedStageMS(started)
	if err != nil {
		return plannedBundle{}, &dataquery.BundleIncompatibleError{Err: err}
	}
	identity := make([]bundleFlightSlot, 0, len(resolved.misses))
	for _, branch := range resolved.misses {
		slot := resolved.slots[branch.ID]
		identity = append(identity, bundleFlightSlot{ID: branch.ID, Key: slot.key, Generation: slot.generation})
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return plannedBundle{}, fmt.Errorf("encode aggregate bundle flight identity: %w", err)
	}
	return plannedBundle{resolved: resolved, plan: plan, planningMS: planningMS, flightKey: string(encoded)}, nil
}

func (r *Runtime) executePlannedBundle(ctx context.Context, planned plannedBundle) (bundleExecution, bool, error) {
	if err := enterBundleStage(ctx, bundleStageExecute); err != nil {
		return bundleExecution{}, false, err
	}
	value, shared, err := r.queryCache.coalesce(ctx, planned.flightKey, func() (any, error) {
		execCtx, statements := withPhysicalStatementCounter(dataquery.WithIndependentResultBudget(ctx, r.queryResultLimits()))
		var execution bundleExecution
		summary, executeErr := admitPhysicalQuery(execCtx, planned.resolved.misses[0].Query, func(queryCtx context.Context) (dataquery.Result, error) {
			var err error
			execution, err = r.executeArrowBundle(queryCtx, planned)
			return execution.summary, err
		})
		execution.summary = summary
		if count := int(statements.Load()); count > 0 {
			dataquery.ObservePhysicalQuery(ctx, dataquery.PhysicalQueryObservation{Count: count, Result: summary})
		}
		return execution, executeErr
	})
	if err != nil {
		return bundleExecution{}, shared, err
	}
	return value.(bundleExecution), shared, nil
}

func (r *Runtime) executeArrowBundle(ctx context.Context, planned plannedBundle) (bundleExecution, error) {
	lease, leasedCtx, err := acquireDatabaseLease(ctx, r.db)
	if err != nil {
		return bundleExecution{}, err
	}
	if lease != nil {
		defer lease.Release()
		ctx = leasedCtx
	}
	if err := r.ensureRequiredExtensions(ctx); err != nil {
		return bundleExecution{}, err
	}
	ctx, connectionWait := dataquery.WithConnectionWaitCounter(ctx)
	started := time.Now()
	summary := dataquery.Result{PlanningMS: planned.planningMS, SQL: planned.plan.Plan.SQL}
	// captureArrowPlan transfers one creator-owned reference to this stage.
	// It is released here after splitting on every success and error path.
	source, err := r.captureArrowPlan(ctx, planned.plan.Plan)
	if source != nil {
		defer source.Release()
	}
	if err == nil {
		if stageErr := enterBundleStage(ctx, bundleStageSplitStoreDecode); stageErr != nil {
			err = stageErr
		} else {
			var execution bundleExecution
			execution, err = r.splitStoreDecodeBundle(ctx, planned, source)
			execution.summary = summary
			if err == nil {
				summary = execution.summary
				summary.ExecutionState = dataquery.ExecutionSucceeded
				execution.summary = summary
				execution.summary.ConnectionWaitMS, execution.summary.DatabaseMS = bundleDatabaseTimings(started, connectionWait.Duration())
				return execution, nil
			}
		}
	}
	summary.ConnectionWaitMS, summary.DatabaseMS = bundleDatabaseTimings(started, connectionWait.Duration())
	return bundleExecution{summary: summary}, err
}

func bundleDatabaseTimings(started time.Time, wait time.Duration) (int64, int64) {
	databaseMS := elapsedStageMS(started)
	waitMS := wait.Milliseconds()
	if waitMS >= databaseMS {
		return waitMS, 0
	}
	return waitMS, databaseMS - waitMS
}

// ownedBundleArrow owns the creator reference for every projected branch.
// Cache storage acquires its own hold; decoded Go rows retain no Arrow memory.
type ownedBundleArrow map[string]*arrowresult.Result

func (owned ownedBundleArrow) release() {
	for _, result := range owned {
		result.Release()
	}
}

func (r *Runtime) splitStoreDecodeBundle(ctx context.Context, planned plannedBundle, source *arrowresult.Result) (bundleExecution, error) {
	branches, err := splitArrowBundle(ctx, planned.plan, source)
	if err != nil {
		return bundleExecution{}, err
	}
	owned := ownedBundleArrow(branches)
	defer owned.release()
	if err := ctx.Err(); err != nil {
		return bundleExecution{}, err
	}
	execution := bundleExecution{decoded: make(map[string]semanticquery.Rows, len(branches)), bytes: make(map[string]int64, len(branches))}
	for _, request := range planned.resolved.misses {
		branch := branches[request.ID]
		if branch == nil {
			return bundleExecution{}, fmt.Errorf("bundle execution omitted branch %q", request.ID)
		}
		execution.bytes[request.ID] = branch.Bytes()
		branchLease, err := branch.Acquire()
		if err != nil {
			return bundleExecution{}, err
		}
		values, decodeErr := arrowresult.DecodeRows(branchLease)
		branchLease.Release()
		if decodeErr != nil {
			return bundleExecution{}, decodeErr
		}
		execution.decoded[request.ID] = make(semanticquery.Rows, len(values))
		for index := range values {
			execution.decoded[request.ID][index] = semanticquery.Row(values[index])
		}
	}
	if err := ctx.Err(); err != nil {
		return bundleExecution{}, err
	}
	for _, request := range planned.resolved.misses {
		slot := planned.resolved.slots[request.ID]
		r.queryCache.scope.StoreArrow(slot.key, resultcache.Token(slot.generation), branches[request.ID], resultcache.Metadata{SQL: planned.plan.Plan.SQL})
	}
	r.queryCache.syncStats()
	return execution, nil
}

func finishExecutedBundle(ctx context.Context, planned plannedBundle, execution bundleExecution, shared bool) (dataquery.BundleResult, error) {
	resolved := planned.resolved
	resolved.result.SQL = planned.plan.Plan.SQL
	for _, branch := range resolved.misses {
		rows := dataQueryRows(execution.decoded[branch.ID])
		branchResult := dataquery.Result{Rows: rows, Columns: dataquery.ColumnsFromNames(bundleOutputColumns(planned.plan, branch.ID)), SQL: planned.plan.Plan.SQL, PlanningMS: execution.summary.PlanningMS, ConnectionWaitMS: execution.summary.ConnectionWaitMS, DatabaseMS: execution.summary.DatabaseMS, ExecutionState: dataquery.ExecutionSucceeded, Status: dataquery.StatusSuccess, RowsReturned: len(rows), BytesEstimate: execution.bytes[branch.ID], CacheOutcome: dataquery.CacheMiss}
		if shared {
			branchResult.CacheOutcome = dataquery.CacheCoalesced
		}
		dataquery.ObserveCacheOutcome(ctx, branchResult.CacheOutcome)
		resolved.result.Results[branch.ID] = branchResult
	}
	return finishBundle(ctx, resolved, nil)
}

func finishBundle(ctx context.Context, resolved resolvedBundle, executeErr error) (dataquery.BundleResult, error) {
	stageErr := enterBundleStage(ctx, bundleStageTransformObserve)
	if executeErr == nil {
		executeErr = stageErr
	}
	return finishBundleAfterStage(resolved, executeErr)
}

func finishBundleAfterStage(resolved resolvedBundle, executeErr error) (dataquery.BundleResult, error) {
	transformErr := applyBundleTransforms(resolved, executeErr)
	if executeErr != nil {
		return dataquery.BundleResult{}, executeErr
	}
	if transformErr != nil {
		return dataquery.BundleResult{}, transformErr
	}
	return resolved.result, nil
}

func applyBundleTransforms(resolved resolvedBundle, executeErr error) error {
	for _, branch := range resolved.requests {
		transform := resolved.transforms[branch.ID]
		if transform == nil {
			continue
		}
		branchResult := resolved.result.Results[branch.ID]
		if err := transform(&branchResult, executeErr); err != nil {
			return &dataquery.BundleBranchError{ID: branch.ID, Err: err}
		}
		if executeErr == nil {
			resolved.result.Results[branch.ID] = branchResult
		}
	}
	return nil
}
