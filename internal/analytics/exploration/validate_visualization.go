package exploration

import (
	"errors"
	"fmt"
)

func validateVisualization(value *ExplorationVisualizationConfig, selected map[string]string) error {
	if value == nil {
		return nil
	}
	kind, err := value.Kind()
	if err != nil {
		return err
	}
	validateBase := func(base *ExplorationVisualizationConfigBase) error {
		if base.Legend != nil && !validLegend(*base.Legend) {
			return fmt.Errorf("invalid legend %q", *base.Legend)
		}
		if base.DisplayUnits != nil && !validDisplayUnits(*base.DisplayUnits) {
			return fmt.Errorf("invalid display units %q", *base.DisplayUnits)
		}
		if base.Orientation != nil && *base.Orientation != ExplorationVisualizationOrientationHorizontal && *base.Orientation != ExplorationVisualizationOrientationVertical {
			return fmt.Errorf("invalid orientation %q", *base.Orientation)
		}
		if base.Stacking != nil && *base.Stacking != ExplorationVisualizationStackingModeNone && *base.Stacking != ExplorationVisualizationStackingModeNormal && *base.Stacking != ExplorationVisualizationStackingModePercent {
			return fmt.Errorf("invalid stacking mode %q", *base.Stacking)
		}
		return nil
	}
	field := func(ref ExplorationVisualizationFieldRef) error {
		return validateVisualizationFieldRef(ref, selected)
	}
	optionalField := func(ref *ExplorationVisualizationFieldRef) error {
		if ref == nil {
			return nil
		}
		return field(*ref)
	}
	fields := func(refs []ExplorationVisualizationFieldRef) error {
		if len(refs) > 100 {
			return errors.New("visualization fields exceed the maximum item count")
		}
		for index, ref := range refs {
			if err := field(ref); err != nil {
				return fmt.Errorf("field %d: %w", index, err)
			}
		}
		return nil
	}
	switch variant := value.Value.(type) {
	case *CartesianExplorationVisualization:
		if variant.Kind != "cartesian" {
			return fmt.Errorf("visualization kind %q does not match variant", kind)
		}
		if err := validateBase(&variant.ExplorationVisualizationConfigBase); err != nil {
			return err
		}
		if !validCartesianMark(variant.Mark) {
			return fmt.Errorf("invalid cartesian mark %q", variant.Mark)
		}
		if err := optionalField(variant.X); err != nil {
			return fmt.Errorf("invalid x: %w", err)
		}
		if variant.Y != nil {
			if err := fields(*variant.Y); err != nil {
				return fmt.Errorf("invalid y: %w", err)
			}
		}
		return optionalField(variant.Series)
	case *PointExplorationVisualization:
		if variant.Kind != "point" {
			return fmt.Errorf("visualization kind %q does not match variant", kind)
		}
		if err := validateBase(&variant.ExplorationVisualizationConfigBase); err != nil {
			return err
		}
		if variant.Mark != "point" {
			return fmt.Errorf("invalid point mark %q", variant.Mark)
		}
		if err := field(variant.X); err != nil {
			return fmt.Errorf("invalid x: %w", err)
		}
		if err := field(variant.Y); err != nil {
			return fmt.Errorf("invalid y: %w", err)
		}
		if err := optionalField(variant.Size); err != nil {
			return fmt.Errorf("invalid size: %w", err)
		}
		if err := optionalField(variant.Color); err != nil {
			return fmt.Errorf("invalid color: %w", err)
		}
		if variant.Identity != nil {
			return fields(*variant.Identity)
		}
		return nil
	case *ProportionalExplorationVisualization:
		if variant.Kind != "proportional" {
			return fmt.Errorf("visualization kind %q does not match variant", kind)
		}
		if err := validateBase(&variant.ExplorationVisualizationConfigBase); err != nil {
			return err
		}
		if !validProportionalMark(variant.Mark) {
			return fmt.Errorf("invalid proportional mark %q", variant.Mark)
		}
		if err := field(variant.Category); err != nil {
			return fmt.Errorf("invalid category: %w", err)
		}
		if err := field(variant.Value); err != nil {
			return fmt.Errorf("invalid value: %w", err)
		}
		return optionalField(variant.Series)
	case *HierarchyExplorationVisualization:
		if variant.Kind != "hierarchy" {
			return fmt.Errorf("visualization kind %q does not match variant", kind)
		}
		if err := validateBase(&variant.ExplorationVisualizationConfigBase); err != nil {
			return err
		}
		if !validHierarchyMark(variant.Mark) {
			return fmt.Errorf("invalid hierarchy mark %q", variant.Mark)
		}
		if err := field(variant.Node); err != nil {
			return fmt.Errorf("invalid node: %w", err)
		}
		if err := optionalField(variant.Parent); err != nil {
			return fmt.Errorf("invalid parent: %w", err)
		}
		return optionalField(variant.Value)
	case *PolarExplorationVisualization:
		if variant.Kind != "polar" {
			return fmt.Errorf("visualization kind %q does not match variant", kind)
		}
		if err := validateBase(&variant.ExplorationVisualizationConfigBase); err != nil {
			return err
		}
		if !validPolarMark(variant.Mark) {
			return fmt.Errorf("invalid polar mark %q", variant.Mark)
		}
		if err := optionalField(variant.Category); err != nil {
			return fmt.Errorf("invalid category: %w", err)
		}
		if err := field(variant.Value); err != nil {
			return fmt.Errorf("invalid value: %w", err)
		}
		return optionalField(variant.Series)
	case *TableExplorationVisualization:
		if variant.Kind != "table" {
			return fmt.Errorf("visualization kind %q does not match variant", kind)
		}
		if err := validateBase(&variant.ExplorationVisualizationConfigBase); err != nil {
			return err
		}
		return fields(variant.Columns)
	case *MatrixExplorationVisualization:
		if variant.Kind != "matrix" {
			return fmt.Errorf("visualization kind %q does not match variant", kind)
		}
		if err := validateBase(&variant.ExplorationVisualizationConfigBase); err != nil {
			return err
		}
		if err := fields(variant.Rows); err != nil {
			return fmt.Errorf("invalid rows: %w", err)
		}
		if err := fields(variant.Columns); err != nil {
			return fmt.Errorf("invalid columns: %w", err)
		}
		return fields(variant.Metrics)
	case *PivotExplorationVisualization:
		if variant.Kind != "pivot" {
			return fmt.Errorf("visualization kind %q does not match variant", kind)
		}
		if err := validateBase(&variant.ExplorationVisualizationConfigBase); err != nil {
			return err
		}
		if err := fields(variant.Rows); err != nil {
			return fmt.Errorf("invalid rows: %w", err)
		}
		if err := fields(variant.Columns); err != nil {
			return fmt.Errorf("invalid columns: %w", err)
		}
		return fields(variant.Metrics)
	case *KPIExplorationVisualization:
		if variant.Kind != "kpi" {
			return fmt.Errorf("visualization kind %q does not match variant", kind)
		}
		if err := validateBase(&variant.ExplorationVisualizationConfigBase); err != nil {
			return err
		}
		if err := field(variant.Value); err != nil {
			return fmt.Errorf("invalid value: %w", err)
		}
		if err := optionalField(variant.Comparison); err != nil {
			return fmt.Errorf("invalid comparison: %w", err)
		}
		if err := optionalField(variant.Goal); err != nil {
			return fmt.Errorf("invalid goal: %w", err)
		}
		if variant.Trend != nil {
			if err := field(variant.Trend.Category); err != nil {
				return fmt.Errorf("invalid trend category: %w", err)
			}
			if err := field(variant.Trend.Value); err != nil {
				return fmt.Errorf("invalid trend value: %w", err)
			}
		}
		return validateKPIPresentation(variant.Presentation)
	case *GeographicExplorationVisualization:
		if variant.Kind != "geographic" {
			return fmt.Errorf("visualization kind %q does not match variant", kind)
		}
		if err := validateBase(&variant.ExplorationVisualizationConfigBase); err != nil {
			return err
		}
		if !validGeographicLayer(variant.Layer) {
			return fmt.Errorf("invalid geographic layer %q", variant.Layer)
		}
		if err := field(variant.Latitude); err != nil {
			return fmt.Errorf("invalid latitude: %w", err)
		}
		if err := field(variant.Longitude); err != nil {
			return fmt.Errorf("invalid longitude: %w", err)
		}
		if err := optionalField(variant.Color); err != nil {
			return fmt.Errorf("invalid color: %w", err)
		}
		return optionalField(variant.Size)
	default:
		return fmt.Errorf("unsupported visualization kind %q", kind)
	}
}

