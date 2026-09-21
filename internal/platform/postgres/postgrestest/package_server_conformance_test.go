package postgrestest_test

import (
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

var activePackageRoleTests atomic.Int32

func TestPackageServerRejectsUnrelatedURL(t *testing.T) {
	if os.Getenv(postgrestest.PackageServerURLEnv) == "" {
		t.Skip("requires the PostgreSQL conformance package runner")
	}
	if os.Getenv("LEAPVIEW_POSTGRES_CONFORMANCE_BAD_TOKEN_CHILD") == "1" {
		postgrestest.Start(t)
		return
	}
	command := exec.Command(os.Args[0], "-test.run=^TestPackageServerRejectsUnrelatedURL$")
	command.Env = append(os.Environ(),
		"LEAPVIEW_POSTGRES_CONFORMANCE_BAD_TOKEN_CHILD=1",
		postgrestest.PackageServerTokenEnv+"=not-the-runner-token",
	)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "not the runner-owned disposable server") {
		t.Fatalf("unrelated PostgreSQL URL was not rejected: err=%v output=%s", err, output)
	}
}

func TestPackageServerParallelRoleIsolationOne(t *testing.T) {
	testPackageServerParallelRole(t, "first-password")
}

func TestPackageServerParallelRoleIsolationTwo(t *testing.T) {
	testPackageServerParallelRole(t, "second-password")
}

func testPackageServerParallelRole(t *testing.T, password string) {
	t.Helper()
	if os.Getenv(postgrestest.PackageServerURLEnv) == "" {
		t.Skip("requires the PostgreSQL conformance package runner")
	}
	t.Parallel()
	if active := activePackageRoleTests.Add(1); active != 1 {
		t.Fatalf("package-shared role tests overlapped: %d active", active)
	}
	t.Cleanup(func() { activePackageRoleTests.Add(-1) })

	h := postgrestest.Start(t)
	role := h.EnsureRole(t, postgrestest.Role{Name: "package_parallel_role", Password: password, Login: true})
	database := h.NewDatabase(t, "")
	h.GrantDatabase(t, database.Name, role, "CONNECT")
	pool := openPackageTestDatabase(t, database.URL(role))
	var currentUser string
	if err := pool.QueryRow(t.Context(), "SELECT current_user").Scan(&currentUser); err != nil {
		t.Fatalf("query isolated package role: %v", err)
	}
	if currentUser != role.Name {
		t.Fatalf("current user = %q, want %q", currentUser, role.Name)
	}
}

func TestPackageServerKeepsTestDatabasesIsolated(t *testing.T) {
	if os.Getenv(postgrestest.PackageServerURLEnv) == "" {
		t.Skip("requires the PostgreSQL conformance package runner")
	}
	first := postgrestest.Start(t)
	firstRole := first.EnsureRole(t, postgrestest.Role{Name: "package_isolation_role"})
	firstDB := first.NewDatabase(t, "package_isolation_database")
	firstPool := openPackageTestDatabase(t, firstDB.AdminURL())
	if _, err := firstPool.Exec(t.Context(), "CREATE TABLE isolated_marker (id integer)"); err != nil {
		t.Fatalf("create marker in first test database: %v", err)
	}

	second := postgrestest.Start(t)
	secondRole := second.EnsureRole(t, postgrestest.Role{Name: "package_isolation_role"})
	secondDB := second.NewDatabase(t, "package_isolation_database")
	if first.AdminURL() != second.AdminURL() {
		t.Fatal("package harnesses did not use the same PostgreSQL server")
	}
	if firstRole != secondRole {
		t.Fatalf("shared role = %+v, want %+v", secondRole, firstRole)
	}
	if firstDB.Name == secondDB.Name {
		t.Fatalf("package harnesses reused database name %q", firstDB.Name)
	}
	secondPool := openPackageTestDatabase(t, secondDB.AdminURL())
	var marker *string
	if err := secondPool.QueryRow(t.Context(), "SELECT to_regclass('public.isolated_marker')::text").Scan(&marker); err != nil {
		t.Fatalf("check second test database: %v", err)
	}
	if marker != nil {
		t.Fatalf("first test database marker leaked into second: %q", *marker)
	}
	t.Run("overlapping child fixture", func(t *testing.T) {
		child := postgrestest.Start(t)
		child.EnsureRole(t, postgrestest.Role{Name: "package_isolation_role"})
		childDB := child.NewDatabase(t, "package_isolation_database")
		if childDB.Name == firstDB.Name || childDB.Name == secondDB.Name {
			t.Fatalf("child database reused an active name %q", childDB.Name)
		}
	})
}

func TestPackageServerNestedParallelFixtureKeepsSharedRoleAlive(t *testing.T) {
	if os.Getenv(postgrestest.PackageServerURLEnv) == "" {
		t.Skip("requires the PostgreSQL conformance package runner")
	}
	role := postgrestest.Role{Name: "package_nested_parallel_role", Password: "nested-parallel-password", Login: true}

	t.Run("child A", func(t *testing.T) {
		child := postgrestest.Start(t)
		childRole := child.EnsureRole(t, role)
		childDB := child.NewDatabase(t, "package_nested_parallel_child")
		child.GrantDatabase(t, childDB.Name, childRole, "CONNECT")
		childPool := openPackageTestDatabase(t, childDB.URL(childRole))

		// Pause only after the fixture exists. The parent continues and creates
		// another fixture that must keep the same shared role alive until both
		// fixture cleanups have dropped their databases.
		t.Parallel()
		assertPackageTestCurrentUser(t, childPool, childRole.Name)
	})

	parent := postgrestest.Start(t)
	parentRole := parent.EnsureRole(t, role)
	parentDB := parent.NewDatabase(t, "package_nested_parallel_parent")
	parent.GrantDatabase(t, parentDB.Name, parentRole, "CONNECT")
	parentPool := openPackageTestDatabase(t, parentDB.URL(parentRole))
	assertPackageTestCurrentUser(t, parentPool, parentRole.Name)
}

func openPackageTestDatabase(t *testing.T, databaseURL string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func assertPackageTestCurrentUser(t *testing.T, pool *pgxpool.Pool, want string) {
	t.Helper()
	var currentUser string
	if err := pool.QueryRow(t.Context(), "SELECT current_user").Scan(&currentUser); err != nil {
		t.Fatalf("query package role: %v", err)
	}
	if currentUser != want {
		t.Fatalf("current user = %q, want %q", currentUser, want)
	}
}
