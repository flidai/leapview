package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type VisualizationDataService struct {
	mu       *sync.RWMutex
	reports  *ReportService
	runtimes map[projectgraph.ResourceID]*modelRuntime
	filters  *FilterService
	tiles    *spatialTileRegistry
}

func (s *VisualizationDataService) visuals(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, filters dashboard.Filters, keys []string) (map[string]visualizationir.VisualizationEnvelope, error) {
	visuals := make(map[string]visualizationir.VisualizationEnvelope, len(keys))
	batchedData, err := s.batchedSingleValueData(ctx, runtime, report, filters, keys)
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		definition, ok := report.Visualizations[key]
		if !ok {
			return nil, fmt.Errorf("page references unknown visual %q", key)
		}
		visual, err := newVisualPlan(definition)
		if err != nil {
			return nil, err
		}
		data, batched := batchedData[key]
		if !batched {
			data, err = s.visualData(ctx, runtime, report, key, visual, filters)
			if err != nil {
				return nil, err
			}
		}
		frame, err := frameFromDatums(definition, data)
		if err != nil {
			return nil, err
		}
		frame.Completeness = visualizationFrameCompleteness(definition.Spec, definition.Query.DatasetID, len(data), visualizationQueryLimit(definition.Query))
		frames := map[string]visualizationruntime.Frame{definition.Query.DatasetID: frame}
		contextFrames, err := s.contextFrames(ctx, runtime, report, key, definition, filters)
		if err != nil {
			return nil, err
		}
		for datasetID, contextFrame := range contextFrames {
			frames[datasetID] = contextFrame
		}
		envelope, err := visualizationruntime.EnvelopeFromFrames(definition, frames, selectedEntries(filters, "visual", key), 0, 0)
		if err != nil {
			return nil, err
		}
		envelope.Highlights, err = selectedHighlights(runtime, report, filters, key)
		if err != nil {
			return nil, err
		}
		envelope.SpatialSelection = selectedSpatialState(filters, key)
		if err := visualizationir.ValidateEnvelope(envelope); err != nil {
			return nil, err
		}
		visuals[key] = envelope
	}
	return visuals, nil
}

func fieldBindingsToDataFields(bindings []visualizationdefinition.FieldBinding) []dataquery.Field {
	fields := make([]dataquery.Field, len(bindings))
	for index, binding := range bindings {
		fields[index] = dataquery.Field{Field: binding.FieldID, Alias: binding.Alias}
	}
	return fields
}

func frameFromDatums(definition visualizationdefinition.Definition, data []dashboard.Datum) (visualizationruntime.Frame, error) {
	base, err := visualizationir.SpecificationBase(definition.Spec)
	if err != nil {
		return visualizationruntime.Frame{}, fmt.Errorf("visualization %q has invalid compiled dataset", definition.ID)
	}
	var schema *visualizationir.VisualizationDatasetSchema
	for index := range base.Datasets {
		if base.Datasets[index].ID == definition.Query.DatasetID {
			schema = &base.Datasets[index]
			break
		}
	}
	if schema == nil {
		return visualizationruntime.Frame{}, fmt.Errorf("visualization %q primary query targets unknown dataset %q", definition.ID, definition.Query.DatasetID)
	}
	columns := sourceFrameColumns(schema.Fields)
	rows := make([][]any, len(data))
	for index, datum := range data {
		rows[index] = make([]any, len(columns))
		for columnIndex, column := range columns {
			rows[index][columnIndex] = normalizeDatumValue(datum[column])
		}
	}
	return visualizationruntime.Frame{Columns: columns, Rows: rows}, nil
}