func validateVisualizationFieldRef(ref ExplorationVisualizationFieldRef, selected map[string]string) error {
	if err := validateFieldReference(ref.Field, "visualization field"); err != nil {
		return err
	}
	if _, ok := selected[ref.Field]; !ok {
		return fmt.Errorf("field %q is not selected", ref.Field)
	}
	if ref.Format != nil {
		if _, err := ref.Format.Kind(); err != nil {
			return fmt.Errorf("invalid format: %w", err)
		}
	}
	return nil
}

func validateKPIPresentation(value *ExplorationKPIPresentation) error {
	if value == nil {
		return nil
	}
	if value.Mode != nil && *value.Mode != ExplorationVisualizationKPIModeCompact && *value.Mode != ExplorationVisualizationKPIModeBullet && *value.Mode != ExplorationVisualizationKPIModeProgress {
		return fmt.Errorf("invalid KPI mode %q", *value.Mode)
	}
	if value.Delta != nil && *value.Delta != ExplorationVisualizationKPIDeltaModeAbsolute && *value.Delta != ExplorationVisualizationKPIDeltaModeRelative {
		return fmt.Errorf("invalid KPI delta %q", *value.Delta)
	}
	if value.FavorableDirection != nil && *value.FavorableDirection != ExplorationVisualizationKPIDirectionIncrease && *value.FavorableDirection != ExplorationVisualizationKPIDirectionDecrease && *value.FavorableDirection != ExplorationVisualizationKPIDirectionNeutral {
		return fmt.Errorf("invalid KPI favorableDirection %q", *value.FavorableDirection)
	}
	if value.MissingComparison != nil && *value.MissingComparison != ExplorationVisualizationKPIMissingComparisonShowUnavailable && *value.MissingComparison != ExplorationVisualizationKPIMissingComparisonHide {
		return fmt.Errorf("invalid KPI missingComparison %q", *value.MissingComparison)
	}
	if value.DisplayUnits != nil && !validDisplayUnits(*value.DisplayUnits) {
		return fmt.Errorf("invalid KPI display units %q", *value.DisplayUnits)
	}
	if value.Tone != nil && !validTone(*value.Tone) {
		return fmt.Errorf("invalid KPI tone %q", *value.Tone)
	}
	if value.Ranges != nil {
		for _, item := range *value.Ranges {
			if !validTone(item.Tone) {
				return fmt.Errorf("invalid KPI range tone %q", item.Tone)
			}
		}
	}
	if value.Thresholds != nil {
		for _, item := range *value.Thresholds {
			if !validTone(item.Tone) {
				return fmt.Errorf("invalid KPI threshold tone %q", item.Tone)
			}
		}
	}
	return nil
}

