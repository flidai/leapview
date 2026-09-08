package explorationadapter

import (
	"crypto/sha256"
	"fmt"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func dashboardLegend(value *exploration.ExplorationVisualizationLegendPosition) *document.DashboardLegendPosition {
	if value == nil {
		return nil
	}
	converted := document.DashboardLegendPosition(*value)
	if converted == "hidden" {
		converted = document.DashboardLegendPositionNone
	}
	return &converted
}

func dashboardDisplayUnits(value *exploration.ExplorationVisualizationDisplayUnits) *visualizationir.VisualizationDisplayUnits {
	if value == nil {
		return nil
	}
	converted := visualizationir.VisualizationDisplayUnits(*value)
	return &converted
}

func cloneMapped[T any, U any](value *T, convert func(T) U) *U {
	if value == nil {
		return nil
	}
	converted := convert(*value)
	return &converted
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func validateDashboardResultField(value string) error {
	if value == "" {
		return fmt.Errorf("result field is required")
	}
	for index, char := range []byte(value) {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || char == '_' || (index > 0 && char >= '0' && char <= '9') {
			continue
		}
		return fmt.Errorf("result field %q is not a valid dashboard field", value)
	}
	return nil
}

func hasConflictingDataset(field, dataset string) bool {
	qualified, _, ok := strings.Cut(field, ".")
	return ok && qualified != "" && qualified != dataset
}

func validateQueryOutputs(dimensions []document.DashboardDimensionSelection, metrics []document.DashboardMetricSelection) error {
	seen := make(map[string]string, len(dimensions)+len(metrics))
	for index, selection := range dimensions {
		output := dimensionOutput(selection)
		if output == "" {
			return fmt.Errorf("dimension %d has no result field output", index)
		}
		if prior, exists := seen[output]; exists {
			return fmt.Errorf("query output %q is selected more than once (%s and dimension %d)", output, prior, index)
		}
		seen[output] = fmt.Sprintf("dimension %d", index)
	}
	for index, selection := range metrics {
		output := metricOutput(selection)
		if output == "" {
			return fmt.Errorf("metric %d has no result field output", index)
		}
		if prior, exists := seen[output]; exists {
			return fmt.Errorf("query output %q is selected more than once (%s and metric %d)", output, prior, index)
		}
		seen[output] = fmt.Sprintf("metric %d", index)
	}
	return nil
}

func scopedFilterID(visualID string, index int) (string, error) {
	if err := validateDashboardLocalIdentifier(visualID); err != nil {
		return "", fmt.Errorf("visual id: %w", err)
	}
	id := fmt.Sprintf("exploration_filter_%s_%d", visualID, index)
	if len(id) <= 128 {
		return id, nil
	}
	digest := sha256.Sum256([]byte(visualID))
	return fmt.Sprintf("exploration_filter_%x_%d", digest[:12], index), nil
}

func validateDashboardLocalIdentifier(value string) error {
	if value == "" {
		return fmt.Errorf("identifier is required")
	}
	if len(value) > 128 {
		return fmt.Errorf("identifier is longer than 128 characters")
	}
	for index, char := range []byte(value) {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || char == '_' || (index > 0 && ((char >= '0' && char <= '9') || char == '-')) {
			continue
		}
		return fmt.Errorf("identifier %q is not a valid dashboard identifier", value)
	}
	return nil
}

func validDashboardTimeGrain(value document.DashboardTimeGrain) bool {
	switch value {
	case document.DashboardTimeGrainSecond, document.DashboardTimeGrainMinute, document.DashboardTimeGrainHour, document.DashboardTimeGrainDay, document.DashboardTimeGrainWeek, document.DashboardTimeGrainMonth, document.DashboardTimeGrainQuarter, document.DashboardTimeGrainYear:
		return true
	default:
		return false
	}
}
func dimensionName(value document.DashboardDimensionSelection) (string, bool) {
	if value.String != nil {
		return *value.String, true
	}
	if value.Reference != nil {
		return value.Reference.Dimension, true
	}
	return "", false
}
func dimensionOutput(value document.DashboardDimensionSelection) string {
	if value.Reference != nil && value.Reference.Alias != nil && strings.TrimSpace(*value.Reference.Alias) != "" {
		return *value.Reference.Alias
	}
	name, _ := dimensionName(value)
	return name
}
func metricName(value document.DashboardMetricSelection) (string, bool) {
	if value.String != nil {
		return *value.String, true
	}
	if value.Reference != nil {
		return value.Reference.Metric, true
	}
	return "", false
}
func metricOutput(value document.DashboardMetricSelection) string {
	if value.Reference != nil && value.Reference.Alias != nil && strings.TrimSpace(*value.Reference.Alias) != "" {
		return *value.Reference.Alias
	}
	name, _ := metricName(value)
	return name
}
func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func cloneInt32(value *int32) *int32 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func cloneKPIRanges(value *[]exploration.ExplorationVisualizationKPIQualitativeRange) *[]visualizationir.VisualizationKPIQualitativeRange {
	if value == nil {
		return nil
	}
	result := make([]visualizationir.VisualizationKPIQualitativeRange, len(*value))
	for index, item := range *value {
		result[index] = visualizationir.VisualizationKPIQualitativeRange{Minimum: cloneFloat64(item.Minimum), Maximum: cloneFloat64(item.Maximum), Label: item.Label, Tone: visualizationir.VisualizationTone(item.Tone)}
	}
	return &result
}

func cloneThresholds(value *[]exploration.ExplorationVisualizationThreshold) *[]visualizationir.VisualizationThreshold {
	if value == nil {
		return nil
	}
	result := make([]visualizationir.VisualizationThreshold, len(*value))
	for index, item := range *value {
		result[index] = visualizationir.VisualizationThreshold{Value: item.Value, Tone: visualizationir.VisualizationTone(item.Tone)}
	}
	return &result
}

func cloneFloat64(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func validateKPIPresentation(value *exploration.ExplorationKPIPresentation) error {
	if value == nil {
		return nil
	}
	if value.Mode != nil && *value.Mode != exploration.ExplorationVisualizationKPIModeCompact && *value.Mode != exploration.ExplorationVisualizationKPIModeBullet && *value.Mode != exploration.ExplorationVisualizationKPIModeProgress {
		return fmt.Errorf("unsupported KPI mode %q", *value.Mode)
	}
	if value.Delta != nil && *value.Delta != exploration.ExplorationVisualizationKPIDeltaModeAbsolute && *value.Delta != exploration.ExplorationVisualizationKPIDeltaModeRelative {
		return fmt.Errorf("unsupported KPI delta mode %q", *value.Delta)
	}
	if value.FavorableDirection != nil && *value.FavorableDirection != exploration.ExplorationVisualizationKPIDirectionIncrease && *value.FavorableDirection != exploration.ExplorationVisualizationKPIDirectionDecrease && *value.FavorableDirection != exploration.ExplorationVisualizationKPIDirectionNeutral {
		return fmt.Errorf("unsupported KPI favorable direction %q", *value.FavorableDirection)
	}
	if value.MissingComparison != nil && *value.MissingComparison != exploration.ExplorationVisualizationKPIMissingComparisonShowUnavailable && *value.MissingComparison != exploration.ExplorationVisualizationKPIMissingComparisonHide {
		return fmt.Errorf("unsupported KPI missing-comparison mode %q", *value.MissingComparison)
	}
	if value.DisplayUnits != nil && !validDisplayUnits(*value.DisplayUnits) {
		return fmt.Errorf("unsupported KPI display units %q", *value.DisplayUnits)
	}
	if value.Tone != nil && !validTone(*value.Tone) {
		return fmt.Errorf("unsupported KPI tone %q", *value.Tone)
	}
	if value.Ranges != nil {
		for index, item := range *value.Ranges {
			if !validTone(item.Tone) {
				return fmt.Errorf("unsupported KPI range %d tone %q", index, item.Tone)
			}
		}
	}
	if value.Thresholds != nil {
		for index, item := range *value.Thresholds {
			if !validTone(item.Tone) {
				return fmt.Errorf("unsupported KPI threshold %d tone %q", index, item.Tone)
			}
		}
	}
	return nil
}

func validExplorationLegend(value exploration.ExplorationVisualizationLegendPosition) bool {
	switch value {
	case exploration.ExplorationVisualizationLegendPositionHidden, exploration.ExplorationVisualizationLegendPositionTop, exploration.ExplorationVisualizationLegendPositionRight, exploration.ExplorationVisualizationLegendPositionBottom, exploration.ExplorationVisualizationLegendPositionLeft:
		return true
	default:
		return false
	}
}

func validDisplayUnits(value exploration.ExplorationVisualizationDisplayUnits) bool {
	switch value {
	case exploration.ExplorationVisualizationDisplayUnitsAuto, exploration.ExplorationVisualizationDisplayUnitsNone, exploration.ExplorationVisualizationDisplayUnitsThousands, exploration.ExplorationVisualizationDisplayUnitsMillions, exploration.ExplorationVisualizationDisplayUnitsBillions, exploration.ExplorationVisualizationDisplayUnitsTrillions:
		return true
	default:
		return false
	}
}

func validStacking(value exploration.ExplorationVisualizationStackingMode) bool {
	switch value {
	case exploration.ExplorationVisualizationStackingModeNone, exploration.ExplorationVisualizationStackingModeNormal, exploration.ExplorationVisualizationStackingModePercent:
		return true
	default:
		return false
	}
}

func validTone(value exploration.ExplorationVisualizationTone) bool {
	switch value {
	case exploration.ExplorationVisualizationToneNeutral, exploration.ExplorationVisualizationToneInk, exploration.ExplorationVisualizationToneSuccess, exploration.ExplorationVisualizationToneWarning, exploration.ExplorationVisualizationToneDanger:
		return true
	default:
		return false
	}
}

func validCartesianMark(value exploration.ExplorationVisualizationCartesianMark) bool {
	switch value {
	case exploration.ExplorationVisualizationCartesianMarkLine, exploration.ExplorationVisualizationCartesianMarkArea, exploration.ExplorationVisualizationCartesianMarkBar, exploration.ExplorationVisualizationCartesianMarkColumn, exploration.ExplorationVisualizationCartesianMarkHistogram, exploration.ExplorationVisualizationCartesianMarkCombo, exploration.ExplorationVisualizationCartesianMarkWaterfall, exploration.ExplorationVisualizationCartesianMarkCandlestick, exploration.ExplorationVisualizationCartesianMarkBoxplot, exploration.ExplorationVisualizationCartesianMarkHeatmap:
		return true
	default:
		return false
	}
}

func validProportionalMark(value exploration.ExplorationVisualizationProportionalMark) bool {
	switch value {
	case exploration.ExplorationVisualizationProportionalMarkPie, exploration.ExplorationVisualizationProportionalMarkDonut, exploration.ExplorationVisualizationProportionalMarkFunnel:
		return true
	default:
		return false
	}
}

func validPolarMark(value exploration.ExplorationVisualizationPolarMark) bool {
	switch value {
	case exploration.ExplorationVisualizationPolarMarkRadar, exploration.ExplorationVisualizationPolarMarkGauge:
		return true
	default:
		return false
	}
}
func dashboardOrientation(value *exploration.ExplorationVisualizationOrientation) *document.DashboardOrientation {
	if value == nil {
		return nil
	}
	converted := document.DashboardOrientation(*value)
	return &converted
}
func dashboardStacking(value *exploration.ExplorationVisualizationStackingMode) *document.DashboardStackingMode {
	if value == nil {
		return nil
	}
	converted := document.DashboardStackingMode(*value)
	return &converted
}
func derefRefs(value *[]exploration.ExplorationVisualizationFieldRef) []exploration.ExplorationVisualizationFieldRef {
	if value == nil {
		return nil
	}
	return append([]exploration.ExplorationVisualizationFieldRef(nil), (*value)...)
}