func (s *VisualizationDataService) contextFrames(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, visualID string, definition visualizationdefinition.Definition, filters dashboard.Filters) (map[string]visualizationruntime.Frame, error) {
	if len(definition.SecondaryQueries) == 0 {
		return nil, nil
	}
	queryFilters, err := s.filters.semanticFilters(ctx, runtime, report, filters, "visual", visualID)
	if err != nil {
		return nil, err
	}
	base, err := visualizationir.SpecificationBase(definition.Spec)
	if err != nil {
		return nil, err
	}
	schemas := make(map[string]visualizationir.VisualizationDatasetSchema, len(base.Datasets))
	for _, schema := range base.Datasets {
		schemas[schema.ID] = schema
	}
	datasetIDs := make([]string, 0, len(definition.SecondaryQueries))
	for datasetID := range definition.SecondaryQueries {
		datasetIDs = append(datasetIDs, datasetID)
	}
	sort.Strings(datasetIDs)
	frames := make(map[string]visualizationruntime.Frame, len(datasetIDs))
	for _, datasetID := range datasetIDs {
		binding := definition.SecondaryQueries[datasetID]
		query := binding.Aggregate
		if binding.Kind != visualizationdefinition.QueryAggregate || query == nil {
			return nil, fmt.Errorf("visualization %q context dataset %q has invalid aggregate binding", visualID, datasetID)
		}
		dimensions := make([]reportdef.QueryField, 0, len(query.Dimensions)+1)
		for _, field := range query.Dimensions {
			dimensions = append(dimensions, queryFieldRef(field, field.Alias))
		}
		if query.Series != nil {
			dimensions = append(dimensions, queryFieldRef(*query.Series, query.Series.Alias))
		}
		metrics := make([]reportdef.QueryField, len(query.Metrics))
		for index, field := range query.Metrics {
			metrics[index] = queryFieldRef(field, field.Alias)
		}
		queryTime := dashboardauthoring.QueryTime{}
		if query.Time != nil {
			queryTime = dashboardauthoring.QueryTime{Field: query.Time.FieldID, Alias: query.Time.Alias, Grain: query.Time.Grain}
		}
		sorts := make([]reportdef.QuerySort, len(query.Sort))
		for index, value := range query.Sort {
			sorts[index] = reportdef.QuerySort{Field: value.FieldID, Direction: value.Direction}
		}
		data, err := s.querySemanticDatums(ctx, runtime, reportdef.AggregateQuery{
			Dataset: query.TableID, Dimensions: dimensions, Metrics: metrics, Time: queryTime,
			Filters: queryFilters, Sort: sorts, Limit: int(query.Limit),
		})
		if err != nil {
			return nil, fmt.Errorf("visualization %q context dataset %q: %w", visualID, datasetID, err)
		}
		schema, ok := schemas[datasetID]
		if !ok {
			return nil, fmt.Errorf("visualization %q context dataset %q has no schema", visualID, datasetID)
		}
		columns := sourceFrameColumns(schema.Fields)
		rows := make([][]any, len(data))
		for rowIndex, datum := range data {
			rows[rowIndex] = make([]any, len(columns))
			for columnIndex, column := range columns {
				rows[rowIndex][columnIndex] = normalizeDatumValue(datum[column])
			}
		}
		frames[datasetID] = visualizationruntime.Frame{Columns: columns, Rows: rows, Completeness: visualizationFrameCompleteness(definition.Spec, datasetID, len(rows), int(query.Limit))}
	}
	return frames, nil
}

func sourceFrameColumns(fields []visualizationir.VisualizationField) []string {
	columns := make([]string, 0, len(fields))
	for _, field := range fields {
		if field.Provenance != nil && field.Provenance.Kind == visualizationir.VisualizationFieldProvenanceKindVisualCalculation {
			continue
		}
		columns = append(columns, field.ID)
	}
	return columns
}

func (s *VisualizationDataService) bundledVisuals(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, filters dashboard.Filters, keys []string) (map[string]visualizationir.VisualizationEnvelope, error) {
	port, ok := runtime.data.(dataquery.BundleExecutor)
	if !ok {
		return nil, &dataquery.BundleIncompatibleError{Err: fmt.Errorf("runtime has no governed bundle port")}
	}
	requests := make([]dataquery.BundleRequest, 0, len(keys))
	definitions := map[string]visualPlan{}
	for _, key := range keys {
		definition, ok := report.Visualizations[key]
		if !ok {
			return nil, fmt.Errorf("unknown visual %q", key)
		}
		visual, err := newVisualPlan(definition)
		if err != nil {
			return nil, err
		}
		request, err := s.bundleAggregateRequest(ctx, runtime, report, filters, key, visual)
		if err != nil {
			return nil, err
		}
		requests = append(requests, dataquery.BundleRequest{ID: key, Query: reportAggregateDataQuery(report.SemanticModel, request)})
		definitions[key] = visual
	}
	bundle, err := port.ExecuteDataQueryBundle(ctx, requests)
	if err != nil {
		return nil, err
	}
	visuals := make(map[string]visualizationir.VisualizationEnvelope, len(keys))
	for _, key := range keys {
		visual := definitions[key]
		data := datumsFromDataQuery(bundle.Results[key].Rows)
		switch visual.ResultShape() {
		case visualizationdefinition.ResultScalar:
			for _, row := range data {
				if _, ok := row["label"]; !ok {
					row["label"] = singleValueTitle(runtime, visual)
				}
				row["series"] = ""
			}
		case visualizationdefinition.ResultCategoryMultiMeasure:
			data = categoryMultiMeasureDatums(runtime, visual, data)
		default:
			if visual.Series != nil {
				for _, row := range data {
					if _, ok := row["series"]; !ok {
						row["series"] = ""
					}
				}
			}
		}
		definition := report.Visualizations[key]
		frame, frameErr := frameFromDatums(definition, data)
		if frameErr != nil {
			return nil, frameErr
		}
		frame.Completeness = visualizationFrameCompleteness(definition.Spec, definition.Query.DatasetID, len(data), visualizationQueryLimit(definition.Query))
		frames := map[string]visualizationruntime.Frame{definition.Query.DatasetID: frame}
		contextFrames, contextErr := s.contextFrames(ctx, runtime, report, key, definition, filters)
		if contextErr != nil {
			return nil, contextErr
		}
		for datasetID, contextFrame := range contextFrames {
			frames[datasetID] = contextFrame
		}
		envelope, envelopeErr := visualizationruntime.EnvelopeFromFrames(definition, frames, selectedEntries(filters, "visual", key), 0, 0)
		if envelopeErr != nil {
			return nil, envelopeErr
		}
		envelope.Highlights, envelopeErr = selectedHighlights(runtime, report, filters, key)
		if envelopeErr != nil {
			return nil, envelopeErr
		}
		envelope.SpatialSelection = selectedSpatialState(filters, key)
		if envelopeErr := visualizationir.ValidateEnvelope(envelope); envelopeErr != nil {
			return nil, envelopeErr
		}
		visuals[key] = envelope
	}
	return visuals, nil
}

