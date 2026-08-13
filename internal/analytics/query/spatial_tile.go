package query

import (
	"fmt"
	"math"
	"strings"
)

const (
	SpatialTileMinimumZoom  = 0
	SpatialTileMaximumZoom  = 18
	SpatialTileExtent       = 4096
	mercatorMaximumLatitude = 85.0511287798066
	mercatorHalfWorld       = 20037508.342789244
)

// PlanSpatialTileAggregate plans one 4x4-style metatile at the final semantic
// bucket grain. Coordinate cells are injected into each fact aggregate before
// count-distinct, average, ratio dependencies, and other measures are reduced.
// The resulting statement returns one native MVT byte value per child tile.
func (p *Planner) PlanSpatialTileAggregate(request SpatialTileRequest) (Plan, error) {
	if err := validateSpatialTileRequest(request); err != nil {
		return Plan{}, err
	}
	west, south, east, north := spatialMetatileBounds(request.Zoom, request.MetatileX, request.MetatileY, request.MetatileSize)
	filters := append([]Filter(nil), request.Filters...)
	filters = append(filters,
		Filter{Field: request.Latitude.Field, Operator: "greater_than_or_equal", Values: []any{south}},
		Filter{Field: request.Latitude.Field, Operator: "less_than", Values: []any{north}},
		Filter{Field: request.Longitude.Field, Operator: "greater_than_or_equal", Values: []any{west}},
		Filter{Field: request.Longitude.Field, Operator: "less_than", Values: []any{east}},
	)
	governed, err := p.Plan(Request{
		Table: request.Table,
		Dimensions: []Field{
			{Field: request.Latitude.Field, Alias: request.Latitude.Alias},
			{Field: request.Longitude.Field, Alias: request.Longitude.Alias},
		},
		Measures: request.Measures, Filters: filters, ColumnMasks: request.ColumnMasks,
		SpatialBucket: &SpatialBucket{Latitude: request.Latitude, Longitude: request.Longitude, Zoom: request.Zoom, CellPixels: request.CellPixels},
	})
	if err != nil {
		return Plan{}, err
	}
	latitude, err := outputAlias(request.Latitude)
	if err != nil {
		return Plan{}, err
	}
	longitude, err := outputAlias(request.Longitude)
	if err != nil {
		return Plan{}, err
	}
	cellsPerTile := 256 / request.CellPixels
	measureColumns := make([]string, 0, len(request.Measures))
	for _, measure := range request.Measures {
		alias, err := outputAlias(measure)
		if err != nil {
			return Plan{}, err
		}
		measureColumns = append(measureColumns, alias)
	}

	cellX, cellY := longitude, latitude
	tileX := fmt.Sprintf("CAST(FLOOR(%s / %d) AS INTEGER)", cellX, cellsPerTile)
	tileY := fmt.Sprintf("CAST(FLOOR(%s / %d) AS INTEGER)", cellY, cellsPerTile)
	centerLatitude := fmt.Sprintf("LEAST(%.17g, GREATEST(-%.17g, CAST(__lv_center_latitude AS DOUBLE)))", mercatorMaximumLatitude, mercatorMaximumLatitude)
	centerX := fmt.Sprintf("(CAST(__lv_center_longitude AS DOUBLE) * %.17g / 180)", mercatorHalfWorld)
	centerY := fmt.Sprintf("(LN(TAN((90 + %s) * PI() / 360)) / (PI() / 180) * %.17g / 180)", centerLatitude, mercatorHalfWorld)
	// MapLibre promotes this property to the rendered feature ID. Keep the
	// exact cell identity as a string: JavaScript numbers cannot distinguish
	// adjacent 63-bit integers above Number.MAX_SAFE_INTEGER.
	identity := fmt.Sprintf("CONCAT('aggregate:%d:', CAST(%s AS BIGINT), ':', CAST(%s AS BIGINT))", request.Zoom, cellY, cellX)
	targetZoom := request.TargetZoom
	if targetZoom == 0 {
		targetZoom = min(request.Zoom+1, SpatialTileMaximumZoom)
	}

	properties := []string{
		identity + " AS __lv_id",
		"TRUE AS __lv_aggregate",
		"'aggregated' AS __lv_precision",
		fmt.Sprintf("%d AS __lv_target_zoom", targetZoom),
		"CAST(__lv_west AS DOUBLE) AS __lv_west", "CAST(__lv_south AS DOUBLE) AS __lv_south",
		"CAST(__lv_east AS DOUBLE) AS __lv_east", "CAST(__lv_north AS DOUBLE) AS __lv_north",
		"__lv_count",
		"__lv_coordinate_count",
		"CASE WHEN __lv_coordinate_count >= 1000000 THEN printf('%.1fM', __lv_coordinate_count / 1000000.0) WHEN __lv_coordinate_count >= 1000 THEN printf('%.1fk', __lv_coordinate_count / 1000.0) ELSE CAST(__lv_coordinate_count AS VARCHAR) END AS __lv_coordinate_count_abbreviated",
	}
	properties = append(properties, measureColumns...)

	var sql strings.Builder
	sql.WriteString("WITH governed AS (\n")
	sql.WriteString(governed.SQL)
	sql.WriteString("\n), tile_features AS (\nSELECT ")
	sql.WriteString(tileX)
	sql.WriteString(" AS __tile_x, ")
	sql.WriteString(tileY)
	sql.WriteString(" AS __tile_y, ")
	sql.WriteString(strings.Join(properties, ", "))
	sql.WriteString(", ST_AsMVTGeom(ST_Point(")
	sql.WriteString(centerX)
	sql.WriteString(", ")
	sql.WriteString(centerY)
	sql.WriteString("), ST_Extent(ST_TileEnvelope(")
	sql.WriteString(fmt.Sprintf("%d, %s, %s", request.Zoom, tileX, tileY))
	sql.WriteString(fmt.Sprintf(")), %d, %d, TRUE) AS geom\nFROM governed\n)", SpatialTileExtent, request.Buffer))
	sql.WriteString("\nSELECT __tile_x, __tile_y, COUNT(*) AS feature_count, ST_AsMVT(tile_features, 'primary', ")
	sql.WriteString(fmt.Sprint(SpatialTileExtent))
	sql.WriteString(", 'geom') AS mvt\nFROM tile_features\nGROUP BY __tile_x, __tile_y\nORDER BY __tile_y, __tile_x")

	return Plan{
		SQL: sql.String(), Args: governed.Args, Columns: []string{"__tile_x", "__tile_y", "feature_count", "mvt"}, Mode: "spatial_mvt_aggregated",
		Facts: governed.Facts, PhysicalDependencies: governed.PhysicalDependencies, RelationshipPaths: governed.RelationshipPaths,
	}, nil
}

