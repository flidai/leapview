package materialize

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/masking"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	analyticsresource "github.com/flidai/leapview/internal/analytics/resource"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	"github.com/flidai/leapview/internal/workload"
)

type RuntimeConfig struct {
	ModelID string
	Model   *semanticmodel.Model
	// ResultPartition is the stable production or candidate namespace for
	// dependency-keyed query result reuse.
	ResultPartition resultidentity.Partition
	// QueryResultCache has stable partition lifetime. ImmutableByteCache remains
	// generation-owned so spatial tile and other opaque byte identities do not
	// acquire cross-generation reuse semantics.
	QueryResultCache   *resultcache.Scope
	ImmutableByteCache *resultcache.Scope
	// ExecutionScope owns mutable query, bundle, Arrow, and immutable-byte
	// flights for one runtime generation. It must never be shared through the
	// stable result partition scope.
	ExecutionScope *resultcache.ExecutionScope
	ResultLimits   dataquery.ResultLimits
	// DependencyEvidence is immutable activation evidence used to derive an
	// exact dependency identity from each validated query plan. When it is
	// absent or incomplete, result reuse fails closed while execution remains
	// available.
	DependencyEvidence resultidentity.Evidence
	RequiredExtensions []string
	TableRelation      semanticquery.TableRelation

	Database Database
	Sources  SourcePreparer
	Resolver SourcePathResolver
	// SnapshotOnly binds this view to immutable model.* relations. It skips
	// authored source-file validation and uses source-free semantic verification
	// because committed generations may outlive source credentials/files.
	SnapshotOnly bool
	// OwnDatabase and OwnQueryCache transfer close ownership to the runtime.
	// Supplied resources are borrowed by default because project runtimes
	// commonly share a process database and cache scope.
	OwnDatabase   bool
	OwnQueryCache bool
}

type ModelTableQuery struct {
	Table       string
	Columns     []string
	Sort        []semanticquery.Sort
	ColumnMasks []semanticquery.ColumnMask
	Limit       int
	Offset      int
}

type Runtime struct {
	modelID            string
	model              *semanticmodel.Model
	planner            *semanticquery.Planner
	db                 Database
	sources            SourcePreparer
	queryCache         *queryResultCache
	resultPartition    resultidentity.Partition
	resultLimits       dataquery.ResultLimits
	dependencyEvidence resultidentity.Evidence
	requiredExtensions []string
	lastRefresh        time.Time
	sourceObservations []SourceObservation
	snapshotOnly       bool
	dbOwned            bool
	closeOnce          sync.Once
	closeErr           error
}

// LookupImmutableBytes, StoreImmutableBytes, and CoalesceImmutableBytes expose
// the serving-generation result-cache scope to byte-oriented consumers such
// as vector tiles without leaking cache implementation details.
func (r *Runtime) LookupImmutableBytes(key string) ([]byte, bool, error) {
	return r.queryCache.lookupBytes(key)
}

func (r *Runtime) StoreImmutableBytes(key string, value []byte) bool {
	return r.queryCache.storeBytes(key, value) == resultcache.StoreStored
}

func (r *Runtime) CoalesceImmutableBytes(ctx context.Context, key string, execute func(context.Context) error) (bool, error) {
	return r.queryCache.coalesceBytes(ctx, key, execute)
}

type Database interface {
	Executor
	Close() error
	Path() string
}

type arrowDatabase interface {
	QueryArrow(context.Context, semanticquery.Plan, arrowquery.Sink) error
}

type schemaDiscoverer interface {
	DiscoverSchemas(context.Context, *semanticmodel.Model) error
}