func boundedFrameCompleteness(rows, limit int) visualizationir.VisualizationCompleteness {
	if rows == 0 {
		return visualizationir.VisualizationCompletenessEmpty
	}
	if limit > 0 && rows >= limit {
		return visualizationir.VisualizationCompletenessTruncated
	}
	return visualizationir.VisualizationCompletenessComplete
}

func visualizationFrameCompleteness(spec visualizationir.VisualizationSpec, datasetID string, rows, limit int) visualizationir.VisualizationCompleteness {
	base, err := visualizationir.SpecificationBase(spec)
	if err != nil || base.Calculations == nil {
		if rows == 0 {
			return visualizationir.VisualizationCompletenessEmpty
		}
		return visualizationir.VisualizationCompletenessComplete
	}
	for _, calculation := range *base.Calculations {
		if calculation.Dataset == datasetID {
			return boundedFrameCompleteness(rows, limit)
		}
	}
	if rows == 0 {
		return visualizationir.VisualizationCompletenessEmpty
	}
	return visualizationir.VisualizationCompletenessComplete
}

func visualizationQueryLimit(query visualizationdefinition.QueryBinding) int {
	switch query.Kind {
	case visualizationdefinition.QueryAggregate:
		return int(query.Aggregate.Limit)
	case visualizationdefinition.QueryDetail:
		return int(query.Detail.Limit)
	case visualizationdefinition.QueryMatrix:
		return int(query.Matrix.Limit)
	case visualizationdefinition.QueryPivot:
		return int(query.Pivot.Limit)
	case visualizationdefinition.QuerySpatial:
		return int(query.Spatial.Limit)
	default:
		return 0
	}
}

func (s *VisualizationDataService) bundleAggregateRequest(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, filters dashboard.Filters, visualID string, visual visualPlan) (reportdef.AggregateQuery, error) {
	queryFilters, err := s.filters.semanticFilters(ctx, runtime, report, filters, "visual", visualID)
	if err != nil {
		return reportdef.AggregateQuery{}, err
	}
	switch visual.ResultShape() {
	case visualizationdefinition.ResultScalar:
		dimensions := []reportdef.QueryField{}
		if len(visual.Dimensions) == 1 {
			dimensions = append(dimensions, fieldRef(visual.Dimensions[0].FieldID, "label"))
		}
		sorts := visualSorts(visual)
		if len(dimensions) == 0 {
			sorts = nil
		}
		return reportdef.AggregateQuery{Dataset: visual.Table, Dimensions: dimensions, Metrics: []reportdef.QueryField{queryFieldRef(visual.Metrics[0], "value")}, Filters: queryFilters, Sort: sorts, Limit: visual.Limit}, nil
	case visualizationdefinition.ResultCategoryValue, visualizationdefinition.ResultCategorySeriesValue:
		dimensions, queryTime := categoryDimension(visual, "label")
		if visual.Series != nil {
			dimensions = append(dimensions, fieldRef(visual.Series.FieldID, "series"))
		}
		sorts := visualSorts(visual)
		if len(visual.Sort) == 0 {
			sorts = []reportdef.QuerySort{{Field: "label", Direction: "asc"}}
		}
		return reportdef.AggregateQuery{Dataset: visual.Table, Dimensions: dimensions, Metrics: []reportdef.QueryField{queryFieldRef(visual.Metrics[0], "value")}, Time: queryTime, Filters: queryFilters, Sort: sorts, Limit: visual.Limit}, nil
	case visualizationdefinition.ResultCategoryMultiMeasure:
		dimensions, queryTime := categoryDimension(visual, "label")
		metrics := make([]reportdef.QueryField, 0, len(visual.Metrics))
		for index, metric := range visual.Metrics {
			metrics = append(metrics, queryFieldRef(metric, fmt.Sprintf("value_%d", index)))
		}
		return reportdef.AggregateQuery{Dataset: visual.Table, Dimensions: dimensions, Metrics: metrics, Time: queryTime, Filters: queryFilters, Sort: visualSorts(visual), Limit: visual.Limit}, nil
	default:
		return reportdef.AggregateQuery{}, &dataquery.BundleIncompatibleError{Err: fmt.Errorf("visual %q result shape %q is not bundleable", visualID, visual.ResultShape())}
	}
}

