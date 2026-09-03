package materialize

import (
	"context"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/analytics/arrowdecode"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/query/planir"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	"github.com/flidai/leapview/pkg/arrowresult"
)

type plannedArrowQuery struct {
	plan          semanticquery.Plan
	countPlan     *semanticquery.Plan
	planningMS    int64
	countOnly     bool
	totalFromData bool
	dependency    resultidentity.Dependency
	reusable      bool
}

func rowPlanWithTotal(plan semanticquery.Plan) (semanticquery.Plan, error) {
	graph, err := planir.WithTotalRows(plan.IR, totalRowsColumn)
	if err != nil {
		return semanticquery.Plan{}, fmt.Errorf("add total rows to row plan: %w", err)
	}
	rendered, err := planir.RenderDuckDB(graph)
	if err != nil {
		return semanticquery.Plan{}, fmt.Errorf("render total rows plan: %w", err)
	}
	plan.IR = graph
	plan.SQL = rendered.SQL
	plan.Args = rendered.Args
	plan.Columns = rendered.Columns
	return plan, nil
}

func (r *Runtime) executeGovernedDataQueryArrow(ctx context.Context, request dataquery.Query, transform dataquery.ResultTransformer) (dataquery.Result, error) {
	cacheStarted := cacheObservationStarted(ctx, time.Now())
	cacheable := dashboardQueryResultCacheable(request)
	var planned plannedArrowQuery
	var planErr error
	admissionReason := dataquery.CacheAdmissionReasonQueryNotCacheable
	if !cacheable {
		observeQueryCacheAdmission(ctx, dataquery.CacheAdmissionBypassed, admissionReason)
	}
	if cacheable {
		planned, planErr = r.planOwnedArrowQuery(request)
		if planErr == nil {
			planned.dependency, planned.reusable = r.dependencyForPlan(planned.plan)
		}
		switch {
		case planErr != nil:
			admissionReason = dataquery.CacheAdmissionReasonPlanningFailed
			observeQueryCacheAdmission(ctx, dataquery.CacheAdmissionRejected, admissionReason)
		case !planCacheDeterministic(r.model, planned.plan):
			// Opaque or volatile plans, and plans whose participating datasets
			// cannot be resolved to safe materialized tables, carry no positive
			// cache-admission evidence.
			admissionReason = dataquery.CacheAdmissionReasonNonDeterministic
			observeQueryCacheAdmission(ctx, dataquery.CacheAdmissionBypassed, admissionReason)
		case !planned.reusable:
			admissionReason = dataquery.CacheAdmissionReasonDependencyUnavailable
			observeQueryCacheAdmission(ctx, dataquery.CacheAdmissionBypassed, admissionReason)
		default:
			admissionReason = queryCacheIdentityReason(request, r.resultPartition, planned.dependency)
			decision := dataquery.CacheAdmissionBypassed
			if admissionReason == dataquery.CacheAdmissionReasonEligible {
				decision = dataquery.CacheAdmissionEligible
			}
			observeQueryCacheAdmission(ctx, decision, admissionReason)
		}
	}
	execute := func(executionCtx context.Context) (arrowQueryExecution, error) {
		var execution arrowQueryExecution
		current := planned
		execCtx, statements := withPhysicalStatementCounter(dataquery.WithResultBudget(executionCtx, r.queryResultLimits()))
		summary, err := admitPhysicalQuery(execCtx, request, func(queryCtx context.Context) (dataquery.Result, error) {
			if !cacheable {
				var planningErr error
				current, planningErr = r.planOwnedArrowQuery(request)
				if planningErr != nil {
					return dataquery.Result{PlanningMS: current.planningMS}, planningErr
				}
			}
			lease, leasedCtx, acquireErr := acquireDatabaseLease(queryCtx, r.db)
			if acquireErr != nil {
				return dataquery.Result{PlanningMS: current.planningMS}, acquireErr
			}
			if lease != nil {
				defer lease.Release()
				queryCtx = leasedCtx
			}
			if extensionErr := r.ensureRequiredExtensions(queryCtx); extensionErr != nil {
				return dataquery.Result{PlanningMS: current.planningMS}, extensionErr
			}
			queryCtx, connectionWait := dataquery.WithConnectionWaitCounter(queryCtx)
			databaseStarted := time.Now()
			data, queryErr := r.captureArrowPlan(queryCtx, current.plan)
			databaseMS := elapsedStageMS(databaseStarted)
			waitMS := connectionWait.Duration().Milliseconds()
			if waitMS >= databaseMS {
				databaseMS = 0
			} else {
				databaseMS -= waitMS
			}
			execution.data = data
			execution.metadata = resultcache.Metadata{SQL: current.plan.SQL}
			result := dataquery.Result{SQL: current.plan.SQL, PlanningMS: current.planningMS, ConnectionWaitMS: waitMS, DatabaseMS: databaseMS}
			if queryErr != nil {
				return result, queryErr
			}
			if total, known, extractErr := arrowResultTotal(request, data, current.countOnly || current.totalFromData); extractErr != nil {
				return result, extractErr
			} else if known {
				execution.metadata.TotalRows = total
				execution.metadata.TotalRowsKnown = true
			}
			if request.IncludeTotal && !execution.metadata.TotalRowsKnown && current.countPlan != nil {
				countStarted := time.Now()
				countData, countErr := r.captureArrowPlan(queryCtx, *current.countPlan)
				result.DatabaseMS += elapsedStageMS(countStarted)
				if countErr != nil {
					return result, countErr
				}
				total, known, extractErr := arrowResultTotal(request, countData, true)
				countData.Release()
				if extractErr != nil {
					return result, extractErr
				}
				execution.metadata.TotalRows, execution.metadata.TotalRowsKnown = total, known
			}
			return result, nil
		})
		execution.summary = summary
		if count := int(statements.Load()); count > 0 {
			dataquery.ObservePhysicalQuery(ctx, dataquery.PhysicalQueryObservation{Count: count, Result: summary})
		}
		return execution, err
	}

	var result dataquery.Result
	err := planErr
	if planErr != nil {
		// Planning used to run inside admitPhysicalQuery, which classified an
		// unsuccessful attempt as an execution failure. Preserve that public
		// classification even though planning now precedes cache eligibility.
		result = dataquery.Result{PlanningMS: planned.planningMS, ExecutionState: dataquery.ExecutionFailed}
	} else if cacheable && admissionReason == dataquery.CacheAdmissionReasonEligible {
		result, err = r.queryCache.executeArrow(ctx, request, r.resultPartition, planned.dependency, planned.plan.SQL, cacheStarted, execute)
		observeQueryCacheOutcome(ctx, result, err)
	} else {
		execution, executeErr := execute(ctx)
		err = executeErr
		if execution.data != nil {
			lease, acquireErr := execution.data.Acquire()
			if acquireErr == nil {
				result, acquireErr = decodeArrowQueryResult(request, lease, execution.metadata, execution.summary)
				lease.Release()
			}
			execution.data.Release()
			if err == nil {
				err = acquireErr
			}
		} else {
			result = execution.summary
		}
		if cacheable {
			result.CacheOutcome = dataquery.CacheMiss
			observeQueryCacheOutcome(ctx, result, err)
			outcome := dataquery.CacheObservationMiss
			if err != nil {
				outcome = dataquery.CacheObservationError
			}
			observeTypedCacheFinal(ctx, outcome, time.Since(cacheStarted))
		}
	}
	if planErr != nil && cacheable {
		observeQueryCacheOutcome(ctx, result, err)
		observeTypedCacheFinal(ctx, dataquery.CacheObservationError, time.Since(cacheStarted))
	}
	if _, ok := dataquery.ResultLimitReasonOf(err); ok {
		return dataquery.Result{Status: dataquery.StatusError, ExecutionState: dataquery.ExecutionFailed, Error: err.Error()}, err
	}
	if transform != nil {
		if transformErr := transform(&result, err); transformErr != nil {
			return dataquery.Result{Status: dataquery.StatusError, ExecutionState: dataquery.ExecutionRejected, Error: transformErr.Error()}, transformErr
		}
	}
	return result, err
}

