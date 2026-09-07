package compiler

// This file lowers generated Dashboard presentation DTOs directly into the
// existing renderer-independent Visual IR presentation structs. It does not
// pass through dashboard/authoring or renderer configuration objects.

import (
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

// LowerCanonicalDashboardPresentation returns the matching Visual IR
// presentation struct as one of the concrete IR presentation types. The
// visual type is checked against the closed Dashboard presentation union before
// any fields are copied.
func LowerCanonicalDashboardPresentation(value document.DashboardPresentation, visualType document.DashboardVisualType) (any, error) {
	expected := canonicalPresentationType(visualType)
	if expected == "" {
		return nil, fmt.Errorf("unsupported visual type %q", visualType)
	}
	kind, err := value.Type()
	if err != nil {
		return nil, err
	}
	if kind != expected {
		return nil, fmt.Errorf("visual type %q requires %s presentation, got %s", visualType, expected, kind)
	}
	if err := validateCanonicalPresentationApplicability(value, visualType); err != nil {
		return nil, err
	}
	switch variant := value.Value.(type) {
	case *document.CartesianDashboardPresentation:
		base, err := lowerBasePresentation(variant.Legend, variant.Labels, variant.DisplayUnits)
		if err != nil {
			return nil, err
		}
		out := visualizationir.CartesianVisualizationPresentation{VisualizationPresentation: base}
		if variant.Smooth != nil {
			out.Smooth = *variant.Smooth
		}
		if variant.ShowSymbols != nil {
			out.ShowSymbols = *variant.ShowSymbols
		}
		if variant.DataZoom != nil {
			out.DataZoom = *variant.DataZoom
		}
		if variant.Step != nil {
			out.Step = *variant.Step
		}
		if variant.SymbolSize != nil {
			if !finiteDashboardFloat(*variant.SymbolSize) {
				return nil, fmt.Errorf("presentation.symbolSize must be finite")
			}
			if *variant.SymbolSize <= 0 {
				return nil, fmt.Errorf("presentation.symbolSize must be greater than zero")
			}
			out.SymbolSize = variant.SymbolSize
		}
		if variant.Stacking != nil {
			stacking, err := lowerStacking(*variant.Stacking)
			if err != nil {
				return nil, err
			}
			out.Stacking = &stacking
			out.Stacked = stacking != visualizationir.VisualizationStackingModeNone
		}
		if variant.Orientation != nil {
			orientation, err := lowerOrientation(*variant.Orientation)
			if err != nil {
				return nil, err
			}
			out.Orientation = &orientation
		}
		if variant.LabelPosition != nil {
			position, err := lowerLabelPosition(*variant.LabelPosition)
			if err != nil {
				return nil, err
			}
			out.LabelPosition = &position
		}
		if variant.Series != nil {
			series, err := lowerCanonicalComboSeries(*variant.Series)
			if err != nil {
				return nil, err
			}
			out.ComboSeries = &series
		}
		return out, nil
	case *document.PointDashboardPresentation:
		base, err := lowerBasePresentation(variant.Legend, variant.Labels, nil)
		if err != nil {
			return nil, err
		}
		out := visualizationir.PointVisualizationPresentation{
			VisualizationPresentation: base,
			Overplot:                  visualizationir.VisualizationPointOverplotStrategyOpacity,
			Opacity:                   0.7,
			LargeMode:                 visualizationir.VisualizationPointLargeModeAutomatic,
			LargeThreshold:            10000,
			Brush:                     []visualizationir.VisualizationPointBrushGesture{},
		}
		if variant.Overplot != nil {
			overplot := variant.Overplot
			switch overplot.Strategy {
			case visualizationir.VisualizationPointOverplotStrategyShowAll, visualizationir.VisualizationPointOverplotStrategyOpacity:
			default:
				return nil, fmt.Errorf("unsupported point overplot strategy %q", overplot.Strategy)
			}
			out.Overplot = overplot.Strategy
			if overplot.Opacity != nil {
				if *overplot.Opacity <= 0 || *overplot.Opacity > 1 {
					return nil, fmt.Errorf("point overplot opacity must be greater than 0 and at most 1")
				}
				out.Opacity = *overplot.Opacity
			}
			if overplot.LargeMode != nil {
				switch *overplot.LargeMode {
				case visualizationir.VisualizationPointLargeModeAutomatic, visualizationir.VisualizationPointLargeModeAlways, visualizationir.VisualizationPointLargeModeNever:
				default:
					return nil, fmt.Errorf("unsupported point largeMode %q", *overplot.LargeMode)
				}
				out.LargeMode = *overplot.LargeMode
			}
			if overplot.LargeThreshold != nil {
				if *overplot.LargeThreshold <= 0 {
					return nil, fmt.Errorf("point overplot largeThreshold must be greater than 0")
				}
				out.LargeThreshold = *overplot.LargeThreshold
			}
		}
		if variant.Brush != nil {
			out.Brush = append([]visualizationir.VisualizationPointBrushGesture(nil), (*variant.Brush)...)
		}
		return out, nil
	case *document.ProportionalDashboardPresentation:
		base, err := lowerBasePresentation(variant.Legend, variant.Labels, variant.DisplayUnits)
		if err != nil {
			return nil, err
		}
		if visualType == document.DashboardVisualTypeDonut && variant.CenterLabel != nil && strings.TrimSpace(*variant.CenterLabel) == "" {
			return nil, fmt.Errorf("presentation.centerLabel must not be empty")
		}
		out := visualizationir.ProportionalVisualizationPresentation{VisualizationPresentation: base, Orientation: visualizationir.VisualizationOrientationVertical}
		if variant.Orientation != nil {
			orientation, orientationErr := lowerOrientation(*variant.Orientation)
			if orientationErr != nil {
				return nil, orientationErr
			}
			out.Orientation = orientation
		}
		if variant.Rose != nil {
			out.Rose = *variant.Rose
		}
		out.CenterLabel = variant.CenterLabel
		if variant.LabelPosition != nil {
			position, positionErr := lowerLabelPosition(*variant.LabelPosition)
			if positionErr != nil {
				return nil, positionErr
			}
			out.LabelPosition = &position
		}
		out.InnerRadius = variant.InnerRadius
		out.OuterRadius = variant.OuterRadius
		if out.InnerRadius != nil {
			if !finiteDashboardFloat(*out.InnerRadius) {
				return nil, fmt.Errorf("presentation.innerRadius must be finite")
			}
			if *out.InnerRadius < 0 || *out.InnerRadius > 1 {
				return nil, fmt.Errorf("presentation.innerRadius must be between zero and one")
			}
		}
		if out.OuterRadius != nil {
			if !finiteDashboardFloat(*out.OuterRadius) {
				return nil, fmt.Errorf("presentation.outerRadius must be finite")
			}
			if *out.OuterRadius <= 0 || *out.OuterRadius > 1 {
				return nil, fmt.Errorf("presentation.outerRadius must be greater than zero and at most one")
			}
		}
		if out.InnerRadius != nil && out.OuterRadius != nil && *out.InnerRadius >= *out.OuterRadius {
			return nil, fmt.Errorf("presentation.innerRadius must be less than outerRadius")
		}
		if variant.Align != nil {
			switch *variant.Align {
			case document.DashboardProportionalAlignmentLeft, document.DashboardProportionalAlignmentCenter, document.DashboardProportionalAlignmentRight:
			default:
				return nil, fmt.Errorf("presentation.align must be left, center, or right")
			}
			align := string(*variant.Align)
			out.Align = &align
		}
		if variant.Sort != nil {
			switch *variant.Sort {
			case visualizationir.VisualizationSortDirectionAscending, visualizationir.VisualizationSortDirectionDescending:
			default:
				return nil, fmt.Errorf("presentation.sort must be ascending or descending")
			}
		}
		out.Sort = variant.Sort
		return out, nil
	case *document.HierarchyDashboardPresentation:
		base, err := lowerBasePresentation(variant.Legend, variant.Labels, nil)
		if err != nil {
			return nil, err
		}
		out := visualizationir.HierarchyVisualizationPresentation{VisualizationPresentation: base, Orientation: visualizationir.VisualizationOrientationVertical}
		if variant.Orientation != nil {
			orientation, err := lowerOrientation(*variant.Orientation)
			if err != nil {
				return nil, err
			}
			out.Orientation = orientation
		}
		out.InitialDepth = variant.InitialDepth
		if variant.Roam != nil {
			out.Roam = *variant.Roam
		}
		if variant.Layout != nil {
			switch *variant.Layout {
			case visualizationir.VisualizationHierarchyLayoutStandard, visualizationir.VisualizationHierarchyLayoutCircular:
				out.Layout = variant.Layout
			default:
				return nil, fmt.Errorf("presentation.layout must be standard or circular")
			}
		}
		out.Breadcrumb = variant.Breadcrumb
		out.NodeGap = variant.NodeGap
		out.Curveness = variant.Curveness
		if variant.Focus != nil {
			switch *variant.Focus {
			case visualizationir.VisualizationGraphFocusNone, visualizationir.VisualizationGraphFocusAdjacency:
				out.Focus = variant.Focus
			default:
				return nil, fmt.Errorf("presentation.focus must be none or adjacency")
			}
		}
		if out.InitialDepth != nil && *out.InitialDepth < 0 {
			return nil, fmt.Errorf("hierarchy initialDepth must not be negative")
		}
		if out.NodeGap != nil && *out.NodeGap < 0 {
			return nil, fmt.Errorf("hierarchy nodeGap must not be negative")
		}
		if out.Curveness != nil && (*out.Curveness < 0 || *out.Curveness > 1) {
			return nil, fmt.Errorf("hierarchy curveness must be between zero and one")
		}
		return out, nil
	case *document.PolarDashboardPresentation:
		base, err := lowerBasePresentation(variant.Legend, variant.Labels, variant.DisplayUnits)
		if err != nil {
			return nil, err
		}
		out := visualizationir.PolarVisualizationPresentation{
			VisualizationPresentation: base,
			Minimum:                   variant.Minimum,
			Maximum:                   variant.Maximum,
			Target:                    variant.Target,
			ShowPointer:               true,
			Area:                      variant.Area,
			ProgressWidth:             variant.ProgressWidth,
			Thresholds:                variant.Thresholds,
		}
		if variant.ShowPointer != nil {
			out.ShowPointer = *variant.ShowPointer
		}
		if out.Minimum != nil && !finiteDashboardFloat(*out.Minimum) {
			return nil, fmt.Errorf("presentation.minimum must be finite")
		}
		if out.Maximum != nil && !finiteDashboardFloat(*out.Maximum) {
			return nil, fmt.Errorf("presentation.maximum must be finite")
		}
		if visualType == document.DashboardVisualTypeGauge && (out.Minimum == nil || out.Maximum == nil) {
			return nil, fmt.Errorf("presentation.minimum and presentation.maximum are required for gauge visuals")
		}
		if out.Minimum != nil && out.Maximum != nil && *out.Minimum >= *out.Maximum {
			return nil, fmt.Errorf("presentation.minimum must be less than maximum")
		}
		if visualType == document.DashboardVisualTypeRadar && out.Maximum != nil && *out.Maximum <= 0 {
			return nil, fmt.Errorf("presentation.maximum must be greater than zero for radar visuals")
		}
		if out.Target != nil {
			if !finiteDashboardFloat(*out.Target) {
				return nil, fmt.Errorf("presentation.target must be finite")
			}
			if out.Minimum != nil && out.Maximum != nil && (*out.Target < *out.Minimum || *out.Target > *out.Maximum) {
				return nil, fmt.Errorf("presentation.target must be within the gauge domain")
			}
		}
		if out.ProgressWidth != nil {
			if !finiteDashboardFloat(*out.ProgressWidth) {
				return nil, fmt.Errorf("presentation.progressWidth must be finite")
			}
			if *out.ProgressWidth <= 0 {
				return nil, fmt.Errorf("presentation.progressWidth must be greater than zero")
			}
		}
		if out.Thresholds != nil {
			var previous float64
			for index, threshold := range *out.Thresholds {
				if !finiteDashboardFloat(threshold.Value) {
					return nil, fmt.Errorf("presentation.thresholds[%d].value must be finite", index)
				}
				if out.Minimum != nil && out.Maximum != nil && (threshold.Value < *out.Minimum || threshold.Value > *out.Maximum) {
					return nil, fmt.Errorf("presentation.thresholds[%d].value must be within the gauge domain", index)
				}
				if index > 0 && threshold.Value <= previous {
					return nil, fmt.Errorf("presentation.thresholds[%d].value must be greater than the previous threshold", index)
				}
				previous = threshold.Value
			}
		}
		return out, nil
	case *document.GeographicDashboardPresentation:
		base, err := lowerBasePresentation(nil, variant.Labels, nil)
		if err != nil {
			return nil, err
		}
		base.LabelPolicy = visualizationir.VisualizationLabelPolicy{Density: visualizationir.VisualizationLabelDensityHidden, Priority: []visualizationir.VisualizationLabelPriority{}, MaxCharacters: 24, MinimumSpacing: 0, TooltipFallback: true}
		if variant.Labels != nil {
			base.LabelPolicy, err = lowerLabelPolicy(*variant.Labels)
			if err != nil {
				return nil, err
			}
		}
		out := visualizationir.GeographicVisualizationPresentation{
			VisualizationPresentation: base,
			Roam:                      true, Theme: visualizationir.VisualizationMapThemeAuto,
			LabelDensity: visualizationir.VisualizationMapLabelDensityNormal,
			Camera:       visualizationir.VisualizationMapCamera{Mode: visualizationir.VisualizationMapCameraModeFitData, Padding: 32, MaximumZoom: 14},
			Controls:     visualizationir.VisualizationMapControls{Zoom: true, Reset: true, Compass: true},
		}
		if variant.Roam != nil {
			out.Roam = *variant.Roam
		}
		if variant.Theme != nil {
			out.Theme = *variant.Theme
		}
		if variant.LabelDensity != nil {
			out.LabelDensity = *variant.LabelDensity
		}
		if variant.Camera != nil {
			camera := variant.Camera
			if camera.Mode != nil {
				out.Camera.Mode = *camera.Mode
			}
			out.Camera.Center, out.Camera.Zoom = camera.Center, camera.Zoom
			if camera.Padding != nil {
				out.Camera.Padding = *camera.Padding
			}
			if camera.MinimumZoom != nil {
				out.Camera.MinimumZoom = *camera.MinimumZoom
			}
			if camera.MaximumZoom != nil {
				out.Camera.MaximumZoom = *camera.MaximumZoom
			}
		}
		if variant.Controls != nil {
			if variant.Controls.Zoom != nil {
				out.Controls.Zoom = *variant.Controls.Zoom
			}
			if variant.Controls.Reset != nil {
				out.Controls.Reset = *variant.Controls.Reset
			}
			if variant.Controls.Compass != nil {
				out.Controls.Compass = *variant.Controls.Compass
			}
		}
		return out, nil
	case *document.TableDashboardPresentation:
		if variant.RowHeight <= 0 {
			return nil, fmt.Errorf("table rowHeight must be greater than zero")
		}
		return visualizationir.GridVisualizationPresentation{RowHeight: int64(variant.RowHeight), ShowHeader: variant.ShowHeader, Striped: variant.Striped}, nil
	case *document.KPIDashboardPresentation:
		out := visualizationir.KPIVisualizationPresentation{
			Mode:               visualizationir.VisualizationKPIModeCompact,
			Delta:              visualizationir.VisualizationKPIDeltaModeAbsolute,
			FavorableDirection: visualizationir.VisualizationKPIDirectionNeutral,
			MissingComparison:  visualizationir.VisualizationKPIMissingComparisonShowUnavailable,
			Ranges:             []visualizationir.VisualizationKPIQualitativeRange{},
			DisplayUnits:       variant.DisplayUnits,
			Note:               variant.Note,
			Tone:               variant.Tone,
		}
		if variant.Mode != nil {
			out.Mode = *variant.Mode
		}
		if variant.Delta != nil {
			out.Delta = *variant.Delta
		}
		if variant.FavorableDirection != nil {
			out.FavorableDirection = *variant.FavorableDirection
		}
		if variant.MissingComparison != nil {
			out.MissingComparison = *variant.MissingComparison
		}
		if variant.Ranges != nil {
			out.Ranges = append([]visualizationir.VisualizationKPIQualitativeRange(nil), (*variant.Ranges)...)
		}
		if variant.Thresholds != nil {
			out.Thresholds = variant.Thresholds
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported Dashboard presentation variant %T", value.Value)
	}
}

// ValidateCanonicalPresentationResultReferences keeps any future result-name
// presentation bindings on the governed lowered query boundary. Semantic
// members and physical fields are never re-resolved here.
func ValidateCanonicalPresentationResultReferences(query LoweredDashboardQuery, names []string) error {
	return query.ValidateDownstreamReferences(DashboardResultReferences{Presentation: names})
}

// LowerCanonicalDashboardPresentationForQuery composes the closed
// visual/presentation lowering with the already lowered governed query, so a
// renderer cannot receive a presentation for an incompatible query shape.
func LowerCanonicalDashboardPresentationForQuery(value document.DashboardPresentation, visualType document.DashboardVisualType, query LoweredDashboardQuery) (any, error) {
	if !canonicalQueryCompatible(visualType, query.Type) {
		return nil, fmt.Errorf("visual type %q is incompatible with %s query", visualType, query.Type)
	}
	lowered, err := LowerCanonicalDashboardPresentation(value, visualType)
	if err != nil {
		return nil, err
	}
	if visualType == document.DashboardVisualTypeCombo {
		if err := validateCanonicalComboSeries(value, query); err != nil {
			return nil, err
		}
	}
	return lowered, nil
}

func validateCanonicalPresentationApplicability(value document.DashboardPresentation, visualType document.DashboardVisualType) error {
	optionSupported := func(option string, present bool, supported bool) error {
		if present && !supported {
			return fmt.Errorf("presentation.%s is not supported for %s visuals", option, visualType)
		}
		return nil
	}
	switch variant := value.Value.(type) {
	case *document.CartesianDashboardPresentation:
		return validateCanonicalCartesianPresentationApplicability(variant, visualType)
	case *document.HierarchyDashboardPresentation:
		if variant == nil {
			return nil
		}
		if err := optionSupported("orientation", variant.Orientation != nil, visualType == document.DashboardVisualTypeTree || visualType == document.DashboardVisualTypeSankey); err != nil {
			return err
		}
		if err := optionSupported("initialDepth", variant.InitialDepth != nil, visualType == document.DashboardVisualTypeTree || visualType == document.DashboardVisualTypeTreemap); err != nil {
			return err
		}
		if err := optionSupported("roam", variant.Roam != nil, visualType == document.DashboardVisualTypeGraph || visualType == document.DashboardVisualTypeTree || visualType == document.DashboardVisualTypeTreemap || visualType == document.DashboardVisualTypeSunburst); err != nil {
			return err
		}
		if err := optionSupported("layout", variant.Layout != nil, visualType == document.DashboardVisualTypeGraph || visualType == document.DashboardVisualTypeTree); err != nil {
			return err
		}
		if err := optionSupported("breadcrumb", variant.Breadcrumb != nil, visualType == document.DashboardVisualTypeTreemap); err != nil {
			return err
		}
		if err := optionSupported("nodeGap", variant.NodeGap != nil, visualType == document.DashboardVisualTypeSankey); err != nil {
			return err
		}
		if err := optionSupported("curveness", variant.Curveness != nil, visualType == document.DashboardVisualTypeGraph || visualType == document.DashboardVisualTypeSankey); err != nil {
			return err
		}
		return optionSupported("focus", variant.Focus != nil, visualType == document.DashboardVisualTypeGraph)
	case *document.ProportionalDashboardPresentation:
		if variant == nil {
			return nil
		}
		if err := optionSupported("orientation", variant.Orientation != nil, visualType == document.DashboardVisualTypeFunnel); err != nil {
			return err
		}
		if err := optionSupported("rose", variant.Rose != nil, visualType == document.DashboardVisualTypePie || visualType == document.DashboardVisualTypeDonut); err != nil {
			return err
		}
		if err := optionSupported("centerLabel", variant.CenterLabel != nil, visualType == document.DashboardVisualTypeDonut); err != nil {
			return err
		}
		if err := optionSupported("innerRadius", variant.InnerRadius != nil, visualType == document.DashboardVisualTypeDonut); err != nil {
			return err
		}
		if err := optionSupported("outerRadius", variant.OuterRadius != nil, visualType == document.DashboardVisualTypePie || visualType == document.DashboardVisualTypeDonut); err != nil {
			return err
		}
		if err := optionSupported("align", variant.Align != nil, visualType == document.DashboardVisualTypeFunnel); err != nil {
			return err
		}
		return optionSupported("sort", variant.Sort != nil, visualType == document.DashboardVisualTypeFunnel)
	case *document.PolarDashboardPresentation:
		if variant == nil {
			return nil
		}
		if err := optionSupported("legend", variant.Legend != nil, visualType == document.DashboardVisualTypeRadar); err != nil {
			return err
		}
		if err := optionSupported("minimum", variant.Minimum != nil, visualType == document.DashboardVisualTypeGauge); err != nil {
			return err
		}
		if err := optionSupported("maximum", variant.Maximum != nil, visualType == document.DashboardVisualTypeGauge || visualType == document.DashboardVisualTypeRadar); err != nil {
			return err
		}
		if err := optionSupported("target", variant.Target != nil, visualType == document.DashboardVisualTypeGauge); err != nil {
			return err
		}
		if err := optionSupported("showPointer", variant.ShowPointer != nil, visualType == document.DashboardVisualTypeGauge); err != nil {
			return err
		}
		if err := optionSupported("area", variant.Area != nil, visualType == document.DashboardVisualTypeRadar); err != nil {
			return err
		}
		if err := optionSupported("progressWidth", variant.ProgressWidth != nil, visualType == document.DashboardVisualTypeGauge); err != nil {
			return err
		}
		return optionSupported("thresholds", variant.Thresholds != nil, visualType == document.DashboardVisualTypeGauge)
	default:
		return nil
	}
}

// lowerCanonicalComboSeries maps the closed Dashboard combo policy into the
// existing renderer-neutral IR contract. The IR's SeriesValue is the
// canonical compiled result-field name for multi-measure combo series.
func lowerCanonicalComboSeries(values []document.DashboardComboSeries) ([]visualizationir.VisualizationComboSeries, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("combo presentation.series must contain at least one entry")
	}
	result := make([]visualizationir.VisualizationComboSeries, len(values))
	seen := make(map[string]int, len(values))
	for index, value := range values {
		field := strings.TrimSpace(string(value.Field))
		if field == "" {
			return nil, fmt.Errorf("combo presentation.series[%d].field is required", index)
		}
		if previous, ok := seen[field]; ok {
			return nil, fmt.Errorf("combo presentation.series[%d].field %q duplicates series[%d]", index, field, previous)
		}
		seen[field] = index
		mark, err := lowerComboSeriesMark(value.Mark)
		if err != nil {
			return nil, fmt.Errorf("combo presentation.series[%d]: %w", index, err)
		}
		axis, err := lowerComboSeriesAxis(value.Axis)
		if err != nil {
			return nil, fmt.Errorf("combo presentation.series[%d]: %w", index, err)
		}
		result[index] = visualizationir.VisualizationComboSeries{SeriesValue: field, Mark: mark, Axis: axis}
	}
	return result, nil
}

func lowerComboSeriesMark(value document.DashboardComboSeriesMark) (visualizationir.VisualizationCartesianMark, error) {
	switch string(value) {
	case "line":
		return visualizationir.VisualizationCartesianMarkLine, nil
	case "area":
		return visualizationir.VisualizationCartesianMarkArea, nil
	case "bar":
		return visualizationir.VisualizationCartesianMarkBar, nil
	case "column":
		return visualizationir.VisualizationCartesianMarkColumn, nil
	default:
		return "", fmt.Errorf("unsupported mark %q; combo series support line, area, bar, and column", value)
	}
}

func lowerComboSeriesAxis(value document.DashboardComboSeriesAxis) (visualizationir.VisualizationAxis, error) {
	switch string(value) {
	case "primary":
		return visualizationir.VisualizationAxisPrimary, nil
	case "secondary":
		return visualizationir.VisualizationAxisSecondary, nil
	default:
		return "", fmt.Errorf("unsupported axis %q; combo series support primary and secondary", value)
	}
}

func validateCanonicalComboSeries(value document.DashboardPresentation, query LoweredDashboardQuery) error {
	variant, ok := value.Value.(*document.CartesianDashboardPresentation)
	if !ok || variant == nil || variant.Series == nil {
		return nil
	}
	if query.Binding.Aggregate == nil {
		return fmt.Errorf("combo presentation.series requires an aggregate query")
	}
	if query.Binding.Aggregate.Series != nil {
		return fmt.Errorf("combo presentation.series is only applicable to multi-measure aggregate queries")
	}
	metrics := make(map[string]struct{}, len(query.Binding.Aggregate.Metrics))
	for _, metric := range query.Binding.Aggregate.Metrics {
		metrics[metric.Alias] = struct{}{}
	}
	configured := make(map[string]struct{}, len(*variant.Series))
	for index, series := range *variant.Series {
		field := strings.TrimSpace(string(series.Field))
		if err := query.ValidateResultReference(field); err != nil {
			return fmt.Errorf("combo presentation.series[%d].field: %w", index, err)
		}
		if _, ok := metrics[field]; !ok {
			return fmt.Errorf("combo presentation.series[%d].field %q must reference a compiled metric result", index, field)
		}
		configured[field] = struct{}{}
	}
	if len(*variant.Series) != len(metrics) {
		return fmt.Errorf("combo presentation.series must configure every compiled metric exactly once (got %d entries for %d metrics)", len(*variant.Series), len(metrics))
	}
	for metric := range metrics {
		if _, ok := configured[metric]; !ok {
			return fmt.Errorf("combo presentation.series is missing compiled metric %q", metric)
		}
	}
	return nil
}

func lowerBasePresentation(legend *document.DashboardLegendPosition, labels *document.DashboardLabelPolicy, units *visualizationir.VisualizationDisplayUnits) (visualizationir.VisualizationPresentation, error) {
	out := visualizationir.VisualizationPresentation{Legend: visualizationir.VisualizationLegendPositionBottom, LabelPolicy: defaultCanonicalLabelPolicy(), DisplayUnits: units}
	if legend != nil {
		value, err := lowerLegend(*legend)
		if err != nil {
			return visualizationir.VisualizationPresentation{}, err
		}
		out.Legend = value
	}
	if labels != nil {
		value, err := lowerLabelPolicy(*labels)
		if err != nil {
			return visualizationir.VisualizationPresentation{}, err
		}
		out.LabelPolicy = value
	}
	return out, nil
}

func defaultCanonicalLabelPolicy() visualizationir.VisualizationLabelPolicy {
	return visualizationir.VisualizationLabelPolicy{Density: visualizationir.VisualizationLabelDensityAutomatic, Priority: []visualizationir.VisualizationLabelPriority{visualizationir.VisualizationLabelPrioritySelected, visualizationir.VisualizationLabelPriorityAnomaly, visualizationir.VisualizationLabelPriorityThreshold}, MaxCharacters: 24, MinimumSpacing: 6, TooltipFallback: true}
}

func lowerLabelPolicy(value document.DashboardLabelPolicy) (visualizationir.VisualizationLabelPolicy, error) {
	switch value.Density {
	case document.DashboardLabelDensityHidden, document.DashboardLabelDensityAutomatic, document.DashboardLabelDensityDense, document.DashboardLabelDensityAlways:
	default:
		return visualizationir.VisualizationLabelPolicy{}, fmt.Errorf("unsupported label density %q", value.Density)
	}
	out := defaultCanonicalLabelPolicy()
	out.Density = visualizationir.VisualizationLabelDensity(value.Density)
	if value.Priority != nil {
		out.Priority = make([]visualizationir.VisualizationLabelPriority, 0, len(*value.Priority))
		seen := map[visualizationir.VisualizationLabelPriority]struct{}{}
		for _, priority := range *value.Priority {
			if priority != document.DashboardLabelPrioritySelected && priority != document.DashboardLabelPriorityAnomaly && priority != document.DashboardLabelPriorityThreshold {
				return visualizationir.VisualizationLabelPolicy{}, fmt.Errorf("unsupported label priority %q", priority)
			}
			compiled := visualizationir.VisualizationLabelPriority(priority)
			if _, exists := seen[compiled]; exists {
				return visualizationir.VisualizationLabelPolicy{}, fmt.Errorf("duplicate label priority %q", priority)
			}
			seen[compiled] = struct{}{}
			out.Priority = append(out.Priority, compiled)
		}
	}
	if value.MaxCharacters != nil {
		if *value.MaxCharacters < 4 || *value.MaxCharacters > 200 {
			return visualizationir.VisualizationLabelPolicy{}, fmt.Errorf("label maxCharacters must be between 4 and 200")
		}
		out.MaxCharacters = *value.MaxCharacters
	}
	if value.MinimumSpacing != nil {
		if *value.MinimumSpacing < 0 || *value.MinimumSpacing > 64 {
			return visualizationir.VisualizationLabelPolicy{}, fmt.Errorf("label minimumSpacing must be between 0 and 64")
		}
		out.MinimumSpacing = *value.MinimumSpacing
	}
	if value.TooltipFallback != nil {
		out.TooltipFallback = *value.TooltipFallback
	}
	if out.Density != visualizationir.VisualizationLabelDensityAlways && !out.TooltipFallback {
		return visualizationir.VisualizationLabelPolicy{}, fmt.Errorf("labels that can be suppressed require tooltip fallback")
	}
	return out, nil
}

func lowerLegend(value document.DashboardLegendPosition) (visualizationir.VisualizationLegendPosition, error) {
	switch value {
	case document.DashboardLegendPositionNone:
		return visualizationir.VisualizationLegendPositionHidden, nil
	case document.DashboardLegendPositionTop:
		return visualizationir.VisualizationLegendPositionTop, nil
	case document.DashboardLegendPositionRight:
		return visualizationir.VisualizationLegendPositionRight, nil
	case document.DashboardLegendPositionBottom:
		return visualizationir.VisualizationLegendPositionBottom, nil
	case document.DashboardLegendPositionLeft:
		return visualizationir.VisualizationLegendPositionLeft, nil
	default:
		return "", fmt.Errorf("unsupported legend position %q", value)
	}
}

func lowerOrientation(value document.DashboardOrientation) (visualizationir.VisualizationOrientation, error) {
	switch value {
	case document.DashboardOrientationHorizontal:
		return visualizationir.VisualizationOrientationHorizontal, nil
	case document.DashboardOrientationVertical:
		return visualizationir.VisualizationOrientationVertical, nil
	default:
		return "", fmt.Errorf("unsupported orientation %q", value)
	}
}

func lowerStacking(value document.DashboardStackingMode) (visualizationir.VisualizationStackingMode, error) {
	switch value {
	case document.DashboardStackingModeNone:
		return visualizationir.VisualizationStackingModeNone, nil
	case document.DashboardStackingModeNormal:
		return visualizationir.VisualizationStackingModeNormal, nil
	case document.DashboardStackingModePercent:
		return visualizationir.VisualizationStackingModePercent, nil
	default:
		return "", fmt.Errorf("unsupported stacking mode %q", value)
	}
}

func lowerLabelPosition(value document.DashboardLabelPosition) (visualizationir.VisualizationLabelPosition, error) {
	switch value {
	case document.DashboardLabelPositionAutomatic:
		return visualizationir.VisualizationLabelPositionAutomatic, nil
	case document.DashboardLabelPositionInside:
		return visualizationir.VisualizationLabelPositionInside, nil
	case document.DashboardLabelPositionOutside:
		return visualizationir.VisualizationLabelPositionOutside, nil
	case document.DashboardLabelPositionTop:
		return visualizationir.VisualizationLabelPositionTop, nil
	default:
		return "", fmt.Errorf("unsupported label position %q", value)
	}
}
