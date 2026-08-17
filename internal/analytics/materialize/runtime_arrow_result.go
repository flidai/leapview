package materialize

import (
	"context"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/analytics/arrowresult"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/resultcache"
)

type plannedArrowQuery struct {
	plan          semanticquery.Plan
	countPlan     *semanticquery.Plan
	planningMS    int64
	countOnly     bool
	totalFromData bool
}

func (r *Runtime) executeGovernedDataQueryArrow(ctx context.Context, request dataquery.Query, transform dataquery.ResultTransformer) (dataquery.Result, error) {
	execute := func() (arrowQueryExecution, error) {
		var execution arrowQueryExecution
		execCtx, statements := withPhysicalStatementCounter(dataquery.WithResultBudget(ctx, r.queryResultLimits()))
		summary, err := admitPhysicalQuery(execCtx, request, func(queryCtx context.Context) (dataquery.Result, error) {
			planned, planErr := r.planOwnedArrowQuery(request)
			if planErr != nil {
				return dataquery.Result{PlanningMS: planned.planningMS}, planErr
			}
			lease, leasedCtx, acquireErr := acquireDatabaseLease(queryCtx, r.db)
			if acquireErr != nil {
				return dataquery.Result{PlanningMS: planned.planningMS}, acquireErr
			}
			if lease != nil {
				defer lease.Release()
				queryCtx = leasedCtx
			}
			if extensionErr := r.ensureRequiredExtensions(queryCtx); extensionErr != nil {
				return dataquery.Result{PlanningMS: planned.planningMS}, extensionErr
			}
			queryCtx, connectionWait := dataquery.WithConnectionWaitCounter(queryCtx)
			databaseStarted := time.Now()
			data, queryErr := r.captureArrowPlan(queryCtx, planned.plan)
			databaseMS := elapsedStageMS(databaseStarted)
			waitMS := connectionWait.Duration().Milliseconds()
			if waitMS >= databaseMS {
				databaseMS = 0
			} else {
				databaseMS -= waitMS
			}
			execution.data = data
			execution.metadata = resultcache.Metadata{SQL: planned.plan.SQL}
			result := dataquery.Result{SQL: planned.plan.SQL, PlanningMS: planned.planningMS, ConnectionWaitMS: waitMS, DatabaseMS: databaseMS}
			if queryErr != nil {
				return result, queryErr
			}
			if total, known, extractErr := arrowResultTotal(request, data, planned.countOnly || planned.totalFromData); extractErr != nil {
				return result, extractErr
			} else if known {
				execution.metadata.TotalRows = total
				execution.metadata.TotalRowsKnown = true
			}
			if request.IncludeTotal && !execution.metadata.TotalRowsKnown && planned.countPlan != nil {
				countStarted := time.Now()
				countData, countErr := r.captureArrowPlan(queryCtx, *planned.countPlan)
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
	var err error
	if dashboardQueryResultCacheable(request) {
		result, err = r.queryCache.executeArrow(ctx, request, execute)
		observeQueryCacheOutcome(ctx, result, err)
	} else {
		execution, executeErr := execute()
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
		planned.plan, err = planner.PlanHistogram(semanticquery.RawValueRequest{Dataset: request.Target, Dimensions: dataQueryFields(request.Fields), Metric: dataQueryFields([]dataquery.Field{request.Value})[0], Filters: dataQueryFilters(request.Filters), ColumnMasks: dataQueryColumnMasks(request.ColumnMasks)}, request.BinCount)
	case dataquery.KindSemanticDistribution:
		planned.plan, err = planner.PlanDistribution(semanticquery.RawValueRequest{Dataset: request.Target, Dimensions: dataQueryFields(request.Fields), Metric: dataQueryFields([]dataquery.Field{request.Value})[0], Filters: dataQueryFilters(request.Filters), ColumnMasks: dataQueryColumnMasks(request.ColumnMasks)}, dataQuerySorts(request.Sort), request.Limit)
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
	rows, err := arrowresult.DecodeRows(lease)
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
	rows, err := arrowresult.DecodeRows(lease)
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
