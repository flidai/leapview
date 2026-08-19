package compiler

// This is the canonical generated-dashboard compilation boundary.  A
// DashboardDocument is lowered directly into query bindings, Visual IR, and
// immutable dashboard definition state; no dashboard/authoring value is used
// as an intermediate representation.

import (
	"fmt"
	"math"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	"github.com/flidai/leapview/internal/dashboard/visualization/geometry"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	"github.com/flidai/leapview/internal/dashboard/visualization/mapasset"
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
		if err := lowerCanonicalVisualSeries(&query, visual.Type); err != nil {
			return DocumentResult{}, fmt.Errorf("visual %q query: %w", visualID, err)
		}
		presentation, err := LowerCanonicalDashboardPresentationForQuery(visual.Presentation, visual.Type, query)
		if err != nil {
			return DocumentResult{}, fmt.Errorf("visual %q presentation: %w", visualID, err)
		}
		if err := validateCanonicalVisualResultReferences(visual, query); err != nil {
			return DocumentResult{}, fmt.Errorf("visual %q references: %w", visualID, err)
		}
		if err := validateDerivedResultAliases(query, visual.Type); err != nil {
			return DocumentResult{}, fmt.Errorf("visual %q result aliases: %w", visualID, err)
		}
		if err := validateCanonicalInteractionKinds(visual); err != nil {
			return DocumentResult{}, fmt.Errorf("visual %q interactions: %w", visualID, err)
		}
		if visual.Type == document.DashboardVisualTypeMap {
			geographic, _ := visual.Presentation.Value.(*document.GeographicDashboardPresentation)
			query.Binding, err = canonicalSpatialBinding(query.Binding, geographic, visual.Query)
			if err != nil {
				return DocumentResult{}, fmt.Errorf("visual %q geographic delivery: %w", visualID, err)
			}
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
		if err := appendCanonicalCalculationOutputs(&spec); err != nil {
			return DocumentResult{}, fmt.Errorf("visual %q calculations: %w", visualID, err)
		}
		if err := visualizationir.ValidateSpec(spec); err != nil {
			return DocumentResult{}, fmt.Errorf("visual %q IR: %w", visualID, err)
		}
		definitionBinding := query.Binding
		if visual.Type == document.DashboardVisualTypeMatrix && definitionBinding.Pivot != nil {
			pivot := definitionBinding.Pivot
			definitionBinding.Kind = visualizationdefinition.QueryMatrix
			definitionBinding.ResultShape = visualizationdefinition.ResultMatrixWindow
			definitionBinding.Matrix = &visualizationdefinition.MatrixQueryBinding{TableID: pivot.TableID, Rows: pivot.Rows, Columns: pivot.Columns, Metrics: pivot.Metrics, Limit: pivot.Limit}
			definitionBinding.Pivot = nil
		}
		compiled, err := visualizationdefinition.NewWithSecondaryQueries(visualID, spec, definitionBinding, secondary)
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

// lowerCanonicalVisualSeries derives a category-series binding from the
// ordered aggregate dimensions. The canonical Dashboard query has no
// renderer-specific `series` property: the first dimension is the category
// axis and the optional second dimension is the authored series field.
func lowerCanonicalVisualSeries(query *LoweredDashboardQuery, visualType document.DashboardVisualType) error {
	if query == nil || query.Binding.Aggregate == nil {
		return nil
	}
	switch visualType {
	case document.DashboardVisualTypeLine, document.DashboardVisualTypeArea, document.DashboardVisualTypeBar, document.DashboardVisualTypeColumn:
	default:
		return nil
	}
	dimensions := query.Binding.Aggregate.Dimensions
	if len(dimensions) <= 1 {
		return nil
	}
	if len(dimensions) != 2 {
		return fmt.Errorf("%s supports at most one category-series dimension", visualType)
	}
	series := dimensions[1]
	query.Binding.Aggregate.Dimensions = append([]visualizationdefinition.FieldBinding(nil), dimensions[:1]...)
	query.Binding.Aggregate.Series = &series
	query.Binding.ResultShape = visualizationdefinition.ResultCategorySeriesValue
	return nil
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

func validateDerivedResultAliases(query LoweredDashboardQuery, visualType document.DashboardVisualType) error {
	reserved := map[string]struct{}{}
	switch visualType {
	case document.DashboardVisualTypeTreemap, document.DashboardVisualTypeTree, document.DashboardVisualTypeSunburst:
		reserved = map[string]struct{}{"node": {}, "parent": {}}
	case document.DashboardVisualTypeGraph, document.DashboardVisualTypeSankey:
		return nil
	case document.DashboardVisualTypeWaterfall:
		reserved = map[string]struct{}{"start": {}, "end": {}, "positive": {}}
	default:
		return nil
	}
	for _, field := range query.ResultFrame {
		if _, exists := reserved[field.Name]; exists {
			return fmt.Errorf("result field %q is reserved for derived %s output", field.Name, visualType)
		}
	}
	return nil
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
	if query.Binding.Spatial != nil && query.Binding.Spatial.Tiles != nil {
		base.DataBudget.MaxRows = 0
	}
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
	base.ConditionalFormatting, conversionErr = canonicalConditionalFormatting(visual.Presentation, query)
	if conversionErr != nil {
		return visualizationir.VisualizationSpec{}, fmt.Errorf("conditional formatting: %w", conversionErr)
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
			columns = append(columns, visualizationir.TableVisualizationColumn{Field: ref(i), Label: query.ResultFrame[i].Name, Formatting: []visualizationir.TableVisualizationFormattingRule{}})
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
		} else if query.Binding.Aggregate != nil && len(query.Binding.Aggregate.Dimensions) >= 2 {
			rows = bindingRefs(query.Binding.Aggregate.Dimensions[:1])
			columns = bindingRefs(query.Binding.Aggregate.Dimensions[1:2])
			metricRefs = bindingRefs(query.Binding.Aggregate.Metrics)
		} else {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("matrix visual requires a matrix, pivot, or two-dimensional aggregate query binding")
		}
		if len(rows) == 0 || len(metricRefs) == 0 {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("matrix visual requires non-empty rows and metrics")
		}
		base.Kind = "matrix"
		return visualizationir.VisualizationSpec{Value: &visualizationir.MatrixVisualizationSpec{VisualizationSpecBase: base, Kind: "matrix", Rows: rows, Columns: columns, Metrics: metricRefs, MetricFormatting: map[string][]visualizationir.TableVisualizationFormattingRule{}, Presentation: p}}, nil
	case document.DashboardVisualTypeHeatmap:
		p, ok := presentation.(visualizationir.CartesianVisualizationPresentation)
		if !ok {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("heatmap presentation lowering returned %T", presentation)
		}
		if err := requireOperands("heatmap", 2, 1, false, false); err != nil {
			return visualizationir.VisualizationSpec{}, err
		}
		base.Kind = "cartesian"
		// Heatmaps use the matrix result shape at runtime (row, column,
		// value), while retaining the canonical Cartesian IR contract so
		// presentation validation and renderer capabilities remain shared with
		// other Cartesian marks.
		return visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{
			VisualizationSpecBase: base,
			Kind:                  "cartesian",
			Mark:                  visualizationir.VisualizationCartesianMarkHeatmap,
			X:                     dimensions[0],
			Y:                     []visualizationir.VisualizationFieldRef{dimensions[1], metrics[0]},
			Presentation:          p,
		}}, nil
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
		kpi := &visualizationir.KPIVisualizationSpec{VisualizationSpecBase: base, Kind: "kpi", Value: metricRef(0), Presentation: p}
		if variant, ok := visual.Presentation.Value.(*document.KPIDashboardPresentation); ok {
			var bindingErr error
			kpi.Comparison, bindingErr = canonicalKPIValueBinding(variant.Comparison, query, secondarySchemas)
			if bindingErr != nil {
				return visualizationir.VisualizationSpec{}, fmt.Errorf("KPI comparison: %w", bindingErr)
			}
			kpi.Goal, bindingErr = canonicalKPIValueBinding(variant.Goal, query, secondarySchemas)
			if bindingErr != nil {
				return visualizationir.VisualizationSpec{}, fmt.Errorf("KPI goal: %w", bindingErr)
			}
			kpi.Trend, bindingErr = canonicalKPITrendBinding(variant.Trend, query, secondarySchemas)
			if bindingErr != nil {
				return visualizationir.VisualizationSpec{}, fmt.Errorf("KPI trend: %w", bindingErr)
			}
		}
		return visualizationir.VisualizationSpec{Value: kpi}, nil
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
		if len(dimensions) < 1 || len(metrics) != 1 {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("hierarchy requires at least one dimension and exactly one metric, got %d and %d", len(dimensions), len(metrics))
		}
		if (visual.Type == document.DashboardVisualTypeGraph || visual.Type == document.DashboardVisualTypeSankey) && len(dimensions) != 2 {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("%s requires exactly two dimensions and one metric", visual.Type)
		}
		base.Kind = "hierarchy"
		if visual.Type == document.DashboardVisualTypeGraph || visual.Type == document.DashboardVisualTypeSankey {
			source, target, value := dimensions[0], dimensions[1], metrics[0]
			return visualizationir.VisualizationSpec{Value: &visualizationir.HierarchyVisualizationSpec{VisualizationSpecBase: base, Kind: "hierarchy", Mark: visualizationir.VisualizationHierarchyMark(typ), Node: source, Source: &source, Target: &target, Value: &value, Presentation: p}}, nil
		}
		node := visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "node"}
		parent := visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "parent"}
		value := metrics[0]
		return visualizationir.VisualizationSpec{Value: &visualizationir.HierarchyVisualizationSpec{VisualizationSpecBase: base, Kind: "hierarchy", Mark: visualizationir.VisualizationHierarchyMark(typ), Node: node, Parent: &parent, Value: &value, Presentation: p}}, nil
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
		p, ok := presentation.(visualizationir.PointVisualizationPresentation)
		if !ok {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("scatter presentation lowering returned %T", presentation)
		}
		variant, ok := visual.Presentation.Value.(*document.PointDashboardPresentation)
		if !ok || variant == nil {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("scatter point presentation variant is required")
		}
		if len(variant.Identity) == 0 {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("scatter requires at least one identity field")
		}
		if variant.X == "" || variant.Y == "" {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("scatter requires x and y result fields")
		}
		pointRef := func(name, channel string) (visualizationir.VisualizationFieldRef, error) {
			if name == "" {
				return visualizationir.VisualizationFieldRef{}, fmt.Errorf("scatter %s result field is required", channel)
			}
			return canonicalResultRef(query, "primary", name)
		}
		identity := make([]visualizationir.VisualizationFieldRef, 0, len(variant.Identity))
		for index, name := range variant.Identity {
			ref, refErr := pointRef(name, fmt.Sprintf("identity[%d]", index))
			if refErr != nil {
				return visualizationir.VisualizationSpec{}, refErr
			}
			identity = append(identity, ref)
		}
		x, err := pointRef(variant.X, "x")
		if err != nil {
			return visualizationir.VisualizationSpec{}, err
		}
		y, err := pointRef(variant.Y, "y")
		if err != nil {
			return visualizationir.VisualizationSpec{}, err
		}
		for datasetIndex := range base.Datasets {
			if base.Datasets[datasetIndex].ID != "primary" {
				continue
			}
			for fieldIndex := range base.Datasets[datasetIndex].Fields {
				for _, identityRef := range identity {
					if base.Datasets[datasetIndex].Fields[fieldIndex].ID == identityRef.Field {
						base.Datasets[datasetIndex].Fields[fieldIndex].Role = visualizationir.VisualizationFieldRoleIdentity
					}
				}
			}
		}
		point := &visualizationir.PointVisualizationSpec{VisualizationSpecBase: base, Kind: "point", Identity: identity, X: x, Y: y, Presentation: p}
		if variant.Size != nil {
			value, refErr := pointRef(*variant.Size, "size")
			if refErr != nil {
				return visualizationir.VisualizationSpec{}, refErr
			}
			point.Size = &value
		}
		if variant.Color != nil {
			value, refErr := pointRef(*variant.Color, "color")
			if refErr != nil {
				return visualizationir.VisualizationSpec{}, refErr
			}
			point.Color = &value
		}
		if variant.Series != nil {
			value, refErr := pointRef(*variant.Series, "series")
			if refErr != nil {
				return visualizationir.VisualizationSpec{}, refErr
			}
			point.Series = &value
		}
		if variant.Label != nil {
			value, refErr := pointRef(*variant.Label, "label")
			if refErr != nil {
				return visualizationir.VisualizationSpec{}, refErr
			}
			point.Label = &value
		}
		if variant.Tooltip != nil {
			tooltip := make([]visualizationir.VisualizationFieldRef, 0, len(*variant.Tooltip))
			for index, name := range *variant.Tooltip {
				value, refErr := pointRef(name, fmt.Sprintf("tooltip[%d]", index))
				if refErr != nil {
					return visualizationir.VisualizationSpec{}, refErr
				}
				tooltip = append(tooltip, value)
			}
			point.Tooltip = &tooltip
		}
		if variant.ColorScale != nil {
			if variant.Color == nil {
				return visualizationir.VisualizationSpec{}, fmt.Errorf("scatter colorScale requires color")
			}
			if variant.ColorScale.Minimum != nil && variant.ColorScale.Maximum != nil && *variant.ColorScale.Minimum >= *variant.ColorScale.Maximum {
				return visualizationir.VisualizationSpec{}, fmt.Errorf("scatter colorScale minimum must be less than maximum")
			}
			point.ColorScale = &visualizationir.PointVisualizationColorScale{Kind: variant.ColorScale.Kind, Minimum: variant.ColorScale.Minimum, Maximum: variant.ColorScale.Maximum, Scheme: variant.ColorScale.Scheme}
		}
		if variant.SizeScale != nil {
			if variant.Size == nil {
				return visualizationir.VisualizationSpec{}, fmt.Errorf("scatter sizeScale requires size")
			}
			if variant.SizeScale.MinimumPixels <= 0 || variant.SizeScale.MaximumPixels <= 0 || variant.SizeScale.MinimumPixels > variant.SizeScale.MaximumPixels {
				return visualizationir.VisualizationSpec{}, fmt.Errorf("scatter sizeScale pixel bounds are invalid")
			}
			if variant.SizeScale.Minimum != nil && variant.SizeScale.Maximum != nil && *variant.SizeScale.Minimum >= *variant.SizeScale.Maximum {
				return visualizationir.VisualizationSpec{}, fmt.Errorf("scatter sizeScale minimum must be less than maximum")
			}
			point.SizeScale = &visualizationir.PointVisualizationSizeScale{Minimum: variant.SizeScale.Minimum, Maximum: variant.SizeScale.Maximum, MinimumPixels: variant.SizeScale.MinimumPixels, MaximumPixels: variant.SizeScale.MaximumPixels}
		}
		base.Kind = "point"
		point.VisualizationSpecBase = base
		return visualizationir.VisualizationSpec{Value: point}, nil
	case document.DashboardVisualTypeMap:
		p, ok := presentation.(visualizationir.GeographicVisualizationPresentation)
		if !ok {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("geographic presentation lowering returned %T", presentation)
		}
		variant, _ := visual.Presentation.Value.(*document.GeographicDashboardPresentation)
		if variant == nil || variant.Layers == nil || len(*variant.Layers) == 0 {
			if len(dimensions) != 2 {
				return visualizationir.VisualizationSpec{}, fmt.Errorf("map visual requires exactly two dimensions for latitude and longitude, got %d", len(dimensions))
			}
		}
		if len(metrics) > 1 {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("map visual supports at most one metric, got %d", len(metrics))
		}
		base.Kind = "geographic"
		spatialInteractions, spatialErr := canonicalSpatialInteractions(visual.Interactions, query)
		if spatialErr != nil {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("spatial interactions: %w", spatialErr)
		}
		layers, layerErr := canonicalGeographicLayers(variant, query)
		if layerErr != nil {
			return visualizationir.VisualizationSpec{}, layerErr
		}
		if len(layers) == 0 {
			lat, lon := dimensions[0], dimensions[1]
			layerBase := visualizationir.VisualizationGeographicLayerBase{ID: "points", Kind: "point", Tooltip: []visualizationir.VisualizationFieldRef{}, Position: visualizationir.VisualizationMapLayerPositionBelowLabels, Visibility: visualizationir.VisualizationMapVisibility{MinimumZoom: 0, MaximumZoom: 24}}
			layer := visualizationir.VisualizationPointLayer{VisualizationGeographicLayerBase: layerBase, Kind: "point", Latitude: lat, Longitude: lon, Size: visualizationir.VisualizationMapSizeScale{MinimumRadius: 2, MaximumRadius: 12}, Color: visualizationir.VisualizationMapColorScale{Kind: visualizationir.VisualizationMapColorScaleKindSequential, Palette: "default"}, Stroke: visualizationir.VisualizationMapStroke{Color: "#ffffff", Width: 1, Opacity: .8}, Cluster: visualizationir.VisualizationMapCluster{Enabled: true, Radius: 40, MaximumZoom: 14, MinimumPoints: 2}, Opacity: .8}
			layers = []visualizationir.VisualizationGeographicLayer{{Value: &layer}}
		}
		basemap := "streets"
		if variant != nil && variant.Basemap != nil {
			basemap = *variant.Basemap
		}
		if basemap != "" && basemap != "blank" {
			asset, resolveErr := mapasset.Resolve(basemap)
			if resolveErr != nil {
				return visualizationir.VisualizationSpec{}, resolveErr
			}
			p.Basemap = &asset
		}
		return visualizationir.VisualizationSpec{Value: &visualizationir.GeographicVisualizationSpec{VisualizationSpecBase: base, Kind: "geographic", Layers: layers, SpatialInteractions: spatialInteractions, Presentation: p}}, nil
	case document.DashboardVisualTypeHistogram, document.DashboardVisualTypeBoxplot:
		p, ok := presentation.(visualizationir.CartesianVisualizationPresentation)
		if !ok {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("statistical presentation lowering returned %T", presentation)
		}
		if typ == document.DashboardVisualTypeBoxplot {
			if len(query.ResultFrame) != 6 {
				return visualizationir.VisualizationSpec{}, fmt.Errorf("boxplot requires label,min,q1,median,q3,max result fields")
			}
			values := make([]visualizationir.VisualizationFieldRef, 0, len(query.ResultFrame)-1)
			for _, field := range query.ResultFrame[1:] {
				values = append(values, visualizationir.VisualizationFieldRef{Dataset: "primary", Field: field.Name})
			}
			base.Kind = "cartesian"
			return visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{VisualizationSpecBase: base, Kind: "cartesian", Mark: visualizationir.VisualizationCartesianMarkBoxplot, X: ref(0), Y: values, Presentation: p}}, nil
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
	case document.DashboardVisualTypeCandlestick:
		p, ok := presentation.(visualizationir.CartesianVisualizationPresentation)
		if !ok {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("candlestick presentation lowering returned %T", presentation)
		}
		if len(dimensions) != 1 || len(metrics) != 4 {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("candlestick requires one dimension and exactly four metrics")
		}
		base.Kind = "cartesian"
		return visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{VisualizationSpecBase: base, Kind: "cartesian", Mark: visualizationir.VisualizationCartesianMarkCandlestick, X: dimensions[0], Y: metrics, Presentation: p}}, nil
	case document.DashboardVisualTypeWaterfall:
		p, ok := presentation.(visualizationir.CartesianVisualizationPresentation)
		if !ok {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("waterfall presentation lowering returned %T", presentation)
		}
		if err := requireOperands("waterfall", 1, 1, false, false); err != nil {
			return visualizationir.VisualizationSpec{}, err
		}
		base.Kind = "cartesian"
		return visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{VisualizationSpecBase: base, Kind: "cartesian", Mark: visualizationir.VisualizationCartesianMarkWaterfall, X: dimensions[0], Y: []visualizationir.VisualizationFieldRef{{Dataset: "primary", Field: "start"}, metrics[0]}, Presentation: p}}, nil
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
		var series *visualizationir.VisualizationFieldRef
		if query.Binding.Aggregate != nil && query.Binding.Aggregate.Series != nil {
			seriesRef := visualizationir.VisualizationFieldRef{Dataset: "primary", Field: query.Binding.Aggregate.Series.Alias}
			series = &seriesRef
		}
		return visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{VisualizationSpecBase: base, Kind: "cartesian", Mark: mark, X: x, Y: metrics, Series: series, Presentation: p}}, nil
	}
}

