package runtime

import (
	"github.com/flidai/leapview/internal/analytics/dataquery"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
)

func reportAggregateDataQuery(modelID string, request reportdef.AggregateQuery) dataquery.Query {
	return dataquery.Query{
		Surface:   dataquery.SurfaceDashboard,
		Operation: dataquery.OperationDashboardAggregate,
		ModelID:   modelID,
		Kind:      dataquery.KindSemanticAggregate,
		Target:    request.Table,
		Fields:    reportFieldsToDataFields(request.Dimensions),
		Metrics:   reportFieldsToDataFields(request.Metrics),
		Time:      dataquery.Time{Field: request.Time.Field, Grain: request.Time.Grain, Alias: request.Time.Alias},
		Filters:   reportFiltersToDataFilters(request.Filters),
		Sort:      reportSortToDataSort(request.Sort),
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
		Target:       request.Table,
		Fields:       reportFieldsToDataFields(request.Dimensions),
		Metrics:      reportFieldsToDataFields(request.Metrics),
		Filters:      reportFiltersToDataFilters(request.Filters),
		Sort:         reportSortToDataSort(request.Sort),
		Limit:        request.Limit,
		Offset:       request.Offset,
		IncludeTotal: includeTotal,
	}
}

func countOnlyDataQuery(request dataquery.Query) dataquery.Query {
	request.Operation = dataquery.OperationDashboardCount
	request.AuthorizationFields = append(append([]dataquery.Field{}, request.Fields...), request.Metrics...)
	request.Fields = nil
	request.Metrics = nil
	request.Sort = nil
	request.Offset = 0
	request.Limit = 0
	request.IncludeTotal = true
	return request
}

func reportFieldsToDataFields(fields []reportdef.QueryField) []dataquery.Field {
	out := make([]dataquery.Field, 0, len(fields))
	for _, field := range fields {
		out = append(out, dataquery.Field{
			Field: field.Field,
			Alias: field.Alias,
		})
	}
	return out
}

func reportFiltersToDataFilters(filters []reportdef.QueryFilter) []dataquery.Filter {
	out := make([]dataquery.Filter, 0, len(filters))
	for _, filter := range filters {
		groups := make([]dataquery.FilterGroup, 0, len(filter.Groups))
		for _, group := range filter.Groups {
			groups = append(groups, dataquery.FilterGroup{Filters: reportFiltersToDataFilters(group.Filters)})
		}
		out = append(out, dataquery.Filter{
			Field:    filter.Field,
			Fact:     filter.Fact,
			Operator: filter.Operator,
			Values:   append([]any{}, filter.Values...),
			Groups:   groups,
			Spatial:  reportSpatialFilterToData(filter.Spatial),
		})
	}
	return out
}

func reportSpatialFilterToData(value *reportdef.SpatialFilter) *dataquery.SpatialFilter {
	if value == nil {
		return nil
	}
	points := make([]dataquery.SpatialPoint, len(value.Points))
	for index, point := range value.Points {
		points[index] = dataquery.SpatialPoint{Longitude: point.Longitude, Latitude: point.Latitude}
	}
	return &dataquery.SpatialFilter{
		Kind: value.Kind, LatitudeField: value.LatitudeField, LongitudeField: value.LongitudeField, Fact: value.Fact,
		West: value.West, South: value.South, East: value.East, North: value.North, Points: points,
		Center: dataquery.SpatialPoint{Longitude: value.Center.Longitude, Latitude: value.Center.Latitude}, RadiusMeters: value.RadiusMeters,
	}
}

func reportSortToDataSort(sort []reportdef.QuerySort) []dataquery.Sort {
	out := make([]dataquery.Sort, 0, len(sort))
	for _, item := range sort {
		out = append(out, dataquery.Sort{Field: item.Field, Direction: item.Direction})
	}
	return out
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
