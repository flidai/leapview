package module

import (
	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/release"
)

// EffectiveCandidateArtifacts applies the durable plan's reuse disposition to
// the inspected artifact set before native physical materialization begins. An
// inspected reuse_base set is only safe when the exact candidate-level
// decision is reusable; relation-scoped partial reuse deliberately refreshes
// source data while retaining the sealed base for unchanged relations.
func EffectiveCandidateArtifacts(plan deployment.DeliveryPlan, candidateID string, inspected release.CandidateArtifactSet) (release.CandidateArtifactSet, error) {
	effective := inspected
	if effective.Generation.DataMode != release.GenerationDataReuseBase {
		// Base gate evidence is meaningful only to a reuse-base execution. Never
		// carry it into a source refresh, including ordinary inspected refreshes.
		effective.Generation.BaseGateEvidence = nil
		return effective, nil
	}
	decision, hasDecision := deployment.ResolveDeliveryReuseDecision(&plan, candidateID)
	if hasDecision && decision.Reusable {
		return effective, nil
	}
	effective.Generation.DataMode = release.GenerationDataRefreshSources
	revision, err := release.CandidateSourcesDataRevision(effective.Artifact.SourceDigest, effective.Generation.ManagedDataPins)
	if err != nil {
		return release.CandidateArtifactSet{}, err
	}
	effective.Generation.DataRevision = revision
	effective.Generation.BaseGateEvidence = nil
	return effective, nil
}
