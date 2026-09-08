package runtimefactory

import (
	"errors"
	"fmt"

	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/release"
)

// ErrSemanticActivationNotReady indicates that a candidate contains a
// protected semantic model, while the retained publication/approval tuple and
// neutral activation verification boundary have not been wired for admission.
// It is intentionally distinct from ordinary plan validation failures so the
// caller can preserve the candidate's fail-closed lifecycle state.
var ErrSemanticActivationNotReady = errors.New("semantic activation is not ready")

func validateSemanticActivationReadiness(artifacts release.CandidateArtifactSet) error {
	if !candidateArtifactsContainProtectedSemanticModel(artifacts) {
		return nil
	}
	return fmt.Errorf("%w: protected semantic activation requires retained publication/version/policy approval evidence and neutral semantic verification; neither is wired", ErrSemanticActivationNotReady)
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
