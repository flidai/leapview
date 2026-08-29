// Package migrations assembles the clean-slate PostgreSQL control-plane
// baseline. The foundation is intentionally tiny; capability packages own
// their table/function/trigger DDL and expose SchemaSQL for composition.
package migrations

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// BaselineRevision is the first clean-slate control-plane schema revision.
const BaselineRevision int64 = 1

// BaselineMigrationID is the immutable identifier recorded in
// platform.schema_revision.
const BaselineMigrationID = "001_control_plane"

// AdvisoryLockKey serializes all baseline attempts in a transaction-scoped
// PostgreSQL advisory lock. It is deliberately stable across process builds.
const AdvisoryLockKey int64 = 0x4c565f7067730001

//go:embed 001_control_plane.sql
var baselineSQL string

// Component is a read-only snapshot of one assembled capability. It is useful
// to diagnostics and tests without exposing the mutable internal registry.
type Component struct {
	Name string
	SQL  string
}

// Plan is the product-supplied ordered capability baseline. The generic
// migration runner deliberately does not import product capabilities; the
// application composition layer owns this list and its role policy.
type Plan struct {
	Components    []Component
	RolePolicySQL string
}

// BaselineSQL returns the foundation SQL. Product composition supplies ordered
// capability SQL through Plan when calling Apply.
func BaselineSQL() string { return baselineSQL }

// Checksum is the SHA-256 digest recorded with the schema revision.
// Length-prefix framing makes component names and bytes unambiguous while
// keeping the digest independent of Go formatting or map iteration order.
func (p Plan) Checksum() string {
	h := sha256.New()
	writeChecksumPart(h, "foundation", baselineSQL)
	for _, component := range p.Components {
		writeChecksumPart(h, component.Name, component.SQL)
	}
	writeChecksumPart(h, "baseline.role_policy", p.RolePolicySQL)
	return hex.EncodeToString(h.Sum(nil))
}

func (p Plan) validate() error {
	if len(p.Components) == 0 || strings.TrimSpace(p.RolePolicySQL) == "" {
		return errors.New("PostgreSQL migration plan is incomplete")
	}
	seen := make(map[string]struct{}, len(p.Components))
	for _, component := range p.Components {
		if strings.TrimSpace(component.Name) == "" || strings.TrimSpace(component.SQL) == "" {
			return errors.New("PostgreSQL migration component is incomplete")
		}
		if _, exists := seen[component.Name]; exists {
			return fmt.Errorf("duplicate PostgreSQL migration component %q", component.Name)
		}
		seen[component.Name] = struct{}{}
	}
	return nil
}

func writeChecksumPart(w io.Writer, name, sql string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(name)))
	_, _ = w.Write(length[:])
	_, _ = io.WriteString(w, name)
	binary.BigEndian.PutUint64(length[:], uint64(len(sql)))
	_, _ = w.Write(length[:])
	_, _ = io.WriteString(w, sql)
}

// Tx is the transaction boundary required by Apply. pgx.Tx and pgxpool.Tx
// both satisfy it; keeping the boundary here avoids opening a second
// connection or introducing repository policy into the schema package.
type Tx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Apply executes foundation and every ordered capability on a caller-owned
// transaction. The advisory lock, DDL, revision record, and verification all
// share that transaction; callers decide commit/rollback. Existing matching
// revisions are idempotent, while any checksum or migration-ID drift fails
// closed before capability SQL is replayed.
func Apply(ctx context.Context, tx Tx, plan Plan) error {
	if tx == nil {
		return errors.New("postgres migration transaction is nil")
	}
	if err := plan.validate(); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1::bigint)`, AdvisoryLockKey); err != nil {
		return fmt.Errorf("acquire PostgreSQL migration advisory lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE leapview_control_owner`); err != nil {
		return fmt.Errorf("assume PostgreSQL migration owner role: %w", err)
	}
	if _, err := tx.Exec(ctx, baselineSQL); err != nil {
		return fmt.Errorf("apply PostgreSQL migration foundation: %w", err)
	}

	checksum := plan.Checksum()
	var recordedRevision int64
	var recordedID, recordedChecksum string
	err := tx.QueryRow(ctx, `
		SELECT revision, migration_id, checksum
		FROM platform.schema_revision WHERE revision = $1`, BaselineRevision).
		Scan(&recordedRevision, &recordedID, &recordedChecksum)
	if err == nil {
		if recordedRevision != BaselineRevision || recordedID != BaselineMigrationID || recordedChecksum != checksum {
			return fmt.Errorf("PostgreSQL schema revision mismatch: got revision=%d migration=%q checksum=%q", recordedRevision, recordedID, recordedChecksum)
		}
		// Reapply the deny-by-default role policy even when DDL is already
		// present. This repairs accidental ACL widening without replaying
		// capability triggers that are not all CREATE OR REPLACE-safe.
		if _, err := tx.Exec(ctx, plan.RolePolicySQL); err != nil {
			return fmt.Errorf("reapply PostgreSQL migration role policy: %w", err)
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("inspect PostgreSQL schema revision: %w", err)
	}

	for _, component := range plan.Components {
		if _, err := tx.Exec(ctx, component.SQL); err != nil {
			return fmt.Errorf("apply PostgreSQL capability %s: %w", component.Name, err)
		}
	}
	if _, err := tx.Exec(ctx, plan.RolePolicySQL); err != nil {
		return fmt.Errorf("apply PostgreSQL migration role policy: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO platform.schema_revision (revision, migration_id, checksum)
		VALUES ($1, $2, $3)`, BaselineRevision, BaselineMigrationID, checksum); err != nil {
		return fmt.Errorf("record PostgreSQL schema revision: %w", err)
	}

	if err := tx.QueryRow(ctx, `
		SELECT revision, migration_id, checksum
		FROM platform.schema_revision WHERE revision = $1`, BaselineRevision).
		Scan(&recordedRevision, &recordedID, &recordedChecksum); err != nil {
		return fmt.Errorf("verify PostgreSQL schema revision: %w", err)
	}
	if recordedRevision != BaselineRevision || recordedID != BaselineMigrationID || recordedChecksum != checksum {
		return fmt.Errorf("PostgreSQL schema revision mismatch: got revision=%d migration=%q checksum=%q", recordedRevision, recordedID, recordedChecksum)
	}
	return nil
}
