package runtime

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	"github.com/flidai/leapview/internal/dashboard/reportmodel"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

// visualPlan is the runtime query plan derived from the immutable compiled
// visualization definition. It deliberately contains no authoring model or
// renderer-native configuration.
type visualPlan struct {
	Definition visualizationdefinition.Definition
	Table      string
	Dimensions []visualizationdefinition.FieldBinding
	Series     *visualizationdefinition.FieldBinding
	Measures   []visualizationdefinition.FieldBinding
	Time       *visualizationdefinition.TimeBinding
	Sort       []visualizationdefinition.Sort
	Limit      int
}

func newVisualPlan(definition visualizationdefinition.Definition) (visualPlan, error) {
	plan := visualPlan{Definition: definition}
	switch definition.Query.Kind {
	case visualizationdefinition.QueryAggregate:
		query := definition.Query.Aggregate
		if query == nil {
			return visualPlan{}, fmt.Errorf("visualization %q has no aggregate binding", definition.ID)
		}
		plan.Table, plan.Dimensions, plan.Series, plan.Measures, plan.Time, plan.Sort, plan.Limit = query.TableID, query.Dimensions, query.Series, query.Measures, query.Time, query.Sort, int(query.Limit)
	case visualizationdefinition.QuerySpatial:
		query := definition.Query.Spatial
		if query == nil {
			return visualPlan{}, fmt.Errorf("visualization %q has no spatial binding", definition.ID)
		}
		plan.Table, plan.Dimensions, plan.Series, plan.Measures, plan.Time, plan.Sort, plan.Limit = query.TableID, query.Dimensions, query.Series, query.Measures, query.Time, query.Sort, int(query.Limit)
	default:
		return visualPlan{}, fmt.Errorf("visualization %q query kind %q is not a chart query", definition.ID, definition.Query.Kind)
	}
	return plan, nil
}

func (visual visualPlan) ResultShape() visualizationdefinition.ResultShape {
	return visual.Definition.Query.ResultShape
}

func (visual visualPlan) Title() string {
	base, err := visualizationir.SpecificationBase(visual.Definition.Spec)
	if err != nil {
		return visual.Definition.ID
	}
	return base.Title
}

func (visual visualPlan) KindAndType() (string, string) {
	switch value := visual.Definition.Spec.Value.(type) {
	case *visualizationir.KPIVisualizationSpec:
		return "kpi", "kpi"
	case *visualizationir.CartesianVisualizationSpec:
		return "chart", string(value.Mark)
	case *visualizationir.PointVisualizationSpec:
		return "chart", "scatter"
	case *visualizationir.ProportionalVisualizationSpec:
		return "chart", string(value.Mark)
	case *visualizationir.HierarchyVisualizationSpec:
		return "chart", string(value.Mark)
	case *visualizationir.PolarVisualizationSpec:
		return "chart", string(value.Mark)
	case *visualizationir.GeographicVisualizationSpec:
		return "chart", "map"
	default:
		return "chart", ""
	}
}

func (visual visualPlan) Interaction() (visualizationir.VisualizationInteraction, bool) {
	base, err := visualizationir.SpecificationBase(visual.Definition.Spec)
	if err != nil || len(base.Interactions) == 0 {
		return visualizationir.VisualizationInteraction{}, false
	}
	return base.Interactions[0], true
}

func (visual visualPlan) HistogramBins() int {
	if value, ok := visual.Definition.Spec.Value.(*visualizationir.CartesianVisualizationSpec); ok && value.Presentation.HistogramBins != nil {
		return int(*value.Presentation.HistogramBins)
	}
	return 20
}

func fieldRef(field string, alias string) reportdef.QueryField {
	return reportdef.QueryField{Field: field, Alias: alias}
}

func queryFieldRef(ref visualizationdefinition.FieldBinding, alias string) reportdef.QueryField {
	return reportdef.QueryField{
		Field: ref.FieldID,
		Alias: alias,
	}
}

func queryDimensionFields(dimensions []visualizationdefinition.FieldBinding) []string {
	fields := make([]string, len(dimensions))
	for i, dimension := range dimensions {
		fields[i] = dimension.FieldID
	}
	return fields
}

