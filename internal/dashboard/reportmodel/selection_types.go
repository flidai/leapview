package reportmodel

import "fmt"

// SelectionScope identifies whether an interaction mapping addresses a
// conformed semantic dimension or a dataset-local physical field.
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

type SelectionMappingIdentity struct {
	Field   string
	Dataset string
	Grain   string
}

// CanonicalizeMappings validates that an incoming tuple contains each
// configured mapping identity exactly once and returns mappings in the
// compiler-authored order.
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