func (r *Runtime) dependencyForPlan(plan semanticquery.Plan) (resultidentity.Dependency, bool) {
	if r == nil || !r.dependencyEvidence.Available() {
		return resultidentity.Dependency{}, false
	}
	projection, err := plan.ResultDependencies()
	if err != nil {
		return resultidentity.Dependency{}, false
	}
	dependency, err := r.dependencyEvidence.Dependency(r.dependencyPlanInput(projection))
	if err != nil {
		return resultidentity.Dependency{}, false
	}
	return dependency, true
}

func (r *Runtime) captureArrowPlan(ctx context.Context, plan semanticquery.Plan) (*arrowresult.Result, error) {
	db, ok := r.db.(arrowDatabase)
	if !ok {
		return nil, fmt.Errorf("analytical database does not support native Arrow execution")
	}
	collector := arrowresult.NewBuilder()
	markPhysicalStatement(ctx)
	if err := db.QueryArrow(ctx, plan, collector); err != nil {
		collector.Abort()
		return nil, err
	}
	return collector.Finish()
}

func (r *Runtime) planOwnedArrowQuery(request dataquery.Query) (plannedArrowQuery, error) {
	started := time.Now()
	planner, plannerErr := r.queryPlanner()
	if plannerErr != nil {
		return plannedArrowQuery{}, plannerErr
	}
	var planned plannedArrowQuery
	var err error
	switch request.Kind {
	case dataquery.KindSemanticAggregate:
		planned.plan, err = planner.Plan(semanticquery.Request{
			Dataset: request.Target, Dimensions: dataQueryFields(request.Fields), Metrics: dataQueryFields(request.Metrics),
			Time:    semanticquery.Time{Field: request.Time.Field, Grain: request.Time.Grain, Alias: request.Time.Alias},
			Filters: dataQueryFilters(request.Filters), Sort: dataQuerySorts(request.Sort),
			ColumnMasks: dataQueryColumnMasks(request.ColumnMasks), Limit: request.Limit, Offset: request.Offset,
		})
	case dataquery.KindSemanticRows:
		if len(request.Fields) == 0 && len(request.Metrics) == 0 && request.IncludeTotal {
			if len(request.ColumnMasks) > 0 {
				err = fmt.Errorf("table count is unavailable because its authorization projection contains masked fields")
				break
			}
			planned.countOnly = true
			planned.plan, err = planner.PlanCount(semanticquery.CountRequest{Dataset: request.Target, Filters: dataQueryFilters(request.Filters)})
			break
		}
		planned.plan, err = planner.PlanRows(semanticquery.RowRequest{
			Dataset: request.Target, Dimensions: dataQueryFields(request.Fields), Metrics: dataQueryFields(request.Metrics),
			Filters: dataQueryFilters(request.Filters), Sort: dataQuerySorts(request.Sort),
			ColumnMasks: dataQueryColumnMasks(request.ColumnMasks), Limit: request.Limit, Offset: request.Offset,
		})
		if err == nil && request.IncludeTotal {
			planned.plan, err = rowPlanWithTotal(planned.plan)
			planned.totalFromData = true
			if err == nil {
				count, countErr := planner.PlanCount(semanticquery.CountRequest{Dataset: request.Target, Filters: dataQueryFilters(request.Filters)})
				if countErr != nil {
					err = countErr
				} else {
					planned.countPlan = &count
				}
			}
		}
	case dataquery.KindModelTableRows:
		planned.plan, err = r.modelTableQueryPlan(ModelTableQuery{Table: request.Target, Columns: dataquery.FieldNames(request.Fields), Sort: dataQuerySorts(request.Sort), ColumnMasks: dataQueryColumnMasks(request.ColumnMasks), Limit: request.Limit, Offset: request.Offset})
		if err == nil && request.IncludeTotal {
			relation, relationErr := r.physicalModelTable(request.Target)
			if relationErr != nil {
				err = relationErr
			} else {
				count := semanticquery.Plan{SQL: "SELECT count(*) AS value FROM " + relation, Columns: []string{"value"}}
				planned.countPlan = &count
			}
		}
	case dataquery.KindSemanticHistogram:
		histogramOptions := semanticquery.HistogramOptions{}
		if request.Histogram != nil {
			histogramOptions.NullPolicy = request.Histogram.NullPolicy
			histogramOptions.Approximation = request.Histogram.Approximation
			if request.Histogram.DomainMinimum != nil && request.Histogram.DomainMaximum != nil {
				histogramOptions.Domain = &semanticquery.HistogramDomain{Minimum: *request.Histogram.DomainMinimum, Maximum: *request.Histogram.DomainMaximum}
			}
		}
		planned.plan, err = planner.PlanHistogram(semanticquery.RawValueRequest{Dataset: request.Target, Dimensions: dataQueryFields(request.Fields), Metric: dataQueryFields([]dataquery.Field{request.Value})[0], Filters: dataQueryFilters(request.Filters), ColumnMasks: dataQueryColumnMasks(request.ColumnMasks)}, request.BinCount, histogramOptions)
	case dataquery.KindSemanticDistribution:
		distributionOptions := semanticquery.DistributionOptions{}
		if request.Distribution != nil {
			distributionOptions.Quantiles = append([]float64(nil), request.Distribution.Quantiles...)
			distributionOptions.Outliers = request.Distribution.Outliers
			distributionOptions.Approximation = request.Distribution.Approximation
			if request.Distribution.WhiskerLower != nil && request.Distribution.WhiskerUpper != nil {
				distributionOptions.Whiskers = &semanticquery.DistributionWhiskers{Lower: *request.Distribution.WhiskerLower, Upper: *request.Distribution.WhiskerUpper}
			}
		}
		planned.plan, err = planner.PlanDistribution(semanticquery.RawValueRequest{Dataset: request.Target, Dimensions: dataQueryFields(request.Fields), Metric: dataQueryFields([]dataquery.Field{request.Value})[0], Filters: dataQueryFilters(request.Filters), ColumnMasks: dataQueryColumnMasks(request.ColumnMasks)}, dataQuerySorts(request.Sort), request.Limit, distributionOptions)
	case dataquery.KindSemanticSpatialTile:
		if request.SpatialTile == nil {
			err = fmt.Errorf("semantic spatial tile query requires tile coordinates")
			break
		}
		tile := request.SpatialTile
		if tile.Precision == dataquery.SpatialTilePrecisionRaw {
			planned.plan, err = planner.PlanSpatialTileRaw(semanticquery.SpatialTileRawRequest{
				Dataset: request.Target, Dimensions: dataQueryFields(request.Fields), Metrics: dataQueryFields(request.Metrics), Identity: dataQueryFields(tile.Identity),
				Time:    semanticquery.Time{Field: request.Time.Field, Grain: request.Time.Grain, Alias: request.Time.Alias},
				Filters: dataQueryFilters(request.Filters), ColumnMasks: dataQueryColumnMasks(request.ColumnMasks),
				Latitude: semanticquery.Field{Field: tile.Latitude.Field, Alias: tile.Latitude.Alias}, Longitude: semanticquery.Field{Field: tile.Longitude.Field, Alias: tile.Longitude.Alias},
				Zoom: tile.Zoom, MetatileX: tile.MetatileX, MetatileY: tile.MetatileY, MetatileSize: tile.MetatileSize, FeatureCap: tile.FeatureCap, Buffer: tile.Buffer,
			})
		} else {
			planned.plan, err = planner.PlanSpatialTileAggregate(semanticquery.SpatialTileRequest{
				Dataset: request.Target, Metrics: dataQueryFields(request.Metrics), Filters: dataQueryFilters(request.Filters), ColumnMasks: dataQueryColumnMasks(request.ColumnMasks),
				Latitude: semanticquery.Field{Field: tile.Latitude.Field, Alias: tile.Latitude.Alias}, Longitude: semanticquery.Field{Field: tile.Longitude.Field, Alias: tile.Longitude.Alias},
				Zoom: tile.Zoom, TargetZoom: tile.TargetZoom, MetatileX: tile.MetatileX, MetatileY: tile.MetatileY, MetatileSize: tile.MetatileSize, CellPixels: tile.CellPixels, Buffer: tile.Buffer,
			})
		}
	case dataquery.KindSemanticSpatialTileBudget:
		if request.SpatialTileBudget == nil {
			err = fmt.Errorf("semantic spatial tile budget query requires a budget probe")
			break
		}
		budget := request.SpatialTileBudget
		planned.plan, err = planner.PlanSpatialTileBudget(semanticquery.SpatialTileBudgetRequest{
			Dataset: request.Target, Dimensions: dataQueryFields(request.Fields), Metrics: dataQueryFields(request.Metrics), Identity: dataQueryFields(budget.Identity),
			Time:    semanticquery.Time{Field: request.Time.Field, Grain: request.Time.Grain, Alias: request.Time.Alias},
			Filters: dataQueryFilters(request.Filters), ColumnMasks: dataQueryColumnMasks(request.ColumnMasks),
			Latitude: semanticquery.Field{Field: budget.Latitude.Field, Alias: budget.Latitude.Alias}, Longitude: semanticquery.Field{Field: budget.Longitude.Field, Alias: budget.Longitude.Alias},
			Zoom: budget.Zoom, FeatureCap: budget.FeatureCap, MaximumBytes: budget.MaximumBytes, Buffer: budget.Buffer,
		})
	case dataquery.KindSemanticSpatialMetadata:
		if request.SpatialMetadata == nil {
			err = fmt.Errorf("semantic spatial metadata query requires coordinates")
			break
		}
		planned.plan, err = planner.PlanSpatialMetadata(semanticquery.SpatialMetadataRequest{
			Dataset: request.Target, Metrics: dataQueryFields(request.Metrics), Filters: dataQueryFilters(request.Filters), ColumnMasks: dataQueryColumnMasks(request.ColumnMasks),
			Latitude: semanticquery.Field{Field: request.SpatialMetadata.Latitude.Field, Alias: request.SpatialMetadata.Latitude.Alias}, Longitude: semanticquery.Field{Field: request.SpatialMetadata.Longitude.Field, Alias: request.SpatialMetadata.Longitude.Alias},
			FeatureCap: request.SpatialMetadata.FeatureCap, RawMinimumZoom: request.SpatialMetadata.RawMinimumZoom, MaximumZoom: request.SpatialMetadata.MaximumZoom,
		})
	default:
		err = fmt.Errorf("unsupported data query kind %q", request.Kind)
	}
	planned.planningMS = elapsedStageMS(started)
	return planned, err
}

