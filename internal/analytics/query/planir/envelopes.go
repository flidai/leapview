package planir

import (
	"fmt"
	"math"
	"strings"
)

const (
	spatialTileExtent          = 4096
	spatialMercatorMaxLatitude = 85.0511287798066
	spatialMercatorHalfWorld   = 20037508.342789244
)

func (r *duckRenderer) renderSpatialEnvelope(id string, n SpatialEnvelope) (string, []string, error) {
	inputs := n.Inputs()
	if len(inputs) == 0 {
		return "", nil, fmt.Errorf("spatial envelope %q has no input", id)
	}
	switch n.Operation {
	case SpatialEnvelopeTileAggregate:
		child, _, err := r.renderNode(inputs[0])
		if err != nil {
			return "", nil, err
		}
		if n.MemberInput != "" {
			member, _, memberErr := r.renderNode(inputs[1])
			if memberErr != nil {
				return "", nil, memberErr
			}
			return r.renderSpatialAggregateEnvelopeWithMembers(child, member, n), nodeColumns(n), nil
		}
		return r.renderSpatialAggregateEnvelope(child, n), nodeColumns(n), nil
	case SpatialEnvelopeTileRaw:
		child, _, err := r.renderNode(inputs[0])
		if err != nil {
			return "", nil, err
		}
		return r.renderSpatialRawEnvelope(child, n, false), nodeColumns(n), nil
	case SpatialEnvelopeTileBudget:
		child, _, err := r.renderNode(inputs[0])
		if err != nil {
			return "", nil, err
		}
		return r.renderSpatialRawEnvelope(child, n, true), nodeColumns(n), nil
	case SpatialEnvelopeMetadata:
		coordinate, _, err := r.renderNode(inputs[0])
		if err != nil {
			return "", nil, err
		}
		totals := ""
		if len(inputs) > 1 {
			totals, _, err = r.renderNode(inputs[1])
			if err != nil {
				return "", nil, err
			}
		}
		return r.renderSpatialMetadataEnvelope(coordinate, totals, n), nodeColumns(n), nil
	default:
		return "", nil, fmt.Errorf("unsupported spatial envelope operation %q", n.Operation)
	}
}

func (r *duckRenderer) renderSpatialAggregateEnvelope(child string, n SpatialEnvelope) string {
	relation := envelopeRelation(child)
	latitude, longitude := quoteName(n.Latitude), quoteName(n.Longitude)
	centerLatitude := fmt.Sprintf("LEAST(%.17g, GREATEST(-%.17g, CAST(__lv_center_latitude AS DOUBLE)))", spatialMercatorMaxLatitude, spatialMercatorMaxLatitude)
	centerX := fmt.Sprintf("(CAST(__lv_center_longitude AS DOUBLE) * %.17g / 180)", spatialMercatorHalfWorld)
	centerY := fmt.Sprintf("(LN(TAN((90 + %s) * PI() / 360)) / (PI() / 180) * %.17g / 180)", centerLatitude, spatialMercatorHalfWorld)
	// The aggregate input may be bucketed with the authored cluster radius,
	// which is intentionally independent from CellPixels. Assign the output
	// feature to its XYZ tile from its governed center coordinate rather than
	// assuming those two grids share an integer cell size.
	world := 1 << n.Zoom
	tileX := fmt.Sprintf("LEAST(%d, GREATEST(0, CAST(FLOOR(((CAST(__lv_center_longitude AS DOUBLE)) + 180) / 360 * %d) AS INTEGER)))", world-1, world)
	tileY := fmt.Sprintf("LEAST(%d, GREATEST(0, CAST(FLOOR((1 - LN(TAN(RADIANS(%s)) + 1 / COS(RADIANS(%s))) / PI()) / 2 * %d) AS INTEGER)))", world-1, centerLatitude, centerLatitude, world)
	identity := fmt.Sprintf("CONCAT('aggregate:%d:', CAST(%s AS BIGINT), ':', CAST(%s AS BIGINT))", n.Zoom, latitude, longitude)
	targetZoom := n.TargetZoom
	properties := []string{
		identity + " AS __lv_id",
		"TRUE AS __lv_aggregate",
		"'aggregated' AS __lv_precision",
		fmt.Sprintf("%d AS __lv_target_zoom", targetZoom),
		"CAST(__lv_west AS DOUBLE) AS __lv_west",
		"CAST(__lv_south AS DOUBLE) AS __lv_south",
		"CAST(__lv_east AS DOUBLE) AS __lv_east",
		"CAST(__lv_north AS DOUBLE) AS __lv_north",
		"__lv_count",
		"__lv_coordinate_count",
		"CASE WHEN __lv_coordinate_count >= 1000000 THEN printf('%.1fM', __lv_coordinate_count / 1000000.0) WHEN __lv_coordinate_count >= 1000 THEN printf('%.1fk', __lv_coordinate_count / 1000.0) ELSE CAST(__lv_coordinate_count AS VARCHAR) END AS __lv_coordinate_count_abbreviated",
	}
	if n.Cluster != nil {
		properties = append(properties, fmt.Sprintf("CAST(__lv_coordinate_count >= %d AS BOOLEAN) AS __lv_clustered", n.Cluster.MinimumPoints))
	}
	if len(n.MetricProperties) > 0 {
		for _, metric := range n.MetricProperties {
			properties = append(properties, fmt.Sprintf("CAST(%s AS %s) AS %s", quoteName(metric.Source), spatialPropertyCast(metric.Type), quoteName(metric.Name)))
		}
	} else {
		for _, metric := range n.Metrics {
			properties = append(properties, quoteName(metric))
		}
	}
	return fmt.Sprintf("WITH governed AS (\nSELECT * FROM %s\n), tile_features AS (\nSELECT %s, ST_AsMVTGeom(ST_Point(%s, %s), ST_Extent(ST_TileEnvelope(%d, %s, %s)), %d, %d, TRUE) AS geom\nFROM governed\n)\nSELECT __tile_x, __tile_y, COUNT(*) AS feature_count, ST_AsMVT(tile_features, 'primary', %d, 'geom') AS mvt\nFROM tile_features\nGROUP BY __tile_x, __tile_y\nORDER BY __tile_y, __tile_x", relation, tileX+" AS __tile_x, "+tileY+" AS __tile_y, "+strings.Join(properties, ", "), centerX, centerY, n.Zoom, tileX, tileY, spatialTileExtent, n.Buffer, spatialTileExtent)
}

