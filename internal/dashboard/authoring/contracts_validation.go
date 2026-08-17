package authoring

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func (d *Dashboard) ValidateContract() error {
	return d.validateContract()
}

func (d *Dashboard) validateContract() error {
	if d.ID == "" || d.Title == "" {
		return fmt.Errorf("dashboard requires id and title")
	}
	if d.SemanticModel == "" {
		return fmt.Errorf("dashboard %q requires semantic_model", d.ID)
	}
	if len(d.Visuals) == 0 {
		return fmt.Errorf("dashboard %q requires visuals", d.ID)
	}
	if len(d.Pages) == 0 {
		return fmt.Errorf("dashboard %q requires pages", d.ID)
	}
	if err := d.validateFilterArchitectureContract(); err != nil {
		return err
	}
	for name, authored := range d.Visuals {
		if (authored.Chart == nil) == (authored.Tabular == nil) {
			return fmt.Errorf("visual %q must contain exactly one authoring variant", name)
		}
		if authored.Chart != nil {
			if err := d.validateChartContract(name, *authored.Chart); err != nil {
				return err
			}
			continue
		}
		if err := d.validateTabularContract(name, authored.Type, *authored.Tabular); err != nil {
			return err
		}
	}
	return d.validatePages()
}

func (d *Dashboard) validateChartContract(name string, visual Visual) error {
	kind := visual.KindOrDefault()
	if kind != "kpi" && visual.Title == "" {
		return fmt.Errorf("visual %q requires title", name)
	}
	if kind != "kpi" && visual.Type == "" {
		return fmt.Errorf("visual %q requires type", name)
	}
	if !supportsVisualKind(kind) {
		return fmt.Errorf("visual %q has unsupported kind %q", name, kind)
	}
	shape := visual.ResultShape()
	renderer := visual.ownedRenderer()
	if !supportsVisualShape(shape) {
		return fmt.Errorf("visual %q has unsupported shape %q", name, shape)
	}
	if !rendererSupportsType(renderer, visual.Type) {
		return fmt.Errorf("visual %q has unsupported type %q", name, visual.Type)
	}
	if !rendererSupportsShapeType(renderer, shape, visual.Type) {
		return fmt.Errorf("visual %q type %q does not support data shape %q", name, visual.Type, shape)
	}
	if err := validateVisualQueryShape(name, visual); err != nil {
		return err
	}
	if shape == "point" {
		if err := validatePointVisual(name, visual); err != nil {
			return err
		}
	}
	if err := validateVisualPresentation(name, visual); err != nil {
		return err
	}
	if !visual.Query.Series.IsZero() {
		if !supportsSeries(shape) {
			return fmt.Errorf("visual %q shape %q does not support series", name, shape)
		}
		if !rendererTypeSupportsSeries(renderer, visual.Type) {
			return fmt.Errorf("visual %q type %q does not support series", name, visual.Type)
		}
	}
	if shape == "geo" {
		if err := validateGeographicVisual(name, visual); err != nil {
			return err
		}
	}
	for _, sort := range visual.Query.Sort {
		if sort.Field == "" && sort.Expr == "" {
			return fmt.Errorf("visual %q has sort missing field or expr", name)
		}
	}
	if !visual.Interaction.RowSelection.IsZero() {
		return fmt.Errorf("visual %q does not support row_selection", name)
	}
	if !visual.Interaction.PointSelection.IsZero() {
		if kind == "kpi" {
			return fmt.Errorf("visual %q kind kpi does not support point_selection", name)
		}
		if err := d.validateSelectionInteraction("visual", name, "point_selection", visual.Interaction.PointSelection); err != nil {
			return err
		}
	}
	if !visual.Interaction.SpatialSelection.IsZero() {
		if visual.Type != "map" {
			return fmt.Errorf("visual %q type %q does not support spatial_selection", name, visual.Type)
		}
		if err := validateSpatialSelectionInteraction(name, visual); err != nil {
			return err
		}
		for _, target := range visual.Interaction.SpatialSelection.Targets {
			if err := d.validateInteractionTarget("visual", name, "spatial_selection", target); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *Dashboard) validateTabularContract(name, visualType string, table TableVisual) error {
	if table.Title == "" {
		return fmt.Errorf("table %q requires title", name)
	}
	if err := validateTableStyle(name, table.Style); err != nil {
		return err
	}
	switch table.CardinalityOrDefault() {
	case TableCardinalityBounded, TableCardinalityExact:
	default:
		return fmt.Errorf("table %q has unsupported cardinality %q", name, table.Cardinality)
	}
	for _, column := range table.Columns {
		if err := validateTableColumn(name, column); err != nil {
			return err
		}
	}
	for metric, rules := range table.MetricFormatting {
		for _, rule := range rules {
			if err := validateTableFormattingRule(name, metric, rule); err != nil {
				return err
			}
		}
	}
	if err := validateConditionalFormatting(name, visualType, table.ConditionalFormatting); err != nil {
		return err
	}
	switch visualType {
	case "table":
		if table.Query.Table == "" {
			return fmt.Errorf("table %q type table requires query.table", name)
		}
		if len(table.Query.Fields) == 0 && len(table.Query.Columns) == 0 {
			return fmt.Errorf("table %q type table requires query.fields or query.columns", name)
		}
	case "matrix":
		if !table.Interaction.RowSelection.IsZero() {
			return fmt.Errorf("table %q type matrix does not support row_selection", name)
		}
		if len(table.Query.Rows) == 0 || len(table.Query.Metrics) == 0 {
			return fmt.Errorf("table %q type matrix requires query.rows and query.metrics", name)
		}
		if len(table.Query.Columns) > 1 {
			return fmt.Errorf("table %q type matrix supports at most one column dimension", name)
		}
	case "pivot":
		if !table.Interaction.RowSelection.IsZero() {
			return fmt.Errorf("table %q type pivot does not support row_selection", name)
		}
		if len(table.Query.Rows) == 0 || len(table.Query.Columns) != 1 || len(table.Query.Metrics) != 1 {
			return fmt.Errorf("table %q type pivot requires query.rows, one query column dimension, and one query metric", name)
		}
	default:
		return fmt.Errorf("visual %q has unsupported tabular type %q", name, visualType)
	}
	if !table.Interaction.PointSelection.IsZero() {
		return fmt.Errorf("table %q does not support point_selection", name)
	}
	if !table.Interaction.SpatialSelection.IsZero() {
		return fmt.Errorf("table %q does not support spatial_selection", name)
	}
	if !table.Interaction.RowSelection.IsZero() {
		if err := d.validateSelectionInteraction("visual", name, "row_selection", table.Interaction.RowSelection); err != nil {
			return err
		}
	}
	return nil
}

func validateGeographicVisual(name string, visual Visual) error {
	if len(visual.Geo.Layers) == 0 {
		return fmt.Errorf("visual %q geographic visualization requires geo.layers", name)
	}
	aliases := map[string]struct{}{}
	for _, field := range visual.Query.Dimensions {
		aliases[defaultString(field.Alias, fieldRefAlias(field.Field))] = struct{}{}
	}
	if visual.Query.Time.Field != "" {
		aliases[defaultString(visual.Query.Time.Alias, fieldRefAlias(visual.Query.Time.Field))] = struct{}{}
	}
	for _, field := range visual.Query.Metrics {
		aliases[defaultString(field.Alias, fieldRefAlias(field.Field))] = struct{}{}
	}
	requireAlias := func(layerID, property, alias string) error {
		if strings.TrimSpace(alias) == "" {
			return fmt.Errorf("visual %q geographic layer %q requires %s", name, layerID, property)
		}
		if _, ok := aliases[alias]; !ok {
			return fmt.Errorf("visual %q geographic layer %q %s references unknown query alias %q", name, layerID, property, alias)
		}
		return nil
	}
	optionalAlias := func(layerID, property, alias string) error {
		if strings.TrimSpace(alias) == "" {
			return nil
		}
		return requireAlias(layerID, property, alias)
	}
	if !oneOf(visual.Geo.Theme, "", "auto", "light", "dark") {
		return fmt.Errorf("visual %q has unsupported geo.theme %q", name, visual.Geo.Theme)
	}
	if !oneOf(visual.Geo.LabelDensity, "", "hidden", "normal", "dense") {
		return fmt.Errorf("visual %q has unsupported geo.label_density %q", name, visual.Geo.LabelDensity)
	}
	if !oneOf(visual.Geo.Camera.Mode, "", "fit_data", "fixed", "preserve") {
		return fmt.Errorf("visual %q has unsupported geo.camera.mode %q", name, visual.Geo.Camera.Mode)
	}
	if len(visual.Geo.Camera.Center) != 0 && len(visual.Geo.Camera.Center) != 2 {
		return fmt.Errorf("visual %q geo.camera.center requires longitude and latitude", name)
	}
	if len(visual.Geo.Camera.Center) == 2 && (visual.Geo.Camera.Center[0] < -180 || visual.Geo.Camera.Center[0] > 180 || visual.Geo.Camera.Center[1] < -90 || visual.Geo.Camera.Center[1] > 90) {
		return fmt.Errorf("visual %q geo.camera.center is outside geographic bounds", name)
	}
	if visual.Geo.Camera.MinimumZoom < 0 || visual.Geo.Camera.MaximumZoom < 0 || (visual.Geo.Camera.MaximumZoom > 0 && visual.Geo.Camera.MinimumZoom > visual.Geo.Camera.MaximumZoom) {
		return fmt.Errorf("visual %q has invalid geo.camera zoom range", name)
	}
	seen := map[string]struct{}{}
	for _, layer := range visual.Geo.Layers {
		if strings.TrimSpace(layer.ID) == "" {
			return fmt.Errorf("visual %q geographic layer requires id", name)
		}
		if _, exists := seen[layer.ID]; exists {
			return fmt.Errorf("visual %q has duplicate geographic layer %q", name, layer.ID)
		}
		seen[layer.ID] = struct{}{}
		if layer.Value != "" {
			if err := requireAlias(layer.ID, "value", layer.Value); err != nil {
				return err
			}
		}
		for property, alias := range map[string]string{"category": layer.Category, "label": layer.Label, "path": layer.Path, "order": layer.Order} {
			if err := optionalAlias(layer.ID, property, alias); err != nil {
				return err
			}
		}
		for _, alias := range layer.Tooltip {
			if err := requireAlias(layer.ID, "tooltip", alias); err != nil {
				return err
			}
		}
		if !oneOf(layer.Position, "", "below_labels", "above_labels") {
			return fmt.Errorf("visual %q geographic layer %q has unsupported position %q", name, layer.ID, layer.Position)
		}
		if layer.Visibility.MinimumZoom < 0 || layer.Visibility.MaximumZoom < 0 || (layer.Visibility.MaximumZoom > 0 && layer.Visibility.MinimumZoom > layer.Visibility.MaximumZoom) {
			return fmt.Errorf("visual %q geographic layer %q has invalid visibility zoom range", name, layer.ID)
		}
		if layer.Size.MinimumRadius < 0 || layer.Size.MaximumRadius < 0 || (layer.Size.MaximumRadius > 0 && layer.Size.MinimumRadius > layer.Size.MaximumRadius) {
			return fmt.Errorf("visual %q geographic layer %q size minimum_radius must not exceed maximum_radius", name, layer.ID)
		}
		if layer.Size.DomainMinimum != nil && layer.Size.DomainMaximum != nil && *layer.Size.DomainMinimum >= *layer.Size.DomainMaximum {
			return fmt.Errorf("visual %q geographic layer %q has invalid size domain", name, layer.ID)
		}
		if !oneOf(layer.Color.Kind, "", "sequential", "diverging", "categorical") {
			return fmt.Errorf("visual %q geographic layer %q has unsupported color kind %q", name, layer.ID, layer.Color.Kind)
		}
		if layer.Color.DomainMinimum != nil && layer.Color.DomainMaximum != nil && *layer.Color.DomainMinimum >= *layer.Color.DomainMaximum {
			return fmt.Errorf("visual %q geographic layer %q has invalid color domain", name, layer.ID)
		}
		if layer.Opacity < 0 || layer.Opacity > 1 || layer.Stroke.Opacity < 0 || layer.Stroke.Opacity > 1 {
			return fmt.Errorf("visual %q geographic layer %q opacity must be between zero and one", name, layer.ID)
		}
		if layer.Cluster.Enabled && layer.Kind != "point" && oneOf(layer.Kind, "choropleth", "heat", "density", "reference", "path") {
			return fmt.Errorf("visual %q geographic layer %q clustering is only supported for point layers", name, layer.ID)
		}
		switch layer.Kind {
		case "choropleth":
			if strings.TrimSpace(layer.GeometryAsset) == "" {
				return fmt.Errorf("visual %q choropleth layer %q requires geometry_asset", name, layer.ID)
			}
			if err := requireAlias(layer.ID, "join", layer.Join); err != nil {
				return err
			}
			if layer.Latitude != "" || layer.Longitude != "" {
				return fmt.Errorf("visual %q choropleth layer %q does not accept latitude or longitude", name, layer.ID)
			}
		case "point", "heat", "density", "path":
			if layer.GeometryAsset != "" || layer.Join != "" {
				return fmt.Errorf("visual %q geographic layer %q kind %q does not accept geometry_asset or join", name, layer.ID, layer.Kind)
			}
			if err := requireAlias(layer.ID, "latitude", layer.Latitude); err != nil {
				return err
			}
			if err := requireAlias(layer.ID, "longitude", layer.Longitude); err != nil {
				return err
			}
			if layer.Kind == "path" {
				if err := requireAlias(layer.ID, "path", layer.Path); err != nil {
					return err
				}
				if err := requireAlias(layer.ID, "order", layer.Order); err != nil {
					return err
				}
			}
		case "reference":
			if strings.TrimSpace(layer.GeometryAsset) == "" {
				return fmt.Errorf("visual %q reference layer %q requires geometry_asset", name, layer.ID)
			}
			if layer.Join != "" || layer.Latitude != "" || layer.Longitude != "" || layer.Value != "" || layer.Category != "" {
				return fmt.Errorf("visual %q reference layer %q does not accept query field bindings", name, layer.ID)
			}
		default:
			return fmt.Errorf("visual %q geographic layer %q has unsupported kind %q", name, layer.ID, layer.Kind)
		}
	}
	return nil
}

func validateVisualPresentation(name string, visual Visual) error {
	if err := validateContextDatasetsAndMetadata(name, visual); err != nil {
		return err
	}
	if err := validateKPIConfiguration(name, visual); err != nil {
		return err
	}
	presentation := visual.Presentation
	if !oneOf(presentation.Legend, "", "hidden", "top", "right", "bottom", "left") {
		return fmt.Errorf("visual %q has unsupported presentation.legend %q", name, presentation.Legend)
	}
	if !validDisplayUnits(presentation.DisplayUnits) {
		return fmt.Errorf("visual %q has unsupported presentation.display_units %q", name, presentation.DisplayUnits)
	}
	if err := validateLabelPolicy(name, visual.Type, presentation.Labels); err != nil {
		return err
	}
	if !oneOf(presentation.Orientation, "", "horizontal", "vertical") {
		return fmt.Errorf("visual %q has unsupported presentation.orientation %q", name, presentation.Orientation)
	}
	if !oneOf(presentation.LabelPosition, "", "automatic", "inside", "outside", "top") {
		return fmt.Errorf("visual %q has unsupported presentation.label_position %q", name, presentation.LabelPosition)
	}
	if !oneOf(presentation.Tone, "", "neutral", "ink", "success", "warning", "danger") {
		return fmt.Errorf("visual %q has unsupported presentation.tone %q", name, presentation.Tone)
	}
	if presentation.HistogramBins > 0 && visual.Type != "histogram" {
		return fmt.Errorf("visual %q presentation.histogram_bins is only valid for histogram", name)
	}
	if len(presentation.SeriesTypes) > 0 && visual.Type != "combo" {
		return fmt.Errorf("visual %q presentation.series_types is only valid for combo", name)
	}
	if presentation.DualAxis && visual.Type != "combo" {
		return fmt.Errorf("visual %q presentation.dual_axis is only valid for combo", name)
	}
	if presentation.Basemap != "" && visual.Type != "map" {
		return fmt.Errorf("visual %q presentation.basemap is only valid for map", name)
	}
	if visual.Type == "map" && (presentation.Basemap != "" || presentation.Roam) {
		return fmt.Errorf("visual %q map presentation.basemap and presentation.roam were replaced by geo.basemap and geo.controls", name)
	}
	if presentation.InnerRadius < 0 || presentation.InnerRadius > 1 || presentation.OuterRadius < 0 || presentation.OuterRadius > 1 || (presentation.InnerRadius > 0 && presentation.OuterRadius > 0 && presentation.InnerRadius >= presentation.OuterRadius) {
		return fmt.Errorf("visual %q has invalid presentation radii", name)
	}
	if (presentation.InnerRadius > 0 || presentation.OuterRadius > 0 || presentation.CenterLabel != "") && visual.Type != "donut" {
		return fmt.Errorf("visual %q donut presentation is only valid for donut", name)
	}
	if presentation.Rose && visual.Type != "pie" && visual.Type != "donut" {
		return fmt.Errorf("visual %q presentation.rose is only valid for pie or donut", name)
	}
	if presentation.Align != "" && (visual.Type != "funnel" || !oneOf(presentation.Align, "left", "center", "right")) {
		return fmt.Errorf("visual %q has unsupported presentation.align %q", name, presentation.Align)
	}
	if presentation.Sort != "" && (visual.Type != "funnel" || !oneOf(presentation.Sort, "ascending", "descending")) {
		return fmt.Errorf("visual %q has unsupported presentation.sort %q", name, presentation.Sort)
	}
	if presentation.Layout != "" && (!oneOf(visual.Type, "tree", "graph") || !oneOf(presentation.Layout, "standard", "circular")) {
		return fmt.Errorf("visual %q has unsupported presentation.layout %q", name, presentation.Layout)
	}
	if presentation.Focus != "" && (!oneOf(visual.Type, "graph", "sankey") || !oneOf(presentation.Focus, "none", "adjacency")) {
		return fmt.Errorf("visual %q has unsupported presentation.focus %q", name, presentation.Focus)
	}
	if presentation.InitialDepth < 0 || (presentation.InitialDepth > 0 && !oneOf(visual.Type, "tree", "treemap", "sunburst")) {
		return fmt.Errorf("visual %q has unsupported presentation.initial_depth %d", name, presentation.InitialDepth)
	}
	if presentation.NodeGap < 0 || (presentation.NodeGap > 0 && visual.Type != "sankey") {
		return fmt.Errorf("visual %q has unsupported presentation.node_gap %v", name, presentation.NodeGap)
	}
	if presentation.Curveness < 0 || presentation.Curveness > 1 || (presentation.Curveness > 0 && !oneOf(visual.Type, "graph", "sankey")) {
		return fmt.Errorf("visual %q has unsupported presentation.curveness %v", name, presentation.Curveness)
	}
	if presentation.Breadcrumb != nil && visual.Type != "treemap" {
		return fmt.Errorf("visual %q presentation.breadcrumb is only valid for treemap", name)
	}
	if presentation.Roam && !oneOf(visual.Type, "tree", "treemap", "sunburst", "graph") {
		return fmt.Errorf("visual %q presentation.roam is unsupported for type %q", name, visual.Type)
	}
	if (presentation.Minimum != nil || presentation.Maximum != nil || presentation.ProgressWidth > 0 || len(presentation.Thresholds) > 0) && visual.Type != "gauge" && visual.Type != "kpi" {
		return fmt.Errorf("visual %q threshold presentation is only valid for gauge or kpi", name)
	}
	if presentation.Target != nil && visual.Type != "gauge" {
		return fmt.Errorf("visual %q presentation.target is only valid for gauge", name)
	}
	if visual.Type == "gauge" && (presentation.Minimum == nil || presentation.Maximum == nil) {
		return fmt.Errorf("visual %q type gauge requires presentation.minimum and presentation.maximum", name)
	}
	if presentation.Minimum != nil && presentation.Maximum != nil && *presentation.Minimum >= *presentation.Maximum {
		return fmt.Errorf("visual %q presentation.minimum must be less than maximum", name)
	}
	if visual.Type == "gauge" && presentation.Target != nil && (*presentation.Target < *presentation.Minimum || *presentation.Target > *presentation.Maximum) {
		return fmt.Errorf("visual %q presentation target %v must be within [%v, %v]", name, *presentation.Target, *presentation.Minimum, *presentation.Maximum)
	}
	previous := -1.0e308
	for _, threshold := range presentation.Thresholds {
		if threshold.Value < previous {
			return fmt.Errorf("visual %q presentation.thresholds must be ordered", name)
		}
		if !oneOf(threshold.Tone, "neutral", "ink", "success", "warning", "danger") {
			return fmt.Errorf("visual %q has unsupported threshold tone %q", name, threshold.Tone)
		}
		if visual.Type == "gauge" && (threshold.Value < *presentation.Minimum || threshold.Value > *presentation.Maximum) {
			return fmt.Errorf("visual %q presentation threshold %v must be within [%v, %v]", name, threshold.Value, *presentation.Minimum, *presentation.Maximum)
		}
		previous = threshold.Value
	}
	if err := validateDecisionContext(name, visual); err != nil {
		return err
	}
	if err := validateSeriesPresentation(name, visual); err != nil {
		return err
	}
	if err := validateConditionalFormatting(name, visual.Type, visual.Presentation.ConditionalFormatting); err != nil {
		return err
	}
	return nil
}

func validateLabelPolicy(name, visualType string, policy VisualLabelPolicy) error {
	if policy.IsZero() {
		return nil
	}
	if !oneOf(visualType,
		"line", "area", "bar", "column", "combo", "scatter", "waterfall", "heatmap", "histogram",
		"candlestick", "boxplot", "pie", "donut", "funnel", "tree", "treemap", "sunburst", "sankey", "graph",
		"gauge",
	) {
		return fmt.Errorf("visual %q label policies are unsupported for type %q", name, visualType)
	}
	if !oneOf(policy.Density, "", "hidden", "automatic", "dense", "always") {
		return fmt.Errorf("visual %q has unsupported presentation.labels.density %q", name, policy.Density)
	}
	seen := make(map[string]struct{}, len(policy.Priority))
	for _, priority := range policy.Priority {
		if !oneOf(priority, "selected", "anomaly", "threshold") {
			return fmt.Errorf("visual %q has unsupported presentation.labels priority %q", name, priority)
		}
		if _, exists := seen[priority]; exists {
			return fmt.Errorf("visual %q has duplicate presentation.labels priority %q", name, priority)
		}
		seen[priority] = struct{}{}
	}
	if policy.MaxCharacters != nil && (*policy.MaxCharacters < 4 || *policy.MaxCharacters > 200) {
		return fmt.Errorf("visual %q presentation.labels.max_characters must be between 4 and 200", name)
	}
	if policy.MinimumSpacing != nil && (*policy.MinimumSpacing < 0 || *policy.MinimumSpacing > 64) {
		return fmt.Errorf("visual %q presentation.labels.minimum_spacing must be between 0 and 64", name)
	}
	if policy.Density != "always" && policy.TooltipFallback != nil && !*policy.TooltipFallback {
		return fmt.Errorf("visual %q labels that can be suppressed require tooltip fallback", name)
	}
	return nil
}

func validateContextDatasetsAndMetadata(name string, visual Visual) error {
	hasMetadata := visual.Metadata.Title != nil || visual.Metadata.Subtitle != nil || visual.Metadata.Description != nil || visual.Metadata.Summary != nil
	if visual.Type == "map" && (len(visual.Datasets) > 0 || hasMetadata) {
		return fmt.Errorf("visual %q type %q does not support context datasets or data-bound metadata", name, visual.Type)
	}
	for datasetID, query := range visual.Datasets {
		if strings.TrimSpace(datasetID) == "" {
			return fmt.Errorf("visual %q context dataset id is required", name)
		}
		if datasetID == "primary" {
			return fmt.Errorf("visual %q dataset id %q is reserved", name, datasetID)
		}
		if len(query.Dimensions) == 0 && query.Time.Field == "" && len(query.Metrics) == 0 {
			return fmt.Errorf("visual %q dataset %q requires dimensions, time, or metrics", name, datasetID)
		}
	}
	bindings := []struct {
		name    string
		binding *VisualTextBinding
	}{
		{"title", visual.Metadata.Title},
		{"subtitle", visual.Metadata.Subtitle},
		{"description", visual.Metadata.Description},
		{"summary", visual.Metadata.Summary},
	}
	for _, item := range bindings {
		if item.binding == nil {
			continue
		}
		dataset := item.binding.Dataset
		if dataset == "" {
			dataset = "primary"
		}
		if dataset != "primary" {
			if _, ok := visual.Datasets[dataset]; !ok {
				return fmt.Errorf("visual %q metadata %s references unknown dataset %q", name, item.name, dataset)
			}
		}
		if strings.TrimSpace(item.binding.Field) == "" {
			return fmt.Errorf("visual %q metadata %s requires field", name, item.name)
		}
		if !oneOf(item.binding.Reducer, "", "first", "last", "minimum", "maximum", "mean", "median") {
			return fmt.Errorf("visual %q metadata %s has unsupported reducer %q", name, item.name, item.binding.Reducer)
		}
		if strings.TrimSpace(item.binding.Fallback) == "" {
			return fmt.Errorf("visual %q metadata %s requires fallback", name, item.name)
		}
	}
	return nil
}

func validateKPIConfiguration(name string, visual Visual) error {
	configured := visual.KPI.Mode != "" || visual.KPI.Comparison != nil || visual.KPI.Goal != nil || visual.KPI.Trend != nil ||
		visual.KPI.Delta != "" || visual.KPI.FavorableDirection != "" || visual.KPI.MissingComparison != "" || len(visual.KPI.Ranges) > 0
	if visual.Type != "kpi" {
		if configured {
			return fmt.Errorf("visual %q kpi configuration is only valid for type kpi", name)
		}
		return nil
	}
	if !oneOf(visual.KPI.Mode, "", "compact", "bullet", "progress") {
		return fmt.Errorf("visual %q has unsupported kpi.mode %q", name, visual.KPI.Mode)
	}
	if !oneOf(visual.KPI.Delta, "", "absolute", "relative") {
		return fmt.Errorf("visual %q has unsupported kpi.delta %q", name, visual.KPI.Delta)
	}
	if !oneOf(visual.KPI.FavorableDirection, "", "increase", "decrease", "neutral") {
		return fmt.Errorf("visual %q has unsupported kpi.favorable_direction %q", name, visual.KPI.FavorableDirection)
	}
	if !oneOf(visual.KPI.MissingComparison, "", "show_unavailable", "hide") {
		return fmt.Errorf("visual %q has unsupported kpi.missing_comparison %q", name, visual.KPI.MissingComparison)
	}
	if visual.KPI.Comparison != nil && visual.KPI.FavorableDirection == "" {
		return fmt.Errorf("visual %q kpi comparison requires favorable_direction", name)
	}
	if oneOf(visual.KPI.Mode, "bullet", "progress") && visual.KPI.Goal == nil {
		return fmt.Errorf("visual %q kpi mode %q requires an explicit goal", name, visual.KPI.Mode)
	}
	for bindingName, binding := range map[string]*VisualKPIValueBinding{
		"comparison": visual.KPI.Comparison,
		"goal":       visual.KPI.Goal,
	} {
		if binding == nil {
			continue
		}
		if err := validateKPIValueBinding(name, bindingName, visual, *binding); err != nil {
			return err
		}
	}
	if trend := visual.KPI.Trend; trend != nil {
		query, ok := visual.Datasets[trend.Dataset]
		if !ok {
			return fmt.Errorf("visual %q kpi trend references unknown dataset %q", name, trend.Dataset)
		}
		if query.Limit <= 1 {
			return fmt.Errorf("visual %q kpi trend dataset must have limit greater than one", name)
		}
		sorted := false
		for _, sort := range query.Sort {
			if sort.Field == trend.Category && oneOf(sort.Direction, "asc", "desc") {
				sorted = true
				break
			}
		}
		if !sorted {
			return fmt.Errorf("visual %q kpi trend dataset must sort by category field %q", name, trend.Category)
		}
		if visual.DataBudget.MaxRows > 0 && query.Limit > visual.DataBudget.MaxRows {
			return fmt.Errorf("visual %q kpi trend limit %d exceeds data budget %d", name, query.Limit, visual.DataBudget.MaxRows)
		}
		aliases := visualQueryAliases(query)
		for role, field := range map[string]string{"category": trend.Category, "value": trend.Value} {
			if strings.TrimSpace(field) == "" {
				return fmt.Errorf("visual %q kpi trend requires %s field", name, role)
			}
			if _, ok := aliases[field]; !ok {
				return fmt.Errorf("visual %q kpi trend references unknown field %q in dataset %q", name, field, trend.Dataset)
			}
		}
	}
	var previousMaximum *float64
	for index, valueRange := range visual.KPI.Ranges {
		if strings.TrimSpace(valueRange.Label) == "" {
			return fmt.Errorf("visual %q kpi range %d requires label", name, index)
		}
		if !oneOf(valueRange.Tone, "neutral", "ink", "success", "warning", "danger") {
			return fmt.Errorf("visual %q kpi range %d has unsupported tone %q", name, index, valueRange.Tone)
		}
		if valueRange.Minimum != nil && valueRange.Maximum != nil && *valueRange.Minimum >= *valueRange.Maximum {
			return fmt.Errorf("visual %q kpi range %d minimum must be less than maximum", name, index)
		}
		if index > 0 && valueRange.Minimum == nil {
			return fmt.Errorf("visual %q kpi range %d requires minimum", name, index)
		}
		if index < len(visual.KPI.Ranges)-1 && valueRange.Maximum == nil {
			return fmt.Errorf("visual %q kpi range %d requires maximum", name, index)
		}
		if previousMaximum != nil && valueRange.Minimum != nil && *valueRange.Minimum < *previousMaximum {
			return fmt.Errorf("visual %q kpi ranges overlap at index %d", name, index)
		}
		previousMaximum = valueRange.Maximum
	}
	return nil
}

func validateKPIValueBinding(name, bindingName string, visual Visual, binding VisualKPIValueBinding) error {
	datasetID := binding.Dataset
	if datasetID == "" {
		datasetID = "primary"
	}
	aliases := map[string]struct{}{"value": {}}
	if datasetID != "primary" {
		query, ok := visual.Datasets[datasetID]
		if !ok {
			return fmt.Errorf("visual %q kpi %s references unknown dataset %q", name, bindingName, datasetID)
		}
		aliases = visualQueryAliases(query)
	}
	if _, ok := aliases[binding.Field]; !ok {
		return fmt.Errorf("visual %q kpi %s references unknown field %q in dataset %q", name, bindingName, binding.Field, datasetID)
	}
	if !oneOf(binding.Reducer, "", "first", "last", "minimum", "maximum", "mean", "median") {
		return fmt.Errorf("visual %q kpi %s has unsupported reducer %q", name, bindingName, binding.Reducer)
	}
	return nil
}

func visualQueryAliases(query VisualQuery) map[string]struct{} {
	aliases := make(map[string]struct{}, len(query.Dimensions)+len(query.Metrics)+2)
	for _, field := range query.Dimensions {
		aliases[defaultString(field.Alias, fieldRefAlias(field.Field))] = struct{}{}
	}
	if query.Time.Field != "" {
		aliases[defaultString(query.Time.Alias, fieldRefAlias(query.Time.Field))] = struct{}{}
	}
	if !query.Series.IsZero() {
		aliases[defaultString(query.Series.Alias, fieldRefAlias(query.Series.Field))] = struct{}{}
	}
	for _, field := range query.Metrics {
		aliases[defaultString(field.Alias, fieldRefAlias(field.Field))] = struct{}{}
	}
	return aliases
}

func validateConditionalFormatting(name, visualType string, formats []VisualConditionalFormat) error {
	if len(formats) > 0 && !oneOf(visualType,
		"line", "area", "bar", "column", "combo", "scatter", "waterfall", "heatmap",
		"kpi", "table", "matrix", "pivot",
	) {
		return fmt.Errorf("visual %q type %q does not support conditional formatting", name, visualType)
	}
	ids := make(map[string]struct{}, len(formats))
	targets := make(map[string]struct{}, len(formats))
	for _, format := range formats {
		if strings.TrimSpace(format.ID) == "" {
			return fmt.Errorf("visual %q conditional formatting requires id", name)
		}
		if _, exists := ids[format.ID]; exists {
			return fmt.Errorf("visual %q has duplicate conditional formatting id %q", name, format.ID)
		}
		ids[format.ID] = struct{}{}
		if strings.TrimSpace(format.Field) == "" {
			return fmt.Errorf("visual %q conditional formatting %q requires field", name, format.ID)
		}
		targetKey := format.Target + "\x00" + format.Field
		if _, exists := targets[targetKey]; exists {
			return fmt.Errorf("visual %q has ambiguous conditional formatting target %q for field %q", name, format.Target, format.Field)
		}
		targets[targetKey] = struct{}{}
		if err := validateConditionalTarget(name, visualType, format); err != nil {
			return err
		}
		if err := validateConditionalStyle(name, format.ID, "null", format.Null, false); err != nil {
			return err
		}
		if format.Null == (VisualConditionalStyle{}) {
			return fmt.Errorf("visual %q conditional formatting %q requires null style", name, format.ID)
		}
		switch format.Kind {
		case "gradient":
			if format.Minimum == nil || format.Maximum == nil {
				return fmt.Errorf("visual %q conditional formatting %q gradient requires minimum and maximum", name, format.ID)
			}
			if *format.Minimum >= *format.Maximum {
				return fmt.Errorf("visual %q conditional formatting %q minimum must be less than maximum", name, format.ID)
			}
			if format.Low.Color == "" || format.High.Color == "" {
				return fmt.Errorf("visual %q conditional formatting %q gradient requires low and high color intents", name, format.ID)
			}
			if err := validateConditionalStyle(name, format.ID, "low", format.Low, false); err != nil {
				return err
			}
			if err := validateConditionalStyle(name, format.ID, "high", format.High, false); err != nil {
				return err
			}
		case "rules":
			if len(format.Rules) == 0 {
				return fmt.Errorf("visual %q conditional formatting %q requires rules", name, format.ID)
			}
			if format.Default == (VisualConditionalStyle{}) {
				return fmt.Errorf("visual %q conditional formatting %q requires default style", name, format.ID)
			}
			for index, rule := range format.Rules {
				if !oneOf(rule.Operator, "less_than", "less_or_equal", "greater_than", "greater_or_equal", "equal", "not_equal") {
					return fmt.Errorf("visual %q conditional formatting %q rule %d has unsupported operator %q", name, format.ID, index, rule.Operator)
				}
				if err := validateConditionalStyle(name, format.ID, fmt.Sprintf("rule %d", index), rule.Style, true); err != nil {
					return err
				}
			}
			if err := validateConditionalStyle(name, format.ID, "default", format.Default, true); err != nil {
				return err
			}
		case "field":
			if strings.TrimSpace(format.SourceField) == "" {
				return fmt.Errorf("visual %q conditional formatting %q requires source_field", name, format.ID)
			}
			if len(format.Values) == 0 {
				return fmt.Errorf("visual %q conditional formatting %q requires values", name, format.ID)
			}
			if format.Default == (VisualConditionalStyle{}) {
				return fmt.Errorf("visual %q conditional formatting %q requires default style", name, format.ID)
			}
			keys := make([]string, 0, len(format.Values))
			for value := range format.Values {
				keys = append(keys, value)
			}
			sort.Strings(keys)
			for _, value := range keys {
				if strings.TrimSpace(value) == "" {
					return fmt.Errorf("visual %q conditional formatting %q has empty field value", name, format.ID)
				}
				if err := validateConditionalStyle(name, format.ID, fmt.Sprintf("value %q", value), format.Values[value], true); err != nil {
					return err
				}
			}
			if err := validateConditionalStyle(name, format.ID, "default", format.Default, true); err != nil {
				return err
			}
		default:
			return fmt.Errorf("visual %q conditional formatting %q has unsupported kind %q", name, format.ID, format.Kind)
		}
	}
	return nil
}

func validateConditionalTarget(name, visualType string, format VisualConditionalFormat) error {
	if !oneOf(format.Target, "mark_fill", "mark_stroke", "series_color", "label_foreground", "visual_background", "cell_foreground", "cell_background", "kpi_value", "icon") {
		return fmt.Errorf("visual %q conditional formatting %q has unsupported target %q", name, format.ID, format.Target)
	}
	isKPI := visualType == "kpi"
	isTabular := oneOf(visualType, "table", "matrix", "pivot")
	if strings.HasPrefix(format.Target, "cell_") && !isTabular {
		return fmt.Errorf("visual %q conditional formatting %q target %q is only valid for tabular visuals", name, format.ID, format.Target)
	}
	if format.Target == "kpi_value" && !isKPI {
		return fmt.Errorf("visual %q conditional formatting %q target %q is only valid for KPI visuals", name, format.ID, format.Target)
	}
	if format.Target == "visual_background" && !isKPI {
		return fmt.Errorf("visual %q conditional formatting %q target %q is only valid for KPI visuals", name, format.ID, format.Target)
	}
	if isKPI && oneOf(format.Target, "mark_fill", "mark_stroke", "series_color") {
		return fmt.Errorf("visual %q conditional formatting %q target %q is incompatible with KPI visuals", name, format.ID, format.Target)
	}
	if isTabular && oneOf(format.Target, "mark_fill", "mark_stroke", "series_color", "kpi_value") {
		return fmt.Errorf("visual %q conditional formatting %q target %q is incompatible with tabular visuals", name, format.ID, format.Target)
	}
	return nil
}

func validateConditionalStyle(name, formatID, position string, style VisualConditionalStyle, redundantCue bool) error {
	if style == (VisualConditionalStyle{}) {
		return fmt.Errorf("visual %q conditional formatting %q %s style is empty", name, formatID, position)
	}
	if style.Color != "" && !validColorIntent(style.Color) {
		return fmt.Errorf("visual %q conditional formatting %q has unsupported color intent %q", name, formatID, style.Color)
	}
	if style.Icon != "" && !oneOf(style.Icon, "circle", "square", "diamond", "triangle_up", "triangle_down", "arrow_up", "arrow_down", "warning") {
		return fmt.Errorf("visual %q conditional formatting %q has unsupported icon intent %q", name, formatID, style.Icon)
	}
	if redundantCue && style.Color != "" && style.Icon == "" {
		return fmt.Errorf("visual %q conditional formatting %q %s color requires a redundant icon cue", name, formatID, position)
	}
	return nil
}

func validateSeriesPresentation(name string, visual Visual) error {
	presentation := visual.Presentation
	if presentation.Stacked && presentation.Stacking != "" {
		return fmt.Errorf("visual %q cannot combine presentation.stacked and presentation.stacking", name)
	}
	if !oneOf(presentation.Stacking, "", "none", "normal", "percent") {
		return fmt.Errorf("visual %q has unsupported presentation.stacking %q", name, presentation.Stacking)
	}
	if presentation.Stacking != "" && presentation.Stacking != "none" {
		if !oneOf(visual.Type, "line", "area", "bar", "column", "combo") {
			return fmt.Errorf("visual %q stacking is unsupported for type %q", name, visual.Type)
		}
		if presentation.Stacking == "percent" && visual.Query.Series.IsZero() && len(visual.Query.Metrics) < 2 {
			return fmt.Errorf("visual %q percent stacking requires a series or multiple metrics", name)
		}
		if presentation.Stacking == "percent" && presentation.DualAxis {
			return fmt.Errorf("visual %q percent stacking cannot use dual axes", name)
		}
	}
	hasSeriesIntent := len(presentation.SeriesOrder) > 0 || len(presentation.SeriesColors) > 0
	if !hasSeriesIntent {
		return nil
	}
	if !oneOf(visual.Type, "line", "area", "bar", "column", "combo", "scatter") {
		return fmt.Errorf("visual %q series intent is unsupported for type %q", name, visual.Type)
	}
	if visual.Query.Series.IsZero() && len(visual.Query.Metrics) < 2 {
		return fmt.Errorf("visual %q series intent requires a series or multiple metrics", name)
	}
	seen := make(map[string]struct{}, len(presentation.SeriesOrder))
	for _, value := range presentation.SeriesOrder {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("visual %q series order value cannot be empty", name)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("visual %q has duplicate series order value %q", name, value)
		}
		seen[value] = struct{}{}
	}
	for value, intent := range presentation.SeriesColors {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("visual %q series color value cannot be empty", name)
		}
		if !validColorIntent(intent) {
			return fmt.Errorf("visual %q has unsupported color intent %q", name, intent)
		}
	}
	return nil
}

