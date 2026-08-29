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
	switch len(dest) {
	case 3:
		*dest[0].(*int64) = r.revision
		*dest[1].(*string) = r.migrationID
		*dest[2].(*string) = r.checksum
	default:
		return errors.New("unexpected destination count")
	}
	return nil
}

func TestBaselineMetadata(t *testing.T) {
	if BaselineRevision != 1 || BaselineMigrationID != "001_control_plane" {
		t.Fatalf("baseline metadata = revision %d, id %q", BaselineRevision, BaselineMigrationID)
	}
	sql := BaselineSQL()
	for _, marker := range []string{"platform.schema_revision", "platform.reject_schema_revision_mutation", "leapview_control_owner", "leapview_control_backup"} {
		if !strings.Contains(sql, marker) {
			t.Errorf("foundation missing required contract marker %q", marker)
		}
	}
	if strings.Contains(sql, "CREATE TABLE IF NOT EXISTS platform.operation") || strings.Contains(sql, "CREATE SCHEMA IF NOT EXISTS access") {
		t.Fatal("foundation must not duplicate capability-owned DDL")
	}
	if strings.Contains(sql, "-- +goose") {
		t.Fatal("PostgreSQL baseline must use its own migration mechanism")
	}
	if strings.Contains(sql, "repeat('0', 64)") {
		t.Fatal("baseline must not seed a fake schema checksum")
	}
}

func testPlan() Plan {
	return Plan{
		Components:    []Component{{Name: "test.capability", SQL: "SELECT 1"}},
		RolePolicySQL: "SELECT 2",
	}
}

func TestApplyUsesCallerOwnedTransaction(t *testing.T) {
	plan := testPlan()
	recorder := &recordingTx{revision: BaselineRevision, migrationID: BaselineMigrationID, recordedChecksum: plan.Checksum()}
	if err := Apply(context.Background(), recorder, plan); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(recorder.sqls) != 4 || recorder.sqls[2] != BaselineSQL() {
		t.Fatal("Apply() did not acquire its lock and execute the authored foundation")
	}
	if err := Apply(context.Background(), nil, plan); err == nil {
		t.Fatal("Apply(nil) unexpectedly succeeded")
	}
}

func TestApplyRejectsRevisionChecksumMismatch(t *testing.T) {
	recorder := &recordingTx{revision: BaselineRevision, migrationID: BaselineMigrationID, recordedChecksum: strings.Repeat("f", 64)}
	if err := Apply(context.Background(), recorder, testPlan()); err == nil {
		t.Fatal("Apply() accepted a mismatched recorded checksum")
	}
}

func TestApplyRejectsIncompleteOrDuplicatePlan(t *testing.T) {
	recorder := &recordingTx{queryErr: pgx.ErrNoRows}
	for _, plan := range []Plan{
		{},
		{Components: []Component{{Name: "one", SQL: "SELECT 1"}}},
		{Components: []Component{{Name: "one", SQL: "SELECT 1"}, {Name: "one", SQL: "SELECT 2"}}, RolePolicySQL: "SELECT 3"},
	} {
		if err := Apply(context.Background(), recorder, plan); err == nil {
			t.Fatalf("Apply accepted invalid plan %#v", plan)
		}
	}
}
