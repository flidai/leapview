package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	auditActorID = "10000000-0000-0000-0000-000000000001"
	auditEventID = "20000000-0000-0000-0000-000000000001"
)

type auditDatabase struct {
	admin    *pgxpool.Pool
	runtime  *pgxpool.Pool
	readonly *pgxpool.Pool
}

func newAuditDatabase(t *testing.T) auditDatabase {
	t.Helper()
	h := postgrestest.Start(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator"})
	runtimeRole := h.EnsureRole(t, postgrestest.Role{
		Name: "leapview_control_runtime", Password: "leapview-conformance-secret", Login: true,
	})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly"})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup"})
	h.GrantRole(t, owner, migrator)
	database := h.NewDatabase(t, "")
	h.GrantDatabase(t, database.Name, migrator, "CONNECT", "CREATE")

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	conn, err := admin.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `SET ROLE leapview_control_migrator`); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		conn.Release()
		t.Fatal(err)
	}
	if err := ApplySchema(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		conn.Release()
		t.Fatalf("apply baseline: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	conn.Release()

	// Seed an actor UUID used by the immutable audit evidence.
	if _, err := admin.Exec(ctx, `
		INSERT INTO access.principal (id, principal_type, status)
		VALUES ($1::uuid, 'user', 'active')`, auditActorID); err != nil {
		t.Fatal(err)
	}
	runtime, err := pgxpool.New(ctx, database.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	return auditDatabase{admin: admin, runtime: runtime}
}

func auditIntent(id, action string) access.AuditIntent {
	return access.AuditIntent{
		EventID:           id,
		Source:            "access",
		Operation:         "principal.update",
		PrincipalID:       auditActorID,
		Action:            action,
		ResourceKind:      "principal",
		ResourceID:        auditActorID,
		Capability:        access.CapabilityResourceEdit,
		Outcome:           "success",
		RequestID:         "30000000-0000-0000-0000-000000000001",
		CorrelationID:     "40000000-0000-0000-0000-000000000001",
		AggregateKey:      "principal:" + auditActorID,
		AggregateSequence: 1,
		MetadataJSON:      `{"reason":"test"}`,
	}
}

func TestRecordAuditEventPostgreSQL18AtomicImmutableAndCanonical(t *testing.T) {
	db := newAuditDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	repo := New()

	t.Run("commit and rollback share source transaction", func(t *testing.T) {
		commitGroup := "50000000-0000-0000-0000-000000000001"
		tx, err := db.runtime.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO access.access_group (id, name, provider) VALUES ($1::uuid, 'atomic-commit', 'atomic-commit')`, commitGroup); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if _, err := repo.RecordAuditEvent(ctx, tx, auditIntent(auditEventID, "principal.updated")); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		var groups, events int
		if err := db.runtime.QueryRow(ctx, `SELECT count(*) FROM access.access_group WHERE id = $1::uuid`, commitGroup).Scan(&groups); err != nil {
			t.Fatal(err)
		}
		if err := db.runtime.QueryRow(ctx, `SELECT count(*) FROM audit.audit_event WHERE audit_id = $1::uuid`, auditEventID).Scan(&events); err != nil {
			t.Fatal(err)
		}
		if groups != 1 || events != 1 {
			t.Fatalf("committed source/audit rows = %d/%d, want 1/1", groups, events)
		}

		rollbackGroup := "50000000-0000-0000-0000-000000000002"
		rollbackEvent := "20000000-0000-0000-0000-000000000002"
		tx, err = db.runtime.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO access.access_group (id, name, provider) VALUES ($1::uuid, 'atomic-rollback', 'atomic-rollback')`, rollbackGroup); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if _, err := repo.RecordAuditEvent(ctx, tx, auditIntent(rollbackEvent, "principal.updated")); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if err := db.runtime.QueryRow(ctx, `SELECT count(*) FROM access.access_group WHERE id = $1::uuid`, rollbackGroup).Scan(&groups); err != nil {
			t.Fatal(err)
		}
		if err := db.runtime.QueryRow(ctx, `SELECT count(*) FROM audit.audit_event WHERE audit_id = $1::uuid`, rollbackEvent).Scan(&events); err != nil {
			t.Fatal(err)
		}
		if groups != 0 || events != 0 {
			t.Fatalf("rolled-back source/audit rows = %d/%d, want 0/0", groups, events)
		}
	})

	t.Run("actor action resource and correlation are persisted", func(t *testing.T) {
		var principal, action, kind, resource, request, correlation, metadata string
		if err := db.runtime.QueryRow(ctx, `
			SELECT principal_id::text, action, resource_kind, resource_id,
			       request_id::text, correlation_id::text, metadata::text
			FROM audit.audit_event WHERE audit_id = $1::uuid`, auditEventID).
			Scan(&principal, &action, &kind, &resource, &request, &correlation, &metadata); err != nil {
			t.Fatal(err)
		}
		if principal != auditActorID || action != "principal.updated" || kind != "principal" || resource != auditActorID ||
			request != "30000000-0000-0000-0000-000000000001" || correlation != "40000000-0000-0000-0000-000000000001" {
			t.Fatalf("audit identity = principal %q action %q resource %q/%q request %q correlation %q", principal, action, kind, resource, request, correlation)
		}
		var metadataObject map[string]any
		if err := json.Unmarshal([]byte(metadata), &metadataObject); err != nil {
			t.Fatal(err)
		}
		if metadataObject["reason"] != "test" {
			t.Fatalf("metadata was not canonicalized: %s", metadata)
		}
		got, err := repo.GetAuditEvent(ctx, db.runtime, auditEventID)
		if err != nil {
			t.Fatal(err)
		}
		if got.AuditID != auditEventID || got.PrincipalID != auditActorID || got.Action != "principal.updated" {
			t.Fatalf("bounded read = %#v", got)
		}
		list, err := repo.ListAuditEvents(ctx, db.runtime, maxAuditReadRows+1)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) > maxAuditReadRows {
			t.Fatalf("bounded export returned %d rows", len(list))
		}
		// Historical actor evidence survives the principal's monotonic
		// revocation; access identities are themselves append-only.
		if _, err := db.admin.Exec(ctx, `
			UPDATE access.principal
			SET status = 'disabled',
				disabled_at = clock_timestamp(),
				revoked_at = clock_timestamp(),
				updated_at = clock_timestamp()
			WHERE id = $1::uuid`, auditActorID); err != nil {
			t.Fatal(err)
		}
		var preserved string
		if err := db.runtime.QueryRow(ctx, `SELECT principal_id::text FROM audit.audit_event WHERE audit_id = $1::uuid`, auditEventID).Scan(&preserved); err != nil {
			t.Fatal(err)
		}
		if preserved != auditActorID {
			t.Fatalf("actor evidence changed after principal revocation: %q", preserved)
		}
	})

	t.Run("duplicate identity is idempotent and changed payload conflicts", func(t *testing.T) {
		tx, err := db.runtime.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		first, err := repo.RecordAuditEvent(ctx, tx, auditIntent(auditEventID, "principal.updated"))
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		tx, err = db.runtime.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		replay, err := repo.RecordAuditEvent(ctx, tx, auditIntent(auditEventID, "principal.updated"))
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if replay.AuditID != first.AuditID || replay.OccurredAt.IsZero() {
			t.Fatalf("replay = %#v", replay)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}

		changed := auditIntent(auditEventID, "principal.deleted")
		tx, err = db.runtime.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = repo.RecordAuditEvent(ctx, tx, changed)
		if !errors.Is(err, access.ErrAuditIntentConflict) {
			t.Fatalf("changed replay = %v, want conflict", err)
		}
		_ = tx.Rollback(ctx)
		var count int
		if err := db.runtime.QueryRow(ctx, `SELECT count(*) FROM audit.audit_event WHERE audit_id = $1::uuid`, auditEventID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("duplicate row count = %d, want 1", count)
		}
	})

	t.Run("invalid metadata and required audit failure are fail closed", func(t *testing.T) {
		for name, metadata := range map[string]string{
			"oversized": `{"value":"` + strings.Repeat("x", maxAuditMetadataBytes) + `"}`,
			"unsafe":    `{"password":"do-not-store"}`,
			"duplicate": `{"a":1,"a":2}`,
		} {
			intent := auditIntent("20000000-0000-0000-0000-0000000000"+fmt.Sprintf("%02d", len(name)), "principal.updated")
			intent.MetadataJSON = metadata
			tx, err := db.runtime.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO access.access_group (id, name, provider) VALUES (gen_random_uuid(), $1, $1)`, "invalid-"+name); err != nil {
				_ = tx.Rollback(ctx)
				t.Fatal(err)
			}
			if _, err := repo.RecordAuditEvent(ctx, tx, intent); err == nil {
				_ = tx.Rollback(ctx)
				t.Fatalf("accepted %s metadata", name)
			}
			if err := tx.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
		}

		// A database-level length rejection occurs after the source mutation;
		// the caller must roll back the exact transaction.
		intent := auditIntent("20000000-0000-0000-0000-000000000099", "principal.updated")
		intent.Action = strings.Repeat("x", 256)
		tx, err := db.runtime.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO access.access_group (id, name, provider) VALUES (gen_random_uuid(), 'required-audit-failure', 'required-audit-failure')`); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if _, err := repo.RecordAuditEvent(ctx, tx, intent); err == nil {
			_ = tx.Rollback(ctx)
			t.Fatal("database length rejection unexpectedly accepted")
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := db.runtime.QueryRow(ctx, `SELECT count(*) FROM access.access_group WHERE name = 'required-audit-failure'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatal("source mutation survived required audit failure")
		}
	})

	t.Run("runtime is insert-only and no outbox exists", func(t *testing.T) {
		if _, err := db.runtime.Exec(ctx, `UPDATE audit.audit_event SET action = 'tampered' WHERE audit_id = $1::uuid`, auditEventID); err == nil {
			t.Fatal("runtime UPDATE unexpectedly succeeded")
		}
		if _, err := db.runtime.Exec(ctx, `DELETE FROM audit.audit_event WHERE audit_id = $1::uuid`, auditEventID); err == nil {
			t.Fatal("runtime DELETE unexpectedly succeeded")
		}
		var exists bool
		if err := db.runtime.QueryRow(ctx, `SELECT to_regclass('audit.audit_outbox') IS NOT NULL`).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatal("same-database audit outbox unexpectedly exists")
		}
	})
}
