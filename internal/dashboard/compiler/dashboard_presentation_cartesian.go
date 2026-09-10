package compiler

import (
	"fmt"

	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func validateCanonicalCartesianPresentationApplicability(variant *document.CartesianDashboardPresentation, visualType document.DashboardVisualType) error {
	if variant == nil {
		return nil
	}
	optionSupported := func(option string, present bool, supported bool) error {
		if present && !supported {
			return fmt.Errorf("presentation.%s is not supported for %s visuals", option, visualType)
		}
		return nil
	}
	// Label policy, label position, display units, and axes use common paths.
	// ECharts does not render series labels for financial marks, so accepting
	// authored label controls there would silently discard user intent.
	if err := optionSupported("labels", variant.Labels != nil, document.SupportsPresentationField(visualType, "labels")); err != nil {
		return err
	}
	if err := optionSupported("labelPosition", variant.LabelPosition != nil, document.SupportsPresentationField(visualType, "labelPosition")); err != nil {
		return err
	}
	if err := optionSupported("legend", variant.Legend != nil, document.SupportsPresentationField(visualType, "legend")); err != nil {
		return err
	}
	legendSupported := document.SupportsPresentationField(visualType, "legend")
	if err := optionSupported("legendTitle", variant.LegendTitle != nil, legendSupported); err != nil {
		return err
	}
	if err := optionSupported("legendItems", variant.LegendItems != nil, document.SupportsPresentationField(visualType, "legendItems")); err != nil {
		return err
	}
	if err := optionSupported("stacking", variant.Stacking != nil, document.SupportsPresentationField(visualType, "stacking")); err != nil {
		return err
	}
	if variant.Stacking != nil && *variant.Stacking == document.DashboardStackingModePercent && variant.DisplayUnits != nil {
		return fmt.Errorf("presentation.displayUnits is incompatible with percent stacking because the renderer owns the percent formatter")
	}
	if err := optionSupported("orientation", variant.Orientation != nil, document.SupportsPresentationField(visualType, "orientation")); err != nil {
		return err
	}
	// Every current Cartesian mark has a renderer-owned data-zoom channel,
	// including heatmaps. The closed presentation union keeps this option out
	// of non-Cartesian families.
	comboLineArea := visualType != document.DashboardVisualTypeCombo || variant.Series == nil
	if visualType == document.DashboardVisualTypeCombo && variant.Series != nil {
		// Keep the existing combo-series diagnostics authoritative when the
		// authored configuration is empty or malformed. A later lowering pass
		// reports the precise series path in those cases.
		comboLineArea = comboSeriesSupportsLineControls(*variant.Series)
	}
	if err := optionSupported("showSymbols", variant.ShowSymbols != nil, document.SupportsPresentationField(visualType, "showSymbols") && comboLineArea); err != nil {
		return err
	}
	if err := optionSupported("smooth", variant.Smooth != nil, document.SupportsPresentationField(visualType, "smooth") && comboLineArea); err != nil {
		return err
	}
	if err := optionSupported("step", variant.Step != nil, document.SupportsPresentationField(visualType, "step") && comboLineArea); err != nil {
		return err
	}
	if err := optionSupported("symbolSize", variant.SymbolSize != nil, document.SupportsPresentationField(visualType, "symbolSize") && comboLineArea); err != nil {
		return err
	}
	if err := optionSupported("series", variant.Series != nil, document.SupportsPresentationField(visualType, "series")); err != nil {
		return err
	}
	if err := optionSupported("seriesIntent", variant.SeriesIntent != nil, document.SupportsPresentationField(visualType, "seriesIntent")); err != nil {
		return err
	}
	if err := optionSupported("gainColor", variant.GainColor != nil, document.SupportsPresentationField(visualType, "gainColor")); err != nil {
		return err
	}
	if err := optionSupported("lossColor", variant.LossColor != nil, document.SupportsPresentationField(visualType, "lossColor")); err != nil {
		return err
	}
	// Decision-context declarations are lowered separately, but their authored
	// presence is part of the same applicability contract. Empty explicitly-
	// authored collections are still declarations and must not be accepted on
	// marks whose renderer has no context channel.
	for _, option := range []struct {
		name    string
		present bool
	}{
		{"referenceLines", variant.ReferenceLines != nil},
		{"referenceBands", variant.ReferenceBands != nil},
		{"eventAnnotations", variant.EventAnnotations != nil},
	} {
		if err := optionSupported(option.name, option.present, document.SupportsPresentationField(visualType, option.name)); err != nil {
			return err
		}
	}
	return nil
}

func comboSeriesSupportsLineControls(values []document.DashboardComboSeries) bool {
	compiled, err := lowerCanonicalComboSeries(values)
	if err != nil {
		// Applicability must not mask the established diagnostic for an invalid
		// combo series (empty, unknown mark, duplicate, and so on).
		return true
	}
	for _, series := range compiled {
		if series.Mark == visualizationir.VisualizationCartesianMarkLine || series.Mark == visualizationir.VisualizationCartesianMarkArea {
			return true
		}
	}
	return false
}