func displayField(field string) string {
	parts := strings.Split(field, ".")
	return parts[len(parts)-1]
}

func visualSorts(visual visualPlan) []reportdef.QuerySort {
	if len(visual.Sort) == 0 {
		return []reportdef.QuerySort{{Field: defaultSortColumn(visual), Direction: "asc"}}
	}
	sorts := make([]reportdef.QuerySort, 0, len(visual.Sort))
	for _, sort := range visual.Sort {
		field := sort.FieldID
		if field == "" {
			field = defaultSortColumn(visual)
		}
		if field != "value" && field != "series" {
			for index, dimension := range visual.Dimensions {
				if field == dimension.FieldID || field == dimension.Alias || field == displayField(dimension.FieldID) {
					field = dimensionSortColumn(visual.ResultShape(), index)
					break
				}
			}
			if visual.Series != nil && (field == visual.Series.FieldID || field == visual.Series.Alias || field == displayField(visual.Series.FieldID)) {
				field = "series"
			}
		}
		sorts = append(sorts, reportdef.QuerySort{Field: field, Direction: sort.Direction})
	}
	return sorts
}

func measureLabel(name string, measure semanticmodel.MetricMeasure) string {
	if strings.TrimSpace(measure.Label) != "" {
		return measure.Label
	}
	return name
}

func aggregateMemberMetadata(model *semanticmodel.Model, name string) semanticmodel.MetricMeasure {
	if model == nil {
		return semanticmodel.MetricMeasure{Name: name, Field: name}
	}
	if measure, err := model.ResolveMeasure(name); err == nil {
		return measure
	}
	if metric, ok := model.Metrics[name]; ok {
		return semanticmodel.MetricMeasure{
			Name: name, Field: name, Label: metric.Label, Description: metric.Description,
			Unit: metric.Unit, Format: metric.Format, Hidden: metric.Hidden,
		}
	}
	return semanticmodel.MetricMeasure{Name: name, Field: name}
}

func optionInt(options map[string]any, key string, fallback, minValue, maxValue int) int {
	if options == nil {
		return fallback
	}
	var value int
	switch typed := options[key].(type) {
	case int:
		value = typed
	case int64:
		value = int(typed)
	case float64:
		value = int(typed)
	case string:
		parsed, err := strconv.Atoi(typed)
		if err != nil {
			return fallback
		}
		value = parsed
	default:
		return fallback
	}
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func datumFloat(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case string:
		parsed, _ := strconv.ParseFloat(typed, 64)
		return parsed
	default:
		return 0
	}
}

func formatBinLabel(start, end float64) string {
	if math.Abs(start-end) < 0.000001 {
		return strconv.FormatFloat(round(start), 'f', -1, 64)
	}
	return fmt.Sprintf("%s-%s", strconv.FormatFloat(round(start), 'f', -1, 64), strconv.FormatFloat(round(end), 'f', -1, 64))
}

func distributionSorts(visual visualPlan) []reportdef.QuerySort {
	if len(visual.Sort) == 0 {
		return nil
	}
	sorts := make([]reportdef.QuerySort, 0, len(visual.Sort))
	for _, sortSpec := range visual.Sort {
		field := sortSpec.FieldID
		if field == "" {
			field = "label"
		}
		if field != "label" && field != "min" && field != "q1" && field != "median" && field != "q3" && field != "max" {
			field = "label"
		}
		sorts = append(sorts, reportdef.QuerySort{Field: field, Direction: sortSpec.Direction})
	}
	return sorts
}

func defaultSortColumn(visual visualPlan) string {
	switch visual.ResultShape() {
	case visualizationdefinition.ResultMatrixCells:
		return "row"
	case visualizationdefinition.ResultGraphEdges:
		return "source"
	case visualizationdefinition.ResultGeographicFeatures:
		return "name"
	case visualizationdefinition.ResultHierarchyNodes:
		return "value"
	default:
		return "label"
	}
}

