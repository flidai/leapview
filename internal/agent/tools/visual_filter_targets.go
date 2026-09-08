package tools

import (
	"fmt"
	"strings"

	dashboarddocument "github.com/flidai/leapview/internal/dashboard/document"
)

const agentVisualFilterTarget = "page/visual"

// normalizeAgentVisualFilterTargets resolves the one synthetic component
// identity used by the agent visual wrapper before the dashboard compiler
// validates filter scope. The compiler's canonical target identity is the
// visual ID; accepting any other target would either fail open to a global
// filter or apply a filter from an unrelated authored visual.
func normalizeAgentVisualFilterTargets(input agentVisualInput, visualID string) (agentVisualInput, error) {
	visualID = strings.TrimSpace(visualID)
	if visualID == "" {
		return agentVisualInput{}, fmt.Errorf("compiled visual identity is required")
	}
	if len(input.Filters) == 0 {
		return input, nil
	}
	result := input
	result.Filters = make([]dashboarddocument.DashboardFilter, len(input.Filters))
	for index, filter := range input.Filters {
		copyFilter := filter
		if filter.Targets != nil {
			targets := make([]string, len(*filter.Targets))
			seen := make(map[string]struct{}, len(*filter.Targets))
			for targetIndex, target := range *filter.Targets {
				target = strings.TrimSpace(target)
				switch target {
				case agentVisualFilterTarget:
					target = visualID
				case visualID:
				default:
					return agentVisualInput{}, fmt.Errorf("filter %q target %q is not valid for agent visual %q", filter.ID, target, visualID)
				}
				if _, exists := seen[target]; exists {
					return agentVisualInput{}, fmt.Errorf("filter %q repeats target %q", filter.ID, target)
				}
				seen[target] = struct{}{}
				targets[targetIndex] = target
			}
			copyFilter.Targets = &targets
		}
		result.Filters[index] = copyFilter
	}
	return result, nil
}
