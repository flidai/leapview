package report

import (
	"context"

	"github.com/flidai/leapview/internal/analytics/dataquery"
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
		Fields:    dataQueryFields(request.Dimensions),
		Metrics:   dataQueryFields(request.Metrics),
		Time:      dataquery.Time{Field: request.Time.Field, Grain: request.Time.Grain, Alias: request.Time.Alias},
		Filters:   dataQueryFilters(request.Filters),
		Sort:      dataQuerySort(request.Sort),
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
		Fields:    dataQueryFields(request.Dimensions),
		Metrics:   dataQueryFields(request.Metrics),
		Filters:   dataQueryFilters(request.Filters),
		Sort:      dataQuerySort(request.Sort),
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
		Filters:      dataQueryFilters(request.Filters),
		Limit:        1,
		IncludeTotal: true,
	})
	if err != nil {
		return 0, err
	}
	return result.TotalRows, nil
}

func (s dataQueryService) Histogram(ctx context.Context, request RawValueQuery, binCount int) ([]HistogramBin, error) {
	result, err := s.executor.ExecuteDataQuery(ctx, dataquery.Query{
		ProjectID: s.projectID,
		Surface:   dataquery.SurfaceDashboard,
		Operation: dataquery.OperationDashboardHistogram,
		ModelID:   s.modelID,
		Kind:      dataquery.KindSemanticHistogram,
		Target:    request.Dataset,
		Fields:    dataQueryFields(request.Dimensions),
		Value:     dataQueryField(request.Metric),
		Filters:   dataQueryFilters(request.Filters),
		BinCount:  binCount,
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
	result, err := s.executor.ExecuteDataQuery(ctx, dataquery.Query{
		ProjectID: s.projectID,
		Surface:   dataquery.SurfaceDashboard,
		Operation: dataquery.OperationDashboardDistribution,
		ModelID:   s.modelID,
		Kind:      dataquery.KindSemanticDistribution,
		Target:    request.Dataset,
		Fields:    dataQueryFields(request.Dimensions),
		Value:     dataQueryField(request.Metric),
		Filters:   dataQueryFilters(request.Filters),
		Sort:      dataQuerySort(sort),
		Limit:     limit,
	})
	return rowsFromDataQuery(result.Rows), err
}

func dataQueryField(field QueryField) dataquery.Field {
	fields := dataQueryFields([]QueryField{field})
	if len(fields) == 0 {
		return dataquery.Field{}
	}
	return fields[0]
}

func dataQueryFields(fields []QueryField) []dataquery.Field {
	out := make([]dataquery.Field, 0, len(fields))
	for _, field := range fields {
		out = append(out, dataquery.Field{
			Field: field.Field,
			Alias: field.Alias,
		})
	}
	return out
}

func dataQueryFilters(filters []QueryFilter) []dataquery.Filter {
	out := make([]dataquery.Filter, 0, len(filters))
	for _, filter := range filters {
		groups := make([]dataquery.FilterGroup, 0, len(filter.Groups))
		for _, group := range filter.Groups {
			groups = append(groups, dataquery.FilterGroup{Filters: dataQueryFilters(group.Filters)})
		}
		out = append(out, dataquery.Filter{
			Field:    filter.Field,
			Dataset:  filter.Dataset,
			Operator: filter.Operator,
			Values:   append([]any{}, filter.Values...),
			Groups:   groups,
		})
	}
	return out
}

func dataQuerySort(sort []QuerySort) []dataquery.Sort {
	out := make([]dataquery.Sort, 0, len(sort))
	for _, item := range sort {
		out = append(out, dataquery.Sort{Field: item.Field, Direction: item.Direction})
	}
	return out
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