func canonicalConditionalFormatting(presentation document.DashboardPresentation, query LoweredDashboardQuery) (*[]visualizationir.VisualizationConditionalFormat, error) {
	if presentation.Value == nil {
		return nil, nil
	}
	base, err := presentation.Base()
	if err != nil {
		return nil, err
	}
	if base.ConditionalFormatting == nil {
		return nil, nil
	}
	result := make([]visualizationir.VisualizationConditionalFormat, 0, len(*base.ConditionalFormatting))
	for index, authored := range *base.ConditionalFormatting {
		field, err := canonicalResultRef(query, "primary", authored.Field)
		if err != nil {
			return nil, fmt.Errorf("entry %d field: %w", index, err)
		}
		compiled := visualizationir.VisualizationConditionalFormat{ID: authored.ID, Target: authored.Target, Field: field}
		switch rule := authored.Rule.Value.(type) {
		case *document.DashboardGradientConditionalRule:
			if rule == nil {
				return nil, fmt.Errorf("entry %d gradient rule is nil", index)
			}
			if !finiteDashboardFloat(rule.Minimum) || !finiteDashboardFloat(rule.Maximum) || rule.Minimum >= rule.Maximum {
				return nil, fmt.Errorf("entry %d gradient minimum must be finite and less than maximum", index)
			}
			compiled.Rule.Value = &visualizationir.GradientVisualizationConditionalRule{
				VisualizationConditionalRuleBase: visualizationir.VisualizationConditionalRuleBase{Kind: "gradient"},
				Kind:                             "gradient", Minimum: rule.Minimum, Maximum: rule.Maximum,
				Low: canonicalConditionalStyle(rule.Low), High: canonicalConditionalStyle(rule.High), NullStyle: canonicalConditionalStyle(rule.NullStyle),
			}
		case *document.DashboardRulesConditionalRule:
			if rule == nil {
				return nil, fmt.Errorf("entry %d rules rule is nil", index)
			}
			thresholds := make([]visualizationir.VisualizationConditionalThreshold, len(rule.Rules))
			for thresholdIndex, threshold := range rule.Rules {
				if !finiteDashboardFloat(threshold.Value) {
					return nil, fmt.Errorf("entry %d rule %d value must be finite", index, thresholdIndex)
				}
				thresholds[thresholdIndex] = visualizationir.VisualizationConditionalThreshold{Operator: threshold.Operator, Value: threshold.Value, Style: canonicalConditionalStyle(threshold.Style)}
			}
			compiled.Rule.Value = &visualizationir.RulesVisualizationConditionalRule{
				VisualizationConditionalRuleBase: visualizationir.VisualizationConditionalRuleBase{Kind: "rules"},
				Kind:                             "rules", Rules: thresholds, NullStyle: canonicalConditionalStyle(rule.NullStyle), DefaultStyle: canonicalConditionalStyle(rule.DefaultStyle),
			}
		case *document.DashboardFieldConditionalRule:
			if rule == nil {
				return nil, fmt.Errorf("entry %d field rule is nil", index)
			}
			source, err := canonicalResultRef(query, "primary", rule.Source)
			if err != nil {
				return nil, fmt.Errorf("entry %d source: %w", index, err)
			}
			values := make(map[string]visualizationir.VisualizationConditionalStyle, len(rule.Values))
			for value, style := range rule.Values {
				values[value] = canonicalConditionalStyle(style)
			}
			compiled.Rule.Value = &visualizationir.FieldVisualizationConditionalRule{
				VisualizationConditionalRuleBase: visualizationir.VisualizationConditionalRuleBase{Kind: "field"},
				Kind:                             "field", Source: source, Values: values, NullStyle: canonicalConditionalStyle(rule.NullStyle), DefaultStyle: canonicalConditionalStyle(rule.DefaultStyle),
			}
		default:
			return nil, fmt.Errorf("entry %d has unsupported rule %T", index, authored.Rule.Value)
		}
		result = append(result, compiled)
	}
	return &result, nil
}