func validColorIntent(value string) bool {
	if oneOf(value, "accent", "neutral", "ink", "success", "warning", "danger") {
		return true
	}
	index, err := strconv.Atoi(strings.TrimPrefix(value, "data_"))
	return err == nil && strings.HasPrefix(value, "data_") && index >= 1 && index <= 8
}

func validateDecisionContext(name string, visual Visual) error {
	presentation := visual.Presentation
	hasDecisionContext := len(presentation.Axes) > 0 || len(presentation.ReferenceLines) > 0 || len(presentation.ReferenceBands) > 0 || len(presentation.EventAnnotations) > 0 || len(presentation.Tooltip) > 0
	if !hasDecisionContext {
		return nil
	}
	if !oneOf(visual.Type, "line", "area", "bar", "column", "combo", "scatter", "waterfall", "heatmap") {
		return fmt.Errorf("visual %q decision context is only valid for cartesian visualizations", name)
	}

	axes := make(map[string]struct{}, len(presentation.Axes))
	for _, axis := range presentation.Axes {
		if !oneOf(axis.ID, "x", "primary_y", "secondary_y") {
			return fmt.Errorf("visual %q has unsupported axis %q", name, axis.ID)
		}
		if _, exists := axes[axis.ID]; exists {
			return fmt.Errorf("visual %q has duplicate axis %q", name, axis.ID)
		}
		axes[axis.ID] = struct{}{}
		if axis.ID == "secondary_y" && visual.Type != "combo" {
			return fmt.Errorf("visual %q secondary_y axis is only valid for combo", name)
		}
		if !oneOf(axis.Scale, "", "automatic", "linear", "log") {
			return fmt.Errorf("visual %q axis %q has unsupported scale %q", name, axis.ID, axis.Scale)
		}
		if !oneOf(axis.Zero, "", "automatic", "include", "exclude") {
			return fmt.Errorf("visual %q axis %q has unsupported zero policy %q", name, axis.ID, axis.Zero)
		}
		if !oneOf(axis.TickDensity, "", "automatic", "sparse", "normal", "dense") {
			return fmt.Errorf("visual %q axis %q has unsupported tick density %q", name, axis.ID, axis.TickDensity)
		}
		if !validDisplayUnits(axis.DisplayUnits) {
			return fmt.Errorf("visual %q presentation.axes axis %q has unsupported display_units %q", name, axis.ID, axis.DisplayUnits)
		}
		if axis.Minimum != nil && axis.Maximum != nil && *axis.Minimum >= *axis.Maximum {
			return fmt.Errorf("visual %q axis %q minimum must be less than maximum", name, axis.ID)
		}
		if axis.Scale == "log" {
			if axis.Zero == "include" {
				return fmt.Errorf("visual %q axis %q log scale cannot include zero", name, axis.ID)
			}
			if axis.Minimum != nil && *axis.Minimum <= 0 || axis.Maximum != nil && *axis.Maximum <= 0 {
				return fmt.Errorf("visual %q axis %q log scale requires a positive domain", name, axis.ID)
			}
		}
	}

	tooltip := make(map[string]struct{}, len(presentation.Tooltip))
	for _, field := range presentation.Tooltip {
		if strings.TrimSpace(field) == "" {
			return fmt.Errorf("visual %q tooltip field cannot be empty", name)
		}
		if _, exists := tooltip[field]; exists {
			return fmt.Errorf("visual %q has duplicate tooltip field %q", name, field)
		}
		tooltip[field] = struct{}{}
	}

	ids := make(map[string]struct{}, len(presentation.ReferenceLines)+len(presentation.ReferenceBands)+len(presentation.EventAnnotations))
	validateIdentity := func(id string) error {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("visual %q decision context ID is required", name)
		}
		if _, exists := ids[id]; exists {
			return fmt.Errorf("visual %q has duplicate decision context ID %q", name, id)
		}
		ids[id] = struct{}{}
		return nil
	}
	validateAxis := func(axis string) error {
		if !oneOf(axis, "x", "primary_y", "secondary_y") {
			return fmt.Errorf("visual %q has unsupported decision context axis %q", name, axis)
		}
		if axis == "secondary_y" && visual.Type != "combo" {
			return fmt.Errorf("visual %q secondary_y decision context is only valid for combo", name)
		}
		return nil
	}
	validateTone := func(tone string) error {
		if !oneOf(tone, "", "neutral", "ink", "success", "warning", "danger") {
			return fmt.Errorf("visual %q has unsupported decision context tone %q", name, tone)
		}
		return nil
	}

	for _, line := range presentation.ReferenceLines {
		if !oneOf(visual.Type, "line", "area", "bar", "column", "combo", "scatter", "waterfall") {
			return fmt.Errorf("visual %q reference lines are unsupported for type %q", name, visual.Type)
		}
		if err := validateIdentity(line.ID); err != nil {
			return err
		}
		if err := validateAxis(line.Axis); err != nil {
			return err
		}
		if err := validateReferenceValue(name, "reference line "+line.ID, line.Value); err != nil {
			return err
		}
		if err := validateTone(line.Tone); err != nil {
			return err
		}
	}
	for _, band := range presentation.ReferenceBands {
		if !oneOf(visual.Type, "line", "area", "bar", "column", "combo", "scatter", "waterfall") {
			return fmt.Errorf("visual %q reference bands are unsupported for type %q", name, visual.Type)
		}
		if err := validateIdentity(band.ID); err != nil {
			return err
		}
		if err := validateAxis(band.Axis); err != nil {
			return err
		}
		if err := validateReferenceValue(name, "reference band "+band.ID+" from", band.From); err != nil {
			return err
		}
		if err := validateReferenceValue(name, "reference band "+band.ID+" to", band.To); err != nil {
			return err
		}
		if band.From.Number != nil && band.To.Number != nil && *band.From.Number >= *band.To.Number {
			return fmt.Errorf("visual %q reference band %q from must be less than to", name, band.ID)
		}
		if err := validateTone(band.Tone); err != nil {
			return err
		}
	}
	for _, event := range presentation.EventAnnotations {
		if err := validateIdentity(event.ID); err != nil {
			return err
		}
		if event.Axis != "x" {
			return fmt.Errorf("visual %q event annotation axis must be x", name)
		}
		if strings.TrimSpace(event.Label) == "" {
			return fmt.Errorf("visual %q event annotation %q requires a label", name, event.ID)
		}
		if err := validateReferenceValue(name, "event annotation "+event.ID, event.Value); err != nil {
			return err
		}
		if err := validateTone(event.Tone); err != nil {
			return err
		}
	}
	return nil
}

