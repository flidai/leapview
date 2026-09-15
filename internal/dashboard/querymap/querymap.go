package querymap

import "github.com/flidai/leapview/internal/analytics/dataquery"

// QueryField is a report-definition field reference. Alias is part of the
// request identity and is intentionally copied without normalization.
type QueryField struct {
	Field string
	Alias string
}

// QueryFilter is a report-definition filter. Groups are recursively mapped by
// Filters, preserving the authored nesting and order.
type QueryFilter struct {
	Field    string
	Dataset  string
	Operator string
	Values   []any
	Groups   []QueryFilterGroup
	Spatial  *SpatialFilter
}

type SpatialFilter struct {
	Kind           string
	LatitudeField  string
	LongitudeField string
	Dataset        string
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

// Field maps one report field to a governed data-query field.
func Field(value QueryField) dataquery.Field {
	return dataquery.Field{Field: value.Field, Alias: value.Alias}
}

// Fields maps report fields using the historical non-nil empty-slice contract.
func Fields(values []QueryField) []dataquery.Field {
	out := make([]dataquery.Field, len(values))
	for index, value := range values {
		out[index] = Field(value)
	}
	return out
}

// Filters maps filters and every nested filter group recursively. It preserves
// the historical dashboard conversion contract: nil and empty collections
// become non-nil empty collections, and filter spatial predicates are omitted
// because the non-spatial execution/authz callers historically did so.
func Filters(values []QueryFilter) []dataquery.Filter {
	return mapFilters(values, false)
}

// FiltersWithSpatial is the variant used by the spatial dashboard runtime,
// whose legacy mapper carried spatial predicates into dataquery.Filter.
func FiltersWithSpatial(values []QueryFilter) []dataquery.Filter {
	return mapFilters(values, true)
}

func mapFilters(values []QueryFilter, includeSpatial bool) []dataquery.Filter {
	out := make([]dataquery.Filter, len(values))
	for index, value := range values {
		groups := make([]dataquery.FilterGroup, len(value.Groups))
		for groupIndex, group := range value.Groups {
			groups[groupIndex] = dataquery.FilterGroup{Filters: mapFilters(group.Filters, includeSpatial)}
		}
		out[index] = dataquery.Filter{
			Field:    value.Field,
			Dataset:  value.Dataset,
			Operator: value.Operator,
			Values:   cloneValues(value.Values),
			Groups:   groups,
		}
		if includeSpatial {
			out[index].Spatial = Spatial(value.Spatial)
		}
	}
	return out
}

// Spatial maps a report spatial predicate, retaining nil and point order.
func Spatial(value *SpatialFilter) *dataquery.SpatialFilter {
	if value == nil {
		return nil
	}
	points := make([]dataquery.SpatialPoint, len(value.Points))
	for index, point := range value.Points {
		points[index] = dataquery.SpatialPoint{Longitude: point.Longitude, Latitude: point.Latitude}
	}
	return &dataquery.SpatialFilter{
		Kind:           value.Kind,
		LatitudeField:  value.LatitudeField,
		LongitudeField: value.LongitudeField,
		Dataset:        value.Dataset,
		West:           value.West,
		South:          value.South,
		East:           value.East,
		North:          value.North,
		Points:         points,
		Center:         dataquery.SpatialPoint{Longitude: value.Center.Longitude, Latitude: value.Center.Latitude},
		RadiusMeters:   value.RadiusMeters,
	}
}

// Sorts maps report sort specifications while retaining order and direction.
func Sorts(values []QuerySort) []dataquery.Sort {
	out := make([]dataquery.Sort, len(values))
	for index, value := range values {
		out[index] = dataquery.Sort{Field: value.Field, Direction: value.Direction}
	}
	return out
}

func cloneValues(values []any) []any {
	return append([]any{}, values...)
}
