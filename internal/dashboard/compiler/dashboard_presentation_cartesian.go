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
	// Labels, label position, display units, and axes use common paths.
	if err := optionSupported("legend", variant.Legend != nil,
		visualType == document.DashboardVisualTypeLine ||
			visualType == document.DashboardVisualTypeArea ||
			visualType == document.DashboardVisualTypeBar ||
			visualType == document.DashboardVisualTypeColumn ||
			visualType == document.DashboardVisualTypeCombo ||
			visualType == document.DashboardVisualTypeCandlestick); err != nil {
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