func validLegend(value ExplorationVisualizationLegendPosition) bool {
	switch value {
	case ExplorationVisualizationLegendPositionHidden, ExplorationVisualizationLegendPositionTop, ExplorationVisualizationLegendPositionRight, ExplorationVisualizationLegendPositionBottom, ExplorationVisualizationLegendPositionLeft:
		return true
	default:
		return false
	}
}

func validDisplayUnits(value ExplorationVisualizationDisplayUnits) bool {
	switch value {
	case ExplorationVisualizationDisplayUnitsAuto, ExplorationVisualizationDisplayUnitsNone, ExplorationVisualizationDisplayUnitsThousands, ExplorationVisualizationDisplayUnitsMillions, ExplorationVisualizationDisplayUnitsBillions, ExplorationVisualizationDisplayUnitsTrillions:
		return true
	default:
		return false
	}
}

func validTone(value ExplorationVisualizationTone) bool {
	switch value {
	case ExplorationVisualizationToneNeutral, ExplorationVisualizationToneInk, ExplorationVisualizationToneSuccess, ExplorationVisualizationToneWarning, ExplorationVisualizationToneDanger:
		return true
	default:
		return false
	}
}

func validCartesianMark(value ExplorationVisualizationCartesianMark) bool {
	switch value {
	case ExplorationVisualizationCartesianMarkLine, ExplorationVisualizationCartesianMarkArea, ExplorationVisualizationCartesianMarkBar, ExplorationVisualizationCartesianMarkColumn, ExplorationVisualizationCartesianMarkHistogram, ExplorationVisualizationCartesianMarkCombo, ExplorationVisualizationCartesianMarkWaterfall, ExplorationVisualizationCartesianMarkCandlestick, ExplorationVisualizationCartesianMarkBoxplot, ExplorationVisualizationCartesianMarkHeatmap:
		return true
	default:
		return false
	}
}

func validProportionalMark(value ExplorationVisualizationProportionalMark) bool {
	return value == ExplorationVisualizationProportionalMarkPie || value == ExplorationVisualizationProportionalMarkDonut || value == ExplorationVisualizationProportionalMarkFunnel
}

func validHierarchyMark(value ExplorationVisualizationHierarchyMark) bool {
	switch value {
	case ExplorationVisualizationHierarchyMarkTreemap, ExplorationVisualizationHierarchyMarkSunburst, ExplorationVisualizationHierarchyMarkTree, ExplorationVisualizationHierarchyMarkSankey, ExplorationVisualizationHierarchyMarkGraph:
		return true
	default:
		return false
	}
}

func validPolarMark(value ExplorationVisualizationPolarMark) bool {
	return value == ExplorationVisualizationPolarMarkRadar || value == ExplorationVisualizationPolarMarkGauge
}

func validGeographicLayer(value ExplorationGeographicLayerKind) bool {
	switch value {
	case ExplorationGeographicLayerKindChoropleth, ExplorationGeographicLayerKindPoint, ExplorationGeographicLayerKindHeat, ExplorationGeographicLayerKindDensity, ExplorationGeographicLayerKindReference, ExplorationGeographicLayerKindPath:
		return true
	default:
		return false
	}
}
