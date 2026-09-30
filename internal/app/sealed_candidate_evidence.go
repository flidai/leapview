package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	appdeploymentpostgres "github.com/flidai/leapview/internal/app/deploymentpostgres"
	appruntimefactory "github.com/flidai/leapview/internal/app/runtimefactory"
	"github.com/flidai/leapview/internal/deployment"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	releasemodule "github.com/flidai/leapview/internal/release/module"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
)

type sealedCandidateDeliveryReader interface {
	ResolveCandidateGeneration(context.Context, string) (appdeploymentpostgres.CandidateGenerationResolution, error)
	Candidate(context.Context, string) (appdeploymentpostgres.DeliveryCandidate, error)
	SnapshotSeal(context.Context, string) (appdeploymentpostgres.SnapshotSeal, error)
}

type sealedCandidateProvenanceReader interface {
	CandidateProvenance(context.Context, projectgraph.ResourceID, string, int64) (release.Provenance, error)
}

// sealedCandidateEvidenceSource projects immutable candidate and seal metadata
// for a runtime that is being prepared before the delivery pointer moves. It
// is deliberately separate from activeConnectionEvidenceSource: it proves
// qualification identity, not that the candidate has been published.
type sealedCandidateEvidenceSource struct {
	delivery    sealedCandidateDeliveryReader
	releases    sealedCandidateProvenanceReader
	targetID    string
	environment string
}

func (source sealedCandidateEvidenceSource) SealedCandidateResultIdentityEvidence(
	ctx context.Context,
	identity projectgraph.ServingIdentity,
	candidateID string,
	sealID string,
	artifactDigest string,
) (appruntimefactory.ActivationEvidence, error) {
	invalid := func(reason string) (appruntimefactory.ActivationEvidence, error) {
		return appruntimefactory.ActivationEvidence{}, fmt.Errorf("%w: sealed candidate runtime evidence %s", releasemodule.ErrProvenanceInvalid, reason)
	}
	if ctx == nil || source.delivery == nil || source.releases == nil || identity.Validate() != nil ||
		identity.Environment != source.environment || source.targetID == "" || source.targetID != strings.TrimSpace(source.targetID) ||
		!canonicalUUID(candidateID) || !canonicalUUID(sealID) || platformdigest.ValidateSHA256Identity(artifactDigest) != nil {
		return invalid("request identity is invalid")
	}
	if err := ctx.Err(); err != nil {
		return appruntimefactory.ActivationEvidence{}, err
	}

	resolution, err := source.delivery.ResolveCandidateGeneration(ctx, candidateID)
	if err != nil {
		return invalid("candidate generation is unavailable")
	}
	if resolution.GenerationCount != 1 || resolution.CandidateID != candidateID || resolution.TargetID != source.targetID ||
		resolution.ProjectID != identity.ProjectID.String() || resolution.Environment != identity.Environment ||
		resolution.GenerationID != identity.GenerationID || resolution.SnapshotSealID != sealID ||
		resolution.ArtifactDigest != artifactDigest || !sealedCandidateQualified(resolution.Status) || !canonicalUUID(resolution.PlanID) {
		return invalid("candidate generation identity differs")
	}

	candidate, err := source.delivery.Candidate(ctx, candidateID)
	if err != nil {
		return invalid("candidate qualification is unavailable")
	}
	if candidate.CandidateID != candidateID || candidate.TargetID != source.targetID || candidate.PlanID != resolution.PlanID ||
		!canonicalUUID(candidate.AttemptID) || candidate.SnapshotSealID != sealID || !sealedCandidateQualified(candidate.Status) ||
		candidate.CandidateRevision != resolution.CandidateRevision || candidate.ArtifactDigest != artifactDigest ||
		platformdigest.ValidateSHA256Identity(candidate.QualificationDigest) != nil || candidate.QualifiedAt.IsZero() || !candidate.RetiredAt.IsZero() {
		return invalid("candidate record differs")
	}

	seal, err := source.delivery.SnapshotSeal(ctx, sealID)
	if err != nil {
		return invalid("snapshot seal is unavailable")
	}
	if seal.SealID != sealID || seal.CandidateID != candidateID || seal.AttemptID != candidate.AttemptID ||
		seal.ServingArtifactDigest != artifactDigest || seal.RuntimeVersion == "" || seal.DuckLakeSnapshotID <= 0 ||
		seal.QualifiedAt.IsZero() || seal.CatalogVersion <= 0 ||
		platformdigest.ValidateSHA256Identity(seal.PlanDigest) != nil || len(seal.QualificationEvidence) == 0 {
		return invalid("snapshot seal identity differs")
	}

	var qualification appdeploymentpostgres.NativeQualificationEvidence
	if err := strictjson.DecodeWithOptions(seal.QualificationEvidence, &qualification, strictjson.Options{
		MaxBytes: appdeploymentpostgres.NativeQualificationMaxBytes,
	}); err != nil {
		return invalid("qualification evidence is invalid")
	}
	_, qualificationDigest, err := qualification.Canonical()
	if err != nil || qualificationDigest != candidate.QualificationDigest || qualification.CandidateID != candidateID ||
		qualification.AttemptID != candidate.AttemptID || qualification.PhysicalPoolID != seal.PhysicalPoolID ||
		qualification.CatalogID != seal.CatalogID || qualification.SnapshotID != seal.DuckLakeSnapshotID ||
		qualification.ObjectRoot != seal.ObjectRoot || qualification.RelationNamespace != seal.RelationNamespace ||
		qualification.RelationManifestDigest != seal.RelationManifestDigest || qualification.ClosureDigest != seal.ClosureDigest ||
		qualification.Runtime.CatalogType != "postgres" || qualification.Runtime.MetadataSchema != ducklake.MetadataSchemaForPool(seal.PhysicalPoolID) ||
		qualification.Runtime.DuckDBRuntime != seal.DuckDBVersion ||
		qualification.Runtime.DuckLakeExtension != seal.DuckLakeExtensionVersion ||
		qualification.Runtime.CatalogFormat != seal.DuckLakeSpecVersion ||
		qualification.Runtime.CompatibilityDigest != seal.CompatibilityDigest ||
		qualification.Runtime.CatalogSchemaVersion != seal.CatalogSchemaVersion ||
		qualification.Gates.CandidateID != candidateID || qualification.Gates.RuntimeVersion != seal.RuntimeVersion ||
		qualification.Gates.DuckDBVersion != seal.DuckDBVersion {
		return invalid("qualification evidence differs from the seal")
	}

	projectID := identity.ProjectID
	provenance, err := source.releases.CandidateProvenance(ctx, projectID, candidateID, candidate.CandidateRevision)
	if err != nil {
		return invalid("candidate provenance is unavailable")
	}
	if provenance.Version != release.ProvenanceVersion || provenance.Validate() != nil ||
		provenance.Candidate.ID != candidateID || provenance.Candidate.Revision != candidate.CandidateRevision ||
		provenance.Plan.Identity != identity || provenance.Plan.TargetID != source.targetID ||
		provenance.Artifact.ContentDigest != artifactDigest || provenance.Plan.RuntimeVersion != seal.RuntimeVersion ||
		provenance.Plan.GateEvidence == nil || qualification.Gates.SourceDigest != provenance.Artifact.SourceDigest ||
		qualification.Gates.RuntimeVersion != provenance.Plan.RuntimeVersion ||
		qualification.Gates.BindingGeneration != release.BindingFingerprint(provenance.Plan.Bindings) ||
		!sameCanonicalGateEvidence(qualification.Gates, *provenance.Plan.GateEvidence) ||
		hasAmbiguousLocalPin(provenance.Plan) {
		return invalid("candidate provenance differs from qualification")
	}

	evidence := resultIdentityEvidenceFromProvenance(provenance)
	return evidence, nil
}

