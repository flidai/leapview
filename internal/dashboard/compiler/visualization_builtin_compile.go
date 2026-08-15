package compiler

import (
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

// compileBuiltInVisualizationSpec is the canonical authoring-to-IR boundary
// for first-party charts. Runtime data never participates in specification
// construction; it is shaped later against the immutable dataset schema.
func compileBuiltInVisualizationSpec(id string, authored dashboardauthoring.Visual, model *semanticmodel.Model) (visualizationir.VisualizationSpec, error) {
	shape := authored.ResultShape()
	columns := compiledShapeColumns(shape)
	if shape == "point" {
		for _, binding := range compiledVisualFields(authored.Query) {
			columns = append(columns, binding.Alias)
		}
	}
	if shape == "hierarchy" {
		seen := map[string]struct{}{"node": {}, "parent": {}, "value": {}}
		for _, binding := range compiledFields(authored.Query.Dimensions) {
			if _, exists := seen[binding.Alias]; exists {
				return visualizationir.VisualizationSpec{}, fmt.Errorf("hierarchy query alias %q conflicts with a reserved frame field", binding.Alias)
			}
			seen[binding.Alias] = struct{}{}
			columns = append(columns, binding.Alias)
		}
		if binding := compiledTime(authored.Query.Time); binding != nil {
			if _, exists := seen[binding.Alias]; exists {
				return visualizationir.VisualizationSpec{}, fmt.Errorf("hierarchy query alias %q conflicts with a reserved frame field", binding.Alias)
			}
			columns = append(columns, binding.Alias)
		}
	}
	fields := make([]visualizationir.VisualizationField, len(columns))
	identities := map[string]struct{}{}
	for _, identity := range authored.Point.Identity {
		identities[identity] = struct{}{}
	}
	for _, mapping := range authored.Interaction.PointSelection.Mappings {
		identities[mapping.Value] = struct{}{}
	}
	pointMeasures := map[string]struct{}{}
	for _, measure := range compiledFields(authored.Query.Measures) {
		pointMeasures[measure.Alias] = struct{}{}
	}
	pointTime := ""
	if value := compiledTime(authored.Query.Time); value != nil {
		pointTime = value.Alias
	}
	for index, column := range columns {
		role := visualizationir.VisualizationFieldRoleDimension
		if compiledShapeMeasure(column) || shape == "point" && containsStringKey(pointMeasures, column) {
			role = visualizationir.VisualizationFieldRoleMeasure
		}
		if _, ok := identities[column]; ok {
			role = visualizationir.VisualizationFieldRoleIdentity
		}
		dataType := compiledShapeDataType(column)
		if shape == "point" && containsStringKey(pointMeasures, column) {
			dataType = visualizationir.VisualizationDataTypeDecimal
		}
		if shape == "point" && column == pointTime {
			dataType = visualizationir.VisualizationDataTypeTemporal
		}
		fields[index] = visualizationir.VisualizationField{ID: column, Role: role, DataType: dataType, Nullable: true, Label: compiledShapeLabel(column)}
	}
	applyBuiltInFieldSemantics(fields, shape, authored, model)
	title := compiledVisualTitle(authored, id, model)
	accessibilityTitle := title
	if authored.Accessibility.Title != "" {
		accessibilityTitle = authored.Accessibility.Title
	}
	accessibilityDescription := title
	if authored.Description != "" {
		accessibilityDescription = authored.Description
	}
	if authored.Accessibility.Description != "" {
		accessibilityDescription = authored.Accessibility.Description
	}
	completeness := visualizationir.VisualizationCompletenessComplete
	if authored.DataBudget.RequiredCompleteness != "" {
		completeness = visualizationir.VisualizationCompleteness(authored.DataBudget.RequiredCompleteness)
	}
	accessibility := visualizationir.VisualizationAccessibility{Title: accessibilityTitle, Description: accessibilityDescription}
	if authored.Accessibility.Summary != "" {
		accessibility.Summary = &authored.Accessibility.Summary
	}
	if authored.Accessibility.AnnounceChanges {
		accessibility.AnnounceChanges = &authored.Accessibility.AnnounceChanges
	}
	contextSchemas, err := compileContextDatasetSchemas(authored, model)
	if err != nil {
		return visualizationir.VisualizationSpec{}, err
	}
	datasets := append([]visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: fields}}, contextSchemas...)
	metadataBindings, err := compileMetadataBindings(authored.Metadata, datasets)
	if err != nil {
		return visualizationir.VisualizationSpec{}, err
	}
	base := visualizationir.VisualizationSpecBase{
		Title: title, Subtitle: optionalString(authored.Subtitle), Datasets: datasets, MetadataBindings: metadataBindings,
		DataBudget:    visualizationir.VisualizationDataBudget{MaxRows: compiledVisualDataBudgetMaxRows(authored, shape), RequiredCompleteness: completeness},
		Accessibility: accessibility, Interactions: compiledSelectionInteractions("point_selection", authored.Interaction.PointSelection),
	}
	conditionalFormatting, err := compileConditionalFormatting(columns, authored.Presentation.ConditionalFormatting)
	if err != nil {
		return visualizationir.VisualizationSpec{}, err
	}
	base.ConditionalFormatting = conditionalFormatting
	ref := func(field string) visualizationir.VisualizationFieldRef {
		return visualizationir.VisualizationFieldRef{Dataset: "primary", Field: field}
	}
	optionalRef := func(field string) *visualizationir.VisualizationFieldRef {
		for _, column := range columns {
			if column == field {
				value := ref(field)
				return &value
			}
		}
		return nil
	}
	presentation := authored.Presentation
	common := visualizationir.VisualizationPresentation{Legend: compiledLegend(presentation.Legend), LabelPolicy: compiledLabelPolicy(presentation, authored.Type), DisplayUnits: compiledDisplayUnits(presentation.DisplayUnits)}

	switch authored.Type {
	case "kpi":
		base.Kind = "kpi"
		kpi, err := compileKPIConfiguration(authored.KPI, datasets)
		if err != nil {
			return visualizationir.VisualizationSpec{}, err
		}
		return visualizationir.VisualizationSpec{Value: &visualizationir.KPIVisualizationSpec{
			VisualizationSpecBase: base, Kind: "kpi", Value: ref("value"),
			Comparison: kpi.comparison, Goal: kpi.goal, Trend: kpi.trend,
			Presentation: visualizationir.KPIVisualizationPresentation{
				Mode: kpi.mode, Delta: kpi.delta, FavorableDirection: kpi.favorableDirection,
				MissingComparison: kpi.missingComparison, Ranges: kpi.ranges,
				DisplayUnits: compiledDisplayUnits(presentation.DisplayUnits),
				Note:         optionalString(presentation.Note), Tone: compiledTone(presentation.Tone), Thresholds: compiledThresholds(presentation.Thresholds),
			},
		}}, nil
	case "pie", "donut", "funnel":
		base.Kind = "proportional"
		return visualizationir.VisualizationSpec{Value: &visualizationir.ProportionalVisualizationSpec{
			VisualizationSpecBase: base, Kind: "proportional", Mark: visualizationir.VisualizationProportionalMark(authored.Type), Category: ref("label"), Value: ref("value"), Series: optionalRef("series"),
			Presentation: visualizationir.ProportionalVisualizationPresentation{VisualizationPresentation: common, Orientation: compiledOrientation(presentation.Orientation), Rose: presentation.Rose, CenterLabel: optionalString(presentation.CenterLabel), LabelPosition: compiledLabelPosition(presentation.LabelPosition), InnerRadius: optionalPositiveFloat(presentation.InnerRadius), OuterRadius: optionalPositiveFloat(presentation.OuterRadius), Align: optionalString(presentation.Align), Sort: compiledSortDirection(presentation.Sort)},
		}}, nil
	case "treemap", "sunburst", "tree", "sankey", "graph":
		base.Kind = "hierarchy"
		return visualizationir.VisualizationSpec{Value: &visualizationir.HierarchyVisualizationSpec{
			VisualizationSpecBase: base, Kind: "hierarchy", Mark: visualizationir.VisualizationHierarchyMark(authored.Type), Node: ref(firstCompiledField(columns, "node", "source", "label")), Value: optionalRef("value"), Parent: optionalRef("parent"), Source: optionalRef("source"), Target: optionalRef("target"),
			Presentation: visualizationir.HierarchyVisualizationPresentation{VisualizationPresentation: common, Orientation: compiledOrientation(presentation.Orientation), InitialDepth: optionalPositiveInt32(presentation.InitialDepth), Roam: presentation.Roam, Layout: compiledHierarchyLayout(presentation.Layout), Breadcrumb: presentation.Breadcrumb, NodeGap: optionalPositiveFloat(presentation.NodeGap), Curveness: optionalPositiveFloat(presentation.Curveness), Focus: compiledGraphFocus(presentation.Focus)},
		}}, nil
	case "radar", "gauge":
		base.Kind = "polar"
		return visualizationir.VisualizationSpec{Value: &visualizationir.PolarVisualizationSpec{
			VisualizationSpecBase: base, Kind: "polar", Mark: visualizationir.VisualizationPolarMark(authored.Type), Category: optionalRef("label"), Value: ref("value"), Series: optionalRef("series"),
			Presentation: visualizationir.PolarVisualizationPresentation{VisualizationPresentation: common, Minimum: presentation.Minimum, Maximum: presentation.Maximum, Target: presentation.Target, ShowPointer: true, Area: presentation.Area, ProgressWidth: optionalPositiveFloat(presentation.ProgressWidth), Thresholds: compiledThresholds(presentation.Thresholds)},
		}}, nil
	case "scatter":
		base.Kind = "point"
		decisionContext, err := compileCartesianDecisionContext(datasets, presentation)
		if err != nil {
			return visualizationir.VisualizationSpec{}, err
		}
		identity := make([]visualizationir.VisualizationFieldRef, 0, len(authored.Point.Identity))
		for _, field := range authored.Point.Identity {
			identity = append(identity, ref(field))
		}
		tooltip := make([]visualizationir.VisualizationFieldRef, 0, len(authored.Point.Tooltip))
		for _, field := range authored.Point.Tooltip {
			tooltip = append(tooltip, ref(field))
		}
		var tooltipRef *[]visualizationir.VisualizationFieldRef
		if len(tooltip) > 0 {
			tooltipRef = &tooltip
		}
		var colorScale *visualizationir.PointVisualizationColorScale
		if authored.Point.Color != "" {
			kind := authored.Point.ColorScale.Kind
			if kind == "" {
				kind = "categorical"
				if containsStringKey(pointMeasures, authored.Point.Color) {
					kind = "quantitative"
				}
			}
			colorScale = &visualizationir.PointVisualizationColorScale{
				Kind: visualizationir.VisualizationPointColorScaleKind(kind), Minimum: authored.Point.ColorScale.Minimum,
				Maximum: authored.Point.ColorScale.Maximum, Scheme: optionalString(authored.Point.ColorScale.Scheme),
			}
		}
		var sizeScale *visualizationir.PointVisualizationSizeScale
		if authored.Point.Size != "" {
			minimumPixels := authored.Point.SizeScale.MinimumPixels
			if minimumPixels <= 0 {
				minimumPixels = 8
			}
			maximumPixels := authored.Point.SizeScale.MaximumPixels
			if maximumPixels <= 0 {
				maximumPixels = 40
			}
			sizeScale = &visualizationir.PointVisualizationSizeScale{
				Minimum: authored.Point.SizeScale.Minimum, Maximum: authored.Point.SizeScale.Maximum,
				MinimumPixels: minimumPixels, MaximumPixels: maximumPixels,
			}
		}
		overplot := authored.Point.Overplot.Strategy
		if overplot == "" {
			overplot = "opacity"
		}
		opacity := authored.Point.Overplot.Opacity
		if opacity <= 0 {
			opacity = 0.7
		}
		largeMode := authored.Point.Overplot.LargeMode
		if largeMode == "" {
			largeMode = "automatic"
		}
		largeThreshold := authored.Point.Overplot.LargeThreshold
		if largeThreshold <= 0 {
			largeThreshold = 2_000
		}
		brush := make([]visualizationir.VisualizationPointBrushGesture, 0, len(authored.Point.Brush))
		for _, gesture := range authored.Point.Brush {
			brush = append(brush, visualizationir.VisualizationPointBrushGesture(gesture))
		}
		return visualizationir.VisualizationSpec{Value: &visualizationir.PointVisualizationSpec{
			VisualizationSpecBase: base, Kind: "point", Identity: identity, X: ref(authored.Point.X), Y: ref(authored.Point.Y),
			Size: optionalRef(authored.Point.Size), Color: optionalRef(authored.Point.Color), Series: optionalRef(authored.Point.Series),
			Label: optionalRef(authored.Point.Label), Tooltip: tooltipRef, ColorScale: colorScale, SizeScale: sizeScale,
			Axes: decisionContext.axes, ReferenceLines: decisionContext.referenceLines, ReferenceBands: decisionContext.referenceBands,
			EventAnnotations: decisionContext.eventAnnotations,
			Presentation: visualizationir.PointVisualizationPresentation{
				VisualizationPresentation: common, Overplot: visualizationir.VisualizationPointOverplotStrategy(overplot), Opacity: opacity,
				LargeMode: visualizationir.VisualizationPointLargeMode(largeMode), LargeThreshold: int64(largeThreshold), Brush: brush,
			},
		}}, nil
	default:
		mark := visualizationir.VisualizationCartesianMark(authored.Type)
		supported := map[visualizationir.VisualizationCartesianMark]bool{
			visualizationir.VisualizationCartesianMarkLine: true, visualizationir.VisualizationCartesianMarkArea: true, visualizationir.VisualizationCartesianMarkBar: true,
			visualizationir.VisualizationCartesianMarkColumn: true, visualizationir.VisualizationCartesianMarkHistogram: true,
			visualizationir.VisualizationCartesianMarkCombo: true, visualizationir.VisualizationCartesianMarkWaterfall: true, visualizationir.VisualizationCartesianMarkCandlestick: true,
			visualizationir.VisualizationCartesianMarkBoxplot: true, visualizationir.VisualizationCartesianMarkHeatmap: true,
		}
		if !supported[mark] {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("unsupported visualization type %q", authored.Type)
		}
		base.Kind = "cartesian"
		xField := firstCompiledField(columns, "label", "row", "name")
		y := make([]visualizationir.VisualizationFieldRef, 0, len(columns))
		for _, column := range columns {
			if column != xField && column != "series" && column != "selected" && column != "positive" {
				y = append(y, ref(column))
			}
		}
		if len(y) == 0 {
			y = append(y, ref("value"))
		}
		showSymbols := true
		if presentation.ShowSymbols != nil {
			showSymbols = *presentation.ShowSymbols
		}
		area := authored.Type == "area"
		if presentation.Area != nil && *presentation.Area {
			area = true
		}
		decisionContext, err := compileCartesianDecisionContext(datasets, presentation)
		if err != nil {
			return visualizationir.VisualizationSpec{}, err
		}
		return visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{
			VisualizationSpecBase: base, Kind: "cartesian", Mark: mark, X: ref(xField), Y: y, Series: optionalRef("series"),
			Axes: decisionContext.axes, ReferenceLines: decisionContext.referenceLines, ReferenceBands: decisionContext.referenceBands,
			EventAnnotations: decisionContext.eventAnnotations, Tooltip: decisionContext.tooltip,
			Presentation: visualizationir.CartesianVisualizationPresentation{
				VisualizationPresentation: common, Smooth: presentation.Smooth, Stacked: presentation.Stacked, ShowSymbols: showSymbols,
				DataZoom: presentation.DataZoom, Area: area, Step: presentation.Step, Orientation: compiledOptionalOrientation(presentation.Orientation),
				LabelPosition: compiledLabelPosition(presentation.LabelPosition), SymbolSize: optionalPositiveFloat(presentation.SymbolSize),
				HistogramBins: optionalPositiveInt32(presentation.HistogramBins), ComboSeries: compiledComboSeries(presentation.SeriesTypes, presentation.DualAxis),
				Stacking: compiledStackingMode(presentation), SeriesIntent: compiledSeriesIntent(presentation.SeriesOrder, presentation.SeriesColors),
			},
		}}, nil
	}
}