func dimensionSortColumn(shape visualizationdefinition.ResultShape, index int) string {
	switch shape {
	case visualizationdefinition.ResultMatrixCells:
		if index == 1 {
			return "chart_column"
		}
		return "row"
	case visualizationdefinition.ResultGraphEdges:
		if index == 1 {
			return "target"
		}
		return "source"
	case visualizationdefinition.ResultGeographicFeatures:
		return "name"
	case visualizationdefinition.ResultHierarchyNodes:
		return fmt.Sprintf("level_%d", index)
	default:
		return "label"
	}
}

func visualInteractionConfig(selection dashboardauthoring.SelectionInteraction) dashboard.InteractionConfig {
	return interactionConfig("point_selection", selection)
}

func tableInteractionConfig(selection dashboardauthoring.SelectionInteraction) dashboard.InteractionConfig {
	return interactionConfig("row_selection", selection)
}

func interactionConfig(kind string, selection dashboardauthoring.SelectionInteraction) dashboard.InteractionConfig {
	mappings := make([]dashboard.InteractionConfigMapping, 0, len(selection.Mappings))
	for _, mapping := range selection.Mappings {
		mappings = append(mappings, dashboard.InteractionConfigMapping{
			Field: mapping.Field,
			Fact:  mapping.Fact,
			Grain: mapping.Grain,
			Value: mapping.Value,
			Label: mapping.Label,
		})
	}
	return dashboard.InteractionConfig{
		Kind:     kind,
		Toggle:   selection.Toggle,
		Mappings: mappings,
		Targets:  append([]string{}, selection.Targets...),
	}
}

func compiledInteractionConfig(interaction visualizationir.VisualizationInteraction) dashboard.InteractionConfig {
	mappings := make([]dashboard.InteractionConfigMapping, 0, len(interaction.Mappings))
	for _, mapping := range interaction.Mappings {
		fact, grain, label := "", "", ""
		if mapping.TargetFactID != nil {
			fact = *mapping.TargetFactID
		}
		if mapping.Grain != nil {
			grain = *mapping.Grain
		}
		if mapping.Label != nil {
			label = mapping.Label.Field
		}
		mappings = append(mappings, dashboard.InteractionConfigMapping{Field: mapping.TargetFieldID, Fact: fact, Grain: grain, Value: mapping.Source.Field, Label: label})
	}
	targets := make([]string, 0, len(interaction.Targets))
	for _, target := range interaction.Targets {
		if target.Effect != visualizationir.VisualizationInteractionEffectNone {
			targets = append(targets, target.VisualID)
		}
	}
	return dashboard.InteractionConfig{Kind: interaction.ID, Toggle: interaction.Mode == visualizationir.VisualizationSelectionModeMultiple, Mappings: mappings, Targets: targets}
}

func selectedEntries(filters dashboard.Filters, sourceKind, sourceID string) []dashboard.InteractionSelectionEntry {
	entries := []dashboard.InteractionSelectionEntry{}
	for _, selection := range filters.Selections {
		if selection.SourceKind != sourceKind || selection.SourceID != sourceID {
			continue
		}
		for _, entry := range selection.Entries {
			entries = append(entries, copySelectionEntry(entry))
		}
	}
	return entries
}