func categoryMultiMeasureDatums(runtime *modelRuntime, visual visualPlan, rows []dashboard.Datum) []dashboard.Datum {
	data := make([]dashboard.Datum, 0, len(rows)*len(visual.Metrics))
	for _, row := range rows {
		for index, metricRef := range visual.Metrics {
			metric := aggregateMemberMetadata(runtime.model, metricRef.FieldID)
			data = append(data, dashboard.Datum{
				"label":  row["label"],
				"series": metricLabel(metricRef.FieldID, metric),
				"value":  row[fmt.Sprintf("value_%d", index)],
			})
		}
	}
	return data
}

func datumsFromDataQuery(rows []dataquery.Row) []dashboard.Datum {
	out := make([]dashboard.Datum, 0, len(rows))
	for _, row := range rows {
		datum := dashboard.Datum{}
		for key, value := range row {
			datum[key] = normalizeDatumValue(value)
		}
		out = append(out, datum)
	}
	return out
}

type singleValueBatchItem struct {
	visualID string
	visual   visualPlan
	filters  []reportdef.QueryFilter
}

func (s *VisualizationDataService) batchedSingleValueData(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, filters dashboard.Filters, keys []string) (map[string][]dashboard.Datum, error) {
	groups := map[string][]singleValueBatchItem{}
	order := []string{}
	for _, visualID := range keys {
		definition, ok := report.Visualizations[visualID]
		if !ok {
			continue
		}
		visual, err := newVisualPlan(definition)
		if err != nil {
			return nil, err
		}
		if visual.ResultShape() != visualizationdefinition.ResultScalar || len(visual.Dimensions) != 0 || visual.Time != nil || len(visual.Metrics) != 1 {
			continue
		}
		queryFilters, err := s.filters.semanticFilters(ctx, runtime, report, filters, "visual", visualID)
		if err != nil {
			return nil, err
		}
		scope, err := json.Marshal(struct {
			Table   string                  `json:"table"`
			Filters []reportdef.QueryFilter `json:"filters"`
			Limit   int                     `json:"limit"`
		}{Table: visual.Table, Filters: queryFilters, Limit: visual.Limit})
		if err != nil {
			return nil, fmt.Errorf("encode visual %q query scope: %w", visualID, err)
		}
		key := string(scope)
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], singleValueBatchItem{visualID: visualID, visual: visual, filters: queryFilters})
	}
	result := map[string][]dashboard.Datum{}
	for _, key := range order {
		items := groups[key]
		if len(items) < 2 {
			continue
		}
		metricAliases := map[string]string{}
		metrics := make([]reportdef.QueryField, 0, len(items))
		for _, item := range items {
			metric := item.visual.Metrics[0]
			if _, exists := metricAliases[metric.FieldID]; exists {
				continue
			}
			alias := fmt.Sprintf("value_%d", len(metrics))
			metricAliases[metric.FieldID] = alias
			metrics = append(metrics, queryFieldRef(metric, alias))
		}
		rows, err := s.querySemanticDatums(ctx, runtime, reportdef.AggregateQuery{
			Dataset: items[0].visual.Table,
			Metrics: metrics,
			Filters: items[0].filters,
			Limit:   items[0].visual.Limit,
		})
		if err != nil {
			return nil, err
		}
		var row dashboard.Datum
		if len(rows) > 0 {
			row = rows[0]
		}
		for _, item := range items {
			value := any(nil)
			if row != nil {
				value = row[metricAliases[item.visual.Metrics[0].FieldID]]
			}
			result[item.visualID] = []dashboard.Datum{{
				"label":  singleValueTitle(runtime, item.visual),
				"series": "",
				"value":  value,
			}}
		}
	}
	return result, nil
}

func singleValueTitle(runtime *modelRuntime, visual visualPlan) string {
	metricRef := visual.Metrics[0]
	metricName := metricRef.FieldID
	title := visual.Title()
	if title == "" {
		if metric, err := runtime.model.ResolveMetric(metricName); err == nil {
			title = metric.Label
		} else if metric, ok := runtime.model.Metrics[metricName]; ok {
			title = metric.Label
		}
	}
	if title == "" {
		title = defaultString(metricName, metricRef.Alias)
	}
	return title
}

func (s *VisualizationDataService) visualData(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, visualID string, visual visualPlan, filters dashboard.Filters) ([]dashboard.Datum, error) {
	switch visual.ResultShape() {
	case visualizationdefinition.ResultScalar:
		return s.singleValueData(ctx, runtime, report, visualID, visual, filters)
	case visualizationdefinition.ResultCategoryMultiMeasure:
		return s.categoryMultiMeasureData(ctx, runtime, report, visualID, visual, filters)
	case visualizationdefinition.ResultCategoryDelta:
		return s.categoryDeltaData(ctx, runtime, report, visualID, visual, filters)
	case visualizationdefinition.ResultHistogramBins:
		return s.binnedMeasureData(ctx, runtime, report, visualID, visual, filters)
	case visualizationdefinition.ResultHierarchyNodes:
		return s.hierarchyData(ctx, runtime, report, visualID, visual, filters)
	case visualizationdefinition.ResultMatrixCells:
		return s.matrixData(ctx, runtime, report, visualID, visual, filters)
	case visualizationdefinition.ResultGraphEdges:
		return s.graphData(ctx, runtime, report, visualID, visual, filters)
	case visualizationdefinition.ResultGeographicFeatures:
		return s.geoData(ctx, runtime, report, visualID, visual, filters)
	case visualizationdefinition.ResultOHLC:
		return s.ohlcData(ctx, runtime, report, visualID, visual, filters)
	case visualizationdefinition.ResultDistribution:
		return s.distributionData(ctx, runtime, report, visualID, visual, filters)
	case visualizationdefinition.ResultPoints:
		return s.pointData(ctx, runtime, report, visualID, visual, filters)
	default:
		return s.categoryData(ctx, runtime, report, visualID, visual, filters)
	}
}