func containsStringKey(values map[string]struct{}, candidate string) bool {
	_, ok := values[candidate]
	return ok
}

func compileContextDatasetSchemas(authored dashboardauthoring.Visual, model *semanticmodel.Model) ([]visualizationir.VisualizationDatasetSchema, error) {
	datasetIDs := sortedMapKeys(authored.Datasets)
	out := make([]visualizationir.VisualizationDatasetSchema, 0, len(datasetIDs))
	for _, datasetID := range datasetIDs {
		query := authored.Datasets[datasetID]
		bindings := compiledVisualFields(query)
		fields := make([]visualizationir.VisualizationField, 0, len(bindings))
		seen := make(map[string]struct{}, len(bindings))
		dimensions := make(map[string]struct{}, len(query.Dimensions)+2)
		for _, binding := range compiledFields(query.Dimensions) {
			dimensions[binding.Alias] = struct{}{}
		}
		if binding := compiledOptionalField(query.Series); binding != nil {
			dimensions[binding.Alias] = struct{}{}
		}
		if binding := compiledTime(query.Time); binding != nil {
			dimensions[binding.Alias] = struct{}{}
		}
		for _, binding := range bindings {
			if _, exists := seen[binding.Alias]; exists {
				return nil, fmt.Errorf("context dataset %q uses duplicate alias %q", datasetID, binding.Alias)
			}
			seen[binding.Alias] = struct{}{}
			role := visualizationir.VisualizationFieldRoleMeasure
			dataType := visualizationir.VisualizationDataTypeDecimal
			if _, ok := dimensions[binding.Alias]; ok {
				role = visualizationir.VisualizationFieldRoleDimension
				dataType = visualizationir.VisualizationDataTypeString
			}
			field := visualizationir.VisualizationField{
				ID: binding.Alias, Role: role, DataType: dataType, Nullable: true, Label: binding.Alias,
			}
			if model != nil {
				applySemanticField(&field, binding.FieldID, model)
			} else {
				source := binding.FieldID
				field.SourceRef = &source
			}
			fields = append(fields, field)
		}
		out = append(out, visualizationir.VisualizationDatasetSchema{ID: datasetID, Fields: fields})
	}
	return out, nil
}