func canonicalConditionalStyle(authored document.DashboardConditionalStyle) visualizationir.VisualizationConditionalStyle {
	return visualizationir.VisualizationConditionalStyle{Color: authored.Color, Icon: authored.Icon}
}

func canonicalSpatialBinding(binding visualizationdefinition.QueryBinding, presentation *document.GeographicDashboardPresentation, authoredQuery document.DashboardQuery) (visualizationdefinition.QueryBinding, error) {
	if binding.Aggregate == nil {
		return binding, nil
	}
	spatial := &visualizationdefinition.SpatialQueryBinding{TableID: binding.Aggregate.TableID, Dimensions: append([]visualizationdefinition.FieldBinding(nil), binding.Aggregate.Dimensions...), Metrics: append([]visualizationdefinition.FieldBinding(nil), binding.Aggregate.Metrics...), Limit: binding.Aggregate.Limit, Sort: append([]visualizationdefinition.Sort(nil), binding.Aggregate.Sort...)}
	result := visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QuerySpatial, ResultShape: visualizationdefinition.ResultGeographicFeatures, ModelID: binding.ModelID, DatasetID: binding.DatasetID, Identity: append([]string(nil), binding.Identity...), Spatial: spatial}
	if presentation == nil || presentation.Layers == nil {
		return result, nil
	}
	latitudeAlias, longitudeAlias := "", ""
	hasTiled, hasInline := false, false
	cellRadius := 32.0
	for index, layer := range *presentation.Layers {
		switch value := layer.Value.(type) {
		case *document.DashboardPointGeographicLayer:
			hasTiled = true
			if value != nil {
				if value.Size != nil && value.Size.MaximumRadius != nil {
					cellRadius = math.Max(cellRadius, *value.Size.MaximumRadius)
				}
				if value.Cluster != nil && value.Cluster.Radius != nil {
					cellRadius = math.Max(cellRadius, float64(*value.Cluster.Radius))
				}
				if err := mergeTiledCoordinates(&latitudeAlias, &longitudeAlias, value.Latitude, value.Longitude); err != nil {
					return visualizationdefinition.QueryBinding{}, fmt.Errorf("layer %d: %w", index, err)
				}
			}
		case *document.DashboardHeatGeographicLayer:
			hasTiled = true
			if value != nil {
				if value.Heat != nil && value.Heat.Radius != nil {
					cellRadius = math.Max(cellRadius, *value.Heat.Radius)
				}
				if err := mergeTiledCoordinates(&latitudeAlias, &longitudeAlias, value.Latitude, value.Longitude); err != nil {
					return visualizationdefinition.QueryBinding{}, fmt.Errorf("layer %d: %w", index, err)
				}
			}
		case *document.DashboardDensityGeographicLayer:
			hasTiled = true
			if value != nil {
				if value.Heat != nil && value.Heat.Radius != nil {
					cellRadius = math.Max(cellRadius, *value.Heat.Radius)
				}
				if err := mergeTiledCoordinates(&latitudeAlias, &longitudeAlias, value.Latitude, value.Longitude); err != nil {
					return visualizationdefinition.QueryBinding{}, fmt.Errorf("layer %d: %w", index, err)
				}
			}
		case *document.DashboardChoroplethGeographicLayer, *document.DashboardPathGeographicLayer:
			hasInline = true
		case *document.DashboardReferenceGeographicLayer:
		default:
			return visualizationdefinition.QueryBinding{}, fmt.Errorf("layer %d has unsupported variant %T", index, layer.Value)
		}
	}
	if hasTiled && hasInline {
		return visualizationdefinition.QueryBinding{}, fmt.Errorf("cannot mix tiled point, heat, or density layers with inline choropleth or path layers")
	}
	if !hasTiled {
		return result, nil
	}
	if aggregate, ok := authoredQuery.Value.(*document.AggregateDashboardQuery); ok && aggregate.Limit != nil {
		return visualizationdefinition.QueryBinding{}, fmt.Errorf("tiled geographic visual must not set query.limit; tile budgets govern transport")
	}
	latitude, latitudeOK := fieldBindingByAlias(spatial.Dimensions, latitudeAlias)
	longitude, longitudeOK := fieldBindingByAlias(spatial.Dimensions, longitudeAlias)
	if !latitudeOK || !longitudeOK {
		return visualizationdefinition.QueryBinding{}, fmt.Errorf("tiled coordinates %q and %q must reference compiled dimension aliases", latitudeAlias, longitudeAlias)
	}
	spatial.Limit = 0
	spatial.Tiles = &visualizationdefinition.SpatialTileBinding{
		Latitude: latitude, Longitude: longitude,
		MinimumZoom: 0, MaximumZoom: 18, RawMinimumZoom: 5,
		FeatureCap: 5000, MaximumBytes: 512 * 1024, MetatileSize: 4,
		CellRadius: int32(math.Round(math.Max(32, math.Min(64, cellRadius)))),
	}
	return result, nil
}

