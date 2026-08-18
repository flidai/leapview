package query

import (
	"fmt"
	"math"
	"strings"

	"github.com/flidai/leapview/internal/analytics/query/planir"
)

func containsOutputColumn(columns []string, target string) bool {
	for _, column := range columns {
		if column == target {
			return true
		}
	}
	return false
}

const (
	SpatialTileMinimumZoom           = 0
	SpatialTileMaximumZoom           = 18
	SpatialTileExtent                = 4096
	SpatialTileMaximumFeaturesColumn = "__spatial_tile_maximum_features"
	SpatialTileMaximumBytesColumn    = "__spatial_tile_maximum_bytes"
	mercatorMaximumLatitude          = 85.0511287798066
	mercatorHalfWorld                = 20037508.342789244
)

// PlanSpatialTileAggregate plans one 4x4-style metatile at the final semantic
// bucket grain. Coordinate cells are injected into each dataset aggregate before
// count-distinct, average, ratio dependencies, and other metrics are reduced.
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
	var err error
	latitude, err := outputAlias(request.Latitude)
	if err != nil {
		return Plan{}, err
	}
	longitude, err := outputAlias(request.Longitude)
	if err != nil {
		return Plan{}, err
	}
	metricColumns := make([]string, 0, len(request.Metrics))
	metricProperties := make([]planir.SpatialProperty, 0, len(request.Metrics))

	targetZoom := request.TargetZoom
	if targetZoom == 0 {
		targetZoom = min(request.Zoom+1, SpatialTileMaximumZoom)
	}
	irGraph, err := p.spatialAggregatePlanIR(request, filters)
	if err != nil {
		return Plan{}, err
	}
	for _, metric := range request.Metrics {
		alias, err := outputAlias(metric)
		if err != nil {
			return Plan{}, err
		}
		metricColumns = append(metricColumns, alias)
		typ := spatialGraphColumnType(irGraph, alias)
		if typ == "" {
			return Plan{}, fmt.Errorf("spatial metric %q has no typed PlanIR output", metric.Field)
		}
		metricProperties = append(metricProperties, planir.SpatialProperty{Name: alias, Source: alias, Type: typ})
	}

	meta := spatialEnvelopeMeta(irGraph, []string{"__tile_x", "__tile_y", "feature_count", "mvt"}, "spatial_mvt_aggregate")
	envelope := planir.SpatialEnvelope{NodeMeta: meta, Operation: planir.SpatialEnvelopeTileAggregate, Input: irGraph.Output, Latitude: latitude, Longitude: longitude, Metrics: metricColumns, MetricProperties: metricProperties, Zoom: request.Zoom, TargetZoom: targetZoom, CellPixels: request.CellPixels, Buffer: request.Buffer}
	return renderSpatialEnvelopePlan(irGraph, envelope, "spatial_mvt_aggregated")
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
	raw, err := p.planSpatialRawSource(spatialRawSourceRequest{
		Dataset: request.Dataset, Dimensions: request.Dimensions, Metrics: request.Metrics, Identity: request.Identity,
		Time: request.Time, Filters: filters, ColumnMasks: request.ColumnMasks, Latitude: request.Latitude, Longitude: request.Longitude,
	})
	if err != nil {
		return Plan{}, err
	}
	meta := spatialEnvelopeMeta(raw.plan.IR, []string{"__tile_x", "__tile_y", "feature_count", "mvt"}, "spatial_mvt_raw")
	envelope := planir.SpatialEnvelope{NodeMeta: meta, Operation: planir.SpatialEnvelopeTileRaw, Input: raw.plan.IR.Output, Latitude: raw.latitude, Longitude: raw.longitude, Properties: raw.properties, Identity: raw.identity, Zoom: request.Zoom, Buffer: request.Buffer, FeatureCap: request.FeatureCap}
	return renderSpatialEnvelopePlan(raw.plan.IR, envelope, "spatial_mvt_raw")
}

