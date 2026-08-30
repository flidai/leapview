package runtime

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func (s *VisualizationDataService) tiledEnvelope(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, dashboardID, pageID, visualID string, filters dashboard.Filters) (visualizationir.VisualizationEnvelope, error) {
	definition, ok := report.Visualizations[visualID]
	if !ok {
		return visualizationir.VisualizationEnvelope{}, fmt.Errorf("unknown tiled visual %q", visualID)
	}
	spatial := definition.Query.Spatial
	if definition.Query.Kind != visualizationdefinition.QuerySpatial || spatial == nil || spatial.Tiles == nil {
		return visualizationir.VisualizationEnvelope{}, fmt.Errorf("visual %q has no compiled spatial tiles", visualID)
	}
	queryFilters, err := s.filters.semanticFilters(ctx, runtime, report, filters, "visual", visualID)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	query := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardSpatialMetadata,
		ModelID: definition.Query.ModelID, Kind: dataquery.KindSemanticSpatialMetadata,
		Fields: fieldBindingsToDataFields(spatial.Dimensions), Metrics: fieldBindingsToDataFields(spatial.Metrics), Filters: reportFiltersToDataFilters(queryFilters),
		SpatialMetadata: &dataquery.SpatialMetadata{
			Latitude:   dataquery.Field{Field: spatial.Tiles.Latitude.FieldID, Alias: spatial.Tiles.Latitude.Alias},
			Longitude:  dataquery.Field{Field: spatial.Tiles.Longitude.FieldID, Alias: spatial.Tiles.Longitude.Alias},
			FeatureCap: int(spatial.Tiles.FeatureCap), RawMinimumZoom: int(spatial.Tiles.RawMinimumZoom), MaximumZoom: int(spatial.Tiles.MaximumZoom),
		},
	}
	result, err := runtime.data.ExecuteDataQuery(ctx, query)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	if len(result.Rows) != 1 {
		return visualizationir.VisualizationEnvelope{}, fmt.Errorf("spatial metadata for %q returned %d rows, want one", visualID, len(result.Rows))
	}
	row := result.Rows[0]
	effectiveRawMinimumZoom, ok := spatialInteger(row["__spatial_raw_minimum_zoom"])
	if !ok || effectiveRawMinimumZoom < int64(spatial.Tiles.RawMinimumZoom) || effectiveRawMinimumZoom > int64(spatial.Tiles.MaximumZoom)+1 {
		return visualizationir.VisualizationEnvelope{}, fmt.Errorf("spatial metadata for %q has invalid raw precision transition", visualID)
	}
	effectiveRawMinimumZoom, err = s.spatialRawMinimumZoomByByteBudget(ctx, runtime, definition, queryFilters, int(effectiveRawMinimumZoom))
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	cardinality, ok := spatialInteger(row["__spatial_cardinality"])
	if !ok || cardinality < 0 {
		return visualizationir.VisualizationEnvelope{}, fmt.Errorf("spatial metadata for %q has invalid cardinality", visualID)
	}
	extent := visualizationir.VisualizationSpatialBounds{West: -180, South: -85.0511287798066, East: 180, North: 85.0511287798066}
	if cardinality > 0 {
		var valuesOK bool
		extent.West, valuesOK = numericTableValue(row["__spatial_west"])
		if !valuesOK {
			return visualizationir.VisualizationEnvelope{}, fmt.Errorf("spatial metadata for %q has invalid extent", visualID)
		}
		extent.South, valuesOK = numericTableValue(row["__spatial_south"])
		if !valuesOK {
			return visualizationir.VisualizationEnvelope{}, fmt.Errorf("spatial metadata for %q has invalid extent", visualID)
		}
		extent.East, valuesOK = numericTableValue(row["__spatial_east"])
		if !valuesOK {
			return visualizationir.VisualizationEnvelope{}, fmt.Errorf("spatial metadata for %q has invalid extent", visualID)
		}
		extent.North, valuesOK = numericTableValue(row["__spatial_north"])
		if !valuesOK {
			return visualizationir.VisualizationEnvelope{}, fmt.Errorf("spatial metadata for %q has invalid extent", visualID)
		}
	}
	rawDomains := make([]visualizationir.VisualizationSpatialScaleDomain, 0, len(spatial.Metrics))
	aggregateDomains := make([]visualizationir.VisualizationSpatialScaleDomain, 0, len(spatial.Metrics))
	for _, metric := range spatial.Metrics {
		minimum, minimumOK := numericTableValue(row["__spatial_raw_min_"+metric.Alias])
		maximum, maximumOK := numericTableValue(row["__spatial_raw_max_"+metric.Alias])
		total, totalOK := numericTableValue(row["__spatial_total_"+metric.Alias])
		domain := visualizationir.VisualizationSpatialScaleDomain{Field: metric.Alias}
		if minimumOK {
			domain.Minimum = floatPointer(minimum)
		}
		if maximumOK {
			domain.Maximum = floatPointer(maximum)
		}
		if totalOK {
			domain.Total = floatPointer(total)
		}
		rawDomains = append(rawDomains, domain)
		aggregate := domain
		if totalOK {
			aggregateMinimum, aggregateMaximum := total, total
			if minimumOK {
				aggregateMinimum = math.Min(aggregateMinimum, minimum)
				aggregateMaximum = math.Max(aggregateMaximum, minimum)
			}
			if maximumOK {
				aggregateMinimum = math.Min(aggregateMinimum, maximum)
				aggregateMaximum = math.Max(aggregateMaximum, maximum)
			}
			aggregate.Minimum, aggregate.Maximum = floatPointer(aggregateMinimum), floatPointer(aggregateMaximum)
		}
		aggregateDomains = append(aggregateDomains, aggregate)
	}
	publicID := spatialTilePublicationFromContext(ctx)
	projectID := projectgraph.ResourceID("")
	if publicID == "" {
		if s.reports == nil {
			return visualizationir.VisualizationEnvelope{}, fmt.Errorf("project spatial tile route requires dashboard report identity")
		}
		projectID = s.reports.projectID
		if err := projectID.Validate(); err != nil {
			return visualizationir.VisualizationEnvelope{}, fmt.Errorf("project spatial tile route identity: %w", err)
		}
	}
	token, err := s.tiles.register(spatialTileRevision{
		DashboardID: dashboardID, PageID: pageID, VisualID: visualID, PublicID: publicID,
		PrincipalID: dataquery.MetadataFromContext(ctx).PrincipalID, StreamID: dataquery.MetadataFromContext(ctx).StreamID, Filters: filters, RawMinimumZoom: int(effectiveRawMinimumZoom), AuthoredRawMinimumZoom: int(spatial.Tiles.RawMinimumZoom),
	})
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	tileURL := publicSpatialTileURL(publicID, visualID, token)
	if publicID == "" {
		tileURL = spatialTileURL(dashboardID, visualID, token)
	}
	envelope, err := visualizationruntime.SpatialTiledEnvelopeFromMetadata(definition, visualizationruntime.SpatialTiledMetadata{
		Cardinality: cardinality, Extent: extent, RawDomains: rawDomains, AggregateDomains: aggregateDomains,
		TileURL: tileURL, RawMinimumZoom: int32(effectiveRawMinimumZoom),
	}, selectedEntries(filters, "visual", visualID), 0, 0)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	envelope.Highlights, err = selectedHighlights(runtime, report, filters, visualID)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	envelope.SpatialSelection = selectedSpatialState(filters, visualID)
	return envelope, visualizationir.ValidateEnvelope(envelope)
}