// PlanSpatialTileRaw emits coordinate-grain MVT features for child tiles that
// fit the raw feature budget. Overflow children are returned with a NULL MVT
// so the caller can deterministically substitute the aggregate plan; raw rows
// are never truncated.
func (p *Planner) PlanSpatialTileRaw(request SpatialTileRawRequest) (Plan, error) {
	if err := validateSpatialTileRawRequest(request); err != nil {
		return Plan{}, err
	}
	west, south, east, north := spatialMetatileBounds(request.Zoom, request.MetatileX, request.MetatileY, request.MetatileSize)
	filters := append([]Filter(nil), request.Filters...)
	filters = append(filters,
		Filter{Field: request.Latitude.Field, Operator: "greater_than_or_equal", Values: []any{south}},
		Filter{Field: request.Latitude.Field, Operator: "less_than", Values: []any{north}},
		Filter{Field: request.Longitude.Field, Operator: "greater_than_or_equal", Values: []any{west}},
		Filter{Field: request.Longitude.Field, Operator: "less_than", Values: []any{east}},
	)
	governed, err := p.Plan(Request{Table: request.Table, Dimensions: request.Dimensions, Measures: request.Measures, Time: request.Time, Filters: filters, ColumnMasks: request.ColumnMasks})
	if err != nil {
		return Plan{}, err
	}
	latitude, err := outputAlias(request.Latitude)
	if err != nil {
		return Plan{}, err
	}
	longitude, err := outputAlias(request.Longitude)
	if err != nil {
		return Plan{}, err
	}
	if !containsOutputColumn(governed.Columns, latitude) || !containsOutputColumn(governed.Columns, longitude) {
		return Plan{}, fmt.Errorf("raw spatial tile coordinates must be selected dimensions")
	}
	world := 1 << request.Zoom
	tileX := fmt.Sprintf("LEAST(%d, GREATEST(0, CAST(FLOOR(((%s) + 180) / 360 * %d) AS INTEGER)))", world-1, longitude, world)
	clamped := fmt.Sprintf("LEAST(%.17g, GREATEST(-%.17g, (%s)))", mercatorMaximumLatitude, mercatorMaximumLatitude, latitude)
	tileY := fmt.Sprintf("LEAST(%d, GREATEST(0, CAST(FLOOR((1 - LN(TAN(RADIANS(%s)) + 1 / COS(RADIANS(%s))) / PI()) / 2 * %d) AS INTEGER)))", world-1, clamped, clamped, world)
	mercatorX := fmt.Sprintf("((%s) * %.17g / 180)", longitude, mercatorHalfWorld)
	mercatorY := fmt.Sprintf("(LN(TAN((90 + %s) * PI() / 360)) / (PI() / 180) * %.17g / 180)", clamped, mercatorHalfWorld)

	identityAliases := make([]string, 0, len(request.Identity))
	for _, field := range request.Identity {
		alias, err := outputAlias(field)
		if err != nil {
			return Plan{}, err
		}
		if !containsOutputColumn(governed.Columns, alias) {
			return Plan{}, fmt.Errorf("raw spatial tile identity %q must be selected", alias)
		}
		identityAliases = append(identityAliases, alias)
	}
	if len(identityAliases) == 0 {
		for _, field := range request.Dimensions {
			alias, err := outputAlias(field)
			if err != nil {
				return Plan{}, err
			}
			identityAliases = append(identityAliases, alias)
		}
	}
	identityParts := make([]string, 0, len(identityAliases))
	for _, alias := range identityAliases {
		identityParts = append(identityParts, "COALESCE(CAST("+alias+" AS VARCHAR), '<null>')")
	}
	// Preserve all 63 hash bits through MVT/JavaScript by promoting their
	// decimal string representation instead of an unsafe JS number.
	identity := "CONCAT('raw:', CAST(hash(concat_ws('\\x1f', " + strings.Join(identityParts, ", ") + ")) & 9223372036854775807 AS VARCHAR))"

	properties := append([]string(nil), governed.Columns...)
	properties = append(properties, identity+" AS __lv_id", "FALSE AS __lv_aggregate", "'raw' AS __lv_precision")
	var sql strings.Builder
	sql.WriteString("WITH governed AS (\n")
	sql.WriteString(governed.SQL)
	sql.WriteString("\n), located AS (\nSELECT ")
	sql.WriteString(tileX)
	sql.WriteString(" AS __tile_x, ")
	sql.WriteString(tileY)
	sql.WriteString(" AS __tile_y, ")
	sql.WriteString(strings.Join(properties, ", "))
	sql.WriteString("\nFROM governed\n), counted AS (\nSELECT *, COUNT(*) OVER (PARTITION BY __tile_x, __tile_y) AS __tile_feature_count\nFROM located\n), tile_counts AS (\nSELECT __tile_x, __tile_y, MAX(__tile_feature_count) AS feature_count\nFROM counted\nGROUP BY __tile_x, __tile_y\n), encodable AS (\nSELECT *, ST_AsMVTGeom(ST_Point(")
	sql.WriteString(mercatorX)
	sql.WriteString(", ")
	sql.WriteString(mercatorY)
	sql.WriteString("), ST_Extent(ST_TileEnvelope(")
	sql.WriteString(fmt.Sprintf("%d, __tile_x, __tile_y", request.Zoom))
	sql.WriteString(fmt.Sprintf(")), %d, %d, TRUE) AS geom\nFROM counted\nWHERE __tile_feature_count <= %d\n), encoded AS (\nSELECT __tile_x, __tile_y, ST_AsMVT(encodable, 'primary', %d, 'geom') AS mvt\nFROM encodable\nGROUP BY __tile_x, __tile_y\n)\nSELECT c.__tile_x, c.__tile_y, c.feature_count, e.mvt\nFROM tile_counts c\nLEFT JOIN encoded e USING (__tile_x, __tile_y)\nORDER BY c.__tile_y, c.__tile_x", SpatialTileExtent, request.Buffer, request.FeatureCap, SpatialTileExtent))
	return Plan{SQL: sql.String(), Args: governed.Args, Columns: []string{"__tile_x", "__tile_y", "feature_count", "mvt"}, Mode: "spatial_mvt_raw", Facts: governed.Facts, PhysicalDependencies: governed.PhysicalDependencies, RelationshipPaths: governed.RelationshipPaths}, nil
}

