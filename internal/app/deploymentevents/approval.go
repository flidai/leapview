package deploymentevents

import (
	"context"
	"errors"
	"fmt"

	depauth "github.com/flidai/leapview/internal/deployment/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	eventswatermill "github.com/flidai/leapview/internal/platform/events/watermill"
)

var _ depauth.ApprovalEventAppender = (*Adapter)(nil)

// AppendApprovalEvent writes one immutable approval event using the supplied
// transaction. The event payload includes exact publication and credential
// evidence and is replay-checked by the platform event authority.
func (a *Adapter) AppendApprovalEvent(ctx context.Context, tx depauth.Tx, input depauth.ApprovalEvent) error {
	if a == nil || a.events == nil || a.boundary == nil || tx == nil {
		return fmt.Errorf("%w: approval event adapter is not configured", depauth.ErrInvalid)
	}
	eventType, err := depauth.ApprovalEventType(input.Action)
	if err != nil {
		return err
	}
	payload, err := depauth.ApprovalEvidencePayload(input.Action, input.Request, input.Decision, input.Evidence)
	if err != nil {
		return err
	}
	stored, err := a.boundary.AppendEvent(ctx, tx, eventswatermill.TopicDelivery, eventspostgres.EventInput{
		EventID: input.Evidence.EventID, ScopeID: input.Request.TargetID,
		AggregateType: "delivery_approval", AggregateID: input.Request.RequestID,
		EventType: eventType, SchemaVersion: 1, CorrelationID: input.Evidence.EventID,
		Payload: payload,
	})
	if err != nil {
		if isWatermillValidation(err) {
			return fmt.Errorf("%w: approval event: %v", depauth.ErrInvalid, err)
		}
		var conflict *eventspostgres.EventConflictError
		if errors.As(err, &conflict) {
			return fmt.Errorf("%w: approval event identity differs", depauth.ErrConflict)
		}
		return err
	}
	if stored.EventID != input.Evidence.EventID || stored.ScopeID != input.Request.TargetID || stored.AggregateType != "delivery_approval" || stored.AggregateID != input.Request.RequestID || stored.EventType != eventType || stored.SchemaVersion != 1 || stored.CorrelationID != input.Evidence.EventID {
		return fmt.Errorf("%w: approval event identity differs", depauth.ErrConflict)
	}
	return nil
}
