package reportmodel

import (
	"fmt"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

// ResolveCompiledSelectionInteraction resolves the semantic types of the
// compiler-owned IR mappings without reconstructing authoring dashboard models.
func ResolveCompiledSelectionInteraction(definition *dashboarddefinition.Definition, model *semanticmodel.Model, sourceKind, sourceID string) (ResolvedSelectionInteraction, error) {
	if sourceKind != "visual" {
		return ResolvedSelectionInteraction{}, fmt.Errorf("unknown source kind %q", sourceKind)
	}
	source, ok := definition.Visualizations[sourceID]
	if !ok {
		return ResolvedSelectionInteraction{}, fmt.Errorf("unknown source visualization %q", sourceID)
	}
	base, err := visualizationir.SpecificationBase(source.Spec)
	if err != nil {
		return ResolvedSelectionInteraction{}, err
	}
	if len(base.Interactions) == 0 {
		return ResolvedSelectionInteraction{}, fmt.Errorf("visualization %q has no selection interaction", sourceID)
	}
	interaction := base.Interactions[0]
	resolved := ResolvedSelectionInteraction{Mappings: make([]ResolvedSelectionMapping, 0, len(interaction.Mappings))}
	for index, mapping := range interaction.Mappings {
		item, err := resolveCompiledMapping(model, mapping)
		if err != nil {
			return ResolvedSelectionInteraction{}, fmt.Errorf("visualization %q interaction mapping %d: %w", sourceID, index, err)
		}
		resolved.Mappings = append(resolved.Mappings, item)
	}
	for _, target := range interaction.Targets {
		if _, ok := definition.Visualizations[target.VisualID]; !ok {
			return ResolvedSelectionInteraction{}, fmt.Errorf("interaction references unknown target %q", target.VisualID)
		}
		resolved.Targets = append(resolved.Targets, ResolvedSelectionTarget{Kind: "visual", ID: target.VisualID, Effect: string(target.Effect)})
	}
	return resolved, nil
}

// ResolveCompiledSpatialSelectionInteraction resolves compiler-owned
// geographic mappings without reconstructing the authoring dashboard.
func ResolveCompiledSpatialSelectionInteraction(definition *dashboarddefinition.Definition, model *semanticmodel.Model, sourceID, interactionID string) (ResolvedSpatialSelectionInteraction, error) {
	source, ok := definition.Visualizations[sourceID]
	if !ok {
		return ResolvedSpatialSelectionInteraction{}, fmt.Errorf("unknown source visualization %q", sourceID)
	}
	spec, ok := source.Spec.Value.(*visualizationir.GeographicVisualizationSpec)
	if !ok {
		return ResolvedSpatialSelectionInteraction{}, fmt.Errorf("visualization %q is not geographic", sourceID)
	}
	var interaction *visualizationir.VisualizationSpatialSelectionInteraction
	for index := range spec.SpatialInteractions {
		if spec.SpatialInteractions[index].ID == interactionID {
			interaction = &spec.SpatialInteractions[index]
			break
		}
	}
	if interaction == nil {
		return ResolvedSpatialSelectionInteraction{}, fmt.Errorf("visualization %q has no spatial interaction %q", sourceID, interactionID)
	}
	resolve := func(mapping visualizationir.VisualizationSpatialFieldMapping) (ResolvedSelectionMapping, error) {
		compiled := visualizationir.VisualizationInteractionMapping{TargetFieldID: mapping.TargetFieldID, TargetDatasetID: mapping.TargetDatasetID}
		resolved, err := resolveCompiledMapping(model, compiled)
		if err != nil {
			return ResolvedSelectionMapping{}, err
		}
		if resolved.Type != "number" {
			return ResolvedSelectionMapping{}, fmt.Errorf("field %q must be numeric", resolved.Field)
		}
		return resolved, nil
	}
	latitude, err := resolve(interaction.Latitude)
	if err != nil {
		return ResolvedSpatialSelectionInteraction{}, fmt.Errorf("visualization %q spatial latitude: %w", sourceID, err)
	}
	longitude, err := resolve(interaction.Longitude)
	if err != nil {
		return ResolvedSpatialSelectionInteraction{}, fmt.Errorf("visualization %q spatial longitude: %w", sourceID, err)
	}
	resolved := ResolvedSpatialSelectionInteraction{Latitude: latitude, Longitude: longitude}
	for _, target := range interaction.Targets {
		if _, ok := definition.Visualizations[target.VisualID]; !ok {
			return ResolvedSpatialSelectionInteraction{}, fmt.Errorf("spatial interaction references unknown target %q", target.VisualID)
		}
		resolved.Targets = append(resolved.Targets, ResolvedSelectionTarget{Kind: "visual", ID: target.VisualID, Effect: string(target.Effect)})
	}
	return resolved, nil
}

func resolveCompiledMapping(model *semanticmodel.Model, mapping visualizationir.VisualizationInteractionMapping) (ResolvedSelectionMapping, error) {
	field, dataset, grain := mapping.TargetFieldID, "", ""
	if mapping.TargetDatasetID != nil {
		dataset = *mapping.TargetDatasetID
	}
	if mapping.Grain != nil {
		grain = *mapping.Grain
	}
	if !strings.Contains(field, ".") {
		dimension, err := model.ResolveSemanticDimension(field)
		if err != nil {
			return ResolvedSelectionMapping{}, err
		}
		return ResolvedSelectionMapping{Field: field, Grain: grain, Type: dimension.Type, Scope: SelectionScopeConformed}, nil
	}
	if dataset == "" {
		return ResolvedSelectionMapping{}, fmt.Errorf("physical field %q requires dataset", field)
	}
	dimension, err := model.ResolveDimension(field)
	if err != nil {
		return ResolvedSelectionMapping{}, err
	}
	if err := model.CanReachField(dataset, field); err != nil {
		return ResolvedSelectionMapping{}, err
	}
	return ResolvedSelectionMapping{Field: field, Dataset: dataset, Grain: grain, Type: dimension.Type, Scope: SelectionScopeDatasetLocal}, nil
}
