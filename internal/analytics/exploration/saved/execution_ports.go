package saved

import (
	"fmt"

	canonical "github.com/flidai/leapview/internal/analytics/exploration"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// ExecuteRequest identifies the current revision to execute for one actor.
// Request and correlation IDs are copied into the governed query metadata by
// the application service; they are not part of the authored payload.
type ExecuteRequest struct {
	ProjectID        projectgraph.ResourceID
	ID               ExplorationID
	ActorID          string
	RequestID        string
	CorrelationID    string
	ExpectedRevision RevisionToken
	Operation        string
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
	if !input.ExpectedRevision.IsZero() {
		if err := input.ExpectedRevision.ValidateComplete(); err != nil {
			return err
		}
	}
	if input.Operation != "" {
		if err := validateBoundedText(input.Operation, MaxOperationLength, "execute operation"); err != nil {
			return err
		}
	}
	return nil
}

// ExecuteSpecRequest runs caller-authored URL state without reading a saved
// exploration. The active viewer, not a link owner, supplies the identity.
type ExecuteSpecRequest struct {
	ProjectID     projectgraph.ResourceID
	ActorID       string
	Spec          canonical.ExplorationSpec
	RequestID     string
	CorrelationID string
	Operation     string
}

func (input ExecuteSpecRequest) ValidateEnvelope() error {
	if err := (ReadRequest{ProjectID: input.ProjectID, ID: ExplorationID("url-export"), ActorID: input.ActorID}).Validate(); err != nil {
		return err
	}
	for _, item := range []struct {
		value, name string
		limit       int
	}{
		{input.RequestID, "execute request id", MaxRequestIDLength},
		{input.CorrelationID, "execute correlation id", MaxCorrelationIDLength},
		{input.Operation, "execute operation", MaxOperationLength},
	} {
		if item.value != "" {
			if err := validateBoundedText(item.value, item.limit, item.name); err != nil {
				return err
			}
		}
	}
	return nil
}

func (input ExecuteSpecRequest) Validate() error {
	if err := input.ValidateEnvelope(); err != nil {
		return err
	}
	if err := canonical.ValidateShape(&input.Spec); err != nil {
		return fmt.Errorf("%w: canonical exploration spec: %v", ErrInvalidPayload, err)
	}
	return nil
}
