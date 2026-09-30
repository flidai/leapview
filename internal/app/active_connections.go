package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	appdeploymentpostgres "github.com/flidai/leapview/internal/app/deploymentpostgres"
	appruntimefactory "github.com/flidai/leapview/internal/app/runtimefactory"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	releasemodule "github.com/flidai/leapview/internal/release/module"
)

type servingStateProvenanceReader interface {
	ProvenanceForServingState(context.Context, projectgraph.ServingIdentity) (releasemodule.Provenance, error)
}

type committedGenerationReader interface {
	CommittedGeneration(context.Context, string, projectgraph.ServingIdentity) (appdeploymentpostgres.CommittedGenerationEvidence, error)
}

type activeConnectionEvidenceSource struct {
	releases    servingStateProvenanceReader
	commitments committedGenerationReader
	targetID    string
	environment string
}

func (source activeConnectionEvidenceSource) BindingEvidence(
	ctx context.Context,
	servingStateID string,
	projectID string,
) ([]analyticsmodule.ActiveRuntimeBindingEvidence, error) {
	provenance, err := source.provenance(ctx, servingStateID, projectID)
	if err != nil {
		return nil, err
	}
	result := make([]analyticsmodule.ActiveRuntimeBindingEvidence, len(provenance.Plan.Bindings))
	for index, evidence := range provenance.Plan.Bindings {
		bindingID, parseErr := connectionbinding.ParseBindingID(evidence.BindingID)
		connectionID, connectionErr := connectionbinding.ParseConnectionID(evidence.ConnectionID)
		if parseErr != nil || connectionErr != nil {
			return nil, fmt.Errorf("%w: invalid release binding evidence", releasemodule.ErrProvenanceInvalid)
		}
		result[index] = analyticsmodule.ActiveRuntimeBindingEvidence{
			BindingID: bindingID, ConnectionID: connectionID,
			ConnectorKind: evidence.ConnectorKind, Revision: evidence.Revision,
			ValidatedVersion: evidence.ValidatedVersion, EndpointConfigHash: evidence.EndpointConfigHash, Access: evidence.Access,
			CredentialVersionID: evidence.CredentialVersionID,
		}
	}
	return result, nil
}

func (source activeConnectionEvidenceSource) ResultIdentityEvidence(
	ctx context.Context,
	identity projectgraph.ServingIdentity,
) (appruntimefactory.ActivationEvidence, error) {
	if err := identity.Validate(); err != nil || identity.Environment != source.environment {
		return appruntimefactory.ActivationEvidence{}, fmt.Errorf("%w: result identity serving scope does not match runtime target", releasemodule.ErrProvenanceInvalid)
	}
	provenance, err := source.provenance(ctx, identity.GenerationID, identity.ProjectID.String())
	if err != nil {
		return appruntimefactory.ActivationEvidence{}, err
	}
	return resultIdentityEvidenceFromProvenance(provenance), nil
}

func (source activeConnectionEvidenceSource) provenance(
	ctx context.Context,
	servingStateID string,
	projectID string,
) (releasemodule.Provenance, error) {
	if source.releases == nil {
		return releasemodule.Provenance{}, releasemodule.ErrNotFound
	}
	identity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID(projectID), source.environment, servingStateID)
	if err != nil {
		return releasemodule.Provenance{}, err
	}
	provenance, err := source.releases.ProvenanceForServingState(ctx, identity)
	if err != nil {
		return releasemodule.Provenance{}, err
	}
	if provenance.Plan.TargetID != strings.TrimSpace(source.targetID) ||
		provenance.Plan.Identity != identity {
		return releasemodule.Provenance{}, fmt.Errorf("%w: release target does not match runtime target", releasemodule.ErrProvenanceInvalid)
	}
	if err := source.verifyCredentialPinCommitment(ctx, provenance); err != nil {
		return releasemodule.Provenance{}, err
	}
	return provenance, nil
}

// Candidate provenance is useful during preparation but cannot prove a local
// credential was committed. This check binds pins to delivery's exact sealed
// generation. Current workload authority and admission remain separate gates.
func (source activeConnectionEvidenceSource) verifyCredentialPinCommitment(ctx context.Context, provenance releasemodule.Provenance) error {
	hasLocalPin := false
	for _, binding := range provenance.Plan.Bindings {
		hasLocalPin = hasLocalPin || binding.CredentialVersionID != ""
	}
	if !hasLocalPin {
		return nil
	}
	if provenance.Version != release.ProvenanceVersion || provenance.Validate() != nil {
		return releasemodule.ErrProvenanceInvalid
	}
	if source.commitments == nil {
		return releasemodule.ErrNotFound
	}
	proof, err := source.commitments.CommittedGeneration(ctx, source.targetID, provenance.Plan.Identity)
	if err != nil {
		return releasemodule.ErrNotFound
	}
	if proof.Identity != provenance.Plan.Identity || proof.TargetID != source.targetID ||
		proof.PublicationID == "" || proof.SnapshotSealID == "" ||
		proof.CandidateID != provenance.Candidate.ID || proof.CandidateRevision != provenance.Candidate.Revision ||
		proof.ServingArtifactDigest != provenance.Artifact.ContentDigest ||
		proof.BindingFingerprint != release.BindingFingerprint(provenance.Plan.Bindings) {
		return releasemodule.ErrProvenanceInvalid
	}
	return nil
}
