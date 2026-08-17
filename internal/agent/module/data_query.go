package module

import (
	"context"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
)

func executeAggregateRows(ctx context.Context, metrics queryruntime.Metrics, modelID string, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	result, err := metrics.ExecuteDataQuery(ctx, dataquery.Query{
		ModelID: modelID,
		Kind:    dataquery.KindSemanticAggregate,
		Target:  request.Table,
		Fields:  queryFieldsToDataFields(request.Dimensions),
		Metrics: queryFieldsToDataFields(request.Metrics),
		Time:    dataquery.Time{Field: request.Time.Field, Grain: request.Time.Grain, Alias: request.Time.Alias},
		Filters: queryFiltersToDataFilters(request.Filters),
		Sort:    querySortToDataSort(request.Sort),
		Limit:   request.Limit,
		Offset:  request.Offset,
	})
	return queryRowsFromDataResult(result.Rows), err
}

func executePreviewRows(ctx context.Context, metrics queryruntime.Metrics, modelID string, request reportdef.RowQuery) (reportdef.QueryRows, error) {
	result, err := metrics.ExecuteDataQuery(ctx, dataquery.Query{
		ModelID: modelID,
		Kind:    dataquery.KindSemanticRows,
		Target:  request.Table,
		Fields:  queryFieldsToDataFields(request.Dimensions),
		Metrics: queryFieldsToDataFields(request.Metrics),
		Filters: queryFiltersToDataFilters(request.Filters),
		Sort:    querySortToDataSort(request.Sort),
		Limit:   request.Limit,
		Offset:  request.Offset,
	})
	return queryRowsFromDataResult(result.Rows), err
}

func executeHistogram(ctx context.Context, metrics queryruntime.Metrics, modelID string, request reportdef.RawValueQuery, binCount int) ([]reportdef.HistogramBin, error) {
	result, err := metrics.ExecuteDataQuery(ctx, dataquery.SemanticHistogram(
		modelID, request.Table, queryFieldsToDataFields(request.Dimensions),
		dataquery.Field{Field: request.Metric.Field, Alias: request.Metric.Alias}, queryFiltersToDataFilters(request.Filters), binCount,
	))
	if err != nil {
		return nil, err
	}
	bins := make([]reportdef.HistogramBin, 0, len(result.Rows))
	for _, row := range result.Rows {
		bins = append(bins, reportdef.HistogramBin{
			Bucket: int(dataQueryNumber(row["bucket"])), Count: int(dataQueryNumber(row["count"])),
			Start: dataQueryNumber(row["start"]), End: dataQueryNumber(row["end"]),
		})
	}
	return bins, nil
}

func executeDistribution(ctx context.Context, metrics queryruntime.Metrics, modelID string, request reportdef.RawValueQuery, sort []reportdef.QuerySort, limit int) (reportdef.QueryRows, error) {
	result, err := metrics.ExecuteDataQuery(ctx, dataquery.SemanticDistribution(
		modelID, request.Table, queryFieldsToDataFields(request.Dimensions),
		dataquery.Field{Field: request.Metric.Field, Alias: request.Metric.Alias}, queryFiltersToDataFilters(request.Filters), querySortToDataSort(sort), limit,
	))
	return queryRowsFromDataResult(result.Rows), err
}

func dataQueryNumber(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int32:
		return float64(typed)
	case int64:
		return float64(typed)
	default:
		return 0
	}
}

func queryFieldsToDataFields(fields []reportdef.QueryField) []dataquery.Field {
	out := make([]dataquery.Field, 0, len(fields))
	for _, field := range fields {
		out = append(out, dataquery.Field{
			Field: field.Field,
			Alias: field.Alias,
		})
	}
	return out
}

func queryFiltersToDataFilters(filters []reportdef.QueryFilter) []dataquery.Filter {
	out := make([]dataquery.Filter, 0, len(filters))
	for _, filter := range filters {
		groups := make([]dataquery.FilterGroup, 0, len(filter.Groups))
		for _, group := range filter.Groups {
			groups = append(groups, dataquery.FilterGroup{Filters: queryFiltersToDataFilters(group.Filters)})
		}
		out = append(out, dataquery.Filter{
			Field:    filter.Field,
			Fact:     filter.Fact,
			Operator: filter.Operator,
			Values:   append([]any{}, filter.Values...),
			Groups:   groups,
		})
	}
	return out
}

func querySortToDataSort(sort []reportdef.QuerySort) []dataquery.Sort {
	out := make([]dataquery.Sort, 0, len(sort))
	for _, item := range sort {
		out = append(out, dataquery.Sort{Field: item.Field, Direction: item.Direction})
	}
	return out
}

func queryRowsFromDataResult(rows []dataquery.Row) reportdef.QueryRows {
	out := make(reportdef.QueryRows, 0, len(rows))
	for _, row := range rows {
		converted := reportdef.QueryRow{}
		for key, value := range row {
			converted[key] = value
		}
		out = append(out, converted)
	}
	return out
}
