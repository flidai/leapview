package module

import (
	"context"
	"database/sql"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectsqlite "github.com/flidai/leapview/internal/project/sqlite"
)

// EnsureIdentity installs the minimum mutable project identity required by
// legacy control-plane tables without overwriting authored project metadata.
func EnsureIdentity(ctx context.Context, database *sql.DB, id projectgraph.ResourceID) error {
	return projectsqlite.NewRepository(database).EnsureIdentity(ctx, id)
}
