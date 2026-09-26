package http

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
)

func dashboardQueryFilters(
	definition dashboarddefinition.Definition,
	pageID string,
	rawState map[string]any,
	rawSelections []map[string]any,
	rawSpatialSelections []map[string]any,
) (dashboard.Filters, error) {
	filters := definition.DefaultFiltersForPage(pageID)
	if len(rawState) > 0 {
		var input struct {
			Version  string                     `json:"version"`
			Controls map[string]json.RawMessage `json:"controls"`
		}
		encoded, err := json.Marshal(rawState)
		if err != nil {
			return dashboard.Filters{}, fmt.Errorf("encode filterState: %w", err)
		}
		if err := json.Unmarshal(encoded, &input); err != nil {
			return dashboard.Filters{}, fmt.Errorf("decode filterState: %w", err)
		}
		if input.Version != "typed_v1" {
			return dashboard.Filters{}, fmt.Errorf("filterState version must be typed_v1")
		}
		machine := dashboardfilter.NewMachine(dashboardfilter.ApplicationImmediate, definition.FilterBindingSpecs())
		bindings := definition.CompiledFilterBindings()
		keys := make([]string, 0, len(input.Controls))
		for key := range input.Controls {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			binding, ok := bindings[key]
			if !ok || binding.Scope == dashboardfilter.ScopePage && binding.PageID != pageID {
				return dashboard.Filters{}, fmt.Errorf("unknown filter binding %q for page %q", key, pageID)
			}
			var expression dashboardfilter.Expression
			if err := json.Unmarshal(input.Controls[key], &expression); err != nil {
				return dashboard.Filters{}, fmt.Errorf("filter binding %q: %w", key, err)
			}
			state := machine.State()
			if _, err := machine.Execute(dashboardfilter.Command{
				Kind: dashboardfilter.CommandMutate, BaseRevision: state.Revision,
				ClientMutationID: "api:" + key, BindingKey: key,
				Operation: dashboardfilter.MutationSet, Expression: &expression,
			}); err != nil {
				return dashboard.Filters{}, fmt.Errorf("filter binding %q: %w", key, err)
			}
		}
		state := machine.State()
		filters.CompiledState = &state
	}
	if err := decodeDashboardSelectionState(rawSelections, &filters.Selections); err != nil {
		return dashboard.Filters{}, fmt.Errorf("interactionSelections: %w", err)
	}
	for index, selection := range filters.Selections {
		if err := validateDashboardInteractionSource(definition, pageID, selection.SourceKind, selection.SourceID, selection.InteractionKind); err != nil {
			return dashboard.Filters{}, fmt.Errorf("interactionSelections[%d]: %w", index, err)
		}
	}
	if err := decodeDashboardSelectionState(rawSpatialSelections, &filters.SpatialSelections); err != nil {
		return dashboard.Filters{}, fmt.Errorf("spatialSelections: %w", err)
	}
	for index, selection := range filters.SpatialSelections {
		if err := validateDashboardInteractionSource(definition, pageID, "visual", selection.VisualID, selection.InteractionID); err != nil {
			return dashboard.Filters{}, fmt.Errorf("spatialSelections[%d]: %w", index, err)
		}
	}
	return filters, nil
}

// validateDashboardInteractionSource keeps API query filters tied to the
// server-owned visual interaction graph. Runtime semantic validation still
// checks the interaction mappings against the resolved model; this boundary
// prevents a caller from borrowing an interaction source from another page or
// inventing a non-visual source before execution begins.
func validateDashboardInteractionSource(definition dashboarddefinition.Definition, pageID, sourceKind, sourceID, interactionID string) error {
	if strings.TrimSpace(sourceKind) != "visual" {
		return fmt.Errorf("source kind %q is not allowed", sourceKind)
	}
	if strings.TrimSpace(sourceID) == "" {
		return fmt.Errorf("source visual ID is required")
	}
	if strings.TrimSpace(interactionID) == "" {
		return fmt.Errorf("interaction ID is required for visual %q", sourceID)
	}
	if _, ok := definition.Visualizations[sourceID]; !ok {
		return fmt.Errorf("unknown source visual %q", sourceID)
	}
	page, ok := definition.PageOrDefault(pageID)
	if !ok || (pageID != "" && page.ID != pageID) {
		return fmt.Errorf("unknown dashboard page %q", pageID)
	}
	for _, component := range page.Visuals {
		if component.Visual == sourceID {
			return nil
		}
	}
	return fmt.Errorf("source visual %q is not on page %q", sourceID, page.ID)
}
