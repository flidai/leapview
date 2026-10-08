package module

import (
	"github.com/flidai/leapview/internal/credential"
	credentialpostgres "github.com/flidai/leapview/internal/credential/postgres"
)

// Transaction contracts let the application compose native publication with
// the credential journal in one commit, without selecting another authority.
type ActivationRepository = credentialpostgres.Repository
type ActivationCommitAuthorizer = credentialpostgres.ActivationCommitAuthorizer
type ActivationRequestAuthorizer = credentialpostgres.ActivationRequestAuthorizer
type ActivationPreparation = credential.ActivationPreparation
type PreparedActivation = credential.PreparedActivation

var ErrValidationForbidden = credential.ErrForbidden
