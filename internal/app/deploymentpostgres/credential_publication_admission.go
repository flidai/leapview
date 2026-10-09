package deploymentpostgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/credential"
	credentialpostgres "github.com/flidai/leapview/internal/credential/postgres"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/platform/typednil"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

// newCredentialPublicationAdmission creates an admission port for one
// prepared credential operation. It is intentionally unexported and is not
// installed by the ordinary deployment composition: callers that explicitly
// coordinate a credential operation must supply the current-authority
// callback. The callback receives the same transaction that owns publication
// activation and must reauthorize the actor and current credential binding.
func newCredentialPublicationAdmission(
	delivery *deploymentpostgres.Repository,
	provenance candidateProvenanceTransactionReader,
	credentials *credentialpostgres.Repository,
	operationID string,
	authorize credentialpostgres.ActivationCommitAuthorizer,
) (deploymentpostgres.ActivationAdmissionPort, error) {
	if delivery == nil || !delivery.Configured() || typednil.IsNil(provenance) || !provenance.Configured() ||
		credentials == nil || authorize == nil {
		return nil, errors.New("credential publication admission requires configured authorities and current authorization")
	}
	parsedOperationID, err := uuid.Parse(operationID)
	if err != nil || parsedOperationID == uuid.Nil || parsedOperationID.String() != operationID {
		return nil, errors.New("credential publication admission requires a canonical operation ID")
	}

	return func(ctx context.Context, tx deploymentpostgres.Tx, publication deploymentpostgres.DeliveryPublication) error {
		if ctx == nil || typednil.IsNil(tx) || publication.State != "pending" {
			return deploymentpostgres.ErrInvalid
		}
		intent := credential.ActivationPublication{
			TargetID: publication.TargetID, ExpectedTargetRevision: publication.ExpectedTargetRevision,
			PredecessorGenerationID: publication.ExpectedBaseGenerationID,
			CandidateID:             publication.CandidateID, GenerationID: publication.GenerationID,
			PublicationID: publication.PublicationID, ActorID: publication.ActorID,
		}
		commitAuthority := func(ctx context.Context, tx credentialpostgres.Tx, prepared credential.PreparedActivation) error {
			if !intent.Matches(prepared.Preparation) {
				return fmt.Errorf("%w: credential operation differs from delivery publication", credential.ErrConflict)
			}
			if err := verifyCandidateCredentialPin(ctx, tx, delivery, provenance, publication, prepared); err != nil {
				return err
			}
			if err := verifyCredentialActivationContinuity(ctx, tx, delivery, provenance, publication, prepared.Preparation.Receipt.Binding.ResourceID); err != nil {
				return err
			}
			return authorize(ctx, tx, prepared)
		}
		_, err := credentials.CommitActivationPublicationTx(ctx, tx, operationID, intent, commitAuthority)
		return err
	}, nil
}

func verifyCandidateCredentialPin(
	ctx context.Context,
	tx deploymentpostgres.Tx,
	delivery *deploymentpostgres.Repository,
	provenanceRepository candidateProvenanceTransactionReader,
	publication deploymentpostgres.DeliveryPublication,
	prepared credential.PreparedActivation,
) error {
	preparation := prepared.Preparation
	binding := preparation.Receipt.Binding
	if binding.ScopeKind != "connection" || binding.TargetID != publication.TargetID ||
		binding.DeploymentID != publication.TargetID || binding.Provider != "postgres" {
		return fmt.Errorf("%w: credential receipt scope differs from delivery publication", credential.ErrConflict)
	}
	resolution, err := delivery.ResolveCandidateGenerationTx(ctx, tx, publication.CandidateID)
	if err != nil {
		return fmt.Errorf("resolve candidate for credential publication: %w", err)
	}
	candidate, err := delivery.CandidateTx(ctx, tx, publication.CandidateID)
	if err != nil {
		return fmt.Errorf("read candidate for credential publication: %w", err)
	}
	if resolution.CandidateID != publication.CandidateID || resolution.TargetID != publication.TargetID ||
		resolution.GenerationID != publication.GenerationID || resolution.SnapshotSealID != publication.SnapshotSealID ||
		candidate.CandidateID != publication.CandidateID || candidate.TargetID != publication.TargetID ||
		candidate.SnapshotSealID != publication.SnapshotSealID || candidate.CandidateRevision != resolution.CandidateRevision {
		return fmt.Errorf("%w: candidate identity differs from delivery publication", credential.ErrConflict)
	}
	if binding.ProjectID != resolution.ProjectID || binding.Environment != resolution.Environment {
		return fmt.Errorf("%w: credential receipt project or environment differs from candidate", credential.ErrConflict)
	}
	projectID, err := projectgraph.NewResourceID(resolution.ProjectID)
	if err != nil {
		return fmt.Errorf("%w: candidate project identity is invalid", credential.ErrConflict)
	}
	provenance, err := provenanceRepository.CandidateProvenanceTx(ctx, tx, projectID, resolution.CandidateID, resolution.CandidateRevision)
	if err != nil {
		return fmt.Errorf("read candidate provenance for credential publication: %w", err)
	}
	identity := provenance.Plan.Identity
	if provenance.Candidate.ID != resolution.CandidateID || provenance.Candidate.Revision != resolution.CandidateRevision ||
		provenance.Plan.TargetID != publication.TargetID || identity.GenerationID != publication.GenerationID ||
		identity.ProjectID.String() != resolution.ProjectID || identity.Environment != resolution.Environment ||
		provenance.Artifact.ContentDigest != candidate.ArtifactDigest {
		return fmt.Errorf("%w: candidate provenance identity differs from delivery publication", credential.ErrConflict)
	}

	matchingConnection := 0
	matchingPin := 0
	for _, evidence := range provenance.Plan.Bindings {
		if evidence.ConnectionID != binding.ResourceID {
			continue
		}
		matchingConnection++
		if evidence.BindingID == preparation.Receipt.BindingID && evidence.ConnectorKind == binding.Provider &&
			evidence.Revision == preparation.Receipt.BindingRevision && evidence.CredentialVersionID == binding.VersionID &&
			evidence.EndpointConfigHash == binding.Destination && evidence.ValidatedVersion == "" && evidence.Access == "" {
			matchingPin++
		}
	}
	if matchingConnection != 1 || matchingPin != 1 {
		return fmt.Errorf("%w: persisted candidate credential pin differs from validation receipt", credential.ErrConflict)
	}
	return nil
}
