package authoring

import (
	"fmt"
	"strings"
)

func validateVisualQueryShape(name string, visual Visual) error {
	dimensionCount := len(visual.Query.Dimensions)
	if visual.Query.Time.Field != "" {
		dimensionCount++
	}
	if visual.KindOrDefault() == "kpi" {
		if visual.ResultShape() != "single_value" {
			return fmt.Errorf("visual %q kind kpi requires shape single_value", name)
		}
		if len(visual.Query.Measures) != 1 {
			return fmt.Errorf("visual %q kind kpi requires exactly one query measure", name)
		}
		if dimensionCount != 0 {
			return fmt.Errorf("visual %q kind kpi does not support query dimensions", name)
		}
		if !visual.Query.Series.IsZero() {
			return fmt.Errorf("visual %q kind kpi does not support series", name)
		}
		return nil
	}
	shape := visual.ResultShape()
	if (shape == "binned_measure" || shape == "distribution") && strings.TrimSpace(visual.Query.Table) == "" {
		return fmt.Errorf("visual %q shape %s requires query.table", name, shape)
	}
	switch shape {
	case "point":
		if len(visual.Query.Measures) == 0 {
			return fmt.Errorf("visual %q shape point requires query measures", name)
		}
	case "ohlc":
		if len(visual.Query.Measures) != 4 {
			return fmt.Errorf("visual %q shape ohlc requires exactly four query measures", name)
		}
	case "category_multi_measure":
		if len(visual.Query.Measures) < 2 {
			return fmt.Errorf("visual %q shape category_multi_measure requires at least two query measures", name)
		}
	default:
		if len(visual.Query.Measures) != 1 {
			return fmt.Errorf("visual %q requires exactly one query measure", name)
		}
	}
	if len(visual.Query.Measures) == 0 {
		return fmt.Errorf("visual %q requires exactly one query measure", name)
	}
	switch shape {
	case "point":
		if dimensionCount == 0 {
			return fmt.Errorf("visual %q shape point requires at least one stable query dimension or time field", name)
		}
		if !visual.Query.Series.IsZero() {
			return fmt.Errorf("visual %q shape point does not support query series; bind point.series to a dimension alias", name)
		}
	case "category_value":
		if dimensionCount != 1 {
			return fmt.Errorf("visual %q shape category_value requires exactly one query dimension", name)
		}
		if !visual.Query.Series.IsZero() {
			return fmt.Errorf("visual %q shape category_value does not support series", name)
		}
	case "category_series_value":
		if dimensionCount != 1 {
			return fmt.Errorf("visual %q shape category_series_value requires exactly one query dimension", name)
		}
		if visual.Query.Series.IsZero() {
			return fmt.Errorf("visual %q shape category_series_value requires query series", name)
		}
	case "category_multi_measure":
		if dimensionCount != 1 {
			return fmt.Errorf("visual %q shape category_multi_measure requires exactly one query dimension", name)
		}
		if !visual.Query.Series.IsZero() {
			return fmt.Errorf("visual %q shape category_multi_measure does not support series", name)
		}
	case "category_delta":
		if dimensionCount != 1 {
			return fmt.Errorf("visual %q shape category_delta requires exactly one query dimension", name)
		}
		if !visual.Query.Series.IsZero() {
			return fmt.Errorf("visual %q shape category_delta does not support series", name)
		}
	case "binned_measure":
		if dimensionCount != 0 {
			return fmt.Errorf("visual %q shape binned_measure does not support query dimensions", name)
		}
		if !visual.Query.Series.IsZero() {
			return fmt.Errorf("visual %q shape binned_measure does not support series", name)
		}
	case "hierarchy":
		if dimensionCount == 0 {
			return fmt.Errorf("visual %q shape hierarchy requires at least one query dimension", name)
		}
		if !visual.Query.Series.IsZero() {
			return fmt.Errorf("visual %q shape hierarchy does not support series", name)
		}
	case "single_value":
		if len(visual.Query.Dimensions) > 1 {
			return fmt.Errorf("visual %q shape single_value supports at most one query dimension", name)
		}
		if !visual.Query.Series.IsZero() {
			return fmt.Errorf("visual %q shape single_value does not support series", name)
		}
	case "matrix":
		if len(visual.Query.Dimensions) != 2 {
			return fmt.Errorf("visual %q shape matrix requires exactly two query dimensions", name)
		}
		if !visual.Query.Series.IsZero() {
			return fmt.Errorf("visual %q shape matrix does not support series", name)
		}
	case "graph":
		if len(visual.Query.Dimensions) != 2 {
			return fmt.Errorf("visual %q shape graph requires exactly two query dimensions", name)
		}
		if !visual.Query.Series.IsZero() {
			return fmt.Errorf("visual %q shape graph does not support series", name)
		}
	case "geo":
		if len(visual.Query.Dimensions) == 0 {
			return fmt.Errorf("visual %q shape geo requires query dimensions", name)
		}
		if !visual.Query.Series.IsZero() {
			return fmt.Errorf("visual %q shape geo does not support series", name)
		}
	case "ohlc":
		if len(visual.Query.Dimensions) != 1 {
			return fmt.Errorf("visual %q shape ohlc requires exactly one query dimension", name)
		}
		if !visual.Query.Series.IsZero() {
			return fmt.Errorf("visual %q shape ohlc does not support series", name)
		}
	case "distribution":
		if len(visual.Query.Dimensions) != 1 {
			return fmt.Errorf("visual %q shape distribution requires exactly one query dimension", name)
		}
		if !visual.Query.Series.IsZero() {
			return fmt.Errorf("visual %q shape distribution does not support series", name)
		}
	}
	return nil
}