// renderSpatialAggregateEnvelopeWithMembers keeps the precision family
// revision-wide while honoring minimumPoints. Buckets at or above the
// threshold remain one aggregate feature; buckets below it project their
// governed coordinate-grain members as unclustered features. This preserves
// exact member identity and interaction fields without mixing raw and
// aggregate tile requests.
func (r *duckRenderer) renderSpatialAggregateEnvelopeWithMembers(child, member string, n SpatialEnvelope) string {
	aggregateRelation := envelopeRelation(child)
	memberRelation := envelopeRelation(member)
	latitude, longitude := quoteName(n.Latitude), quoteName(n.Longitude)
	world := 1 << n.Zoom
	aggregateTileX := fmt.Sprintf("LEAST(%d, GREATEST(0, CAST(FLOOR(((CAST(g.__lv_center_longitude AS DOUBLE)) + 180) / 360 * %d) AS INTEGER)))", world-1, world)
	centerLatitude := fmt.Sprintf("LEAST(%.17g, GREATEST(-%.17g, CAST(g.__lv_center_latitude AS DOUBLE)))", spatialMercatorMaxLatitude, spatialMercatorMaxLatitude)
	aggregateTileY := fmt.Sprintf("LEAST(%d, GREATEST(0, CAST(FLOOR((1 - LN(TAN(RADIANS(%s)) + 1 / COS(RADIANS(%s))) / PI()) / 2 * %d) AS INTEGER)))", world-1, centerLatitude, centerLatitude, world)
	aggregateX := fmt.Sprintf("(CAST(g.__lv_center_longitude AS DOUBLE) * %.17g / 180)", spatialMercatorHalfWorld)
	aggregateY := fmt.Sprintf("(LN(TAN((90 + %s) * PI() / 360)) / (PI() / 180) * %.17g / 180)", centerLatitude, spatialMercatorHalfWorld)
	memberLongitude := "m." + longitude
	memberLatitude := "m." + latitude
	memberClampedLatitude := fmt.Sprintf("LEAST(%.17g, GREATEST(-%.17g, (%s)))", spatialMercatorMaxLatitude, spatialMercatorMaxLatitude, memberLatitude)
	memberTileX := fmt.Sprintf("LEAST(%d, GREATEST(0, CAST(FLOOR(((%s) + 180) / 360 * %d) AS INTEGER)))", world-1, memberLongitude, world)
	memberTileY := fmt.Sprintf("LEAST(%d, GREATEST(0, CAST(FLOOR((1 - LN(TAN(RADIANS(%s)) + 1 / COS(RADIANS(%s))) / PI()) / 2 * %d) AS INTEGER)))", world-1, memberClampedLatitude, memberClampedLatitude, world)
	memberX := fmt.Sprintf("((%s) * %.17g / 180)", memberLongitude, spatialMercatorHalfWorld)
	memberY := fmt.Sprintf("(LN(TAN((90 + %s) * PI() / 360)) / (PI() / 180) * %.17g / 180)", memberClampedLatitude, spatialMercatorHalfWorld)

	clusterCells := 256 / int(n.Cluster.Radius)
	if clusterCells < 1 {
		clusterCells = 1
	}
	clusterGlobalCells := (1 << n.Zoom) * clusterCells
	memberBucketLongitude := fmt.Sprintf("LEAST(%d, GREATEST(0, FLOOR(((%s) + 180) / 360 * %d)))", clusterGlobalCells-1, memberLongitude, clusterGlobalCells)
	memberBucketLatitude := fmt.Sprintf("LEAST(%d, GREATEST(0, FLOOR((1 - LN(TAN(RADIANS(%s)) + 1 / COS(RADIANS(%s))) / PI()) / 2 * %d)))", clusterGlobalCells-1, memberClampedLatitude, memberClampedLatitude, clusterGlobalCells)

	identity := fmt.Sprintf("CONCAT('raw:', CAST(hash(concat_ws('\\x1f', %s)) & 9223372036854775807 AS VARCHAR))", spatialIdentityParts(n.MemberIdentity, "m", n.Latitude, n.Longitude))
	baseNames := []string{"__lv_id", "__lv_aggregate", "__lv_precision", "__lv_target_zoom", "__lv_west", "__lv_south", "__lv_east", "__lv_north", "__lv_count", "__lv_coordinate_count", "__lv_coordinate_count_abbreviated", "__lv_clustered"}
	propertyNames := append([]string(nil), baseNames...)
	propertyTypes := map[string]string{
		"__lv_id": "string", "__lv_aggregate": "boolean", "__lv_precision": "string", "__lv_target_zoom": "integer",
		"__lv_west": "float", "__lv_south": "float", "__lv_east": "float", "__lv_north": "float", "__lv_count": "integer",
		"__lv_coordinate_count": "integer", "__lv_coordinate_count_abbreviated": "string", "__lv_clustered": "boolean",
	}
	aggregateSources := map[string]string{
		"__lv_id":        fmt.Sprintf("CONCAT('aggregate:%d:', CAST(g.%s AS BIGINT), ':', CAST(g.%s AS BIGINT))", n.Zoom, latitude, longitude),
		"__lv_aggregate": "TRUE", "__lv_precision": "'aggregated'", "__lv_target_zoom": fmt.Sprintf("%d", n.TargetZoom),
		"__lv_west": "CAST(g.__lv_west AS DOUBLE)", "__lv_south": "CAST(g.__lv_south AS DOUBLE)", "__lv_east": "CAST(g.__lv_east AS DOUBLE)", "__lv_north": "CAST(g.__lv_north AS DOUBLE)",
		"__lv_count": "g.__lv_count", "__lv_coordinate_count": "g.__lv_coordinate_count",
		"__lv_coordinate_count_abbreviated": "CASE WHEN g.__lv_coordinate_count >= 1000000 THEN printf('%.1fM', g.__lv_coordinate_count / 1000000.0) WHEN g.__lv_coordinate_count >= 1000 THEN printf('%.1fk', g.__lv_coordinate_count / 1000.0) ELSE CAST(g.__lv_coordinate_count AS VARCHAR) END",
		"__lv_clustered":                    fmt.Sprintf("CAST(g.__lv_coordinate_count >= %d AS BOOLEAN)", n.Cluster.MinimumPoints),
	}
	memberSources := map[string]string{
		"__lv_id": identity, "__lv_aggregate": "FALSE", "__lv_precision": "'aggregated'", "__lv_target_zoom": "CAST(NULL AS INTEGER)",
		"__lv_west": memberLongitude, "__lv_south": memberLatitude, "__lv_east": memberLongitude, "__lv_north": memberLatitude,
		"__lv_count": "1", "__lv_coordinate_count": "1", "__lv_coordinate_count_abbreviated": "'1'", "__lv_clustered": "FALSE",
	}
	for _, property := range n.MetricProperties {
		if _, ok := aggregateSources[property.Name]; !ok {
			propertyNames = append(propertyNames, property.Name)
		}
		aggregateSources[property.Name] = "g." + quoteName(property.Source)
		propertyTypes[property.Name] = property.Type
	}
	for _, metric := range n.Metrics {
		if _, ok := aggregateSources[metric]; !ok {
			propertyNames = append(propertyNames, metric)
		}
		aggregateSources[metric] = "g." + quoteName(metric)
		if propertyTypes[metric] == "" {
			propertyTypes[metric] = "string"
		}
	}
	for _, property := range n.MemberProperties {
		if _, ok := aggregateSources[property.Name]; !ok {
			propertyNames = append(propertyNames, property.Name)
			propertyTypes[property.Name] = property.Type
		}
		memberSources[property.Name] = "m." + quoteName(property.Source)
	}
	aggregateExpressions := make([]string, 0, len(propertyNames))
	memberExpressions := make([]string, 0, len(propertyNames))
	for _, name := range propertyNames {
		typ := propertyTypes[name]
		aggregateExpression, aggregateOK := aggregateSources[name]
		if !aggregateOK {
			aggregateExpression = fmt.Sprintf("CAST(NULL AS %s)", spatialPropertyCast(typ))
		}
		memberExpression, memberOK := memberSources[name]
		if !memberOK {
			memberExpression = fmt.Sprintf("CAST(NULL AS %s)", spatialPropertyCast(typ))
		}
		if name != "__lv_id" && name != "__lv_aggregate" && name != "__lv_precision" && name != "__lv_target_zoom" && name != "__lv_west" && name != "__lv_south" && name != "__lv_east" && name != "__lv_north" && name != "__lv_count" && name != "__lv_coordinate_count" && name != "__lv_coordinate_count_abbreviated" && name != "__lv_clustered" {
			aggregateExpression = fmt.Sprintf("CAST(%s AS %s)", aggregateExpression, spatialPropertyCast(typ))
			memberExpression = fmt.Sprintf("CAST(%s AS %s)", memberExpression, spatialPropertyCast(typ))
		}
		aggregateExpressions = append(aggregateExpressions, aggregateExpression+" AS "+quoteName(name))
		memberExpressions = append(memberExpressions, memberExpression+" AS "+quoteName(name))
	}
	aggregateSelect := fmt.Sprintf("SELECT %s AS __tile_x, %s AS __tile_y, %s, ST_AsMVTGeom(ST_Point(%s, %s), ST_Extent(ST_TileEnvelope(%d, %s, %s)), %d, %d, TRUE) AS geom FROM governed g WHERE g.__lv_coordinate_count >= %d", aggregateTileX, aggregateTileY, strings.Join(aggregateExpressions, ", "), aggregateX, aggregateY, n.Zoom, aggregateTileX, aggregateTileY, spatialTileExtent, n.Buffer, n.Cluster.MinimumPoints)
	memberSelect := fmt.Sprintf("SELECT %s AS __tile_x, %s AS __tile_y, %s, ST_AsMVTGeom(ST_Point(%s, %s), ST_Extent(ST_TileEnvelope(%d, %s, %s)), %d, %d, TRUE) AS geom FROM member_buckets m JOIN governed g ON g.%s = m.__lv_bucket_latitude AND g.%s = m.__lv_bucket_longitude WHERE g.__lv_coordinate_count < %d", memberTileX, memberTileY, strings.Join(memberExpressions, ", "), memberX, memberY, n.Zoom, memberTileX, memberTileY, spatialTileExtent, n.Buffer, latitude, longitude, n.Cluster.MinimumPoints)
	return fmt.Sprintf("WITH governed AS (SELECT * FROM %s), members AS (SELECT * FROM %s), member_buckets AS (SELECT m.*, %s AS __lv_bucket_longitude, %s AS __lv_bucket_latitude FROM members m)\n, tile_features AS ((%s) UNION ALL (%s))\nSELECT __tile_x, __tile_y, COUNT(*) AS feature_count, ST_AsMVT(tile_features, 'primary', %d, 'geom') AS mvt FROM tile_features GROUP BY __tile_x, __tile_y ORDER BY __tile_y, __tile_x", aggregateRelation, memberRelation, memberBucketLongitude, memberBucketLatitude, aggregateSelect, memberSelect, spatialTileExtent)
}