func selectedHighlights(runtime *modelRuntime, report *dashboarddefinition.Definition, filters dashboard.Filters, targetID string) ([]visualizationir.VisualizationHighlightState, error) {
	highlights := []visualizationir.VisualizationHighlightState{}
	for _, selection := range filters.Selections {
		if len(selection.Entries) == 0 {
			continue
		}
		resolved, err := reportmodel.ResolveCompiledSelectionInteraction(report, runtime.model, selection.SourceKind, selection.SourceID)
		if err != nil {
			return nil, err
		}
		if resolvedSelectionEffect(resolved, "visual", targetID) != string(visualizationir.VisualizationInteractionEffectHighlight) {
			continue
		}
		state := visualizationir.VisualizationHighlightState{
			SourceVisualID: selection.SourceID, InteractionID: selection.InteractionKind,
			Entries: []visualizationir.VisualizationHighlightEntry{}, Label: selection.Label,
		}
		for _, entry := range selection.Entries {
			next := visualizationir.VisualizationHighlightEntry{Mappings: []visualizationir.VisualizationHighlightMapping{}, Label: entry.Label}
			for _, mapping := range entry.Mappings {
				next.Mappings = append(next.Mappings, visualizationir.VisualizationHighlightMapping{
					TargetFieldID: mapping.Field, TargetFactID: optionalRuntimeString(mapping.Fact),
					Grain: optionalRuntimeString(mapping.Grain), Value: mapping.Value, Label: optionalRuntimeString(mapping.Label),
				})
			}
			state.Entries = append(state.Entries, next)
		}
		highlights = append(highlights, state)
	}
	for _, selection := range filters.SpatialSelections {
		resolved, err := reportmodel.ResolveCompiledSpatialSelectionInteraction(report, runtime.model, selection.VisualID, selection.InteractionID)
		if err != nil {
			return nil, err
		}
		if resolvedSpatialSelectionEffect(resolved, "visual", targetID) != string(visualizationir.VisualizationInteractionEffectHighlight) {
			continue
		}
		geometry := selection.Geometry
		highlights = append(highlights, visualizationir.VisualizationHighlightState{
			SourceVisualID: selection.VisualID, InteractionID: selection.InteractionID,
			Entries:                 []visualizationir.VisualizationHighlightEntry{},
			SpatialGeometry:         &geometry,
			SpatialLatitudeFieldID:  optionalRuntimeString(resolved.Latitude.Field),
			SpatialLongitudeFieldID: optionalRuntimeString(resolved.Longitude.Field),
			Label:                   "Spatial selection",
		})
	}
	return highlights, nil
}

func optionalRuntimeString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func selectedSpatialState(filters dashboard.Filters, visualID string) *visualizationir.VisualizationSpatialSelectionState {
	for index := len(filters.SpatialSelections) - 1; index >= 0; index-- {
		selection := filters.SpatialSelections[index]
		if selection.VisualID == visualID {
			return &visualizationir.VisualizationSpatialSelectionState{VisualID: visualID, InteractionID: selection.InteractionID, Geometry: selection.Geometry}
		}
	}
	return nil
}

func copySelectionEntry(entry dashboard.InteractionSelectionEntry) dashboard.InteractionSelectionEntry {
	next := dashboard.InteractionSelectionEntry{
		Label:    entry.Label,
		Mappings: make([]dashboard.InteractionSelectionMapping, len(entry.Mappings)),
	}
	copy(next.Mappings, entry.Mappings)
	return next
}

func normalizeDatumValue(value any) any {
	switch typed := normalizeDBValue(value).(type) {
	case float64:
		return round(typed)
	case float32:
		return round(float64(typed))
	case *big.Int:
		if typed != nil && typed.BitLen() <= 53 {
			return float64(typed.Int64())
		}
		return typed
	case big.Int:
		if typed.BitLen() <= 53 {
			return float64(typed.Int64())
		}
		return typed
	default:
		return typed
	}
}

func normalizeDBValue(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case []byte:
		return string(typed)
	case time.Time:
		return typed.Format("2006-01-02")
	case float32:
		return round(float64(typed))
	case float64:
		return round(typed)
	default:
		return typed
	}
}

func formatMetric(value float64, format string) string {
	switch format {
	case "currency":
		return formatCurrency(value)
	case "integer":
		return formatInt(int64(math.Round(value)))
	case "decimal":
		return fmt.Sprintf("%.2f", value)
	default:
		return fmt.Sprintf("%.2f", value)
	}
}

func formatCurrency(value float64) string {
	if value >= 1000000 {
		return fmt.Sprintf("R$ %.1fm", value/1000000)
	}
	if value >= 1000 {
		return fmt.Sprintf("R$ %.1fk", value/1000)
	}
	return fmt.Sprintf("R$ %.0f", value)
}

func formatInt(value int64) string {
	if value >= 1000000 {
		return fmt.Sprintf("%.1fm", float64(value)/1000000)
	}
	if value >= 1000 {
		return fmt.Sprintf("%.1fk", float64(value)/1000)
	}
	return fmt.Sprintf("%d", value)
}

func round(value float64) float64 {
	return math.Round(value*100) / 100
}
