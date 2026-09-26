package jobs

import jobauthority "github.com/flidai/leapview/pkg/authority"

// AuthorityEnvelope and its supporting types are aliases so the public jobs
// API carries the shared authority contract without importing LeapView's
// private access package directly.
type AuthorityMode = jobauthority.AuthorityMode
type CredentialEvidence = jobauthority.CredentialEvidence
type ExecutionGrantEvidence = jobauthority.ExecutionGrantEvidence
type AuthorityTarget = jobauthority.AuthorityTarget
type AuthorityEnvelope = jobauthority.AuthorityEnvelope
type AuthorityRevalidator = jobauthority.AuthorityRevalidator
type AuthorityRevalidatorFunc = jobauthority.AuthorityRevalidatorFunc

const (
	AuthorityEnvelopeProfile = jobauthority.AuthorityEnvelopeProfile
	CredentialClassSession   = jobauthority.CredentialClassSession
	CredentialClassAPIToken  = jobauthority.CredentialClassAPIToken
	CallerAuthorityMode      = jobauthority.CallerAuthorityMode
	DelegatedWorkloadMode    = jobauthority.DelegatedWorkloadMode
)

var (
	ErrAuthorityInvalid       = jobauthority.ErrAuthorityInvalid
	ErrAuthorityRequired      = jobauthority.ErrAuthorityRequired
	ErrAuthorityRevalidator   = jobauthority.ErrAuthorityRevalidator
	ErrAuthorityNoPermissions = jobauthority.ErrAuthorityNoPermissions
)

var MarshalAuthority = jobauthority.MarshalAuthority
var UnmarshalAuthority = jobauthority.UnmarshalAuthority