func spatialIdentityParts(identity []string, qualifier, latitude, longitude string) string {
	fields := identity
	if len(fields) == 0 {
		fields = []string{latitude, longitude}
	}
	parts := make([]string, len(fields))
	for index, field := range fields {
		qualified := field
		if qualifier != "" {
			qualified = qualifier + "." + quoteName(field)
		} else {
			qualified = quoteName(field)
		}
		parts[index] = "COALESCE(CAST(" + qualified + " AS VARCHAR), '<null>')"
	}
	return strings.Join(parts, ", ")
}

func (r *duckRenderer) renderSpatialRawEnvelope(child string, n SpatialEnvelope, budget bool) string {
	relation := envelopeRelation(child)
	latitude, longitude := quoteName(n.Latitude), quoteName(n.Longitude)
	world := 1 << n.Zoom
	tileX := fmt.Sprintf("LEAST(%d, GREATEST(0, CAST(FLOOR(((%s) + 180) / 360 * %d) AS INTEGER)))", world-1, longitude, world)
	clamped := fmt.Sprintf("LEAST(%.17g, GREATEST(-%.17g, (%s)))", spatialMercatorMaxLatitude, spatialMercatorMaxLatitude, latitude)
	tileY := fmt.Sprintf("LEAST(%d, GREATEST(0, CAST(FLOOR((1 - LN(TAN(RADIANS(%s)) + 1 / COS(RADIANS(%s))) / PI()) / 2 * %d) AS INTEGER)))", world-1, clamped, clamped, world)
	mercatorX := fmt.Sprintf("((%s) * %.17g / 180)", longitude, spatialMercatorHalfWorld)
	mercatorY := fmt.Sprintf("(LN(TAN((90 + %s) * PI() / 360)) / (PI() / 180) * %.17g / 180)", clamped, spatialMercatorHalfWorld)
	properties := make([]string, 0, len(n.Properties)+3)
	columns := make([]string, 0, len(n.Properties))
	for _, property := range n.Properties {
		cast := spatialPropertyCast(property.Type)
		properties = append(properties, fmt.Sprintf("CAST(%s AS %s) AS %s", quoteName(property.Source), cast, quoteName(property.Name)))
		columns = append(columns, quoteName(property.Source))
	}
	identity := append([]string(nil), n.Identity...)
	if len(identity) == 0 {
		identity = []string{n.Latitude, n.Longitude}
	}
	identityParts := make([]string, len(identity))
	for i, field := range identity {
		identityParts[i] = "COALESCE(CAST(" + quoteName(field) + " AS VARCHAR), '<null>')"
	}
	identityExpr := "CONCAT('raw:', CAST(hash(concat_ws('\\x1f', " + strings.Join(identityParts, ", ") + ")) & 9223372036854775807 AS VARCHAR))"
	properties = append(properties, identityExpr+" AS __lv_id", "FALSE AS __lv_aggregate", "'raw' AS __lv_precision")
	// Count is a transport property, never an authored metric. Raw features
	// represent one coordinate; aggregate features project their governed
	// coordinate count from the aggregate plan above.
	properties = append(properties, "1 AS __lv_coordinate_count")
	located := "SELECT " + tileX + " AS __tile_x, " + tileY + " AS __tile_y"
	if len(properties) > 0 {
		located += ", " + strings.Join(properties, ", ")
	}
	if !budget {
		return fmt.Sprintf("WITH governed AS (\nSELECT * FROM %s\n), located AS (\n%s\nFROM governed\n), counted AS (\nSELECT *, COUNT(*) OVER (PARTITION BY __tile_x, __tile_y) AS __tile_feature_count\nFROM located\n), tile_counts AS (\nSELECT __tile_x, __tile_y, MAX(__tile_feature_count) AS feature_count\nFROM counted\nGROUP BY __tile_x, __tile_y\n), encodable AS (\nSELECT *, ST_AsMVTGeom(ST_Point(%s, %s), ST_Extent(ST_TileEnvelope(%d, __tile_x, __tile_y)), %d, %d, TRUE) AS geom\nFROM counted\nWHERE __tile_feature_count <= %d\n), encoded AS (\nSELECT __tile_x, __tile_y, ST_AsMVT(encodable, 'primary', %d, 'geom') AS mvt\nFROM encodable\nGROUP BY __tile_x, __tile_y\n)\nSELECT c.__tile_x, c.__tile_y, c.feature_count, e.mvt\nFROM tile_counts c\nLEFT JOIN encoded e USING (__tile_x, __tile_y)\nORDER BY c.__tile_y, c.__tile_x", relation, located, mercatorX, mercatorY, n.Zoom, spatialTileExtent, n.Buffer, n.FeatureCap, spatialTileExtent)
	}
	estimated := "64"
	for _, column := range columns {
		estimated += " + COALESCE(OCTET_LENGTH(ENCODE(CAST(" + column + " AS VARCHAR))), 0)"
	}
	return fmt.Sprintf("WITH governed AS (\nSELECT * FROM %s\n), located AS (\n%s, %s AS __tile_estimated_bytes\nFROM governed\n), tile_counts AS (\nSELECT __tile_x, __tile_y, COUNT(*) AS feature_count, SUM(__tile_estimated_bytes) AS estimated_bytes\nFROM located\nGROUP BY __tile_x, __tile_y\n), maximum_tile AS (\nSELECT COALESCE(MAX(feature_count), 0) AS __spatial_tile_maximum_features, COALESCE(MAX(estimated_bytes), 0) AS __spatial_tile_maximum_estimated_bytes\nFROM tile_counts\n), encodable AS (\nSELECT l.* EXCLUDE (__tile_estimated_bytes), c.feature_count AS __tile_feature_count, ST_AsMVTGeom(ST_Point(%s, %s), ST_Extent(ST_TileEnvelope(%d, l.__tile_x, l.__tile_y)), %d, %d, TRUE) AS geom\nFROM located l JOIN tile_counts c USING (__tile_x, __tile_y) CROSS JOIN maximum_tile m\nWHERE m.__spatial_tile_maximum_features <= %d AND m.__spatial_tile_maximum_estimated_bytes <= %d\n), encoded AS (\nSELECT __tile_x, __tile_y, ST_AsMVT(encodable, 'primary', %d, 'geom') AS mvt\nFROM encodable\nGROUP BY __tile_x, __tile_y\n)\nSELECT m.__spatial_tile_maximum_features, CASE WHEN m.__spatial_tile_maximum_estimated_bytes > %d THEN %d ELSE COALESCE(MAX(OCTET_LENGTH(e.mvt)), 0) END AS __spatial_tile_maximum_bytes\nFROM maximum_tile m LEFT JOIN encoded e ON TRUE\nGROUP BY m.__spatial_tile_maximum_features, m.__spatial_tile_maximum_estimated_bytes", relation, located, estimated, mercatorX, mercatorY, n.Zoom, spatialTileExtent, n.Buffer, n.FeatureCap, n.MaximumBytes, spatialTileExtent, n.MaximumBytes, n.MaximumBytes+1)
}

