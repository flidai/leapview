package compiler

// This is the canonical generated-dashboard compilation boundary.  A
// DashboardDocument is lowered directly into query bindings, Visual IR, and
// immutable dashboard definition state; no dashboard/authoring value is used
// as an intermediate representation.

import (
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

// DocumentResult is the immutable result of compiling one generated
// DashboardDocument.  Normalized retains the source DTO for export and
// revision evidence; Definition is the serving-ready IR.
type DocumentResult struct {
	Normalized document.DashboardDocument
	Definition definition.Definition
}

// CompileDocument compiles a generated dashboard document without projecting
// through the legacy dashboard authoring package.
func CompileDocument(doc document.DashboardDocument, models map[string]*semanticmodel.Model) (DocumentResult, error) {
	if doc.Kind != document.DashboardResourceKindDashboard {
		return DocumentResult{}, fmt.Errorf("dashboard document kind %q is invalid", doc.Kind)
	}
	id := strings.TrimSpace(doc.Metadata.ID)
	if id == "" {
		return DocumentResult{}, fmt.Errorf("dashboard metadata.id is required")
	}
	modelID := strings.TrimSpace(doc.Spec.SemanticModel)
	model := models[modelID]
	if model == nil {
		for key, candidate := range models {
			if candidate != nil && (key == modelID || candidate.Name == modelID) {
				modelID, model = key, candidate
				break
			}
		}
	}
	if model == nil {
		return DocumentResult{}, fmt.Errorf("dashboard semantic model %q is unavailable", modelID)
	}
	filters, err := CompileCanonicalDashboardFilters(doc, model)
	if err != nil {
		return DocumentResult{}, fmt.Errorf("compile dashboard filters: %w", err)
	}
	layout, err := CompileDashboardLayout(doc.Spec)
	if err != nil {
		return DocumentResult{}, fmt.Errorf("compile dashboard layout: %w", err)
	}
	visuals := make(map[string]visualizationdefinition.Definition, len(doc.Spec.Visuals))
	visualIDs := make([]string, 0, len(doc.Spec.Visuals))
	for visualID := range doc.Spec.Visuals {
		visualIDs = append(visualIDs, visualID)
	}
	sort.Strings(visualIDs)
	for _, visualID := range visualIDs {
		visual := doc.Spec.Visuals[visualID]
		query, err := LowerDashboardQuery(visual.Query, model, modelID)
		if err != nil {
			return DocumentResult{}, fmt.Errorf("visual %q query: %w", visualID, err)
		}
		presentation, err := LowerCanonicalDashboardPresentationForQuery(visual.Presentation, visual.Type, query)
		if err != nil {
			return DocumentResult{}, fmt.Errorf("visual %q presentation: %w", visualID, err)
		}
		if err := validateCanonicalVisualResultReferences(visual, query); err != nil {
			return DocumentResult{}, fmt.Errorf("visual %q references: %w", visualID, err)
		}
		if err := validateCanonicalInteractionKinds(visual); err != nil {
			return DocumentResult{}, fmt.Errorf("visual %q interactions: %w", visualID, err)
		}
		if visual.Type == document.DashboardVisualTypeMap {
			query.Binding = canonicalSpatialBinding(query.Binding)
		}
		adjustCanonicalResultShape(&query.Binding, visual.Type)
		secondary := make(map[string]visualizationdefinition.QueryBinding)
		secondarySchemas := make([]visualizationir.VisualizationDatasetSchema, 0)
		if visual.Datasets != nil {
			datasetIDs := make([]string, 0, len(*visual.Datasets))
			for datasetID := range *visual.Datasets {
				datasetIDs = append(datasetIDs, datasetID)
			}
			sort.Strings(datasetIDs)
			for _, datasetID := range datasetIDs {
				datasetQuery := (*visual.Datasets)[datasetID]
				secondaryQuery, lowerErr := LowerDashboardQuery(datasetQuery, model, modelID)
				if lowerErr != nil {
					return DocumentResult{}, fmt.Errorf("visual %q dataset %q query: %w", visualID, datasetID, lowerErr)
				}
				// LowerDashboardQuery produces a primary binding by default. A
				// context dataset is a distinct result frame and must carry its
				// authored dataset ID through validation and runtime execution.
				secondaryQuery.Binding.DatasetID = datasetID
				secondary[datasetID] = secondaryQuery.Binding
				secondarySchemas = append(secondarySchemas, visualizationir.VisualizationDatasetSchema{ID: datasetID, Fields: canonicalResultFields(secondaryQuery, model)})
			}
		}
		spec, err := canonicalVisualizationSpec(visualID, visual, query, presentation, secondarySchemas, model)
		if err != nil {
			return DocumentResult{}, fmt.Errorf("visual %q: %w", visualID, err)
		}
		if err := visualizationir.ValidateSpec(spec); err != nil {
			return DocumentResult{}, fmt.Errorf("visual %q IR: %w", visualID, err)
		}
		compiled, err := visualizationdefinition.NewWithSecondaryQueries(visualID, spec, query.Binding, secondary)
		if err != nil {
			return DocumentResult{}, fmt.Errorf("visual %q definition: %w", visualID, err)
		}
		visuals[visualID] = compiled
	}
	if err := completeVisualizationInteractionGraph(visuals); err != nil {
		return DocumentResult{}, fmt.Errorf("complete dashboard interaction graph: %w", err)
	}
	compiled, err := definition.New(id, valueOrString(doc.Metadata.DisplayName, doc.Metadata.Name), valueOrString(doc.Metadata.Description, ""), modelID, layout.Pages, visuals)
	if err != nil {
		return DocumentResult{}, err
	}
	compiled, err = AttachDashboardLayout(compiled, doc.Spec)
	if err != nil {
		return DocumentResult{}, err
	}
	if err := filters.ApplyToDefinition(&compiled); err != nil {
		return DocumentResult{}, err
	}
	return DocumentResult{Normalized: doc, Definition: compiled}, nil
}

func adjustCanonicalResultShape(binding *visualizationdefinition.QueryBinding, visualType document.DashboardVisualType) {
	if binding == nil {
		return
	}
	switch visualType {
	case document.DashboardVisualTypeHeatmap:
		binding.ResultShape = visualizationdefinition.ResultMatrixCells
	case document.DashboardVisualTypeHistogram:
		binding.ResultShape = visualizationdefinition.ResultHistogramBins
	case document.DashboardVisualTypeBoxplot:
		binding.ResultShape = visualizationdefinition.ResultDistribution
	case document.DashboardVisualTypeScatter:
		binding.ResultShape = visualizationdefinition.ResultPoints
	case document.DashboardVisualTypeGraph, document.DashboardVisualTypeSankey:
		binding.ResultShape = visualizationdefinition.ResultGraphEdges
	case document.DashboardVisualTypeTreemap, document.DashboardVisualTypeTree, document.DashboardVisualTypeSunburst:
		binding.ResultShape = visualizationdefinition.ResultHierarchyNodes
	case document.DashboardVisualTypeWaterfall:
		binding.ResultShape = visualizationdefinition.ResultCategoryDelta
	case document.DashboardVisualTypeCandlestick:
		binding.ResultShape = visualizationdefinition.ResultOHLC
	case document.DashboardVisualTypeCombo:
		binding.ResultShape = visualizationdefinition.ResultCategoryMultiMeasure
	}
}

func validateCanonicalVisualResultReferences(visual document.DashboardVisual, query LoweredDashboardQuery) error {
	refs := make([]string, 0)
	if visual.Calculations != nil {
		for _, calculation := range *visual.Calculations {
			refs = append(refs, calculation.Source)
			if calculation.Parent != nil {
				refs = append(refs, *calculation.Parent)
			}
			if calculation.PartitionBy != nil {
				refs = append(refs, (*calculation.PartitionBy)...)
			}
			if calculation.OrderBy != nil {
				for _, order := range *calculation.OrderBy {
					refs = append(refs, order.Field)
				}
			}
			if calculation.Lookup != nil {
				refs = append(refs, calculation.Lookup.Field)
			}
		}
	}
	if visual.Interactions != nil {
		for _, interaction := range *visual.Interactions {
			switch value := interaction.Value.(type) {
			case *document.SelectionDashboardInteraction:
				for _, mapping := range value.Mappings {
					refs = append(refs, mapping.Field)
					if mapping.Label != nil {
						refs = append(refs, *mapping.Label)
					}
				}
			case *document.SpatialSelectionDashboardInteraction:
				refs = append(refs, value.Latitude.Source, value.Longitude.Source)
			}
		}
	}
	for _, ref := range refs {
		if strings.TrimSpace(ref) == "" {
			continue
		}
		if err := query.ValidateResultReference(ref); err != nil {
			return err
		}
	}
	return nil
}

func validateCanonicalInteractionKinds(visual document.DashboardVisual) error {
	if visual.Interactions == nil {
		return nil
	}
	for index, interaction := range *visual.Interactions {
		kind, err := interaction.Type()
		if err != nil {
			return fmt.Errorf("interaction %d: %w", index, err)
		}
		if kind == "spatialSelection" && visual.Type != document.DashboardVisualTypeMap {
			return fmt.Errorf("interaction %d spatialSelection is only supported on map visuals", index)
		}
	}
	return nil
}

func valueOrString(value *string, fallback string) string {
	if value != nil && strings.TrimSpace(*value) != "" {
		return *value
	}
	return fallback
}

func canonicalVisualizationSpec(id string, visual document.DashboardVisual, query LoweredDashboardQuery, presentation any, secondarySchemas []visualizationir.VisualizationDatasetSchema, model *semanticmodel.Model) (visualizationir.VisualizationSpec, error) {
	title := valueOrString(visual.Title, id)
	description := valueOrString(visual.Description, title)
	datasets := append([]visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: canonicalResultFields(query, model)}}, secondarySchemas...)
	base := visualizationir.VisualizationSpecBase{Title: title, Datasets: datasets, DataBudget: visualizationir.VisualizationDataBudget{MaxRows: 1000, RequiredCompleteness: visualizationir.VisualizationCompletenessComplete}, Accessibility: visualizationir.VisualizationAccessibility{Title: title, Description: description}, Interactions: []visualizationir.VisualizationInteraction{}}
	if visual.Subtitle != nil {
		base.Subtitle = visual.Subtitle
	}
	if visual.Accessibility != nil {
		if visual.Accessibility.Title != nil {
			base.Accessibility.Title = *visual.Accessibility.Title
		}
		if visual.Accessibility.Description != nil {
			base.Accessibility.Description = *visual.Accessibility.Description
		}
		base.Accessibility.Summary = visual.Accessibility.Summary
		base.Accessibility.AnnounceChanges = visual.Accessibility.AnnounceChanges
	}
	if visual.DataBudget != nil {
		if visual.DataBudget.MaxRows <= 0 {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("dataBudget.maxRows must be positive")
		}
		base.DataBudget.MaxRows = int64(visual.DataBudget.MaxRows)
		base.DataBudget.RequiredCompleteness = visualizationir.VisualizationCompletenessComplete
		if visual.DataBudget.RequiredCompleteness != nil {
			base.DataBudget.RequiredCompleteness = *visual.DataBudget.RequiredCompleteness
		}
	}
	var conversionErr error
	base.MetadataBindings, conversionErr = canonicalMetadataBindings(visual.Metadata, query, secondarySchemas)
	if conversionErr != nil {
		return visualizationir.VisualizationSpec{}, fmt.Errorf("metadata bindings: %w", conversionErr)
	}
	base.Calculations, conversionErr = canonicalCalculations(visual.Calculations, query)
	if conversionErr != nil {
		return visualizationir.VisualizationSpec{}, fmt.Errorf("calculations: %w", conversionErr)
	}
	base.Interactions, conversionErr = canonicalInteractions(visual.Interactions, query)
	if conversionErr != nil {
		return visualizationir.VisualizationSpec{}, fmt.Errorf("interactions: %w", conversionErr)
	}
	ref := func(index int) visualizationir.VisualizationFieldRef {
		if index < 0 || index >= len(query.ResultFrame) {
			return visualizationir.VisualizationFieldRef{}
		}
		return visualizationir.VisualizationFieldRef{Dataset: "primary", Field: query.ResultFrame[index].Name}
	}
	dimRef := func(index int) visualizationir.VisualizationFieldRef {
		return canonicalBindingRef(query, false, index)
	}
	metricRef := func(index int) visualizationir.VisualizationFieldRef {
		return canonicalBindingRef(query, true, index)
	}
	dimensions, metrics := canonicalQueryOperandRefs(query)
	if visual.Type == document.DashboardVisualTypeScatter {
		identityNames := make(map[string]struct{}, len(dimensions))
		for _, dimension := range dimensions {
			identityNames[dimension.Field] = struct{}{}
		}
		for datasetIndex := range base.Datasets {
			if base.Datasets[datasetIndex].ID != "primary" {
				continue
			}
			for fieldIndex := range base.Datasets[datasetIndex].Fields {
				if _, ok := identityNames[base.Datasets[datasetIndex].Fields[fieldIndex].ID]; ok {
					base.Datasets[datasetIndex].Fields[fieldIndex].Role = visualizationir.VisualizationFieldRoleIdentity
				}
			}
		}
	}
	requireOperands := func(kind string, dimensionsN, metricsN int, allowMoreDimensions, allowMoreMetrics bool) error {
		if len(dimensions) < dimensionsN || len(metrics) < metricsN {
			return fmt.Errorf("%s requires at least %d dimension(s) and %d metric(s), got %d and %d", kind, dimensionsN, metricsN, len(dimensions), len(metrics))
		}
		if !allowMoreDimensions && len(dimensions) > dimensionsN {
			return fmt.Errorf("%s does not support %d dimension operands", kind, len(dimensions))
		}
		if !allowMoreMetrics && len(metrics) > metricsN {
			return fmt.Errorf("%s does not support %d metric operands", kind, len(metrics))
		}
		return nil
	}
	typ := visual.Type
	switch typ {
	case document.DashboardVisualTypeTable:
		p, ok := presentation.(visualizationir.GridVisualizationPresentation)
		if !ok {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("table presentation lowering returned %T", presentation)
		}
		columns := make([]visualizationir.TableVisualizationColumn, 0, len(query.ResultFrame))
		for i := range query.ResultFrame {
			columns = append(columns, visualizationir.TableVisualizationColumn{Field: ref(i), Label: query.ResultFrame[i].Name})
		}
		base.Kind = "table"
		return visualizationir.VisualizationSpec{Value: &visualizationir.TableVisualizationSpec{VisualizationSpecBase: base, Kind: "table", Columns: columns, Presentation: p}}, nil
	case document.DashboardVisualTypeMatrix:
		p, ok := presentation.(visualizationir.GridVisualizationPresentation)
		if !ok {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("matrix presentation lowering returned %T", presentation)
		}
		var rows, columns, metricRefs []visualizationir.VisualizationFieldRef
		if query.Binding.Matrix != nil {
			rows = bindingRefs(query.Binding.Matrix.Rows)
			columns = bindingRefs(query.Binding.Matrix.Columns)
			metricRefs = bindingRefs(query.Binding.Matrix.Metrics)
		} else if query.Binding.Pivot != nil {
			rows = bindingRefs(query.Binding.Pivot.Rows)
			columns = bindingRefs(query.Binding.Pivot.Columns)
			metricRefs = bindingRefs(query.Binding.Pivot.Metrics)
		} else {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("matrix visual requires a matrix or pivot query binding")
		}
		if len(rows) == 0 || len(metricRefs) == 0 {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("matrix visual requires non-empty rows and metrics")
		}
		base.Kind = "matrix"
		return visualizationir.VisualizationSpec{Value: &visualizationir.MatrixVisualizationSpec{VisualizationSpecBase: base, Kind: "matrix", Rows: rows, Columns: columns, Metrics: metricRefs, MetricFormatting: map[string][]visualizationir.TableVisualizationFormattingRule{}, Presentation: p}}, nil
	case document.DashboardVisualTypePivot:
		p, ok := presentation.(visualizationir.GridVisualizationPresentation)
		if !ok {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("pivot presentation lowering returned %T", presentation)
		}
		if query.Binding.Pivot == nil {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("pivot visual requires a pivot query binding")
		}
		rows := bindingRefs(query.Binding.Pivot.Rows)
		columns := bindingRefs(query.Binding.Pivot.Columns)
		metricRefs := bindingRefs(query.Binding.Pivot.Metrics)
		if len(rows) == 0 || len(columns) == 0 || len(metricRefs) == 0 {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("pivot visual requires non-empty rows, columns, and metrics")
		}
		base.Kind = "pivot"
		return visualizationir.VisualizationSpec{Value: &visualizationir.PivotVisualizationSpec{VisualizationSpecBase: base, Kind: "pivot", Rows: rows, Columns: columns, Metrics: metricRefs, MetricFormatting: map[string][]visualizationir.TableVisualizationFormattingRule{}, Presentation: p}}, nil
	case document.DashboardVisualTypeKpi:
		p, ok := presentation.(visualizationir.KPIVisualizationPresentation)
		if !ok {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("KPI presentation lowering returned %T", presentation)
		}
		if err := requireOperands("KPI", 0, 1, false, false); err != nil {
			return visualizationir.VisualizationSpec{}, err
		}
		base.Kind = "kpi"
		return visualizationir.VisualizationSpec{Value: &visualizationir.KPIVisualizationSpec{VisualizationSpecBase: base, Kind: "kpi", Value: metricRef(0), Presentation: p}}, nil
	case document.DashboardVisualTypePie, document.DashboardVisualTypeDonut, document.DashboardVisualTypeFunnel:
		p, ok := presentation.(visualizationir.ProportionalVisualizationPresentation)
		if !ok {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("proportional presentation lowering returned %T", presentation)
		}
		if err := requireOperands("proportional", 1, 1, false, false); err != nil {
			return visualizationir.VisualizationSpec{}, err
		}
		base.Kind = "proportional"
		return visualizationir.VisualizationSpec{Value: &visualizationir.ProportionalVisualizationSpec{VisualizationSpecBase: base, Kind: "proportional", Mark: visualizationir.VisualizationProportionalMark(typ), Category: dimRef(0), Value: metricRef(0), Presentation: p}}, nil
	case document.DashboardVisualTypeTreemap, document.DashboardVisualTypeSankey, document.DashboardVisualTypeGraph, document.DashboardVisualTypeTree, document.DashboardVisualTypeSunburst:
		p, ok := presentation.(visualizationir.HierarchyVisualizationPresentation)
		if !ok {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("hierarchy presentation lowering returned %T", presentation)
		}
		if len(dimensions) < 1 || len(dimensions) > 2 || len(metrics) != 1 {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("hierarchy requires one or two dimensions and exactly one metric, got %d and %d", len(dimensions), len(metrics))
		}
		base.Kind = "hierarchy"
		var parent *visualizationir.VisualizationFieldRef
		if len(dimensions) > 1 {
			parentRef := dimensions[1]
			parent = &parentRef
		}
		value := metricRef(0)
		return visualizationir.VisualizationSpec{Value: &visualizationir.HierarchyVisualizationSpec{VisualizationSpecBase: base, Kind: "hierarchy", Mark: visualizationir.VisualizationHierarchyMark(typ), Node: dimensions[0], Parent: parent, Value: &value, Presentation: p}}, nil
	case document.DashboardVisualTypeGauge, document.DashboardVisualTypeRadar:
		p, ok := presentation.(visualizationir.PolarVisualizationPresentation)
		if !ok {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("polar presentation lowering returned %T", presentation)
		}
		if len(dimensions) > 1 || len(metrics) != 1 {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("polar requires zero or one dimension and exactly one metric, got %d and %d", len(dimensions), len(metrics))
		}
		base.Kind = "polar"
		var category *visualizationir.VisualizationFieldRef
		if len(dimensions) > 0 {
			categoryRef := dimensions[0]
			category = &categoryRef
		}
		return visualizationir.VisualizationSpec{Value: &visualizationir.PolarVisualizationSpec{VisualizationSpecBase: base, Kind: "polar", Mark: visualizationir.VisualizationPolarMark(typ), Category: category, Value: metricRef(0), Presentation: p}}, nil
	case document.DashboardVisualTypeScatter:
		p, ok := presentation.(visualizationir.CartesianVisualizationPresentation)
		if !ok {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("scatter presentation lowering returned %T", presentation)
		}
		pointPresentation, err := canonicalPointPresentation(p)
		if err != nil {
			return visualizationir.VisualizationSpec{}, err
		}
		if len(dimensions) == 0 || len(metrics) != 2 {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("scatter requires at least one identity dimension and exactly two metric operands, got %d and %d", len(dimensions), len(metrics))
		}
		base.Kind = "point"
		return visualizationir.VisualizationSpec{Value: &visualizationir.PointVisualizationSpec{VisualizationSpecBase: base, Kind: "point", Identity: dimensions, X: metrics[0], Y: metrics[1], Presentation: pointPresentation}}, nil
	case document.DashboardVisualTypeMap:
		p, ok := presentation.(visualizationir.GeographicVisualizationPresentation)
		if !ok {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("geographic presentation lowering returned %T", presentation)
		}
		if len(dimensions) != 2 {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("map visual requires exactly two dimensions for latitude and longitude, got %d", len(dimensions))
		}
		if len(metrics) > 1 {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("map visual supports at most one metric, got %d", len(metrics))
		}
		base.Kind = "geographic"
		spatialInteractions, spatialErr := canonicalSpatialInteractions(visual.Interactions, query)
		if spatialErr != nil {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("spatial interactions: %w", spatialErr)
		}
		lat, lon := dimensions[0], dimensions[1]
		layerBase := visualizationir.VisualizationGeographicLayerBase{ID: "points", Kind: "point", Position: visualizationir.VisualizationMapLayerPositionBelowLabels, Visibility: visualizationir.VisualizationMapVisibility{MinimumZoom: 0, MaximumZoom: 24}}
		layer := visualizationir.VisualizationPointLayer{VisualizationGeographicLayerBase: layerBase, Kind: "point", Latitude: lat, Longitude: lon, Size: visualizationir.VisualizationMapSizeScale{MinimumRadius: 2, MaximumRadius: 12}, Color: visualizationir.VisualizationMapColorScale{Kind: visualizationir.VisualizationMapColorScaleKindSequential, Palette: "default"}, Stroke: visualizationir.VisualizationMapStroke{Color: "#ffffff", Width: 1, Opacity: .8}, Cluster: visualizationir.VisualizationMapCluster{Enabled: true, Radius: 40, MaximumZoom: 14, MinimumPoints: 2}, Opacity: .8}
		return visualizationir.VisualizationSpec{Value: &visualizationir.GeographicVisualizationSpec{VisualizationSpecBase: base, Kind: "geographic", Layers: []visualizationir.VisualizationGeographicLayer{{Value: &layer}}, SpatialInteractions: spatialInteractions, Presentation: p}}, nil
	case document.DashboardVisualTypeHistogram, document.DashboardVisualTypeBoxplot:
		p, ok := presentation.(visualizationir.CartesianVisualizationPresentation)
		if !ok {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("statistical presentation lowering returned %T", presentation)
		}
		if len(query.ResultFrame) < 2 {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("%s query must emit at least two result fields", typ)
		}
		statMark := visualizationir.VisualizationCartesianMarkHistogram
		if typ == document.DashboardVisualTypeBoxplot {
			statMark = visualizationir.VisualizationCartesianMarkBoxplot
		}
		base.Kind = "cartesian"
		return visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{VisualizationSpecBase: base, Kind: "cartesian", Mark: statMark, X: ref(0), Y: []visualizationir.VisualizationFieldRef{ref(1)}, Presentation: p}}, nil
	default:
		p, ok := presentation.(visualizationir.CartesianVisualizationPresentation)
		if !ok {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("cartesian presentation lowering returned %T", presentation)
		}
		mark := visualizationir.VisualizationCartesianMark(typ)
		if typ == document.DashboardVisualTypeHistogram {
			mark = visualizationir.VisualizationCartesianMarkHistogram
		}
		if len(dimensions) > 1 || len(metrics) < 1 {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("cartesian requires zero or one dimension and at least one metric, got %d and %d", len(dimensions), len(metrics))
		}
		base.Kind = "cartesian"
		var x visualizationir.VisualizationFieldRef
		if len(dimensions) > 0 {
			x = dimensions[0]
		} else {
			x = metrics[0]
		}
		return visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{VisualizationSpecBase: base, Kind: "cartesian", Mark: mark, X: x, Y: metrics, Presentation: p}}, nil
	}
}

