package postgres

import (
	"encoding/json"
	"fmt"
	"time"
)

// ApprovalEventType is the stable event-log name for one approval mutation.
// Keeping this mapping in the delivery package makes event and audit
// composition adapters agree on one immutable vocabulary.
func ApprovalEventType(action ApprovalAction) (string, error) {
	switch action {
	case ApprovalActionRequest:
		return "approval_requested", nil
	case ApprovalActionApprove:
		return "approval_granted", nil
	case ApprovalActionDeny:
		return "approval_rejected", nil
	case ApprovalActionRevoke:
		return "approval_revoked", nil
	default:
		return "", fmt.Errorf("%w: unknown approval action %q", ErrApprovalInvalid, action)
	}
}

// ApprovalEvidencePayload is the canonical JSON payload shared by the event
// and audit appenders. It deliberately includes every immutable publication
// scope and credential evidence so a replay can never silently retarget an
// approval decision.
func ApprovalEvidencePayload(action ApprovalAction, request ApprovalRequest, decision *ApprovalDecision, evidence ApprovalEvidence) (json.RawMessage, error) {
	eventType, err := ApprovalEventType(action)
	if err != nil {
		return nil, err
	}
	// A request event has no decision actor. Do not leak the requester's
	// credential into the decision fields: those fields are immutable evidence
	// for a decision row only and must remain empty until one exists.
	actor := ApprovalActor{}
	revision := int64(0)
	decisionID := ""
	decisionAt := time.Time{}
	if decision != nil {
		actor = decision.DecidedBy
		revision = decision.Revision
		decisionID = decision.DecisionID
		decisionAt = decision.DecidedAt
	}
	var metadata any = map[string]any{}
	if len(evidence.Metadata) > 0 {
		if err := json.Unmarshal(evidence.Metadata, &metadata); err != nil {
			return nil, fmt.Errorf("%w: approval evidence metadata: %v", ErrApprovalInvalid, err)
		}
	}
	payload := map[string]any{
		"action": action, "event_type": eventType,
		"request_id": request.RequestID, "publication_id": request.PublicationID,
		"target_id": request.TargetID, "candidate_id": request.CandidateID,
		"generation_id": request.GenerationID, "request_digest": request.RequestDigest,
		"expected_target_revision":      request.ExpectedTargetRevision,
		"policy_revision":               request.PolicyRevision,
		"requested_by":                  request.RequestedBy.PrincipalID,
		"request_credential_class":      request.RequestedBy.CredentialClass,
		"request_credential_id":         request.RequestedBy.CredentialID,
		"request_credential_expires_at": formatApprovalTime(request.RequestedBy.CredentialExpiresAt),
		"expires_at":                    formatApprovalTime(request.ExpiresAt),
		"decision_id":                   decisionID, "decision_revision": revision,
		"decided_by": actor.PrincipalID, "decision_credential_class": actor.CredentialClass,
		"decision_credential_id":         actor.CredentialID,
		"decision_credential_expires_at": formatApprovalTime(actor.CredentialExpiresAt),
		"decided_at":                     formatApprovalTime(decisionAt),
		"evidence":                       metadata,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: approval evidence payload: %v", ErrApprovalInvalid, err)
	}
	return encoded, nil
}

func formatApprovalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