func arrowResultTotal(request dataquery.Query, result *arrowresult.Result, expected bool) (int, bool, error) {
	if result == nil || !expected {
		return 0, false, nil
	}
	lease, err := result.Acquire()
	if err != nil {
		return 0, false, err
	}
	defer lease.Release()
	rows, err := arrowdecode.DecodeRows(lease)
	if err != nil {
		return 0, false, err
	}
	if len(rows) == 0 {
		return 0, request.Offset == 0, nil
	}
	value, ok := rows[0][totalRowsColumn]
	if !ok {
		value = rows[0]["value"]
	}
	return intFromDataQueryValue(value), true, nil
}

func decodeArrowQueryResult(request dataquery.Query, lease *arrowresult.Lease, metadata resultcache.Metadata, summary dataquery.Result) (dataquery.Result, error) {
	rows, err := arrowdecode.DecodeRows(lease)
	if err != nil {
		return dataquery.Result{}, err
	}
	countOnly := request.Kind == dataquery.KindSemanticRows && len(request.Fields) == 0 && len(request.Metrics) == 0 && request.IncludeTotal
	if countOnly {
		rows = nil
	}
	transportTotalColumn := ""
	if request.IncludeTotal {
		transportTotalColumn = totalRowsColumn
	}
	if transportTotalColumn != "" {
		for _, row := range rows {
			delete(row, transportTotalColumn)
		}
	}
	columns := make([]string, 0)
	if schema := lease.Schema(); schema != nil && !countOnly {
		for _, field := range schema.Fields() {
			if field.Name != transportTotalColumn {
				columns = append(columns, field.Name)
			}
		}
	}
	result := summary
	result.SQL = metadata.SQL
	result.Columns = dataquery.ColumnsFromNames(columns)
	result.Rows = make([]dataquery.Row, len(rows))
	for index := range rows {
		result.Rows[index] = dataquery.Row(rows[index])
	}
	result.TotalRows, result.TotalRowsKnown = metadata.TotalRows, metadata.TotalRowsKnown
	result.Warnings = append([]string{}, metadata.Warnings...)
	result.RowsReturned = len(result.Rows)
	result.BytesEstimate = lease.Bytes()
	if result.ExecutionState == "" {
		result.ExecutionState = dataquery.ExecutionSucceeded
	}
	if result.Status == "" {
		result.Status = dataquery.StatusSuccess
	}
	return result, nil
}
