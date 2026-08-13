package compiler

import (
	"fmt"
	"math"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
)

func compileSecondaryQueryBindings(ctx compileContext, authored reportdef.Visual) (map[string]visualizationdefinition.QueryBinding, error) {
	if len(authored.Datasets) == 0 {
		return nil, nil
	}
	out := make(map[string]visualizationdefinition.QueryBinding, len(authored.Datasets))
	datasetIDs := make([]string, 0, len(authored.Datasets))
	for datasetID := range authored.Datasets {
		datasetIDs = append(datasetIDs, datasetID)
	}
	sort.Strings(datasetIDs)
	for _, datasetID := range datasetIDs {
		query := authored.Datasets[datasetID]
		tableID := query.Table
		if tableID == "" {
			tableID = authored.Query.Table
		}
		limit := query.Limit
		if limit <= 0 {
			limit = 1000
		}
		maxRows := int(compiledVisualDataBudgetMaxRows(authored, authored.ResultShape()))
		if limit > maxRows {
			limit = maxRows
		}
		binding := visualizationdefinition.QueryBinding{
			Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultCategoryMultiMeasure,
			ModelID: ctx.modelID, DatasetID: datasetID,
			Aggregate: &visualizationdefinition.AggregateQueryBinding{
				TableID: tableID, Dimensions: compiledFields(query.Dimensions), Measures: compiledFields(query.Measures),
				Series: compiledOptionalField(query.Series), Time: compiledTime(query.Time), Sort: compiledSort(query.Sort), Limit: int64(limit),
			},
		}
		if err := binding.Validate(); err != nil {
			return nil, fmt.Errorf("context dataset %q: %w", datasetID, err)
		}
		out[datasetID] = binding
	}
	return out, nil
}

func compileVisualizationQueryBinding(ctx compileContext, authored reportdef.Visual) (visualizationdefinition.QueryBinding, error) {
	limit := compiledVisualLimit(authored)
	resultShape, err := compiledVisualResultShape(authored)
	if err != nil {
		return visualizationdefinition.QueryBinding{}, err
	}
	binding := visualizationdefinition.QueryBinding{
		Kind: visualizationdefinition.QueryAggregate, ResultShape: resultShape, ModelID: ctx.modelID, DatasetID: ctx.datasetID,
		Identity: compiledVisualizationIdentity(authored),
		Aggregate: &visualizationdefinition.AggregateQueryBinding{
			TableID: authored.Query.Table, Dimensions: compiledFields(authored.Query.Dimensions), Measures: compiledFields(authored.Query.Measures),
			Series: compiledOptionalField(authored.Query.Series), Time: compiledTime(authored.Query.Time), Sort: compiledSort(authored.Query.Sort), Limit: limit,
		},
	}
	switch ctx.capability.Renderer {
	case visualizationdefinition.RendererMapLibre:
		return compiledSpatialBinding(ctx.modelID, authored, ctx.model)
	}
	return binding, nil
}

func compiledVisualizationIdentity(authored reportdef.Visual) []string {
	identities := interactionIdentity(authored.Interaction.PointSelection)
	if authored.ResultShape() != "point" {
		return identities
	}
	for _, identityAlias := range authored.Point.Identity {
		for _, field := range compiledVisualFields(authored.Query) {
			if field.Alias == identityAlias {
				identities = append(identities, field.FieldID)
				break
			}
		}
	}
	return uniqueStrings(identities)
}

func compiledVisualResultShape(authored reportdef.Visual) (visualizationdefinition.ResultShape, error) {
	if _, ok := reportdef.VisualizationCapabilityForType(authored.Type); !ok {
		return "", fmt.Errorf("unsupported visualization type %q", authored.Type)
	}
	switch authored.ResultShape() {
	case "single_value":
		return visualizationdefinition.ResultScalar, nil
	case "category_multi_measure":
		return visualizationdefinition.ResultCategoryMultiMeasure, nil
	case "category_delta":
		return visualizationdefinition.ResultCategoryDelta, nil
	case "binned_measure":
		return visualizationdefinition.ResultHistogramBins, nil
	case "hierarchy":
		return visualizationdefinition.ResultHierarchyNodes, nil
	case "matrix":
		return visualizationdefinition.ResultMatrixCells, nil
	case "graph":
		return visualizationdefinition.ResultGraphEdges, nil
	case "geo":
		return visualizationdefinition.ResultGeographicFeatures, nil
	case "ohlc":
		return visualizationdefinition.ResultOHLC, nil
	case "distribution":
		return visualizationdefinition.ResultDistribution, nil
	case "point":
		return visualizationdefinition.ResultPoints, nil
	case "category_series_value":
		return visualizationdefinition.ResultCategorySeriesValue, nil
	case "category_value":
		return visualizationdefinition.ResultCategoryValue, nil
	default:
		return "", fmt.Errorf("unsupported visualization result shape %q", authored.ResultShape())
	}
}