func OpenRuntime(ctx context.Context, config RuntimeConfig) (*Runtime, error) {
	runtime, err := NewRuntimeView(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := runtime.Refresh(ctx); err != nil {
		return nil, errors.Join(err, runtime.Close())
	}
	return runtime, nil
}

func NewRuntimeView(ctx context.Context, config RuntimeConfig) (runtime *Runtime, retErr error) {
	cacheOwned := (config.QueryResultCache == nil && config.ImmutableByteCache == nil) || config.OwnQueryCache
	var cache *queryResultCache
	// Ownership transfers at call entry. This defer therefore covers planner
	// compilation and validation failures that occur before a query cache is
	// constructed, as well as failures after construction.
	defer func() {
		if retErr == nil {
			return
		}
		var cleanupErr error
		if cache != nil {
			cleanupErr = cache.close()
		} else if cacheOwned {
			var resultErr, byteErr error
			if config.QueryResultCache != nil {
				resultErr = config.QueryResultCache.Close()
			}
			if config.ImmutableByteCache != nil && config.ImmutableByteCache != config.QueryResultCache {
				byteErr = config.ImmutableByteCache.Close()
			}
			cleanupErr = errors.Join(resultErr, byteErr)
		}
		var databaseErr error
		if config.OwnDatabase {
			databaseErr = closeDatabase(config.Database)
		}
		retErr = errors.Join(retErr, cleanupErr, databaseErr)
	}()
	if config.Model == nil {
		return nil, fmt.Errorf("semantic model is required")
	}
	if config.Database == nil {
		return nil, fmt.Errorf("materialization database is required")
	}
	if config.Sources == nil {
		return nil, fmt.Errorf("source preparer is required")
	}
	resolver := config.Resolver
	if resolver == nil {
		resolver = defaultSourcePathResolver{}
	}
	if !config.SnapshotOnly {
		if err := ValidateFilesWithResolver(config.Model, resolver); err != nil {
			return nil, err
		}
	}
	plannerOptions := []semanticquery.PlannerOption{}
	if config.TableRelation != nil {
		plannerOptions = append(plannerOptions, semanticquery.WithTableRelation(config.TableRelation))
	}
	planner, err := semanticquery.NewCompiledPlanner(config.Model, plannerOptions...)
	if err != nil {
		return nil, fmt.Errorf("compile semantic model: %w", err)
	}
	if !planner.IsCompiled() {
		return nil, fmt.Errorf("compiled semantic planner is required")
	}
	if config.QueryResultCache == nil && config.ImmutableByteCache == nil {
		cache = newQueryResultCache(256)
	} else if config.QueryResultCache == nil || config.ImmutableByteCache == nil {
		return nil, fmt.Errorf("query result and immutable byte cache scopes are both required")
	} else {
		execution, executionOwned := config.ExecutionScope, false
		if execution == nil {
			execution, executionOwned = resultcache.NewExecutionScope(), true
		}
		cache = newQueryResultCacheWithExecutionScope(config.QueryResultCache, config.ImmutableByteCache, execution, executionOwned)
	}
	if config.QueryResultCache != nil && config.OwnQueryCache {
		cache.ownScope()
	}
	limits := config.ResultLimits
	if limits.MaxRows <= 0 {
		limits.MaxRows = 10000
	}
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = 32 << 20
	}
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	runtime = &Runtime{
		modelID: config.ModelID, model: config.Model, planner: planner, db: config.Database,
		sources: config.Sources, requiredExtensions: normalizedExtensions(config.RequiredExtensions),
		queryCache: cache, resultPartition: config.ResultPartition,
		resultLimits: limits, dependencyEvidence: config.DependencyEvidence,
		dbOwned: config.OwnDatabase, snapshotOnly: config.SnapshotOnly,
	}
	return runtime, nil
}

func normalizedExtensions(names []string) []string {
	unique := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			unique[name] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(unique))
	for name := range unique {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	return ordered
}

func (r *Runtime) ensureRequiredExtensions(ctx context.Context) error {
	extensions, ok := r.db.(interface {
		EnsureExtension(context.Context, string) error
	})
	if !ok {
		return nil
	}
	for _, name := range r.requiredExtensions {
		if err := extensions.EnsureExtension(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) queryPlanner() (*semanticquery.Planner, error) {
	if r == nil {
		return nil, fmt.Errorf("compiled semantic planner is unavailable")
	}
	if r.planner == nil {
		return nil, fmt.Errorf("compiled semantic planner is unavailable")
	}
	return r.planner, nil
}

// Planner returns the immutable planner bound during activation. It is a
// narrow read-only port used by dashboard optimization and authorization.
func (r *Runtime) Planner() *semanticquery.Planner {
	if r == nil {
		return nil
	}
	return r.planner
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		cacheErr := r.queryCache.close()
		var dbErr error
		if r.dbOwned {
			dbErr = closeDatabase(r.db)
		}
		r.closeErr = errors.Join(cacheErr, dbErr)
	})
	return r.closeErr
}

func closeDatabase(db Database) error {
	if db == nil {
		return nil
	}
	return db.Close()
}

// CloseView releases generation-scoped cache state without closing the
// process-owned analytical database shared by other runtimes.
func (r *Runtime) CloseView() error {
	if r == nil || r.queryCache == nil {
		return nil
	}
	return r.queryCache.close()
}

func (r *Runtime) Refresh(ctx context.Context) error {
	prepared, err := r.sources.Prepare(ctx, r.model)
	if err != nil {
		return err
	}
	lastRefresh, refreshErr := Refresh(ctx, r.db, prepared, r.model)
	observations, observationErr := captureSourceObservations(ctx, prepared)
	// Materialization replaces tables one at a time. A later failure can therefore
	// leave the database changed even though the refresh did not complete.
	r.ClearQueryCache()
	err = errors.Join(refreshErr, observationErr, prepared.Close())
	if err != nil {
		return err
	}
	if discoverer, ok := r.db.(schemaDiscoverer); ok {
		if err := discoverer.DiscoverSchemas(ctx, r.model); err != nil {
			return err
		}
	}
	r.lastRefresh = lastRefresh
	r.sourceObservations = observations
	return nil
}

func (r *Runtime) RefreshModelTables(ctx context.Context, tableNames []string) error {
	prepared, err := r.sources.Prepare(ctx, r.model)
	if err != nil {
		return err
	}
	lastRefresh, refreshErr := RefreshModelTables(ctx, r.db, prepared, r.model, tableNames)
	observations, observationErr := captureSourceObservations(ctx, prepared)
	// Selected-table refreshes have the same partial-mutation behavior as full
	// refreshes, so invalidate before inspecting the terminal error.
	r.ClearQueryCache()
	err = errors.Join(refreshErr, observationErr, prepared.Close())
	if err != nil {
		return err
	}
	if discoverer, ok := r.db.(schemaDiscoverer); ok {
		if err := discoverer.DiscoverSchemas(ctx, r.model); err != nil {
			return err
		}
	}
	r.lastRefresh = lastRefresh
	r.sourceObservations = observations
	return nil
}