func (s *VisualizationDataService) pointData(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, visualID string, visual visualPlan, filters dashboard.Filters) ([]dashboard.Datum, error) {
	queryFilters, err := s.filters.semanticFilters(ctx, runtime, report, filters, "visual", visualID)
	if err != nil {
		return nil, err
	}
	dimensions := make([]reportdef.QueryField, 0, len(visual.Dimensions))
	for _, dimension := range visual.Dimensions {
		dimensions = append(dimensions, fieldRef(dimension.FieldID, dimension.Alias))
	}
	metrics := make([]reportdef.QueryField, 0, len(visual.Metrics))
	for _, metric := range visual.Metrics {
		metrics = append(metrics, queryFieldRef(metric, metric.Alias))
	}
	var queryTime dashboardauthoring.QueryTime
	if visual.Time != nil {
		queryTime = dashboardauthoring.QueryTime{Field: visual.Time.FieldID, Grain: visual.Time.Grain, Alias: visual.Time.Alias}
	}
	sorts := make([]reportdef.QuerySort, 0, len(visual.Sort)+1)
	for _, sort := range visual.Sort {
		field := sort.FieldID
		for _, binding := range append(append([]visualizationdefinition.FieldBinding{}, visual.Dimensions...), visual.Metrics...) {
			if field == binding.FieldID || field == binding.Alias {
				field = binding.Alias
				break
			}
		}
		if visual.Time != nil && (field == visual.Time.FieldID || field == visual.Time.Alias) {
			field = visual.Time.Alias
		}
		sorts = append(sorts, reportdef.QuerySort{Field: field, Direction: sort.Direction})
	}
	if len(sorts) == 0 {
		point, ok := visual.Definition.Spec.Value.(*visualizationir.PointVisualizationSpec)
		if ok && len(point.Identity) > 0 {
			for _, identity := range point.Identity {
				sorts = append(sorts, reportdef.QuerySort{Field: identity.Field, Direction: "asc"})
			}
		}
	}
	rows, err := s.querySemanticDatums(ctx, runtime, reportdef.AggregateQuery{
		Dataset: visual.Table, Dimensions: dimensions, Metrics: metrics, Time: queryTime,
		Filters: queryFilters, Sort: sorts, Limit: visual.Limit,
	})
	if err != nil {
		return nil, err
	}
	point, ok := visual.Definition.Spec.Value.(*visualizationir.PointVisualizationSpec)
	if !ok {
		return nil, fmt.Errorf("visual %q point result has specification %T", visualID, visual.Definition.Spec.Value)
	}
	filtered := rows[:0]
	for _, row := range rows {
		// The public point contract defines null coordinates as omitted marks;
		// stable identity nulls and duplicates remain hard validation errors.
		if row[point.X.Field] == nil || row[point.Y.Field] == nil {
			continue
		}
		filtered = append(filtered, row)
	}
	return filtered, nil
}

func (s *VisualizationDataService) categoryData(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, visualID string, visual visualPlan, filters dashboard.Filters) ([]dashboard.Datum, error) {
	queryFilters, err := s.filters.semanticFilters(ctx, runtime, report, filters, "visual", visualID)
	if err != nil {
		return nil, err
	}
	dimensionAlias := "label"
	metricAlias := "value"
	dimensions, queryTime := categoryDimension(visual, dimensionAlias)
	columns := []string{dimensionAlias, metricAlias}
	if visual.Series != nil {
		dimensions = append(dimensions, fieldRef(visual.Series.FieldID, "series"))
		columns = []string{dimensionAlias, "series", metricAlias}
	}
	sorts := visualSorts(visual)
	if len(visual.Sort) == 0 {
		sorts = []reportdef.QuerySort{{Field: dimensionAlias, Direction: "asc"}}
	}
	data, err := s.querySemanticDatums(ctx, runtime, reportdef.AggregateQuery{
		Dataset:    visual.Table,
		Dimensions: dimensions,
		Metrics:    []reportdef.QueryField{queryFieldRef(visual.Metrics[0], metricAlias)},
		Time:       queryTime,
		Filters:    queryFilters,
		Sort:       sorts,
		Limit:      visual.Limit,
	})
	if err != nil {
		return nil, err
	}
	for _, row := range data {
		for _, column := range columns {
			if _, ok := row[column]; !ok && column == "series" {
				row[column] = ""
			}
		}
	}
	return data, nil
}