func mergeTiledCoordinates(latitude, longitude *string, nextLatitude, nextLongitude string) error {
	if strings.TrimSpace(nextLatitude) == "" || strings.TrimSpace(nextLongitude) == "" {
		return fmt.Errorf("tiled layer requires latitude and longitude")
	}
	if *latitude == "" && *longitude == "" {
		*latitude, *longitude = nextLatitude, nextLongitude
		return nil
	}
	if *latitude != nextLatitude || *longitude != nextLongitude {
		return fmt.Errorf("tiled coordinate layers must share one latitude and longitude pair")
	}
	return nil
}

func fieldBindingByAlias(fields []visualizationdefinition.FieldBinding, alias string) (visualizationdefinition.FieldBinding, bool) {
	for _, field := range fields {
		if field.Alias == alias {
			return field, true
		}
	}
	return visualizationdefinition.FieldBinding{}, false
}

func canonicalGeographicLayers(value *document.GeographicDashboardPresentation, query LoweredDashboardQuery) ([]visualizationir.VisualizationGeographicLayer, error) {
	if value == nil || value.Layers == nil {
		return nil, nil
	}
	result := make([]visualizationir.VisualizationGeographicLayer, 0, len(*value.Layers))
	for index, authored := range *value.Layers {
		if authored.Value == nil {
			return nil, fmt.Errorf("map layer %d is required", index)
		}
		var layer visualizationir.VisualizationGeographicLayer
		var err error
		switch variant := authored.Value.(type) {
		case *document.DashboardPointGeographicLayer:
			if variant == nil {
				return nil, fmt.Errorf("map layer %d variant is nil", index)
			}
			layer, err = canonicalPointGeographicLayer(variant, query)
		case *document.DashboardChoroplethGeographicLayer:
			if variant == nil {
				return nil, fmt.Errorf("map layer %d variant is nil", index)
			}
			layer, err = canonicalChoroplethGeographicLayer(variant, query)
		case *document.DashboardReferenceGeographicLayer:
			if variant == nil {
				return nil, fmt.Errorf("map layer %d variant is nil", index)
			}
			layer, err = canonicalReferenceGeographicLayer(variant, query)
		case *document.DashboardHeatGeographicLayer:
			if variant == nil {
				return nil, fmt.Errorf("map layer %d variant is nil", index)
			}
			layer, err = canonicalHeatGeographicLayer(variant, query)
		case *document.DashboardDensityGeographicLayer:
			if variant == nil {
				return nil, fmt.Errorf("map layer %d variant is nil", index)
			}
			layer, err = canonicalDensityGeographicLayer(variant, query)
		case *document.DashboardPathGeographicLayer:
			if variant == nil {
				return nil, fmt.Errorf("map layer %d variant is nil", index)
			}
			layer, err = canonicalPathGeographicLayer(variant, query)
		default:
			return nil, fmt.Errorf("map layer %d uses unsupported variant %T", index, authored.Value)
		}
		if err != nil {
			return nil, fmt.Errorf("map layer %d: %w", index, err)
		}
		result = append(result, layer)
	}
	return result, nil
}