func validateSpatialTileRequest(request SpatialTileRequest) error {
	if request.Latitude.Field == "" || request.Longitude.Field == "" {
		return fmt.Errorf("spatial tile requires coordinate fields")
	}
	if request.Zoom < SpatialTileMinimumZoom || request.Zoom > SpatialTileMaximumZoom {
		return fmt.Errorf("spatial tile zoom must be between %d and %d", SpatialTileMinimumZoom, SpatialTileMaximumZoom)
	}
	targetZoom := request.TargetZoom
	if targetZoom == 0 {
		targetZoom = min(request.Zoom+1, SpatialTileMaximumZoom)
	}
	if targetZoom < request.Zoom || targetZoom > SpatialTileMaximumZoom || (request.Zoom < SpatialTileMaximumZoom && targetZoom == request.Zoom) {
		return fmt.Errorf("spatial tile aggregate target zoom must advance from %d and remain at or below %d", request.Zoom, SpatialTileMaximumZoom)
	}
	worldTiles := 1 << request.Zoom
	if request.MetatileSize <= 0 || request.MetatileSize > 4 || request.MetatileX < 0 || request.MetatileY < 0 || request.MetatileX >= worldTiles || request.MetatileY >= worldTiles {
		return fmt.Errorf("spatial metatile is outside the XYZ world")
	}
	if request.MetatileX%request.MetatileSize != 0 || request.MetatileY%request.MetatileSize != 0 {
		return fmt.Errorf("spatial metatile origin must align to its size")
	}
	if request.CellPixels < 32 || request.CellPixels > 64 {
		return fmt.Errorf("spatial tile cell size must be between 32 and 64 CSS pixels")
	}
	if request.Buffer < 0 || request.Buffer > SpatialTileExtent {
		return fmt.Errorf("spatial tile buffer is outside the MVT extent")
	}
	return nil
}

