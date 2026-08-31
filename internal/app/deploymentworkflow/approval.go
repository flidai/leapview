package deploymentworkflow

import (
	"context"
	"encoding/json"
	"fmt"

	depauth "github.com/flidai/leapview/internal/deployment/postgres"
	jobpolicy "github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/pkg/jobs"
)

var _ depauth.ApprovalActivationAppender = (*Adapter)(nil)

// EnqueueApprovalActivation records the approval event and deterministic
// activation job in the caller-owned transaction. A worker later rechecks the
// effective decision and publication fence before activating.
func (a *Adapter) EnqueueApprovalActivation(ctx context.Context, tx depauth.Tx, request depauth.ApprovalRequest, decision depauth.ApprovalDecision) error {
	if a == nil || a.jobs == nil || a.delivery == nil || tx == nil {
		return fmt.Errorf("%w: approval activation workflow is not configured", depauth.ErrInvalid)
	}
	publication, err := a.delivery.PublicationTx(ctx, tx, request.PublicationID)
	if err != nil {
		return err
	}
	if publication.TargetID != request.TargetID || publication.GenerationID != request.GenerationID || publication.CandidateID != request.CandidateID || publication.RequestDigest != request.RequestDigest || publication.ExpectedTargetRevision != request.ExpectedTargetRevision {
		return fmt.Errorf("%w: approval activation publication identity differs", depauth.ErrApprovalConflict)
	}
	if decision.RequestID != request.RequestID || decision.Decision != depauth.ApprovalActionApprove || decision.DecisionID == "" || decision.Revision <= 0 || decision.DecidedBy.PrincipalID == "" {
		return fmt.Errorf("%w: approval activation decision identity is invalid", depauth.ErrApprovalInvalid)
	}
	payload := map[string]any{
		"request_id": request.RequestID, "publication_id": request.PublicationID,
		"target_id": request.TargetID, "generation_id": request.GenerationID,
		"candidate_id": request.CandidateID, "request_digest": request.RequestDigest,
		"expected_target_revision": request.ExpectedTargetRevision,
		"policy_revision":          request.PolicyRevision, "decision_id": decision.DecisionID,
		"decision_revision": decision.Revision,
		// Keep requester, reviewer, and publication actor distinct. Activation
		// resolves and rechecks the latter from the immutable publication row.
		"publication_actor_id": publication.ActorID, "requested_by": request.RequestedBy.PrincipalID,
		"decided_by": decision.DecidedBy.PrincipalID, "idempotency_key": decision.DecisionID,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	intent := jobs.WorkflowIntent{
		Event: jobs.EventInput{Key: "delivery:approval:activation:" + decision.DecisionID, ResourceKind: "delivery_approval", ResourceID: request.RequestID, EventType: "activation_requested", Data: encoded},
		Job:   jobs.EnqueueInput{ID: "delivery-approval-activation-" + decision.DecisionID, Kind: "delivery.approval.activate", WorkloadClass: jobpolicy.WorkloadClassControl, PrincipalID: decision.DecidedBy.PrincipalID, PartitionKey: request.TargetID, ResourceKind: "delivery_publication", ResourceID: request.PublicationID, EstimatedMemoryBytes: 1, Payload: encoded},
	}
	if err := a.jobs.RecordWorkflow(ctx, tx, intent); err != nil {
		return err
	}
	return nil
}
