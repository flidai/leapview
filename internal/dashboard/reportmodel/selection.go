package reportmodel

import (
	"fmt"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
)

type SelectionScope string

const (
	SelectionScopeConformed    SelectionScope = "conformed"
	SelectionScopeDatasetLocal SelectionScope = "dataset_local"
)

type ResolvedSelectionInteraction struct {
	Mappings []ResolvedSelectionMapping
	Targets  []ResolvedSelectionTarget
}

type ResolvedSelectionMapping struct {
	Field   string
	Dataset string
	Grain   string
	Type    string
	Scope   SelectionScope
}

type ResolvedSelectionTarget struct {
	Kind     string
	ID       string
	Datasets []string
	Effect   string
}

type ResolvedSpatialSelectionInteraction struct {
	Latitude  ResolvedSelectionMapping
	Longitude ResolvedSelectionMapping
	Targets   []ResolvedSelectionTarget
}

// ResolveSpatialSelectionInteraction proves that both governed coordinate
// fields are numeric and can be applied to every explicitly targeted query.
func ResolveSpatialSelectionInteraction(d *dashboardauthoring.Dashboard, model *semanticmodel.Model, sourceID string) (ResolvedSpatialSelectionInteraction, error) {
	visual, ok := d.Visuals[sourceID]
	if !ok || visual.Chart == nil {
		return ResolvedSpatialSelectionInteraction{}, fmt.Errorf("unknown source visualization %q", sourceID)
	}
	selection := visual.Chart.Interaction.SpatialSelection
	resolve := func(axis string, mapping dashboardauthoring.SpatialSelectionMapping) (ResolvedSelectionMapping, error) {
		resolved, err := resolveSelectionMapping(model, dashboardauthoring.SelectionMapping{Field: mapping.Field, Dataset: mapping.Dataset, Value: mapping.Source})
		if err != nil {
			return ResolvedSelectionMapping{}, fmt.Errorf("visual %q spatial_selection %s: %w", sourceID, axis, err)
		}
		if resolved.Type != "number" {
			return ResolvedSelectionMapping{}, fmt.Errorf("visual %q spatial_selection %s field %q must be numeric", sourceID, axis, mapping.Field)
		}
		return resolved, nil
	}
	latitude, err := resolve("latitude", selection.Latitude)
	if err != nil {
		return ResolvedSpatialSelectionInteraction{}, err
	}
	longitude, err := resolve("longitude", selection.Longitude)
	if err != nil {
		return ResolvedSpatialSelectionInteraction{}, err
	}
	mappings := []ResolvedSelectionMapping{latitude, longitude}
	if err := validateSelectionTupleScope(mappings); err != nil {
		return ResolvedSpatialSelectionInteraction{}, fmt.Errorf("visual %q spatial_selection coordinates %w", sourceID, err)
	}
	if err := validateSelectionSourceDatasets(d, model, "visual", sourceID, mappings); err != nil {
		return ResolvedSpatialSelectionInteraction{}, err
	}
	targetEffects, err := authoredInteractionTargets(selection.Targets, selection.HighlightTargets, selection.NoneTargets)
	if err != nil {
		return ResolvedSpatialSelectionInteraction{}, fmt.Errorf("visual %q spatial_selection: %w", sourceID, err)
	}
	resolved := ResolvedSpatialSelectionInteraction{Latitude: latitude, Longitude: longitude, Targets: make([]ResolvedSelectionTarget, 0, len(targetEffects))}
	for _, target := range targetEffects {
		targetID, effect := target.ID, target.Effect
		targetKind, err := selectionTargetKind(d, targetID)
		if err != nil {
			return ResolvedSpatialSelectionInteraction{}, err
		}
		datasets, err := TargetDatasets(d, model, targetKind, targetID)
		if err != nil {
			return ResolvedSpatialSelectionInteraction{}, fmt.Errorf("visual %q spatial_selection target %q: %w", sourceID, targetID, err)
		}
		if err := validateSelectionTarget(model, targetID, datasets, mappings); err != nil {
			return ResolvedSpatialSelectionInteraction{}, fmt.Errorf("visual %q spatial_selection: %w", sourceID, err)
		}
		resolved.Targets = append(resolved.Targets, ResolvedSelectionTarget{Kind: targetKind, ID: targetID, Datasets: datasets, Effect: effect})
	}
	return resolved, nil
}