func captureSourceObservations(ctx context.Context, prepared PreparedSources) ([]SourceObservation, error) {
	provider, ok := prepared.(SourceObservationProvider)
	if !ok {
		return nil, nil
	}
	return provider.SourceObservations(ctx)
}

// SourceObservations returns the immutable source evidence captured during the
// most recent refresh. It is safe to call after refresh has detached its live
// source session; no authored source is re-opened here.
func (r *Runtime) SourceObservations() []SourceObservation {
	if r == nil {
		return nil
	}
	result := make([]SourceObservation, len(r.sourceObservations))
	for i, item := range r.sourceObservations {
		result[i] = item
		result[i].Schema = append([]semanticmodel.ColumnSchema(nil), item.Schema...)
		for column := range result[i].Schema {
			if result[i].Schema[column].Nullable != nil {
				nullable := *result[i].Schema[column].Nullable
				result[i].Schema[column].Nullable = &nullable
			}
		}
	}
	return result
}

// VerifySemantic prepares representative governed plans and proves all
// authored primary/unique entity claims against the discovered serving data.
// It intentionally never executes authored SQL; only planner-generated
// statements and bounded key checks are run during deployment verification.
func (r *Runtime) VerifySemantic(ctx context.Context) error {
	if r == nil || r.model == nil {
		return fmt.Errorf("semantic verification: materialization runtime is not initialized")
	}
	if r.planner == nil {
		return fmt.Errorf("semantic verification: compiled semantic planner is unavailable")
	}
	verificationModel := r.model
	if r.snapshotOnly {
		// Serving verification is source-free for reopened snapshots. The
		// execution snapshot preserves discovered model-table schemas while
		// stripping source/connection state that may no longer be available.
		verificationModel = r.model.ExecutionSnapshot()
	}
	if _, err := r.planner.PrepareRepresentativePlans(verificationModel); err != nil {
		return err
	}
	return r.VerifyEntityClaims(ctx)
}

// VerifyEntityClaims checks non-null and uniqueness for every primary and
// unique entity tuple. Queries are bounded by EXISTS/LIMIT so a pathological
// relation cannot materialize an unbounded verification result.
func (r *Runtime) VerifyEntityClaims(ctx context.Context) error {
	if r == nil || r.model == nil || r.db == nil {
		return fmt.Errorf("entity verification: materialization runtime is not initialized")
	}
	provider, ok := r.db.(analyticsresource.SessionProvider)
	if !ok {
		return fmt.Errorf("entity verification: analytical database does not support schema sessions")
	}
	lease, queryCtx, err := acquireDatabaseLease(ctx, r.db)
	if err != nil {
		return fmt.Errorf("entity verification: acquire database lease: %w", err)
	}
	if lease != nil {
		defer lease.Release()
	}
	session, err := provider.Session(queryCtx)
	if err != nil {
		return fmt.Errorf("entity verification: open database session: %w", err)
	}
	if r.planner == nil || r.planner.CompiledModel() == nil {
		return fmt.Errorf("entity verification: compiled dataset bindings are unavailable")
	}
	for _, tableName := range r.planner.CompiledModel().DatasetNames() {
		if err := queryCtx.Err(); err != nil {
			return err
		}
		dataset, ok := r.planner.Dataset(tableName)
		if !ok {
			return fmt.Errorf("entity verification table %q: compiled dataset is unavailable", tableName)
		}
		table := dataset.Table()
		relation, err := r.physicalModelTable(tableName)
		if err != nil {
			return fmt.Errorf("entity verification table %q: %w", tableName, err)
		}
		entityNames := make([]string, 0, len(table.Entities))
		for entityName, entity := range table.Entities {
			if entity.Type == "primary" || entity.Type == "unique" {
				entityNames = append(entityNames, entityName)
			}
		}
		sort.Strings(entityNames)
		for _, entityName := range entityNames {
			entity := table.Entities[entityName]
			fields := make([]string, len(entity.Fields))
			for index, field := range entity.Fields {
				if err := validateIdentifier(field); err != nil {
					return fmt.Errorf("entity verification table %q entity %q field %q: %w", tableName, entityName, field, err)
				}
				fields[index] = quoteMaterializedIdentifier(field)
			}
			for _, field := range fields {
				var found bool
				err := session.QueryRowContext(queryCtx, "SELECT EXISTS (SELECT 1 FROM "+relation+" WHERE "+field+" IS NULL LIMIT 1)").Scan(&found)
				if err != nil {
					return fmt.Errorf("entity verification table %q entity %q null check: %w", tableName, entityName, err)
				}
				if found {
					return fmt.Errorf("entity verification table %q entity %q has null key field %q", tableName, entityName, strings.Trim(field, `"`))
				}
			}
			groupFields := strings.Join(fields, ", ")
			var duplicate bool
			duplicateSQL := "SELECT EXISTS (SELECT 1 FROM (SELECT " + groupFields + " FROM " + relation + " GROUP BY " + groupFields + " HAVING COUNT(*) > 1 LIMIT 1) AS __leapview_duplicates)"
			if err := session.QueryRowContext(queryCtx, duplicateSQL).Scan(&duplicate); err != nil {
				return fmt.Errorf("entity verification table %q entity %q uniqueness check: %w", tableName, entityName, err)
			}
			if duplicate {
				return fmt.Errorf("entity verification table %q entity %q has duplicate key tuple", tableName, entityName)
			}
		}
	}
	return nil
}