func defaultString(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func fieldAlias(field string) string {
	parts := strings.Split(field, ".")
	return parts[len(parts)-1]
}

func (s *VisualizationDataService) categoryMultiMeasureData(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, visualID string, visual visualPlan, filters dashboard.Filters) ([]dashboard.Datum, error) {
	queryFilters, err := s.filters.semanticFilters(ctx, runtime, report, filters, "visual", visualID)
	if err != nil {
		return nil, err
	}
	dimensions, queryTime := categoryDimension(visual, "label")
	metrics := make([]reportdef.QueryField, 0, len(visual.Metrics))
	for index, metric := range visual.Metrics {
		metrics = append(metrics, queryFieldRef(metric, fmt.Sprintf("value_%d", index)))
	}
	rows, err := s.querySemanticDatums(ctx, runtime, reportdef.AggregateQuery{
		Dataset:    visual.Table,
		Dimensions: dimensions,
		Metrics:    metrics,
		Time:       queryTime,
		Filters:    queryFilters,
		Sort:       visualSorts(visual),
		Limit:      visual.Limit,
	})
	if err != nil {
		return nil, err
	}
	return categoryMultiMeasureDatums(runtime, visual, rows), nil
}

func categoryDimension(visual visualPlan, alias string) ([]reportdef.QueryField, dashboardauthoring.QueryTime) {
	if visual.Time != nil {
		return nil, dashboardauthoring.QueryTime{Field: visual.Time.FieldID, Grain: visual.Time.Grain, Alias: alias}
	}
	return []reportdef.QueryField{fieldRef(visual.Dimensions[0].FieldID, alias)}, dashboardauthoring.QueryTime{}
}

func (s *VisualizationDataService) categoryDeltaData(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, visualID string, visual visualPlan, filters dashboard.Filters) ([]dashboard.Datum, error) {
	rows, err := s.categoryData(ctx, runtime, report, visualID, visual, filters)
	if err != nil {
		return nil, err
	}
	cumulative := 0.0
	for _, row := range rows {
		value := datumFloat(row["value"])
		start := cumulative
		cumulative += value
		row["start"] = round(start)
		row["end"] = round(cumulative)
		row["positive"] = value >= 0
	}
	return rows, nil
}

func (s *VisualizationDataService) binnedMeasureData(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, visualID string, visual visualPlan, filters dashboard.Filters) ([]dashboard.Datum, error) {
	queryFilters, err := s.filters.semanticFilters(ctx, runtime, report, filters, "visual", visualID)
	if err != nil {
		return nil, err
	}
	bins, err := runtime.data.Histogram(ctx, reportdef.RawValueQuery{
		Dataset: visual.Table,
		Metric:  queryFieldRef(visual.Metrics[0], "value"),
		Filters: queryFilters,
	}, visual.HistogramBins())
	if err != nil {
		return nil, err
	}
	data := make([]dashboard.Datum, 0, len(bins))
	for _, bin := range bins {
		data = append(data, dashboard.Datum{
			"label":    formatBinLabel(bin.Start, bin.End),
			"binStart": round(bin.Start),
			"binEnd":   round(bin.End),
			"value":    bin.Count,
		})
	}
	return data, nil
}

func (s *VisualizationDataService) hierarchyData(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, visualID string, visual visualPlan, filters dashboard.Filters) ([]dashboard.Datum, error) {
	queryFilters, err := s.filters.semanticFilters(ctx, runtime, report, filters, "visual", visualID)
	if err != nil {
		return nil, err
	}
	dimensions := make([]reportdef.QueryField, 0, len(visual.Dimensions))
	levelAliases := make([]string, 0, len(visual.Dimensions))
	for _, dimensionName := range visual.Dimensions {
		alias := dimensionName.Alias
		dimensions = append(dimensions, fieldRef(dimensionName.FieldID, alias))
		levelAliases = append(levelAliases, alias)
	}
	queryTime := dashboardauthoring.QueryTime{}
	if visual.Time != nil {
		alias := visual.Time.Alias
		queryTime = dashboardauthoring.QueryTime{Field: visual.Time.FieldID, Grain: visual.Time.Grain, Alias: alias}
		levelAliases = append(levelAliases, alias)
	}
	rows, err := runtime.data.Query(ctx, reportdef.AggregateQuery{
		Dataset:    visual.Table,
		Dimensions: dimensions,
		Time:       queryTime,
		Metrics:    []reportdef.QueryField{queryFieldRef(visual.Metrics[0], "value")},
		Filters:    queryFilters,
		Sort:       visualSorts(visual),
		Limit:      visual.Limit,
	})
	if err != nil {
		return nil, err
	}
	return flattenHierarchyRows(rows, levelAliases)
}

type hierarchyFrameNode struct {
	name   string
	parent any
	value  float64
	levels []any
}

// flattenHierarchyRows materializes the hierarchy declared by the compiled
// node/parent/value frame. Parent values are stable, escaped path identities,
// which permits the same display label under different parents without making
// renderer-specific row identities part of the public contract.
func flattenHierarchyRows(rows reportdef.QueryRows, levelAliases []string) ([]dashboard.Datum, error) {
	if len(levelAliases) == 0 {
		return nil, fmt.Errorf("hierarchy requires at least one level")
	}
	nodes := make(map[string]*hierarchyFrameNode)
	for rowIndex, row := range rows {
		value, ok := hierarchyNumericValue(normalizeDatumValue(row["value"]))
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("hierarchy row %d has a nonnumeric value", rowIndex)
		}
		segments := make([]string, 0, len(levelAliases))
		levelValues := make([]any, 0, len(levelAliases))
		for _, alias := range levelAliases {
			raw := normalizeDatumValue(row[alias])
			if raw == nil || strings.TrimSpace(fmt.Sprint(raw)) == "" {
				return nil, fmt.Errorf("hierarchy row %d has an empty level %q", rowIndex, alias)
			}
			segments = append(segments, fmt.Sprint(raw))
			levelValues = append(levelValues, raw)
		}
		for level, name := range segments {
			id := hierarchyPathID(segments[:level+1])
			var parent any
			if level > 0 {
				parent = hierarchyPathID(segments[:level])
			}
			node, exists := nodes[id]
			if !exists {
				node = &hierarchyFrameNode{name: name, parent: parent, levels: append([]any(nil), levelValues[:level+1]...)}
				nodes[id] = node
			}
			node.value += value
		}
	}
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]dashboard.Datum, 0, len(ids))
	for _, id := range ids {
		node := nodes[id]
		row := dashboard.Datum{"node": node.name, "parent": node.parent, "value": round(node.value)}
		for index, alias := range levelAliases {
			row[alias] = nil
			if index < len(node.levels) {
				row[alias] = node.levels[index]
			}
		}
		result = append(result, row)
	}
	return result, nil
}

