package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestExistingInstanceIDDoesNotCreateIdentity(t *testing.T) {
	db := bootstrapTestDB(t)
	repository := New(db)
	if _, err := repository.ExistingInstanceID(t.Context()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing existing instance ID error = %v, want ErrNotFound", err)
	}
	var count int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM platform.instance_identity`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("existing-ID read created %d instance identities", count)
	}
	const instanceID = "lvinst_0123456789abcdefghijklmnopqrstuv"
	if err := repository.EnsureInstanceID(t.Context(), instanceID); err != nil {
		t.Fatal(err)
	}
	if got, err := repository.ExistingInstanceID(t.Context()); err != nil || got != instanceID {
		t.Fatalf("ExistingInstanceID() = %q, %v; want %q", got, err, instanceID)
	}
}

func TestCustomerOwnerDeclarationReplayConflictAndRollback(t *testing.T) {
	db := bootstrapTestDB(t)
	repository := New(db)
	if _, err := repository.CustomerOwner(t.Context()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing customer owner error = %v, want ErrNotFound", err)
	}
	const instanceID = "lvinst_0123456789abcdefghijklmnopqrstuv"
	if err := repository.EnsureInstanceID(t.Context(), instanceID); err != nil {
		t.Fatal(err)
	}

	inserted, err := repository.DeclareCustomerOwner(t.Context(), "customer_acme")
	if err != nil || !inserted {
		t.Fatalf("first customer owner declaration inserted=%t, err=%v", inserted, err)
	}
	inserted, err = repository.DeclareCustomerOwner(t.Context(), "customer_acme")
	if err != nil || inserted {
		t.Fatalf("matching customer owner replay inserted=%t, err=%v; want false, nil", inserted, err)
	}
	if _, err := repository.DeclareCustomerOwner(t.Context(), "customer_other"); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting customer owner error = %v, want ErrConflict", err)
	}
	if got, err := repository.CustomerOwner(t.Context()); err != nil || got != "customer_acme" {
		t.Fatalf("CustomerOwner() = %q, %v; want original owner", got, err)
	}
	var declaredAt time.Time
	if err := db.QueryRow(t.Context(), `SELECT declared_at FROM platform.instance_customer_owner WHERE singleton_id = 1`).Scan(&declaredAt); err != nil {
		t.Fatal(err)
	}
	var databaseTimeValid bool
	if err := db.QueryRow(t.Context(), `SELECT $1 <= clock_timestamp() AND $1 > clock_timestamp() - interval '1 minute'`, declaredAt).Scan(&databaseTimeValid); err != nil {
		t.Fatal(err)
	}
	if declaredAt.IsZero() || !databaseTimeValid {
		t.Fatalf("declared_at = %s, want a recent database timestamp", declaredAt)
	}

	rollbackDB := bootstrapTestDB(t)
	rollbackRepository := New(rollbackDB)
	if err := rollbackRepository.EnsureInstanceID(t.Context(), instanceID); err != nil {
		t.Fatal(err)
	}
	tx, err := rollbackDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if inserted, err := rollbackRepository.WithTx(tx).DeclareCustomerOwner(t.Context(), "customer_rollback"); err != nil || !inserted {
		_ = tx.Rollback(t.Context())
		t.Fatalf("transactional owner declaration inserted=%t, err=%v", inserted, err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := rollbackRepository.CustomerOwner(t.Context()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rolled-back owner = %v, want ErrNotFound", err)
	}
}

func TestCustomerOwnerDeclarationConcurrentReplayConverges(t *testing.T) {
	db := bootstrapTestDB(t)
	repository := New(db)
	if err := repository.EnsureInstanceID(t.Context(), "lvinst_0123456789abcdefghijklmnopqrstuv"); err != nil {
		t.Fatal(err)
	}

	const workers = 12
	type result struct {
		inserted bool
		err      error
	}
	results := make(chan result, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			inserted, err := repository.DeclareCustomerOwner(context.Background(), "customer_concurrent")
			results <- result{inserted: inserted, err: err}
		}()
	}
	wait.Wait()
	close(results)
	insertions := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent declaration error: %v", result.err)
		}
		if result.inserted {
			insertions++
		}
	}
	if insertions != 1 {
		t.Fatalf("successful concurrent inserts = %d, want one", insertions)
	}
	if got, err := repository.CustomerOwner(t.Context()); err != nil || got != "customer_concurrent" {
		t.Fatalf("CustomerOwner() = %q, %v; want winning declaration", got, err)
	}
}

func TestCustomerOwnerRejectsNonCanonicalIDs(t *testing.T) {
	db := bootstrapTestDB(t)
	repository := New(db)
	if err := repository.EnsureInstanceID(t.Context(), "lvinst_0123456789abcdefghijklmnopqrstuv"); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", " customer", "customer ", "customer id", "customer\tid", "customer\u00a0id", "customer\u0085id", "customer\x00id", strings.Repeat("x", 256), string([]byte{0xff})} {
		if _, err := repository.DeclareCustomerOwner(t.Context(), invalid); !errors.Is(err, ErrInvalid) {
			t.Errorf("DeclareCustomerOwner(%q) error = %v, want ErrInvalid", invalid, err)
		}
	}
	if _, err := repository.DeclareCustomerOwner(t.Context(), "客户-01"); err != nil {
		t.Fatalf("canonical UTF-8 owner rejected: %v", err)
	}
}

func TestCustomerOwnerRowsAreDatabaseImmutable(t *testing.T) {
	db := bootstrapTestDB(t)
	repository := New(db)
	if err := repository.EnsureInstanceID(t.Context(), "lvinst_0123456789abcdefghijklmnopqrstuv"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO platform.instance_customer_owner(singleton_id, instance_id, owner_id) VALUES (1, 'lvinst_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'customer_unknown')`); err == nil {
		t.Fatal("customer owner with an unknown instance ID unexpectedly succeeded")
	}
	for _, invalid := range []string{"customer id", "customer\tid", "customer\u00a0id", "customer\u0085id", strings.Repeat("x", 256)} {
		if _, err := db.Exec(t.Context(), `INSERT INTO platform.instance_customer_owner(singleton_id, instance_id, owner_id) VALUES (1, 'lvinst_0123456789abcdefghijklmnopqrstuv', $1)`, invalid); err == nil {
			t.Errorf("database accepted invalid customer owner %q", invalid)
		}
	}
	if _, err := repository.DeclareCustomerOwner(t.Context(), "customer_immutable"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `UPDATE platform.instance_customer_owner SET owner_id = 'customer_changed'`); err == nil {
		t.Fatal("customer owner update unexpectedly succeeded")
	}
	if _, err := db.Exec(t.Context(), `DELETE FROM platform.instance_customer_owner`); err == nil {
		t.Fatal("customer owner delete unexpectedly succeeded")
	}
}