// PlanSpatialTileBudget returns the exact revision-wide maximum raw feature
// count and encoded MVT byte length at one zoom. MVT encoding is skipped when
// any tile already exceeds the feature cap, allowing callers to advance zoom
// without paying to encode a precision level that cannot be selected.
func (p *Planner) PlanSpatialTileBudget(request SpatialTileBudgetRequest) (Plan, error) {
	if err := validateSpatialTileBudgetRequest(request); err != nil {
		return Plan{}, err
	}
	filters := append([]Filter(nil), request.Filters...)
	filters = append(filters,
		Filter{Field: request.Latitude.Field, Operator: "greater_than_or_equal", Values: []any{-mercatorMaximumLatitude}},
		Filter{Field: request.Latitude.Field, Operator: "less_than_or_equal", Values: []any{mercatorMaximumLatitude}},
		Filter{Field: request.Longitude.Field, Operator: "greater_than_or_equal", Values: []any{-180.0}},
		Filter{Field: request.Longitude.Field, Operator: "less_than_or_equal", Values: []any{180.0}},
	)
	raw, err := p.planSpatialRawSource(spatialRawSourceRequest{
		Dataset: request.Dataset, Dimensions: request.Dimensions, Metrics: request.Metrics, Identity: request.Identity,
		Time: request.Time, Filters: filters, ColumnMasks: request.ColumnMasks, Latitude: request.Latitude, Longitude: request.Longitude,
	})
	if err != nil {
		return Plan{}, err
	}
	meta := spatialEnvelopeMeta(raw.plan.IR, []string{SpatialTileMaximumFeaturesColumn, SpatialTileMaximumBytesColumn}, "spatial_mvt_budget")
	envelope := planir.SpatialEnvelope{NodeMeta: meta, Operation: planir.SpatialEnvelopeTileBudget, Input: raw.plan.IR.Output, Latitude: raw.latitude, Longitude: raw.longitude, Properties: raw.properties, Identity: raw.identity, Zoom: request.Zoom, Buffer: request.Buffer, FeatureCap: request.FeatureCap, MaximumBytes: request.MaximumBytes}
	return renderSpatialEnvelopePlan(raw.plan.IR, envelope, "spatial_mvt_budget")
}

type spatialRawSourceRequest struct {
	Dataset     string
	Dimensions  []Field
	Metrics     []Field
	Identity    []Field
	Filters     []Filter
	ColumnMasks []ColumnMask
	Time        Time
	Latitude    Field
	Longitude   Field
}

type spatialRawSource struct {
	plan       Plan
	latitude   string
	longitude  string
	properties []planir.SpatialProperty
	identity   []string
}

func (p *Planner) planSpatialRawSource(request spatialRawSourceRequest) (spatialRawSource, error) {
	governed, err := p.Plan(Request{Dataset: request.Dataset, Dimensions: request.Dimensions, Metrics: request.Metrics, Time: request.Time, Filters: request.Filters, ColumnMasks: request.ColumnMasks})
	if err != nil {
		return spatialRawSource{}, err
	}
	latitude, err := outputAlias(request.Latitude)
	if err != nil {
		return spatialRawSource{}, err
	}
	longitude, err := outputAlias(request.Longitude)
	if err != nil {
		return spatialRawSource{}, err
	}
	if !containsOutputColumn(governed.Columns, latitude) || !containsOutputColumn(governed.Columns, longitude) {
		return spatialRawSource{}, fmt.Errorf("raw spatial tile coordinates must be selected dimensions")
	}
	identityAliases := make([]string, 0, len(request.Identity))
	for _, field := range request.Identity {
		alias, err := outputAlias(field)
		if err != nil {
			return spatialRawSource{}, err
		}
		if !containsOutputColumn(governed.Columns, alias) {
			return spatialRawSource{}, fmt.Errorf("raw spatial tile identity %q must be selected", alias)
		}
		identityAliases = append(identityAliases, alias)
	}
	if len(identityAliases) == 0 {
		for _, field := range request.Dimensions {
			alias, err := outputAlias(field)
			if err != nil {
				return spatialRawSource{}, err
			}
			identityAliases = append(identityAliases, alias)
		}
	}
	types := make(map[string]string, len(governed.Columns))
	for _, field := range request.Dimensions {
		alias, aliasErr := outputAlias(field)
		if aliasErr != nil {
			return spatialRawSource{}, aliasErr
		}
		if dimension, resolveErr := p.resolveDimension(field.Field); resolveErr == nil {
			types[alias] = spatialLogicalTypeWithFallback(string(dimension.Datatype), dimension.Type)
		} else if dimension, ok := p.compiled.SemanticDimension(field.Field); ok {
			types[alias] = spatialLogicalTypeWithFallback(string(dimension.Datatype), dimension.Type)
		}
	}
	for _, field := range request.Metrics {
		alias, aliasErr := outputAlias(field)
		if aliasErr != nil {
			return spatialRawSource{}, aliasErr
		}
		typ := spatialGraphColumnType(governed.IR, alias)
		if typ == "" {
			return spatialRawSource{}, fmt.Errorf("spatial metric %q has no typed PlanIR output", field.Field)
		}
		types[alias] = typ
	}
	types[latitude], types[longitude] = "float", "float"
	properties := make([]planir.SpatialProperty, 0, len(governed.Columns))
	for _, column := range governed.Columns {
		properties = append(properties, planir.SpatialProperty{Name: column, Source: column, Type: types[column]})
	}
	return spatialRawSource{plan: governed, latitude: latitude, longitude: longitude, properties: properties, identity: identityAliases}, nil
}