func (r *Runtime) queryResultLimits() dataquery.ResultLimits {
	limits := r.resultLimits
	if limits.MaxRows <= 0 {
		limits.MaxRows = 10000
	}
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = 32 << 20
	}
	return limits
}

func (r *Runtime) ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
	if r == nil || r.db == nil {
		return dataquery.Result{}, fmt.Errorf("materialization runtime is not initialized")
	}
	ctx = dataquery.WithResultBudget(ctx, r.queryResultLimits())
	if request.ModelID == "" {
		request.ModelID = r.modelID
	}
	if r.modelID != "" && request.ModelID != "" && request.ModelID != r.modelID {
		return dataquery.Result{}, fmt.Errorf("semantic model %q is not available in runtime for %q", request.ModelID, r.modelID)
	}
	var transform dataquery.ResultTransformer
	if governor, ok := dataquery.GovernorFromContext(ctx); ok && !dataquery.GovernanceApplied(ctx) {
		governed, nextTransform, err := governor.GovernDataQuery(ctx, request)
		if err != nil {
			return dataquery.Result{Status: dataquery.StatusError, ExecutionState: dataquery.ExecutionRejected, Error: err.Error()}, err
		}
		request = governed
		transform = nextTransform
		ctx = dataquery.WithGovernanceApplied(ctx)
	}
	if err := request.Validate(); err != nil {
		return dataquery.Result{}, err
	}
	if _, ok := r.db.(arrowDatabase); !ok {
		return dataquery.Result{}, fmt.Errorf("analytical database does not support native Arrow execution")
	}
	return r.executeGovernedDataQueryArrow(ctx, request, transform)
}

// ExecuteDataQueryArrow is the native, streaming execution path for Arrow
// transports. It deliberately bypasses the retained-result cache because API
// queries are not cacheable and this contract must remain batch-streaming and
// unbuffered.
func (r *Runtime) ExecuteDataQueryArrow(ctx context.Context, request dataquery.Query, sink arrowquery.Sink) (dataquery.Result, error) {
	if r == nil || r.db == nil {
		return dataquery.Result{}, fmt.Errorf("materialization runtime is not initialized")
	}
	if sink == nil {
		return dataquery.Result{}, fmt.Errorf("Arrow sink is required")
	}
	db, ok := r.db.(arrowDatabase)
	if !ok {
		return dataquery.Result{}, fmt.Errorf("analytical database does not support native Arrow execution")
	}
	ctx = dataquery.WithResultBudget(ctx, r.queryResultLimits())
	if request.ModelID == "" {
		request.ModelID = r.modelID
	}
	if r.modelID != "" && request.ModelID != "" && request.ModelID != r.modelID {
		return dataquery.Result{}, fmt.Errorf("semantic model %q is not available in runtime for %q", request.ModelID, r.modelID)
	}
	var transform dataquery.ResultTransformer
	if governor, found := dataquery.GovernorFromContext(ctx); found && !dataquery.GovernanceApplied(ctx) {
		governed, nextTransform, err := governor.GovernDataQuery(ctx, request)
		if err != nil {
			return dataquery.Result{Status: dataquery.StatusError, ExecutionState: dataquery.ExecutionRejected, Error: err.Error()}, err
		}
		request, transform = governed, nextTransform
		ctx = dataquery.WithGovernanceApplied(ctx)
	}
	if err := request.Validate(); err != nil {
		return dataquery.Result{}, err
	}

	executePhysical := func(execCtx context.Context) (dataquery.Result, error) {
		planningStarted := time.Now()
		plan, err := r.planArrowQuery(request)
		planningMS := elapsedStageMS(planningStarted)
		if err != nil {
			return dataquery.Result{PlanningMS: planningMS}, err
		}
		lease, leasedCtx, err := acquireDatabaseLease(execCtx, r.db)
		if err != nil {
			return dataquery.Result{}, err
		}
		if lease != nil {
			defer lease.Release()
			execCtx = leasedCtx
		}
		if err := r.ensureRequiredExtensions(execCtx); err != nil {
			return dataquery.Result{PlanningMS: planningMS}, err
		}
		execCtx, connectionWait := dataquery.WithConnectionWaitCounter(execCtx)
		databaseStarted := time.Now()
		markPhysicalStatement(execCtx)
		err = db.QueryArrow(execCtx, plan, sink)
		databaseMS := elapsedStageMS(databaseStarted)
		rows, bytes := 0, int64(0)
		if budget, found := dataquery.ResultBudgetFromContext(execCtx); found {
			rows, bytes = budget.Usage()
		}
		if stats, found := sink.(arrowquery.SinkStats); found {
			rows = stats.RowsWritten()
		}
		result := dataquery.Result{
			Columns: dataquery.ColumnsFromNames(plan.Columns), SQL: plan.SQL,
			PlanningMS: planningMS, DatabaseMS: databaseMS,
			ConnectionWaitMS: connectionWait.Duration().Milliseconds(),
			RowsReturned:     rows, BytesEstimate: bytes,
		}
		if result.ConnectionWaitMS >= result.DatabaseMS {
			result.DatabaseMS = 0
		} else {
			result.DatabaseMS -= result.ConnectionWaitMS
		}
		return result, err
	}

	execCtx, statements := withPhysicalStatementCounter(ctx)
	result, err := admitPhysicalQuery(execCtx, request, executePhysical)
	if count := int(statements.Load()); count > 0 {
		dataquery.ObservePhysicalQuery(ctx, dataquery.PhysicalQueryObservation{Count: count, Result: result})
	}
	if _, found := dataquery.ResultLimitReasonOf(err); found {
		result.Status, result.ExecutionState, result.Error = dataquery.StatusError, dataquery.ExecutionFailed, err.Error()
	}
	if transform != nil {
		if transformErr := transform(&result, err); transformErr != nil {
			return dataquery.Result{Status: dataquery.StatusError, ExecutionState: dataquery.ExecutionRejected, Error: transformErr.Error()}, transformErr
		}
	}
	return result, err
}