func canonicalSpatialBinding(binding visualizationdefinition.QueryBinding) visualizationdefinition.QueryBinding {
	if binding.Aggregate == nil {
		return binding
	}
	spatial := &visualizationdefinition.SpatialQueryBinding{TableID: binding.Aggregate.TableID, Dimensions: append([]visualizationdefinition.FieldBinding(nil), binding.Aggregate.Dimensions...), Metrics: append([]visualizationdefinition.FieldBinding(nil), binding.Aggregate.Metrics...), Limit: binding.Aggregate.Limit, Sort: append([]visualizationdefinition.Sort(nil), binding.Aggregate.Sort...)}
	return visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QuerySpatial, ResultShape: visualizationdefinition.ResultGeographicFeatures, ModelID: binding.ModelID, DatasetID: binding.DatasetID, Identity: append([]string(nil), binding.Identity...), Spatial: spatial}
}

func canonicalPointPresentation(value visualizationir.CartesianVisualizationPresentation) (visualizationir.PointVisualizationPresentation, error) {
	if value.Smooth || value.Stacked || value.DataZoom || value.Area || value.Step || value.Orientation != nil || value.LabelPosition != nil || value.SymbolSize != nil || value.HistogramBins != nil || value.ComboSeries != nil || value.Stacking != nil || value.SeriesIntent != nil || value.ShowSymbols {
		return visualizationir.PointVisualizationPresentation{}, fmt.Errorf("scatter presentation contains cartesian fields without point equivalents")
	}
	return visualizationir.PointVisualizationPresentation{
		VisualizationPresentation: value.VisualizationPresentation,
		Opacity:                   .7,
		LargeThreshold:            10000,
		Brush:                     []visualizationir.VisualizationPointBrushGesture{},
	}, nil
}

