package compiler

import (
	"fmt"
	"strings"

	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	visualizationgeometry "github.com/flidai/leapview/internal/dashboard/visualization/geometry"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationmapasset "github.com/flidai/leapview/internal/dashboard/visualization/mapasset"
)

func compileGeographicVisualizationSpec(authored dashboardauthoring.Visual) (visualizationir.VisualizationSpec, error) {
	tiled, err := geographicUsesTiledDelivery(authored)
	if err != nil {
		return visualizationir.VisualizationSpec{}, err
	}
	fields := geographicVisualizationFields(authored)
	known := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		known[field.ID] = struct{}{}
	}
	fieldRef := func(layerID, property, alias string) (*visualizationir.VisualizationFieldRef, error) {
		if alias == "" {
			return nil, nil
		}
		if _, ok := known[alias]; !ok {
			return nil, fmt.Errorf("geographic layer %q %s references unknown query alias %q", layerID, property, alias)
		}
		ref := visualizationir.VisualizationFieldRef{Dataset: "primary", Field: alias}
		return &ref, nil
	}
	layers := make([]visualizationir.VisualizationGeographicLayer, len(authored.Geo.Layers))
	for index, authoredLayer := range authored.Geo.Layers {
		layer, err := compileGeographicLayer(authoredLayer, fieldRef)
		if err != nil {
			return visualizationir.VisualizationSpec{}, err
		}
		layers[index] = layer
	}
	title := authored.Title
	if title == "" {
		title = "Map"
	}
	accessibilityTitle := authored.Accessibility.Title
	if accessibilityTitle == "" {
		accessibilityTitle = title
	}
	accessibilityDescription := authored.Accessibility.Description
	if accessibilityDescription == "" {
		accessibilityDescription = title
	}
	maxRows := compiledVisualLimit(authored)
	if tiled {
		maxRows = 0
	}
	base := visualizationir.VisualizationSpecBase{
		Kind: "geographic", Title: title, Datasets: []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: fields}},
		DataBudget:    visualizationir.VisualizationDataBudget{MaxRows: maxRows, RequiredCompleteness: visualizationir.VisualizationCompletenessComplete},
		Accessibility: visualizationir.VisualizationAccessibility{Title: accessibilityTitle, Description: accessibilityDescription},
		Interactions:  compiledSelectionInteractions("point_selection", authored.Interaction.PointSelection),
	}
	fieldIDs := make([]string, len(fields))
	for index, field := range fields {
		fieldIDs[index] = field.ID
	}
	conditionalFormatting, err := compileConditionalFormatting(fieldIDs, authored.Presentation.ConditionalFormatting)
	if err != nil {
		return visualizationir.VisualizationSpec{}, err
	}
	base.ConditionalFormatting = conditionalFormatting
	legend := visualizationir.VisualizationLegendPosition(authored.Presentation.Legend)
	if legend == "" {
		legend = visualizationir.VisualizationLegendPositionHidden
	}
	basemapID := strings.TrimSpace(authored.Geo.Basemap)
	if basemapID == "" {
		basemapID = "streets"
	}
	var basemap *visualizationir.VisualizationMapStyleAsset
	if basemapID != "blank" {
		asset, err := visualizationmapasset.Resolve(basemapID)
		if err != nil {
			return visualizationir.VisualizationSpec{}, fmt.Errorf("geographic basemap: %w", err)
		}
		basemap = &asset
	}
	theme := visualizationir.VisualizationMapTheme(authored.Geo.Theme)
	if theme == "" {
		theme = visualizationir.VisualizationMapThemeAuto
	}
	labelDensity := visualizationir.VisualizationMapLabelDensity(authored.Geo.LabelDensity)
	if labelDensity == "" {
		labelDensity = visualizationir.VisualizationMapLabelDensityNormal
	}
	camera := compileMapCamera(authored.Geo.Camera)
	controls := compileMapControls(authored.Geo.Controls)
	spatialInteractions := compiledSpatialSelectionInteractions(authored.Interaction.SpatialSelection)
	return visualizationir.VisualizationSpec{Value: &visualizationir.GeographicVisualizationSpec{
		VisualizationSpecBase: base, Kind: "geographic", Layers: layers, SpatialInteractions: spatialInteractions,
		Presentation: visualizationir.GeographicVisualizationPresentation{
			VisualizationPresentation: visualizationir.VisualizationPresentation{
				Legend: legend,
				LabelPolicy: visualizationir.VisualizationLabelPolicy{
					Density: visualizationir.VisualizationLabelDensityHidden, Priority: []visualizationir.VisualizationLabelPriority{},
					MaxCharacters: 24, MinimumSpacing: 0, TooltipFallback: true,
				},
			},
			Roam: true, Basemap: basemap, Theme: theme, LabelDensity: labelDensity, Camera: camera, Controls: controls,
		},
	}}, nil
}

