package compiler

import (
	"fmt"

	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

// completeVisualizationInteractionGraph makes the default no-effect policy
// explicit in the immutable IR. Runtime planning never has to infer a missing
// edge, and conflicting declarations fail compilation.
func completeVisualizationInteractionGraph(definitions map[string]visualizationdefinition.Definition) error {
	ids := sortedMapKeys(definitions)
	for _, sourceID := range ids {
		definition := definitions[sourceID]
		base, err := mutableSpecificationBase(definition.Spec)
		if err != nil {
			return err
		}
		for index := range base.Interactions {
			targets, err := completeInteractionTargets(base.Interactions[index].Targets, ids)
			if err != nil {
				return fmt.Errorf("visual %q interaction %q: %w", sourceID, base.Interactions[index].ID, err)
			}
			base.Interactions[index].Targets = targets
			if err := validateHighlightTargets(definitions, sourceID, base.Interactions[index]); err != nil {
				return err
			}
		}
		if geographic, ok := definition.Spec.Value.(*visualizationir.GeographicVisualizationSpec); ok {
			for index := range geographic.SpatialInteractions {
				targets, err := completeInteractionTargets(geographic.SpatialInteractions[index].Targets, ids)
				if err != nil {
					return fmt.Errorf("visual %q spatial interaction %q: %w", sourceID, geographic.SpatialInteractions[index].ID, err)
				}
				geographic.SpatialInteractions[index].Targets = targets
				if err := validateSpatialHighlightTargets(definitions, sourceID, geographic.SpatialInteractions[index]); err != nil {
					return err
				}
			}
		}
		definition, err = visualizationdefinition.NewWithSecondaryQueries(
			definition.ID,
			definition.Spec,
			definition.Query,
			definition.SecondaryQueries,
		)
		if err != nil {
			return fmt.Errorf("rebuild visual %q after completing interaction graph: %w", sourceID, err)
		}
		definitions[sourceID] = definition
	}
	return nil
}

func validateHighlightTargets(
	definitions map[string]visualizationdefinition.Definition,
	sourceID string,
	interaction visualizationir.VisualizationInteraction,
) error {
	for _, target := range interaction.Targets {
		if target.Effect != visualizationir.VisualizationInteractionEffectHighlight {
			continue
		}
		for _, mapping := range interaction.Mappings {
			if err := validateTargetDataset(definitions[target.VisualID], mapping.TargetDatasetID); err != nil {
				return fmt.Errorf("visual %q interaction %q highlight target %q: %w", sourceID, interaction.ID, target.VisualID, err)
			}
			if missing, err := missingHighlightField(definitions[target.VisualID], []string{mapping.TargetFieldID}, mapping.TargetDatasetID); err != nil {
				return err
			} else if missing != "" {
				return fmt.Errorf(
					"visual %q interaction %q highlight target %q does not expose mapped field %q",
					sourceID,
					interaction.ID,
					target.VisualID,
					missing,
				)
			}
		}
	}
	return nil
}

func validateSpatialHighlightTargets(
	definitions map[string]visualizationdefinition.Definition,
	sourceID string,
	interaction visualizationir.VisualizationSpatialSelectionInteraction,
) error {
	for _, target := range interaction.Targets {
		if target.Effect != visualizationir.VisualizationInteractionEffectHighlight {
			continue
		}
		for _, mapping := range []visualizationir.VisualizationSpatialFieldMapping{interaction.Latitude, interaction.Longitude} {
			if err := validateTargetDataset(definitions[target.VisualID], mapping.TargetDatasetID); err != nil {
				return fmt.Errorf("visual %q spatial interaction %q highlight target %q: %w", sourceID, interaction.ID, target.VisualID, err)
			}
			if missing, err := missingHighlightField(definitions[target.VisualID], []string{mapping.TargetFieldID}, mapping.TargetDatasetID); err != nil {
				return err
			} else if missing != "" {
				return fmt.Errorf(
					"visual %q spatial interaction %q highlight target %q does not expose mapped field %q",
					sourceID,
					interaction.ID,
					target.VisualID,
					missing,
				)
			}
		}
	}
	return nil
}

func validateTargetDataset(definition visualizationdefinition.Definition, datasetID *string) error {
	if datasetID == nil || *datasetID == "" {
		return nil
	}
	base, err := definition.Spec.Base()
	if err != nil {
		return err
	}
	for _, dataset := range base.Datasets {
		if dataset.ID == *datasetID {
			return nil
		}
	}
	return fmt.Errorf("target dataset %q is not declared by visual %q", *datasetID, definition.ID)
}

func missingHighlightField(definition visualizationdefinition.Definition, fields []string, datasetID *string) (string, error) {
	if _, isKPI := definition.Spec.Value.(*visualizationir.KPIVisualizationSpec); isKPI {
		return "", nil
	}
	base, err := definition.Spec.Base()
	if err != nil {
		return "", err
	}
	exposed := map[string]struct{}{}
	for _, dataset := range base.Datasets {
		if datasetID != nil && dataset.ID != *datasetID {
			continue
		}
		for _, field := range dataset.Fields {
			exposed[field.ID] = struct{}{}
			if field.SourceRef != nil {
				exposed[*field.SourceRef] = struct{}{}
			}
		}
	}
	for _, field := range fields {
		if _, ok := exposed[field]; !ok {
			return field, nil
		}
	}
	return "", nil
}

func completeInteractionTargets(authored []visualizationir.VisualizationInteractionTarget, visualIDs []string) ([]visualizationir.VisualizationInteractionTarget, error) {
	byID := make(map[string]visualizationir.VisualizationInteractionEffect, len(authored))
	for _, target := range authored {
		if target.VisualID == "" {
			return nil, fmt.Errorf("target visual is required")
		}
		switch target.Effect {
		case visualizationir.VisualizationInteractionEffectNone, visualizationir.VisualizationInteractionEffectFilter, visualizationir.VisualizationInteractionEffectHighlight:
		default:
			return nil, fmt.Errorf("target %q has unsupported effect %q", target.VisualID, target.Effect)
		}
		if previous, exists := byID[target.VisualID]; exists {
			return nil, fmt.Errorf("target %q declares both %q and %q", target.VisualID, previous, target.Effect)
		}
		byID[target.VisualID] = target.Effect
	}
	out := make([]visualizationir.VisualizationInteractionTarget, 0, len(visualIDs))
	for _, id := range visualIDs {
		effect, exists := byID[id]
		if !exists {
			effect = visualizationir.VisualizationInteractionEffectNone
		}
		out = append(out, visualizationir.VisualizationInteractionTarget{VisualID: id, Effect: effect})
		delete(byID, id)
	}
	for id := range byID {
		return nil, fmt.Errorf("references unknown target %q", id)
	}
	return out, nil
}
