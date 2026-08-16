package access

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/project/graph"
)

// CanonicalAuditEvent is the project-generation audit contract. It carries
// the immutable serving identity and exact ResourceRef used for authorization;
// it has no workspace, path, parent, or route-derived scope. Global identity
// and platform events remain represented by the global audit contract and do
// not use this type.
type CanonicalAuditEvent struct {
	Identity      graph.ServingIdentity
	PrincipalID   string
	Action        string
	Resource      ResourceRef
	Capability    Capability
	Status        string
	RequestID     string
	CorrelationID string
	MetadataJSON  string
}

func (event CanonicalAuditEvent) Validate() error {
	if err := event.Identity.Validate(); err != nil {
		return fmt.Errorf("audit identity: %w", err)
	}
	if strings.TrimSpace(event.PrincipalID) == "" {
		return fmt.Errorf("audit principal id is required")
	}
	if strings.TrimSpace(event.Action) == "" {
		return fmt.Errorf("audit action is required")
	}
	if err := event.Resource.Validate(); err != nil {
		return fmt.Errorf("audit resource: %w", err)
	}
	if err := ValidateCapabilityForKind(event.Resource.Kind(), event.Capability); err != nil {
		return fmt.Errorf("audit capability: %w", err)
	}
	return nil
}

// ValidateAgainst checks the exact resource kind and project identity against
// the authoritative graph used to build the serving generation.
func (event CanonicalAuditEvent) ValidateAgainst(project graph.ProjectGraph) error {
	if err := event.Validate(); err != nil {
		return err
	}
	if event.Identity.ProjectID != project.ProjectID() {
		return fmt.Errorf("audit project %q does not match graph %q", event.Identity.ProjectID, project.ProjectID())
	}
	if err := event.Resource.ValidateAgainst(project); err != nil {
		return fmt.Errorf("audit resource: %w", err)
	}
	return nil
}

// CanonicalAuditRecorder persists project-generation audit records. It is
// deliberately separate from the global identity audit recorder so callers
// cannot accidentally fill a project scope from a route or workspace value.
type CanonicalAuditRecorder interface {
	RecordCanonicalAuditEvent(context.Context, CanonicalAuditEvent) error
}

func PersistCanonicalAuditEvent(ctx context.Context, recorder CanonicalAuditRecorder, event CanonicalAuditEvent) error {
	if recorder == nil {
		return fmt.Errorf("canonical audit recorder is required")
	}
	if err := event.Validate(); err != nil {
		return err
	}
	if err := recorder.RecordCanonicalAuditEvent(ctx, event); err != nil {
		return fmt.Errorf("persist canonical audit event %q: %w", event.Action, err)
	}
	return nil
}
