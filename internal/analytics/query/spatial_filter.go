package query

import "github.com/flidai/leapview/internal/analytics/query/planir"

// ValidateSpatialFilter adapts the public query request shape to the closed
// PlanIR geometry vocabulary. Rendering and geometry validation live in
// planir, so query planning has one canonical spatial implementation.
func ValidateSpatialFilter(filter SpatialFilter) error {
	return planir.SpatialPredicate{
		Kind:         filter.Kind,
		Latitude:     filter.LatitudeField,
		Longitude:    filter.LongitudeField,
		West:         filter.West,
		South:        filter.South,
		East:         filter.East,
		North:        filter.North,
		Points:       toPlanIRSpatialPoints(filter.Points),
		Center:       planir.SpatialPoint{Longitude: filter.Center.Longitude, Latitude: filter.Center.Latitude},
		RadiusMeters: filter.RadiusMeters,
	}.Validate()
}

func toPlanIRSpatialPoints(points []SpatialPoint) []planir.SpatialPoint {
	if len(points) == 0 {
		return nil
	}
	out := make([]planir.SpatialPoint, len(points))
	for i, point := range points {
		out[i] = planir.SpatialPoint{Longitude: point.Longitude, Latitude: point.Latitude}
	}
	return out
}