func compiledSpatialSelectionInteractions(selection dashboardauthoring.SpatialSelectionInteraction) []visualizationir.VisualizationSpatialSelectionInteraction {
	if selection.IsZero() {
		return []visualizationir.VisualizationSpatialSelectionInteraction{}
	}
	gestures := make([]visualizationir.VisualizationSpatialSelectionGesture, len(selection.Gestures))
	for index, gesture := range selection.Gestures {
		gestures[index] = visualizationir.VisualizationSpatialSelectionGesture(gesture)
	}
	mapping := func(value dashboardauthoring.SpatialSelectionMapping) visualizationir.VisualizationSpatialFieldMapping {
		return visualizationir.VisualizationSpatialFieldMapping{
			Source:        visualizationir.VisualizationFieldRef{Dataset: "primary", Field: value.Source},
			TargetFieldID: value.Field, TargetFactID: optionalString(value.Fact),
		}
	}
	return []visualizationir.VisualizationSpatialSelectionInteraction{{
		ID: "spatial_selection", Gestures: gestures, Latitude: mapping(selection.Latitude), Longitude: mapping(selection.Longitude),
		Targets: compiledInteractionTargets(selection.Targets, selection.HighlightTargets, selection.NoneTargets),
	}}
}

type geographicFieldResolver func(layerID, property, alias string) (*visualizationir.VisualizationFieldRef, error)

