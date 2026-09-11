package runtime

import (
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard/querymap"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
)

func reportAggregateDataQuery(modelID string, request reportdef.AggregateQuery) dataquery.Query {
	return dataquery.Query{
		Surface:   dataquery.SurfaceDashboard,
		Operation: dataquery.OperationDashboardAggregate,
		ModelID:   modelID,
		Kind:      dataquery.KindSemanticAggregate,
		Target:    request.Dataset,
		Fields:    querymap.Fields(request.Dimensions),
		Metrics:   querymap.Fields(request.Metrics),
		Time:      dataquery.Time{Field: request.Time.Field, Grain: request.Time.Grain, Alias: request.Time.Alias},
		Filters:   querymap.FiltersWithSpatial(request.Filters),
		Sort:      querymap.Sorts(request.Sort),
		Limit:     request.Limit,
		Offset:    request.Offset,
	}
}

func reportRowDataQuery(modelID string, request reportdef.RowQuery, includeTotal bool) dataquery.Query {
	return dataquery.Query{
		Surface:      dataquery.SurfaceDashboard,
		Operation:    dataquery.OperationDashboardRows,
		ModelID:      modelID,
		Kind:         dataquery.KindSemanticRows,
		Target:       request.Dataset,
		Fields:       querymap.Fields(request.Dimensions),
		Metrics:      querymap.Fields(request.Metrics),
		Filters:      querymap.FiltersWithSpatial(request.Filters),
		Sort:         querymap.Sorts(request.Sort),
		Limit:        request.Limit,
		Offset:       request.Offset,
		IncludeTotal: includeTotal,
	}
}

func countOnlyDataQuery(request dataquery.Query) dataquery.Query {
	request.Operation = dataquery.OperationDashboardCount
	authorizationFields := make([]dataquery.Field, 0, len(request.Fields)+len(request.Metrics))
	for _, field := range request.Fields {
		field.Kind = dataquery.FieldKindDimension
		authorizationFields = append(authorizationFields, field)
	}
	for _, field := range request.Metrics {
		field.Kind = dataquery.FieldKindMetric
		authorizationFields = append(authorizationFields, field)
	}
	request.AuthorizationFields = authorizationFields
	request.Fields = nil
	request.Metrics = nil
	request.Sort = nil
	request.Offset = 0
	request.Limit = 0
	request.IncludeTotal = true
	return request
}

func reportRowsFromDataQuery(rows []dataquery.Row) reportdef.QueryRows {
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
