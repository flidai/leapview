package saved

import (
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// ExecuteRequest identifies the current revision to execute for one actor.
// Request and correlation IDs are copied into the governed query metadata by
// the application service; they are not part of the authored payload.
type ExecuteRequest struct {
	ProjectID     projectgraph.ResourceID
	ID            ExplorationID
	ActorID       string
	RequestID     string
	CorrelationID string
}

func (input ExecuteRequest) Validate() error {
	if err := (ReadRequest{ProjectID: input.ProjectID, ID: input.ID, ActorID: input.ActorID}).Validate(); err != nil {
		return err
	}
	if input.RequestID != "" {
		if err := validateBoundedText(input.RequestID, MaxRequestIDLength, "execute request id"); err != nil {
			return err
		}
	}
	if input.CorrelationID != "" {
		if err := validateBoundedText(input.CorrelationID, MaxCorrelationIDLength, "execute correlation id"); err != nil {
			return err
		}
	}
	return nil
}
