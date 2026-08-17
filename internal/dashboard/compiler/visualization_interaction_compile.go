package compiler

import (
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func compiledSelectionInteractions(id string, selection dashboardauthoring.SelectionInteraction) []visualizationir.VisualizationInteraction {
	if selection.IsZero() {
		return []visualizationir.VisualizationInteraction{}
	}
	mappings := make([]visualizationir.VisualizationInteractionMapping, 0, len(selection.Mappings))
	for _, mapping := range selection.Mappings {
		value := visualizationir.VisualizationFieldRef{Dataset: "primary", Field: mapping.Value}
		item := visualizationir.VisualizationInteractionMapping{Source: value, TargetFieldID: mapping.Field}
		if mapping.Fact != "" {
			item.TargetDatasetID = &mapping.Fact
		}
		if mapping.Grain != "" {
			item.Grain = &mapping.Grain
		}
		if mapping.Label != "" {
			label := visualizationir.VisualizationFieldRef{Dataset: "primary", Field: mapping.Label}
			item.Label = &label
		}
		mappings = append(mappings, item)
	}
	mode := visualizationir.VisualizationSelectionModeSingle
	if selection.Toggle {
		mode = visualizationir.VisualizationSelectionModeMultiple
	}
	return []visualizationir.VisualizationInteraction{{ID: id, Kind: visualizationir.VisualizationInteractionKindSelect, Mappings: mappings, Targets: compiledInteractionTargets(selection.Targets, selection.HighlightTargets, selection.NoneTargets), Mode: mode, RequiresStableIdentity: true}}
}

func compiledInteractionTargets(filter, highlight, none []string) []visualizationir.VisualizationInteractionTarget {
	targets := make([]visualizationir.VisualizationInteractionTarget, 0, len(filter)+len(highlight)+len(none))
	appendTargets := func(ids []string, effect visualizationir.VisualizationInteractionEffect) {
		for _, id := range ids {
			targets = append(targets, visualizationir.VisualizationInteractionTarget{VisualID: id, Effect: effect})
		}
	}
	appendTargets(filter, visualizationir.VisualizationInteractionEffectFilter)
	appendTargets(highlight, visualizationir.VisualizationInteractionEffectHighlight)
	appendTargets(none, visualizationir.VisualizationInteractionEffectNone)
	return targets
}

func interactionIdentity(selection dashboardauthoring.SelectionInteraction) []string {
	fields := make([]string, 0, len(selection.Mappings))
	for _, mapping := range selection.Mappings {
		fields = append(fields, mapping.Field)
	}
	return uniqueStrings(fields)
}
