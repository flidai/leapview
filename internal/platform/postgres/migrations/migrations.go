// Package migrations contains the authored PostgreSQL control-plane schema.
//
// PostgreSQL migrations are intentionally separate from the historical SQLite
// migrations.  Capability repositories can apply the baseline in a caller-owned
// transaction; this package does not open a connection or select a database.
package migrations

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// BaselineRevision is the first clean-slate control-plane schema revision.
const BaselineRevision int64 = 1

// BaselineMigrationID is the immutable identifier recorded in
// platform.schema_revision.
const BaselineMigrationID = "001_control_plane"

//go:embed 001_control_plane.sql
var baselineSQL string

// BaselineSQL returns the exact authored baseline migration.  Callers should
// execute it as a migration authority, inside a transaction where the driver
// supports transactional DDL.
func BaselineSQL() string { return baselineSQL }

// BaselineChecksum is the SHA-256 digest recorded with the schema revision.
func BaselineChecksum() string {
	sum := sha256.Sum256([]byte(baselineSQL))
	return hex.EncodeToString(sum[:])
}

// Tx is the transaction boundary required by Apply.  pgx.Tx and pgxpool.Tx
// both satisfy it; keeping the boundary here avoids opening a second
// connection or introducing repository policy into the schema package.
type Tx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Apply executes the clean baseline on a caller-owned transaction and records
// the exact SHA-256 of the authored file in platform.schema_revision.  The SQL
// contains no BEGIN/COMMIT so a failed migration can be rolled back by the
// caller.  A pre-existing revision with a different checksum is rejected.
func Apply(ctx context.Context, tx Tx) error {
	if tx == nil {
		return errors.New("postgres migration transaction is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := tx.Exec(ctx, baselineSQL)
	if err != nil {
		return err
	}
	checksum := BaselineChecksum()
	if _, err := tx.Exec(ctx, `
		INSERT INTO platform.schema_revision (revision, migration_id, checksum)
		VALUES ($1, $2, $3)
		ON CONFLICT (revision) DO NOTHING`, BaselineRevision, BaselineMigrationID, checksum); err != nil {
		return fmt.Errorf("record PostgreSQL schema revision: %w", err)
	}
	var revision int64
	var migrationID, recordedChecksum string
	if err := tx.QueryRow(ctx, `
		SELECT revision, migration_id, checksum
		FROM platform.schema_revision WHERE revision = $1`, BaselineRevision).
		Scan(&revision, &migrationID, &recordedChecksum); err != nil {
		return fmt.Errorf("verify PostgreSQL schema revision: %w", err)
	}
	if revision != BaselineRevision || migrationID != BaselineMigrationID || recordedChecksum != checksum {
		return fmt.Errorf("PostgreSQL schema revision mismatch: got revision=%d migration=%q checksum=%q", revision, migrationID, recordedChecksum)
	}
	return nil
}