func compileGeographicLayer(authored dashboardauthoring.VisualGeoLayer, fieldRef geographicFieldResolver) (visualizationir.VisualizationGeographicLayer, error) {
	ref := func(property, alias string) (*visualizationir.VisualizationFieldRef, error) {
		return fieldRef(authored.ID, property, alias)
	}
	value, err := ref("value", authored.Value)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	category, err := ref("category", authored.Category)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	label, err := ref("label", authored.Label)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	tooltip := make([]visualizationir.VisualizationFieldRef, 0, len(authored.Tooltip))
	for _, alias := range authored.Tooltip {
		field, err := ref("tooltip", alias)
		if err != nil {
			return visualizationir.VisualizationGeographicLayer{}, err
		}
		if field != nil {
			tooltip = append(tooltip, *field)
		}
	}
	base := visualizationir.VisualizationGeographicLayerBase{
		ID: authored.ID, Kind: authored.Kind, Label: label, Tooltip: tooltip,
		Position: mapLayerPosition(authored.Position), Visibility: mapVisibility(authored.Visibility),
	}
	color := mapColorScale(authored.Color)
	stroke := mapStroke(authored.Stroke)
	opacity := authored.Opacity
	if opacity == 0 {
		opacity = 0.82
	}
	coordinates := func() (visualizationir.VisualizationFieldRef, visualizationir.VisualizationFieldRef, error) {
		latitude, err := ref("latitude", authored.Latitude)
		if err != nil {
			return visualizationir.VisualizationFieldRef{}, visualizationir.VisualizationFieldRef{}, err
		}
		longitude, err := ref("longitude", authored.Longitude)
		if err != nil {
			return visualizationir.VisualizationFieldRef{}, visualizationir.VisualizationFieldRef{}, err
		}
		if latitude == nil || longitude == nil {
			return visualizationir.VisualizationFieldRef{}, visualizationir.VisualizationFieldRef{}, fmt.Errorf("geographic layer %q requires coordinates", authored.ID)
		}
		return *latitude, *longitude, nil
	}
	switch authored.Kind {
	case "point":
		latitude, longitude, err := coordinates()
		if err != nil {
			return visualizationir.VisualizationGeographicLayer{}, err
		}
		return visualizationir.VisualizationGeographicLayer{Value: &visualizationir.VisualizationPointLayer{
			VisualizationGeographicLayerBase: base, Kind: "point", Latitude: latitude, Longitude: longitude, Value: value, Category: category,
			Size: mapSizeScale(authored.Size), Color: color, Stroke: stroke, Cluster: mapCluster(authored.Cluster), Opacity: opacity,
		}}, nil
	case "choropleth":
		geometry, err := visualizationgeometry.Resolve(authored.GeometryAsset)
		if err != nil {
			return visualizationir.VisualizationGeographicLayer{}, fmt.Errorf("geographic layer %q: %w", authored.ID, err)
		}
		join, err := ref("join", authored.Join)
		if err != nil {
			return visualizationir.VisualizationGeographicLayer{}, err
		}
		if join == nil {
			return visualizationir.VisualizationGeographicLayer{}, fmt.Errorf("geographic layer %q requires join", authored.ID)
		}
		return visualizationir.VisualizationGeographicLayer{Value: &visualizationir.VisualizationChoroplethLayer{VisualizationGeographicLayerBase: base, Kind: "choropleth", Geometry: geometry, Join: *join, Value: value, Category: category, Color: color, Stroke: stroke, Opacity: opacity}}, nil
	case "heat", "density":
		latitude, longitude, err := coordinates()
		if err != nil {
			return visualizationir.VisualizationGeographicLayer{}, err
		}
		heat := mapHeatStyle(authored.Heat)
		if authored.Kind == "heat" {
			return visualizationir.VisualizationGeographicLayer{Value: &visualizationir.VisualizationHeatLayer{VisualizationGeographicLayerBase: base, Kind: "heat", Latitude: latitude, Longitude: longitude, Value: value, Color: color, Heat: heat, Opacity: opacity}}, nil
		}
		return visualizationir.VisualizationGeographicLayer{Value: &visualizationir.VisualizationDensityLayer{VisualizationGeographicLayerBase: base, Kind: "density", Latitude: latitude, Longitude: longitude, Value: value, Color: color, Heat: heat, Opacity: opacity}}, nil
	case "reference":
		geometry, err := visualizationgeometry.Resolve(authored.GeometryAsset)
		if err != nil {
			return visualizationir.VisualizationGeographicLayer{}, fmt.Errorf("geographic layer %q: %w", authored.ID, err)
		}
		return visualizationir.VisualizationGeographicLayer{Value: &visualizationir.VisualizationReferenceLayer{VisualizationGeographicLayerBase: base, Kind: "reference", Geometry: geometry, Color: color, Stroke: stroke, Opacity: opacity}}, nil
	case "path":
		latitude, longitude, err := coordinates()
		if err != nil {
			return visualizationir.VisualizationGeographicLayer{}, err
		}
		path, err := ref("path", authored.Path)
		if err != nil {
			return visualizationir.VisualizationGeographicLayer{}, err
		}
		order, err := ref("order", authored.Order)
		if err != nil {
			return visualizationir.VisualizationGeographicLayer{}, err
		}
		if path == nil || order == nil {
			return visualizationir.VisualizationGeographicLayer{}, fmt.Errorf("geographic layer %q requires path and order", authored.ID)
		}
		return visualizationir.VisualizationGeographicLayer{Value: &visualizationir.VisualizationPathLayer{VisualizationGeographicLayerBase: base, Kind: "path", Latitude: latitude, Longitude: longitude, Path: *path, Order: *order, Value: value, Category: category, Color: color, Stroke: stroke, Line: mapLineStyle(authored.Line), Opacity: opacity}}, nil
	default:
		return visualizationir.VisualizationGeographicLayer{}, fmt.Errorf("geographic layer %q has unsupported kind %q", authored.ID, authored.Kind)
	}
}

