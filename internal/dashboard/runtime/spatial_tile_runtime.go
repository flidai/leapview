package runtime

import (
	"context"
	"fmt"
	"math"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
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
		Fields: fieldBindingsToDataFields(spatial.Dimensions), Measures: fieldBindingsToDataFields(spatial.Measures), Filters: reportFiltersToDataFilters(queryFilters),
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
	rawDomains := make([]visualizationir.VisualizationSpatialScaleDomain, 0, len(spatial.Measures))
	aggregateDomains := make([]visualizationir.VisualizationSpatialScaleDomain, 0, len(spatial.Measures))
	for _, measure := range spatial.Measures {
		minimum, minimumOK := numericTableValue(row["__spatial_raw_min_"+measure.Alias])
		maximum, maximumOK := numericTableValue(row["__spatial_raw_max_"+measure.Alias])
		total, totalOK := numericTableValue(row["__spatial_total_"+measure.Alias])
		domain := visualizationir.VisualizationSpatialScaleDomain{Field: measure.Alias}
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
	token, err := s.tiles.register(spatialTileRevision{
		DashboardID: dashboardID, PageID: pageID, VisualID: visualID, PublicID: publicID,
		PrincipalID: dataquery.MetadataFromContext(ctx).PrincipalID, Filters: filters, RawMinimumZoom: int(effectiveRawMinimumZoom),
	})
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	tileURL := spatialTileURL(s.workspaceID, dashboardID, visualID, token)
	if publicID != "" {
		tileURL = publicSpatialTileURL(publicID, visualID, token)
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

func (s *SnapshotService) querySpatialTile(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, visualID string, rawMinimumZoom, zoom, x, y int) (SpatialTileResult, error) {
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
	return s.visualizations.spatialTile(ctx, runtime, report, filters, visualID, rawMinimumZoom, zoom, x, y)
}

func (s *VisualizationDataService) spatialTile(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, filters dashboard.Filters, visualID string, rawMinimumZoom, zoom, x, y int) (SpatialTileResult, error) {
	definition := report.Visualizations[visualID]
	spatial := definition.Query.Spatial
	if spatial == nil || spatial.Tiles == nil {
		return SpatialTileResult{}, fmt.Errorf("visual %q has no compiled spatial tiles", visualID)
	}
	queryFilters, err := s.filters.semanticFilters(ctx, runtime, report, filters, "visual", visualID)
	if err != nil {
		return SpatialTileResult{}, err
	}
	fields := fieldBindingsToDataFields(spatial.Dimensions)
	if spatial.Series != nil {
		fields = append(fields, dataquery.Field{Field: spatial.Series.FieldID, Alias: spatial.Series.Alias})
	}
	identity := make([]dataquery.Field, 0, len(definition.Query.Identity))
	for _, fieldID := range definition.Query.Identity {
		for _, binding := range append(append([]visualizationdefinition.FieldBinding(nil), spatial.Dimensions...), spatial.Measures...) {
			if binding.FieldID == fieldID {
				identity = append(identity, dataquery.Field{Field: binding.FieldID, Alias: binding.Alias})
				break
			}
		}
	}
	metatileSize := int(spatial.Tiles.MetatileSize)
	metatileX, metatileY := x/metatileSize*metatileSize, y/metatileSize*metatileSize
	buffer := int(spatial.Tiles.CellRadius) * 16
	execute := func(precision dataquery.SpatialTilePrecision) (dataquery.Result, error) {
		targetZoom := spatialAggregateTargetZoom(zoom, rawMinimumZoom, int(spatial.Tiles.MaximumZoom))
		query := dataquery.Query{
			Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardSpatialTile,
			ModelID: definition.Query.ModelID, Kind: dataquery.KindSemanticSpatialTile,
			Fields: fields, Measures: fieldBindingsToDataFields(spatial.Measures), Filters: reportFiltersToDataFilters(queryFilters),
			SpatialTile: &dataquery.SpatialTile{
				Latitude: dataquery.Field{Field: spatial.Tiles.Latitude.FieldID, Alias: spatial.Tiles.Latitude.Alias}, Longitude: dataquery.Field{Field: spatial.Tiles.Longitude.FieldID, Alias: spatial.Tiles.Longitude.Alias},
				Identity: identity, Zoom: zoom, TargetZoom: targetZoom, MetatileX: metatileX, MetatileY: metatileY, MetatileSize: metatileSize,
				CellPixels: int(spatial.Tiles.CellRadius), Buffer: buffer, FeatureCap: int(spatial.Tiles.FeatureCap), Precision: precision,
			},
		}
		if spatial.Time != nil {
			query.Time = dataquery.Time{Field: spatial.Time.FieldID, Alias: spatial.Time.Alias, Grain: spatial.Time.Grain}
		}
		return runtime.data.ExecuteDataQuery(ctx, query)
	}
	precision := spatialTilePrecision(zoom, rawMinimumZoom)
	result, err := execute(precision)
	if err != nil {
		return SpatialTileResult{}, err
	}
	tile, features, found, err := spatialTileFromRows(result.Rows, x, y)
	if err != nil {
		return SpatialTileResult{}, err
	}
	if precision == dataquery.SpatialTilePrecisionRaw && !spatialRawMetatileFits(result.Rows, spatial.Tiles.MaximumBytes) {
		return SpatialTileResult{}, fmt.Errorf("raw spatial tile exceeds the revision-wide feature or byte budget at zoom %d", zoom)
	}
	if !found {
		tile = []byte{}
	}
	if int64(len(tile)) > spatial.Tiles.MaximumBytes {
		return SpatialTileResult{}, fmt.Errorf("encoded tile exceeds %d-byte budget", spatial.Tiles.MaximumBytes)
	}
	return SpatialTileResult{Bytes: tile, Features: features, Precision: string(precision), CacheOutcome: result.CacheOutcome}, nil
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
