package resultidentity

import (
	"fmt"
	"sort"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// DatasetRelation binds one semantic dataset alias to the immutable physical
// relation revision that backs it. Multiple aliases may intentionally point at
// the same relation.
type DatasetRelation struct {
	Dataset  string
	Relation RelationRevision
}

// EvidenceInput is the activation-owned evidence from which query-specific
// dependencies are derived. It contains no query, cache, engine, or serving
// orchestration objects.
type EvidenceInput struct {
	SemanticModelID     projectgraph.ResourceID
	SemanticModelDigest string
	DatasetRelations    []DatasetRelation
	BindingFingerprint  string
	RuntimeDigest       string
	CapabilityDigest    string
}

// PlanInput is the small, query-planning-owned projection needed to select
// the exact relation revisions for one result.
type PlanInput struct {
	Datasets       []string
	PlannerDigest  string
	SettingsDigest string
	ResultFormat   ResultFormat
}

// Evidence is an immutable, opaque activation snapshot. Dependency selects a
// query-specific subset without reconstructing relation or binding identity.
type Evidence struct {
	semanticModelID     projectgraph.ResourceID
	semanticModelDigest string
	datasetRelations    map[string]RelationRevision
	bindingFingerprint  string
	runtimeDigest       string
	capabilityDigest    string
}

// Available reports whether the value contains validated activation evidence.
// The zero value is deliberately unavailable and therefore cannot authorize
// result reuse.
func (e Evidence) Available() bool { return len(e.datasetRelations) > 0 }

// NewEvidence validates and defensively retains complete activation evidence.
func NewEvidence(input EvidenceInput) (Evidence, error) {
	if err := input.SemanticModelID.Validate(); err != nil {
		return Evidence{}, fmt.Errorf("%w: semantic model ID: %v", ErrInvalidDependency, err)
	}
	if err := validateDigest("semantic model digest", input.SemanticModelDigest); err != nil {
		return Evidence{}, err
	}
	if len(input.DatasetRelations) == 0 {
		return Evidence{}, fmt.Errorf("%w: at least one dataset relation is required", ErrInvalidDependency)
	}
	if err := validateDigest("binding fingerprint", input.BindingFingerprint); err != nil {
		return Evidence{}, err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "runtime digest", value: input.RuntimeDigest},
		{name: "capability digest", value: input.CapabilityDigest},
	} {
		if err := validateDigest(field.name, field.value); err != nil {
			return Evidence{}, err
		}
	}
	relations := make(map[string]RelationRevision, len(input.DatasetRelations))
	revisions := make(map[projectgraph.ResourceID]string, len(input.DatasetRelations))
	for index, item := range input.DatasetRelations {
		if err := validateOpaqueText(item.Dataset); err != nil {
			return Evidence{}, fmt.Errorf("%w: dataset %d: %v", ErrInvalidDependency, index, err)
		}
		if _, exists := relations[item.Dataset]; exists {
			return Evidence{}, fmt.Errorf("%w: duplicate dataset %q", ErrInvalidDependency, item.Dataset)
		}
		if err := item.Relation.RelationID.Validate(); err != nil {
			return Evidence{}, fmt.Errorf("%w: dataset %q relation ID: %v", ErrInvalidDependency, item.Dataset, err)
		}
		if err := validateDigest(fmt.Sprintf("dataset %q relation revision digest", item.Dataset), item.Relation.RevisionDigest); err != nil {
			return Evidence{}, err
		}
		if revision, exists := revisions[item.Relation.RelationID]; exists && revision != item.Relation.RevisionDigest {
			return Evidence{}, fmt.Errorf("%w: relation %q has conflicting revisions", ErrInvalidDependency, item.Relation.RelationID)
		}
		revisions[item.Relation.RelationID] = item.Relation.RevisionDigest
		relations[item.Dataset] = item.Relation
	}

	return Evidence{
		semanticModelID: input.SemanticModelID, semanticModelDigest: input.SemanticModelDigest,
		datasetRelations: relations, bindingFingerprint: input.BindingFingerprint,
		runtimeDigest: input.RuntimeDigest, capabilityDigest: input.CapabilityDigest,
	}, nil
}

// Dependency derives the stable identity for one validated query plan. Every
// requested dataset must have activation evidence; missing evidence fails
// closed and never produces a partial dependency.
func (e Evidence) Dependency(input PlanInput) (Dependency, error) {
	if !e.Available() {
		return Dependency{}, fmt.Errorf("%w: activation evidence is unavailable", ErrInvalidDependency)
	}
	if len(input.Datasets) == 0 {
		return Dependency{}, fmt.Errorf("%w: planned datasets are required", ErrInvalidDependency)
	}
	if err := validateDigest("planner digest", input.PlannerDigest); err != nil {
		return Dependency{}, err
	}
	if err := validateDigest("settings digest", input.SettingsDigest); err != nil {
		return Dependency{}, err
	}
	if err := validateOpaqueText(input.ResultFormat.Name); err != nil {
		return Dependency{}, fmt.Errorf("%w: result format name: %v", ErrInvalidDependency, err)
	}
	if input.ResultFormat.Version == 0 {
		return Dependency{}, fmt.Errorf("%w: result format version must be positive", ErrInvalidDependency)
	}

	datasets := append([]string(nil), input.Datasets...)
	sort.Strings(datasets)
	relationByID := make(map[projectgraph.ResourceID]RelationRevision, len(datasets))
	for index, dataset := range datasets {
		if err := validateOpaqueText(dataset); err != nil {
			return Dependency{}, fmt.Errorf("%w: planned dataset %d: %v", ErrInvalidDependency, index, err)
		}
		if index > 0 && datasets[index-1] == dataset {
			return Dependency{}, fmt.Errorf("%w: duplicate planned dataset %q", ErrInvalidDependency, dataset)
		}
		relation, ok := e.datasetRelations[dataset]
		if !ok {
			return Dependency{}, fmt.Errorf("%w: dataset %q has no relation evidence", ErrInvalidDependency, dataset)
		}
		if existing, ok := relationByID[relation.RelationID]; ok && existing.RevisionDigest != relation.RevisionDigest {
			return Dependency{}, fmt.Errorf("%w: relation %q has conflicting revisions", ErrInvalidDependency, relation.RelationID)
		}
		relationByID[relation.RelationID] = relation
	}
	relations := make([]RelationRevision, 0, len(relationByID))
	for _, relation := range relationByID {
		relations = append(relations, relation)
	}

	return NewDependency(DependencyInput{
		SemanticModelID: e.semanticModelID, SemanticModelDigest: e.semanticModelDigest,
		Relations: relations, BindingFingerprint: e.bindingFingerprint,
		Execution: ExecutionIdentity{
			PlannerDigest: input.PlannerDigest, RuntimeDigest: e.runtimeDigest,
			CapabilityDigest: e.capabilityDigest, SettingsDigest: input.SettingsDigest,
		},
		ResultFormat: input.ResultFormat,
	})
}
