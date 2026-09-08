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
	ProjectID     projectgraph.ResourceID
	ID            ExplorationID
	ActorID       string
	RequestID     string
	CorrelationID string
	// ExpectedRevision is required by export callers. A zero token preserves
	// the interactive "execute current" behavior; a non-zero token is checked
	// against the lifecycle while holding the same serving lease.
	ExpectedRevision RevisionToken
	// Operation lets an audited caller distinguish an export from an interactive
	// execution without changing the authored exploration query.
	Operation string
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

// ExecuteSpecRequest executes canonical URL state without creating or reading
// a durable saved-exploration identity. The application service still acquires
// the active lease, authorizes the target semantic model with RESOURCE_USE,
// validates against that generation, and invokes the lease-bound executor.
type ExecuteSpecRequest struct {
	ProjectID     projectgraph.ResourceID
	ActorID       string
	Spec          canonical.ExplorationSpec
	RequestID     string
	CorrelationID string
	Operation     string
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

// ValidateEnvelope checks only caller identity and transport metadata. The
// application service uses this before authorization, then validates the
// authored spec after the target model capability has been accepted; this
// keeps malformed field/model details from becoming an unauthorized metadata
// oracle while retaining a complete validation method for direct callers.
func (input ExecuteSpecRequest) ValidateEnvelope() error {
	if err := (ReadRequest{ProjectID: input.ProjectID, ID: ExplorationID("url-export"), ActorID: input.ActorID}).Validate(); err != nil {
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
	if input.Operation != "" {
		if err := validateBoundedText(input.Operation, MaxOperationLength, "execute operation"); err != nil {
			return err
		}
	}
	return nil
}