func validatePointVisual(name string, visual Visual) error {
	point := visual.Point
	if len(point.Identity) == 0 {
		return fmt.Errorf("visual %q scatter requires point.identity", name)
	}
	if strings.TrimSpace(point.X) == "" || strings.TrimSpace(point.Y) == "" {
		return fmt.Errorf("visual %q scatter requires point.x and point.y", name)
	}

	stable := payloadKeySet{}
	measures := payloadKeySet{}
	all := payloadKeySet{}
	add := func(keys payloadKeySet, field, alias string) {
		if field == "" {
			return
		}
		value := defaultString(alias, fieldRefAlias(field))
		keys[value] = struct{}{}
		all[value] = struct{}{}
	}
	for _, field := range visual.Query.Dimensions {
		add(stable, field.Field, field.Alias)
	}
	add(stable, visual.Query.Time.Field, visual.Query.Time.Alias)
	for _, field := range visual.Query.Measures {
		add(measures, field.Field, field.Alias)
	}

	seenIdentity := payloadKeySet{}
	for _, field := range point.Identity {
		if _, ok := all[field]; !ok {
			return fmt.Errorf("visual %q point.identity references unknown query alias %q", name, field)
		}
		if _, ok := stable[field]; !ok {
			return fmt.Errorf("visual %q point identity field %q must reference a dimension or time alias", name, field)
		}
		if seenIdentity.Contains(field) {
			return fmt.Errorf("visual %q has duplicate point.identity field %q", name, field)
		}
		seenIdentity[field] = struct{}{}
	}

	if _, ok := all[point.X]; !ok {
		return fmt.Errorf("visual %q point.x references unknown query alias %q", name, point.X)
	}
	if !measures.Contains(point.X) && !stable.Contains(point.X) {
		return fmt.Errorf("visual %q point.x field %q must reference a measure or time alias", name, point.X)
	}
	if stable.Contains(point.X) && point.X != defaultString(visual.Query.Time.Alias, fieldRefAlias(visual.Query.Time.Field)) {
		return fmt.Errorf("visual %q point.x field %q must reference a measure or time alias", name, point.X)
	}
	if _, ok := all[point.Y]; !ok {
		return fmt.Errorf("visual %q point.y references unknown query alias %q", name, point.Y)
	}
	if !measures.Contains(point.Y) {
		return fmt.Errorf("visual %q point.y field %q must reference a measure", name, point.Y)
	}
	if point.X == point.Y {
		return fmt.Errorf("visual %q point.x and point.y must reference independent aliases", name)
	}
	if point.Size != "" {
		if _, ok := all[point.Size]; !ok {
			return fmt.Errorf("visual %q point.size references unknown query alias %q", name, point.Size)
		}
		if !measures.Contains(point.Size) {
			return fmt.Errorf("visual %q point.size field %q must reference a measure", name, point.Size)
		}
	}
	for _, channel := range []struct {
		name  string
		field string
	}{
		{name: "color", field: point.Color},
		{name: "series", field: point.Series},
		{name: "label", field: point.Label},
	} {
		if channel.field != "" && !all.Contains(channel.field) {
			return fmt.Errorf("visual %q point.%s references unknown query alias %q", name, channel.name, channel.field)
		}
	}
	if point.Series != "" && !stable.Contains(point.Series) {
		return fmt.Errorf("visual %q point.series field %q must reference a dimension or time alias", name, point.Series)
	}

	seenTooltip := payloadKeySet{}
	for _, field := range point.Tooltip {
		if !all.Contains(field) {
			return fmt.Errorf("visual %q point.tooltip references unknown query alias %q", name, field)
		}
		if seenTooltip.Contains(field) {
			return fmt.Errorf("visual %q has duplicate point.tooltip field %q", name, field)
		}
		seenTooltip[field] = struct{}{}
	}

	if point.Color != "" {
		expectedKind := "categorical"
		if measures.Contains(point.Color) {
			expectedKind = "quantitative"
		}
		if point.ColorScale.Kind != "" && point.ColorScale.Kind != expectedKind {
			return fmt.Errorf("visual %q point.color_scale kind %q does not match %s color field %q", name, point.ColorScale.Kind, expectedKind, point.Color)
		}
	} else if point.ColorScale != (VisualPointColorScale{}) {
		return fmt.Errorf("visual %q point.color_scale requires point.color", name)
	}
	if point.ColorScale.Kind != "" && !oneOf(point.ColorScale.Kind, "categorical", "quantitative") {
		return fmt.Errorf("visual %q point.color_scale has unsupported kind %q", name, point.ColorScale.Kind)
	}
	if point.ColorScale.Minimum != nil && point.ColorScale.Maximum != nil && *point.ColorScale.Minimum >= *point.ColorScale.Maximum {
		return fmt.Errorf("visual %q point.color_scale minimum must be less than maximum", name)
	}
	if point.Size == "" && point.SizeScale != (VisualPointSizeScale{}) {
		return fmt.Errorf("visual %q point.size_scale requires point.size", name)
	}
	if point.SizeScale.Minimum != nil && point.SizeScale.Maximum != nil && *point.SizeScale.Minimum >= *point.SizeScale.Maximum {
		return fmt.Errorf("visual %q point.size_scale minimum must be less than maximum", name)
	}
	if point.SizeScale.MinimumPixels < 0 || point.SizeScale.MaximumPixels < 0 ||
		(point.SizeScale.MinimumPixels > 0 && point.SizeScale.MaximumPixels > 0 && point.SizeScale.MinimumPixels >= point.SizeScale.MaximumPixels) {
		return fmt.Errorf("visual %q point.size_scale pixel range must be increasing and non-negative", name)
	}

	if !oneOf(point.Overplot.Strategy, "", "show_all", "opacity") {
		return fmt.Errorf("visual %q point.overplot has unsupported strategy %q", name, point.Overplot.Strategy)
	}
	if point.Overplot.Opacity < 0 || point.Overplot.Opacity > 1 {
		return fmt.Errorf("visual %q point.overplot opacity must be between zero and one", name)
	}
	if !oneOf(point.Overplot.LargeMode, "", "automatic", "always", "never") {
		return fmt.Errorf("visual %q point.overplot has unsupported large_mode %q", name, point.Overplot.LargeMode)
	}
	if point.Overplot.LargeThreshold < 0 {
		return fmt.Errorf("visual %q point.overplot large_threshold must not be negative", name)
	}

	seenBrush := payloadKeySet{}
	for _, gesture := range point.Brush {
		if !oneOf(gesture, "rectangle", "lasso") {
			return fmt.Errorf("visual %q has unsupported point.brush gesture %q", name, gesture)
		}
		if seenBrush.Contains(gesture) {
			return fmt.Errorf("visual %q has duplicate point.brush gesture %q", name, gesture)
		}
		seenBrush[gesture] = struct{}{}
	}
	if len(point.Brush) > 0 && visual.Interaction.PointSelection.IsZero() {
		return fmt.Errorf("visual %q point.brush requires point_selection", name)
	}
	return nil
}

