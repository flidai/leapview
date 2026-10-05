package compiler

import (
	"fmt"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/document"
)

// CanonicalCompatibleFilterVisualTargets returns visual definition IDs whose
// current queries can consume the dimension. IDs preserve all compatible
// placements while excluding unrelated datasets from new report-wide slicers.
func CanonicalCompatibleFilterVisualTargets(doc document.DashboardDocument, model *semanticmodel.Model, dimension string) ([]string, error) {
	dimension = strings.TrimSpace(dimension)
	if model == nil {
		return nil, fmt.Errorf("semantic model is required")
	}
	if _, err := model.ResolveSemanticDimension(dimension); err != nil {
		return nil, fmt.Errorf("dimension %q is not a semantic dimension: %w", dimension, err)
	}
	targets := make([]string, 0)
	seen := make(map[string]struct{})
	for _, page := range doc.Spec.Pages {
		for _, component := range page.Components {
			base, err := component.Base()
			if err != nil {
				return nil, fmt.Errorf("page %q component: %w", page.ID, err)
			}
			placed, ok := component.Value.(*document.VisualDashboardPageComponent)
			if !ok {
				continue
			}
			visual, ok := doc.Spec.Visuals[placed.Visual]
			if !ok {
				return nil, fmt.Errorf("page %q component %q references unknown visual %q", page.ID, base.ID, placed.Visual)
			}
			datasets, err := canonicalVisualQueryDatasets(visual.Query, model)
			if err != nil || !canonicalDimensionApplies(dimension, datasets, model) {
				continue
			}
			if _, exists := seen[placed.Visual]; exists {
				continue
			}
			seen[placed.Visual] = struct{}{}
			targets = append(targets, placed.Visual)
		}
	}
	return targets, nil
}

func canonicalVisualQueryDatasets(query document.DashboardQuery, model *semanticmodel.Model) ([]string, error) {
	if lowered, err := LowerDashboardQueryBinding(query, model, model.Name); err == nil {
		datasets := loweredDashboardQueryDatasets(lowered)
		if len(datasets) > 0 {
			return datasets, nil
		}
	}
	return canonicalQueryDatasets(query, model)
}