func (r *duckRenderer) renderSpatialMetadataEnvelope(coordinate, totals string, n SpatialEnvelope) string {
	coordinateRelation := envelopeRelation(coordinate)
	coordinateCTE := "spatial_coordinate_grain"
	r.ctes = append(r.ctes, coordinateCTE+" AS (SELECT * FROM "+coordinateRelation+")")
	wholeCTE := "spatial_whole_filter"
	if totals == "" {
		r.ctes = append(r.ctes, wholeCTE+" AS (SELECT 1 AS __spatial_present)")
	} else {
		r.ctes = append(r.ctes, wholeCTE+" AS (SELECT * FROM "+envelopeRelation(totals)+")")
	}
	occupancy := make([]string, 0, n.MaximumZoom-n.RawMinimumZoom+1)
	for zoom := n.RawMinimumZoom; zoom <= n.MaximumZoom; zoom++ {
		x := spatialMetadataBucketX(n.Longitude, zoom)
		y := spatialMetadataBucketY(n.Latitude, zoom)
		occupancy = append(occupancy, fmt.Sprintf("SELECT %d AS zoom, COALESCE(MAX(feature_count), 0) AS maximum_features FROM (SELECT COUNT(*) AS feature_count FROM %s GROUP BY %s, %s)", zoom, coordinateCTE, x, y))
	}
	occupancyCTE := "spatial_raw_zoom_occupancy"
	r.ctes = append(r.ctes, occupancyCTE+" AS ("+strings.Join(occupancy, " UNION ALL ")+")")
	transitionCTE := "spatial_raw_transition"
	transition := "COALESCE(MIN(zoom) FILTER (WHERE maximum_features <= " + fmt.Sprint(n.FeatureCap) + "), " + fmt.Sprint(n.MaximumZoom+1) + ")"
	if n.Cluster != nil && !n.Cluster.Enabled {
		// A disabled authored cluster policy is an explicit raw-only contract;
		// transport budgeting may still reject an unbounded raw source, but it
		// must never silently substitute server aggregates.
		transition = "0"
	}
	r.ctes = append(r.ctes, transitionCTE+" AS (SELECT "+transition+" AS "+quoteName("__spatial_raw_minimum_zoom")+" FROM "+occupancyCTE+")")
	selects := []string{
		"MIN(c." + quoteName(n.Longitude) + ") AS __spatial_west",
		"MIN(c." + quoteName(n.Latitude) + ") AS __spatial_south",
		"MAX(c." + quoteName(n.Longitude) + ") AS __spatial_east",
		"MAX(c." + quoteName(n.Latitude) + ") AS __spatial_north",
		"COUNT(*) AS __spatial_cardinality",
	}
	for _, metric := range n.Metrics {
		selects = append(selects, "MIN(c."+quoteName(metric)+") AS "+quoteName("__spatial_raw_min_"+metric), "MAX(c."+quoteName(metric)+") AS "+quoteName("__spatial_raw_max_"+metric), "MAX(t."+quoteName(metric)+") AS "+quoteName("__spatial_total_"+metric))
	}
	selects = append(selects, "MAX(r."+quoteName("__spatial_raw_minimum_zoom")+") AS "+quoteName("__spatial_raw_minimum_zoom"))
	return "SELECT " + strings.Join(selects, ", ") + " FROM " + coordinateCTE + " c CROSS JOIN " + wholeCTE + " t CROSS JOIN " + transitionCTE + " r"
}

