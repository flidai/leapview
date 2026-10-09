package credential

import (
	"context"
	"errors"
	"time"

	"github.com/flidai/leapview/internal/platform/typednil"
)

// VersionDependency identifies retained authority that may still require the
// exact version. It contains no credential values or provider references.
type VersionDependency struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type VersionStatus struct {
	VersionID        string              `json:"versionId"`
	State            string              `json:"state"`
	RetiredAt        time.Time           `json:"retiredAt,omitempty"`
	Dependencies     []VersionDependency `json:"dependencies"`
	MoreDependencies bool                `json:"moreDependencies"`
}

// RetirementAuthority checks live permissions and all durable references while
// holding the publication and version fences. Retire atomically records the
// irreversible local state and its audit; it never deletes encrypted history.
type RetirementAuthority interface {
	InspectVersion(context.Context, string, Resource, string) (VersionStatus, error)
	RetireVersion(context.Context, string, Resource, string) (VersionStatus, error)
}

// ConfigureRetirement is a composition-time operation, before serving requests.
func (service *ActivationCoordinator) ConfigureRetirement(authority RetirementAuthority) error {
	if service == nil || typednil.IsNil(authority) || service.retirement != nil {
		return ErrUnavailable
	}
	service.retirement = authority
	return nil
}

func (service *ActivationCoordinator) InspectVersion(ctx context.Context, actor string, resource Resource, version string) (VersionStatus, error) {
	if service == nil || ctx == nil || !canonical(actor) || resource.Validate() != nil || !activationUUID(version) {
		return VersionStatus{}, ErrInvalid
	}
	if typednil.IsNil(service.retirement) {
		return VersionStatus{}, ErrUnavailable
	}
	return service.retirement.InspectVersion(ctx, actor, resource, version)
}

// RetireVersion shares activation's serialization and provider drain. A process
// crash leaves either an available version or a committed retired_local record;
// normal startup reconstructs only authoritative, non-retired current versions.
func (service *ActivationCoordinator) RetireVersion(ctx context.Context, actor string, resource Resource, version string) (VersionStatus, error) {
	if service == nil || ctx == nil || !canonical(actor) || resource.Validate() != nil || !activationUUID(version) {
		return VersionStatus{}, ErrInvalid
	}
	if typednil.IsNil(service.retirement) {
		return VersionStatus{}, ErrUnavailable
	}
	unlock, err := service.lock(ctx)
	if err != nil {
		return VersionStatus{}, err
	}
	defer unlock()
	status, err := service.retirement.InspectVersion(ctx, actor, resource, version)
	if err != nil {
		return status, err
	}
	if status.State == "retired_local" && service.admission.Ready() {
		return status, nil
	}
	if len(status.Dependencies) != 0 || status.MoreDependencies {
		return status, ErrConflict
	}
	if _, err := service.authority.Pending(ctx); !errors.Is(err, ErrNotFound) {
		if err == nil {
			err = ErrConflict
		}
		return status, err
	}
	if err := service.pause(ctx); err != nil {
		return status, err
	}
	status, retireErr := service.retirement.RetireVersion(ctx, actor, resource, version)
	// Rebuild current clients even after a rejected/ambiguous commit. The
	// authority must have proven no current or historical dependency before
	// retirement; no fallback to a different credential version is permitted.
	if err := service.runtime.RestoreCurrent(ctx); err != nil {
		return status, errors.Join(retireErr, err)
	}
	if _, err := service.authority.Pending(ctx); !errors.Is(err, ErrNotFound) {
		if err == nil {
			err = ErrConflict
		}
		return status, errors.Join(retireErr, err)
	}
	if err := service.admission.Resume(); err != nil {
		return status, errors.Join(retireErr, err)
	}
	return status, retireErr
}