func hierarchyPathID(segments []string) string {
	id := ""
	for _, segment := range segments {
		id = visualizationir.HierarchyNodeIdentity(id, segment)
	}
	return id
}

func hierarchyNumericValue(value any) (float64, bool) {
	switch value := value.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int8:
		return float64(value), true
	case int16:
		return float64(value), true
	case int32:
		return float64(value), true
	case int64:
		return float64(value), true
	case uint:
		return float64(value), true
	case uint8:
		return float64(value), true
	case uint16:
		return float64(value), true
	case uint32:
		return float64(value), true
	case uint64:
		return float64(value), true
	default:
		return 0, false
	}
}

func (s *VisualizationDataService) singleValueData(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, visualID string, visual visualPlan, filters dashboard.Filters) ([]dashboard.Datum, error) {
	metricRef := visual.Metrics[0]
	title := singleValueTitle(runtime, visual)
	queryFilters, err := s.filters.semanticFilters(ctx, runtime, report, filters, "visual", visualID)
	if err != nil {
		return nil, err
	}
	dimensions := []reportdef.QueryField{}
	if len(visual.Dimensions) == 1 {
		dimensions = append(dimensions, fieldRef(visual.Dimensions[0].FieldID, "label"))
	}
	sorts := visualSorts(visual)
	if len(dimensions) == 0 {
		sorts = nil
	}
	data, err := s.querySemanticDatums(ctx, runtime, reportdef.AggregateQuery{
		Dataset:    visual.Table,
		Dimensions: dimensions,
		Metrics:    []reportdef.QueryField{queryFieldRef(metricRef, "value")},
		Filters:    queryFilters,
		Sort:       sorts,
		Limit:      visual.Limit,
	})
	if err != nil {
		return nil, err
	}
	for _, row := range data {
		if _, ok := row["label"]; !ok {
			row["label"] = title
		}
		row["series"] = ""
	}
	return data, nil
}

func (s *VisualizationDataService) matrixData(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, visualID string, visual visualPlan, filters dashboard.Filters) ([]dashboard.Datum, error) {
	return s.dimensionPairData(ctx, runtime, report, visualID, visual, filters, "row", "column")
}

func (s *VisualizationDataService) graphData(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, visualID string, visual visualPlan, filters dashboard.Filters) ([]dashboard.Datum, error) {
	return s.dimensionPairData(ctx, runtime, report, visualID, visual, filters, "source", "target")
}

func (s *VisualizationDataService) dimensionPairData(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, visualID string, visual visualPlan, filters dashboard.Filters, leftAlias, rightAlias string) ([]dashboard.Datum, error) {
	rightSQLAlias := rightAlias
	if rightAlias == "column" {
		rightSQLAlias = "chart_column"
	}
	queryFilters, err := s.filters.semanticFilters(ctx, runtime, report, filters, "visual", visualID)
	if err != nil {
		return nil, err
	}
	data, err := s.querySemanticDatums(ctx, runtime, reportdef.AggregateQuery{
		Dataset: visual.Table,
		Dimensions: []reportdef.QueryField{
			fieldRef(visual.Dimensions[0].FieldID, leftAlias),
			fieldRef(visual.Dimensions[1].FieldID, rightSQLAlias),
		},
		Metrics: []reportdef.QueryField{queryFieldRef(visual.Metrics[0], "value")},
		Filters: queryFilters,
		Sort:    visualSorts(visual),
		Limit:   visual.Limit,
	})
	if err != nil {
		return nil, err
	}
	if rightAlias == "column" {
		for _, row := range data {
			row["column"] = row[rightSQLAlias]
			delete(row, rightSQLAlias)
		}
	}
	return data, nil
}