func compileMetadataBindings(authored dashboardauthoring.VisualMetadataBindings, datasets []visualizationir.VisualizationDatasetSchema) (*visualizationir.VisualizationMetadataBindings, error) {
	compile := func(name string, binding *dashboardauthoring.VisualTextBinding) (*visualizationir.VisualizationTextBinding, error) {
		if binding == nil {
			return nil, nil
		}
		datasetID := binding.Dataset
		if datasetID == "" {
			datasetID = "primary"
		}
		if !compiledDatasetContainsField(datasets, datasetID, binding.Field) {
			return nil, fmt.Errorf("%s metadata binding field %q is not in dataset %q", name, binding.Field, datasetID)
		}
		reducer := binding.Reducer
		if reducer == "" {
			reducer = "first"
		}
		return &visualizationir.VisualizationTextBinding{
			Field:   visualizationir.VisualizationFieldRef{Dataset: datasetID, Field: binding.Field},
			Reducer: visualizationir.VisualizationReferenceReducer(reducer), Prefix: binding.Prefix, Suffix: binding.Suffix, Fallback: binding.Fallback,
		}, nil
	}
	title, err := compile("title", authored.Title)
	if err != nil {
		return nil, err
	}
	subtitle, err := compile("subtitle", authored.Subtitle)
	if err != nil {
		return nil, err
	}
	description, err := compile("description", authored.Description)
	if err != nil {
		return nil, err
	}
	summary, err := compile("summary", authored.Summary)
	if err != nil {
		return nil, err
	}
	if title == nil && subtitle == nil && description == nil && summary == nil {
		return nil, nil
	}
	return &visualizationir.VisualizationMetadataBindings{Title: title, Subtitle: subtitle, Description: description, Summary: summary}, nil
}

