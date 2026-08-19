package ducklake

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/workload"
)

func TestNodeUsesDuckDBBackedCatalog(t *testing.T) {
	layout := NewLayout(filepath.Join("tmp", "node"))
	if layout.CatalogPath != filepath.Join("tmp", "node", "catalog.duckdb") {
		t.Fatalf("CatalogPath = %q, want DuckDB-backed catalog", layout.CatalogPath)
	}
}

func TestEnvironmentAcquireRequiresWorkloadPermit(t *testing.T) {
	node := openLeaseTestNode(t)
	defer node.Close()

	if _, err := node.Acquire(context.Background()); !errors.Is(err, ErrUnadmitted) {
		t.Fatalf("Acquire() error = %v, want ErrUnadmitted", err)
	}
}

func TestEnvironmentLeasePinsConnectionAndNestedAcquireReusesIt(t *testing.T) {
	node := openLeaseTestNode(t)
	defer node.Close()

	ctx, releaseWorkload := admittedTestContext(t, workload.Interactive, "sales")
	defer releaseWorkload()
	lease, err := node.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}

	nested, err := node.Acquire(lease.Context())
	if err != nil {
		t.Fatal(err)
	}
	if nested.Context() != lease.Context() {
		t.Fatal("nested lease did not reuse the current connection context")
	}
	nested.Release()
	nested.Release()

	rows, err := node.Query(lease.Context(), semanticquery.Plan{SQL: "SELECT 1 AS value", Columns: []string{"value"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %#v", rows)
	}
	if err := node.Exec(lease.Context(), "CREATE TEMP TABLE pinned_values AS SELECT 1 AS value UNION ALL SELECT 2"); err != nil {
		t.Fatal(err)
	}
	count, err := node.Count(lease.Context(), semanticquery.Plan{SQL: "SELECT count(*) FROM pinned_values"})
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
	lease.Release()
	lease.Release()
}

func TestEnvironmentRejectsConflictingNestedAcquire(t *testing.T) {
	first := openLeaseTestNode(t)
	defer first.Close()
	second := openLeaseTestNode(t)
	defer second.Close()

	ctx, releaseWorkload := admittedTestContext(t, workload.Interactive, "sales")
	defer releaseWorkload()
	lease, err := first.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if _, err := second.Acquire(lease.Context()); !errors.Is(err, ErrConflictingLease) {
		t.Fatalf("Acquire() error = %v, want ErrConflictingLease", err)
	}
}

func TestEnvironmentAdmitsQuackExtension(t *testing.T) {
	node, err := Open(context.Background(), admittedConfig(t, Config{RootDir: t.TempDir(), MaxConnections: 2}, "ducklake", "quack"))
	if extensionUnavailable(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	ctx, releaseWorkload := admittedTestContext(t, workload.Refresh, "sales")
	defer releaseWorkload()
	lease, err := node.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if err := node.EnsureExtension(lease.Context(), "quack"); err != nil {
		t.Fatalf("EnsureExtension(quack): %v", err)
	}
}

func TestEnvironmentRejectsUnapprovedExtension(t *testing.T) {
	node := openLeaseTestNode(t)
	defer node.Close()
	ctx, releaseWorkload := admittedTestContext(t, workload.Refresh, "sales")
	defer releaseWorkload()
	lease, err := node.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if err := node.EnsureExtension(lease.Context(), "arbitrary"); err == nil || !strings.Contains(err.Error(), "is not approved") {
		t.Fatalf("EnsureExtension(arbitrary) error = %v, want the approved extension allowlist to reject it", err)
	}
}

func TestSpatialExtensionIsApprovedForGovernedTileExecution(t *testing.T) {
	if _, ok := approvedExtensions["spatial"]; !ok {
		t.Fatal("spatial extension is not approved")
	}
}

func TestCommitRetryClassificationIsTypedAndNarrow(t *testing.T) {
	transient := classifyCommitError(errors.New("database is locked by another writer"))
	var conflict *TransientCommitError
	if !errors.As(transient, &conflict) || !retryableCommitError(transient) {
		t.Fatalf("classified error = %T %v, want transient commit error", transient, transient)
	}
	nonTransient := classifyCommitError(errors.New("authentication failed"))
	if retryableCommitError(nonTransient) {
		t.Fatalf("authentication error classified retryable: %v", nonTransient)
	}
}

func TestExtensionInitializationCoalescesConcurrentRefreshUse(t *testing.T) {
	node := openLeaseTestNode(t)
	defer node.Close()
	ctx, releaseWorkload := admittedTestContext(t, workload.Refresh, "sales")
	defer releaseWorkload()
	lease, err := node.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	var group sync.WaitGroup
	errorsByCall := make(chan error, 8)
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			errorsByCall <- node.EnsureExtension(lease.Context(), "sqlite")
		}()
	}
	group.Wait()
	close(errorsByCall)
	for err := range errorsByCall {
		if err != nil {
			t.Skipf("sqlite extension unavailable: %v", err)
		}
	}
	stats := node.AnalyticalStats()
	if stats.ExtensionSuccess != 1 {
		t.Fatalf("extension initializations = %d, want one coalesced success", stats.ExtensionSuccess)
	}
}

