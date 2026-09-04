package postgres

import (
	"context"
	"embed"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is implemented by pgx connections, transactions and pools.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// MaintenanceDBTX is the native PostgreSQL surface for the separately
// authenticated managed-data retention connection. Runtime repositories do
// not retain this destructive capability.
type MaintenanceDBTX interface {
	DBTX
}

// Tx is the strict native transaction surface accepted by managed-data
// mutation side-effect ports. Commit and rollback are included in the shape
// so a pool or connection cannot be passed where one atomic boundary is
// required; adapters never invoke either method themselves.
type Tx = pgx.Tx

//go:embed schema.sql
var schemaFiles embed.FS

var schemaSQL = func() string {
	b, _ := schemaFiles.ReadFile("schema.sql")
	return string(b)
}()

// SchemaSQL returns the capability-owned clean-slate PostgreSQL DDL.
func SchemaSQL() string { return schemaSQL }

// ApplySchema executes DDL using a caller-owned transaction. It never begins,
// commits, or rolls back a transaction itself.
func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return errors.New("managed-data schema transaction is required")
	}
	// sqlc-exception:schema-ddl. schema.sql owns capability DDL, guards,
	// functions, and grants; migration callers retain transaction ownership.
	_, err := tx.Exec(ctx, schemaSQL)
	return err
}