func (r *Runtime) planArrowQuery(request dataquery.Query) (semanticquery.Plan, error) {
	planner, err := r.queryPlanner()
	if err != nil {
		return semanticquery.Plan{}, err
	}
	switch request.Kind {
	case dataquery.KindSemanticAggregate:
		return planner.Plan(semanticquery.Request{
			Dataset: request.Target, Dimensions: dataQueryFields(request.Fields), Metrics: dataQueryFields(request.Metrics),
			Time:    semanticquery.Time{Field: request.Time.Field, Grain: request.Time.Grain, Alias: request.Time.Alias},
			Filters: dataQueryFilters(request.Filters), Sort: dataQuerySorts(request.Sort),
			ColumnMasks: dataQueryColumnMasks(request.ColumnMasks), Limit: request.Limit, Offset: request.Offset,
		})
	case dataquery.KindSemanticRows:
		if request.IncludeTotal {
			return semanticquery.Plan{}, fmt.Errorf("native Arrow row queries do not include an auxiliary total")
		}
		return planner.PlanRows(semanticquery.RowRequest{
			Dataset: request.Target, Dimensions: dataQueryFields(request.Fields), Metrics: dataQueryFields(request.Metrics),
			Filters: dataQueryFilters(request.Filters), Sort: dataQuerySorts(request.Sort),
			ColumnMasks: dataQueryColumnMasks(request.ColumnMasks), Limit: request.Limit, Offset: request.Offset,
		})
	default:
		return semanticquery.Plan{}, fmt.Errorf("data query kind %q does not support native Arrow streaming", request.Kind)
	}
}

// acquireDatabaseLease is mandatory for production analytical environments.
// Lightweight pure-Go test executors do not own physical connections and are
// intentionally allowed to omit the capability.
func acquireDatabaseLease(ctx context.Context, database Database) (analyticsresource.Lease, context.Context, error) {
	provider, ok := database.(analyticsresource.Provider)
	if !ok {
		return nil, ctx, nil
	}
	lease, err := provider.Acquire(ctx)
	if err != nil {
		return nil, ctx, err
	}
	return lease, lease.Context(), nil
}

func admitPhysicalQuery(ctx context.Context, request dataquery.Query, execute func(context.Context) (dataquery.Result, error)) (dataquery.Result, error) {
	admitter, ok := workload.FromContext(ctx)
	if !ok {
		return execute(ctx)
	}
	class := workload.Interactive
	principalID := "system:query"
	var groupIDs []string
	if request.Surface == dataquery.SurfaceAgent {
		class = workload.Background
		if active, admitted := workload.CurrentRequest(ctx); admitted && active.Class == workload.Background {
			principalID = active.PrincipalID
			groupIDs = active.GroupIDs
		}
	}
	operation := request.Operation
	if operation == "" {
		operation = string(request.Kind)
	}
	lease, err := admitter.Acquire(ctx, workload.Request{Class: class, PrincipalID: principalID, GroupIDs: groupIDs, Operation: operation, EstimatedMemoryBytes: 64 << 20})
	if err != nil {
		state := dataquery.ExecutionRejected
		if reason, found := workload.ReasonOf(err); found && reason == workload.QueueTimeout {
			state = dataquery.ExecutionTimeout
		}
		result := dataquery.Result{ExecutionState: state}
		var rejection *workload.Rejection
		if errors.As(err, &rejection) {
			result.QueueWaitMS = durationMillis(rejection.QueueWait)
		}
		return result, err
	}
	defer lease.Release()
	started := time.Now()
	result, err := execute(lease.Context())
	if result.QueueWaitMS == 0 {
		result.QueueWaitMS = durationMillis(lease.QueueWait())
	}
	if result.ExecutionMS == 0 {
		result.ExecutionMS = durationMillis(time.Since(started))
	}
	if result.ExecutionState == "" {
		switch {
		case err == nil:
			result.ExecutionState = dataquery.ExecutionSucceeded
		case lease.Context().Err() == context.DeadlineExceeded:
			result.ExecutionState = dataquery.ExecutionTimeout
		case lease.Context().Err() == context.Canceled:
			result.ExecutionState = dataquery.ExecutionCanceled
		default:
			result.ExecutionState = dataquery.ExecutionFailed
		}
	}
	return result, err
}