func TestCustomerOwnerRolePrivileges(t *testing.T) {
	h := postgrestest.Start(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Login: true, Password: "customer-owner-test"})
	readonly := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly"})
	backup := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup"})
	db := h.NewDatabase(t, "")
	h.GrantDatabase(t, db.Name, owner, "CREATE")
	h.GrantDatabase(t, db.Name, runtime, "CONNECT")
	admin, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	conn, err := admin.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(t.Context(), "SET ROLE "+owner.Name); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var runtimeSelect, runtimeInsert, runtimeUpdate, runtimeDelete, readonlySelect, readonlyInsert, backupSelect, backupInsert bool
	query := `SELECT has_table_privilege($1, 'platform.instance_customer_owner', 'SELECT'),
		has_table_privilege($1, 'platform.instance_customer_owner', 'INSERT'),
		has_table_privilege($1, 'platform.instance_customer_owner', 'UPDATE'),
		has_table_privilege($1, 'platform.instance_customer_owner', 'DELETE'),
		has_table_privilege($2, 'platform.instance_customer_owner', 'SELECT'),
		has_table_privilege($2, 'platform.instance_customer_owner', 'INSERT'),
		has_table_privilege($3, 'platform.instance_customer_owner', 'SELECT'),
		has_table_privilege($3, 'platform.instance_customer_owner', 'INSERT')`
	if err := admin.QueryRow(t.Context(), query, runtime.Name, readonly.Name, backup.Name).Scan(
		&runtimeSelect, &runtimeInsert, &runtimeUpdate, &runtimeDelete,
		&readonlySelect, &readonlyInsert, &backupSelect, &backupInsert,
	); err != nil {
		t.Fatal(err)
	}
	if !runtimeSelect || !runtimeInsert || runtimeUpdate || runtimeDelete || !readonlySelect || readonlyInsert || !backupSelect || backupInsert {
		t.Fatalf("customer owner privileges runtime=%t/%t/%t/%t readonly=%t/%t backup=%t/%t", runtimeSelect, runtimeInsert, runtimeUpdate, runtimeDelete, readonlySelect, readonlyInsert, backupSelect, backupInsert)
	}
}
