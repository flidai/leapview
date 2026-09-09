package app

import (
	"context"
	"fmt"
	"reflect"
	"sort"

	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	identitymodule "github.com/flidai/leapview/internal/project/identityledger/module"
	"github.com/flidai/leapview/internal/release"
)

func resolveContractActivationReferences(ctx context.Context, reader identitymodule.LatestLifecycleEvidenceReader, instanceID, expectedActiveBundle string, artifacts release.CandidateArtifactSet) ([]identitymodule.PolicyActivationReference, error) {
	candidateModels := artifacts.Compiler.Artifact.Models()
	baseModels := artifacts.Compiler.BaseArtifact.Models()
	protected := make(map[projectgraph.ResourceID]struct{})
	for id, model := range candidateModels {
		if analyticsmodule.ModelRequiresSemanticAccess(model) {
			protected[projectgraph.ResourceID(id)] = struct{}{}
		}
	}
	for id, model := range baseModels {
		if analyticsmodule.ModelRequiresSemanticAccess(model) {
			protected[projectgraph.ResourceID(id)] = struct{}{}
		}
	}
	if len(protected) == 0 {
		return nil, nil
	}
	if reader == nil || instanceID == "" || expectedActiveBundle == "" {
		return nil, fmt.Errorf("%w: protected activation requires an already-active identity and publication authority", ErrIdentityLifecycleUnavailable)
	}
	graph := artifacts.Compiler.Graph
	if graph.Digest() == "" {
		graph = artifacts.Compiler.Artifact.Graph()
	}
	if err := graph.Validate(); err != nil {
		return nil, fmt.Errorf("contract activation graph: %w", err)
	}
	ids := make([]projectgraph.ResourceID, 0, len(protected))
	for id := range protected {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	result := make([]identitymodule.PolicyActivationReference, 0, len(ids))
	for _, id := range ids {
		candidateModel, candidateFound := candidateModels[id.String()]
		baseModel, baseFound := baseModels[id.String()]
		if !candidateFound || !baseFound || !reflect.DeepEqual(candidateModel, baseModel) {
			return nil, fmt.Errorf("%w: protected semantic model %s differs from the active published contract", identitymodule.ErrPolicyEvidenceConflict, id)
		}
		evidence, err := reader.ReadLatestLifecycleEvidence(ctx, instanceID, id, projectgraph.KindSemanticModel)
		if err != nil {
			return nil, fmt.Errorf("contract activation evidence %s: %w", id, err)
		}
		if evidence.Identity.Lifecycle != identitymodule.LifecycleActive || evidence.Identity.ActiveBundleID != expectedActiveBundle || evidence.Sequence <= 0 {
			return nil, fmt.Errorf("%w: contract activation identity %s is stale or not active", identitymodule.ErrPolicyEvidenceConflict, id)
		}
		reference, err := identitymodule.NewPolicyActivationReference(evidence.Publication, graph.Digest())
		if err != nil {
			return nil, fmt.Errorf("contract activation publication %s: %w", id, err)
		}
		reference.LifecycleSequence = evidence.Sequence
		reference.ActiveBundleID = evidence.Identity.ActiveBundleID
		if err := reference.Validate(); err != nil {
			return nil, fmt.Errorf("contract activation lifecycle reference %s: %w", id, err)
		}
		result = append(result, reference)
	}
	return result, nil
}
