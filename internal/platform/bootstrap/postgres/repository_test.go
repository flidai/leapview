package postgres

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func bootstrapTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t, postgrestest.Required(os.Getenv("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED")))
	db := h.NewDatabase(t, "")
	p, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("apply bootstrap schema: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPostgreSQL18BootstrapConcurrentIdentityAndClaimReplay(t *testing.T) {
	db := bootstrapTestDB(t)
	r := New(db)
	const workers = 16
	ids := make(chan string, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := r.InstanceID(context.Background())
			ids <- id
			errs <- err
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	var want string
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for id := range ids {
		if want == "" {
			want = id
		}
		if id != want {
			t.Fatalf("concurrent instance identity = %q, want %q", id, want)
		}
	}
	if err := r.BindInstanceEnvironment(t.Context(), "production"); err != nil {
		t.Fatal(err)
	}
	if err := r.BindInstanceEnvironment(t.Context(), "production"); err != nil {
		t.Fatalf("environment replay: %v", err)
	}
	if err := r.BindInstanceEnvironment(t.Context(), "staging"); !errors.Is(err, ErrEnvironment) {
		t.Fatalf("environment conflict = %v, want ErrEnvironment", err)
	}

	input := ProjectClaimInput{ProjectID: "project:analytics", Environment: "production", ClaimedBy: "bootstrap", ClaimedAt: time.Unix(10, 0).UTC()}
	first, err := r.ClaimProject(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.ClaimProject(t.Context(), ProjectClaimInput{ProjectID: input.ProjectID, Environment: input.Environment, ClaimedBy: "another", ClaimedAt: time.Unix(20, 0).UTC()})
	if err != nil {
		t.Fatalf("claim replay: %v", err)
	}
	if first != second {
		t.Fatalf("claim replay changed winner evidence: first=%#v second=%#v", first, second)
	}
	if _, err := r.ClaimProject(t.Context(), ProjectClaimInput{ProjectID: "project:other", Environment: input.Environment, ClaimedBy: input.ClaimedBy, ClaimedAt: input.ClaimedAt}); !errors.Is(err, ErrConflict) {
		t.Fatalf("claim conflict = %v, want ErrConflict", err)
	}
}

func TestBootstrapImmutableTamperAndCallerRollback(t *testing.T) {
	db := bootstrapTestDB(t)
	r := New(db)
	if err := r.EnsureInstanceID(t.Context(), "lvinst_0123456789abcdefghijklmnopqrstuv"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `UPDATE platform.instance_identity SET instance_id = 'lvinst_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'`); err == nil {
		t.Fatal("instance identity update unexpectedly succeeded")
	}
	claim := ProjectClaimInput{ProjectID: "project:rollback", Environment: "dev", ClaimedBy: "test", ClaimedAt: time.Unix(1, 0).UTC()}
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tr := r.WithTx(tx)
	if err := tr.BindInstanceEnvironmentTx(t.Context(), tx, "dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.ClaimProjectTx(t.Context(), tx, claim); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.InstanceEnvironment(t.Context()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rolled-back environment = %v, want ErrNotFound", err)
	}
	if _, err := r.GetProjectClaim(t.Context()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rolled-back claim = %v, want not found", err)
	}
	if _, err := r.ClaimProject(t.Context(), claim); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `DELETE FROM platform.instance_project_claim`); err == nil {
		t.Fatal("instance project claim delete unexpectedly succeeded")
	}
}

func TestBootstrapRolePrivileges(t *testing.T) {
	h := postgrestest.Start(t, postgrestest.Required(os.Getenv("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED")))
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "leapview-conformance-secret", Login: true})
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
	var canRuntimeUpdate, canRuntimeInsert, canReadonlySelect, canBackupSelect bool
	if err := admin.QueryRow(t.Context(), `SELECT has_table_privilege($1, 'platform.setting', 'UPDATE'), has_table_privilege($1, 'platform.instance_identity', 'INSERT'), has_table_privilege($2, 'platform.setting', 'SELECT'), has_table_privilege($3, 'platform.setting', 'SELECT')`, runtime.Name, readonly.Name, backup.Name).Scan(&canRuntimeUpdate, &canRuntimeInsert, &canReadonlySelect, &canBackupSelect); err != nil {
		t.Fatal(err)
	}
	if !canRuntimeUpdate || !canRuntimeInsert || !canReadonlySelect || !canBackupSelect {
		t.Fatalf("bootstrap role grants runtime update/insert=%t/%t readonly/backup select=%t/%t", canRuntimeUpdate, canRuntimeInsert, canReadonlySelect, canBackupSelect)
	}
}
