package migrations

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type recordingTx struct {
	sqls             []string
	revision         int64
	migrationID      string
	recordedChecksum string
	queryErr         error
}

func (r *recordingTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	r.sqls = append(r.sqls, sql)
	return pgconn.CommandTag{}, nil
}

func (r *recordingTx) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return recordingRow{revision: r.revision, migrationID: r.migrationID, checksum: r.recordedChecksum, err: r.queryErr}
}

type recordingRow struct {
	revision    int64
	migrationID string
	checksum    string
	err         error
}

func (r recordingRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 3 {
		return errors.New("unexpected destination count")
	}
	*dest[0].(*int64) = r.revision
	*dest[1].(*string) = r.migrationID
	*dest[2].(*string) = r.checksum
	return nil
}

func TestBaselineMetadata(t *testing.T) {
	if BaselineRevision != 1 || BaselineMigrationID != "001_control_plane" {
		t.Fatalf("baseline metadata = revision %d, id %q", BaselineRevision, BaselineMigrationID)
	}
	sql := BaselineSQL()
	for _, schema := range []string{"access", "delivery", "refresh", "event", "audit", "lineage", "cache", "agent"} {
		if !strings.Contains(sql, "CREATE SCHEMA IF NOT EXISTS "+schema) {
			t.Errorf("baseline does not create %s capability schema", schema)
		}
	}
	for _, role := range []string{"leapview_control_owner", "leapview_control_migrator", "leapview_control_runtime", "leapview_control_readonly"} {
		if !strings.Contains(sql, role) {
			t.Errorf("baseline does not declare/grant %s", role)
		}
	}
	for _, marker := range []string{
		"platform.schema_revision",
		"platform.operation",
		"event.event_aggregate",
		"event.event_retention_root",
		"delivery.delivery_snapshot_retention",
		"octet_length(payload::text) <= 65536",
		"octet_length(properties::text) <= 16384",
		"octet_length(metadata::text) <= 16384",
		"audit.reject_audit_mutation",
		"REVOKE UPDATE, DELETE ON audit.audit_event",
	} {
		if !strings.Contains(sql, marker) {
			t.Errorf("baseline missing required contract marker %q", marker)
		}
	}
	if strings.Contains(sql, "-- +goose") {
		t.Fatal("PostgreSQL baseline must use its own migration mechanism")
	}
	if strings.Contains(sql, "repeat('0', 64)") {
		t.Fatal("baseline must not seed a fake schema checksum")
	}
}

func TestApplyUsesCallerOwnedTransaction(t *testing.T) {
	recorder := &recordingTx{revision: BaselineRevision, migrationID: BaselineMigrationID, recordedChecksum: BaselineChecksum()}
	if err := Apply(context.Background(), recorder); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(recorder.sqls) != 2 || recorder.sqls[0] != BaselineSQL() {
		t.Fatal("Apply() did not execute the authored baseline SQL")
	}
	if err := Apply(context.Background(), nil); err == nil {
		t.Fatal("Apply(nil) unexpectedly succeeded")
	}
}

func TestApplyRejectsRevisionChecksumMismatch(t *testing.T) {
	recorder := &recordingTx{revision: BaselineRevision, migrationID: BaselineMigrationID, recordedChecksum: strings.Repeat("f", 64)}
	if err := Apply(context.Background(), recorder); err == nil {
		t.Fatal("Apply() accepted a mismatched recorded checksum")
	}
}