func durationMillis(duration time.Duration) int64 {
	if duration <= 0 {
		return 0
	}
	if milliseconds := duration.Milliseconds(); milliseconds > 0 {
		return milliseconds
	}
	return 1
}

func bundleOutputColumns(bundle semanticquery.BundlePlan, id string) []string {
	for _, branch := range bundle.Branches {
		if branch.ID == id {
			columns := make([]string, len(branch.Columns))
			for i, column := range branch.Columns {
				columns[i] = column.Output
			}
			return columns
		}
	}
	return nil
}

type physicalStatementCounterContextKey struct{}

func withPhysicalStatementCounter(ctx context.Context) (context.Context, *atomic.Int64) {
	counter := &atomic.Int64{}
	return context.WithValue(ctx, physicalStatementCounterContextKey{}, counter), counter
}

func markPhysicalStatement(ctx context.Context) {
	if counter, ok := ctx.Value(physicalStatementCounterContextKey{}).(*atomic.Int64); ok && counter != nil {
		counter.Add(1)
	}
}

func observeQueryCacheOutcome(ctx context.Context, result dataquery.Result, err error) {
	outcome := result.CacheOutcome
	if err != nil {
		outcome = dataquery.CacheError
	}
	dataquery.ObserveCacheOutcome(ctx, outcome)
}

// dashboardQueryResultCacheable is deliberately explicit. API, CLI, agent,
// preview, and unclassified calls must not populate the dashboard result cache
// even if they happen to use an equivalent physical query shape.
func dashboardQueryResultCacheable(request dataquery.Query) bool {
	if request.Surface != dataquery.SurfaceDashboard {
		return false
	}
	switch request.Operation {
	case dataquery.OperationDashboardAggregate,
		dataquery.OperationDashboardRows,
		dataquery.OperationDashboardCount,
		dataquery.OperationDashboardHistogram,
		dataquery.OperationDashboardDistribution,
		dataquery.OperationDashboardFilterOptions,
		dataquery.OperationDashboardSpatialTile,
		dataquery.OperationDashboardSpatialTileBudget,
		dataquery.OperationDashboardSpatialMetadata:
		return true
	default:
		return false
	}
}

func (r *Runtime) ClearQueryCache() {
	if r != nil && r.queryCache != nil {
		r.queryCache.clear()
	}
}

const totalRowsColumn = "__leapview_total_rows"

func intFromDataQueryValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case uint:
		return int(typed)
	case uint32:
		return int(typed)
	case uint64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func elapsedStageMS(started time.Time) int64 {
	elapsed := time.Since(started).Milliseconds()
	if elapsed <= 0 {
		return 1
	}
	return elapsed
}

func (r *Runtime) modelTableQueryPlan(request ModelTableQuery) (semanticquery.Plan, error) {
	table, err := r.modelTable(request.Table)
	if err != nil {
		return semanticquery.Plan{}, err
	}
	columns, err := modelTableQueryColumns(table, request.Columns)
	if err != nil {
		return semanticquery.Plan{}, err
	}
	relation, err := r.physicalModelTable(request.Table)
	if err != nil {
		return semanticquery.Plan{}, err
	}
	var sql strings.Builder
	sql.WriteString("SELECT ")
	maskSet, err := rawColumnMaskMap(request.ColumnMasks)
	if err != nil {
		return semanticquery.Plan{}, err
	}
	for index, column := range columns {
		if index > 0 {
			sql.WriteString(", ")
		}
		if mask, ok := maskSet[strings.ToLower(request.Table+"."+column)]; ok {
			sql.WriteString(mask)
			sql.WriteString(" AS ")
			sql.WriteString(quoteMaterializedIdentifier(column))
		} else if mask, ok := maskSet[strings.ToLower(column)]; ok {
			sql.WriteString(mask)
			sql.WriteString(" AS ")
			sql.WriteString(quoteMaterializedIdentifier(column))
		} else {
			sql.WriteString(quoteMaterializedIdentifier(column))
		}
	}
	sql.WriteString("\nFROM ")
	sql.WriteString(relation)
	if len(request.Sort) > 0 {
		orderParts := []string{}
		columnSet := modelTableColumnSet(table)
		for _, sortSpec := range request.Sort {
			if !columnSet[sortSpec.Field] {
				return semanticquery.Plan{}, fmt.Errorf("model table %q does not expose sort column %q", request.Table, sortSpec.Field)
			}
			direction := strings.ToUpper(strings.TrimSpace(sortSpec.Direction))
			if direction != "ASC" && direction != "DESC" {
				return semanticquery.Plan{}, fmt.Errorf("unsupported sort direction %q", sortSpec.Direction)
			}
			orderParts = append(orderParts, quoteMaterializedIdentifier(sortSpec.Field)+" "+direction)
		}
		if len(orderParts) > 0 {
			sql.WriteString("\nORDER BY ")
			sql.WriteString(strings.Join(orderParts, ", "))
		}
	}
	if request.Limit > 0 {
		sql.WriteString(fmt.Sprintf("\nLIMIT %d", request.Limit))
	}
	if request.Offset > 0 {
		if request.Limit <= 0 {
			return semanticquery.Plan{}, fmt.Errorf("offset requires limit")
		}
		sql.WriteString(fmt.Sprintf("\nOFFSET %d", request.Offset))
	}
	return semanticquery.Plan{SQL: sql.String(), Columns: columns}, nil
}