func compiledDatasetContainsField(datasets []visualizationir.VisualizationDatasetSchema, datasetID, fieldID string) bool {
	for _, dataset := range datasets {
		if dataset.ID != datasetID {
			continue
		}
		for _, field := range dataset.Fields {
			if field.ID == fieldID {
				return true
			}
		}
	}
	return false
}

type compiledKPIConfigurationValue struct {
	mode               visualizationir.VisualizationKPIMode
	comparison         *visualizationir.VisualizationKPIValueBinding
	goal               *visualizationir.VisualizationKPIValueBinding
	trend              *visualizationir.VisualizationKPITrendBinding
	delta              visualizationir.VisualizationKPIDeltaMode
	favorableDirection visualizationir.VisualizationKPIDirection
	missingComparison  visualizationir.VisualizationKPIMissingComparison
	ranges             []visualizationir.VisualizationKPIQualitativeRange
}

func compileKPIConfiguration(authored dashboardauthoring.VisualKPI, datasets []visualizationir.VisualizationDatasetSchema) (compiledKPIConfigurationValue, error) {
	out := compiledKPIConfigurationValue{
		mode:               visualizationir.VisualizationKPIModeCompact,
		delta:              visualizationir.VisualizationKPIDeltaModeAbsolute,
		favorableDirection: visualizationir.VisualizationKPIDirectionNeutral,
		missingComparison:  visualizationir.VisualizationKPIMissingComparisonShowUnavailable,
		ranges:             []visualizationir.VisualizationKPIQualitativeRange{},
	}
	if authored.Mode != "" {
		out.mode = visualizationir.VisualizationKPIMode(authored.Mode)
	}
	if authored.Delta != "" {
		out.delta = visualizationir.VisualizationKPIDeltaMode(authored.Delta)
	}
	if authored.FavorableDirection != "" {
		out.favorableDirection = visualizationir.VisualizationKPIDirection(authored.FavorableDirection)
	}
	if authored.MissingComparison != "" {
		out.missingComparison = visualizationir.VisualizationKPIMissingComparison(authored.MissingComparison)
	}
	compileValue := func(name, fallbackLabel string, authored *dashboardauthoring.VisualKPIValueBinding) (*visualizationir.VisualizationKPIValueBinding, error) {
		if authored == nil {
			return nil, nil
		}
		datasetID := authored.Dataset
		if datasetID == "" {
			datasetID = "primary"
		}
		if !compiledDatasetContainsField(datasets, datasetID, authored.Field) {
			return nil, fmt.Errorf("KPI %s field %q is not in dataset %q", name, authored.Field, datasetID)
		}
		reducer := authored.Reducer
		if reducer == "" {
			reducer = "first"
		}
		label := strings.TrimSpace(authored.Label)
		if label == "" {
			label = fallbackLabel
		}
		return &visualizationir.VisualizationKPIValueBinding{
			Field:   visualizationir.VisualizationFieldRef{Dataset: datasetID, Field: authored.Field},
			Reducer: visualizationir.VisualizationReferenceReducer(reducer), Label: label,
		}, nil
	}
	var err error
	if out.comparison, err = compileValue("comparison", "Comparison", authored.Comparison); err != nil {
		return out, err
	}
	if out.goal, err = compileValue("goal", "Target", authored.Goal); err != nil {
		return out, err
	}
	if authored.Trend != nil {
		if !compiledDatasetContainsField(datasets, authored.Trend.Dataset, authored.Trend.Category) {
			return out, fmt.Errorf("KPI trend category field %q is not in dataset %q", authored.Trend.Category, authored.Trend.Dataset)
		}
		if !compiledDatasetContainsField(datasets, authored.Trend.Dataset, authored.Trend.Value) {
			return out, fmt.Errorf("KPI trend value field %q is not in dataset %q", authored.Trend.Value, authored.Trend.Dataset)
		}
		out.trend = &visualizationir.VisualizationKPITrendBinding{
			Category: visualizationir.VisualizationFieldRef{Dataset: authored.Trend.Dataset, Field: authored.Trend.Category},
			Value:    visualizationir.VisualizationFieldRef{Dataset: authored.Trend.Dataset, Field: authored.Trend.Value},
		}
	}
	out.ranges = make([]visualizationir.VisualizationKPIQualitativeRange, len(authored.Ranges))
	for index, valueRange := range authored.Ranges {
		out.ranges[index] = visualizationir.VisualizationKPIQualitativeRange{
			Minimum: valueRange.Minimum, Maximum: valueRange.Maximum, Label: valueRange.Label,
			Tone: visualizationir.VisualizationTone(valueRange.Tone),
		}
	}
	return out, nil
}