func validateSpatialTileRawRequest(request SpatialTileRawRequest) error {
	if request.FeatureCap <= 0 || request.FeatureCap > 5000 {
		return fmt.Errorf("raw spatial tile feature cap must be between 1 and 5000")
	}
	return validateSpatialTileRequest(SpatialTileRequest{
		Table: request.Table, Latitude: request.Latitude, Longitude: request.Longitude,
		Zoom: request.Zoom, MetatileX: request.MetatileX, MetatileY: request.MetatileY,
		MetatileSize: request.MetatileSize, CellPixels: 32, Buffer: request.Buffer,
	})
}

func validateAggregateSpatialBucket(bucket SpatialBucket, dimensions []aggregateDimension) error {
	if bucket.Latitude.Field == "" || bucket.Longitude.Field == "" || bucket.Zoom < SpatialTileMinimumZoom || bucket.Zoom > SpatialTileMaximumZoom || bucket.CellPixels < 32 || bucket.CellPixels > 64 {
		return fmt.Errorf("invalid governed spatial bucket")
	}
	latitude, longitude := false, false
	for _, dimension := range dimensions {
		latitude = latitude || dimension.Name == bucket.Latitude.Field
		longitude = longitude || dimension.Name == bucket.Longitude.Field
	}
	if !latitude || !longitude {
		return fmt.Errorf("governed spatial bucket coordinates must be selected dimensions")
	}
	return nil
}

func spatialBucketXExpression(longitude string, zoom, cellPixels int) string {
	globalCells := (1 << zoom) * (256 / cellPixels)
	return fmt.Sprintf("LEAST(%d, GREATEST(0, FLOOR(((%s) + 180) / 360 * %d)))", globalCells-1, longitude, globalCells)
}

func spatialBucketYExpression(latitude string, zoom, cellPixels int) string {
	globalCells := (1 << zoom) * (256 / cellPixels)
	clamped := fmt.Sprintf("LEAST(%.17g, GREATEST(-%.17g, (%s)))", mercatorMaximumLatitude, mercatorMaximumLatitude, latitude)
	return fmt.Sprintf("LEAST(%d, GREATEST(0, FLOOR((1 - LN(TAN(RADIANS(%s)) + 1 / COS(RADIANS(%s))) / PI()) / 2 * %d)))", globalCells-1, clamped, clamped, globalCells)
}

func spatialCellLatitudeExpression(cellY string, globalCells int, southernEdge bool) string {
	offset := ""
	if southernEdge {
		offset = " + 1"
	}
	return fmt.Sprintf("DEGREES(ATAN(SINH(PI() * (1 - 2 * ((%s%s) / %d)))))", cellY, offset, globalCells)
}

func spatialMetatileBounds(zoom, originX, originY, size int) (west, south, east, north float64) {
	world := 1 << zoom
	endX, endY := min(originX+size, world), min(originY+size, world)
	west = float64(originX)/float64(world)*360 - 180
	east = float64(endX)/float64(world)*360 - 180
	north = tileLatitude(originY, world)
	south = tileLatitude(endY, world)
	return west, south, east, north
}

func tileLatitude(y, world int) float64 {
	mercator := math.Pi * (1 - 2*float64(y)/float64(world))
	return math.Atan(math.Sinh(mercator)) * 180 / math.Pi
}