func canonicalMapLayerBase(base *document.DashboardGeographicLayerBase, kind string, query LoweredDashboardQuery) (visualizationir.VisualizationGeographicLayerBase, error) {
	if base == nil {
		return visualizationir.VisualizationGeographicLayerBase{}, fmt.Errorf("map layer base is required")
	}
	id := strings.TrimSpace(base.ID)
	if id == "" {
		return visualizationir.VisualizationGeographicLayerBase{}, fmt.Errorf("map layer id is required")
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return visualizationir.VisualizationGeographicLayerBase{}, fmt.Errorf("map layer %q kind is required", id)
	}
	out := visualizationir.VisualizationGeographicLayerBase{
		ID:         id,
		Kind:       kind,
		Tooltip:    []visualizationir.VisualizationFieldRef{},
		Position:   visualizationir.VisualizationMapLayerPositionBelowLabels,
		Visibility: visualizationir.VisualizationMapVisibility{MinimumZoom: 0, MaximumZoom: 24},
	}
	if base.Label != nil {
		label, err := canonicalResultRef(query, "primary", *base.Label)
		if err != nil {
			return visualizationir.VisualizationGeographicLayerBase{}, fmt.Errorf("label: %w", err)
		}
		out.Label = &label
	}
	if base.Tooltip != nil {
		out.Tooltip = make([]visualizationir.VisualizationFieldRef, 0, len(*base.Tooltip))
		for _, name := range *base.Tooltip {
			ref, err := canonicalResultRef(query, "primary", name)
			if err != nil {
				return visualizationir.VisualizationGeographicLayerBase{}, fmt.Errorf("tooltip %q: %w", name, err)
			}
			out.Tooltip = append(out.Tooltip, ref)
		}
	}
	if base.Position != nil {
		out.Position = *base.Position
	}
	if base.MinimumZoom != nil {
		out.Visibility.MinimumZoom = *base.MinimumZoom
	}
	if base.MaximumZoom != nil {
		out.Visibility.MaximumZoom = *base.MaximumZoom
	}
	if out.Visibility.MinimumZoom < 0 || out.Visibility.MaximumZoom <= out.Visibility.MinimumZoom {
		return visualizationir.VisualizationGeographicLayerBase{}, fmt.Errorf("map layer %q has invalid visibility", id)
	}
	return out, nil
}

func canonicalMapOptionalRef(query LoweredDashboardQuery, name *string, field string) (*visualizationir.VisualizationFieldRef, error) {
	if name == nil {
		return nil, nil
	}
	ref, err := canonicalResultRef(query, "primary", *name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", field, err)
	}
	return &ref, nil
}

func canonicalMapRequiredRef(query LoweredDashboardQuery, name, field string) (visualizationir.VisualizationFieldRef, error) {
	if strings.TrimSpace(name) == "" {
		return visualizationir.VisualizationFieldRef{}, fmt.Errorf("%s is required", field)
	}
	ref, err := canonicalResultRef(query, "primary", name)
	if err != nil {
		return visualizationir.VisualizationFieldRef{}, fmt.Errorf("%s: %w", field, err)
	}
	return ref, nil
}

func canonicalMapColor(value *document.DashboardMapColorScale) visualizationir.VisualizationMapColorScale {
	out := visualizationir.VisualizationMapColorScale{Kind: visualizationir.VisualizationMapColorScaleKindSequential, Palette: "default"}
	if value == nil {
		return out
	}
	if value.Kind != nil {
		out.Kind = *value.Kind
	}
	if value.Palette != nil {
		out.Palette = *value.Palette
	}
	if value.Reverse != nil {
		out.Reverse = *value.Reverse
	}
	out.DomainMinimum = value.DomainMinimum
	out.DomainMidpoint = value.DomainMidpoint
	out.DomainMaximum = value.DomainMaximum
	if value.NullColor != nil {
		out.NullColor = *value.NullColor
	}
	return out
}