func canonicalBindingRef(query LoweredDashboardQuery, metric bool, index int) visualizationir.VisualizationFieldRef {
	if query.Binding.Aggregate != nil {
		fields := query.Binding.Aggregate.Dimensions
		if metric {
			fields = query.Binding.Aggregate.Metrics
			if len(fields) == 0 && query.Binding.Aggregate.Histogram != nil {
				fields = []visualizationdefinition.FieldBinding{query.Binding.Aggregate.Histogram.Metric}
			}
			if len(fields) == 0 && query.Binding.Aggregate.Distribution != nil {
				fields = []visualizationdefinition.FieldBinding{query.Binding.Aggregate.Distribution.Metric}
			}
		}
		if index >= 0 && index < len(fields) {
			return visualizationir.VisualizationFieldRef{Dataset: "primary", Field: fields[index].Alias}
		}
	}
	if query.Binding.Detail != nil && !metric && index >= 0 && index < len(query.Binding.Detail.Fields) {
		return visualizationir.VisualizationFieldRef{Dataset: "primary", Field: query.Binding.Detail.Fields[index].Alias}
	}
	if query.Binding.Pivot != nil {
		fields := query.Binding.Pivot.Rows
		if metric {
			fields = query.Binding.Pivot.Metrics
		}
		if index >= 0 && index < len(fields) {
			return visualizationir.VisualizationFieldRef{Dataset: "primary", Field: fields[index].Alias}
		}
	}
	if query.Binding.Matrix != nil {
		fields := query.Binding.Matrix.Rows
		if metric {
			fields = query.Binding.Matrix.Metrics
		}
		if index >= 0 && index < len(fields) {
			return visualizationir.VisualizationFieldRef{Dataset: "primary", Field: fields[index].Alias}
		}
	}
	if query.Binding.Spatial != nil {
		fields := query.Binding.Spatial.Dimensions
		if metric {
			fields = query.Binding.Spatial.Metrics
		}
		if index >= 0 && index < len(fields) {
			return visualizationir.VisualizationFieldRef{Dataset: "primary", Field: fields[index].Alias}
		}
	}
	return visualizationir.VisualizationFieldRef{}
}

