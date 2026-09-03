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

	platformdb "github.com/flidai/leapview/internal/platform/postgres/internal/db"
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

// SET LOCAL ROLE is PostgreSQL utility syntax and cannot be prepared by
// sqlc's database-backed vet rule. Keep this one protocol-level statement
// explicit and narrowly marked while all parameterized migration operations
// remain generated below.
const setMigrationRoleSQL = `SET LOCAL ROLE leapview_control_owner`

//go:embed 001_control_plane.sql
var baselineSQL string

// Component is a read-only snapshot of one assembled capability. It is useful
// to diagnostics and tests without exposing the mutable internal registry.
type Component struct {
	Name string
	SQL  string
}

// Migration is one immutable forward-only control-plane revision. Capability
// packages own the SQL and application composition assigns its global
// revision and stable migration ID. Revision 1 remains the clean-slate
// baseline; forward migrations never alter its checksum.
type Migration struct {
	Revision    int64
	MigrationID string
	SQL         string
}

// Checksum is the immutable SHA-256 identity recorded for this migration.
func (m Migration) Checksum() string {
	h := sha256.New()
	writeChecksumPart(h, "migration.id", m.MigrationID)
	writeChecksumPart(h, "migration.sql", m.SQL)
	return hex.EncodeToString(h.Sum(nil))
}

// Plan is the product-supplied ordered capability baseline. The generic
// migration runner deliberately does not import product capabilities; the
// application composition layer owns this list and its role policy.
type Plan struct {
	Components    []Component
	Migrations    []Migration
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
	seenMigrationIDs := make(map[string]struct{}, len(p.Migrations))
	wantRevision := BaselineRevision + 1
	for _, migration := range p.Migrations {
		if migration.Revision != wantRevision {
			return fmt.Errorf("PostgreSQL migration revision %d is not the expected revision %d", migration.Revision, wantRevision)
		}
		if strings.TrimSpace(migration.MigrationID) == "" || strings.TrimSpace(migration.SQL) == "" {
			return fmt.Errorf("PostgreSQL migration revision %d is incomplete", migration.Revision)
		}
		if _, exists := seenMigrationIDs[migration.MigrationID]; exists {
			return fmt.Errorf("duplicate PostgreSQL migration ID %q", migration.MigrationID)
		}
		seenMigrationIDs[migration.MigrationID] = struct{}{}
		wantRevision++
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

// migrationDBTX adapts the intentionally narrow migration transaction seam
// to sqlc's pgx/v5 DBTX contract. Apply only uses Exec and QueryRow; Query is
// rejected explicitly so a future accidental streaming call cannot silently
// retain a transaction connection.
type migrationDBTX struct{ Tx }

func (m migrationDBTX) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("PostgreSQL migration transaction does not support Query")
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
	queries := platformdb.New(migrationDBTX{Tx: tx})
	if err := queries.AcquireMigrationLock(ctx, AdvisoryLockKey); err != nil {
		return fmt.Errorf("acquire PostgreSQL migration advisory lock: %w", err)
	}
	// sqlc-exception: analyzer-incompatible. PostgreSQL SET LOCAL ROLE cannot be prepared by sqlc vet.
	if _, err := tx.Exec(ctx, setMigrationRoleSQL); err != nil {
		return fmt.Errorf("assume PostgreSQL migration owner role: %w", err)
	}
	if _, err := tx.Exec(ctx, baselineSQL); err != nil {
		return fmt.Errorf("apply PostgreSQL migration foundation: %w", err)
	}

	checksum := plan.Checksum()
	recorded, err := queries.GetSchemaRevision(ctx, BaselineRevision)
	if err == nil {
		if recorded.Revision != BaselineRevision || recorded.MigrationID != BaselineMigrationID || recorded.Checksum != checksum {
			return fmt.Errorf("PostgreSQL schema revision mismatch: got revision=%d migration=%q checksum=%q", recorded.Revision, recorded.MigrationID, recorded.Checksum)
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("inspect PostgreSQL schema revision: %w", err)
	} else {
		for _, component := range plan.Components {
			if _, err := tx.Exec(ctx, component.SQL); err != nil {
				return fmt.Errorf("apply PostgreSQL capability %s: %w", component.Name, err)
			}
		}
		if err := queries.InsertSchemaRevision(ctx, platformdb.InsertSchemaRevisionParams{
			Revision: BaselineRevision, MigrationID: BaselineMigrationID, Checksum: checksum,
		}); err != nil {
			return fmt.Errorf("record PostgreSQL schema revision: %w", err)
		}

		recorded, err = queries.GetSchemaRevision(ctx, BaselineRevision)
		if err != nil {
			return fmt.Errorf("verify PostgreSQL schema revision: %w", err)
		}
		if recorded.Revision != BaselineRevision || recorded.MigrationID != BaselineMigrationID || recorded.Checksum != checksum {
			return fmt.Errorf("PostgreSQL schema revision mismatch: got revision=%d migration=%q checksum=%q", recorded.Revision, recorded.MigrationID, recorded.Checksum)
		}
	}

	for _, migration := range plan.Migrations {
		migrationChecksum := migration.Checksum()
		recorded, err = queries.GetSchemaRevision(ctx, migration.Revision)
		if err == nil {
			if recorded.Revision != migration.Revision || recorded.MigrationID != migration.MigrationID || recorded.Checksum != migrationChecksum {
				return fmt.Errorf("PostgreSQL schema revision mismatch: got revision=%d migration=%q checksum=%q", recorded.Revision, recorded.MigrationID, recorded.Checksum)
			}
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("inspect PostgreSQL schema revision %d: %w", migration.Revision, err)
		}
		if _, err := tx.Exec(ctx, migration.SQL); err != nil {
			return fmt.Errorf("apply PostgreSQL migration %s: %w", migration.MigrationID, err)
		}
		if err := queries.InsertSchemaRevision(ctx, platformdb.InsertSchemaRevisionParams{
			Revision: migration.Revision, MigrationID: migration.MigrationID, Checksum: migrationChecksum,
		}); err != nil {
			return fmt.Errorf("record PostgreSQL schema revision %d: %w", migration.Revision, err)
		}
		recorded, err = queries.GetSchemaRevision(ctx, migration.Revision)
		if err != nil {
			return fmt.Errorf("verify PostgreSQL schema revision %d: %w", migration.Revision, err)
		}
		if recorded.Revision != migration.Revision || recorded.MigrationID != migration.MigrationID || recorded.Checksum != migrationChecksum {
			return fmt.Errorf("PostgreSQL schema revision mismatch: got revision=%d migration=%q checksum=%q", recorded.Revision, recorded.MigrationID, recorded.Checksum)
		}
	}

	// Reapply the deny-by-default baseline role policy after every newly applied
	// capability migration and on idempotent replay. This repairs accidental
	// ACL widening; each capability migration remains responsible for grants on
	// its newly introduced objects.
	if _, err := tx.Exec(ctx, plan.RolePolicySQL); err != nil {
		return fmt.Errorf("apply PostgreSQL migration role policy: %w", err)
	}
	return nil
}