func ValidateVisualPointSelectionMappingKeys(name string, visual Visual) error {
	if !supportsPointSelection(visual) {
		return fmt.Errorf("visual %q type %q shape %q does not support point_selection", name, visual.Type, visual.ResultShape())
	}
	if visual.ResultShape() == "geo" {
		return validateGeographicPointSelectionMappingKeys(name, visual)
	}
	keys := visualPayloadKeys(visual)
	for index, mapping := range visual.Interaction.PointSelection.Mappings {
		if !keys.Contains(mapping.Value) {
			return fmt.Errorf("visual %q interaction mapping %d references unknown value key %q for shape %q", name, index, mapping.Value, visual.ResultShape())
		}
		if mapping.Label != "" && !keys.Contains(mapping.Label) {
			return fmt.Errorf("visual %q interaction mapping %d references unknown label key %q for shape %q", name, index, mapping.Label, visual.ResultShape())
		}
	}
	return nil
}

func validateGeographicPointSelectionMappingKeys(name string, visual Visual) error {
	selectable := false
	for _, layer := range visual.Geo.Layers {
		if layer.Kind == "point" || layer.Kind == "choropleth" {
			selectable = true
			break
		}
	}
	if !selectable {
		return fmt.Errorf("visual %q geographic point_selection requires at least one point or choropleth layer", name)
	}

	stableAliases := payloadKeySet{}
	allAliases := payloadKeySet{}
	add := func(keys payloadKeySet, field, alias string) {
		if field != "" {
			keys[defaultString(alias, fieldRefAlias(field))] = struct{}{}
		}
	}
	for _, field := range visual.Query.Dimensions {
		add(stableAliases, field.Field, field.Alias)
		add(allAliases, field.Field, field.Alias)
	}
	add(stableAliases, visual.Query.Time.Field, visual.Query.Time.Alias)
	add(allAliases, visual.Query.Time.Field, visual.Query.Time.Alias)
	for _, field := range visual.Query.Measures {
		add(allAliases, field.Field, field.Alias)
	}
	for index, mapping := range visual.Interaction.PointSelection.Mappings {
		if !allAliases.Contains(mapping.Value) {
			return fmt.Errorf("visual %q interaction mapping %d references unknown value query alias %q for shape %q", name, index, mapping.Value, visual.ResultShape())
		}
		if !stableAliases.Contains(mapping.Value) {
			return fmt.Errorf("visual %q interaction mapping %d value query alias %q must reference a dimension or time field", name, index, mapping.Value)
		}
		if mapping.Label != "" && !allAliases.Contains(mapping.Label) {
			return fmt.Errorf("visual %q interaction mapping %d references unknown label query alias %q for shape %q", name, index, mapping.Label, visual.ResultShape())
		}
	}
	return nil
}

