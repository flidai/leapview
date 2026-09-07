package deploymentoperation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	depauth "github.com/flidai/leapview/internal/deployment/postgres"
	operationpostgres "github.com/flidai/leapview/internal/platform/operation/postgres"
)

var _ depauth.ApprovalOperationAppender = (*Adapter)(nil)

// AppendApprovalOperation persists a preallocated terminal operation. The
// operation ID comes from immutable approval evidence, so retries resolve to
// exactly one platform.operation row and remain atomic with the approval
// mutation transaction.
func (a *Adapter) AppendApprovalOperation(ctx context.Context, tx depauth.Tx, input depauth.ApprovalOperation) error {
	if a == nil || a.operations == nil || tx == nil {
		return fmt.Errorf("%w: approval operation adapter is not configured", depauth.ErrInvalid)
	}
	payload, err := depauth.ApprovalEvidencePayload(input.Action, input.Request, input.Decision, input.Evidence)
	if err != nil {
		return err
	}
	actor := input.Request.RequestedBy
	if input.Decision != nil {
		actor = input.Decision.DecidedBy
	}
	operationID := input.Evidence.OperationID
	key := "approval:" + string(input.Action) + ":" + operationID
	requestDigest := input.Request.RequestDigest
	if requestDigest == "" {
		return fmt.Errorf("%w: approval operation request digest is required", depauth.ErrInvalid)
	}
	_, err = a.operations.AppendTerminalTx(ctx, tx, operationpostgres.AppendTerminalInput{
		OperationID: operationID, Scope: input.Request.TargetID,
		OperationType: "delivery.approval." + string(input.Action), IdempotencyKey: key,
		RequestDigest: requestDigest, OwnerID: actor.PrincipalID, Outcome: json.RawMessage(payload),
	})
	if err != nil {
		if errors.Is(err, operationpostgres.ErrConflict) {
			return fmt.Errorf("%w: approval operation identity differs", depauth.ErrConflict)
		}
		return err
	}
	return nil
}
