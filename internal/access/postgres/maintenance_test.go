package postgres

import (
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

type auditRetentionDatabase struct {
	admin       *pgxpool.Pool
	runtime     *pgxpool.Pool
	maintenance *pgxpool.Pool
}

type auditRetentionSeed struct {
	id       string
	metadata string
	occurred time.Time
}

func newAuditRetentionDatabase(t *testing.T) auditRetentionDatabase {
	t.Helper()
	h := postgrestest.Start(t)
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Login: true, Password: "runtime-retention-secret"})
	maintenanceRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Login: true, Password: "maintenance-retention-secret"})
	database := h.NewDatabase(t, "audit_retention_roles")
	admin, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(t.Context(), SchemaSQL()); err != nil {
		t.Fatalf("apply access schema: %v", err)
	}
	runtime, err := pgxpool.New(t.Context(), database.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	maintenance, err := pgxpool.New(t.Context(), database.URL(maintenanceRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(maintenance.Close)
	return auditRetentionDatabase{admin: admin, runtime: runtime, maintenance: maintenance}
}

func seedAuditRetentionRows(t *testing.T, db auditRetentionDatabase, rows ...auditRetentionSeed) {
	t.Helper()
	tx, err := db.admin.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin audit seed transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err := tx.Exec(t.Context(), `ALTER TABLE audit.audit_event DISABLE TRIGGER audit_event_immutable`); err != nil {
		t.Fatalf("disable audit seed trigger: %v", err)
	}
	for _, row := range rows {
		if _, err := tx.Exec(t.Context(), `INSERT INTO audit.audit_event (audit_id, source, operation, action, metadata, occurred_at) VALUES ($1::uuid, 'retention-test', 'retention', 'retention.seed', $2::jsonb, $3::timestamptz)`, row.id, row.metadata, row.occurred); err != nil {
			t.Fatalf("seed audit row %s: %v", row.id, err)
		}
	}
	if _, err := tx.Exec(t.Context(), `ALTER TABLE audit.audit_event ENABLE TRIGGER audit_event_immutable`); err != nil {
		t.Fatalf("enable audit seed trigger: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("commit audit seed transaction: %v", err)
	}
}

func TestPostgreSQL18AuditRetentionRoleBoundaryAndEvidence(t *testing.T) {
	db := newAuditRetentionDatabase(t)
	ctx := t.Context()
	var runtimeExecute, runtimeDelete, maintenanceExecute, maintenanceDelete bool
	if err := db.runtime.QueryRow(ctx, `SELECT has_function_privilege(current_user, 'audit.prune_audit_events(text, timestamptz, integer)', 'EXECUTE'), has_table_privilege(current_user, 'audit.audit_event', 'DELETE')`).Scan(&runtimeExecute, &runtimeDelete); err != nil {
		t.Fatal(err)
	}
	if runtimeExecute || runtimeDelete {
		t.Fatalf("runtime retention privileges execute=%t delete=%t", runtimeExecute, runtimeDelete)
	}
	if err := db.maintenance.QueryRow(ctx, `SELECT has_function_privilege(current_user, 'audit.prune_audit_events(text, timestamptz, integer)', 'EXECUTE'), has_table_privilege(current_user, 'audit.audit_event', 'DELETE')`).Scan(&maintenanceExecute, &maintenanceDelete); err != nil {
		t.Fatal(err)
	}
	if !maintenanceExecute || maintenanceDelete {
		t.Fatalf("maintenance retention privileges execute=%t delete=%t", maintenanceExecute, maintenanceDelete)
	}
	if _, err := db.runtime.Exec(ctx, `SELECT audit.prune_audit_events('standard', clock_timestamp(), 1)`); err == nil {
		t.Fatal("runtime audit retention unexpectedly succeeded")
	}
	if _, err := db.runtime.Exec(ctx, `DELETE FROM audit.audit_event`); err == nil {
		t.Fatal("runtime audit DELETE unexpectedly succeeded")
	}
	if _, err := db.runtime.Exec(ctx, `SET audit.maintenance = 'on'`); err != nil {
		t.Fatalf("runtime forged maintenance marker: %v", err)
	}
	if _, err := db.runtime.Exec(ctx, `DELETE FROM audit.audit_event`); err == nil {
		t.Fatal("runtime DELETE succeeded with forged maintenance marker")
	}
	if _, err := db.maintenance.Exec(ctx, `DELETE FROM audit.audit_event`); err == nil {
		t.Fatal("maintenance direct audit DELETE unexpectedly succeeded")
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	old := now.Add(-48 * time.Hour)
	cutoff := now.Add(-24 * time.Hour)
	seedAuditRetentionRows(t, db,
		auditRetentionSeed{id: "60000000-0000-7000-8000-000000000001", metadata: `{"retention":"short"}`, occurred: old},
		auditRetentionSeed{id: "60000000-0000-7000-8000-000000000002", metadata: `{}`, occurred: old},
		auditRetentionSeed{id: "60000000-0000-7000-8000-000000000003", metadata: `{"retention":"security"}`, occurred: old},
		auditRetentionSeed{id: "60000000-0000-7000-8000-000000000004", metadata: `{"retention":"unknown"}`, occurred: old},
		auditRetentionSeed{id: "60000000-0000-7000-8000-000000000005", metadata: `{"retention":"standard"}`, occurred: old},
	)
	adminConn, err := db.admin.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire administrator connection: %v", err)
	}
	if _, err := adminConn.Exec(ctx, `SET audit.maintenance = 'on'`); err != nil {
		adminConn.Release()
		t.Fatalf("administrator forged maintenance marker: %v", err)
	}
	if _, err := adminConn.Exec(ctx, `DELETE FROM audit.audit_event`); err == nil {
		adminConn.Release()
		t.Fatal("administrator DELETE succeeded with forged maintenance marker")
	}
	adminConn.Release()
	maintenance := NewMaintenance(db.maintenance)
	short, err := maintenance.Prune(ctx, RetentionShort, cutoff, 1)
	if err != nil {
		t.Fatalf("prune short audit events: %v", err)
	}
	if short.RetentionClass != RetentionShort || short.RemovedCount != 1 || short.RequestedLimit != 1 || !short.RequestedCutoff.Equal(cutoff) || short.Cutoff.After(now) {
		t.Fatalf("short retention result = %#v", short)
	}
	standard, err := maintenance.Prune(ctx, RetentionStandard, cutoff, 1000)
	if err != nil {
		t.Fatalf("prune standard audit events: %v", err)
	}
	if standard.RemovedCount != 2 {
		t.Fatalf("standard retention removed %d rows, want 2 (legacy plus standard)", standard.RemovedCount)
	}
	security, err := maintenance.Prune(ctx, RetentionSecurity, cutoff, 1000)
	if err != nil {
		t.Fatalf("prune security audit events: %v", err)
	}
	if security.RemovedCount != 1 {
		t.Fatalf("security retention removed %d rows, want 1", security.RemovedCount)
	}
	var unknownCount int
	if err := db.admin.QueryRow(ctx, `SELECT count(*) FROM audit.audit_event WHERE audit_id = '60000000-0000-7000-8000-000000000004'`).Scan(&unknownCount); err != nil {
		t.Fatal(err)
	}
	if unknownCount != 1 {
		t.Fatal("unknown retention category was removed")
	}
	var floorCount int
	if err := db.admin.QueryRow(ctx, `SELECT count(*) FROM audit.audit_retention_floor WHERE retention_class IN ('short','standard','security')`).Scan(&floorCount); err != nil {
		t.Fatal(err)
	}
	if floorCount != 3 {
		t.Fatalf("retention floor rows = %d, want 3", floorCount)
	}
}

func TestAuditRetentionRawInsertUsesDatabaseTime(t *testing.T) {
	db := newAuditRetentionDatabase(t)
	ctx := t.Context()
	const id = "60500000-0000-7000-8000-000000000001"
	old := time.Now().UTC().Add(-48 * time.Hour)
	started := time.Now().UTC()
	if _, err := db.runtime.Exec(ctx, `INSERT INTO audit.audit_event (audit_id, source, operation, action, metadata, occurred_at) VALUES ($1::uuid, 'retention-test', 'retention', 'retention.raw_insert', '{}'::jsonb, $2::timestamptz)`, id, old); err != nil {
		t.Fatalf("raw runtime audit insert: %v", err)
	}
	var occurred time.Time
	if err := db.admin.QueryRow(ctx, `SELECT occurred_at FROM audit.audit_event WHERE audit_id = $1::uuid`, id).Scan(&occurred); err != nil {
		t.Fatal(err)
	}
	if occurred.Before(started.Add(-time.Minute)) || occurred.After(time.Now().UTC().Add(time.Minute)) {
		t.Fatalf("raw audit insert occurred_at = %s, want database time near %s", occurred, started)
	}
	if !occurred.After(old.Add(time.Hour)) {
		t.Fatalf("raw audit insert retained caller-supplied old timestamp %s", occurred)
	}
}

func TestAuditRetentionRejectsUnboundedOrInvalidRequests(t *testing.T) {
	db := newAuditRetentionDatabase(t)
	maintenance := NewMaintenance(db.maintenance)
	now := time.Now().UTC()
	for _, test := range []struct {
		name  string
		class RetentionClass
		cut   time.Time
		limit int
	}{
		{"class", RetentionClass("forever"), now, 1},
		{"cutoff", RetentionStandard, time.Time{}, 1},
		{"zero limit", RetentionStandard, now, 0},
		{"over limit", RetentionStandard, now, 1001},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := maintenance.Prune(t.Context(), test.class, test.cut, test.limit); err == nil {
				t.Fatal("invalid retention request unexpectedly succeeded")
			}
		})
	}
	if _, err := maintenance.Prune(t.Context(), RetentionStandard, now, 1); err != nil {
		t.Fatalf("valid empty retention request: %v", err)
	}
}

func TestAuditRetentionFloorTracksBoundedBacklog(t *testing.T) {
	db := newAuditRetentionDatabase(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	cutoff := now.Add(-30 * time.Minute)
	rows := []struct {
		id       string
		occurred time.Time
	}{
		{"61000000-0000-7000-8000-000000000001", now.Add(-3 * time.Hour)},
		{"61000000-0000-7000-8000-000000000002", now.Add(-2 * time.Hour)},
		{"61000000-0000-7000-8000-000000000003", now.Add(-time.Hour)},
	}
	seeds := make([]auditRetentionSeed, 0, len(rows))
	for _, row := range rows {
		seeds = append(seeds, auditRetentionSeed{id: row.id, occurred: row.occurred, metadata: `{"retention":"standard"}`})
	}
	seedAuditRetentionRows(t, db, seeds...)
	maintenance := NewMaintenance(db.maintenance)
	first, err := maintenance.Prune(ctx, RetentionStandard, cutoff, 1)
	if err != nil {
		t.Fatalf("first bounded prune: %v", err)
	}
	if first.RemovedCount != 1 || !first.RetainedFloor.Equal(rows[1].occurred) || !first.RetainedFloor.Before(cutoff) {
		t.Fatalf("first bounded prune result = %#v", first)
	}
	second, err := maintenance.Prune(ctx, RetentionStandard, cutoff, 1)
	if err != nil {
		t.Fatalf("second bounded prune: %v", err)
	}
	if second.RemovedCount != 1 || !second.RetainedFloor.Equal(rows[2].occurred) || !second.RetainedFloor.Before(cutoff) {
		t.Fatalf("second bounded prune result = %#v", second)
	}
	final, err := maintenance.Prune(ctx, RetentionStandard, cutoff, 1000)
	if err != nil {
		t.Fatalf("final bounded prune: %v", err)
	}
	if final.RemovedCount != 1 || !final.RetainedFloor.Equal(final.Cutoff) || !final.RetainedFloor.Equal(cutoff) {
		t.Fatalf("final bounded prune result = %#v", final)
	}
}

func TestAuditRetentionPruneTxCallerOwnsRollback(t *testing.T) {
	db := newAuditRetentionDatabase(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	cutoff := now.Add(-time.Hour)
	const id = "62000000-0000-7000-8000-000000000001"
	seedAuditRetentionRows(t, db, auditRetentionSeed{id: id, metadata: `{"retention":"short"}`, occurred: now.Add(-2 * time.Hour)})
	tx, err := db.maintenance.Begin(ctx)
	if err != nil {
		t.Fatalf("begin maintenance transaction: %v", err)
	}
	result, err := NewMaintenance(db.maintenance).PruneTx(ctx, tx, RetentionShort, cutoff, 1)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("prune in caller-owned transaction: %v", err)
	}
	if result.RemovedCount != 1 {
		_ = tx.Rollback(ctx)
		t.Fatalf("prune in caller-owned transaction removed %d rows, want 1", result.RemovedCount)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback caller-owned retention transaction: %v", err)
	}
	var count int
	if err := db.admin.QueryRow(ctx, `SELECT count(*) FROM audit.audit_event WHERE audit_id = $1::uuid`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rolled-back prune left %d rows, want 1", count)
	}
}