func bindingRefs(values []visualizationdefinition.FieldBinding) []visualizationir.VisualizationFieldRef {
	refs := make([]visualizationir.VisualizationFieldRef, 0, len(values))
	for _, value := range values {
		refs = append(refs, visualizationir.VisualizationFieldRef{Dataset: "primary", Field: value.Alias})
	}
	return refs
}

func canonicalQueryOperandRefs(query LoweredDashboardQuery) (dimensions, metrics []visualizationir.VisualizationFieldRef) {
	switch {
	case query.Binding.Aggregate != nil:
		dimensions = bindingRefs(query.Binding.Aggregate.Dimensions)
		metrics = bindingRefs(query.Binding.Aggregate.Metrics)
		if len(metrics) == 0 && query.Binding.Aggregate.Histogram != nil {
			metrics = bindingRefs([]visualizationdefinition.FieldBinding{query.Binding.Aggregate.Histogram.Metric})
		}
		if len(metrics) == 0 && query.Binding.Aggregate.Distribution != nil {
			metrics = bindingRefs([]visualizationdefinition.FieldBinding{query.Binding.Aggregate.Distribution.Metric})
		}
	case query.Binding.Detail != nil:
		dimensions = bindingRefs(query.Binding.Detail.Fields)
	case query.Binding.Matrix != nil:
		dimensions = append(bindingRefs(query.Binding.Matrix.Rows), bindingRefs(query.Binding.Matrix.Columns)...)
		metrics = bindingRefs(query.Binding.Matrix.Metrics)
	case query.Binding.Pivot != nil:
		dimensions = append(bindingRefs(query.Binding.Pivot.Rows), bindingRefs(query.Binding.Pivot.Columns)...)
		metrics = bindingRefs(query.Binding.Pivot.Metrics)
	case query.Binding.Spatial != nil:
		dimensions = bindingRefs(query.Binding.Spatial.Dimensions)
		metrics = bindingRefs(query.Binding.Spatial.Metrics)
	}
	return dimensions, metrics
}