func compileConditionalFormatting(columns []string, authored []dashboardauthoring.VisualConditionalFormat) (*[]visualizationir.VisualizationConditionalFormat, error) {
	if len(authored) == 0 {
		return nil, nil
	}
	fieldRef := func(field string) (visualizationir.VisualizationFieldRef, error) {
		if !containsCompiledColumn(columns, field) {
			return visualizationir.VisualizationFieldRef{}, fmt.Errorf("conditional formatting field %q is not in the compiled result", field)
		}
		return visualizationir.VisualizationFieldRef{Dataset: "primary", Field: field}, nil
	}
	out := make([]visualizationir.VisualizationConditionalFormat, len(authored))
	for index, format := range authored {
		target, err := fieldRef(format.Field)
		if err != nil {
			return nil, err
		}
		compiled := visualizationir.VisualizationConditionalFormat{
			ID: format.ID, Target: visualizationir.VisualizationConditionalTarget(format.Target), Field: target,
		}
		switch format.Kind {
		case "gradient":
			if format.Minimum == nil || format.Maximum == nil {
				return nil, fmt.Errorf("conditional formatting %q gradient requires minimum and maximum", format.ID)
			}
			compiled.Rule.Value = &visualizationir.GradientVisualizationConditionalRule{
				VisualizationConditionalRuleBase: visualizationir.VisualizationConditionalRuleBase{Kind: "gradient"},
				Kind:                             "gradient", Minimum: *format.Minimum, Maximum: *format.Maximum,
				Low: compiledConditionalStyle(format.Low), High: compiledConditionalStyle(format.High), NullStyle: compiledConditionalStyle(format.Null),
			}
		case "rules":
			rules := make([]visualizationir.VisualizationConditionalThreshold, len(format.Rules))
			for ruleIndex, rule := range format.Rules {
				rules[ruleIndex] = visualizationir.VisualizationConditionalThreshold{
					Operator: visualizationir.VisualizationComparisonOperator(rule.Operator),
					Value:    rule.Value,
					Style:    compiledConditionalStyle(rule.Style),
				}
			}
			compiled.Rule.Value = &visualizationir.RulesVisualizationConditionalRule{
				VisualizationConditionalRuleBase: visualizationir.VisualizationConditionalRuleBase{Kind: "rules"},
				Kind:                             "rules", Rules: rules, NullStyle: compiledConditionalStyle(format.Null), DefaultStyle: compiledConditionalStyle(format.Default),
			}
		case "field":
			source, err := fieldRef(format.SourceField)
			if err != nil {
				return nil, err
			}
			values := make(map[string]visualizationir.VisualizationConditionalStyle, len(format.Values))
			for value, style := range format.Values {
				values[value] = compiledConditionalStyle(style)
			}
			compiled.Rule.Value = &visualizationir.FieldVisualizationConditionalRule{
				VisualizationConditionalRuleBase: visualizationir.VisualizationConditionalRuleBase{Kind: "field"},
				Kind:                             "field", Source: source, Values: values, NullStyle: compiledConditionalStyle(format.Null), DefaultStyle: compiledConditionalStyle(format.Default),
			}
		default:
			return nil, fmt.Errorf("conditional formatting %q has unsupported kind %q", format.ID, format.Kind)
		}
		out[index] = compiled
	}
	return &out, nil
}

func compiledConditionalStyle(authored dashboardauthoring.VisualConditionalStyle) visualizationir.VisualizationConditionalStyle {
	style := visualizationir.VisualizationConditionalStyle{}
	if authored.Color != "" {
		color := visualizationir.VisualizationColorIntent(authored.Color)
		style.Color = &color
	}
	if authored.Icon != "" {
		icon := visualizationir.VisualizationIconIntent(authored.Icon)
		style.Icon = &icon
	}
	return style
}

