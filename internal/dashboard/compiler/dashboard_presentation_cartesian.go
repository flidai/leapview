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
	if err := optionSupported("labels", variant.Labels != nil,
		visualType != document.DashboardVisualTypeCandlestick && visualType != document.DashboardVisualTypeBoxplot); err != nil {
		return err
	}
	if err := optionSupported("labelPosition", variant.LabelPosition != nil,
		visualType != document.DashboardVisualTypeCandlestick && visualType != document.DashboardVisualTypeBoxplot); err != nil {
		return err
	}
	if err := optionSupported("legend", variant.Legend != nil,
		visualType == document.DashboardVisualTypeLine ||
			visualType == document.DashboardVisualTypeArea ||
			visualType == document.DashboardVisualTypeBar ||
			visualType == document.DashboardVisualTypeColumn ||
			visualType == document.DashboardVisualTypeCombo ||
			visualType == document.DashboardVisualTypeCandlestick); err != nil {
		return err
	}
	legendSupported := visualType == document.DashboardVisualTypeLine ||
		visualType == document.DashboardVisualTypeArea ||
		visualType == document.DashboardVisualTypeBar ||
		visualType == document.DashboardVisualTypeColumn ||
		visualType == document.DashboardVisualTypeCombo ||
		visualType == document.DashboardVisualTypeCandlestick
	if err := optionSupported("legendTitle", variant.LegendTitle != nil, legendSupported); err != nil {
		return err
	}
	if err := optionSupported("legendItems", variant.LegendItems != nil, legendSupported && visualType != document.DashboardVisualTypeCandlestick); err != nil {
		return err
	}
	if err := optionSupported("stacking", variant.Stacking != nil,
		visualType == document.DashboardVisualTypeLine ||
			visualType == document.DashboardVisualTypeArea ||
			visualType == document.DashboardVisualTypeBar ||
			visualType == document.DashboardVisualTypeColumn ||
			visualType == document.DashboardVisualTypeCombo); err != nil {
		return err
	}
	if variant.Stacking != nil && *variant.Stacking == document.DashboardStackingModePercent && variant.DisplayUnits != nil {
		return fmt.Errorf("presentation.displayUnits is incompatible with percent stacking because the renderer owns the percent formatter")
	}
	if err := optionSupported("orientation", variant.Orientation != nil,
		visualType == document.DashboardVisualTypeLine ||
			visualType == document.DashboardVisualTypeArea ||
			visualType == document.DashboardVisualTypeColumn ||
			visualType == document.DashboardVisualTypeCombo); err != nil {
		return err
	}
	if err := optionSupported("dataZoom", variant.DataZoom != nil, visualType != document.DashboardVisualTypeHeatmap); err != nil {
		return err
	}
	comboLineArea := visualType != document.DashboardVisualTypeCombo || variant.Series == nil
	if visualType == document.DashboardVisualTypeCombo && variant.Series != nil {
		// Keep the existing combo-series diagnostics authoritative when the
		// authored configuration is empty or malformed. A later lowering pass
		// reports the precise series path in those cases.
		comboLineArea = comboSeriesSupportsLineControls(*variant.Series)
	}
	if err := optionSupported("showSymbols", variant.ShowSymbols != nil,
		(visualType == document.DashboardVisualTypeLine || visualType == document.DashboardVisualTypeArea || visualType == document.DashboardVisualTypeCombo) && comboLineArea); err != nil {
		return err
	}
	if err := optionSupported("smooth", variant.Smooth != nil,
		(visualType == document.DashboardVisualTypeLine || visualType == document.DashboardVisualTypeArea || visualType == document.DashboardVisualTypeCombo) && comboLineArea); err != nil {
		return err
	}
	if err := optionSupported("step", variant.Step != nil,
		(visualType == document.DashboardVisualTypeLine || visualType == document.DashboardVisualTypeArea || visualType == document.DashboardVisualTypeCombo) && comboLineArea); err != nil {
		return err
	}
	if err := optionSupported("symbolSize", variant.SymbolSize != nil,
		(visualType == document.DashboardVisualTypeLine || visualType == document.DashboardVisualTypeArea || visualType == document.DashboardVisualTypeCombo) && comboLineArea); err != nil {
		return err
	}
	if err := optionSupported("series", variant.Series != nil, visualType == document.DashboardVisualTypeCombo); err != nil {
		return err
	}
	if err := optionSupported("seriesIntent", variant.SeriesIntent != nil,
		visualType == document.DashboardVisualTypeLine ||
			visualType == document.DashboardVisualTypeArea ||
			visualType == document.DashboardVisualTypeBar ||
			visualType == document.DashboardVisualTypeColumn ||
			visualType == document.DashboardVisualTypeCombo); err != nil {
		return err
	}
	if err := optionSupported("gainColor", variant.GainColor != nil, visualType == document.DashboardVisualTypeCandlestick); err != nil {
		return err
	}
	if err := optionSupported("lossColor", variant.LossColor != nil, visualType == document.DashboardVisualTypeCandlestick); err != nil {
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
		if err := optionSupported(option.name, option.present,
			visualType == document.DashboardVisualTypeLine ||
				visualType == document.DashboardVisualTypeArea ||
				visualType == document.DashboardVisualTypeBar ||
				visualType == document.DashboardVisualTypeColumn ||
				visualType == document.DashboardVisualTypeCombo ||
				visualType == document.DashboardVisualTypeWaterfall); err != nil {
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
