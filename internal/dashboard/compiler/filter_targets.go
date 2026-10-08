package compiler

import (
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/document"
)

// CompatibleDashboardFilterTargets derives explicit visual definition targets
// for a builder-created filter using the same semantic checks as compilation.
// Authored YAML retains strict validation of its explicitly chosen scope.
func CompatibleDashboardFilterTargets(doc document.DashboardDocument, dimension string, model *semanticmodel.Model) ([]string, error) {
	dimension = strings.TrimSpace(dimension)
	if model == nil {
		return nil, fmt.Errorf("semantic model is required")
	}
	semantic, ok := model.Dimensions[dimension]
	if !ok {
		return nil, fmt.Errorf("filter dimension %q is unavailable", dimension)
	}
	if _, err := canonicalSemanticValueKind(semantic); err != nil {
		return nil, err
	}
	targets := map[string]bool{}
	resolved := false
	for _, page := range doc.Spec.Pages {
		for _, component := range page.Components {
			visual, ok := component.Value.(*document.VisualDashboardPageComponent)
			if !ok {
				continue
			}
			definition, ok := doc.Spec.Visuals[visual.Visual]
			if !ok {
				continue
			}
			datasets, err := canonicalVisualQueryDatasets(definition.Query, model)
			if err == nil {
				resolved = true
			}
			if err == nil && canonicalDimensionApplies(dimension, datasets, model) {
				targets[visual.Visual] = true
			}
		}
	}
	// Empty and unfinished dashboards can author filters before choosing fields.
	if !resolved {
		return nil, nil
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("%q does not apply to any chart on this dashboard", dimension)
	}
	result := make([]string, 0, len(targets))
	for id := range targets {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}