type SelectionMappingIdentity struct {
	Field   string
	Dataset string
	Grain   string
}

// CanonicalizeMappings validates that an incoming tuple contains each configured
// mapping identity exactly once and returns the mappings in authored order.
func (r ResolvedSelectionInteraction) CanonicalizeMappings(incoming []SelectionMappingIdentity) ([]ResolvedSelectionMapping, error) {
	if len(incoming) != len(r.Mappings) {
		return nil, fmt.Errorf("selection tuple has %d mappings; want %d", len(incoming), len(r.Mappings))
	}
	configured := make(map[SelectionMappingIdentity]ResolvedSelectionMapping, len(r.Mappings))
	for _, mapping := range r.Mappings {
		identity := SelectionMappingIdentity{Field: mapping.Field, Dataset: mapping.Dataset, Grain: mapping.Grain}
		configured[identity] = mapping
	}
	seen := make(map[SelectionMappingIdentity]bool, len(incoming))
	for _, identity := range incoming {
		if _, ok := configured[identity]; !ok {
			return nil, fmt.Errorf("selection tuple contains unknown mapping identity field=%q dataset=%q grain=%q", identity.Field, identity.Dataset, identity.Grain)
		}
		if seen[identity] {
			return nil, fmt.Errorf("selection tuple contains duplicate mapping identity field=%q dataset=%q grain=%q", identity.Field, identity.Dataset, identity.Grain)
		}
		seen[identity] = true
	}
	canonical := make([]ResolvedSelectionMapping, len(r.Mappings))
	copy(canonical, r.Mappings)
	return canonical, nil
}

// ResolveSelectionInteraction resolves and validates the configured interaction
// for a report source. Its mapping order is canonical and matches the authored
// order, so callers can use the result to validate command tuples exactly.
func ResolveSelectionInteraction(d *dashboardauthoring.Dashboard, model *semanticmodel.Model, sourceKind, sourceID string) (ResolvedSelectionInteraction, error) {
	selection, err := sourceSelection(d, sourceKind, sourceID)
	if err != nil {
		return ResolvedSelectionInteraction{}, err
	}
	exposed, time := sourceSelectionFields(d, sourceKind, sourceID)
	resolved := ResolvedSelectionInteraction{Mappings: make([]ResolvedSelectionMapping, 0, len(selection.Mappings))}
	for index, mapping := range selection.Mappings {
		item, err := resolveSelectionMapping(model, mapping)
		if err != nil {
			return ResolvedSelectionInteraction{}, fmt.Errorf("%s %q interaction mapping %d: %w", sourceKind, sourceID, index, err)
		}
		if !exposed[mapping.Field] {
			return ResolvedSelectionInteraction{}, fmt.Errorf("%s %q interaction mapping %d field %q is not exposed by the source query", sourceKind, sourceID, index, mapping.Field)
		}
		if err := validateSelectionGrain(mapping, item.Type, time); err != nil {
			return ResolvedSelectionInteraction{}, fmt.Errorf("%s %q interaction mapping %d: %w", sourceKind, sourceID, index, err)
		}
		resolved.Mappings = append(resolved.Mappings, item)
	}
	if err := validateSelectionTupleScope(resolved.Mappings); err != nil {
		return ResolvedSelectionInteraction{}, fmt.Errorf("%s %q interaction mappings %w", sourceKind, sourceID, err)
	}
	if err := validateSelectionSourceDatasets(d, model, sourceKind, sourceID, resolved.Mappings); err != nil {
		return ResolvedSelectionInteraction{}, err
	}
	targets, err := authoredInteractionTargets(selection.Targets, selection.HighlightTargets, selection.NoneTargets)
	if err != nil {
		return ResolvedSelectionInteraction{}, fmt.Errorf("%s %q interaction: %w", sourceKind, sourceID, err)
	}
	for _, target := range targets {
		targetID, effect := target.ID, target.Effect
		targetKind, err := selectionTargetKind(d, targetID)
		if err != nil {
			return ResolvedSelectionInteraction{}, err
		}
		datasets, err := TargetDatasets(d, model, targetKind, targetID)
		if err != nil {
			return ResolvedSelectionInteraction{}, fmt.Errorf("%s %q interaction target %q: %w", sourceKind, sourceID, targetID, err)
		}
		if err := validateSelectionTarget(model, targetID, datasets, resolved.Mappings); err != nil {
			return ResolvedSelectionInteraction{}, fmt.Errorf("%s %q interaction: %w", sourceKind, sourceID, err)
		}
		resolved.Targets = append(resolved.Targets, ResolvedSelectionTarget{Kind: targetKind, ID: targetID, Datasets: datasets, Effect: effect})
	}
	return resolved, nil
}

