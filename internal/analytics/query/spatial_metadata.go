package query

import (
	"fmt"
	"strings"
)

const (
	SpatialCardinalityColumn    = "__spatial_cardinality"
	SpatialRawMinimumZoomColumn = "__spatial_raw_minimum_zoom"
)

// PlanSpatialMetadata returns one governed row for a tiled visual revision:
// valid-coordinate extent, exact coordinate-grain cardinality, coordinate-
// grain metric domains, and independently evaluated whole-filter totals.
func (p *Planner) PlanSpatialMetadata(request SpatialMetadataRequest) (Plan, error) {
	if request.Latitude.Field == "" || request.Longitude.Field == "" {
		return Plan{}, fmt.Errorf("spatial metadata requires coordinate fields")
	}
	if request.FeatureCap <= 0 || request.RawMinimumZoom < SpatialTileMinimumZoom || request.MaximumZoom < request.RawMinimumZoom || request.MaximumZoom > SpatialTileMaximumZoom {
		return Plan{}, fmt.Errorf("spatial metadata requires a valid feature cap and zoom range")
	}
	latitude, err := outputAlias(request.Latitude)
	if err != nil {
		return Plan{}, err
	}
	longitude, err := outputAlias(request.Longitude)
	if err != nil {
		return Plan{}, err
	}
	filters := append([]Filter(nil), request.Filters...)
	filters = append(filters,
		Filter{Field: request.Latitude.Field, Operator: "greater_than_or_equal", Values: []any{-mercatorMaximumLatitude}},
		Filter{Field: request.Latitude.Field, Operator: "less_than_or_equal", Values: []any{mercatorMaximumLatitude}},
		Filter{Field: request.Longitude.Field, Operator: "greater_than_or_equal", Values: []any{-180.0}},
		Filter{Field: request.Longitude.Field, Operator: "less_than_or_equal", Values: []any{180.0}},
	)
	coordinate, err := p.Plan(Request{
		Table: request.Table, Dimensions: []Field{request.Latitude, request.Longitude}, Metrics: request.Metrics,
		Filters: filters, ColumnMasks: request.ColumnMasks,
	})
	if err != nil {
		return Plan{}, err
	}
	totals := Plan{SQL: "SELECT 1 AS __spatial_present"}
	if len(request.Metrics) > 0 {
		totals, err = p.Plan(Request{Table: request.Table, Metrics: request.Metrics, Filters: filters, ColumnMasks: request.ColumnMasks})
		if err != nil {
			return Plan{}, err
		}
	}
	irGraph, err := p.spatialMetadataPlanIR(request, filters, coordinate)
	if err != nil {
		return Plan{}, err
	}
	selects := []string{
		"MIN(" + longitude + ") AS __spatial_west",
		"MIN(" + latitude + ") AS __spatial_south",
		"MAX(" + longitude + ") AS __spatial_east",
		"MAX(" + latitude + ") AS __spatial_north",
		"COUNT(*) AS " + SpatialCardinalityColumn,
	}
	columns := []string{"__spatial_west", "__spatial_south", "__spatial_east", "__spatial_north", SpatialCardinalityColumn}
	for _, metric := range request.Metrics {
		alias, err := outputAlias(metric)
		if err != nil {
			return Plan{}, err
		}
		selects = append(selects,
			"MIN(c."+alias+") AS __spatial_raw_min_"+alias,
			"MAX(c."+alias+") AS __spatial_raw_max_"+alias,
			"MAX(t."+alias+") AS __spatial_total_"+alias,
		)
		columns = append(columns, "__spatial_raw_min_"+alias, "__spatial_raw_max_"+alias, "__spatial_total_"+alias)
	}

	// Raw-versus-aggregate precision is a semantic choice, so it must be
	// revision-wide at each zoom. Determine the first zoom where every XYZ
	// tile is within the raw feature cap; a dense and a sparse neighboring
	// metatile must never independently choose different granularities.
	occupancy := make([]string, 0, request.MaximumZoom-request.RawMinimumZoom+1)
	for zoom := request.RawMinimumZoom; zoom <= request.MaximumZoom; zoom++ {
		tileX := spatialBucketXExpression(longitude, zoom, 256)
		tileY := spatialBucketYExpression(latitude, zoom, 256)
		occupancy = append(occupancy, fmt.Sprintf("SELECT %d AS zoom, COALESCE(MAX(feature_count), 0) AS maximum_features FROM (SELECT COUNT(*) AS feature_count FROM coordinate_grain GROUP BY %s, %s)", zoom, tileX, tileY))
	}
	selects = append(selects, "MAX(r."+SpatialRawMinimumZoomColumn+") AS "+SpatialRawMinimumZoomColumn)
	columns = append(columns, SpatialRawMinimumZoomColumn)
	sql := "WITH coordinate_grain AS (\n" + coordinate.SQL + "\n), whole_filter AS (\n" + totals.SQL + "\n), raw_zoom_occupancy AS (\n" + strings.Join(occupancy, "\nUNION ALL\n") + "\n), raw_transition AS (\nSELECT COALESCE(MIN(zoom) FILTER (WHERE maximum_features <= " + fmt.Sprint(request.FeatureCap) + "), " + fmt.Sprint(request.MaximumZoom+1) + ") AS " + SpatialRawMinimumZoomColumn + " FROM raw_zoom_occupancy\n)\nSELECT " + strings.Join(selects, ", ") + "\nFROM coordinate_grain c CROSS JOIN whole_filter t CROSS JOIN raw_transition r"
	return Plan{SQL: sql, Args: append(append([]any(nil), coordinate.Args...), totals.Args...), Columns: columns, Mode: "spatial_metadata", Facts: coordinate.Facts, PhysicalDependencies: coordinate.PhysicalDependencies, RelationshipPaths: coordinate.RelationshipPaths, IR: irGraph}, nil
}