func resultIdentityEvidenceFromProvenance(provenance releasemodule.Provenance) appruntimefactory.ActivationEvidence {
	kinds := make(map[string]string, len(provenance.Plan.Bindings)+len(provenance.Plan.AuthoredConnections)+len(provenance.Plan.ManagedDataPins))
	for _, binding := range provenance.Plan.Bindings {
		kinds[binding.ConnectionID] = binding.ConnectorKind
	}
	for _, authored := range provenance.Plan.AuthoredConnections {
		kinds[authored.ConnectionID] = authored.ConnectorKind
	}
	for _, managed := range provenance.Plan.ManagedDataPins {
		kinds[managed.ConnectionID] = "managed"
	}
	return appruntimefactory.ActivationEvidence{
		RuntimeVersion:     provenance.Plan.RuntimeVersion,
		BindingFingerprint: release.BindingFingerprint(provenance.Plan.Bindings),
		BindingKinds:       kinds,
		Capabilities:       deployment.RuntimeCapabilityEvidence(provenance.Plan.Extensions),
	}
}

func sameCanonicalGateEvidence(left, right release.GateEvidence) bool {
	leftCanonical, leftErr := left.Canonical()
	rightCanonical, rightErr := right.Canonical()
	if leftErr != nil || rightErr != nil {
		return false
	}
	leftJSON, leftErr := json.Marshal(leftCanonical)
	rightJSON, rightErr := json.Marshal(rightCanonical)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func hasAmbiguousLocalPin(plan release.GenerationPlanProvenance) bool {
	local := make(map[string]struct{})
	counts := make(map[string]int)
	for _, binding := range plan.Bindings {
		counts[binding.ConnectionID]++
		if binding.CredentialVersionID != "" {
			local[binding.ConnectionID] = struct{}{}
		}
	}
	if len(local) == 0 {
		return false
	}
	for id := range local {
		if counts[id] != 1 {
			return true
		}
	}
	for _, authored := range plan.AuthoredConnections {
		if _, exists := local[authored.ConnectionID]; exists {
			return true
		}
	}
	for _, managed := range plan.ManagedDataPins {
		if _, exists := local[managed.ConnectionID]; exists {
			return true
		}
	}
	return false
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func sealedCandidateQualified(status string) bool {
	return status == "qualified" || status == "admitted"
}
