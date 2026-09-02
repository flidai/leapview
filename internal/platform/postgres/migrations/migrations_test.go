package migrations

import (
	"context"
	"errors"
	"slices"
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
	revisions        map[int64]recordingRow
}

func (r *recordingTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	r.sqls = append(r.sqls, sql)
	if strings.Contains(sql, "INSERT INTO platform.schema_revision") && len(args) == 3 {
		if r.revisions == nil {
			r.revisions = make(map[int64]recordingRow)
		}
		revision := args[0].(int64)
		r.revisions[revision] = recordingRow{revision: revision, migrationID: args[1].(string), checksum: args[2].(string)}
	}
	return pgconn.CommandTag{}, nil
}

func (r *recordingTx) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	if len(args) == 1 && r.revisions != nil {
		if row, ok := r.revisions[args[0].(int64)]; ok {
			return row
		}
		return recordingRow{err: pgx.ErrNoRows}
	}
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
	for _, marker := range []string{"platform.schema_revision", "platform.reject_schema_revision_mutation", "leapview_control_owner", "leapview_control_maintenance", "leapview_control_backup", ")) <> 6"} {
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
		Migrations:    []Migration{{Revision: 2, MigrationID: "002_test", SQL: "SELECT 3"}},
		RolePolicySQL: "SELECT 2",
	}
}

func TestApplyUsesCallerOwnedTransaction(t *testing.T) {
	plan := testPlan()
	recorder := &recordingTx{revisions: map[int64]recordingRow{
		BaselineRevision: {revision: BaselineRevision, migrationID: BaselineMigrationID, checksum: plan.Checksum()},
		2:                {revision: 2, migrationID: "002_test", checksum: plan.Migrations[0].Checksum()},
	}}
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
	recorder := &recordingTx{revisions: map[int64]recordingRow{
		BaselineRevision: {revision: BaselineRevision, migrationID: BaselineMigrationID, checksum: strings.Repeat("f", 64)},
	}}
	if err := Apply(context.Background(), recorder, testPlan()); err == nil {
		t.Fatal("Apply() accepted a mismatched recorded checksum")
	}
}

func TestApplyAppendsForwardMigrationWithoutChangingBaselineIdentity(t *testing.T) {
	plan := testPlan()
	withoutForward := plan
	withoutForward.Migrations = nil
	if plan.Checksum() != withoutForward.Checksum() {
		t.Fatal("forward migration changed the immutable baseline checksum")
	}
	recorder := &recordingTx{revisions: map[int64]recordingRow{
		BaselineRevision: {revision: BaselineRevision, migrationID: BaselineMigrationID, checksum: plan.Checksum()},
	}}
	if err := Apply(context.Background(), recorder, plan); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	forward := recorder.revisions[2]
	if forward.migrationID != "002_test" || forward.checksum != plan.Migrations[0].Checksum() {
		t.Fatalf("forward revision = %#v", forward)
	}
	if got := recorder.revisions[BaselineRevision].checksum; got != plan.Checksum() {
		t.Fatalf("baseline checksum changed to %q", got)
	}
	if !slices.Contains(recorder.sqls, "SELECT 3") {
		t.Fatal("forward migration SQL was not executed")
	}
}

func TestApplyRejectsIncompleteOrDuplicatePlan(t *testing.T) {
	recorder := &recordingTx{queryErr: pgx.ErrNoRows}
	for _, plan := range []Plan{
		{},
		{Components: []Component{{Name: "one", SQL: "SELECT 1"}}},
		{Components: []Component{{Name: "one", SQL: "SELECT 1"}, {Name: "one", SQL: "SELECT 2"}}, RolePolicySQL: "SELECT 3"},
		{Components: []Component{{Name: "one", SQL: "SELECT 1"}}, Migrations: []Migration{{Revision: 3, MigrationID: "003_gap", SQL: "SELECT 4"}}, RolePolicySQL: "SELECT 3"},
		{Components: []Component{{Name: "one", SQL: "SELECT 1"}}, Migrations: []Migration{{Revision: 2, MigrationID: "", SQL: "SELECT 4"}}, RolePolicySQL: "SELECT 3"},
	} {
		if err := Apply(context.Background(), recorder, plan); err == nil {
			t.Fatalf("Apply accepted invalid plan %#v", plan)
		}
	}
}
