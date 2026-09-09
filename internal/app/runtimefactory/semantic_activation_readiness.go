package runtimefactory

import (
	"errors"
	"fmt"

	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	identityledger "github.com/flidai/leapview/internal/project/identityledger"
	"github.com/flidai/leapview/internal/release"
)

// ErrSemanticActivationNotReady indicates that a candidate contains a
// protected semantic model, while the retained publication/approval tuple and
// neutral activation verification boundary have not been wired for admission.
// It is intentionally distinct from ordinary plan validation failures so the
// caller can preserve the candidate's fail-closed lifecycle state.
var ErrSemanticActivationNotReady = errors.New("semantic activation is not ready")

func validateSemanticActivationReadiness(artifacts release.CandidateArtifactSet, references []identityledger.PolicyActivationReference) error {
	if !candidateArtifactsContainProtectedSemanticModel(artifacts) {
		if len(references) != 0 {
			return fmt.Errorf("%w: contract activation evidence supplied for an unprotected candidate", ErrSemanticActivationNotReady)
		}
		return nil
	}
	protected := make(map[string]struct{})
	for id, model := range artifacts.Compiler.Manifest.SemanticModels {
		if semanticquery.ModelRequiresSemanticAccess(model) {
			protected[id] = struct{}{}
		}
	}
	for id, model := range artifacts.Compiler.Artifact.Models() {
		if semanticquery.ModelRequiresSemanticAccess(model) {
			protected[id] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(references))
	graphDigest := artifacts.Compiler.Graph.Digest()
	if graphDigest == "" {
		graphDigest = artifacts.Compiler.Artifact.Graph().Digest()
	}
	for _, reference := range references {
		if reference.Publication.ResourceKind != projectgraph.KindSemanticModel || reference.Publication.InstanceID == "" || reference.GraphDigest != graphDigest || reference.PolicyEvidenceVersion != identityledger.RegistryPolicyEvidenceVersion || reference.RegistryTypes == nil {
			return fmt.Errorf("%w: contract activation reference scope or graph differs", ErrSemanticActivationNotReady)
		}
		id := reference.Publication.AuthoredID.String()
		if _, ok := protected[id]; !ok {
			return fmt.Errorf("%w: publication %s is not a protected candidate model", ErrSemanticActivationNotReady, id)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("%w: duplicate publication for %s", ErrSemanticActivationNotReady, id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) == len(protected) {
		return nil
	}
	return fmt.Errorf("%w: protected semantic activation requires exact current publication, registry, graph, and policy evidence for every protected model", ErrSemanticActivationNotReady)
}

func candidateArtifactsContainProtectedSemanticModel(artifacts release.CandidateArtifactSet) bool {
	for _, model := range artifacts.Compiler.Manifest.SemanticModels {
		if semanticquery.ModelRequiresSemanticAccess(model) {
			return true
		}
	}
	for _, model := range artifacts.Compiler.Artifact.Models() {
		if semanticquery.ModelRequiresSemanticAccess(model) {
			return true
		}
	}
	return false
}