func (s *SnapshotService) querySpatialTile(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, visualID, revision string, rawMinimumZoom, zoom, x, y int) (SpatialTileResult, error) {
	report, runtime, err := s.reports.reportRuntime(dashboardID, s.runtimes)
	if err != nil {
		return SpatialTileResult{}, err
	}
	if !runtime.ready {
		return SpatialTileResult{}, runtime.missing
	}
	page := dashboardPage(report, pageID)
	filters = report.NormalizeFiltersForPage(page.ID, filters)
	found := false
	for _, id := range pageVisualizationIDs(page) {
		found = found || id == visualID
	}
	if !found {
		return SpatialTileResult{}, fmt.Errorf("visual %q is not on page %q", visualID, page.ID)
	}
	if zoom < 0 || zoom > 18 {
		return SpatialTileResult{}, fmt.Errorf("tile coordinates are outside the XYZ world")
	}
	world := 1 << zoom
	if x < 0 || y < 0 || x >= world || y >= world {
		return SpatialTileResult{}, fmt.Errorf("tile coordinates are outside the XYZ world")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.visualizations.spatialTile(ctx, runtime, report, filters, visualID, revision, rawMinimumZoom, zoom, x, y)
}

type immutableByteCache interface {
	LookupImmutableBytes(string) ([]byte, bool, error)
	StoreImmutableBytes(string, []byte) bool
	CoalesceImmutableBytes(context.Context, string, func(context.Context) error) (bool, error)
}

func (s *VisualizationDataService) spatialTile(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, filters dashboard.Filters, visualID, revision string, rawMinimumZoom, zoom, x, y int) (SpatialTileResult, error) {
	definition := report.Visualizations[visualID]
	spatial := definition.Query.Spatial
	if spatial == nil || spatial.Tiles == nil {
		return SpatialTileResult{}, fmt.Errorf("visual %q has no compiled spatial tiles", visualID)
	}
	queryFilters, err := s.filters.semanticFilters(ctx, runtime, report, filters, "visual", visualID)
	if err != nil {
		return SpatialTileResult{}, err
	}
	fields, identity := spatialTileFieldsAndIdentity(definition)
	metatileSize := int(spatial.Tiles.MetatileSize)
	metatileX, metatileY := x/metatileSize*metatileSize, y/metatileSize*metatileSize
	cache, cacheEnabled := runtime.data.(immutableByteCache)
	childKey := spatialTileByteCacheKey(revision, zoom, x, y)
	if cacheEnabled {
		if cached, ok, cacheErr := lookupSpatialTileBytes(cache, childKey); cacheErr != nil {
			return SpatialTileResult{}, cacheErr
		} else if ok {
			cached.CacheOutcome = dataquery.CacheHit
			return cached, nil
		}
	}
	buffer := int(spatial.Tiles.CellRadius) * 16
	execute := func(executionCtx context.Context, precision dataquery.SpatialTilePrecision) (dataquery.Result, error) {
		targetZoom := spatialAggregateTargetZoom(zoom, rawMinimumZoom, int(spatial.Tiles.MaximumZoom))
		query := dataquery.Query{
			Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardSpatialTile,
			ModelID: definition.Query.ModelID, Kind: dataquery.KindSemanticSpatialTile,
			Fields: fields, Metrics: fieldBindingsToDataFields(spatial.Metrics), Filters: reportFiltersToDataFilters(queryFilters),
			SpatialTile: &dataquery.SpatialTile{
				Latitude: dataquery.Field{Field: spatial.Tiles.Latitude.FieldID, Alias: spatial.Tiles.Latitude.Alias}, Longitude: dataquery.Field{Field: spatial.Tiles.Longitude.FieldID, Alias: spatial.Tiles.Longitude.Alias},
				Identity: identity, Zoom: zoom, TargetZoom: targetZoom, MetatileX: metatileX, MetatileY: metatileY, MetatileSize: metatileSize,
				CellPixels: int(spatial.Tiles.CellRadius), Buffer: buffer, FeatureCap: int(spatial.Tiles.FeatureCap), Precision: precision,
			},
		}
		if spatial.Time != nil {
			query.Time = dataquery.Time{Field: spatial.Time.FieldID, Alias: spatial.Time.Alias, Grain: spatial.Time.Grain}
		}
		return runtime.data.ExecuteDataQuery(executionCtx, query)
	}
	precision := spatialTilePrecision(zoom, rawMinimumZoom)
	generate := func(executionCtx context.Context) (SpatialTileResult, error) {
		if err := executionCtx.Err(); err != nil {
			return SpatialTileResult{}, err
		}
		result, executeErr := execute(executionCtx, precision)
		if executeErr != nil {
			return SpatialTileResult{}, executeErr
		}
		if precision == dataquery.SpatialTilePrecisionRaw && !spatialRawMetatileFits(result.Rows, spatial.Tiles.MaximumBytes) {
			return SpatialTileResult{}, fmt.Errorf("raw spatial tile exceeds the revision-wide feature or byte budget at zoom %d", zoom)
		}
		requested := SpatialTileResult{Precision: string(precision), CacheOutcome: result.CacheOutcome, QueryMS: result.DurationMS}
		for childX := metatileX; childX < metatileX+metatileSize; childX++ {
			for childY := metatileY; childY < metatileY+metatileSize; childY++ {
				if err := executionCtx.Err(); err != nil {
					return SpatialTileResult{}, err
				}
				tile, features, found, tileErr := spatialTileFromRows(result.Rows, childX, childY)
				if tileErr != nil {
					return SpatialTileResult{}, tileErr
				}
				if !found {
					tile = []byte{}
				}
				if int64(len(tile)) > spatial.Tiles.MaximumBytes {
					return SpatialTileResult{}, fmt.Errorf("encoded tile exceeds %d-byte budget", spatial.Tiles.MaximumBytes)
				}
				child := SpatialTileResult{Bytes: tile, Features: features, Precision: string(precision), QueryMS: result.DurationMS}
				if childX == x && childY == y {
					requested = child
				}
				if cacheEnabled && !storeSpatialTileBytes(cache, spatialTileByteCacheKey(revision, zoom, childX, childY), child) {
					return SpatialTileResult{}, fmt.Errorf("spatial tile byte cache rejected a bounded child tile")
				}
			}
		}
		return requested, nil
	}
	if !cacheEnabled {
		return generate(ctx)
	}
	var generated SpatialTileResult
	shared, err := cache.CoalesceImmutableBytes(ctx, spatialTileMetatileCacheKey(revision, zoom, metatileX, metatileY), func(executionCtx context.Context) error {
		if err := executionCtx.Err(); err != nil {
			return err
		}
		if _, ok, lookupErr := lookupSpatialTileBytes(cache, childKey); lookupErr != nil || ok {
			return lookupErr
		}
		var generateErr error
		generated, generateErr = generate(executionCtx)
		return generateErr
	})
	if err != nil {
		return SpatialTileResult{}, err
	}
	if !shared && generated.Bytes != nil {
		generated.CacheOutcome = dataquery.CacheMiss
		return generated, nil
	}
	cached, ok, err := lookupSpatialTileBytes(cache, childKey)
	if err != nil {
		return SpatialTileResult{}, err
	}
	if !ok {
		return SpatialTileResult{}, fmt.Errorf("coalesced spatial tile was not stored")
	}
	if shared {
		cached.CacheOutcome = dataquery.CacheCoalesced
	} else {
		cached.CacheOutcome = dataquery.CacheHit
	}
	return cached, nil
}

const spatialTileByteGenerationVersion = 1

func spatialTileByteCacheKey(revision string, zoom, x, y int) string {
	return fmt.Sprintf("spatial-tile-byte:v%d:%s:%d:%d:%d", spatialTileByteGenerationVersion, revision, zoom, x, y)
}

func spatialTileMetatileCacheKey(revision string, zoom, x, y int) string {
	return fmt.Sprintf("spatial-metatile-flight:v%d:%s:%d:%d:%d", spatialTileByteGenerationVersion, revision, zoom, x, y)
}

func spatialTileMetadataCacheKey(key string) string { return key + ":metadata" }

func storeSpatialTileBytes(cache immutableByteCache, key string, result SpatialTileResult) bool {
	metadata := make([]byte, 9)
	binary.BigEndian.PutUint64(metadata[0:8], uint64(max(result.Features, 0)))
	if result.Precision == string(dataquery.SpatialTilePrecisionRaw) {
		metadata[8] = 1
	}
	return cache.StoreImmutableBytes(key, result.Bytes) && cache.StoreImmutableBytes(spatialTileMetadataCacheKey(key), metadata)
}

func lookupSpatialTileBytes(cache immutableByteCache, key string) (SpatialTileResult, bool, error) {
	tile, found, err := cache.LookupImmutableBytes(key)
	if err != nil || !found {
		return SpatialTileResult{}, false, err
	}
	metadata, metadataFound, err := cache.LookupImmutableBytes(spatialTileMetadataCacheKey(key))
	if err != nil {
		return SpatialTileResult{}, false, err
	}
	if !metadataFound || len(metadata) != 9 {
		return SpatialTileResult{}, false, nil
	}
	precision := string(dataquery.SpatialTilePrecisionAggregated)
	if metadata[8] == 1 {
		precision = string(dataquery.SpatialTilePrecisionRaw)
	}
	return SpatialTileResult{Bytes: tile, Features: int(binary.BigEndian.Uint64(metadata[0:8])), Precision: precision}, true, nil
}

func (s *VisualizationDataService) spatialRawMinimumZoomByByteBudget(ctx context.Context, runtime *modelRuntime, definition visualizationdefinition.Definition, queryFilters []reportdef.QueryFilter, minimumZoom int) (int64, error) {
	spatial := definition.Query.Spatial
	if spatial == nil || spatial.Tiles == nil {
		return 0, fmt.Errorf("visual has no compiled spatial tiles")
	}
	maximumZoom := int(spatial.Tiles.MaximumZoom)
	if minimumZoom > maximumZoom {
		return int64(minimumZoom), nil
	}
	fields, identity := spatialTileFieldsAndIdentity(definition)
	buffer := int(spatial.Tiles.CellRadius) * 16
	for zoom := minimumZoom; zoom <= maximumZoom; zoom++ {
		query := dataquery.Query{
			Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardSpatialTileBudget,
			ModelID: definition.Query.ModelID, Kind: dataquery.KindSemanticSpatialTileBudget,
			Fields: fields, Metrics: fieldBindingsToDataFields(spatial.Metrics), Filters: reportFiltersToDataFilters(queryFilters),
			SpatialTileBudget: &dataquery.SpatialTileBudget{
				Latitude: dataquery.Field{Field: spatial.Tiles.Latitude.FieldID, Alias: spatial.Tiles.Latitude.Alias}, Longitude: dataquery.Field{Field: spatial.Tiles.Longitude.FieldID, Alias: spatial.Tiles.Longitude.Alias},
				Identity: identity, Zoom: zoom, Buffer: buffer, FeatureCap: int(spatial.Tiles.FeatureCap), MaximumBytes: spatial.Tiles.MaximumBytes,
			},
		}
		if spatial.Time != nil {
			query.Time = dataquery.Time{Field: spatial.Time.FieldID, Alias: spatial.Time.Alias, Grain: spatial.Time.Grain}
		}
		result, err := runtime.data.ExecuteDataQuery(ctx, query)
		if err != nil {
			return 0, fmt.Errorf("probe raw spatial tile budget at zoom %d: %w", zoom, err)
		}
		if len(result.Rows) != 1 {
			return 0, fmt.Errorf("raw spatial tile budget at zoom %d returned %d rows, want one", zoom, len(result.Rows))
		}
		maximumFeatures, featuresOK := spatialInteger(result.Rows[0]["__spatial_tile_maximum_features"])
		maximumBytes, bytesOK := spatialInteger(result.Rows[0]["__spatial_tile_maximum_bytes"])
		if !featuresOK || !bytesOK || maximumFeatures < 0 || maximumBytes < 0 {
			return 0, fmt.Errorf("raw spatial tile budget at zoom %d returned invalid maxima", zoom)
		}
		if spatialRawZoomFits(maximumFeatures, maximumBytes, spatial.Tiles.FeatureCap, spatial.Tiles.MaximumBytes) {
			return int64(zoom), nil
		}
	}
	return int64(maximumZoom + 1), nil
}

func spatialRawZoomFits(maximumFeatures, maximumBytes, featureCap, maximumTileBytes int64) bool {
	return maximumFeatures <= featureCap && maximumBytes <= maximumTileBytes
}

func spatialTileFieldsAndIdentity(definition visualizationdefinition.Definition) ([]dataquery.Field, []dataquery.Field) {
	spatial := definition.Query.Spatial
	fields := fieldBindingsToDataFields(spatial.Dimensions)
	if spatial.Series != nil {
		fields = append(fields, dataquery.Field{Field: spatial.Series.FieldID, Alias: spatial.Series.Alias})
	}
	identity := make([]dataquery.Field, 0, len(definition.Query.Identity))
	bindings := append(append([]visualizationdefinition.FieldBinding(nil), spatial.Dimensions...), spatial.Metrics...)
	for _, fieldID := range definition.Query.Identity {
		for _, binding := range bindings {
			if binding.FieldID == fieldID {
				identity = append(identity, dataquery.Field{Field: binding.FieldID, Alias: binding.Alias})
				break
			}
		}
	}
	return fields, identity
}

func spatialAggregateTargetZoom(zoom, rawMinimumZoom, maximumZoom int) int {
	target := max(zoom+2, rawMinimumZoom)
	return min(target, maximumZoom)
}

func spatialTilePrecision(zoom, rawMinimumZoom int) dataquery.SpatialTilePrecision {
	if zoom >= rawMinimumZoom {
		return dataquery.SpatialTilePrecisionRaw
	}
	return dataquery.SpatialTilePrecisionAggregated
}

// spatialRawMetatileFits verifies the revision-wide precision plan. The raw
// planner returns a nil MVT for a child that exceeds the feature cap. That is
// an invariant violation after the global occupancy query, never permission
// for one metatile to silently switch granularity.
func spatialRawMetatileFits(rows []dataquery.Row, maximumBytes int64) bool {
	for _, row := range rows {
		value := row["mvt"]
		if value == nil {
			return false
		}
		var size int
		switch typed := value.(type) {
		case []byte:
			size = len(typed)
		case string:
			size = len(typed)
		default:
			return false
		}
		if int64(size) > maximumBytes {
			return false
		}
	}
	return true
}

func spatialTileFromRows(rows []dataquery.Row, x, y int) ([]byte, int, bool, error) {
	for _, row := range rows {
		tileX, xOK := spatialInteger(row["__tile_x"])
		tileY, yOK := spatialInteger(row["__tile_y"])
		if !xOK || !yOK || int(tileX) != x || int(tileY) != y {
			continue
		}
		features, _ := spatialInteger(row["feature_count"])
		if value := row["mvt"]; value != nil {
			switch typed := value.(type) {
			case []byte:
				return append([]byte(nil), typed...), int(features), true, nil
			case string:
				return []byte(typed), int(features), true, nil
			default:
				return nil, 0, false, fmt.Errorf("tile result has unsupported MVT value %T", value)
			}
		}
		return nil, int(features), true, nil
	}
	return nil, 0, false, nil
}

func spatialInteger(value any) (int64, bool) {
	switch typed := value.(type) {
	case int8:
		return int64(typed), true
	case int16:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case uint:
		if uint64(typed) <= math.MaxInt64 {
			return int64(typed), true
		}
		return 0, false
	case uint8:
		return int64(typed), true
	case uint16:
		return int64(typed), true
	case uint32:
		return int64(typed), true
	case uint64:
		if typed <= math.MaxInt64 {
			return int64(typed), true
		}
		return 0, false
	}
	number, ok := numericTableValue(value)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number {
		return 0, false
	}
	return int64(number), true
}

func floatPointer(value float64) *float64 { return &value }
