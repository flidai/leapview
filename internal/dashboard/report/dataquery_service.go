package report

import (
	"context"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard/querymap"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type DataQueryExecutor interface {
	ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error)
}

type dataQueryService struct {
	projectID projectgraph.ResourceID
	modelID   string
	executor  DataQueryExecutor
}

func NewDataQueryService(projectID projectgraph.ResourceID, modelID string, executor DataQueryExecutor) DataService {
	return dataQueryService{projectID: projectID, modelID: modelID, executor: executor}
}

func (s dataQueryService) Query(ctx context.Context, request AggregateQuery) (QueryRows, error) {
	result, err := s.executor.ExecuteDataQuery(ctx, dataquery.Query{
		ProjectID: s.projectID,
		Surface:   dataquery.SurfaceDashboard,
		Operation: dataquery.OperationDashboardAggregate,
		ModelID:   s.modelID,
		Kind:      dataquery.KindSemanticAggregate,
		Target:    request.Dataset,
		Fields:    querymap.Fields(request.Dimensions),
		Metrics:   querymap.Fields(request.Metrics),
		Time:      dataquery.Time{Field: request.Time.Field, Grain: request.Time.Grain, Alias: request.Time.Alias},
		Filters:   querymap.Filters(request.Filters),
		Sort:      querymap.Sorts(request.Sort),
		Limit:     request.Limit,
		Offset:    request.Offset,
	})
	return rowsFromDataQuery(result.Rows), err
}

func (s dataQueryService) Rows(ctx context.Context, request RowQuery) (QueryRows, error) {
	result, err := s.executor.ExecuteDataQuery(ctx, dataquery.Query{
		ProjectID: s.projectID,
		Surface:   dataquery.SurfaceDashboard,
		Operation: dataquery.OperationDashboardRows,
		ModelID:   s.modelID,
		Kind:      dataquery.KindSemanticRows,
		Target:    request.Dataset,
		Fields:    querymap.Fields(request.Dimensions),
		Metrics:   querymap.Fields(request.Metrics),
		Filters:   querymap.Filters(request.Filters),
		Sort:      querymap.Sorts(request.Sort),
		Limit:     request.Limit,
		Offset:    request.Offset,
	})
	return rowsFromDataQuery(result.Rows), err
}

func (s dataQueryService) Count(ctx context.Context, request CountQuery) (int, error) {
	result, err := s.executor.ExecuteDataQuery(ctx, dataquery.Query{
		ProjectID:    s.projectID,
		Surface:      dataquery.SurfaceDashboard,
		Operation:    dataquery.OperationDashboardCount,
		ModelID:      s.modelID,
		Kind:         dataquery.KindSemanticRows,
		Target:       request.Dataset,
		Filters:      querymap.Filters(request.Filters),
		Limit:        1,
		IncludeTotal: true,
	})
	if err != nil {
		return 0, err
	}
	return result.TotalRows, nil
}

func (s dataQueryService) Histogram(ctx context.Context, request RawValueQuery, binCount int) ([]HistogramBin, error) {
	var options *dataquery.HistogramOptions
	if request.Histogram != nil {
		options = &dataquery.HistogramOptions{NullPolicy: request.Histogram.NullPolicy, Approximation: request.Histogram.Approximation}
		if request.Histogram.Domain != nil {
			minimum, maximum := request.Histogram.Domain.Minimum, request.Histogram.Domain.Maximum
			options.DomainMinimum, options.DomainMaximum = &minimum, &maximum
		}
	}
	result, err := s.executor.ExecuteDataQuery(ctx, dataquery.Query{
		ProjectID: s.projectID,
		Surface:   dataquery.SurfaceDashboard,
		Operation: dataquery.OperationDashboardHistogram,
		ModelID:   s.modelID,
		Kind:      dataquery.KindSemanticHistogram,
		Target:    request.Dataset,
		Fields:    querymap.Fields(request.Dimensions),
		Value:     querymap.Field(request.Metric),
		Filters:   querymap.Filters(request.Filters),
		BinCount:  binCount,
		Histogram: options,
	})
	if err != nil {
		return nil, err
	}
	bins := make([]HistogramBin, 0, len(result.Rows))
	for _, row := range result.Rows {
		bins = append(bins, HistogramBin{
			Bucket: intFromAny(row["bucket"]),
			Count:  intFromAny(row["count"]),
			Start:  floatFromAny(row["start"]),
			End:    floatFromAny(row["end"]),
		})
	}
	return bins, nil
}

func (s dataQueryService) Distribution(ctx context.Context, request RawValueQuery, sort []QuerySort, limit int) (QueryRows, error) {
	var options *dataquery.DistributionOptions
	if request.Distribution != nil {
		options = &dataquery.DistributionOptions{
			Quantiles:     append([]float64(nil), request.Distribution.Quantiles...),
			Outliers:      request.Distribution.Outliers,
			Approximation: request.Distribution.Approximation,
		}
		if request.Distribution.Whiskers != nil {
			lower, upper := request.Distribution.Whiskers.Lower, request.Distribution.Whiskers.Upper
			options.WhiskerLower, options.WhiskerUpper = &lower, &upper
		}
	}
	result, err := s.executor.ExecuteDataQuery(ctx, dataquery.Query{
		ProjectID:    s.projectID,
		Surface:      dataquery.SurfaceDashboard,
		Operation:    dataquery.OperationDashboardDistribution,
		ModelID:      s.modelID,
		Kind:         dataquery.KindSemanticDistribution,
		Target:       request.Dataset,
		Fields:       querymap.Fields(request.Dimensions),
		Value:        querymap.Field(request.Metric),
		Filters:      querymap.Filters(request.Filters),
		Sort:         querymap.Sorts(sort),
		Limit:        limit,
		Distribution: options,
	})
	return rowsFromDataQuery(result.Rows), err
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func floatFromAny(value any) float64 {
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

func rowsFromDataQuery(rows []dataquery.Row) QueryRows {
	out := make(QueryRows, 0, len(rows))
	for _, row := range rows {
		converted := QueryRow{}
		for key, value := range row {
			converted[key] = value
		}
		out = append(out, converted)
	}
	return out
}