func compiledSpatialBinding(modelID string, authored reportdef.Visual, model *semanticmodel.Model) (visualizationdefinition.QueryBinding, error) {
	tiled, err := geographicUsesTiledDelivery(authored)
	if err != nil {
		return visualizationdefinition.QueryBinding{}, err
	}
	limit := compiledVisualLimit(authored)
	if tiled {
		limit = 0
	}
	spatial := &visualizationdefinition.SpatialQueryBinding{
		TableID: authored.Query.Table, Dimensions: compiledFields(authored.Query.Dimensions), Measures: compiledFields(authored.Query.Measures),
		Series: compiledOptionalField(authored.Query.Series), Time: compiledTime(authored.Query.Time), Sort: compiledSort(authored.Query.Sort), Limit: limit,
	}
	if tiled {
		latitudeAlias, longitudeAlias, found, err := authoredTiledCoordinates(authored.Geo.Layers)
		if err != nil {
			return visualizationdefinition.QueryBinding{}, err
		}
		if !found {
			return visualizationdefinition.QueryBinding{}, fmt.Errorf("tiled geographic visual requires a point, heat, or density coordinate layer")
		}
		fields := compiledVisualFields(authored.Query)
		latitude, latitudeOK := fieldBindingByAlias(fields, latitudeAlias)
		longitude, longitudeOK := fieldBindingByAlias(fields, longitudeAlias)
		if !latitudeOK || !longitudeOK {
			return visualizationdefinition.QueryBinding{}, fmt.Errorf("spatial tile coordinates %q and %q must reference compiled query aliases", latitudeAlias, longitudeAlias)
		}
		if spatial.TableID == "" && model != nil {
			latitudeDimension, latitudeErr := model.ResolveDimension(latitude.FieldID)
			longitudeDimension, longitudeErr := model.ResolveDimension(longitude.FieldID)
			if latitudeErr == nil && longitudeErr == nil && latitudeDimension.Table == longitudeDimension.Table {
				spatial.TableID = latitudeDimension.Table
			}
		}
		if spatial.TableID == "" {
			return visualizationdefinition.QueryBinding{}, fmt.Errorf("tiled geographic visual must set query.table when its coordinate fields do not resolve to one fact table")
		}
		spatial.Tiles = &visualizationdefinition.SpatialTileBinding{
			Latitude: latitude, Longitude: longitude,
			MinimumZoom: 0, MaximumZoom: 18, RawMinimumZoom: 5,
			FeatureCap: 5000, MaximumBytes: 512 * 1024, MetatileSize: 4,
			CellRadius: tiledCellRadius(authored.Geo.Layers),
		}
	}
	return visualizationdefinition.QueryBinding{
		Kind: visualizationdefinition.QuerySpatial, ResultShape: visualizationdefinition.ResultGeographicFeatures, ModelID: modelID, DatasetID: "primary", Identity: interactionIdentity(authored.Interaction.PointSelection), Spatial: spatial,
	}, nil
}

func geographicUsesTiledDelivery(authored reportdef.Visual) (bool, error) {
	hasTiled, hasInline := false, false
	for _, layer := range authored.Geo.Layers {
		switch layer.Kind {
		case "point", "heat", "density":
			hasTiled = true
		case "choropleth", "path":
			hasInline = true
		case "reference":
			// Reference geometry remains an independent overlay.
		}
	}
	if hasTiled && hasInline {
		return false, fmt.Errorf("geographic visual cannot mix tiled point/heat/density layers with inline choropleth/path data layers; split them into separate visuals or move geometry to a reference overlay")
	}
	if !hasTiled {
		return false, nil
	}
	if authored.Query.Limit > 0 {
		return false, fmt.Errorf("tiled geographic visual must not set query.limit; tile budgets govern transport independently of source cardinality")
	}
	if authored.DataBudget.MaxRows > 0 {
		return false, fmt.Errorf("tiled geographic visual must not set data_budget.max_rows; tile budgets govern transport independently of source cardinality")
	}
	return true, nil
}

