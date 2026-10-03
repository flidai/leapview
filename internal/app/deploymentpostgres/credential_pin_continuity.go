package deploymentpostgres

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/platform/typednil"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

// verifyPublicationCredentialContinuity runs under activation's target lock,
// after its predecessor CAS check. Local credential pins must remain unchanged
// across ordinary publication. It never resolves or decrypts credentials.
func verifyPublicationCredentialContinuity(ctx context.Context, tx deploymentpostgres.Tx, delivery *deploymentpostgres.Repository, provenance candidateProvenanceTransactionReader, publication deploymentpostgres.DeliveryPublication) error {
	if ctx == nil || typednil.IsNil(tx) || publication.State != "pending" {
		return deploymentpostgres.ErrInvalid
	}
	target, err := delivery.TargetTx(ctx, tx, publication.TargetID)
	if err != nil {
		return err
	}
	if target.ActiveGenerationID != publication.ExpectedBaseGenerationID || target.TargetRevision != publication.ExpectedTargetRevision {
		return deploymentpostgres.ErrCASConflict
	}
	successor, pins, err := publicationGenerationCredentialPins(ctx, tx, delivery, provenance, target, publication.GenerationID)
	if err != nil {
		return err
	}
	if successor.Candidate.ID != publication.CandidateID {
		return credentialPinContinuityConflict()
	}
	resolution, err := delivery.ResolveCandidateGenerationTx(ctx, tx, publication.CandidateID)
	if err != nil {
		return err
	}
	if resolution.TargetID != target.TargetID || resolution.ProjectID != target.ProjectID || resolution.Environment != target.Environment ||
		resolution.GenerationID != publication.GenerationID || resolution.SnapshotSealID != publication.SnapshotSealID || resolution.CandidateRevision != successor.Candidate.Revision {
		return credentialPinContinuityConflict()
	}
	previous := map[string]release.BindingEvidence{}
	if publication.ExpectedBaseGenerationID != "" {
		identity := projectgraph.ServingIdentity{ProjectID: projectgraph.ResourceID(target.ProjectID), Environment: target.Environment, GenerationID: publication.ExpectedBaseGenerationID}
		proof, err := delivery.CommittedGenerationTx(ctx, tx, target.TargetID, identity)
		if err != nil {
			return fmt.Errorf("read predecessor credential commitment: %w", err)
		}
		predecessor, predecessorPins, err := publicationGenerationCredentialPins(ctx, tx, delivery, provenance, target, identity.GenerationID)
		if err != nil {
			return err
		}
		if proof.TargetID != target.TargetID || proof.Identity != identity || proof.CandidateID != predecessor.Candidate.ID ||
			proof.CandidateRevision != predecessor.Candidate.Revision || proof.ServingArtifactDigest != predecessor.Artifact.ContentDigest ||
			(len(predecessorPins) > 0 && proof.BindingFingerprint != release.BindingFingerprint(predecessor.Plan.Bindings)) {
			return credentialPinContinuityConflict()
		}
		previous = predecessorPins
	}
	if !maps.Equal(previous, pins) {
		return credentialPinContinuityConflict()
	}
	return nil
}

func publicationGenerationCredentialPins(ctx context.Context, tx deploymentpostgres.Tx, delivery *deploymentpostgres.Repository, provenance candidateProvenanceTransactionReader, target deploymentpostgres.DeliveryTarget, generationID string) (release.Provenance, map[string]release.BindingEvidence, error) {
	fail := func(err error) (release.Provenance, map[string]release.BindingEvidence, error) {
		return release.Provenance{}, nil, err
	}
	generation, err := delivery.GenerationTx(ctx, tx, generationID)
	if err != nil {
		return fail(err)
	}
	candidate, err := delivery.CandidateTx(ctx, tx, generation.CandidateID)
	if err != nil {
		return fail(err)
	}
	seal, err := delivery.SnapshotSealTx(ctx, tx, generation.SnapshotSealID)
	if err != nil {
		return fail(err)
	}
	if generation.GenerationID != generationID || generation.TargetID != target.TargetID || candidate.TargetID != target.TargetID ||
		candidate.SnapshotSealID != generation.SnapshotSealID || seal.CandidateID != candidate.CandidateID ||
		seal.PlanDigest != generation.PlanDigest || candidate.ArtifactDigest != generation.ServingArtifactDigest || seal.ServingArtifactDigest != generation.ServingArtifactDigest {
		return fail(credentialPinContinuityConflict())
	}
	p, err := provenance.CandidateProvenanceTx(ctx, tx, projectgraph.ResourceID(target.ProjectID), candidate.CandidateID, candidate.CandidateRevision)
	if err != nil {
		return fail(fmt.Errorf("read publication credential provenance: %w", err))
	}
	identity := projectgraph.ServingIdentity{ProjectID: projectgraph.ResourceID(target.ProjectID), Environment: target.Environment, GenerationID: generationID}
	if p.Validate() != nil || p.Plan.TargetID != target.TargetID || p.Plan.Identity != identity ||
		p.Candidate.ID != candidate.CandidateID || p.Candidate.Revision != candidate.CandidateRevision || p.Artifact.ContentDigest != generation.ServingArtifactDigest {
		return fail(credentialPinContinuityConflict())
	}
	pins := make(map[string]release.BindingEvidence)
	counts := make(map[string]int)
	for _, binding := range p.Plan.Bindings {
		counts[binding.ConnectionID]++
		if binding.CredentialVersionID != "" {
			pins[binding.ConnectionID] = binding
		}
	}
	if len(pins) == 0 {
		return p, pins, nil
	}
	for _, count := range counts {
		if count != 1 {
			return fail(credentialPinContinuityConflict())
		}
	}
	for _, authored := range p.Plan.AuthoredConnections {
		if _, overlap := pins[authored.ConnectionID]; overlap {
			return fail(credentialPinContinuityConflict())
		}
	}
	for _, managed := range p.Plan.ManagedDataPins {
		if _, overlap := pins[managed.ConnectionID]; overlap {
			return fail(credentialPinContinuityConflict())
		}
	}
	// The provenance's own gate hash is validated above. Independently bind
	// its pins to the persisted seal, as runtime committed-pin resolution does.
	var qualification struct {
		Gates struct {
			BindingGeneration string `json:"bindingGeneration"`
		} `json:"gates"`
	}
	if json.Unmarshal(seal.QualificationEvidence, &qualification) != nil || qualification.Gates.BindingGeneration != release.BindingFingerprint(p.Plan.Bindings) {
		return fail(credentialPinContinuityConflict())
	}
	return p, pins, nil
}

func credentialPinContinuityConflict() error {
	return fmt.Errorf("%w: publication local credential pins differ from committed authority", deploymentpostgres.ErrConflict)
}