func spatialLogicalType(datatype string) string {
	switch strings.ToLower(datatype) {
	case "decimal":
		return "decimal"
	case "integer":
		return "integer"
	case "float":
		return "float"
	default:
		return "string"
	}
}

func spatialGraphColumnType(graph *planir.Graph, name string) string {
	if graph == nil || graph.Nodes[graph.Output] == nil {
		return ""
	}
	meta := graph.Nodes[graph.Output].Meta()
	for _, metric := range meta.AvailableMetrics {
		if metric.Name == name {
			return spatialLogicalType(metric.Type)
		}
	}
	for _, field := range meta.AvailableFields {
		if field.Name == name {
			return spatialLogicalTypeWithFallback(field.Type, field.Type)
		}
	}
	return ""
}

func spatialLogicalTypeWithFallback(datatype, semanticType string) string {
	if strings.TrimSpace(semanticType) == "" {
		return "string"
	}
	if strings.EqualFold(datatype, "decimal") {
		return "decimal"
	}
	if strings.EqualFold(datatype, "integer") && strings.EqualFold(semanticType, "number") {
		return "integer"
	}
	if strings.EqualFold(datatype, "float") && strings.EqualFold(semanticType, "number") {
		return "float"
	}
	if strings.EqualFold(semanticType, "number") {
		return "float"
	}
	return spatialLogicalType(semanticType)
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
		Dataset: request.Dataset, Latitude: request.Latitude, Longitude: request.Longitude,
		Zoom: request.Zoom, MetatileX: request.MetatileX, MetatileY: request.MetatileY,
		MetatileSize: request.MetatileSize, CellPixels: 32, Buffer: request.Buffer,
	})
}

func validateSpatialTileBudgetRequest(request SpatialTileBudgetRequest) error {
	if request.FeatureCap <= 0 || request.FeatureCap > 5000 {
		return fmt.Errorf("raw spatial tile feature cap must be between 1 and 5000")
	}
	if request.Zoom < SpatialTileMinimumZoom || request.Zoom > SpatialTileMaximumZoom {
		return fmt.Errorf("spatial tile zoom must be between %d and %d", SpatialTileMinimumZoom, SpatialTileMaximumZoom)
	}
	if request.Buffer < 0 || request.Buffer > SpatialTileExtent {
		return fmt.Errorf("spatial tile buffer is outside the MVT extent")
	}
	if request.MaximumBytes <= 0 {
		return fmt.Errorf("raw spatial tile maximum bytes must be positive")
	}
	if request.Latitude.Field == "" || request.Longitude.Field == "" {
		return fmt.Errorf("spatial tile requires coordinate fields")
	}
	return nil
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
