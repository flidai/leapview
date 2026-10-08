package deploymentpostgres

import (
	"errors"

	deploymentaudit "github.com/flidai/leapview/internal/app/deploymentaudit"
	credentialpostgres "github.com/flidai/leapview/internal/credential/postgres"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
)

// NewCredentialPublicationRepository preserves native publication's ordinary
// audit/event/lineage authorities while binding admission to ONE operation.
func NewCredentialPublicationRepository(control deploymentpostgres.DBTX, authorities Authorities, credentials *credentialpostgres.Repository, operationID string, authorize credentialpostgres.ActivationCommitAuthorizer) (*deploymentpostgres.Repository, error) {
	if control == nil || authorities.Access == nil || authorities.Events == nil || authorities.Lineage == nil {
		return nil, errors.New("credential publication requires native transactional authorities")
	}
	admission, err := newCredentialPublicationAdmission(deploymentpostgres.New(control), releasepostgres.New(control), credentials, operationID, authorize)
	if err != nil {
		return nil, err
	}
	return deploymentpostgres.NewWithOptions(control, deploymentpostgres.Options{ActivationAdmission: admission, ActivationAudit: deploymentaudit.NewWithRepository(authorities.Access), Events: authorities.Events, Lineage: authorities.Lineage}), nil
}
