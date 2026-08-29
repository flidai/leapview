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

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	cachepostgres "github.com/flidai/leapview/internal/analytics/cache/postgres"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	queryauditpostgres "github.com/flidai/leapview/internal/analytics/queryaudit/postgres"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	lineagepostgres "github.com/flidai/leapview/internal/lineage/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	cursorsigningpostgres "github.com/flidai/leapview/internal/platform/http/cursorsigning/postgres"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	operationpostgres "github.com/flidai/leapview/internal/platform/operation/postgres"
	projectpostgres "github.com/flidai/leapview/internal/project/postgres"
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

// componentSpec is kept private so callers cannot mutate the authoritative
// ordered list. Name and bytes both participate in the baseline checksum.
type componentSpec struct {
	name string
	sql  func() string
}

// baselineComponents is an explicit dependency order. Access owns the shared
// audit table shape used by deployment; deployment installs its delivery
// authority over that table and its event ledger; events then replace the
// event-log trigger with the durable fan-out implementation.
var baselineComponents = [...]componentSpec{
	{name: "platform.operation", sql: operationpostgres.SchemaSQL},
	{name: "platform.cursor_signing", sql: cursorsigningpostgres.SchemaSQL},
	{name: "project", sql: projectpostgres.SchemaSQL},
	{name: "access", sql: accesspostgres.SchemaSQL},
	{name: "deployment", sql: deploymentpostgres.SchemaSQL},
	{name: "event", sql: eventspostgres.SchemaSQL},
	{name: "ducklake", sql: ducklakepostgres.SchemaSQL},
	{name: "jobs", sql: jobspostgres.SchemaSQL},
	{name: "lineage", sql: lineagepostgres.SchemaSQL},
	{name: "cache", sql: cachepostgres.SchemaSQL},
	{name: "queryaudit", sql: queryauditpostgres.SchemaSQL},
}

// Component is a read-only snapshot of one assembled capability. It is useful
// to diagnostics and tests without exposing the mutable internal registry.
type Component struct {
	Name string
	SQL  string
}

// BaselineComponents returns the deterministic capability list and exact SQL
// bytes used by Apply. The returned slice can be modified by the caller.
func BaselineComponents() []Component {
	components := make([]Component, 0, len(baselineComponents))
	for _, component := range baselineComponents {
		components = append(components, Component{Name: component.name, SQL: component.sql()})
	}
	return components
}

// BaselineSQL returns the foundation SQL. Capability SQL is available through
// BaselineComponents and is applied in order by Apply.
func BaselineSQL() string { return baselineSQL }

// BaselineChecksum is the SHA-256 digest recorded with the schema revision.
// Length-prefix framing makes component names and bytes unambiguous while
// keeping the digest independent of Go formatting or map iteration order.
func BaselineChecksum() string {
	h := sha256.New()
	writeChecksumPart(h, "foundation", baselineSQL)
	for _, component := range baselineComponents {
		writeChecksumPart(h, component.name, component.sql())
	}
	writeChecksumPart(h, "baseline.role_policy", rolePolicySQL)
	return hex.EncodeToString(h.Sum(nil))
}

// rolePolicySQL fills the small privilege gaps in capability schemas that are
// intentionally usable in isolation. It never grants readonly access to the
// cursor-key base table; only the metadata view is exposed.
const rolePolicySQL = `
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA access, delivery, event, audit, ducklake, jobs, lineage, cache TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA delivery, ducklake, jobs, cache TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA event TO leapview_control_runtime;
        GRANT SELECT, INSERT ON ALL TABLES IN SCHEMA audit TO leapview_control_runtime;
        GRANT SELECT ON ALL TABLES IN SCHEMA lineage TO leapview_control_runtime;
        GRANT INSERT ON lineage.graphs, lineage.nodes, lineage.edges, lineage.bindings TO leapview_control_runtime;
        REVOKE INSERT, UPDATE, DELETE ON lineage.revisions FROM leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION lineage.publish_revision(text, text, text) TO leapview_control_runtime;
        REVOKE UPDATE, DELETE ON event.event_log FROM leapview_control_runtime;
        REVOKE UPDATE, DELETE ON audit.audit_event FROM leapview_control_runtime;
        REVOKE UPDATE, DELETE ON ducklake.catalog_identity, ducklake.generation_binding FROM leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION event.prune_event_log(timestamptz, integer) TO leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION jobs.prune(timestamptz, integer) TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA access, delivery, event, audit, ducklake, jobs, lineage, cache TO leapview_control_readonly;
        GRANT SELECT ON ALL TABLES IN SCHEMA access, delivery, event, audit, ducklake, lineage, cache TO leapview_control_readonly;
        -- Jobs payload/evidence is runtime-only; readonly receives the
        -- bounded health projection instead of the queue's raw JSON.
        GRANT SELECT ON jobs.job_observability TO leapview_control_readonly;
        REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON jobs.job, jobs.attempt, jobs.event_sequence, jobs.event FROM leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON ALL TABLES IN SCHEMA access, delivery, event, audit, ducklake, lineage, cache FROM leapview_control_readonly;
        REVOKE SELECT ON access.session, access.local_credential, access.api_token, access.service_principal_secret, access.desktop_authorization_code, access.device_authorization, access.authoring_credential FROM leapview_control_readonly;
        GRANT USAGE ON SCHEMA platform TO leapview_control_readonly;
        GRANT SELECT ON platform.schema_revision, platform.operation, platform.api_cursor_signing_key_metadata TO leapview_control_readonly;
        REVOKE ALL ON platform.api_cursor_signing_keys FROM leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA project, access, delivery, event, audit, ducklake, jobs, lineage, cache TO leapview_control_backup;
        GRANT SELECT ON ALL TABLES IN SCHEMA project, access, delivery, event, audit, ducklake, jobs, lineage, cache TO leapview_control_backup;
        GRANT USAGE ON SCHEMA platform TO leapview_control_backup;
        GRANT SELECT ON platform.schema_revision, platform.operation, platform.api_cursor_signing_keys TO leapview_control_backup;
    END IF;
END
$$;`

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
func Apply(ctx context.Context, tx Tx) error {
	if tx == nil {
		return errors.New("postgres migration transaction is nil")
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

	checksum := BaselineChecksum()
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
		if _, err := tx.Exec(ctx, rolePolicySQL); err != nil {
			return fmt.Errorf("reapply PostgreSQL migration role policy: %w", err)
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("inspect PostgreSQL schema revision: %w", err)
	}

	for _, component := range baselineComponents {
		if _, err := tx.Exec(ctx, component.sql()); err != nil {
			return fmt.Errorf("apply PostgreSQL capability %s: %w", component.name, err)
		}
	}
	if _, err := tx.Exec(ctx, rolePolicySQL); err != nil {
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
