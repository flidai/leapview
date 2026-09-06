package access

import (
	"context"
	"fmt"
)

// AuthorizationControlRevision is the identity and monotonic revision of an
// instance's live authorization control state. It is intentionally a small
// projection: callers that only need cache/lifecycle admission must not load
// or own a second copy of role and grant authority.
type AuthorizationControlRevision struct {
	InstanceID string
	ProjectID  string
	Revision   int64
}

// Validate checks the projection without consulting database state.
func (value AuthorizationControlRevision) Validate() error {
	if err := validateControlToken(value.InstanceID, "instance id"); err != nil {
		return err
	}
	if err := validateControlResourceID(value.ProjectID, "project id"); err != nil {
		return err
	}
	if value.Revision <= 0 {
		return fmt.Errorf("%w: control revision must be positive", ErrControlInvalidInput)
	}
	return nil
}

// AuthorizationControlRevisionReader is the narrow live read used by
// lifecycle-bound consumers. It deliberately does not expand Repository or
// ControlStore, preserving compatibility for existing implementations.
type AuthorizationControlRevisionReader interface {
	ReadAuthorizationControlRevision(context.Context, string) (AuthorizationControlRevision, error)
}
