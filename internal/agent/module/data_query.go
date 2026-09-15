package module

import (
	"context"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard/querymap"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
)

func executeAggregateRows(ctx context.Context, metrics queryruntime.Metrics, modelID string, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	result, err := metrics.ExecuteDataQuery(ctx, dataquery.Query{
		ModelID: modelID,
		Kind:    dataquery.KindSemanticAggregate,
		Target:  request.Dataset,
		Fields:  querymap.Fields(request.Dimensions),
		Metrics: querymap.Fields(request.Metrics),
		Time:    dataquery.Time{Field: request.Time.Field, Grain: request.Time.Grain, Alias: request.Time.Alias},
		Filters: querymap.Filters(request.Filters),
		Sort:    querymap.Sorts(request.Sort),
		Limit:   request.Limit,
		Offset:  request.Offset,
	})
	return queryRowsFromDataResult(result.Rows), err
}

func executePreviewRows(ctx context.Context, metrics queryruntime.Metrics, modelID string, request reportdef.RowQuery) (reportdef.QueryRows, error) {
	result, err := metrics.ExecuteDataQuery(ctx, dataquery.Query{
		ModelID: modelID,
		Kind:    dataquery.KindSemanticRows,
		Target:  request.Dataset,
		Fields:  querymap.Fields(request.Dimensions),
		Metrics: querymap.Fields(request.Metrics),
		Filters: querymap.Filters(request.Filters),
		Sort:    querymap.Sorts(request.Sort),
		Limit:   request.Limit,
		Offset:  request.Offset,
	})
	return queryRowsFromDataResult(result.Rows), err
}

func executeHistogram(ctx context.Context, metrics queryruntime.Metrics, modelID string, request reportdef.RawValueQuery, binCount int) ([]reportdef.HistogramBin, error) {
	result, err := metrics.ExecuteDataQuery(ctx, dataquery.SemanticHistogram(
		modelID, request.Dataset, querymap.Fields(request.Dimensions),
		querymap.Field(request.Metric), querymap.Filters(request.Filters), binCount,
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
		modelID, request.Dataset, querymap.Fields(request.Dimensions),
		querymap.Field(request.Metric), querymap.Filters(request.Filters), querymap.Sorts(sort), limit,
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