func compileMapCamera(authored dashboardauthoring.VisualGeoCamera) visualizationir.VisualizationMapCamera {
	mode := visualizationir.VisualizationMapCameraMode(authored.Mode)
	if mode == "" {
		mode = visualizationir.VisualizationMapCameraModeFitData
	}
	padding := authored.Padding
	if padding == 0 {
		padding = 32
	}
	maximumZoom := authored.MaximumZoom
	if maximumZoom == 0 {
		maximumZoom = 14
	}
	var center *[]float64
	if len(authored.Center) == 2 {
		value := append([]float64(nil), authored.Center...)
		center = &value
	}
	return visualizationir.VisualizationMapCamera{Mode: mode, Center: center, Zoom: authored.Zoom, Padding: int32(padding), MinimumZoom: authored.MinimumZoom, MaximumZoom: maximumZoom}
}

func compileMapControls(authored dashboardauthoring.VisualGeoControls) visualizationir.VisualizationMapControls {
	if !authored.Zoom && !authored.Reset && !authored.Compass {
		return visualizationir.VisualizationMapControls{Zoom: true, Reset: true, Compass: true}
	}
	return visualizationir.VisualizationMapControls{Zoom: authored.Zoom, Reset: authored.Reset, Compass: authored.Compass}
}

func mapLayerPosition(value string) visualizationir.VisualizationMapLayerPosition {
	if value == "above_labels" {
		return visualizationir.VisualizationMapLayerPositionAboveLabels
	}
	return visualizationir.VisualizationMapLayerPositionBelowLabels
}

func mapVisibility(value dashboardauthoring.VisualGeoVisibility) visualizationir.VisualizationMapVisibility {
	maximum := value.MaximumZoom
	if maximum == 0 {
		maximum = 24
	}
	return visualizationir.VisualizationMapVisibility{MinimumZoom: value.MinimumZoom, MaximumZoom: maximum}
}

func mapSizeScale(value dashboardauthoring.VisualGeoSizeScale) visualizationir.VisualizationMapSizeScale {
	minimum, maximum := value.MinimumRadius, value.MaximumRadius
	if minimum == 0 {
		minimum = 5
	}
	if maximum == 0 {
		maximum = 28
	}
	return visualizationir.VisualizationMapSizeScale{MinimumRadius: minimum, MaximumRadius: maximum, DomainMinimum: value.DomainMinimum, DomainMaximum: value.DomainMaximum}
}

func mapColorScale(value dashboardauthoring.VisualGeoColorScale) visualizationir.VisualizationMapColorScale {
	kind := visualizationir.VisualizationMapColorScaleKind(value.Kind)
	if kind == "" {
		kind = visualizationir.VisualizationMapColorScaleKindSequential
	}
	palette := value.Palette
	if palette == "" {
		palette = "blue"
	}
	nullColor := value.NullColor
	if nullColor == "" {
		nullColor = "#d0d7de"
	}
	return visualizationir.VisualizationMapColorScale{Kind: kind, Palette: palette, Reverse: value.Reverse, DomainMinimum: value.DomainMinimum, DomainMidpoint: value.DomainMidpoint, DomainMaximum: value.DomainMaximum, NullColor: nullColor}
}

