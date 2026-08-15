package compiler

import (
	"fmt"
	"math"
	"strings"

	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	dashboardlayout "github.com/flidai/leapview/internal/dashboard/layoutcontract"
)

func validateWidgetPlacements(authored *dashboardauthoring.Dashboard) error {
	for _, rawPage := range authored.Pages {
		page := rawPage.WithDefaults()
		for _, component := range page.Visuals {
			contractID, features, ok := widgetLayoutContract(authored, component)
			if !ok {
				continue
			}
			_, _, width, height := page.Grid.Rect(page.Canvas, component.Placement)
			available := dashboardlayout.Size{
				Width:  int(math.Round(width)),
				Height: int(math.Round(height)),
			}
			resolution, err := dashboardlayout.ResolveOuter(contractID, available, features)
			if err != nil {
				return fmt.Errorf("page %q %s %q layout contract: %w", page.ID, component.Kind, component.ID, err)
			}
			if resolution.Fits {
				continue
			}
			requirements := make([]string, 0, len(resolution.Requirements))
			for _, requirement := range resolution.Requirements {
				requirements = append(requirements, fmt.Sprintf(
					"%s requires %dx%d",
					requirement.Layout,
					requirement.Minimum.Width,
					requirement.Minimum.Height,
				))
			}
			return fmt.Errorf(
				"page %q %s %q provides %dx%d; no valid layout fits (%s)",
				page.ID,
				component.Kind,
				component.ID,
				available.Width,
				available.Height,
				strings.Join(requirements, ", "),
			)
		}
	}
	return nil
}

func widgetLayoutContract(authored *dashboardauthoring.Dashboard, component dashboard.PageVisual) (dashboardlayout.ContractID, []dashboardlayout.Feature, bool) {
	switch component.Kind {
	case "slicer":
		switch component.Presentation.Style {
		case dashboardfilter.PresentationDropdown:
			return dashboardlayout.ContractSlicerDropdown, nil, true
		case dashboardfilter.PresentationInput:
			return dashboardlayout.ContractSlicerInput, nil, true
		case dashboardfilter.PresentationNumericRange:
			return dashboardlayout.ContractSlicerNumericRange, nil, true
		case dashboardfilter.PresentationDateRange:
			return dashboardlayout.ContractSlicerDateRange, nil, true
		case dashboardfilter.PresentationRelativePeriod:
			return dashboardlayout.ContractSlicerRelativePeriod, nil, true
		default:
			return "", nil, false
		}
	case "visual":
		visual, ok := authored.Visuals[component.Visual]
		if !ok || visual.Type != "kpi" || visual.Chart == nil {
			return "", nil, false
		}
		return dashboardlayout.ContractKPI, kpiLayoutFeatures(*visual.Chart), true
	default:
		return "", nil, false
	}
}

func kpiLayoutFeatures(visual dashboardauthoring.Visual) []dashboardlayout.Feature {
	features := make([]dashboardlayout.Feature, 0, 7)
	if visual.Subtitle != "" || visual.Metadata.Subtitle != nil {
		features = append(features, dashboardlayout.FeatureSubtitle)
	}
	if visual.KPI.Comparison != nil {
		features = append(features, dashboardlayout.FeatureComparison)
	}
	if visual.KPI.Mode == "bullet" || visual.KPI.Mode == "progress" {
		features = append(features, dashboardlayout.FeatureProgress)
	}
	if visual.KPI.Goal != nil {
		features = append(features, dashboardlayout.FeatureGoal)
	}
	if len(visual.KPI.Ranges) > 0 || len(visual.Presentation.Thresholds) > 0 {
		features = append(features, dashboardlayout.FeatureStatus)
	}
	if visual.KPI.Trend != nil {
		features = append(features, dashboardlayout.FeatureTrend)
	}
	if visual.Presentation.Note != "" {
		features = append(features, dashboardlayout.FeatureNote)
	}
	return features
}
