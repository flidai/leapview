package module

import (
	"context"
	"errors"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// ErrIdentityRepositoryUnavailable indicates that identity persistence was
// not wired into the composition root.
var ErrIdentityRepositoryUnavailable = errors.New("project identity repository is unavailable")

// IdentityRepository is the module-facing persistence capability required to
// install the minimum durable identity for a claimed project.
type IdentityRepository interface {
	EnsureIdentity(context.Context, projectgraph.ResourceID) error
}

// EnsureIdentity installs the minimum durable project identity required by
// control-plane projections without coupling module code to a database
// driver. The repository is injected by the composition root; PostgreSQL is
// the production authority.
func EnsureIdentity(ctx context.Context, repository IdentityRepository, id projectgraph.ResourceID) error {
	if repository == nil {
		return ErrIdentityRepositoryUnavailable
	}
	return repository.EnsureIdentity(ctx, id)
}
