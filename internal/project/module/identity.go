package module

import (
	"context"
	"errors"

	project "github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// ErrIdentityRepositoryUnavailable indicates that identity persistence was
// not wired into the composition root.
var ErrIdentityRepositoryUnavailable = errors.New("project identity repository is unavailable")

// EnsureIdentity installs the minimum durable project identity required by
// control-plane projections without coupling module code to a database
// driver.  The repository is injected by the composition root; PostgreSQL is
// the production authority and SQLite can only be supplied by an isolated
// fixture implementing project.IdentityRepository.
func EnsureIdentity(ctx context.Context, repository project.IdentityRepository, id projectgraph.ResourceID) error {
	if repository == nil {
		return ErrIdentityRepositoryUnavailable
	}
	return repository.EnsureIdentity(ctx, id)
}