type compiledCartesianDecisionContextValue struct {
	axes             *[]visualizationir.VisualizationAxisConfiguration
	referenceLines   *[]visualizationir.VisualizationReferenceLine
	referenceBands   *[]visualizationir.VisualizationReferenceBand
	eventAnnotations *[]visualizationir.VisualizationEventAnnotation
	tooltip          *[]visualizationir.VisualizationFieldRef
}

func compileCartesianDecisionContext(datasets []visualizationir.VisualizationDatasetSchema, presentation dashboardauthoring.VisualPresentation) (compiledCartesianDecisionContextValue, error) {
	var result compiledCartesianDecisionContextValue
	if len(presentation.Axes) > 0 {
		values := make([]visualizationir.VisualizationAxisConfiguration, len(presentation.Axes))
		for index, authored := range presentation.Axes {
			values[index] = visualizationir.VisualizationAxisConfiguration{
				ID:           visualizationir.VisualizationCartesianAxis(authored.ID),
				Title:        optionalString(authored.Title),
				Scale:        compiledAxisScale(authored.Scale),
				Zero:         compiledAxisZeroPolicy(authored.Zero),
				Minimum:      authored.Minimum,
				Maximum:      authored.Maximum,
				Unit:         optionalString(authored.Unit),
				DisplayUnits: optionalAuthoredDisplayUnits(authored.DisplayUnits),
				TickDensity:  compiledAxisTickDensity(authored.TickDensity),
			}
		}
		result.axes = &values
	}
	compileValue := func(authored dashboardauthoring.VisualReferenceValue) (visualizationir.VisualizationReferenceValue, error) {
		switch {
		case authored.Number != nil:
			return visualizationir.VisualizationReferenceValue{Value: &visualizationir.NumberVisualizationReferenceValue{
				VisualizationReferenceValueBase: visualizationir.VisualizationReferenceValueBase{Kind: "number"},
				Kind:                            "number",
				Value:                           *authored.Number,
			}}, nil
		case authored.Text != "":
			return visualizationir.VisualizationReferenceValue{Value: &visualizationir.TextVisualizationReferenceValue{
				VisualizationReferenceValueBase: visualizationir.VisualizationReferenceValueBase{Kind: "text"},
				Kind:                            "text",
				Value:                           authored.Text,
			}}, nil
		case authored.Field != "":
			datasetID := authored.Dataset
			if datasetID == "" {
				datasetID = "primary"
			}
			if !compiledDatasetContainsField(datasets, datasetID, authored.Field) {
				if datasetID == "primary" {
					return visualizationir.VisualizationReferenceValue{}, fmt.Errorf("reference field %q is not in the compiled result", authored.Field)
				}
				return visualizationir.VisualizationReferenceValue{}, fmt.Errorf("reference field %q is not in compiled dataset %q", authored.Field, datasetID)
			}
			reducer := authored.Reducer
			if reducer == "" {
				reducer = "first"
			}
			return visualizationir.VisualizationReferenceValue{Value: &visualizationir.FieldVisualizationReferenceValue{
				VisualizationReferenceValueBase: visualizationir.VisualizationReferenceValueBase{Kind: "field"},
				Kind:                            "field",
				Field:                           visualizationir.VisualizationFieldRef{Dataset: datasetID, Field: authored.Field},
				Reducer:                         visualizationir.VisualizationReferenceReducer(reducer),
			}}, nil
		default:
			return visualizationir.VisualizationReferenceValue{}, fmt.Errorf("reference value requires number, text, or field")
		}
	}
	tone := func(value string) visualizationir.VisualizationTone {
		if value == "" {
			return visualizationir.VisualizationToneNeutral
		}
		return visualizationir.VisualizationTone(value)
	}
	if len(presentation.ReferenceLines) > 0 {
		values := make([]visualizationir.VisualizationReferenceLine, len(presentation.ReferenceLines))
		for index, authored := range presentation.ReferenceLines {
			value, err := compileValue(authored.Value)
			if err != nil {
				return result, fmt.Errorf("reference line %q: %w", authored.ID, err)
			}
			values[index] = visualizationir.VisualizationReferenceLine{
				ID: authored.ID, Axis: visualizationir.VisualizationCartesianAxis(authored.Axis), Value: value,
				Label: optionalString(authored.Label), Tone: tone(authored.Tone),
			}
		}
		result.referenceLines = &values
	}
	if len(presentation.ReferenceBands) > 0 {
		values := make([]visualizationir.VisualizationReferenceBand, len(presentation.ReferenceBands))
		for index, authored := range presentation.ReferenceBands {
			from, err := compileValue(authored.From)
			if err != nil {
				return result, fmt.Errorf("reference band %q from: %w", authored.ID, err)
			}
			to, err := compileValue(authored.To)
			if err != nil {
				return result, fmt.Errorf("reference band %q to: %w", authored.ID, err)
			}
			values[index] = visualizationir.VisualizationReferenceBand{
				ID: authored.ID, Axis: visualizationir.VisualizationCartesianAxis(authored.Axis), From: from, To: to,
				Label: optionalString(authored.Label), Tone: tone(authored.Tone),
			}
		}
		result.referenceBands = &values
	}
	if len(presentation.EventAnnotations) > 0 {
		values := make([]visualizationir.VisualizationEventAnnotation, len(presentation.EventAnnotations))
		for index, authored := range presentation.EventAnnotations {
			value, err := compileValue(authored.Value)
			if err != nil {
				return result, fmt.Errorf("event annotation %q: %w", authored.ID, err)
			}
			values[index] = visualizationir.VisualizationEventAnnotation{
				ID: authored.ID, Axis: visualizationir.VisualizationCartesianAxis(authored.Axis), Value: value,
				Label: authored.Label, Description: optionalString(authored.Description), Tone: tone(authored.Tone),
			}
		}
		result.eventAnnotations = &values
	}
	if len(presentation.Tooltip) > 0 {
		values := make([]visualizationir.VisualizationFieldRef, len(presentation.Tooltip))
		for index, field := range presentation.Tooltip {
			if !compiledDatasetContainsField(datasets, "primary", field) {
				return result, fmt.Errorf("tooltip field %q is not in the compiled result", field)
			}
			values[index] = visualizationir.VisualizationFieldRef{Dataset: "primary", Field: field}
		}
		result.tooltip = &values
	}
	return result, nil
}