func (r *Runtime) physicalModelTable(tableName string) (string, error) {
	// Entity verification iterates semantic dataset aliases, while the
	// activation table relation is keyed by the backing authored Model name.
	// Resolve the alias at this boundary just as the governed planner does
	// before invoking its TableRelation callback.
	if r != nil && r.planner != nil {
		if dataset, ok := r.planner.Dataset(tableName); ok {
			tableName = dataset.ModelName()
		}
	}
	quoted, err := quotedModelTableName(tableName)
	if err != nil {
		return "", err
	}
	if r != nil && r.planner != nil && r.planner.TableRelation() != nil {
		return r.planner.TableRelation()(tableName)
	}
	return "model." + quoted, nil
}

func (r *Runtime) modelTable(tableName string) (semanticmodel.Table, error) {
	if r == nil || r.model == nil {
		return semanticmodel.Table{}, fmt.Errorf("semantic model is required")
	}
	tableName = strings.TrimSpace(tableName)
	table, ok := r.model.Tables[tableName]
	if !ok {
		return semanticmodel.Table{}, fmt.Errorf("model table %q is not available in semantic model %q", tableName, r.model.Name)
	}
	return table, nil
}

func modelTableQueryColumns(table semanticmodel.Table, requested []string) ([]string, error) {
	columnSet := modelTableColumnSet(table)
	if len(requested) > 0 {
		columns := []string{}
		for _, column := range requested {
			column = strings.TrimSpace(column)
			if column == "" {
				continue
			}
			if !columnSet[column] {
				return nil, fmt.Errorf("model table does not expose column %q", column)
			}
			columns = append(columns, column)
		}
		if len(columns) > 0 {
			return columns, nil
		}
	}
	if len(table.Schema.Columns) > 0 {
		schemaColumns := append([]semanticmodel.ColumnSchema{}, table.Schema.Columns...)
		sort.SliceStable(schemaColumns, func(i, j int) bool {
			if schemaColumns[i].Ordinal != schemaColumns[j].Ordinal {
				return schemaColumns[i].Ordinal < schemaColumns[j].Ordinal
			}
			return schemaColumns[i].Name < schemaColumns[j].Name
		})
		columns := make([]string, 0, len(schemaColumns))
		for _, column := range schemaColumns {
			if column.Name != "" {
				columns = append(columns, column.Name)
			}
		}
		if len(columns) > 0 {
			return columns, nil
		}
	}
	columns := make([]string, 0, len(table.Columns))
	for name := range table.Columns {
		columns = append(columns, name)
	}
	sort.Strings(columns)
	if len(columns) == 0 {
		return nil, fmt.Errorf("model table has no columns")
	}
	return columns, nil
}

func modelTableColumnSet(table semanticmodel.Table) map[string]bool {
	columns := map[string]bool{}
	for name := range table.Columns {
		columns[name] = true
	}
	for _, column := range table.Schema.Columns {
		if column.Name != "" {
			columns[column.Name] = true
		}
	}
	return columns
}

func quotedModelTableName(tableName string) (string, error) {
	if err := validateIdentifier(tableName); err != nil {
		return "", err
	}
	return quoteMaterializedIdentifier(tableName), nil
}

func rawRelationPlan(relation string, columns []string, sort []dataquery.Sort, masks []dataquery.ColumnMask, offset, limit int) (semanticquery.Plan, error) {
	columnSet := map[string]bool{}
	for _, column := range columns {
		if err := validateIdentifier(column); err != nil {
			return semanticquery.Plan{}, err
		}
		columnSet[column] = true
	}
	var sql strings.Builder
	sql.WriteString("WITH data AS (")
	sql.WriteString(relation)
	sql.WriteString(")\nSELECT ")
	maskSet, err := rawColumnMaskMap(dataQueryColumnMasks(masks))
	if err != nil {
		return semanticquery.Plan{}, err
	}
	for index, column := range columns {
		if index > 0 {
			sql.WriteString(", ")
		}
		if mask, ok := maskSet[strings.ToLower(column)]; ok {
			sql.WriteString(mask)
			sql.WriteString(" AS ")
			sql.WriteString(quoteMaterializedIdentifier(column))
		} else {
			sql.WriteString(quoteMaterializedIdentifier(column))
		}
	}
	sql.WriteString(" FROM data")
	if len(sort) > 0 {
		parts := []string{}
		for _, sortSpec := range sort {
			if !columnSet[sortSpec.Field] {
				return semanticquery.Plan{}, fmt.Errorf("raw data does not expose sort column %q", sortSpec.Field)
			}
			direction := strings.ToUpper(strings.TrimSpace(sortSpec.Direction))
			if direction != "ASC" && direction != "DESC" {
				return semanticquery.Plan{}, fmt.Errorf("unsupported sort direction %q", sortSpec.Direction)
			}
			parts = append(parts, quoteMaterializedIdentifier(sortSpec.Field)+" "+direction)
		}
		if len(parts) > 0 {
			sql.WriteString("\nORDER BY ")
			sql.WriteString(strings.Join(parts, ", "))
		}
	}
	if limit > 0 {
		sql.WriteString(fmt.Sprintf("\nLIMIT %d", limit))
	}
	if offset > 0 {
		if limit <= 0 {
			return semanticquery.Plan{}, fmt.Errorf("offset requires limit")
		}
		sql.WriteString(fmt.Sprintf("\nOFFSET %d", offset))
	}
	return semanticquery.Plan{SQL: sql.String(), Columns: columns}, nil
}