func (s *VisualizationDataService) geoData(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, visualID string, visual visualPlan, filters dashboard.Filters) ([]dashboard.Datum, error) {
	queryFilters, err := s.filters.semanticFilters(ctx, runtime, report, filters, "visual", visualID)
	if err != nil {
		return nil, err
	}
	data, err := s.querySemanticDatums(ctx, runtime, reportdef.AggregateQuery{
		Dataset: visual.Table, Dimensions: aliasedQueryFields(visual.Dimensions), Metrics: aliasedQueryFields(visual.Metrics),
		Filters: queryFilters, Sort: aliasedVisualSorts(visual), Limit: visual.Limit,
	})
	if err != nil {
		return nil, err
	}
	return data, nil
}

func aliasedQueryFields(bindings []visualizationdefinition.FieldBinding) []reportdef.QueryField {
	fields := make([]reportdef.QueryField, len(bindings))
	for index, binding := range bindings {
		fields[index] = queryFieldRef(binding, binding.Alias)
	}
	return fields
}

func aliasedVisualSorts(visual visualPlan) []reportdef.QuerySort {
	if len(visual.Sort) == 0 {
		if len(visual.Dimensions) > 0 {
			return []reportdef.QuerySort{{Field: visual.Dimensions[0].Alias, Direction: "asc"}}
		}
		return nil
	}
	bindings := append(append([]visualizationdefinition.FieldBinding{}, visual.Dimensions...), visual.Metrics...)
	sorts := make([]reportdef.QuerySort, len(visual.Sort))
	for index, sort := range visual.Sort {
		field := sort.FieldID
		for _, binding := range bindings {
			if field == binding.FieldID || field == binding.Alias || field == displayField(binding.FieldID) {
				field = binding.Alias
				break
			}
		}
		sorts[index] = reportdef.QuerySort{Field: field, Direction: sort.Direction}
	}
	return sorts
}

func (s *VisualizationDataService) ohlcData(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, visualID string, visual visualPlan, filters dashboard.Filters) ([]dashboard.Datum, error) {
	queryFilters, err := s.filters.semanticFilters(ctx, runtime, report, filters, "visual", visualID)
	if err != nil {
		return nil, err
	}
	return s.querySemanticDatums(ctx, runtime, reportdef.AggregateQuery{
		Dataset:    visual.Table,
		Dimensions: []reportdef.QueryField{fieldRef(visual.Dimensions[0].FieldID, "label")},
		Metrics: []reportdef.QueryField{
			queryFieldRef(visual.Metrics[0], "open"),
			queryFieldRef(visual.Metrics[1], "close"),
			queryFieldRef(visual.Metrics[2], "low"),
			queryFieldRef(visual.Metrics[3], "high"),
		},
		Filters: queryFilters,
		Sort:    visualSorts(visual),
		Limit:   visual.Limit,
	})
}

func (s *VisualizationDataService) distributionData(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, visualID string, visual visualPlan, filters dashboard.Filters) ([]dashboard.Datum, error) {
	queryFilters, err := s.filters.semanticFilters(ctx, runtime, report, filters, "visual", visualID)
	if err != nil {
		return nil, err
	}
	return s.queryDistributionDatums(ctx, runtime, reportdef.RawValueQuery{
		Dataset:    visual.Table,
		Dimensions: []reportdef.QueryField{fieldRef(visual.Dimensions[0].FieldID, "label")},
		Metric:     queryFieldRef(visual.Metrics[0], "value"),
		Filters:    queryFilters,
	}, distributionSorts(visual), visual.Limit)
}

func visualQueryDimensions(visual visualPlan) []string {
	dimensions := queryDimensionFields(visual.Dimensions)
	if visual.Series != nil {
		dimensions = append(dimensions, visual.Series.FieldID)
	}
	return dimensions
}

func (s *VisualizationDataService) querySemanticDatums(ctx context.Context, runtime *modelRuntime, request reportdef.AggregateQuery) ([]dashboard.Datum, error) {
	rows, err := runtime.data.Query(ctx, request)
	if err != nil {
		return nil, err
	}
	return datumsFromAnalytics(rows), nil
}

func (s *VisualizationDataService) queryDistributionDatums(ctx context.Context, runtime *modelRuntime, request reportdef.RawValueQuery, sort []reportdef.QuerySort, limit int) ([]dashboard.Datum, error) {
	rows, err := runtime.data.Distribution(ctx, request, sort, limit)
	if err != nil {
		return nil, err
	}
	return datumsFromAnalytics(rows), nil
}

func datumsFromAnalytics(rows reportdef.QueryRows) []dashboard.Datum {
	data := make([]dashboard.Datum, 0, len(rows))
	for _, row := range rows {
		datum := dashboard.Datum{}
		for column, value := range row {
			datum[column] = normalizeDatumValue(value)
		}
		data = append(data, datum)
	}
	return data
}
