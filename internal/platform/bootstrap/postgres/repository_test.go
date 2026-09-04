package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func bootstrapTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator"})
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Login: true, Password: "runtime-secret"})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly"})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup"})
	h.GrantRole(t, owner, migrator)
	database := h.NewDatabase(t, "")
	h.GrantDatabase(t, database.Name, migrator, "CONNECT", "CREATE")
	h.GrantDatabase(t, database.Name, runtime, "CONNECT")
	migratorDB, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(migratorDB.Close)
	conn, err := migratorDB.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(t.Context(), `SET ROLE leapview_control_migrator`); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.Apply(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("apply PostgreSQL migrations: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	runtimeDB, err := pgxpool.New(t.Context(), database.URL(runtime))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	return runtimeDB
}

func TestPostgreSQLBootstrapIdentityConvergesAndEnvironmentReplayIsImmutable(t *testing.T) {
	db := bootstrapTestDB(t)
	repository := New(db)
	const workers = 16
	ids := make(chan string, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := repository.InstanceID(context.Background())
			ids <- id
			errs <- err
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var want string
	for id := range ids {
		if want == "" {
			want = id
		}
		if id != want {
			t.Fatalf("concurrent instance identity = %q, want %q", id, want)
		}
	}
	if err := repository.BindInstanceEnvironment(t.Context(), "production"); err != nil {
		t.Fatal(err)
	}
	if err := repository.BindInstanceEnvironment(t.Context(), "production"); err != nil {
		t.Fatalf("environment replay: %v", err)
	}
	if _, err := repository.InstanceEnvironment(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := repository.BindInstanceEnvironment(t.Context(), "staging"); !errors.Is(err, ErrEnvironment) {
		t.Fatalf("environment conflict = %v, want ErrEnvironment", err)
	}
}

func TestPostgreSQLBootstrapIdentityExplicitConflict(t *testing.T) {
	db := bootstrapTestDB(t)
	repository := New(db)
	first := "lvinst_0123456789abcdefghijklmnopqrstuv"
	if err := repository.EnsureInstanceID(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := repository.EnsureInstanceID(t.Context(), first); err != nil {
		t.Fatalf("identity replay: %v", err)
	}
	if err := repository.EnsureInstanceID(t.Context(), "lvinst_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); !errors.Is(err, ErrConflict) {
		t.Fatalf("identity conflict = %v, want ErrConflict", err)
	}
}
