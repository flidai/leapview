package deploymentpostgres

import (
	"context"
	"errors"

	deploymentaudit "github.com/flidai/leapview/internal/app/deploymentaudit"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	credentialpostgres "github.com/flidai/leapview/internal/credential/postgres"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
)

// NewFirstSourcePublicationRepository binds the first-source request journal to
// the ordinary publication transaction. A later CAS/approval/audit failure rolls
// back both credential journals together with the native operation outcome.
func NewFirstSourcePublicationRepository(control deploymentpostgres.DBTX, authorities Authorities, credentials *credentialpostgres.Repository, row credentialmodule.ActivationRequestRecord, authorize credentialpostgres.ActivationCommitAuthorizer) (*deploymentpostgres.Repository, error) {
	if control == nil || authorities.Access == nil || authorities.Events == nil || authorities.Lineage == nil || row.Validate() != nil || row.State != "switching" || row.PublicationID == "" || row.PlanID == "" {
		return nil, errors.New("first-source publication requires exact switching intent and native authorities")
	}
	admit, err := newCredentialPublicationAdmission(deploymentpostgres.New(control), releasepostgres.New(control), credentials, row.Request.OperationID, authorize)
	if err != nil {
		return nil, err
	}
	admission := func(ctx context.Context, tx deploymentpostgres.Tx, publication deploymentpostgres.DeliveryPublication) error {
		if publication.PublicationID != row.PublicationID || publication.GenerationID != row.GenerationID || publication.CandidateID != row.CandidateID || publication.ActorID != row.Receipt.ActorID || publication.ExpectedBaseGenerationID != "" {
			return credentialmodule.ErrValidationConflict
		}
		if err := admit(ctx, tx, publication); err != nil {
			return err
		}
		next := row
		next.State = "committed"
		_, err := credentials.TransitionActivationRequestTx(ctx, tx, row, next, row.Receipt.ActorID, "", func(context.Context, credentialpostgres.Tx) error {
			// The operation-specific admission above already locked and verified
			// current authority, exact receipt, candidate and empty predecessor in
			// this transaction; there is no pool-based read between these writes.
			return nil
		})
		return err
	}
	return deploymentpostgres.NewWithOptions(control, deploymentpostgres.Options{ActivationAdmission: admission, ActivationAudit: deploymentaudit.NewWithRepository(authorities.Access), Events: authorities.Events, Lineage: authorities.Lineage}), nil
}
