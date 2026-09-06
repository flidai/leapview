package module

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
)

// ReadAuthorizationControlRevision returns the live instance control
// revision through the repository adapter. The module does not cache,
// evaluate, or project authorization here; callers retain the revision that
// belongs to their serving snapshot and use this read only for revalidation.
func (m *Module) ReadAuthorizationControlRevision(ctx context.Context, instanceID string) (access.AuthorizationControlRevision, error) {
	if m == nil || m.repository == nil {
		return access.AuthorizationControlRevision{}, fmt.Errorf("access repository is unavailable")
	}
	if instanceID == "" || instanceID != strings.TrimSpace(instanceID) || len(instanceID) > 255 || strings.ContainsAny(instanceID, "\x00\r\n\t") {
		return access.AuthorizationControlRevision{}, fmt.Errorf("%w: instance id", access.ErrControlInvalidInput)
	}
	if m.instanceID != "" && m.instanceID != instanceID {
		return access.AuthorizationControlRevision{}, fmt.Errorf("%w: module instance does not match lookup", access.ErrControlIdentityConflict)
	}
	repository, err := m.repository()
	if err != nil {
		return access.AuthorizationControlRevision{}, err
	}
	reader, ok := repository.(access.AuthorizationControlRevisionReader)
	if !ok {
		return access.AuthorizationControlRevision{}, fmt.Errorf("access repository does not support authorization control revision reads")
	}
	value, err := reader.ReadAuthorizationControlRevision(ctx, instanceID)
	if err != nil {
		return access.AuthorizationControlRevision{}, err
	}
	if value.InstanceID != instanceID {
		return access.AuthorizationControlRevision{}, fmt.Errorf("%w: control state instance identity does not match lookup", access.ErrControlIdentityConflict)
	}
	if err := value.Validate(); err != nil {
		return access.AuthorizationControlRevision{}, err
	}
	return value, nil
}