func authoredTiledCoordinates(layers []reportdef.VisualGeoLayer) (latitude, longitude string, found bool, err error) {
	for _, layer := range layers {
		switch layer.Kind {
		case "point", "heat", "density":
		default:
			continue
		}
		if strings.TrimSpace(layer.Latitude) == "" || strings.TrimSpace(layer.Longitude) == "" {
			continue
		}
		if !found {
			latitude, longitude, found = layer.Latitude, layer.Longitude, true
			continue
		}
		if latitude != layer.Latitude || longitude != layer.Longitude {
			return "", "", false, fmt.Errorf("tiled geographic coordinate layers must share one latitude/longitude pair")
		}
	}
	return latitude, longitude, found, nil
}

func tiledCellRadius(layers []reportdef.VisualGeoLayer) int32 {
	radius := 0.0
	for _, layer := range layers {
		switch layer.Kind {
		case "point":
			radius = math.Max(radius, layer.Size.MaximumRadius)
			if layer.Cluster.Enabled {
				radius = math.Max(radius, float64(layer.Cluster.Radius))
			}
		case "heat", "density":
			radius = math.Max(radius, layer.Heat.Radius)
		}
	}
	return int32(math.Round(math.Max(32, math.Min(64, radius))))
}

func fieldBindingByAlias(fields []visualizationdefinition.FieldBinding, alias string) (visualizationdefinition.FieldBinding, bool) {
	for _, field := range fields {
		if field.Alias == alias {
			return field, true
		}
	}
	return visualizationdefinition.FieldBinding{}, false
}

func compiledVisualLimit(authored reportdef.Visual) int64 {
	if authored.DataBudget.MaxRows > 0 {
		return int64(authored.DataBudget.MaxRows)
	}
	if authored.Query.Limit > 0 {
		return int64(authored.Query.Limit)
	}
	if authored.Type == "kpi" || authored.Type == "gauge" {
		return 1
	}
	if authored.Type == "map" {
		return 20_000
	}
	return 1000
}

func compiledVisualFrameLimit(authored reportdef.Visual, shape string) int64 {
	limit := compiledVisualLimit(authored)
	if authored.DataBudget.MaxRows > 0 {
		return limit
	}
	switch shape {
	case "category_multi_measure":
		series := len(authored.Query.Measures)
		if series < 1 {
			series = 1
		}
		return limit * int64(series)
	case "hierarchy":
		levels := len(authored.Query.Dimensions)
		if authored.Query.Time.Field != "" {
			levels++
		}
		if levels < 1 {
			levels = 1
		}
		return limit * int64(levels)
	default:
		return limit
	}
}

func compiledVisualDataBudgetMaxRows(authored reportdef.Visual, shape string) int64 {
	if authored.DataBudget.MaxRows > 0 {
		return int64(authored.DataBudget.MaxRows)
	}
	maxRows := compiledVisualFrameLimit(authored, shape)
	for _, query := range authored.Datasets {
		limit := query.Limit
		if limit <= 0 {
			limit = 1000
		}
		if int64(limit) > maxRows {
			maxRows = int64(limit)
		}
	}
	return maxRows
}

func compiledTableBinding(modelID, visualType string, authored reportdef.TableVisual) visualizationdefinition.QueryBinding {
	binding := visualizationdefinition.QueryBinding{
		ModelID: modelID, DatasetID: "primary", Identity: interactionIdentity(authored.Interaction.RowSelection),
	}
	switch visualType {
	case "matrix":
		binding.Kind = visualizationdefinition.QueryMatrix
		binding.ResultShape = visualizationdefinition.ResultMatrixWindow
		binding.Matrix = &visualizationdefinition.MatrixQueryBinding{
			TableID: authored.Query.Table, Rows: compiledFields(authored.Query.Rows), Columns: compiledFields(authored.Query.Columns), Measures: compiledFields(authored.Query.Measures), Limit: dashboard.TableInteractiveRowCap,
		}
	case "pivot":
		binding.Kind = visualizationdefinition.QueryPivot
		binding.ResultShape = visualizationdefinition.ResultPivotWindow
		binding.Pivot = &visualizationdefinition.PivotQueryBinding{
			TableID: authored.Query.Table, Rows: compiledFields(authored.Query.Rows), Columns: compiledFields(authored.Query.Columns), Measures: compiledFields(authored.Query.Measures), Limit: dashboard.TableInteractiveRowCap,
		}
	default:
		sort := []visualizationdefinition.Sort{}
		if authored.DefaultSort.Key != "" {
			sort = append(sort, visualizationdefinition.Sort{FieldID: authored.DefaultSort.Key, Direction: authored.DefaultSort.Direction})
		}
		binding.Kind = visualizationdefinition.QueryDetail
		binding.ResultShape = visualizationdefinition.ResultDetailWindow
		binding.Detail = &visualizationdefinition.DetailQueryBinding{
			TableID: authored.Query.Table, Fields: compiledTableFields(authored), DefaultSort: sort, Limit: dashboard.TableInteractiveRowCap,
		}
	}
	return binding
}

