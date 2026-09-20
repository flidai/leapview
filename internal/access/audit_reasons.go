package access

// AuditDenialReason is the stable, non-secret reason vocabulary used when a
// privileged request is rejected. Keep these values suitable for durable
// audit metadata; callers must not persist raw error strings because they can
// contain backend details or submitted credentials.
type AuditDenialReason string

const (
	AuditReasonAuthorizationDenied      AuditDenialReason = "authorization_denied"
	AuditReasonCredentialAttenuated     AuditDenialReason = "credential_attenuated"
	AuditReasonRecentAuthentication     AuditDenialReason = "recent_authentication_required"
	AuditReasonApprovalRequired         AuditDenialReason = "approval_required"
	AuditReasonConfigurationUnavailable AuditDenialReason = "configuration_unavailable"
	AuditReasonInvalidRequest           AuditDenialReason = "invalid_request"
	AuditReasonPreconditionFailed       AuditDenialReason = "precondition_failed"
	AuditReasonNotFound                 AuditDenialReason = "not_found"
	AuditReasonConflict                 AuditDenialReason = "conflict"
	AuditReasonIdempotencyConflict      AuditDenialReason = "idempotency_conflict"
	AuditReasonApprovalSeparationOfDuty AuditDenialReason = "approval_separation_of_duty"
	AuditReasonApprovalExpired          AuditDenialReason = "approval_expired"
	AuditReasonApprovalNotDue           AuditDenialReason = "approval_not_due"
	AuditReasonInternalFailure          AuditDenialReason = "internal_failure"
)
