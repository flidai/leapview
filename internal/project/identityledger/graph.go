package identityledger

import (
	"fmt"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// CandidateFromGraph builds the ledger candidate for one validated compiled
// project graph. The graph's project root is an internal graph boundary and is
// deliberately excluded; the remaining authored resources retain their
// explicit graph ID and kind.
func CandidateFromGraph(
	instanceID, bundleID, expectedBundleID, actorID string,
	graph projectgraph.ProjectGraph,
) (Candidate, error) {
	if err := graph.Validate(); err != nil {
		return Candidate{}, fmt.Errorf("%w: project graph: %v", ErrInvalidInput, err)
	}

	graphResources := graph.Resources()
	resources := make([]Resource, 0, len(graphResources))
	for _, resource := range graphResources {
		if resource.Kind == projectgraph.KindProject {
			continue
		}
		if !IsAuthoredKind(resource.Kind) {
			return Candidate{}, fmt.Errorf("%w: graph resource %q has unsupported authored kind %q", ErrInvalidInput, resource.ID, resource.Kind)
		}
		resources = append(resources, Resource{
			AuthoredID: resource.ID,
			Kind:       resource.Kind,
		})
	}
	normalized, err := NormalizeResources(resources)
	if err != nil {
		return Candidate{}, err
	}
	candidate := Candidate{
		InstanceID:       instanceID,
		BundleID:         bundleID,
		ExpectedBundleID: expectedBundleID,
		ActorID:          actorID,
		Resources:        normalized,
	}
	if err := ValidateCandidate(candidate); err != nil {
		return Candidate{}, err
	}
	return candidate, nil
}