func mapStroke(value dashboardauthoring.VisualGeoStroke) visualizationir.VisualizationMapStroke {
	color, width, opacity := value.Color, value.Width, value.Opacity
	if color == "" {
		color = "#ffffff"
	}
	if width == 0 {
		width = 1.5
	}
	if opacity == 0 {
		opacity = 1
	}
	return visualizationir.VisualizationMapStroke{Color: color, Width: width, Opacity: opacity}
}

func mapCluster(value dashboardauthoring.VisualGeoCluster) visualizationir.VisualizationMapCluster {
	radius, maximumZoom, minimumPoints := value.Radius, value.MaximumZoom, value.MinimumPoints
	if radius == 0 {
		radius = 50
	}
	if maximumZoom == 0 {
		maximumZoom = 14
	}
	if minimumPoints == 0 {
		minimumPoints = 2
	}
	return visualizationir.VisualizationMapCluster{Enabled: value.Enabled, Radius: int32(radius), MaximumZoom: int32(maximumZoom), MinimumPoints: int32(minimumPoints), ShowCount: value.ShowCount}
}

func mapHeatStyle(value dashboardauthoring.VisualGeoHeatStyle) visualizationir.VisualizationMapHeatStyle {
	radius, intensity := value.Radius, value.Intensity
	if radius == 0 {
		radius = 32
	}
	if intensity == 0 {
		intensity = 1
	}
	return visualizationir.VisualizationMapHeatStyle{Radius: radius, Intensity: intensity}
}

func mapLineStyle(value dashboardauthoring.VisualGeoLineStyle) visualizationir.VisualizationMapLineStyle {
	width := value.Width
	if width == 0 {
		width = 3
	}
	return visualizationir.VisualizationMapLineStyle{Width: width, Curvature: value.Curvature}
}

func geographicVisualizationFields(authored dashboardauthoring.Visual) []visualizationir.VisualizationField {
	coordinateAliases := map[string]struct{}{}
	for _, layer := range authored.Geo.Layers {
		if layer.Latitude != "" {
			coordinateAliases[layer.Latitude] = struct{}{}
		}
		if layer.Longitude != "" {
			coordinateAliases[layer.Longitude] = struct{}{}
		}
	}
	identity := map[string]bool{}
	for _, mapping := range authored.Interaction.PointSelection.Mappings {
		identity[mapping.Value] = true
	}
	fields := make([]visualizationir.VisualizationField, 0, len(authored.Query.Dimensions)+len(authored.Query.Metrics)+1)
	appendField := func(field dashboardauthoring.FieldRef, role visualizationir.VisualizationFieldRole, dataType visualizationir.VisualizationDataType) {
		if field.Field == "" {
			return
		}
		alias := field.Alias
		if alias == "" {
			alias = fieldAlias(field.Field)
		}
		if identity[alias] {
			role = visualizationir.VisualizationFieldRoleIdentity
		}
		source := field.Field
		fields = append(fields, visualizationir.VisualizationField{ID: alias, SourceRef: &source, Role: role, DataType: dataType, Nullable: true, Label: alias})
	}
	for _, field := range authored.Query.Dimensions {
		dataType := visualizationir.VisualizationDataTypeString
		alias := field.Alias
		if alias == "" {
			alias = fieldAlias(field.Field)
		}
		if _, ok := coordinateAliases[alias]; ok {
			dataType = visualizationir.VisualizationDataTypeDecimal
		}
		appendField(field, visualizationir.VisualizationFieldRoleDimension, dataType)
	}
	if authored.Query.Time.Field != "" {
		appendField(dashboardauthoring.FieldRef{Field: authored.Query.Time.Field, Alias: authored.Query.Time.Alias}, visualizationir.VisualizationFieldRoleDimension, visualizationir.VisualizationDataTypeTemporal)
	}
	for _, field := range authored.Query.Metrics {
		appendField(field, visualizationir.VisualizationFieldRoleMetric, visualizationir.VisualizationDataTypeDecimal)
	}
	return fields
}