func validateSpatialSelectionInteraction(name string, visual Visual) error {
	selection := visual.Interaction.SpatialSelection
	if len(selection.Gestures) == 0 {
		return fmt.Errorf("visual %q spatial_selection requires gestures", name)
	}
	seen := map[string]struct{}{}
	for _, gesture := range selection.Gestures {
		if gesture != "box" && gesture != "lasso" && gesture != "radius" {
			return fmt.Errorf("visual %q spatial_selection has unsupported gesture %q", name, gesture)
		}
		if _, ok := seen[gesture]; ok {
			return fmt.Errorf("visual %q spatial_selection has duplicate gesture %q", name, gesture)
		}
		seen[gesture] = struct{}{}
	}
	if len(selection.Targets) == 0 {
		return fmt.Errorf("visual %q spatial_selection requires targets", name)
	}
	if selection.Latitude.Source == "" || selection.Latitude.Field == "" || selection.Longitude.Source == "" || selection.Longitude.Field == "" {
		return fmt.Errorf("visual %q spatial_selection latitude and longitude require source and field", name)
	}
	if selection.Latitude.Field == selection.Longitude.Field && selection.Latitude.Fact == selection.Longitude.Fact {
		return fmt.Errorf("visual %q spatial_selection latitude and longitude target fields must differ", name)
	}
	stableAliases := payloadKeySet{}
	for _, field := range visual.Query.Dimensions {
		stableAliases[defaultString(field.Alias, fieldRefAlias(field.Field))] = struct{}{}
	}
	if visual.Query.Time.Field != "" {
		stableAliases[defaultString(visual.Query.Time.Alias, fieldRefAlias(visual.Query.Time.Field))] = struct{}{}
	}
	for axis, mapping := range map[string]SpatialSelectionMapping{"latitude": selection.Latitude, "longitude": selection.Longitude} {
		if !stableAliases.Contains(mapping.Source) {
			return fmt.Errorf("visual %q spatial_selection %s references unknown stable query alias %q", name, axis, mapping.Source)
		}
		if strings.Contains(mapping.Field, ".") && mapping.Fact == "" {
			return fmt.Errorf("visual %q spatial_selection %s physical field %q requires fact", name, axis, mapping.Field)
		}
		if !strings.Contains(mapping.Field, ".") && mapping.Fact != "" {
			return fmt.Errorf("visual %q spatial_selection %s semantic field %q must not specify fact", name, axis, mapping.Field)
		}
	}
	coordinateLayer := false
	for _, layer := range visual.Geo.Layers {
		if layer.Latitude == selection.Latitude.Source && layer.Longitude == selection.Longitude.Source && oneOf(layer.Kind, "point", "heat", "density", "path") {
			coordinateLayer = true
			break
		}
	}
	if !coordinateLayer {
		return fmt.Errorf("visual %q spatial_selection source coordinates must match one coordinate layer", name)
	}
	return nil
}

