package reportmodel

import (
	"fmt"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
)

type SelectionScope string

const (
	SelectionScopeConformed SelectionScope = "conformed"
	SelectionScopeFactLocal SelectionScope = "fact_local"
)

type ResolvedSelectionInteraction struct {
	Mappings []ResolvedSelectionMapping
	Targets  []ResolvedSelectionTarget
}

type ResolvedSelectionMapping struct {
	Field string
	Fact  string
	Grain string
	Type  string
	Scope SelectionScope
}

type ResolvedSelectionTarget struct {
	Kind   string
	ID     string
	Facts  []string
	Effect string
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
		resolved, err := resolveSelectionMapping(model, dashboardauthoring.SelectionMapping{Field: mapping.Field, Fact: mapping.Fact, Value: mapping.Source})
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
	if err := validateSelectionSourceFacts(d, model, "visual", sourceID, mappings); err != nil {
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
		facts, err := TargetFacts(d, model, targetKind, targetID)
		if err != nil {
			return ResolvedSpatialSelectionInteraction{}, fmt.Errorf("visual %q spatial_selection target %q: %w", sourceID, targetID, err)
		}
		if err := validateSelectionTarget(model, targetID, facts, mappings); err != nil {
			return ResolvedSpatialSelectionInteraction{}, fmt.Errorf("visual %q spatial_selection: %w", sourceID, err)
		}
		resolved.Targets = append(resolved.Targets, ResolvedSelectionTarget{Kind: targetKind, ID: targetID, Facts: facts, Effect: effect})
	}
	return resolved, nil
}

type SelectionMappingIdentity struct {
	Field string
	Fact  string
	Grain string
}

// CanonicalizeMappings validates that an incoming tuple contains each configured
// mapping identity exactly once and returns the mappings in authored order.
func (r ResolvedSelectionInteraction) CanonicalizeMappings(incoming []SelectionMappingIdentity) ([]ResolvedSelectionMapping, error) {
	if len(incoming) != len(r.Mappings) {
		return nil, fmt.Errorf("selection tuple has %d mappings; want %d", len(incoming), len(r.Mappings))
	}
	configured := make(map[SelectionMappingIdentity]ResolvedSelectionMapping, len(r.Mappings))
	for _, mapping := range r.Mappings {
		identity := SelectionMappingIdentity{Field: mapping.Field, Fact: mapping.Fact, Grain: mapping.Grain}
		configured[identity] = mapping
	}
	seen := make(map[SelectionMappingIdentity]bool, len(incoming))
	for _, identity := range incoming {
		if _, ok := configured[identity]; !ok {
			return nil, fmt.Errorf("selection tuple contains unknown mapping identity field=%q fact=%q grain=%q", identity.Field, identity.Fact, identity.Grain)
		}
		if seen[identity] {
			return nil, fmt.Errorf("selection tuple contains duplicate mapping identity field=%q fact=%q grain=%q", identity.Field, identity.Fact, identity.Grain)
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
	if err := validateSelectionSourceFacts(d, model, sourceKind, sourceID, resolved.Mappings); err != nil {
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
		facts, err := TargetFacts(d, model, targetKind, targetID)
		if err != nil {
			return ResolvedSelectionInteraction{}, fmt.Errorf("%s %q interaction target %q: %w", sourceKind, sourceID, targetID, err)
		}
		if err := validateSelectionTarget(model, targetID, facts, resolved.Mappings); err != nil {
			return ResolvedSelectionInteraction{}, fmt.Errorf("%s %q interaction: %w", sourceKind, sourceID, err)
		}
		resolved.Targets = append(resolved.Targets, ResolvedSelectionTarget{Kind: targetKind, ID: targetID, Facts: facts, Effect: effect})
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
		if mapping.Fact != "" {
			return ResolvedSelectionMapping{}, fmt.Errorf("semantic dimension %q must not specify fact", mapping.Field)
		}
		if mapping.Grain != "" && !containsString(dimension.Grains, mapping.Grain) {
			return ResolvedSelectionMapping{}, fmt.Errorf("semantic dimension %q does not support grain %q", mapping.Field, mapping.Grain)
		}
		return ResolvedSelectionMapping{Field: mapping.Field, Grain: mapping.Grain, Type: dimension.Type, Scope: SelectionScopeConformed}, nil
	}
	if mapping.Fact == "" {
		return ResolvedSelectionMapping{}, fmt.Errorf("physical field %q requires fact", mapping.Field)
	}
	if _, ok := model.Tables[mapping.Fact]; !ok {
		return ResolvedSelectionMapping{}, fmt.Errorf("physical field %q references unknown fact %q", mapping.Field, mapping.Fact)
	}
	dimension, err := model.ResolveDimension(mapping.Field)
	if err != nil {
		return ResolvedSelectionMapping{}, err
	}
	if err := model.CanReachField(mapping.Fact, mapping.Field); err != nil {
		return ResolvedSelectionMapping{}, err
	}
	return ResolvedSelectionMapping{Field: mapping.Field, Fact: mapping.Fact, Grain: mapping.Grain, Type: dimension.Type, Scope: SelectionScopeFactLocal}, nil
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
		identity := SelectionMappingIdentity{Field: mapping.Field, Fact: mapping.Fact, Grain: mapping.Grain}
		if seen[identity] {
			return fmt.Errorf("contains duplicate mapping identity field=%q fact=%q grain=%q", mapping.Field, mapping.Fact, mapping.Grain)
		}
		seen[identity] = true
	}
	if len(mappings) < 2 {
		return nil
	}
	scope, fact := mappings[0].Scope, mappings[0].Fact
	for _, mapping := range mappings[1:] {
		if mapping.Scope != scope || (scope == SelectionScopeFactLocal && mapping.Fact != fact) {
			return fmt.Errorf("must be entirely conformed or fact-local to one fact")
		}
	}
	return nil
}

func validateSelectionSourceFacts(d *dashboardauthoring.Dashboard, model *semanticmodel.Model, sourceKind, sourceID string, mappings []ResolvedSelectionMapping) error {
	facts, err := TargetFacts(d, model, sourceKind, sourceID)
	if err != nil {
		return fmt.Errorf("%s %q interaction source facts: %w", sourceKind, sourceID, err)
	}
	return validateSelectionCompatibility(model, "source", sourceID, facts, mappings)
}

func validateSelectionTarget(model *semanticmodel.Model, targetID string, facts []string, mappings []ResolvedSelectionMapping) error {
	return validateSelectionCompatibility(model, "target", targetID, facts, mappings)
}

func validateSelectionCompatibility(model *semanticmodel.Model, role, id string, facts []string, mappings []ResolvedSelectionMapping) error {
	for _, mapping := range mappings {
		switch mapping.Scope {
		case SelectionScopeConformed:
			dimension := model.Dimensions[mapping.Field]
			for _, fact := range facts {
				if _, ok := dimension.Bindings[fact]; !ok {
					return fmt.Errorf("semantic dimension %q has no binding for %s fact %q", mapping.Field, role, fact)
				}
			}
		case SelectionScopeFactLocal:
			if !containsFact(facts, mapping.Fact) {
				return fmt.Errorf("%s %q does not participate in fact %q", role, id, mapping.Fact)
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

func containsFact(facts []string, fact string) bool {
	for _, candidate := range facts {
		if candidate == fact {
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