func canonicalResultFields(query LoweredDashboardQuery, model *semanticmodel.Model) []visualizationir.VisualizationField {
	fields := make([]visualizationir.VisualizationField, 0, len(query.ResultFrame))
	metricNames := map[string]struct{}{}
	if query.Binding.Aggregate != nil {
		for _, field := range query.Binding.Aggregate.Metrics {
			metricNames[field.Alias] = struct{}{}
		}
		if query.Binding.Aggregate.Histogram != nil {
			metricNames[query.Binding.Aggregate.Histogram.Metric.Alias] = struct{}{}
		}
		if query.Binding.Aggregate.Distribution != nil {
			metricNames[query.Binding.Aggregate.Distribution.Metric.Alias] = struct{}{}
		}
	}
	if query.Binding.Matrix != nil {
		for _, field := range query.Binding.Matrix.Metrics {
			metricNames[field.Alias] = struct{}{}
		}
	}
	if query.Binding.Pivot != nil {
		for _, field := range query.Binding.Pivot.Metrics {
			metricNames[field.Alias] = struct{}{}
		}
	}
	if query.Binding.Spatial != nil {
		for _, field := range query.Binding.Spatial.Metrics {
			metricNames[field.Alias] = struct{}{}
		}
	}
	for i, field := range query.ResultFrame {
		role, typ := visualizationir.VisualizationFieldRoleDimension, visualizationir.VisualizationDataTypeString
		if _, ok := metricNames[field.Name]; ok || field.Name == "count" || field.Name == "value" {
			role, typ = visualizationir.VisualizationFieldRoleMetric, visualizationir.VisualizationDataTypeDecimal
		}
		if query.Type == "records" {
			role, typ = visualizationir.VisualizationFieldRoleDimension, canonicalPhysicalDataType(model, field.Source)
		} else if _, ok := metricNames[field.Name]; !ok {
			typ = canonicalSemanticDataType(model, field.Source, false)
		} else {
			typ = canonicalSemanticDataType(model, field.Source, true)
		}
		if i == 0 && query.Type == "distribution" {
			role, typ = visualizationir.VisualizationFieldRoleMetric, visualizationir.VisualizationDataTypeDecimal
		}
		var sourceRef *string
		if strings.TrimSpace(field.Source) != "" {
			source := field.Source
			sourceRef = &source
		}
		fields = append(fields, visualizationir.VisualizationField{ID: field.Name, SourceRef: sourceRef, Role: role, DataType: typ, Nullable: true, Label: field.Name})
	}
	return fields
}