func canonicalMapStroke(value *document.DashboardMapStroke) (visualizationir.VisualizationMapStroke, error) {
	out := visualizationir.VisualizationMapStroke{Color: "#ffffff", Width: 1, Opacity: .8}
	if value == nil {
		return out, nil
	}
	if value.Color != nil {
		out.Color = *value.Color
	}
	if value.Width != nil {
		out.Width = *value.Width
	}
	if value.Opacity != nil {
		out.Opacity = *value.Opacity
	}
	if out.Width < 0 {
		return visualizationir.VisualizationMapStroke{}, fmt.Errorf("stroke width must be non-negative")
	}
	if out.Opacity < 0 || out.Opacity > 1 {
		return visualizationir.VisualizationMapStroke{}, fmt.Errorf("stroke opacity must be between 0 and 1")
	}
	return out, nil
}

func canonicalMapSize(value *document.DashboardMapSizeScale) (visualizationir.VisualizationMapSizeScale, error) {
	out := visualizationir.VisualizationMapSizeScale{MinimumRadius: 2, MaximumRadius: 12}
	if value == nil {
		return out, nil
	}
	if value.MinimumRadius != nil {
		out.MinimumRadius = *value.MinimumRadius
	}
	if value.MaximumRadius != nil {
		out.MaximumRadius = *value.MaximumRadius
	}
	out.DomainMinimum = value.DomainMinimum
	out.DomainMaximum = value.DomainMaximum
	if out.MinimumRadius < 0 || out.MaximumRadius < out.MinimumRadius {
		return visualizationir.VisualizationMapSizeScale{}, fmt.Errorf("size scale has invalid radius range")
	}
	if out.DomainMinimum != nil && out.DomainMaximum != nil && *out.DomainMaximum < *out.DomainMinimum {
		return visualizationir.VisualizationMapSizeScale{}, fmt.Errorf("size scale has invalid domain range")
	}
	return out, nil
}

func canonicalMapHeat(value *document.DashboardMapHeatStyle) (visualizationir.VisualizationMapHeatStyle, error) {
	out := visualizationir.VisualizationMapHeatStyle{Radius: 24, Intensity: 1}
	if value == nil {
		return out, nil
	}
	if value.Radius != nil {
		out.Radius = *value.Radius
	}
	if value.Intensity != nil {
		out.Intensity = *value.Intensity
	}
	if out.Radius <= 0 || out.Intensity < 0 {
		return visualizationir.VisualizationMapHeatStyle{}, fmt.Errorf("heat style requires positive radius and non-negative intensity")
	}
	return out, nil
}

func canonicalMapLine(value *document.DashboardMapLineStyle) (visualizationir.VisualizationMapLineStyle, error) {
	out := visualizationir.VisualizationMapLineStyle{Width: 3}
	if value == nil {
		return out, nil
	}
	if value.Width != nil {
		out.Width = *value.Width
	}
	if value.Curvature != nil {
		out.Curvature = *value.Curvature
	}
	if out.Width < 0 || out.Curvature < 0 || out.Curvature > 1 {
		return visualizationir.VisualizationMapLineStyle{}, fmt.Errorf("line style has invalid width or curvature")
	}
	return out, nil
}

func canonicalMapCluster(value *document.DashboardMapCluster) (visualizationir.VisualizationMapCluster, error) {
	out := visualizationir.VisualizationMapCluster{Enabled: true, Radius: 40, MaximumZoom: 14, MinimumPoints: 2}
	if value == nil {
		return out, nil
	}
	if value.Enabled != nil {
		out.Enabled = *value.Enabled
	}
	if value.Radius != nil {
		out.Radius = *value.Radius
	}
	if value.MaximumZoom != nil {
		out.MaximumZoom = *value.MaximumZoom
	}
	if value.MinimumPoints != nil {
		out.MinimumPoints = *value.MinimumPoints
	}
	if value.ShowCount != nil {
		out.ShowCount = *value.ShowCount
	}
	if out.Radius <= 0 || out.MaximumZoom < 0 || out.MinimumPoints < 2 {
		return visualizationir.VisualizationMapCluster{}, fmt.Errorf("cluster configuration is invalid")
	}
	return out, nil
}

func canonicalMapOpacity(value *float64) (float64, error) {
	opacity := 1.0
	if value != nil {
		opacity = *value
	}
	if opacity < 0 || opacity > 1 {
		return 0, fmt.Errorf("opacity must be between 0 and 1")
	}
	return opacity, nil
}

func canonicalPointGeographicLayer(layer *document.DashboardPointGeographicLayer, query LoweredDashboardQuery) (visualizationir.VisualizationGeographicLayer, error) {
	base, err := canonicalMapLayerBase(&layer.DashboardGeographicLayerBase, layer.Kind, query)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	latitude, err := canonicalMapRequiredRef(query, layer.Latitude, "latitude")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	longitude, err := canonicalMapRequiredRef(query, layer.Longitude, "longitude")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	value, err := canonicalMapOptionalRef(query, layer.Value, "value")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	category, err := canonicalMapOptionalRef(query, layer.Category, "category")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	size, err := canonicalMapSize(layer.Size)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	stroke, err := canonicalMapStroke(layer.Stroke)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	cluster, err := canonicalMapCluster(layer.Cluster)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	opacity, err := canonicalMapOpacity(layer.Opacity)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	return visualizationir.VisualizationGeographicLayer{Value: &visualizationir.VisualizationPointLayer{VisualizationGeographicLayerBase: base, Kind: layer.Kind, Latitude: latitude, Longitude: longitude, Value: value, Category: category, Size: size, Color: canonicalMapColor(layer.Color), Stroke: stroke, Cluster: cluster, Opacity: opacity}}, nil
}

func canonicalChoroplethGeographicLayer(layer *document.DashboardChoroplethGeographicLayer, query LoweredDashboardQuery) (visualizationir.VisualizationGeographicLayer, error) {
	base, err := canonicalMapLayerBase(&layer.DashboardGeographicLayerBase, layer.Kind, query)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	join, err := canonicalMapRequiredRef(query, layer.Join, "join")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	value, err := canonicalMapOptionalRef(query, layer.Value, "value")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	category, err := canonicalMapOptionalRef(query, layer.Category, "category")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	geometryAsset, err := geometry.Resolve(layer.GeometryAsset)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	stroke, err := canonicalMapStroke(layer.Stroke)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	opacity, err := canonicalMapOpacity(layer.Opacity)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	return visualizationir.VisualizationGeographicLayer{Value: &visualizationir.VisualizationChoroplethLayer{VisualizationGeographicLayerBase: base, Kind: layer.Kind, Geometry: geometryAsset, Join: join, Value: value, Category: category, Color: canonicalMapColor(layer.Color), Stroke: stroke, Opacity: opacity}}, nil
}

func canonicalReferenceGeographicLayer(layer *document.DashboardReferenceGeographicLayer, query LoweredDashboardQuery) (visualizationir.VisualizationGeographicLayer, error) {
	base, err := canonicalMapLayerBase(&layer.DashboardGeographicLayerBase, layer.Kind, query)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	geometryAsset, err := geometry.Resolve(layer.GeometryAsset)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	stroke, err := canonicalMapStroke(layer.Stroke)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	opacity, err := canonicalMapOpacity(layer.Opacity)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	return visualizationir.VisualizationGeographicLayer{Value: &visualizationir.VisualizationReferenceLayer{VisualizationGeographicLayerBase: base, Kind: layer.Kind, Geometry: geometryAsset, Color: canonicalMapColor(layer.Color), Stroke: stroke, Opacity: opacity}}, nil
}