func supportsPointSelection(visual Visual) bool {
	switch visual.Type {
	case "radar":
		return false
	}
	return true
}

type payloadKeySet map[string]struct{}

func (keys payloadKeySet) Contains(key string) bool {
	_, ok := keys[key]
	return ok
}

func visualPayloadKeys(visual Visual) payloadKeySet {
	switch visual.ResultShape() {
	case "point":
		return visualQueryPayloadKeys(visual.Query)
	case "category_series_value", "category_multi_measure":
		return payloadKeys("label", "series", "value", "selected")
	case "category_delta":
		return payloadKeys("label", "value", "start", "end", "positive", "selected")
	case "binned_measure":
		return payloadKeys("label", "binStart", "binEnd", "value")
	case "hierarchy":
		keys := payloadKeys("node", "parent", "value")
		for _, field := range visual.Query.Dimensions {
			keys[defaultString(field.Alias, fieldRefAlias(field.Field))] = struct{}{}
		}
		if visual.Query.Time.Field != "" {
			keys[defaultString(visual.Query.Time.Alias, fieldRefAlias(visual.Query.Time.Field))] = struct{}{}
		}
		return keys
	case "single_value":
		return payloadKeys("label", "value", "series", "selected")
	case "matrix":
		return payloadKeys("row", "column", "value", "selected")
	case "graph":
		return payloadKeys("source", "target", "value")
	case "geo":
		return payloadKeys("name", "value", "selected")
	case "ohlc":
		return payloadKeys("label", "open", "close", "low", "high")
	case "distribution":
		return payloadKeys("label", "min", "q1", "median", "q3", "max")
	default:
		return payloadKeys("label", "value", "selected")
	}
}

func visualQueryPayloadKeys(query VisualQuery) payloadKeySet {
	keys := payloadKeySet{}
	for alias := range visualQueryAliases(query) {
		keys[alias] = struct{}{}
	}
	return keys
}

func payloadKeys(values ...string) payloadKeySet {
	keys := make(payloadKeySet, len(values))
	for _, value := range values {
		keys[value] = struct{}{}
	}
	return keys
}