func canonicalPhysicalDataType(model *semanticmodel.Model, source string) visualizationir.VisualizationDataType {
	if model == nil {
		return visualizationir.VisualizationDataTypeString
	}
	if dimension, err := model.ResolveDimension(source); err == nil {
		return visualizationDataType(dimension.Datatype)
	}
	return visualizationir.VisualizationDataTypeString
}

func canonicalSemanticDataType(model *semanticmodel.Model, source string, metric bool) visualizationir.VisualizationDataType {
	if model == nil {
		return visualizationir.VisualizationDataTypeString
	}
	if metric {
		if datatype, err := model.MetricDataType(source); err == nil {
			return visualizationDataType(datatype)
		}
		return visualizationir.VisualizationDataTypeDecimal
	}
	if dimension, err := model.ResolveSemanticDimension(source); err == nil {
		return visualizationDataType(dimension.Datatype)
	}
	return visualizationir.VisualizationDataTypeString
}

func visualizationDataType(value semanticmodel.LogicalDataType) visualizationir.VisualizationDataType {
	switch value {
	case semanticmodel.DataTypeInteger:
		return visualizationir.VisualizationDataTypeInteger
	case semanticmodel.DataTypeDecimal:
		return visualizationir.VisualizationDataTypeDecimal
	case semanticmodel.DataTypeFloat:
		return visualizationir.VisualizationDataTypeFloat
	case semanticmodel.DataTypeBoolean:
		return visualizationir.VisualizationDataTypeBoolean
	case semanticmodel.DataTypeDate:
		return visualizationir.VisualizationDataTypeDate
	case semanticmodel.DataTypeTime, semanticmodel.DataTypeDateTime, semanticmodel.DataTypeDateTimeTZ:
		return visualizationir.VisualizationDataTypeTemporal
	default:
		return visualizationir.VisualizationDataTypeString
	}
}

func canonicalResultRef(query LoweredDashboardQuery, dataset, name string) (visualizationir.VisualizationFieldRef, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return visualizationir.VisualizationFieldRef{}, fmt.Errorf("compiled result field is required")
	}
	if dataset == "" {
		dataset = "primary"
	}
	if dataset == "primary" {
		for _, field := range query.ResultFrame {
			if field.Name == name {
				return visualizationir.VisualizationFieldRef{Dataset: dataset, Field: name}, nil
			}
		}
		return visualizationir.VisualizationFieldRef{}, fmt.Errorf("reference %q is not a compiled result field", name)
	}
	return visualizationir.VisualizationFieldRef{}, fmt.Errorf("reference %q cannot resolve secondary dataset %q without its compiled result schema", name, dataset)
}

func canonicalDatasetResultRef(query LoweredDashboardQuery, secondary []visualizationir.VisualizationDatasetSchema, dataset, name string) (visualizationir.VisualizationFieldRef, error) {
	dataset = strings.TrimSpace(dataset)
	if dataset == "" || dataset == "primary" {
		return canonicalResultRef(query, "primary", name)
	}
	for _, schema := range secondary {
		if schema.ID != dataset {
			continue
		}
		for _, field := range schema.Fields {
			if field.ID == name {
				return visualizationir.VisualizationFieldRef{Dataset: dataset, Field: name}, nil
			}
		}
		return visualizationir.VisualizationFieldRef{}, fmt.Errorf("reference %q is not a compiled result field in dataset %q", name, dataset)
	}
	return visualizationir.VisualizationFieldRef{}, fmt.Errorf("reference %q targets unknown dataset %q", name, dataset)
}

