// Package definition owns immutable, compiler-produced visualization
// definitions. It deliberately contains no authoring YAML or renderer-native
// configuration.
package definition

import (
	"fmt"

	"github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

const (
	RendererECharts  = "echarts"
	RendererTanStack = "tanstack"
	RendererHTML     = "html"
	RendererMapLibre = "maplibre"
)

type QueryKind string

type ResultShape string

const (
	QueryAggregate QueryKind = "aggregate"
	QueryDetail    QueryKind = "detail"
	QueryMatrix    QueryKind = "matrix"
	QueryPivot     QueryKind = "pivot"
	QuerySpatial   QueryKind = "spatial"
)

const (
	ResultScalar               ResultShape = "scalar"
	ResultCategoryValue        ResultShape = "category_value"
	ResultCategorySeriesValue  ResultShape = "category_series_value"
	ResultCategoryMultiMeasure ResultShape = "category_multi_measure"
	ResultCategoryDelta        ResultShape = "category_delta"
	ResultHistogramBins        ResultShape = "histogram_bins"
	ResultMatrixCells          ResultShape = "matrix_cells"
	ResultHierarchyNodes       ResultShape = "hierarchy_nodes"
	ResultGraphEdges           ResultShape = "graph_edges"
	ResultOHLC                 ResultShape = "ohlc"
	ResultDistribution         ResultShape = "distribution"
	ResultDetailWindow         ResultShape = "detail_window"
	ResultMatrixWindow         ResultShape = "matrix_window"
	ResultPivotWindow          ResultShape = "pivot_window"
	ResultGeographicFeatures   ResultShape = "geographic_features"
	ResultPoints               ResultShape = "points"
)

// QueryBinding is the closed compiler/runtime boundary. Exactly one branch is
// present and must match Kind. It contains stable semantic member IDs and
// compiler-resolved output aliases; authoring query objects never cross this
// boundary.
type QueryBinding struct {
	Kind        QueryKind   `json:"kind" yaml:"kind"`
	ResultShape ResultShape `json:"resultShape" yaml:"result_shape"`
	ModelID     string      `json:"modelID" yaml:"model_id"`
	DatasetID   string      `json:"datasetID" yaml:"dataset_id"`
	Identity    []string    `json:"identity,omitempty" yaml:"identity,omitempty"`

	Aggregate *AggregateQueryBinding `json:"aggregate,omitempty" yaml:"aggregate,omitempty"`
	Detail    *DetailQueryBinding    `json:"detail,omitempty" yaml:"detail,omitempty"`
	Matrix    *MatrixQueryBinding    `json:"matrix,omitempty" yaml:"matrix,omitempty"`
	Pivot     *PivotQueryBinding     `json:"pivot,omitempty" yaml:"pivot,omitempty"`
	Spatial   *SpatialQueryBinding   `json:"spatial,omitempty" yaml:"spatial,omitempty"`
}

type FieldBinding struct {
	FieldID string `json:"fieldID" yaml:"field_id"`
	Alias   string `json:"alias" yaml:"alias"`
}

type TimeBinding struct {
	FieldID string `json:"fieldID" yaml:"field_id"`
	Alias   string `json:"alias" yaml:"alias"`
	Grain   string `json:"grain" yaml:"grain"`
}

type AggregateQueryBinding struct {
	TableID    string         `json:"tableID" yaml:"table_id"`
	Dimensions []FieldBinding `json:"dimensions" yaml:"dimensions"`
	Series     *FieldBinding  `json:"series,omitempty" yaml:"series,omitempty"`
	Measures   []FieldBinding `json:"measures" yaml:"measures"`
	Time       *TimeBinding   `json:"time,omitempty" yaml:"time,omitempty"`
	Sort       []Sort         `json:"sort,omitempty" yaml:"sort,omitempty"`
	Limit      int64          `json:"limit" yaml:"limit"`
}

type DetailQueryBinding struct {
	TableID     string         `json:"tableID" yaml:"table_id"`
	Fields      []FieldBinding `json:"fields" yaml:"fields"`
	DefaultSort []Sort         `json:"defaultSort,omitempty" yaml:"default_sort,omitempty"`
	Limit       int64          `json:"limit" yaml:"limit"`
}

type MatrixQueryBinding struct {
	TableID  string         `json:"tableID" yaml:"table_id"`
	Rows     []FieldBinding `json:"rows" yaml:"rows"`
	Columns  []FieldBinding `json:"columns" yaml:"columns"`
	Measures []FieldBinding `json:"measures" yaml:"measures"`
	Limit    int64          `json:"limit" yaml:"limit"`
}

type PivotQueryBinding = MatrixQueryBinding

// SpatialQueryBinding is the compiler-resolved query contract for a
// geographic visualization. Tiles is present when every data-bound layer can
// use the governed MVT runtime. Viewport remains during the compatibility
// window and is removed once all active streams have tiled parity.
type SpatialQueryBinding struct {
	TableID    string                  `json:"tableID" yaml:"table_id"`
	Dimensions []FieldBinding          `json:"dimensions,omitempty" yaml:"dimensions,omitempty"`
	Series     *FieldBinding           `json:"series,omitempty" yaml:"series,omitempty"`
	Measures   []FieldBinding          `json:"measures,omitempty" yaml:"measures,omitempty"`
	Time       *TimeBinding            `json:"time,omitempty" yaml:"time,omitempty"`
	Sort       []Sort                  `json:"sort,omitempty" yaml:"sort,omitempty"`
	Limit      int64                   `json:"limit" yaml:"limit"`
	Viewport   *SpatialViewportBinding `json:"viewport,omitempty" yaml:"viewport,omitempty"`
	Tiles      *SpatialTileBinding     `json:"tiles,omitempty" yaml:"tiles,omitempty"`
}

// SpatialTileBinding identifies the compiler-resolved coordinate pair and
// bounded server-side transport policy for one native MapLibre vector source.
type SpatialTileBinding struct {
	Latitude       FieldBinding `json:"latitude" yaml:"latitude"`
	Longitude      FieldBinding `json:"longitude" yaml:"longitude"`
	MinimumZoom    int32        `json:"minimumZoom" yaml:"minimum_zoom"`
	MaximumZoom    int32        `json:"maximumZoom" yaml:"maximum_zoom"`
	RawMinimumZoom int32        `json:"rawMinimumZoom" yaml:"raw_minimum_zoom"`
	FeatureCap     int64        `json:"featureCap" yaml:"feature_cap"`
	MaximumBytes   int64        `json:"maximumBytes" yaml:"maximum_bytes"`
	MetatileSize   int32        `json:"metatileSize" yaml:"metatile_size"`
	CellRadius     int32        `json:"cellRadius" yaml:"cell_radius"`
}

// SpatialViewportBinding identifies the one compiler-resolved coordinate pair
// used to govern viewport requests. All coordinate layers in a windowed map
// must use this pair.
type SpatialViewportBinding struct {
	Latitude       FieldBinding `json:"latitude" yaml:"latitude"`
	Longitude      FieldBinding `json:"longitude" yaml:"longitude"`
	FeatureCap     int64        `json:"featureCap" yaml:"feature_cap"`
	RawMinimumZoom float64      `json:"rawMinimumZoom" yaml:"raw_minimum_zoom"`
}

type Sort struct {
	FieldID   string `json:"fieldID" yaml:"field_id"`
	Direction string `json:"direction" yaml:"direction"`
}

func (query QueryBinding) Validate() error {
	if query.Kind == "" || query.ResultShape == "" || query.ModelID == "" || query.DatasetID == "" {
		return fmt.Errorf("visualization query binding requires kind, result shape, model ID, and dataset ID")
	}
	branches := 0
	for _, present := range []bool{query.Aggregate != nil, query.Detail != nil, query.Matrix != nil, query.Pivot != nil, query.Spatial != nil} {
		if present {
			branches++
		}
	}
	if branches != 1 {
		return fmt.Errorf("visualization query binding requires exactly one query branch, got %d", branches)
	}
	view, err := query.validationView()
	if err != nil {
		return err
	}
	if viewport := view.viewport; viewport != nil {
		if view.tiles != nil {
			return fmt.Errorf("spatial query cannot use viewport and tiled delivery together")
		}
		if viewport.FeatureCap <= 0 || viewport.FeatureCap > view.limit {
			return fmt.Errorf("spatial viewport requires a positive feature cap no greater than its row limit")
		}
		if viewport.RawMinimumZoom < 0 || viewport.RawMinimumZoom > 24 {
			return fmt.Errorf("spatial viewport raw minimum zoom must be between 0 and 24")
		}
		if !containsFieldBinding(view.fields, viewport.Latitude) || !containsFieldBinding(view.fields, viewport.Longitude) {
			return fmt.Errorf("spatial viewport coordinates must reference compiled query fields")
		}
	}
	if tiles := view.tiles; tiles != nil {
		if view.limit != 0 {
			return fmt.Errorf("spatial tiled query must not use a row transport limit")
		}
		if tiles.MinimumZoom < 0 || tiles.MaximumZoom > 24 || tiles.MinimumZoom > tiles.MaximumZoom {
			return fmt.Errorf("spatial tiles require an ordered zoom range between 0 and 24")
		}
		if tiles.RawMinimumZoom < tiles.MinimumZoom || tiles.RawMinimumZoom > tiles.MaximumZoom {
			return fmt.Errorf("spatial tiles raw minimum zoom must be within the tile zoom range")
		}
		if tiles.FeatureCap <= 0 || tiles.MaximumBytes <= 0 || tiles.MetatileSize <= 0 {
			return fmt.Errorf("spatial tiles require positive feature, byte, and metatile budgets")
		}
		if tiles.CellRadius < 32 || tiles.CellRadius > 64 {
			return fmt.Errorf("spatial tile cell radius must be between 32 and 64 CSS pixels")
		}
		if !containsFieldBinding(view.fields, tiles.Latitude) || !containsFieldBinding(view.fields, tiles.Longitude) {
			return fmt.Errorf("spatial tile coordinates must reference compiled query fields")
		}
	}
	if !queryKindSupportsResult(query.Kind, query.ResultShape) {
		return fmt.Errorf("visualization query kind %q does not support result shape %q", query.Kind, query.ResultShape)
	}
	if query.Kind == QueryDetail && view.tableID == "" {
		return fmt.Errorf("visualization detail query requires table ID")
	}
	if len(view.fields) == 0 || (view.limit <= 0 && view.tiles == nil) {
		return fmt.Errorf("visualization %s query requires fields and a positive limit unless tiled", query.Kind)
	}
	aliases := make(map[string]int, len(view.fields))
	fieldIDs := make(map[string]struct{}, len(view.fields))
	for index, field := range view.fields {
		if field.FieldID == "" || field.Alias == "" {
			return fmt.Errorf("visualization %s query field %d requires field ID and alias", query.Kind, index)
		}
		if previous, exists := aliases[field.Alias]; exists {
			return fmt.Errorf("visualization %s query fields %d and %d use duplicate alias %q", query.Kind, previous, index, field.Alias)
		}
		aliases[field.Alias] = index
		fieldIDs[field.FieldID] = struct{}{}
	}
	if view.time != nil && view.time.Grain == "" {
		return fmt.Errorf("visualization %s time field requires grain", query.Kind)
	}
	identities := make(map[string]struct{}, len(query.Identity))
	for index, identity := range query.Identity {
		if identity == "" {
			return fmt.Errorf("visualization %s identity %d is empty", query.Kind, index)
		}
		if _, exists := identities[identity]; exists {
			return fmt.Errorf("visualization %s identity %q is duplicated", query.Kind, identity)
		}
		if _, exists := fieldIDs[identity]; !exists {
			return fmt.Errorf("visualization %s identity %q does not reference a query field", query.Kind, identity)
		}
		identities[identity] = struct{}{}
	}
	for index, sort := range view.sorts {
		if sort.FieldID == "" {
			return fmt.Errorf("visualization %s sort %d requires a field", query.Kind, index)
		}
		if sort.Direction != "asc" && sort.Direction != "desc" {
			return fmt.Errorf("visualization %s sort %d has unsupported direction %q", query.Kind, index, sort.Direction)
		}
	}
	return nil
}

func queryKindSupportsResult(kind QueryKind, shape ResultShape) bool {
	switch kind {
	case QueryAggregate:
		switch shape {
		case ResultScalar, ResultCategoryValue, ResultCategorySeriesValue, ResultCategoryMultiMeasure, ResultCategoryDelta, ResultHistogramBins, ResultMatrixCells, ResultHierarchyNodes, ResultGraphEdges, ResultOHLC, ResultDistribution, ResultPoints:
			return true
		}
	case QueryDetail:
		return shape == ResultDetailWindow
	case QueryMatrix:
		return shape == ResultMatrixWindow
	case QueryPivot:
		return shape == ResultPivotWindow
	case QuerySpatial:
		return shape == ResultGeographicFeatures
	}
	return false
}

func containsFieldBinding(fields []FieldBinding, target FieldBinding) bool {
	if target.FieldID == "" || target.Alias == "" {
		return false
	}
	for _, field := range fields {
		if field == target {
			return true
		}
	}
	return false
}

type queryBindingView struct {
	tableID  string
	fields   []FieldBinding
	sorts    []Sort
	time     *TimeBinding
	limit    int64
	viewport *SpatialViewportBinding
	tiles    *SpatialTileBinding
}

func (query QueryBinding) validationView() (queryBindingView, error) {
	var view queryBindingView
	addAggregateFields := func(dimensions []FieldBinding, series *FieldBinding, time *TimeBinding, measures []FieldBinding) {
		view.fields = append(view.fields, dimensions...)
		if series != nil {
			view.fields = append(view.fields, *series)
		}
		view.time = time
		if time != nil {
			view.fields = append(view.fields, FieldBinding{FieldID: time.FieldID, Alias: time.Alias})
		}
		view.fields = append(view.fields, measures...)
	}
	switch query.Kind {
	case QueryAggregate:
		if query.Aggregate == nil {
			return queryBindingView{}, fmt.Errorf("aggregate query binding requires aggregate branch")
		}
		view.tableID, view.sorts, view.limit = query.Aggregate.TableID, query.Aggregate.Sort, query.Aggregate.Limit
		addAggregateFields(query.Aggregate.Dimensions, query.Aggregate.Series, query.Aggregate.Time, query.Aggregate.Measures)
	case QueryDetail:
		if query.Detail == nil {
			return queryBindingView{}, fmt.Errorf("detail query binding requires detail branch")
		}
		view.tableID, view.fields, view.sorts, view.limit = query.Detail.TableID, query.Detail.Fields, query.Detail.DefaultSort, query.Detail.Limit
	case QueryMatrix:
		if query.Matrix == nil {
			return queryBindingView{}, fmt.Errorf("matrix query binding requires matrix branch")
		}
		view.tableID, view.limit = query.Matrix.TableID, query.Matrix.Limit
		view.fields = append(view.fields, query.Matrix.Rows...)
		view.fields = append(view.fields, query.Matrix.Columns...)
		view.fields = append(view.fields, query.Matrix.Measures...)
	case QueryPivot:
		if query.Pivot == nil {
			return queryBindingView{}, fmt.Errorf("pivot query binding requires pivot branch")
		}
		view.tableID, view.limit = query.Pivot.TableID, query.Pivot.Limit
		view.fields = append(view.fields, query.Pivot.Rows...)
		view.fields = append(view.fields, query.Pivot.Columns...)
		view.fields = append(view.fields, query.Pivot.Measures...)
	case QuerySpatial:
		if query.Spatial == nil {
			return queryBindingView{}, fmt.Errorf("spatial query binding requires spatial branch")
		}
		view.tableID, view.sorts, view.limit, view.viewport, view.tiles = query.Spatial.TableID, query.Spatial.Sort, query.Spatial.Limit, query.Spatial.Viewport, query.Spatial.Tiles
		addAggregateFields(query.Spatial.Dimensions, query.Spatial.Series, query.Spatial.Time, query.Spatial.Measures)
	default:
		return queryBindingView{}, fmt.Errorf("unsupported visualization query kind %q", query.Kind)
	}
	return view, nil
}

type Definition struct {
	ID               string                  `json:"id" yaml:"id"`
	RendererID       string                  `json:"rendererID" yaml:"renderer_id"`
	SpecRevision     string                  `json:"specRevision" yaml:"spec_revision"`
	Spec             ir.VisualizationSpec    `json:"spec" yaml:"spec"`
	Query            QueryBinding            `json:"query" yaml:"query"`
	SecondaryQueries map[string]QueryBinding `json:"secondaryQueries,omitempty" yaml:"secondary_queries,omitempty"`
}

func New(id string, spec ir.VisualizationSpec, query QueryBinding) (Definition, error) {
	return NewWithSecondaryQueries(id, spec, query, nil)
}

// NewWithSecondaryQueries constructs an immutable visualization definition
// with optional compiler-owned context datasets. Secondary queries remain
// governed aggregate bindings and are addressed by their stable dataset ID.
func NewWithSecondaryQueries(id string, spec ir.VisualizationSpec, query QueryBinding, secondary map[string]QueryBinding) (Definition, error) {
	renderer, expectedQuery, err := ownership(spec)
	if err != nil {
		return Definition{}, err
	}
	if query.Kind == "" {
		query.Kind = expectedQuery
	}
	revision, err := ir.ComputeSpecRevision(spec)
	if err != nil {
		return Definition{}, fmt.Errorf("compute visualization %q specification revision: %w", id, err)
	}
	definition := Definition{ID: id, RendererID: renderer, SpecRevision: revision.String(), Spec: spec, Query: query}
	if len(secondary) > 0 {
		definition.SecondaryQueries = make(map[string]QueryBinding, len(secondary))
		for datasetID, binding := range secondary {
			definition.SecondaryQueries[datasetID] = binding
		}
	}
	if err := definition.Validate(); err != nil {
		return Definition{}, err
	}
	return definition, nil
}

func (definition Definition) Validate() error {
	if definition.ID == "" || definition.RendererID == "" || definition.Query.Kind == "" {
		return fmt.Errorf("visualization definition requires ID, renderer, and query kind")
	}
	if err := definition.Query.Validate(); err != nil {
		return fmt.Errorf("visualization %q query: %w", definition.ID, err)
	}
	if err := ir.ValidateSpec(definition.Spec); err != nil {
		return fmt.Errorf("visualization %q specification: %w", definition.ID, err)
	}
	renderer, queryKind, err := ownership(definition.Spec)
	if err != nil {
		return err
	}
	if definition.RendererID != renderer {
		return fmt.Errorf("visualization %q renderer %q, want %q", definition.ID, definition.RendererID, renderer)
	}
	if definition.Query.Kind != queryKind {
		return fmt.Errorf("visualization %q query kind %q, want %q", definition.ID, definition.Query.Kind, queryKind)
	}
	base, err := ir.SpecificationBase(definition.Spec)
	if err != nil {
		return err
	}
	if err := validateSecondaryQueries(definition, base); err != nil {
		return err
	}
	if !specSupportsResultShape(definition.Spec, definition.Query.ResultShape) {
		return fmt.Errorf("visualization %q specification does not support result shape %q", definition.ID, definition.Query.ResultShape)
	}
	if err := validateQuerySortFields(definition.Spec, definition.Query); err != nil {
		return fmt.Errorf("visualization %q query: %w", definition.ID, err)
	}
	revision, err := ir.ComputeSpecRevision(definition.Spec)
	if err != nil {
		return err
	}
	if definition.SpecRevision != revision.String() {
		return fmt.Errorf("visualization %q specification revision mismatch", definition.ID)
	}
	return nil
}

func validateSecondaryQueries(definition Definition, base ir.VisualizationSpecBase) error {
	schemas := make(map[string]struct{}, len(base.Datasets))
	for _, schema := range base.Datasets {
		schemas[schema.ID] = struct{}{}
	}
	if _, ok := schemas[definition.Query.DatasetID]; !ok {
		return fmt.Errorf("visualization %q primary query references unknown dataset %q", definition.ID, definition.Query.DatasetID)
	}
	primaryView, err := definition.Query.validationView()
	if err != nil {
		return err
	}
	if primaryView.tiles != nil && base.DataBudget.MaxRows != 0 {
		return fmt.Errorf("visualization %q tiled spatial query must not declare a row budget", definition.ID)
	}
	if primaryView.tiles == nil && primaryView.limit > base.DataBudget.MaxRows {
		return fmt.Errorf("visualization %q primary query limit %d exceeds row budget %d", definition.ID, primaryView.limit, base.DataBudget.MaxRows)
	}
	for datasetID, query := range definition.SecondaryQueries {
		if datasetID == "" || datasetID == definition.Query.DatasetID {
			return fmt.Errorf("visualization %q secondary query dataset %q is invalid", definition.ID, datasetID)
		}
		if query.DatasetID != datasetID {
			return fmt.Errorf("visualization %q secondary query key %q does not match dataset %q", definition.ID, datasetID, query.DatasetID)
		}
		if _, ok := schemas[datasetID]; !ok {
			return fmt.Errorf("visualization %q secondary query references unknown dataset %q", definition.ID, datasetID)
		}
		if query.Kind != QueryAggregate {
			return fmt.Errorf("visualization %q secondary dataset %q requires an aggregate query", definition.ID, datasetID)
		}
		if query.ModelID != definition.Query.ModelID {
			return fmt.Errorf("visualization %q secondary dataset %q must use primary model %q", definition.ID, datasetID, definition.Query.ModelID)
		}
		if err := query.Validate(); err != nil {
			return fmt.Errorf("visualization %q secondary dataset %q: %w", definition.ID, datasetID, err)
		}
		view, err := query.validationView()
		if err != nil {
			return err
		}
		if view.limit > base.DataBudget.MaxRows {
			return fmt.Errorf("visualization %q secondary dataset %q limit %d exceeds row budget %d", definition.ID, datasetID, view.limit, base.DataBudget.MaxRows)
		}
		if err := validateQuerySortFields(definition.Spec, query); err != nil {
			return fmt.Errorf("visualization %q secondary dataset %q: %w", definition.ID, datasetID, err)
		}
	}
	for datasetID := range schemas {
		if datasetID == definition.Query.DatasetID {
			continue
		}
		if _, ok := definition.SecondaryQueries[datasetID]; !ok {
			return fmt.Errorf("visualization %q dataset %q has no compiled query", definition.ID, datasetID)
		}
	}
	return nil
}

func validateQuerySortFields(spec ir.VisualizationSpec, query QueryBinding) error {
	base, err := ir.SpecificationBase(spec)
	if err != nil {
		return err
	}
	available := map[string]struct{}{}
	for _, dataset := range base.Datasets {
		if dataset.ID != query.DatasetID {
			continue
		}
		for _, field := range dataset.Fields {
			available[field.ID] = struct{}{}
			if field.SourceRef != nil {
				available[*field.SourceRef] = struct{}{}
			}
		}
	}
	view, err := query.validationView()
	if err != nil {
		return err
	}
	for _, field := range view.fields {
		available[field.FieldID] = struct{}{}
		available[field.Alias] = struct{}{}
	}
	for _, sort := range view.sorts {
		if _, ok := available[sort.FieldID]; !ok {
			return fmt.Errorf("sort field %q does not reference a compiled query or dataset field", sort.FieldID)
		}
	}
	return nil
}

func specSupportsResultShape(spec ir.VisualizationSpec, shape ResultShape) bool {
	switch value := spec.Value.(type) {
	case *ir.PointVisualizationSpec:
		return shape == ResultPoints
	case *ir.CartesianVisualizationSpec:
		switch value.Mark {
		case ir.VisualizationCartesianMarkWaterfall:
			return shape == ResultCategoryDelta
		case ir.VisualizationCartesianMarkHistogram:
			return shape == ResultHistogramBins
		case ir.VisualizationCartesianMarkHeatmap:
			return shape == ResultMatrixCells
		case ir.VisualizationCartesianMarkCandlestick:
			return shape == ResultOHLC
		case ir.VisualizationCartesianMarkBoxplot:
			return shape == ResultDistribution
		case ir.VisualizationCartesianMarkCombo:
			return shape == ResultCategoryMultiMeasure
		default:
			return shape == ResultCategoryValue || shape == ResultCategorySeriesValue || shape == ResultCategoryMultiMeasure
		}
	case *ir.ProportionalVisualizationSpec:
		return shape == ResultCategoryValue || shape == ResultCategorySeriesValue
	case *ir.HierarchyVisualizationSpec:
		if value.Mark == ir.VisualizationHierarchyMarkGraph || value.Mark == ir.VisualizationHierarchyMarkSankey {
			return shape == ResultGraphEdges
		}
		return shape == ResultHierarchyNodes
	case *ir.PolarVisualizationSpec:
		if value.Mark == ir.VisualizationPolarMarkGauge {
			return shape == ResultScalar
		}
		return shape == ResultCategoryValue || shape == ResultCategorySeriesValue
	case *ir.KPIVisualizationSpec:
		return shape == ResultScalar
	case *ir.GeographicVisualizationSpec:
		return shape == ResultGeographicFeatures
	case *ir.TableVisualizationSpec:
		return shape == ResultDetailWindow
	case *ir.MatrixVisualizationSpec:
		return shape == ResultMatrixWindow
	case *ir.PivotVisualizationSpec:
		return shape == ResultPivotWindow
	default:
		return false
	}
}

func ownership(spec ir.VisualizationSpec) (string, QueryKind, error) {
	switch spec.Value.(type) {
	case *ir.CartesianVisualizationSpec,
		*ir.PointVisualizationSpec,
		*ir.ProportionalVisualizationSpec,
		*ir.HierarchyVisualizationSpec,
		*ir.PolarVisualizationSpec:
		return RendererECharts, QueryAggregate, nil
	case *ir.TableVisualizationSpec:
		return RendererTanStack, QueryDetail, nil
	case *ir.MatrixVisualizationSpec:
		return RendererTanStack, QueryMatrix, nil
	case *ir.PivotVisualizationSpec:
		return RendererTanStack, QueryPivot, nil
	case *ir.KPIVisualizationSpec:
		return RendererHTML, QueryAggregate, nil
	case *ir.GeographicVisualizationSpec:
		return RendererMapLibre, QuerySpatial, nil
	default:
		return "", "", fmt.Errorf("unsupported visualization specification %T", spec.Value)
	}
}