type authoredInteractionTarget struct {
	ID     string
	Effect string
}

func authoredInteractionTargets(filter, highlight, none []string) ([]authoredInteractionTarget, error) {
	targets := make([]authoredInteractionTarget, 0, len(filter)+len(highlight)+len(none))
	seen := make(map[string]string, cap(targets))
	appendTargets := func(ids []string, effect string) error {
		for _, id := range ids {
			if previous, ok := seen[id]; ok {
				return fmt.Errorf("target %q declares both %q and %q", id, previous, effect)
			}
			seen[id] = effect
			targets = append(targets, authoredInteractionTarget{ID: id, Effect: effect})
		}
		return nil
	}
	if err := appendTargets(filter, "filter"); err != nil {
		return nil, err
	}
	if err := appendTargets(highlight, "highlight"); err != nil {
		return nil, err
	}
	if err := appendTargets(none, "none"); err != nil {
		return nil, err
	}
	return targets, nil
}

func sourceSelection(d *dashboardauthoring.Dashboard, sourceKind, sourceID string) (dashboardauthoring.SelectionInteraction, error) {
	switch sourceKind {
	case "visual":
		if visual, ok := d.Visuals[sourceID]; ok {
			if visual.Chart != nil {
				return visual.Chart.Interaction.PointSelection, nil
			}
			if visual.Tabular != nil {
				return visual.Tabular.Interaction.RowSelection, nil
			}
		}
		return dashboardauthoring.SelectionInteraction{}, fmt.Errorf("unknown source visual %q", sourceID)
	default:
		return dashboardauthoring.SelectionInteraction{}, fmt.Errorf("unknown source kind %q", sourceKind)
	}
}

func sourceSelectionFields(d *dashboardauthoring.Dashboard, sourceKind, sourceID string) (map[string]bool, dashboardauthoring.QueryTime) {
	fields := map[string]bool{}
	if sourceKind == "visual" {
		if visual, ok := d.Visuals[sourceID]; ok {
			if visual.Chart != nil {
				for _, dimension := range visual.Chart.Query.Dimensions {
					fields[dimension.Field] = true
				}
				if !visual.Chart.Query.Series.IsZero() {
					fields[visual.Chart.Query.Series.Field] = true
				}
				if visual.Chart.Query.Time.Field != "" {
					fields[visual.Chart.Query.Time.Field] = true
				}
				return fields, visual.Chart.Query.Time
			}
			if visual.Tabular != nil {
				for _, field := range visual.Tabular.Query.Fields {
					fields[field] = true
				}
				for _, columns := range [][]dashboardauthoring.FieldRef{visual.Tabular.Query.Columns, visual.Tabular.Query.Rows} {
					for _, field := range columns {
						fields[field.Field] = true
					}
				}
			}
		}
		return fields, dashboardauthoring.QueryTime{}
	}
	return fields, dashboardauthoring.QueryTime{}
}

func resolveSelectionMapping(model *semanticmodel.Model, mapping dashboardauthoring.SelectionMapping) (ResolvedSelectionMapping, error) {
	if !strings.Contains(mapping.Field, ".") {
		dimension, err := model.ResolveSemanticDimension(mapping.Field)
		if err != nil {
			return ResolvedSelectionMapping{}, err
		}
		if mapping.Dataset != "" {
			return ResolvedSelectionMapping{}, fmt.Errorf("semantic dimension %q must not specify dataset", mapping.Field)
		}
		if mapping.Grain != "" && !containsString(dimension.Grains, mapping.Grain) {
			return ResolvedSelectionMapping{}, fmt.Errorf("semantic dimension %q does not support grain %q", mapping.Field, mapping.Grain)
		}
		return ResolvedSelectionMapping{Field: mapping.Field, Grain: mapping.Grain, Type: dimension.Type, Scope: SelectionScopeConformed}, nil
	}
	if mapping.Dataset == "" {
		return ResolvedSelectionMapping{}, fmt.Errorf("physical field %q requires dataset", mapping.Field)
	}
	if _, ok := model.Tables[mapping.Dataset]; !ok {
		return ResolvedSelectionMapping{}, fmt.Errorf("physical field %q references unknown dataset %q", mapping.Field, mapping.Dataset)
	}
	dimension, err := model.ResolveDimension(mapping.Field)
	if err != nil {
		return ResolvedSelectionMapping{}, err
	}
	if err := model.CanReachField(mapping.Dataset, mapping.Field); err != nil {
		return ResolvedSelectionMapping{}, err
	}
	return ResolvedSelectionMapping{Field: mapping.Field, Dataset: mapping.Dataset, Grain: mapping.Grain, Type: dimension.Type, Scope: SelectionScopeDatasetLocal}, nil
}