func canonicalMetadataBindings(value *document.DashboardMetadataBindings, query LoweredDashboardQuery, secondary []visualizationir.VisualizationDatasetSchema) (*visualizationir.VisualizationMetadataBindings, error) {
	if value == nil {
		return nil, nil
	}
	convert := func(binding *document.DashboardTextBinding) (*visualizationir.VisualizationTextBinding, error) {
		if binding == nil {
			return nil, nil
		}
		field, err := canonicalDatasetResultRef(query, secondary, binding.Dataset, binding.Field)
		if err != nil {
			return nil, err
		}
		reducer := visualizationir.VisualizationReferenceReducerFirst
		if binding.Reducer != nil {
			reducer = *binding.Reducer
		}
		return &visualizationir.VisualizationTextBinding{Field: field, Reducer: reducer, Prefix: valueOrString(binding.Prefix, ""), Suffix: valueOrString(binding.Suffix, ""), Fallback: valueOrString(binding.Fallback, "")}, nil
	}
	title, err := convert(value.Title)
	if err != nil {
		return nil, err
	}
	subtitle, err := convert(value.Subtitle)
	if err != nil {
		return nil, err
	}
	description, err := convert(value.Description)
	if err != nil {
		return nil, err
	}
	summary, err := convert(value.Summary)
	if err != nil {
		return nil, err
	}
	return &visualizationir.VisualizationMetadataBindings{Title: title, Subtitle: subtitle, Description: description, Summary: summary}, nil
}

func canonicalCalculations(values *[]document.DashboardCalculation, query LoweredDashboardQuery) (*[]visualizationir.VisualizationCalculation, error) {
	if values == nil {
		return nil, nil
	}
	result := make([]visualizationir.VisualizationCalculation, len(*values))
	for index, value := range *values {
		source, err := canonicalResultRef(query, "primary", value.Source)
		if err != nil {
			return nil, fmt.Errorf("calculation %d source: %w", index, err)
		}
		calculation := visualizationir.VisualizationCalculation{ID: value.ID, Dataset: "primary", Source: source, Hidden: valueOrBool(value.Hidden), Template: value.Template}
		calculation.Label = valueOrString(value.Label, value.ID)
		calculation.Axis = visualizationir.VisualizationCalculationAxisRows
		if value.Axis != nil {
			calculation.Axis = *value.Axis
		}
		calculation.Reset = visualizationir.VisualizationCalculationResetNone
		if value.Reset != nil {
			calculation.Reset = *value.Reset
		}
		if value.Window != nil {
			window := int64(*value.Window)
			calculation.Window = &window
		}
		if value.Offset != nil {
			offset := int64(*value.Offset)
			calculation.Offset = &offset
		}
		if value.Parent != nil {
			parent, parentErr := canonicalResultRef(query, "primary", *value.Parent)
			if parentErr != nil {
				return nil, fmt.Errorf("calculation %d parent: %w", index, parentErr)
			}
			calculation.Parent = &parent
		}
		if value.PartitionBy != nil {
			calculation.PartitionBy = make([]visualizationir.VisualizationFieldRef, len(*value.PartitionBy))
			for partIndex, field := range *value.PartitionBy {
				ref, refErr := canonicalResultRef(query, "primary", field)
				if refErr != nil {
					return nil, fmt.Errorf("calculation %d partition %d: %w", index, partIndex, refErr)
				}
				calculation.PartitionBy[partIndex] = ref
			}
		}
		if value.OrderBy != nil {
			calculation.OrderBy = make([]visualizationir.VisualizationCalculationOrder, len(*value.OrderBy))
			for orderIndex, order := range *value.OrderBy {
				ref, refErr := canonicalResultRef(query, "primary", order.Field)
				if refErr != nil {
					return nil, fmt.Errorf("calculation %d order %d: %w", index, orderIndex, refErr)
				}
				calculation.OrderBy[orderIndex] = visualizationir.VisualizationCalculationOrder{Field: ref, Direction: visualizationir.VisualizationSortDirection(order.Direction)}
			}
		}
		if value.Lookup != nil {
			ref, refErr := canonicalResultRef(query, "primary", value.Lookup.Field)
			if refErr != nil {
				return nil, fmt.Errorf("calculation %d lookup: %w", index, refErr)
			}
			calculation.Lookup = &visualizationir.VisualizationCalculationLookup{Field: ref, Value: value.Lookup.Value}
		}
		result[index] = calculation
	}
	return &result, nil
}

func valueOrBool(value *bool) bool {
	return value != nil && *value
}

func canonicalSpatialInteractions(values *[]document.DashboardInteraction, query LoweredDashboardQuery) ([]visualizationir.VisualizationSpatialSelectionInteraction, error) {
	if values == nil {
		return []visualizationir.VisualizationSpatialSelectionInteraction{}, nil
	}
	result := make([]visualizationir.VisualizationSpatialSelectionInteraction, 0)
	for index, interaction := range *values {
		kind, err := interaction.Type()
		if err != nil {
			return nil, fmt.Errorf("interaction %d: %w", index, err)
		}
		if kind != "spatialSelection" {
			continue
		}
		value, ok := interaction.Value.(*document.SpatialSelectionDashboardInteraction)
		if !ok || value == nil {
			return nil, fmt.Errorf("interaction %d spatial selection variant is required", index)
		}
		lat, err := canonicalResultRef(query, "primary", value.Latitude.Source)
		if err != nil {
			return nil, fmt.Errorf("interaction %d latitude source: %w", index, err)
		}
		lon, err := canonicalResultRef(query, "primary", value.Longitude.Source)
		if err != nil {
			return nil, fmt.Errorf("interaction %d longitude source: %w", index, err)
		}
		if strings.TrimSpace(value.Latitude.Field) == "" || strings.TrimSpace(value.Longitude.Field) == "" {
			return nil, fmt.Errorf("interaction %d spatial target fields are required", index)
		}
		gestures := make([]visualizationir.VisualizationSpatialSelectionGesture, len(value.Gestures))
		for gestureIndex, gesture := range value.Gestures {
			gestures[gestureIndex] = visualizationir.VisualizationSpatialSelectionGesture(gesture)
		}
		targets := make([]visualizationir.VisualizationInteractionTarget, 0)
		appendTargets := func(ids *[]string, effect visualizationir.VisualizationInteractionEffect) {
			if ids == nil {
				return
			}
			for _, id := range *ids {
				targets = append(targets, visualizationir.VisualizationInteractionTarget{VisualID: id, Effect: effect})
			}
		}
		appendTargets(value.Targets, visualizationir.VisualizationInteractionEffectFilter)
		appendTargets(value.HighlightTargets, visualizationir.VisualizationInteractionEffectHighlight)
		appendTargets(value.NoneTargets, visualizationir.VisualizationInteractionEffectNone)
		result = append(result, visualizationir.VisualizationSpatialSelectionInteraction{
			ID: fmt.Sprintf("spatial-interaction-%d", index), Gestures: gestures,
			Latitude:  visualizationir.VisualizationSpatialFieldMapping{Source: lat, TargetFieldID: value.Latitude.Field, TargetDatasetID: value.Latitude.Dataset},
			Longitude: visualizationir.VisualizationSpatialFieldMapping{Source: lon, TargetFieldID: value.Longitude.Field, TargetDatasetID: value.Longitude.Dataset},
			Targets:   targets,
		})
	}
	return result, nil
}