func compiledFields(fields []reportdef.FieldRef) []visualizationdefinition.FieldBinding {
	out := make([]visualizationdefinition.FieldBinding, 0, len(fields))
	for _, field := range fields {
		if strings.TrimSpace(field.Field) == "" {
			continue
		}
		alias := field.Alias
		if alias == "" {
			alias = fieldAlias(field.Field)
		}
		out = append(out, visualizationdefinition.FieldBinding{FieldID: field.Field, Alias: alias})
	}
	return out
}

func compiledVisualFields(query reportdef.VisualQuery) []visualizationdefinition.FieldBinding {
	out := compiledFields(query.Dimensions)
	if query.Time.Field != "" {
		alias := query.Time.Alias
		if alias == "" {
			alias = fieldAlias(query.Time.Field)
		}
		out = append(out, visualizationdefinition.FieldBinding{FieldID: query.Time.Field, Alias: alias})
	}
	if series := compiledOptionalField(query.Series); series != nil {
		out = append(out, *series)
	}
	out = append(out, compiledFields(query.Measures)...)
	return out
}

func compiledOptionalField(field reportdef.FieldRef) *visualizationdefinition.FieldBinding {
	values := compiledFields([]reportdef.FieldRef{field})
	if len(values) == 0 {
		return nil
	}
	return &values[0]
}

func compiledTime(value reportdef.QueryTime) *visualizationdefinition.TimeBinding {
	if value.Field == "" {
		return nil
	}
	alias := value.Alias
	if alias == "" {
		alias = fieldAlias(value.Field)
	}
	return &visualizationdefinition.TimeBinding{FieldID: value.Field, Alias: alias, Grain: value.Grain}
}

func compiledTableFields(table reportdef.TableVisual) []visualizationdefinition.FieldBinding {
	fields := compiledFields(table.DataColumns)
	if len(fields) > 0 {
		return fields
	}
	out := make([]visualizationdefinition.FieldBinding, 0, len(table.Query.Fields))
	for _, field := range table.Query.Fields {
		out = append(out, visualizationdefinition.FieldBinding{FieldID: field, Alias: fieldAlias(field)})
	}
	return out
}

func fieldAlias(field string) string {
	parts := strings.Split(field, ".")
	return parts[len(parts)-1]
}

func visualQueryFields(query reportdef.VisualQuery) []string {
	fields := make([]string, 0, len(query.Dimensions)+len(query.Measures)+2)
	for _, value := range query.Dimensions {
		fields = append(fields, value.Field)
	}
	if !query.Series.IsZero() {
		fields = append(fields, query.Series.Field)
	}
	if query.Time.Field != "" {
		fields = append(fields, query.Time.Field)
	}
	for _, value := range query.Measures {
		fields = append(fields, value.Field)
	}
	return uniqueStrings(fields)
}

func tableQueryFields(table reportdef.TableVisual) []string {
	fields := make([]string, 0, len(table.DataColumns)+len(table.Query.Fields)+len(table.Query.Rows)+len(table.Query.Columns)+len(table.Query.Measures))
	for _, value := range table.DataColumns {
		fields = append(fields, value.Field)
	}
	fields = append(fields, table.Query.Fields...)
	for _, values := range [][]reportdef.FieldRef{table.Query.Rows, table.Query.Columns, table.Query.Measures} {
		for _, value := range values {
			fields = append(fields, value.Field)
		}
	}
	return uniqueStrings(fields)
}

func compiledSort(values []reportdef.Sort) []visualizationdefinition.Sort {
	out := make([]visualizationdefinition.Sort, len(values))
	for index, value := range values {
		out[index] = visualizationdefinition.Sort{FieldID: value.Field, Direction: value.Direction}
	}
	return out
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