func canonicalHeatGeographicLayer(layer *document.DashboardHeatGeographicLayer, query LoweredDashboardQuery) (visualizationir.VisualizationGeographicLayer, error) {
	return canonicalHeatOrDensityGeographicLayer(layer.Kind, layer.Latitude, layer.Longitude, layer.Value, layer.Color, layer.Heat, layer.Opacity, query, true, &layer.DashboardGeographicLayerBase)
}

func canonicalDensityGeographicLayer(layer *document.DashboardDensityGeographicLayer, query LoweredDashboardQuery) (visualizationir.VisualizationGeographicLayer, error) {
	return canonicalHeatOrDensityGeographicLayer(layer.Kind, layer.Latitude, layer.Longitude, layer.Value, layer.Color, layer.Heat, layer.Opacity, query, false, &layer.DashboardGeographicLayerBase)
}

func canonicalHeatOrDensityGeographicLayer(kind, latitudeName, longitudeName string, valueName *string, color *document.DashboardMapColorScale, heatStyle *document.DashboardMapHeatStyle, opacityValue *float64, query LoweredDashboardQuery, heat bool, baseValue *document.DashboardGeographicLayerBase) (visualizationir.VisualizationGeographicLayer, error) {
	base, err := canonicalMapLayerBase(baseValue, kind, query)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	latitude, err := canonicalMapRequiredRef(query, latitudeName, "latitude")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	longitude, err := canonicalMapRequiredRef(query, longitudeName, "longitude")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	value, err := canonicalMapOptionalRef(query, valueName, "value")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	heatValue, err := canonicalMapHeat(heatStyle)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	opacity, err := canonicalMapOpacity(opacityValue)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	if heat {
		return visualizationir.VisualizationGeographicLayer{Value: &visualizationir.VisualizationHeatLayer{VisualizationGeographicLayerBase: base, Kind: kind, Latitude: latitude, Longitude: longitude, Value: value, Color: canonicalMapColor(color), Heat: heatValue, Opacity: opacity}}, nil
	}
	return visualizationir.VisualizationGeographicLayer{Value: &visualizationir.VisualizationDensityLayer{VisualizationGeographicLayerBase: base, Kind: kind, Latitude: latitude, Longitude: longitude, Value: value, Color: canonicalMapColor(color), Heat: heatValue, Opacity: opacity}}, nil
}

func canonicalPathGeographicLayer(layer *document.DashboardPathGeographicLayer, query LoweredDashboardQuery) (visualizationir.VisualizationGeographicLayer, error) {
	base, err := canonicalMapLayerBase(&layer.DashboardGeographicLayerBase, layer.Kind, query)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	latitude, err := canonicalMapRequiredRef(query, layer.Latitude, "latitude")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	longitude, err := canonicalMapRequiredRef(query, layer.Longitude, "longitude")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	path, err := canonicalMapRequiredRef(query, layer.Path, "path")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	order, err := canonicalMapRequiredRef(query, layer.Order, "order")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	value, err := canonicalMapOptionalRef(query, layer.Value, "value")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	category, err := canonicalMapOptionalRef(query, layer.Category, "category")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	stroke, err := canonicalMapStroke(layer.Stroke)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	line, err := canonicalMapLine(layer.Line)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	opacity, err := canonicalMapOpacity(layer.Opacity)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	return visualizationir.VisualizationGeographicLayer{Value: &visualizationir.VisualizationPathLayer{VisualizationGeographicLayerBase: base, Kind: layer.Kind, Latitude: latitude, Longitude: longitude, Path: path, Order: order, Value: value, Category: category, Color: canonicalMapColor(layer.Color), Stroke: stroke, Line: line, Opacity: opacity}}, nil
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
		if _, ok := metricNames[field.Name]; ok {
			role, typ = visualizationir.VisualizationFieldRoleMetric, visualizationir.VisualizationDataTypeDecimal
		}
		if query.Type == "records" {
			role, typ = visualizationir.VisualizationFieldRoleDimension, canonicalPhysicalDataType(model, field.Source)
		} else if _, ok := metricNames[field.Name]; !ok {
			typ = canonicalSemanticDataType(model, field.Source, false)
		} else {
			typ = canonicalSemanticDataType(model, field.Source, true)
		}
		if query.Type == "distribution" {
			if i == 0 {
				role, typ = visualizationir.VisualizationFieldRoleDimension, visualizationir.VisualizationDataTypeString
			} else {
				role, typ = visualizationir.VisualizationFieldRoleMetric, visualizationir.VisualizationDataTypeDecimal
			}
		}
		if query.Type == "histogram" {
			switch field.Name {
			case "count":
				role, typ = visualizationir.VisualizationFieldRoleMetric, visualizationir.VisualizationDataTypeInteger
			case "start", "end":
				role, typ = visualizationir.VisualizationFieldRoleMetric, visualizationir.VisualizationDataTypeDecimal
			default:
				role, typ = visualizationir.VisualizationFieldRoleDimension, visualizationir.VisualizationDataTypeString
			}
		}
		var sourceRef *string
		if strings.TrimSpace(field.Source) != "" {
			source := field.Source
			sourceRef = &source
		}
		label := field.Name
		var format *visualizationir.VisualizationFormat
		if query.Type == "records" {
			label = canonicalPhysicalFieldLabel(model, field.Source, label)
		} else if _, metric := metricNames[field.Name]; metric && query.Type != "distribution" && query.Type != "histogram" {
			label, format = canonicalMetricPresentation(model, field.Source, label)
		} else if query.Type != "distribution" && query.Type != "histogram" {
			label = canonicalDimensionLabel(model, field.Source, label)
		}
		fields = append(fields, visualizationir.VisualizationField{ID: field.Name, SourceRef: sourceRef, Role: role, DataType: typ, Nullable: true, Label: label, Format: format})
	}
	if query.Binding.ResultShape == visualizationdefinition.ResultHierarchyNodes {
		mark := "node"
		if query.Binding.Aggregate != nil && len(query.Binding.Aggregate.Dimensions) > 1 {
			mark = "node"
		}
		fields = append(fields,
			visualizationir.VisualizationField{ID: mark, Role: visualizationir.VisualizationFieldRoleIdentity, DataType: visualizationir.VisualizationDataTypeString, Nullable: true, Label: mark},
			visualizationir.VisualizationField{ID: "parent", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeString, Nullable: true, Label: "parent"},
		)
	}
	if query.Binding.ResultShape == visualizationdefinition.ResultCategoryDelta {
		fields = append(fields,
			visualizationir.VisualizationField{ID: "start", Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal, Nullable: true, Label: "start"},
			visualizationir.VisualizationField{ID: "end", Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal, Nullable: true, Label: "end"},
			visualizationir.VisualizationField{ID: "positive", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeBoolean, Nullable: true, Label: "positive"},
		)
	}
	return fields
}

