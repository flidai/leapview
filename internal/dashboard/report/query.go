package report

import (
	"context"

	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
)

type QueryField struct {
	Field string
	Alias string
}

type QueryFilter struct {
	Field    string
	Fact     string
	Operator string
	Values   []any
	Groups   []QueryFilterGroup
	Spatial  *SpatialFilter
}

type SpatialFilter struct {
	Kind           string
	LatitudeField  string
	LongitudeField string
	Fact           string
	West           float64
	South          float64
	East           float64
	North          float64
	Points         []SpatialPoint
	Center         SpatialPoint
	RadiusMeters   float64
}

type SpatialPoint struct {
	Longitude float64
	Latitude  float64
}

type QueryFilterGroup struct {
	Filters []QueryFilter
}

type QuerySort struct {
	Field     string
	Direction string
}

type AggregateQuery struct {
	Table      string
	Dimensions []QueryField
	Metrics    []QueryField
	Time       dashboardauthoring.QueryTime
	Filters    []QueryFilter
	Sort       []QuerySort
	Limit      int
	Offset     int
}

type RowQuery struct {
	Table      string
	Dimensions []QueryField
	Metrics    []QueryField
	Filters    []QueryFilter
	Sort       []QuerySort
	Limit      int
	Offset     int
}

type ModelTableQuery struct {
	Table   string
	Columns []string
	Sort    []QuerySort
	Limit   int
	Offset  int
}

type RawValueQuery struct {
	Table      string
	Dimensions []QueryField
	Metric     QueryField
	Filters    []QueryFilter
}

type CountQuery struct {
	Table   string
	Filters []QueryFilter
}

type QueryRow map[string]any

type QueryRows []QueryRow

type HistogramBin struct {
	Bucket int
	Count  int
	Start  float64
	End    float64
}

type DataService interface {
	Query(ctx context.Context, request AggregateQuery) (QueryRows, error)
	Rows(ctx context.Context, request RowQuery) (QueryRows, error)
	Count(ctx context.Context, request CountQuery) (int, error)
	Histogram(ctx context.Context, request RawValueQuery, binCount int) ([]HistogramBin, error)
	Distribution(ctx context.Context, request RawValueQuery, sort []QuerySort, limit int) (QueryRows, error)
}

func SemanticAggregateRequest(request AggregateQuery) semanticquery.Request {
	return semanticquery.Request{
		Table:      request.Table,
		Dimensions: queryFields(request.Dimensions),
		Metrics:    queryFields(request.Metrics),
		Time:       semanticquery.Time{Field: request.Time.Field, Grain: request.Time.Grain, Alias: request.Time.Alias},
		Filters:    queryFilters(request.Filters),
		Sort:       querySorts(request.Sort),
		Limit:      request.Limit,
		Offset:     request.Offset,
	}
}

func SemanticRowRequest(request RowQuery) semanticquery.RowRequest {
	return semanticquery.RowRequest{
		Table:      request.Table,
		Dimensions: queryFields(request.Dimensions),
		Metrics:    queryFields(request.Metrics),
		Filters:    queryFilters(request.Filters),
		Sort:       querySorts(request.Sort),
		Limit:      request.Limit,
		Offset:     request.Offset,
	}
}

func queryFields(fields []QueryField) []semanticquery.Field {
	result := make([]semanticquery.Field, len(fields))
	for i, field := range fields {
		result[i] = queryField(field)
	}
	return result
}

func queryField(field QueryField) semanticquery.Field {
	return semanticquery.Field{
		Field: field.Field,
		Alias: field.Alias,
	}
}

func queryFilters(filters []QueryFilter) []semanticquery.Filter {
	result := make([]semanticquery.Filter, len(filters))
	for i, filter := range filters {
		result[i] = semanticquery.Filter{
			Field:    filter.Field,
			Fact:     filter.Fact,
			Operator: filter.Operator,
			Values:   append([]any{}, filter.Values...),
			Groups:   queryFilterGroups(filter.Groups),
			Spatial:  semanticSpatialFilter(filter.Spatial),
		}
	}
	return result
}

func semanticSpatialFilter(value *SpatialFilter) *semanticquery.SpatialFilter {
	if value == nil {
		return nil
	}
	points := make([]semanticquery.SpatialPoint, len(value.Points))
	for index, point := range value.Points {
		points[index] = semanticquery.SpatialPoint{Longitude: point.Longitude, Latitude: point.Latitude}
	}
	return &semanticquery.SpatialFilter{
		Kind: value.Kind, LatitudeField: value.LatitudeField, LongitudeField: value.LongitudeField, Fact: value.Fact,
		West: value.West, South: value.South, East: value.East, North: value.North, Points: points,
		Center: semanticquery.SpatialPoint{Longitude: value.Center.Longitude, Latitude: value.Center.Latitude}, RadiusMeters: value.RadiusMeters,
	}
}

func queryFilterGroups(groups []QueryFilterGroup) []semanticquery.FilterGroup {
	result := make([]semanticquery.FilterGroup, len(groups))
	for i, group := range groups {
		result[i] = semanticquery.FilterGroup{Filters: queryFilters(group.Filters)}
	}
	return result
}

func querySorts(sorts []QuerySort) []semanticquery.Sort {
	result := make([]semanticquery.Sort, len(sorts))
	for i, sort := range sorts {
		result[i] = semanticquery.Sort{Field: sort.Field, Direction: sort.Direction}
	}
	return result
}