func spatialPropertyCast(typ string) string {
	switch strings.ToLower(typ) {
	case "number", "float":
		return "DOUBLE"
	case "decimal":
		return "VARCHAR"
	case "integer", "int", "long":
		return "BIGINT"
	case "boolean", "bool":
		return "BOOLEAN"
	default:
		return "VARCHAR"
	}
}

func spatialMetadataBucketX(field string, zoom int) string {
	globalCells := 1 << zoom
	return fmt.Sprintf("LEAST(%d, GREATEST(0, FLOOR(((%s) + 180) / 360 * %d)))", globalCells-1, quoteName(field), globalCells)
}

func spatialMetadataBucketY(field string, zoom int) string {
	globalCells := 1 << zoom
	clamped := fmt.Sprintf("LEAST(%.17g, GREATEST(-%.17g, (%s)))", spatialMercatorMaxLatitude, spatialMercatorMaxLatitude, quoteName(field))
	return fmt.Sprintf("LEAST(%d, GREATEST(0, FLOOR((1 - LN(TAN(RADIANS(%s)) + 1 / COS(RADIANS(%s))) / PI()) / 2 * %d)))", globalCells-1, clamped, clamped, globalCells)
}

func envelopeRelation(value string) string {
	trimmed := strings.TrimSpace(value)
	upper := strings.ToUpper(trimmed)
	if strings.HasPrefix(upper, "SELECT ") || strings.HasPrefix(upper, "WITH ") {
		return "(" + trimmed + ")"
	}
	if strings.HasPrefix(trimmed, "(") || strings.Contains(value, "\"") {
		return value
	}
	return quoteName(value)
}