func validDisplayUnits(value string) bool {
	return oneOf(value, "", "auto", "none", "thousands", "millions", "billions", "trillions")
}

func validateReferenceValue(name, context string, value VisualReferenceValue) error {
	if value.Dataset != "" && value.Field == "" {
		return fmt.Errorf("visual %q %s reference value dataset requires field", name, context)
	}
	branches := 0
	if value.Number != nil {
		branches++
	}
	if value.Text != "" {
		branches++
	}
	if value.Field != "" {
		branches++
	}
	if branches != 1 {
		return fmt.Errorf("visual %q %s value requires exactly one of number, text, or field", name, context)
	}
	if value.Field == "" && value.Reducer != "" {
		return fmt.Errorf("visual %q %s reducer requires a field binding", name, context)
	}
	if value.Field != "" && !oneOf(value.Reducer, "", "first", "last", "minimum", "maximum", "mean", "median") {
		return fmt.Errorf("visual %q %s has unsupported reducer %q", name, context, value.Reducer)
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func (d *Dashboard) validateSelectionInteraction(sourceKind, sourceID, kind string, selection SelectionInteraction) error {
	if len(selection.Mappings) == 0 {
		return fmt.Errorf("%s %q %s requires mappings", sourceKind, sourceID, kind)
	}
	for index, mapping := range selection.Mappings {
		if mapping.Field == "" || mapping.Value == "" {
			return fmt.Errorf("%s %q %s mapping %d requires field and value", sourceKind, sourceID, kind, index)
		}
	}
	for _, target := range selection.Targets {
		if err := d.validateInteractionTarget(sourceKind, sourceID, kind, target); err != nil {
			return err
		}
	}
	return nil
}

func (d *Dashboard) validateInteractionTarget(sourceKind, sourceID, kind, target string) error {
	if target == "" {
		return fmt.Errorf("%s %q %s has empty target", sourceKind, sourceID, kind)
	}
	if _, ok := d.Visuals[target]; !ok {
		return fmt.Errorf("%s %q %s references unknown target %q", sourceKind, sourceID, kind, target)
	}
	return nil
}

func (d *Dashboard) validatePages() error {
	seenPages := map[string]struct{}{}
	for index, page := range d.Pages {
		if page.ID == "" || page.Title == "" {
			return fmt.Errorf("page %d requires id and title", index)
		}
		page = page.WithDefaults()
		if _, exists := seenPages[page.ID]; exists {
			return fmt.Errorf("duplicate page id %q", page.ID)
		}
		seenPages[page.ID] = struct{}{}
		for _, visual := range page.Visuals {
			if visual.ID == "" || visual.Kind == "" {
				return fmt.Errorf("page %q has a visual missing id or kind", page.ID)
			}
			if err := validatePlacement(page, visual); err != nil {
				return err
			}
			switch visual.Kind {
			case "header":
				if visual.Visual != "" || visual.Binding.ID != "" {
					return fmt.Errorf("page %q header %q must not reference a visual or filter binding", page.ID, visual.ID)
				}
			case "slicer":
				if visual.Visual != "" {
					return fmt.Errorf("page %q slicer %q must not reference a visual", page.ID, visual.ID)
				}
				if visual.Binding.ID == "" || !d.bindingReferenceExists(page.ID, visual.Binding) {
					return fmt.Errorf("page %q slicer %q references unknown filter binding %s/%s", page.ID, visual.ID, visual.Binding.Scope, visual.Binding.ID)
				}
			case "visual":
				if visual.Visual == "" {
					return fmt.Errorf("page %q visual %q requires visual", page.ID, visual.ID)
				}
				if _, ok := d.Visuals[visual.Visual]; !ok {
					return fmt.Errorf("page %q references unknown visual %q", page.ID, visual.Visual)
				}
				if visual.Binding.ID != "" {
					return fmt.Errorf("page %q visual %q must not reference a filter binding", page.ID, visual.ID)
				}
			default:
				return fmt.Errorf("page %q visual %q has unsupported kind %q", page.ID, visual.ID, visual.Kind)
			}
		}
	}
	return nil
}
