// Package postgres is the clean-slate PostgreSQL authority for refresh
// scheduling and execution.  It intentionally does not implement a second
// queue: executable work is recorded in the platform jobs authority and this
// package only stores the refresh identity and evidence linked to that job.
package postgres

import (
	"context"
	"embed"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is the native pgx surface shared by pools, connections and
// transactions.  Keeping this interface local means a caller can compose a
// refresh mutation, durable event, audit row and jobs enqueue in one pgx
// transaction.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Tx is the caller-owned native transaction surface. Requiring commit and
// rollback keeps pools/connections out of *Tx methods and preserves one atomic
// boundary across refresh and jobs mutations.
type Tx interface {
	DBTX
	Commit(context.Context) error
	Rollback(context.Context) error
}

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

//go:embed schema.sql
var schemaFS embed.FS

var schemaSQL = func() string {
	b, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		panic(err)
	}
	return string(b)
}()

// SchemaSQL returns the complete forward refresh capability schema.  It has
// no transaction control; migration and tests choose the commit boundary.
func SchemaSQL() string { return schemaSQL }

// ApplySchema applies the schema on a caller-owned transaction.
func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return errors.New("refresh schema transaction is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := tx.Exec(ctx, schemaSQL)
	return err
}