func (r *duckRenderer) renderAnalyticalEnvelope(id string, n AnalyticalEnvelope) (string, []string, error) {
	child, _, err := r.renderNode(n.Input)
	if err != nil {
		return "", nil, err
	}
	relation := envelopeRelation(child)
	rawName := r.cteName(id + "_raw")
	r.ctes = append(r.ctes, rawName+" AS (SELECT * FROM "+relation+")")
	switch n.Operation {
	case AnalyticalEnvelopeHistogram:
		return r.renderHistogramEnvelope(rawName, n), nodeColumns(n), nil
	case AnalyticalEnvelopeDistribution:
		return r.renderDistributionEnvelope(rawName, n), nodeColumns(n), nil
	default:
		return "", nil, fmt.Errorf("unsupported analytical envelope operation %q", n.Operation)
	}
}

func (r *duckRenderer) renderHistogramEnvelope(rawName string, n AnalyticalEnvelope) string {
	last := n.BinCount - 1
	bounds := r.cteName(rawName + "_bounds")
	classified := r.cteName(rawName + "_classified")
	minExpr, maxExpr := "MIN("+quoteName(n.Value)+")", "MAX("+quoteName(n.Value)+")"
	if n.Approximation == "approximate" {
		minExpr = "approx_quantile(" + quoteName(n.Value) + ", 0.0)"
		maxExpr = "approx_quantile(" + quoteName(n.Value) + ", 1.0)"
	}
	if n.DomainMinimum != nil {
		minExpr = histogramLiteral(*n.DomainMinimum)
		maxExpr = histogramLiteral(*n.DomainMaximum)
		if n.Approximation == "approximate" {
			minExpr = "CAST(" + minExpr + " AS DOUBLE)"
			maxExpr = "CAST(" + maxExpr + " AS DOUBLE)"
		}
	}
	boundsSQL := bounds + " AS (SELECT " + minExpr + " AS min_value, " + maxExpr + " AS max_value FROM " + quoteName(rawName)
	if n.DomainMinimum != nil {
		boundsSQL += " LIMIT 1"
	}
	boundsSQL += ")"
	r.ctes = append(r.ctes, boundsSQL)
	width := histogramDivide("(b.max_value - b.min_value)", fmt.Sprintf("CAST(%d AS DECIMAL(38,0))", n.BinCount), n.ValueType)
	value := "r." + quoteName(n.Value)
	bucket := fmt.Sprintf("CASE WHEN %s IS NULL THEN -2 WHEN %s < b.min_value THEN -1 WHEN %s > b.max_value THEN %d WHEN b.min_value = b.max_value THEN 0 ELSE LEAST(%d, CAST(FLOOR(%s) AS INTEGER)) END", value, value, value, n.BinCount, last, histogramDivide("("+value+" - b.min_value)", "NULLIF(b.max_value - b.min_value, 0)", n.ValueType)+fmt.Sprintf(" * %d", n.BinCount))
	r.ctes = append(r.ctes, classified+" AS (SELECT "+bucket+" AS bucket, "+value+" AS value, b.min_value, b.max_value, "+width+" AS width FROM "+quoteName(rawName)+" r CROSS JOIN "+quoteName(bounds)+" b)")
	start := fmt.Sprintf("CASE WHEN bucket < 0 OR bucket >= %d THEN NULL WHEN ANY_VALUE(min_value) = ANY_VALUE(max_value) THEN ANY_VALUE(min_value) ELSE ANY_VALUE(min_value) + bucket * ANY_VALUE(width) END", n.BinCount)
	end := fmt.Sprintf("CASE WHEN bucket < 0 OR bucket >= %d THEN NULL WHEN ANY_VALUE(min_value) = ANY_VALUE(max_value) THEN ANY_VALUE(max_value) ELSE ANY_VALUE(min_value) + (bucket + 1) * ANY_VALUE(width) END", n.BinCount)
	return fmt.Sprintf("SELECT bucket, COUNT(*) AS count, %s AS start, %s AS end FROM %s GROUP BY bucket ORDER BY bucket ASC", start, end, quoteName(classified))
}