func TestFatalAnalyticalHealthIsStickyAndObservable(t *testing.T) {
	node := openLeaseTestNode(t)
	defer node.Close()
	node.MarkFatal(errors.New("secret cleanup could not be proved"))
	if err := node.Healthy(); err == nil || !strings.Contains(err.Error(), "secret cleanup") {
		t.Fatalf("Healthy() error = %v", err)
	}
	select {
	case <-node.Fatal():
	default:
		t.Fatal("fatal health signal was not closed")
	}
	ctx, releaseWorkload := admittedTestContext(t, workload.Interactive, "sales")
	defer releaseWorkload()
	if _, err := node.Acquire(ctx); err == nil || !strings.Contains(err.Error(), "fatally unhealthy") {
		t.Fatalf("Acquire() after fatal health error = %v", err)
	}
}

func openLeaseTestNode(t *testing.T) *Environment {
	t.Helper()
	node, err := Open(context.Background(), admittedConfig(t, Config{RootDir: t.TempDir(), MaxConnections: 2}))
	if extensionUnavailable(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	return node
}

func admittedTestContext(t *testing.T, class workload.Class, workspaceID string) (context.Context, func()) {
	t.Helper()
	controller, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	lease, err := controller.Acquire(context.Background(), workload.Request{Class: class, PrincipalID: workspaceID, Operation: "duckdb-test", EstimatedMemoryBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	return lease.Context(), func() {
		lease.Release()
		controller.Close()
	}
}

func TestOneNodeServesPinnedReadsWhileRefreshCommits(t *testing.T) {
	ctx := context.Background()
	node, err := Open(ctx, admittedConfig(t, Config{RootDir: t.TempDir(), MaxConnections: 3}))
	if extensionUnavailable(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()

	first, err := node.Commit(ctx, "state-1", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS model; CREATE TABLE model.metrics AS SELECT 1 AS value")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	reader, err := node.sqlDB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	readTx, err := reader.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer readTx.Rollback()
	var held int
	if err := readTx.QueryRowContext(ctx, "SELECT value FROM "+SnapshotRelation(first, "metrics")).Scan(&held); err != nil {
		t.Fatal(err)
	}

	second, err := node.Commit(ctx, "state-2", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM model.metrics; INSERT INTO model.metrics VALUES (2)")
		return err
	})
	if err != nil {
		t.Fatalf("commit refresh while pinned reader is active: %v", err)
	}

	for snapshot, want := range map[int64]int{first: 1, second: 2} {
		var got int
		query := fmt.Sprintf("SELECT value FROM %s", SnapshotRelation(snapshot, "metrics"))
		if err := node.sqlDB().QueryRowContext(ctx, query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("snapshot %d value = %d, want %d", snapshot, got, want)
		}
	}
	if node.ReadConcurrency() != 3 {
		t.Fatalf("read concurrency = %d, want 3", node.ReadConcurrency())
	}
}

func TestSnapshotRelationRejectsUnsafeTableNames(t *testing.T) {
	if _, err := QualifiedSnapshotRelation(1, "orders; DROP TABLE orders"); err == nil {
		t.Fatal("unsafe table name was accepted")
	}
	if _, err := QualifiedSnapshotRelation(0, "orders"); err == nil {
		t.Fatal("zero snapshot was accepted")
	}
}

func TestNodeAppliesOneSharedResourceEnvelope(t *testing.T) {
	ctx := context.Background()
	tempDir := filepath.Join(t.TempDir(), "temp")
	node, err := Open(ctx, admittedConfig(t, Config{
		RootDir:        t.TempDir(),
		MaxConnections: 3,
		MemoryMaxBytes: 256 << 20,
		TempMaxBytes:   512 << 20,
		MaxThreads:     2,
		TempDir:        tempDir,
	}))
	if extensionUnavailable(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()

	connections := make([]*sql.Conn, 0, 3)
	for range 3 {
		connection, err := node.sqlDB().Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, connection)
	}
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	for _, connection := range connections {
		var threads int
		var configuredTemp string
		var persistentSecrets bool
		var autoInstall bool
		var autoLoad bool
		var configurationLocked bool
		if err := connection.QueryRowContext(ctx, `SELECT
			current_setting('threads'),
			current_setting('temp_directory'),
			current_setting('allow_persistent_secrets'),
			current_setting('autoinstall_known_extensions'),
			current_setting('autoload_known_extensions'),
			current_setting('lock_configuration')`,
		).Scan(&threads, &configuredTemp, &persistentSecrets, &autoInstall, &autoLoad, &configurationLocked); err != nil {
			t.Fatal(err)
		}
		if threads != 2 || configuredTemp != tempDir || persistentSecrets || autoInstall || autoLoad || !configurationLocked {
			t.Fatalf("settings = threads:%d temp:%q persistent:%t auto_install:%t auto_load:%t locked:%t", threads, configuredTemp, persistentSecrets, autoInstall, autoLoad, configurationLocked)
		}
	}
}
