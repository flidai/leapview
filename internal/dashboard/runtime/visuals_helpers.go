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
	Metrics    []visualizationdefinition.FieldBinding
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
		plan.Table, plan.Dimensions, plan.Series, plan.Metrics, plan.Time, plan.Sort, plan.Limit = query.TableID, query.Dimensions, query.Series, query.Metrics, query.Time, query.Sort, int(query.Limit)
		// Histogram and distribution operands own their metric identity in the
		// explicit statistical contract.  Do not require a duplicate generic
		// aggregate metric just to build the runtime plan.
		if len(plan.Metrics) == 0 {
			if query.Histogram != nil {
				plan.Metrics = []visualizationdefinition.FieldBinding{query.Histogram.Metric}
			} else if query.Distribution != nil {
				plan.Metrics = []visualizationdefinition.FieldBinding{query.Distribution.Metric}
			}
		}
	case visualizationdefinition.QuerySpatial:
		query := definition.Query.Spatial
		if query == nil {
			return visualPlan{}, fmt.Errorf("visualization %q has no spatial binding", definition.ID)
		}
		plan.Table, plan.Dimensions, plan.Series, plan.Metrics, plan.Time, plan.Sort, plan.Limit = query.TableID, query.Dimensions, query.Series, query.Metrics, query.Time, query.Sort, int(query.Limit)
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

func visualSorts(visual visualPlan) []reportdef.QuerySort {
	if len(visual.Sort) == 0 {
		if len(visual.Dimensions) > 0 {
			return []reportdef.QuerySort{{Field: visual.Dimensions[0].Alias, Direction: "asc"}}
		}
		return nil
	}
	sorts := make([]reportdef.QuerySort, 0, len(visual.Sort))
	for _, sort := range visual.Sort {
		field := sort.FieldID
		if field == "" {
			if len(visual.Dimensions) > 0 {
				field = visual.Dimensions[0].Alias
			} else if len(visual.Metrics) > 0 {
				field = visual.Metrics[0].Alias
			}
		}
		sorts = append(sorts, reportdef.QuerySort{Field: field, Direction: sort.Direction})
	}
	return sorts
}

type metricMetadata struct {
	Name        string
	Field       string
	Label       string
	Description string
	Unit        string
	Format      string
	Hidden      bool
	DataType    visualizationir.VisualizationDataType
}

func metricLabel(name string, metric metricMetadata) string {
	if strings.TrimSpace(metric.Label) != "" {
		return metric.Label
	}
	return name
}

func aggregateMemberMetadata(model *semanticmodel.Model, name string) metricMetadata {
	if model == nil {
		return metricMetadata{Name: name, Field: name, DataType: visualizationir.VisualizationDataTypeDecimal}
	}
	if metric, err := model.ResolveMetric(name); err == nil {
		return metricMetadata{
			Name: metric.Name, Field: name, Label: metric.Label, Description: metric.Description,
			Unit: metric.Unit, Format: metric.Format, Hidden: metric.Hidden, DataType: runtimeMetricDataType(model, metric),
		}
	}
	return metricMetadata{Name: name, Field: name, DataType: visualizationir.VisualizationDataTypeDecimal}
}

func runtimeMetricDataType(model *semanticmodel.Model, metric semanticmodel.Metric) visualizationir.VisualizationDataType {
	if model == nil {
		return visualizationir.VisualizationDataTypeDecimal
	}
	var dataType semanticmodel.LogicalDataType
	var err error
	if strings.TrimSpace(metric.Name) != "" {
		dataType, err = model.MetricDataType(metric.Name)
	} else {
		dataType, err = model.MetricDataTypeFor(metric)
	}
	if err != nil {
		return visualizationir.VisualizationDataTypeDecimal
	}
	switch dataType {
	case semanticmodel.DataTypeInteger:
		return visualizationir.VisualizationDataTypeInteger
	case semanticmodel.DataTypeDecimal:
		return visualizationir.VisualizationDataTypeDecimal
	case semanticmodel.DataTypeFloat:
		return visualizationir.VisualizationDataTypeFloat
	default:
		return visualizationir.VisualizationDataTypeString
	}
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
		if strings.TrimSpace(sortSpec.FieldID) == "" {
			continue
		}
		sorts = append(sorts, reportdef.QuerySort{Field: sortSpec.FieldID, Direction: sortSpec.Direction})
	}
	return sorts
}

func compiledInteractionConfig(interaction visualizationir.VisualizationInteraction) dashboard.InteractionConfig {
	mappings := make([]dashboard.InteractionConfigMapping, 0, len(interaction.Mappings))
	for _, mapping := range interaction.Mappings {
		dataset, grain, label := "", "", ""
		if mapping.TargetDatasetID != nil {
			dataset = *mapping.TargetDatasetID
		}
		if mapping.Grain != nil {
			grain = *mapping.Grain
		}
		if mapping.Label != nil {
			label = mapping.Label.Field
		}
		mappings = append(mappings, dashboard.InteractionConfigMapping{Field: mapping.TargetFieldID, Dataset: dataset, Grain: grain, Value: mapping.Source.Field, Label: label})
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
					TargetFieldID: mapping.Field, TargetDatasetID: optionalRuntimeString(mapping.Dataset),
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