func canonicalInteractions(values *[]document.DashboardInteraction, query LoweredDashboardQuery) ([]visualizationir.VisualizationInteraction, error) {
	if values == nil {
		return []visualizationir.VisualizationInteraction{}, nil
	}
	result := make([]visualizationir.VisualizationInteraction, 0, len(*values))
	for index := range *values {
		interaction := (*values)[index]
		kind, err := interaction.Type()
		if err != nil {
			return nil, fmt.Errorf("interaction %d: %w", index, err)
		}
		if kind == "spatialSelection" {
			continue
		}
		base, err := interaction.Base()
		if err != nil {
			return nil, fmt.Errorf("interaction %d: %w", index, err)
		}
		compiled := visualizationir.VisualizationInteraction{ID: fmt.Sprintf("interaction-%d", index), Kind: visualizationir.VisualizationInteractionKindSelect, Mappings: []visualizationir.VisualizationInteractionMapping{}, Targets: []visualizationir.VisualizationInteractionTarget{}, Mode: visualizationir.VisualizationSelectionModeSingle}
		if kind == "selection" {
			value := interaction.Value.(*document.SelectionDashboardInteraction)
			compiled.Mode = visualizationir.VisualizationSelectionMode(value.Mode)
			compiled.RequiresStableIdentity = value.Toggle
			for mappingIndex, mapping := range value.Mappings {
				// `field` is the emitted source/result field. `value` is the
				// semantic/physical target identity and `dataset` qualifies that
				// target scope; never resolve the source through the target dataset.
				source, refErr := canonicalResultRef(query, "primary", mapping.Field)
				if refErr != nil {
					return nil, fmt.Errorf("interaction %d mapping %d: %w", index, mappingIndex, refErr)
				}
				if strings.TrimSpace(mapping.Value) == "" {
					return nil, fmt.Errorf("interaction %d mapping %d target value is required", index, mappingIndex)
				}
				label := (*visualizationir.VisualizationFieldRef)(nil)
				if mapping.Label != nil {
					labelRef, labelErr := canonicalResultRef(query, "primary", *mapping.Label)
					if labelErr != nil {
						return nil, fmt.Errorf("interaction %d label %d: %w", index, mappingIndex, labelErr)
					}
					label = &labelRef
				}
				targetDataset := (*string)(nil)
				if mapping.Dataset != nil {
					targetDataset = mapping.Dataset
				}
				grain := (*string)(nil)
				if mapping.Grain != nil {
					value := string(*mapping.Grain)
					grain = &value
				}
				compiled.Mappings = append(compiled.Mappings, visualizationir.VisualizationInteractionMapping{Source: source, TargetFieldID: mapping.Value, TargetDatasetID: targetDataset, Grain: grain, Label: label})
			}
			targets := []string(nil)
			if value.Targets != nil {
				targets = append(targets, (*value.Targets)...)
			}
			for _, target := range targets {
				compiled.Targets = append(compiled.Targets, visualizationir.VisualizationInteractionTarget{VisualID: target, Effect: visualizationir.VisualizationInteractionEffectFilter})
			}
			if value.HighlightTargets != nil {
				for _, target := range *value.HighlightTargets {
					compiled.Targets = append(compiled.Targets, visualizationir.VisualizationInteractionTarget{VisualID: target, Effect: visualizationir.VisualizationInteractionEffectHighlight})
				}
			}
			if value.NoneTargets != nil {
				for _, target := range *value.NoneTargets {
					compiled.Targets = append(compiled.Targets, visualizationir.VisualizationInteractionTarget{VisualID: target, Effect: visualizationir.VisualizationInteractionEffectNone})
				}
			}
		} else {
			value := interaction.Value.(*document.SpatialSelectionDashboardInteraction)
			lat, latErr := canonicalResultRef(query, "primary", value.Latitude.Source)
			if latErr != nil {
				return nil, fmt.Errorf("interaction %d latitude source: %w", index, latErr)
			}
			lon, lonErr := canonicalResultRef(query, "primary", value.Longitude.Source)
			if lonErr != nil {
				return nil, fmt.Errorf("interaction %d longitude source: %w", index, lonErr)
			}
			if strings.TrimSpace(value.Latitude.Field) == "" || strings.TrimSpace(value.Longitude.Field) == "" {
				return nil, fmt.Errorf("interaction %d spatial target fields are required", index)
			}
			compiled.Mappings = append(compiled.Mappings,
				visualizationir.VisualizationInteractionMapping{Source: lat, TargetFieldID: value.Latitude.Field, TargetDatasetID: value.Latitude.Dataset},
				visualizationir.VisualizationInteractionMapping{Source: lon, TargetFieldID: value.Longitude.Field, TargetDatasetID: value.Longitude.Dataset})
			appendTargets := func(targets *[]string, effect visualizationir.VisualizationInteractionEffect) {
				if targets == nil {
					return
				}
				for _, target := range *targets {
					compiled.Targets = append(compiled.Targets, visualizationir.VisualizationInteractionTarget{VisualID: target, Effect: effect})
				}
			}
			appendTargets(value.Targets, visualizationir.VisualizationInteractionEffectFilter)
			appendTargets(value.HighlightTargets, visualizationir.VisualizationInteractionEffectHighlight)
			appendTargets(value.NoneTargets, visualizationir.VisualizationInteractionEffectNone)
		}
		if base.Type == "" {
			return nil, fmt.Errorf("interaction %d type is required", index)
		}
		result = append(result, compiled)
	}
	return result, nil
}