func validateSelectionGrain(mapping dashboardauthoring.SelectionMapping, fieldType string, time dashboardauthoring.QueryTime) error {
	if time.Field == mapping.Field {
		if fieldType != "date" && fieldType != "timestamp" {
			return fmt.Errorf("field %q type %q cannot be used as a grained time selection", mapping.Field, fieldType)
		}
		if mapping.Grain != time.Grain {
			return fmt.Errorf("field %q requires grain %q to match the source query", mapping.Field, time.Grain)
		}
		return nil
	}
	if mapping.Grain != "" {
		return fmt.Errorf("field %q grain is only valid for a grained query time field", mapping.Field)
	}
	return nil
}

func validateSelectionTupleScope(mappings []ResolvedSelectionMapping) error {
	seen := map[SelectionMappingIdentity]bool{}
	for _, mapping := range mappings {
		identity := SelectionMappingIdentity{Field: mapping.Field, Dataset: mapping.Dataset, Grain: mapping.Grain}
		if seen[identity] {
			return fmt.Errorf("contains duplicate mapping identity field=%q dataset=%q grain=%q", mapping.Field, mapping.Dataset, mapping.Grain)
		}
		seen[identity] = true
	}
	if len(mappings) < 2 {
		return nil
	}
	scope, dataset := mappings[0].Scope, mappings[0].Dataset
	for _, mapping := range mappings[1:] {
		if mapping.Scope != scope || (scope == SelectionScopeDatasetLocal && mapping.Dataset != dataset) {
			return fmt.Errorf("must be entirely conformed or dataset-local to one dataset")
		}
	}
	return nil
}

func validateSelectionSourceDatasets(d *dashboardauthoring.Dashboard, model *semanticmodel.Model, sourceKind, sourceID string, mappings []ResolvedSelectionMapping) error {
	datasets, err := TargetDatasets(d, model, sourceKind, sourceID)
	if err != nil {
		return fmt.Errorf("%s %q interaction source datasets: %w", sourceKind, sourceID, err)
	}
	return validateSelectionCompatibility(model, "source", sourceID, datasets, mappings)
}

func validateSelectionTarget(model *semanticmodel.Model, targetID string, datasets []string, mappings []ResolvedSelectionMapping) error {
	return validateSelectionCompatibility(model, "target", targetID, datasets, mappings)
}

func validateSelectionCompatibility(model *semanticmodel.Model, role, id string, datasets []string, mappings []ResolvedSelectionMapping) error {
	for _, mapping := range mappings {
		switch mapping.Scope {
		case SelectionScopeConformed:
			dimension := model.Dimensions[mapping.Field]
			for _, dataset := range datasets {
				if _, ok := dimension.Bindings[dataset]; !ok {
					return fmt.Errorf("semantic dimension %q has no binding for %s dataset %q", mapping.Field, role, dataset)
				}
			}
		case SelectionScopeDatasetLocal:
			if !containsDataset(datasets, mapping.Dataset) {
				return fmt.Errorf("%s %q does not participate in dataset %q", role, id, mapping.Dataset)
			}
		}
	}
	return nil
}

func selectionTargetKind(d *dashboardauthoring.Dashboard, targetID string) (string, error) {
	if _, ok := d.Visuals[targetID]; !ok {
		return "", fmt.Errorf("interaction references unknown target %q", targetID)
	}
	return "visual", nil
}

func containsDataset(datasets []string, dataset string) bool {
	for _, candidate := range datasets {
		if candidate == dataset {
			return true
		}
	}
	return false
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