func canonicalMetricPresentation(model *semanticmodel.Model, name, fallbackLabel string) (string, *visualizationir.VisualizationFormat) {
	if model == nil {
		return fallbackLabel, nil
	}
	metric, ok := model.Metrics[name]
	if !ok {
		return fallbackLabel, nil
	}
	label := fallbackLabel
	if strings.TrimSpace(metric.Label) != "" {
		label = metric.Label
	}
	switch metric.Format {
	case "currency":
		currency := strings.ToUpper(strings.TrimSpace(metric.Unit))
		if currency == "" {
			return label, nil
		}
		minimum, maximum := int32(2), int32(2)
		return label, &visualizationir.VisualizationFormat{Value: &visualizationir.CurrencyVisualizationFormat{Kind: "currency", Currency: currency, MinimumFractionDigits: &minimum, MaximumFractionDigits: &maximum}}
	case "integer":
		digits := int32(0)
		return label, &visualizationir.VisualizationFormat{Value: &visualizationir.NumberVisualizationFormat{Kind: "number", MinimumFractionDigits: &digits, MaximumFractionDigits: &digits}}
	case "decimal":
		return label, &visualizationir.VisualizationFormat{Value: &visualizationir.NumberVisualizationFormat{Kind: "number"}}
	default:
		return label, nil
	}
}

func canonicalDimensionLabel(model *semanticmodel.Model, name, fallback string) string {
	if model == nil {
		return fallback
	}
	dimension, ok := model.Dimensions[name]
	if ok && strings.TrimSpace(dimension.Label) != "" {
		return dimension.Label
	}
	return fallback
}

func canonicalPhysicalFieldLabel(model *semanticmodel.Model, source, fallback string) string {
	if model == nil {
		return fallback
	}
	parts := strings.SplitN(source, ".", 2)
	if len(parts) != 2 {
		return fallback
	}
	table, ok := model.Tables[parts[0]]
	if !ok {
		return fallback
	}
	field, ok := table.Dimensions[parts[1]]
	if ok && strings.TrimSpace(field.Label) != "" {
		return field.Label
	}
	return fallback
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

func canonicalKPIValueBinding(value *document.DashboardKPIValueBinding, query LoweredDashboardQuery, secondary []visualizationir.VisualizationDatasetSchema) (*visualizationir.VisualizationKPIValueBinding, error) {
	if value == nil {
		return nil, nil
	}
	field, err := canonicalDatasetResultRef(query, secondary, value.Dataset, value.Field)
	if err != nil {
		return nil, err
	}
	reducer := visualizationir.VisualizationReferenceReducerFirst
	if value.Reducer != nil {
		reducer = *value.Reducer
	}
	return &visualizationir.VisualizationKPIValueBinding{Field: field, Reducer: reducer, Label: value.Label}, nil
}

func canonicalKPITrendBinding(value *document.DashboardKPITrendBinding, query LoweredDashboardQuery, secondary []visualizationir.VisualizationDatasetSchema) (*visualizationir.VisualizationKPITrendBinding, error) {
	if value == nil {
		return nil, nil
	}
	category, err := canonicalDatasetResultRef(query, secondary, value.Dataset, value.Category)
	if err != nil {
		return nil, err
	}
	metric, err := canonicalDatasetResultRef(query, secondary, value.Dataset, value.Value)
	if err != nil {
		return nil, err
	}
	return &visualizationir.VisualizationKPITrendBinding{Category: category, Value: metric}, nil
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
		calculation := visualizationir.VisualizationCalculation{ID: value.ID, Dataset: "primary", Source: source, Hidden: valueOrBool(value.Hidden), Template: value.Template, OrderBy: []visualizationir.VisualizationCalculationOrder{}, PartitionBy: []visualizationir.VisualizationFieldRef{}}
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
				direction := visualizationir.VisualizationSortDirectionAscending
				if order.Direction == "desc" {
					direction = visualizationir.VisualizationSortDirectionDescending
				}
				calculation.OrderBy[orderIndex] = visualizationir.VisualizationCalculationOrder{Field: ref, Direction: direction}
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

func appendCanonicalCalculationOutputs(spec *visualizationir.VisualizationSpec) error {
	if spec == nil {
		return nil
	}
	base, err := spec.Base()
	if err != nil {
		return err
	}
	if base.Calculations == nil || len(*base.Calculations) == 0 {
		return nil
	}
	var primary *visualizationir.VisualizationDatasetSchema
	for index := range base.Datasets {
		if base.Datasets[index].ID == "primary" {
			primary = &base.Datasets[index]
			break
		}
	}
	if primary == nil {
		return fmt.Errorf("primary dataset is required")
	}
	fields := make(map[string]visualizationir.VisualizationField, len(primary.Fields)+len(*base.Calculations))
	for _, field := range primary.Fields {
		fields[field.ID] = field
	}
	for _, calculation := range *base.Calculations {
		if _, exists := fields[calculation.ID]; exists {
			return fmt.Errorf("calculation output %q collides with an existing result field", calculation.ID)
		}
		source, exists := fields[calculation.Source.Field]
		if !exists {
			return fmt.Errorf("calculation %q references unknown source %q", calculation.ID, calculation.Source.Field)
		}
		calculationID := calculation.ID
		field := visualizationir.VisualizationField{
			ID: calculation.ID, Role: visualizationir.VisualizationFieldRoleMetric,
			DataType: canonicalCalculationDataType(calculation.Template, source.DataType), Nullable: true,
			Label: calculation.Label, Format: calculation.Format,
			Provenance: &visualizationir.VisualizationFieldProvenance{
				Kind:       visualizationir.VisualizationFieldProvenanceKindVisualCalculation,
				SourceRefs: []string{calculation.Source.Field}, CalculationID: &calculationID,
			},
		}
		primary.Fields = append(primary.Fields, field)
		fields[field.ID] = field
		if calculation.Hidden {
			continue
		}
		ref := visualizationir.VisualizationFieldRef{Dataset: "primary", Field: field.ID}
		switch value := spec.Value.(type) {
		case *visualizationir.CartesianVisualizationSpec:
			value.Y = append(value.Y, ref)
		case *visualizationir.TableVisualizationSpec:
			value.Columns = append(value.Columns, visualizationir.TableVisualizationColumn{Field: ref, Label: field.Label, Formatting: []visualizationir.TableVisualizationFormattingRule{}})
		case *visualizationir.MatrixVisualizationSpec:
			value.Metrics = append(value.Metrics, ref)
		case *visualizationir.PivotVisualizationSpec:
			value.Metrics = append(value.Metrics, ref)
		default:
			return fmt.Errorf("visible calculation %q is not supported for %T", calculation.ID, spec.Value)
		}
	}
	base.DataBudget.RequiredCompleteness = visualizationir.VisualizationCompletenessPartial
	return nil
}

func canonicalCalculationDataType(template visualizationir.VisualizationCalculationTemplate, source visualizationir.VisualizationDataType) visualizationir.VisualizationDataType {
	switch template {
	case visualizationir.VisualizationCalculationTemplateRank:
		return visualizationir.VisualizationDataTypeInteger
	case visualizationir.VisualizationCalculationTemplateRunningTotal,
		visualizationir.VisualizationCalculationTemplateDifference:
		if source == visualizationir.VisualizationDataTypeInteger {
			return visualizationir.VisualizationDataTypeDecimal
		}
		return source
	case visualizationir.VisualizationCalculationTemplateMovingAverage,
		visualizationir.VisualizationCalculationTemplatePercentageDifference,
		visualizationir.VisualizationCalculationTemplatePercentOfParent,
		visualizationir.VisualizationCalculationTemplatePercentOfGrandTotal,
		visualizationir.VisualizationCalculationTemplateCumulativeContribution:
		return visualizationir.VisualizationDataTypeDecimal
	default:
		return source
	}
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
		result = append(result, compiled)
	}
	return result, nil
}
