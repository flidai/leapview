package app

import (
	"context"
	"fmt"
	"reflect"
	"sort"

	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	"github.com/flidai/leapview/internal/deployment"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	identitymodule "github.com/flidai/leapview/internal/project/identityledger/module"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/servingstate"
)

type contractActivationArtifactReader interface {
	ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error)
}

func validateCanonicalContractServingArtifact(ctx context.Context, reader contractActivationArtifactReader, generationID string, plan deployment.DeliveryPlan) error {
	compiled, err := readContractActivationArtifact(ctx, reader, generationID)
	if err != nil {
		return err
	}
	return validateCanonicalContractArtifact(compiled, plan)
}

func validateUnsupportedContractServingArtifact(ctx context.Context, reader contractActivationArtifactReader, generationID string, plan deployment.DeliveryPlan, operation string) error {
	compiled, err := readContractActivationArtifact(ctx, reader, generationID)
	if err != nil {
		return err
	}
	return validateUnsupportedContractArtifact(compiled, plan, operation)
}

func validateUnsupportedContractArtifact(compiled projectbundle.CompiledProjectArtifact, plan deployment.DeliveryPlan, operation string) error {
	if err := validateCanonicalContractArtifact(compiled, plan); err != nil {
		return err
	}
	if len(plan.Evidence.ContractActivations) != 0 {
		return fmt.Errorf("%w: %s requires the canonical exact-evidence activation path", ErrIdentityLifecycleUnavailable, operation)
	}
	return nil
}

func readContractActivationArtifact(ctx context.Context, reader contractActivationArtifactReader, generationID string) (projectbundle.CompiledProjectArtifact, error) {
	if reader == nil || generationID == "" {
		return projectbundle.CompiledProjectArtifact{}, fmt.Errorf("%w: serving artifact authority is unavailable", ErrIdentityLifecycleUnavailable)
	}
	artifact, err := reader.ArtifactByServingState(ctx, servingstate.ID(generationID))
	if err != nil {
		return projectbundle.CompiledProjectArtifact{}, fmt.Errorf("load contract activation serving artifact: %w", err)
	}
	compiled, err := loadCompiledArtifact(artifact.Path)
	if err != nil {
		return projectbundle.CompiledProjectArtifact{}, fmt.Errorf("decode contract activation serving artifact: %w", err)
	}
	return compiled, nil
}

func validateCanonicalContractArtifact(compiled projectbundle.CompiledProjectArtifact, plan deployment.DeliveryPlan) error {
	protected := false
	for _, model := range compiled.Manifest.SemanticModels {
		if analyticsmodule.ModelRequiresSemanticAccess(model) {
			protected = true
			break
		}
	}
	if protected && len(plan.Evidence.ContractActivations) == 0 {
		return fmt.Errorf("%w: protected serving artifact has no exact publication activation evidence", ErrIdentityLifecycleUnavailable)
	}
	if !protected && len(plan.Evidence.ContractActivations) != 0 {
		return fmt.Errorf("%w: unprotected serving artifact carries contract activation evidence", identitymodule.ErrPolicyEvidenceConflict)
	}
	return nil
}

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
