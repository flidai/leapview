package postgres

import (
	"context"
	_ "embed"
	"errors"
)

//go:embed schema.sql
var schemaSQL string

// SchemaSQL returns the capability-owned PostgreSQL DDL. Migration callers
// execute it through their existing caller-owned transaction.
func SchemaSQL() string { return schemaSQL }

// ApplySchema installs the clean-slate capability schema in a caller-owned
// transaction. It never commits or rolls back the supplied transaction.
func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return errors.New("agent PostgreSQL transaction is required")
	}
	_, err := tx.Exec(ctx, schemaSQL) // sqlc-exception: schema-ddl
	return err
}