func (r *duckRenderer) renderDistributionEnvelope(rawName string, n AnalyticalEnvelope) string {
	quantiles := n.Quantiles
	if len(quantiles) == 0 {
		quantiles = []float64{0.25, 0.5, 0.75}
	}
	fn := "quantile_cont"
	if n.Approximation == "approximate" {
		fn = "approx_quantile"
	}
	value := quoteName(n.Value)
	columns := n.DistributionColumns
	if len(columns) != len(quantiles)+2 {
		columns = append([]string{"label", "min"}, make([]string, len(quantiles))...)
		for index := range quantiles {
			columns[index+2] = fmt.Sprintf("q%d", index)
		}
		columns = append(columns, "max")
	}
	selectStats := make([]string, 0, len(quantiles))
	for index, quantile := range quantiles {
		selectStats = append(selectStats, fmt.Sprintf("%s(%s, %s) AS %s", fn, value, histogramLiteral(quantile), quoteName(columns[index+2])))
	}
	group := ""
	groupSelect := "'all'"
	groupBy := ""
	statsGroupBy := ""
	join := ""
	where := ""
	if n.Group != "" {
		group = quoteName(n.Group)
		groupSelect = "r." + group
		groupBy = " GROUP BY r." + group
		statsGroupBy = " GROUP BY " + group
	}
	if n.WhiskerLower != nil {
		lower := fmt.Sprintf("%s(%s, %s)", fn, value, histogramLiteral(*n.WhiskerLower))
		upper := fmt.Sprintf("%s(%s, %s)", fn, value, histogramLiteral(*n.WhiskerUpper))
		stats := r.cteName(rawName + "_whiskers")
		if group != "" {
			r.ctes = append(r.ctes, stats+" AS (SELECT "+group+" AS label, "+lower+" AS lower_value, "+upper+" AS upper_value FROM "+quoteName(rawName)+statsGroupBy+")")
			join = " JOIN " + quoteName(stats) + " s ON r." + group + " IS NOT DISTINCT FROM s.label"
			where = " WHERE r." + value + " >= s.lower_value AND r." + value + " <= s.upper_value"
		} else {
			r.ctes = append(r.ctes, stats+" AS (SELECT "+lower+" AS lower_value, "+upper+" AS upper_value FROM "+quoteName(rawName)+")")
			join = " CROSS JOIN " + quoteName(stats) + " s"
			where = " WHERE r." + value + " >= s.lower_value AND r." + value + " <= s.upper_value"
		}
		if n.Outliers != "omit" {
			where = ""
		}
	}
	projections := []string{groupSelect + " AS label", "MIN(" + value + ") AS min"}
	projections = append(projections, selectStats...)
	projections = append(projections, "MAX("+value+") AS max")
	query := "SELECT " + strings.Join(projections, ", ") + " FROM " + quoteName(rawName) + " r" + join + where + groupBy
	// Empty and null-only populations produce an empty frame instead of a
	// synthetic all-null statistic row.
	query += " HAVING COUNT(" + value + ") > 0"
	query += " ORDER BY " + analyticalSortSQL(n.Sort)
	if n.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", n.Limit)
	}
	return query
}

func histogramLiteral(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "NULL"
	}
	return fmt.Sprintf("%.17g", value)
}

func histogramDivide(left, right, typ string) string {
	if typ == "decimal" || typ == "integer" {
		return renderExactDecimalDivide(left, right)
	}
	return "(" + left + " / " + right + ")"
}

func analyticalSortSQL(values []SortKey) string {
	if len(values) == 0 {
		return "label ASC"
	}
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = quoteName(value.Field)
		if value.Descending {
			parts[i] += " DESC"
		} else {
			parts[i] += " ASC"
		}
	}
	return strings.Join(parts, ", ")
}
