package deploymentaudit

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	depauth "github.com/flidai/leapview/internal/deployment/postgres"
)

var _ depauth.ApprovalAuditAppender = (*Adapter)(nil)

// AppendApprovalAudit records approval evidence in Access' immutable audit
// table through the caller-owned delivery transaction.
func (a *Adapter) AppendApprovalAudit(ctx context.Context, tx depauth.Tx, input depauth.ApprovalAudit) error {
	if a == nil || a.audit == nil || tx == nil {
		return fmt.Errorf("%w: approval audit adapter is not configured", depauth.ErrInvalid)
	}
	payload, err := depauth.ApprovalEvidencePayload(input.Action, input.Request, input.Decision, input.Evidence)
	if err != nil {
		return err
	}
	action, err := depauth.ApprovalEventType(input.Action)
	if err != nil {
		return err
	}
	actor := input.Request.RequestedBy
	// The request is the aggregate root (revision zero); decision revisions
	// follow it as their exact aggregate sequence.
	sequence := int64(0)
	if input.Decision != nil {
		actor = input.Decision.DecidedBy
		sequence = input.Decision.Revision
	}
	intent := access.AuditIntent{
		EventID: input.Evidence.AuditID, DomainEventID: input.Evidence.EventID,
		ScopeID: input.Request.TargetID, ActorID: actor.PrincipalID, Source: "deployment",
		Operation: "delivery.approval." + string(input.Action), Action: "delivery." + action,
		ResourceKind: "approval", ResourceID: input.Request.RequestID, Outcome: "success",
		RequestID: input.Request.RequestID, CorrelationID: input.Evidence.EventID,
		RequestDigest: input.Request.RequestDigest,
		AggregateKey:  "delivery:approval:" + input.Request.RequestID, AggregateSequence: sequence,
		MetadataJSON: string(payload),
	}
	_, err = a.audit.RecordAuditEvent(ctx, tx, intent)
	if err != nil {
		return normalize(err, "append approval")
	}
	return nil
}
