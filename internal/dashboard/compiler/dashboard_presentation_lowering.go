package compiler

// This file lowers generated Dashboard presentation DTOs directly into the
// existing renderer-independent Visual IR presentation structs. It does not
// pass through dashboard/authoring or renderer configuration objects.

import (
	"fmt"

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
		if variant.SymbolSize != nil {
			if *variant.SymbolSize <= 0 {
				return nil, fmt.Errorf("cartesian symbolSize must be greater than zero")
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
		return out, nil
	case *document.ProportionalDashboardPresentation:
		base, err := lowerBasePresentation(variant.Legend, variant.Labels, variant.DisplayUnits)
		if err != nil {
			return nil, err
		}
		return visualizationir.ProportionalVisualizationPresentation{VisualizationPresentation: base, Orientation: visualizationir.VisualizationOrientationVertical}, nil
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
		return out, nil
	case *document.PolarDashboardPresentation:
		base, err := lowerBasePresentation(variant.Legend, variant.Labels, variant.DisplayUnits)
		if err != nil {
			return nil, err
		}
		return visualizationir.PolarVisualizationPresentation{VisualizationPresentation: base, ShowPointer: true}, nil
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
		return visualizationir.GeographicVisualizationPresentation{
			VisualizationPresentation: base,
			Roam:                      true, Theme: visualizationir.VisualizationMapThemeAuto,
			LabelDensity: visualizationir.VisualizationMapLabelDensityNormal,
			Camera:       visualizationir.VisualizationMapCamera{Mode: visualizationir.VisualizationMapCameraModeFitData, Padding: 32, MaximumZoom: 14},
			Controls:     visualizationir.VisualizationMapControls{Zoom: true, Reset: true, Compass: true},
		}, nil
	case *document.TableDashboardPresentation:
		if variant.RowHeight <= 0 {
			return nil, fmt.Errorf("table rowHeight must be greater than zero")
		}
		return visualizationir.GridVisualizationPresentation{RowHeight: int64(variant.RowHeight), ShowHeader: variant.ShowHeader, Striped: variant.Striped}, nil
	case *document.KPIDashboardPresentation:
		return visualizationir.KPIVisualizationPresentation{Mode: visualizationir.VisualizationKPIModeCompact, Delta: visualizationir.VisualizationKPIDeltaModeAbsolute, FavorableDirection: visualizationir.VisualizationKPIDirectionNeutral, MissingComparison: visualizationir.VisualizationKPIMissingComparisonShowUnavailable, Ranges: []visualizationir.VisualizationKPIQualitativeRange{}, DisplayUnits: variant.DisplayUnits, Note: variant.Note, Tone: variant.Tone}, nil
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
	return LowerCanonicalDashboardPresentation(value, visualType)
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