func compiledAxisScale(value string) visualizationir.VisualizationAxisScale {
	if value == "" {
		return visualizationir.VisualizationAxisScaleAutomatic
	}
	return visualizationir.VisualizationAxisScale(value)
}

func compiledAxisZeroPolicy(value string) visualizationir.VisualizationAxisZeroPolicy {
	if value == "" {
		return visualizationir.VisualizationAxisZeroPolicyAutomatic
	}
	return visualizationir.VisualizationAxisZeroPolicy(value)
}

func compiledAxisTickDensity(value string) visualizationir.VisualizationAxisTickDensity {
	if value == "" {
		return visualizationir.VisualizationAxisTickDensityAutomatic
	}
	return visualizationir.VisualizationAxisTickDensity(value)
}

func compiledDisplayUnits(value string) *visualizationir.VisualizationDisplayUnits {
	if value == "" {
		value = "auto"
	}
	out := visualizationir.VisualizationDisplayUnits(value)
	return &out
}

func optionalAuthoredDisplayUnits(value string) *visualizationir.VisualizationDisplayUnits {
	if value == "" {
		return nil
	}
	out := visualizationir.VisualizationDisplayUnits(value)
	return &out
}

func compiledStackingMode(presentation dashboardauthoring.VisualPresentation) *visualizationir.VisualizationStackingMode {
	value := presentation.Stacking
	if value == "" {
		if presentation.Stacked {
			value = "normal"
		} else {
			value = "none"
		}
	}
	out := visualizationir.VisualizationStackingMode(value)
	return &out
}

func compiledSeriesIntent(order []string, colors map[string]string) *[]visualizationir.VisualizationSeriesIntent {
	if len(order) == 0 && len(colors) == 0 {
		return nil
	}
	out := make([]visualizationir.VisualizationSeriesIntent, 0, len(order)+len(colors))
	seen := make(map[string]struct{}, len(order))
	for index, value := range order {
		position := int32(index)
		item := visualizationir.VisualizationSeriesIntent{Value: value, Order: &position}
		if authored := colors[value]; authored != "" {
			color := visualizationir.VisualizationColorIntent(authored)
			item.Color = &color
		}
		out = append(out, item)
		seen[value] = struct{}{}
	}
	remaining := make([]string, 0, len(colors))
	for value := range colors {
		if _, exists := seen[value]; !exists {
			remaining = append(remaining, value)
		}
	}
	sort.Strings(remaining)
	for _, value := range remaining {
		color := visualizationir.VisualizationColorIntent(colors[value])
		out = append(out, visualizationir.VisualizationSeriesIntent{Value: value, Color: &color})
	}
	return &out
}

