package module

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

// CanonicalDeliveryPlanID returns the stable plan identity bound to one target,
// project, environment, operation, and caller idempotency key. Recovery paths
// use the same identity to distinguish their own committed restatement from an
// unrelated publication that advanced the target concurrently.
func CanonicalDeliveryPlanID(targetID string, projectID projectgraph.ResourceID, environment string, operation deployment.DeliveryOperationKind, idempotencyKey string) string {
	return "plan-" + digestID(strings.Join([]string{targetID, projectID.String(), environment, string(operation), idempotencyKey}, "\x00"))
}

func digestID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

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
