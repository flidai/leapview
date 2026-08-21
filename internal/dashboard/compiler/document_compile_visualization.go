package compiler

import (
	"fmt"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	"github.com/flidai/leapview/internal/dashboard/visualization/mapasset"
)

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
	if conversionErr = promoteSelectionIdentityFields(&base); conversionErr != nil {
		return visualizationir.VisualizationSpec{}, fmt.Errorf("selection identities: %w", conversionErr)
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