func containsCompiledColumn(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func compiledShapeColumns(shape string) []string {
	columns := map[string][]string{
		"single_value": {"label", "value", "series"}, "category_value": {"label", "value"}, "category_series_value": {"label", "series", "value"},
		"category_multi_measure": {"label", "series", "value"}, "category_delta": {"label", "value", "start", "end", "positive"},
		"binned_measure": {"label", "binStart", "binEnd", "value"}, "hierarchy": {"node", "parent", "value"}, "matrix": {"row", "column", "value"},
		"graph": {"source", "target", "value"}, "ohlc": {"label", "open", "close", "low", "high"}, "distribution": {"label", "min", "q1", "median", "q3", "max"},
	}[shape]
	return append([]string(nil), columns...)
}

func compiledShapeMeasure(field string) bool {
	switch field {
	case "value", "start", "end", "binStart", "binEnd", "open", "close", "low", "high", "min", "q1", "median", "q3", "max":
		return true
	default:
		return false
	}
}

func compiledShapeDataType(field string) visualizationir.VisualizationDataType {
	if compiledShapeMeasure(field) {
		return visualizationir.VisualizationDataTypeDecimal
	}
	if field == "positive" {
		return visualizationir.VisualizationDataTypeBoolean
	}
	return visualizationir.VisualizationDataTypeString
}

func compiledShapeLabel(field string) string {
	labels := map[string]string{"binStart": "Bin Start", "binEnd": "Bin End", "q1": "Q1", "q3": "Q3"}
	if label := labels[field]; label != "" {
		return label
	}
	if field == "" {
		return "Value"
	}
	return strings.ToUpper(field[:1]) + field[1:]
}

func firstCompiledField(columns []string, candidates ...string) string {
	for _, candidate := range candidates {
		for _, column := range columns {
			if candidate == column {
				return candidate
			}
		}
	}
	if len(columns) > 0 {
		return columns[0]
	}
	return "value"
}

func compiledLegend(value string) visualizationir.VisualizationLegendPosition {
	switch value {
	case "hidden":
		return visualizationir.VisualizationLegendPositionHidden
	case "top":
		return visualizationir.VisualizationLegendPositionTop
	case "right":
		return visualizationir.VisualizationLegendPositionRight
	case "left":
		return visualizationir.VisualizationLegendPositionLeft
	default:
		return visualizationir.VisualizationLegendPositionBottom
	}
}

func compiledLabelPolicy(presentation dashboardauthoring.VisualPresentation, visualType string) visualizationir.VisualizationLabelPolicy {
	policy := presentation.Labels
	density := visualizationir.VisualizationLabelDensity(policy.Density)
	if density == "" {
		if policy.IsZero() && !presentation.ShowLabels {
			if visualType == "radar" {
				density = visualizationir.VisualizationLabelDensityHidden
			} else {
				density = visualizationir.VisualizationLabelDensityAutomatic
			}
		} else {
			density = visualizationir.VisualizationLabelDensityAutomatic
		}
	}
	priority := make([]visualizationir.VisualizationLabelPriority, 0)
	if density != visualizationir.VisualizationLabelDensityHidden {
		priority = []visualizationir.VisualizationLabelPriority{
			visualizationir.VisualizationLabelPrioritySelected,
			visualizationir.VisualizationLabelPriorityAnomaly,
			visualizationir.VisualizationLabelPriorityThreshold,
		}
	}
	if policy.Priority != nil {
		priority = make([]visualizationir.VisualizationLabelPriority, 0, len(policy.Priority))
		for _, value := range policy.Priority {
			priority = append(priority, visualizationir.VisualizationLabelPriority(value))
		}
	}
	maxCharacters := int32(24)
	if policy.MaxCharacters != nil {
		maxCharacters = int32(*policy.MaxCharacters)
	}
	minimumSpacing := int32(6)
	switch density {
	case visualizationir.VisualizationLabelDensityHidden, visualizationir.VisualizationLabelDensityAlways:
		minimumSpacing = 0
	case visualizationir.VisualizationLabelDensityDense:
		minimumSpacing = 2
	}
	if policy.MinimumSpacing != nil {
		minimumSpacing = int32(*policy.MinimumSpacing)
	}
	tooltipFallback := true
	if policy.TooltipFallback != nil {
		tooltipFallback = *policy.TooltipFallback
	}
	return visualizationir.VisualizationLabelPolicy{
		Density: density, Priority: priority, MaxCharacters: maxCharacters,
		MinimumSpacing: minimumSpacing, TooltipFallback: tooltipFallback,
	}
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func optionalPositiveFloat(value float64) *float64 {
	if value <= 0 {
		return nil
	}
	return &value
}

func optionalPositiveInt32(value int) *int32 {
	if value <= 0 {
		return nil
	}
	out := int32(value)
	return &out
}

func compiledOrientation(value string) visualizationir.VisualizationOrientation {
	if value == "horizontal" {
		return visualizationir.VisualizationOrientationHorizontal
	}
	return visualizationir.VisualizationOrientationVertical
}

func compiledOptionalOrientation(value string) *visualizationir.VisualizationOrientation {
	if value == "" {
		return nil
	}
	out := compiledOrientation(value)
	return &out
}

func compiledLabelPosition(value string) *visualizationir.VisualizationLabelPosition {
	if value == "" {
		return nil
	}
	out := visualizationir.VisualizationLabelPosition(value)
	return &out
}

func compiledHierarchyLayout(value string) *visualizationir.VisualizationHierarchyLayout {
	if value == "" {
		return nil
	}
	out := visualizationir.VisualizationHierarchyLayout(value)
	return &out
}

func compiledGraphFocus(value string) *visualizationir.VisualizationGraphFocus {
	if value == "" {
		return nil
	}
	out := visualizationir.VisualizationGraphFocus(value)
	return &out
}

func compiledSortDirection(value string) *visualizationir.VisualizationSortDirection {
	if value == "" {
		return nil
	}
	out := visualizationir.VisualizationSortDirection(value)
	return &out
}

func compiledTone(value string) *visualizationir.VisualizationTone {
	if value == "" {
		return nil
	}
	out := visualizationir.VisualizationTone(value)
	return &out
}

func compiledThresholds(values []dashboardauthoring.VisualThreshold) *[]visualizationir.VisualizationThreshold {
	if len(values) == 0 {
		return nil
	}
	out := make([]visualizationir.VisualizationThreshold, len(values))
	for index, value := range values {
		out[index] = visualizationir.VisualizationThreshold{Value: value.Value, Tone: visualizationir.VisualizationTone(value.Tone)}
	}
	return &out
}

func compiledComboSeries(values map[string]string, dualAxis bool) *[]visualizationir.VisualizationComboSeries {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]visualizationir.VisualizationComboSeries, len(keys))
	for index, key := range keys {
		axis := visualizationir.VisualizationAxisPrimary
		if dualAxis && index > 0 {
			axis = visualizationir.VisualizationAxisSecondary
		}
		out[index] = visualizationir.VisualizationComboSeries{SeriesValue: key, Mark: visualizationir.VisualizationCartesianMark(values[key]), Axis: axis}
	}
	return &out
}

func applyCompiledSpecContract(spec *visualizationir.VisualizationSpec, authored dashboardauthoring.Visual) {
	base, err := spec.Base()
	if err != nil {
		return
	}
	base.DataBudget.MaxRows = compiledVisualLimit(authored)
	if authored.DataBudget.RequiredCompleteness != "" {
		base.DataBudget.RequiredCompleteness = visualizationir.VisualizationCompleteness(authored.DataBudget.RequiredCompleteness)
	}
	if authored.Accessibility.Title != "" {
		base.Accessibility.Title = authored.Accessibility.Title
	}
	if authored.Accessibility.Description != "" {
		base.Accessibility.Description = authored.Accessibility.Description
	}
	if authored.Accessibility.Summary != "" {
		base.Accessibility.Summary = &authored.Accessibility.Summary
	}
	if authored.Accessibility.AnnounceChanges {
		base.Accessibility.AnnounceChanges = &authored.Accessibility.AnnounceChanges
	}
}