func sourceQueryColumns(source semanticmodel.Source, requested []string) ([]string, error) {
	columnSet := sourceColumnSet(source)
	if len(requested) > 0 {
		columns := []string{}
		for _, column := range requested {
			column = strings.TrimSpace(column)
			if column == "" {
				continue
			}
			if !columnSet[column] {
				return nil, fmt.Errorf("source does not expose column %q", column)
			}
			columns = append(columns, column)
		}
		if len(columns) > 0 {
			return columns, nil
		}
	}
	if len(source.Schema.Columns) > 0 {
		schemaColumns := append([]semanticmodel.ColumnSchema{}, source.Schema.Columns...)
		sort.SliceStable(schemaColumns, func(i, j int) bool {
			if schemaColumns[i].Ordinal != schemaColumns[j].Ordinal {
				return schemaColumns[i].Ordinal < schemaColumns[j].Ordinal
			}
			return schemaColumns[i].Name < schemaColumns[j].Name
		})
		columns := make([]string, 0, len(schemaColumns))
		for _, column := range schemaColumns {
			if column.Name != "" {
				columns = append(columns, column.Name)
			}
		}
		if len(columns) > 0 {
			return columns, nil
		}
	}
	columns := make([]string, 0, len(source.Fields))
	for name := range source.Fields {
		columns = append(columns, name)
	}
	sort.Strings(columns)
	if len(columns) == 0 {
		return nil, fmt.Errorf("source has no columns")
	}
	return columns, nil
}

func sourceColumnSet(source semanticmodel.Source) map[string]bool {
	columns := map[string]bool{}
	for name := range source.Fields {
		columns[name] = true
	}
	for _, column := range source.Schema.Columns {
		if column.Name != "" {
			columns[column.Name] = true
		}
	}
	return columns
}

func sourceInModel(model *semanticmodel.Model, key string) (semanticmodel.Source, bool) {
	if model == nil {
		return semanticmodel.Source{}, false
	}
	key = strings.TrimSpace(key)
	if source, ok := model.Sources[key]; ok {
		return source, true
	}
	localKey := strings.ReplaceAll(key, ".", "_")
	if source, ok := model.Sources[localKey]; ok {
		return source, true
	}
	return semanticmodel.Source{}, false
}

func dataQueryFields(fields []dataquery.Field) []semanticquery.Field {
	out := make([]semanticquery.Field, 0, len(fields))
	for _, field := range fields {
		out = append(out, semanticquery.Field{
			Field: field.Field,
			Alias: field.Alias,
		})
	}
	return out
}

func dataQueryFilters(filters []dataquery.Filter) []semanticquery.Filter {
	out := make([]semanticquery.Filter, 0, len(filters))
	for _, filter := range filters {
		groups := make([]semanticquery.FilterGroup, 0, len(filter.Groups))
		for _, group := range filter.Groups {
			groups = append(groups, semanticquery.FilterGroup{Filters: dataQueryFilters(group.Filters)})
		}
		out = append(out, semanticquery.Filter{
			Field:    filter.Field,
			Dataset:  filter.Dataset,
			Operator: filter.Operator,
			Values:   append([]any{}, filter.Values...),
			Groups:   groups,
		})
	}
	return out
}

func dataQuerySorts(sort []dataquery.Sort) []semanticquery.Sort {
	out := make([]semanticquery.Sort, 0, len(sort))
	for _, item := range sort {
		out = append(out, semanticquery.Sort{Field: item.Field, Direction: item.Direction})
	}
	return out
}

func dataQueryColumnMasks(masks []dataquery.ColumnMask) []semanticquery.ColumnMask {
	out := make([]semanticquery.ColumnMask, 0, len(masks))
	for _, mask := range masks {
		out = append(out, semanticquery.ColumnMask{Field: mask.Field, Mask: mask.Mask})
	}
	return out
}

func rawColumnMaskMap(masks []semanticquery.ColumnMask) (map[string]string, error) {
	out := map[string]string{}
	for _, mask := range masks {
		field := strings.ToLower(strings.TrimSpace(mask.Field))
		if field == "" {
			continue
		}
		compiled, err := masking.Compile(mask.Mask)
		if err != nil {
			return nil, err
		}
		out[field] = compiled.SQL()
	}
	return out, nil
}

func dataQueryRows(rows semanticquery.Rows) []dataquery.Row {
	out := make([]dataquery.Row, 0, len(rows))
	for _, row := range rows {
		converted := dataquery.Row{}
		for key, value := range row {
			converted[key] = value
		}
		out = append(out, converted)
	}
	return out
}

func quoteMaterializedIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func (r *Runtime) LastRefresh() time.Time {
	if r == nil {
		return time.Time{}
	}
	return r.lastRefresh
}

func (r *Runtime) DBPath() string {
	if r == nil {
		return ""
	}
	return r.db.Path()
}
