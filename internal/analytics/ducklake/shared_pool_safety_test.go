package ducklake

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/app/testing/extensionfixture"
	"github.com/flidai/leapview/internal/workload"
)

func TestDataInliningPolicyUsesPersistedTableSchemaGlobalPrecedence(t *testing.T) {
	policy := DataInliningPolicy{
		ProcessLimit: 0, ProcessSet: true,
		AttachLimit: 0, AttachSet: true,
		Persisted: []PersistedInliningOption{
			{Scope: InliningGlobal, Limit: 0},
			{Scope: InliningSchema, Entry: "model", Limit: 3},
			{Scope: InliningTable, Entry: "model.orders", Limit: 0},
		},
	}
	if got := policy.Effective("model", "model.orders"); got.Limit != 0 || got.Source != InliningTable {
		t.Fatalf("table effective policy = %#v, want explicit table zero", got)
	}
	if got := policy.Effective("model", "customers"); got.Limit != 3 || got.Source != InliningSchema {
		t.Fatalf("schema effective policy = %#v, want schema override", got)
	}
	if err := policy.ValidateZero(); err == nil || !errors.Is(err, ErrInliningNotDisabled) {
		t.Fatalf("ValidateZero() = %v, want persisted non-zero rejection", err)
	}
}

func TestDataInliningPolicyFailsClosedWhenNoScopeIsRecorded(t *testing.T) {
	got := (DataInliningPolicy{}).Effective("model", "orders")
	if got.Limit == 0 {
		t.Fatalf("unrecorded policy = %#v, want non-zero fail-closed value", got)
	}
}

func TestCompatibilityEvidenceIsVersionedAndDigestStable(t *testing.T) {
	tuple := CompatibilityTuple{
		DuckDBRuntime: "v1", DuckLakeExtension: "v2", CatalogFormat: "1.1",
		StorageImplementation: "local", ObjectNamingContract: "uuidv7",
	}
	first, err := NewCompatibilityEvidence(tuple, []CompatibilityCheck{{Name: "remote-read", Passed: true}, {Name: "same-table", Passed: true}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewCompatibilityEvidence(tuple, []CompatibilityCheck{{Name: "same-table", Passed: true}, {Name: "remote-read", Passed: true}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest {
		t.Fatalf("digest changed with check ordering: %q vs %q", first.Digest, second.Digest)
	}
	if err := ValidateCompatibilityEvidence(first, tuple); err != nil {
		t.Fatal(err)
	}
	first.Checks[0].Passed = false
	if err := ValidateCompatibilityEvidence(first, tuple); err == nil {
		t.Fatal("tampered evidence unexpectedly validated")
	}
}

func TestCrossCatalogUnionAndOrphanClassificationIncludesDataAndDeleteFiles(t *testing.T) {
	catalogs := []CatalogFileSet{
		{CatalogID: "a", DataFiles: []string{"./shared.parquet", "a.parquet"}, DeleteFiles: []string{"a.delete.parquet"}},
		{CatalogID: "b", DataFiles: []string{"shared.parquet", "b.parquet"}, DeleteFiles: []string{"b.delete.parquet"}},
	}
	marks := CrossCatalogLiveFileUnion(catalogs)
	if len(marks) != 5 {
		t.Fatalf("live union = %#v, want five typed files", marks)
	}
	classified := ClassifyPoolObjects([]PoolObject{
		{Path: "b.parquet", Kind: DataFile},
		{Path: "unused.parquet", Kind: DataFile},
		{Path: "a.delete.parquet", Kind: DeleteFile},
		{Path: "unused.delete.parquet", Kind: DeleteFile},
	}, catalogs)
	var live, orphan int
	for _, file := range classified {
		if file.Live {
			live++
		} else {
			orphan++
		}
	}
	if live != 2 || orphan != 2 {
		t.Fatalf("classification = %#v, want 2 live and 2 orphan", classified)
	}
}

func TestSharedPoolRejectsNativeCleanupCheckpointAndMaintenance(t *testing.T) {
	env := &Environment{sharedPool: true}
	if err := env.CleanupOldFiles(context.Background(), true); !errors.Is(err, ErrSharedPoolMaintenance) {
		t.Fatalf("cleanup error = %v", err)
	}
	if err := env.DeleteOrphanedFiles(context.Background(), true); !errors.Is(err, ErrSharedPoolMaintenance) {
		t.Fatalf("orphan cleanup error = %v", err)
	}
	if err := env.rejectSharedPoolStatement("CHECKPOINT lake"); !errors.Is(err, ErrUnsafeCheckpoint) {
		t.Fatalf("checkpoint error = %v", err)
	}
	if err := env.rejectSharedPoolStatement("CALL ducklake_cleanup_old_files('lake')"); !errors.Is(err, ErrSharedPoolMaintenance) {
		t.Fatalf("maintenance statement error = %v", err)
	}
	if err := env.rejectSharedPoolStatement("SELECT 'CHECKPOINT ducklake_cleanup_old_files', \"CHECKPOINT\", $$ducklake_delete_orphaned_files$$ -- ducklake_delete_orphaned_files\n"); err != nil {
		t.Fatalf("literal/comment maintenance words rejected: %v", err)
	}
	if err := env.rejectSharedPoolStatement("cHeCkPoInT lake"); !errors.Is(err, ErrUnsafeCheckpoint) {
		t.Fatalf("mixed-case checkpoint error = %v", err)
	}
}

func TestSharedPoolStatementGuardCoversRawSessionTransactionAndPreparedPaths(t *testing.T) {
	env, err := Open(context.Background(), fixtureConfig(t, Config{RootDir: t.TempDir(), MaxConnections: 2}))
	if extensionUnavailable(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	ctx, releaseWorkload := admittedTestContext(t, workload.Interactive, "shared-guard")
	defer releaseWorkload()
	lease, err := env.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	session, err := env.Session(lease.Context())
	if err != nil {
		t.Fatal(err)
	}
	rawConn, err := env.db.Conn(lease.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rawConn.Close()
	unsafeStatements := []string{
		"CALL ducklake_cleanup_old_files('lake')",
		"SELECT ducklake_delete_orphaned_files('lake')",
		"CHECKPOINT lake",
	}
	for _, statement := range unsafeStatements {
		if _, err := session.ExecContext(lease.Context(), statement); err == nil {
			t.Fatalf("session accepted unsafe statement %q", statement)
		}
		if _, err := rawConn.ExecContext(lease.Context(), statement); err == nil {
			t.Fatalf("raw connection accepted unsafe statement %q", statement)
		}
		prepared, err := rawConn.PrepareContext(lease.Context(), statement)
		if err == nil {
			prepared.Close()
			t.Fatalf("prepared raw connection accepted unsafe statement %q", statement)
		}
	}
	if _, err := env.Commit(lease.Context(), "guarded-commit", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(lease.Context(), "CALL ducklake_cleanup_old_files('lake')")
		return err
	}); err == nil {
		t.Fatal("transaction callback accepted unsafe maintenance SQL")
	}
	commentAndString := "SELECT 'ducklake_cleanup_old_files CHECKPOINT' AS text -- ducklake_delete_orphaned_files\n"
	if _, err := session.ExecContext(lease.Context(), commentAndString); err != nil {
		t.Fatalf("session rejected maintenance words in literals/comments: %v", err)
	}
}

func TestScheduledRetentionHasNoNativeDuckLakeMaintenanceSQL(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	retentionDir := filepath.Join(filepath.Dir(filename), "..", "..", "servingstate", "retention")
	entries, err := os.ReadDir(retentionDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(retentionDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		text := strings.ToLower(string(contents))
		for _, token := range []string{"ducklake_cleanup_old_files", "ducklake_delete_orphaned_files", "ducklake_merge_adjacent_files", "ducklake_rewrite_data_files"} {
			if strings.Contains(text, token) {
				t.Fatalf("scheduled retention embeds native DuckLake maintenance SQL %q in %s", token, entry.Name())
			}
		}
	}
}

func TestEnvironmentCloseIsIdempotentAndRejectsNewConnections(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	env := &Environment{db: db}
	if err := env.Close(); err != nil {
		t.Fatal(err)
	}
	if err := env.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
	if _, _, err := env.queryConnection(context.Background()); !errors.Is(err, ErrEnvironmentClosed) {
		t.Fatalf("queryConnection after close = %v", err)
	}
}

func TestQueryConnectionRejectsFatalEnvironment(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	env := &Environment{db: db}
	fatal := errors.New("sealed lease expired")
	env.MarkFatal(fatal)
	if _, _, err := env.queryConnection(context.Background()); !errors.Is(err, fatal) {
		t.Fatalf("queryConnection after fatal mark = %v, want fatal error", err)
	}
	_ = env.Close()
}

func TestReadOnlyOpenDoesNotPrepareMissingLayout(t *testing.T) {
	root := t.TempDir()
	catalog := filepath.Join(root, "catalog.duckdb")
	data := filepath.Join(root, "data")
	if _, err := Open(context.Background(), Config{RootDir: root, ReadOnly: true}); err == nil {
		t.Fatal("read-only Open accepted a missing catalog and data path")
	}
	if _, err := os.Stat(catalog); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only Open catalog stat error = %v, want path to remain absent", err)
	}
	if _, err := os.Stat(data); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only Open data path stat error = %v, want path to remain absent", err)
	}
	if err := os.WriteFile(catalog, []byte("sealed artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), Config{RootDir: root, ReadOnly: true}); err == nil {
		t.Fatal("read-only Open accepted a catalog with missing local data path")
	}
	if _, err := os.Stat(data); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only Open created missing data path: %v", err)
	}
}

func TestEnvironmentReportsZeroInliningAcrossRuntimeScopes(t *testing.T) {
	env, err := Open(context.Background(), fixtureConfig(t, Config{RootDir: t.TempDir()}))
	if extensionUnavailable(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	policy, err := env.DataInliningPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.ValidateZero(); err != nil {
		t.Fatalf("zero inlining policy rejected: %v", err)
	}
	if err := env.ValidateNoLiveInlineData(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialBootstrapRunsForEveryPooledConnector(t *testing.T) {
	var calls atomic.Int32
	bootstrap := func(ctx context.Context, execer driver.ExecerContext) error {
		if _, err := execer.ExecContext(ctx, "SELECT 1", nil); err != nil {
			return err
		}
		calls.Add(1)
		return nil
	}
	env, err := Open(context.Background(), fixtureConfig(t, Config{RootDir: t.TempDir(), MaxConnections: 3, CredentialBootstrap: bootstrap}))
	if extensionUnavailable(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	if got := calls.Load(); got < 3 {
		t.Fatalf("credential bootstrap calls = %d, want one per warmed connector", got)
	}
	connections := make([]*sql.Conn, 0, 3)
	for range 3 {
		connection, err := env.db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := connection.PingContext(context.Background()); err != nil {
			connection.Close()
			t.Fatal(err)
		}
		connections = append(connections, connection)
	}
	for _, connection := range connections {
		if err := connection.Close(); err != nil {
			t.Fatal(err)
		}
	}
	beforeReplacement := calls.Load()
	env.db.SetConnMaxLifetime(time.Nanosecond)
	time.Sleep(2 * time.Millisecond)
	connection, err := env.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.PingContext(context.Background()); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got <= beforeReplacement {
		t.Fatalf("credential bootstrap calls did not increase for replacement: before=%d after=%d", beforeReplacement, got)
	}
}

func TestSharedPoolConformanceRejectsIncompleteEvidence(t *testing.T) {
	compatibility := physicalpool.Compatibility{
		DuckDBRuntime: "duckdb:test", DuckLakeExtension: "ducklake:test", CatalogFormat: "ducklake:v1",
		StorageImplementation: "local", ObjectNamingContract: "uuidv7:v1",
	}
	if _, err := (SharedPoolConformance{Compatibility: compatibility, Checks: map[string]ConformanceCheck{}}).Run(context.Background()); err == nil {
		t.Fatal("incomplete conformance unexpectedly produced evidence")
	}
}

func TestSharedPoolConformanceLocalClosedCloneFixture(t *testing.T) {
	ctx := context.Background()
	sharedData := filepath.Join(t.TempDir(), "shared-data")
	baseRoot := t.TempDir()
	baseCatalog := filepath.Join(baseRoot, "catalog.duckdb")
	base, err := Open(ctx, fixtureConfig(t, Config{RootDir: baseRoot, CatalogPath: baseCatalog, DataPath: sharedData}))
	if extensionUnavailable(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.Commit(ctx, "fixture-base", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS model;
CREATE TABLE model.orders(id BIGINT, value VARCHAR);
CREATE TABLE model.metrics(id BIGINT, value VARCHAR);
INSERT INTO model.orders SELECT range, 'base' FROM range(1, 1001);
INSERT INTO model.metrics SELECT range, 'base' FROM range(1, 1001);`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := base.Exec(ctx, "CALL ducklake_set_option('lake', 'rewrite_delete_threshold', 1.0, schema => 'model', table_name => 'orders')"); err != nil {
		t.Fatal(err)
	}
	baseFilesOrders, err := base.CurrentFileSet(ctx, "base", "model", "orders")
	if err != nil {
		t.Fatal(err)
	}
	baseFilesMetrics, err := base.CurrentFileSet(ctx, "base", "model", "metrics")
	if err != nil {
		t.Fatal(err)
	}
	if err := base.Close(); err != nil {
		t.Fatal(err)
	}
	baseBytes, err := os.ReadFile(baseCatalog)
	if err != nil {
		t.Fatal(err)
	}
	baseDigest := digestBytesForTest(baseBytes)
	copyCatalog := func(path string) error {
		if err := os.WriteFile(path, baseBytes, 0o600); err != nil {
			return err
		}
		copied, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if got := digestBytesForTest(copied); got != baseDigest {
			return fmt.Errorf("catalog copy digest mismatch: source=%s copy=%s", baseDigest, got)
		}
		return nil
	}
	aRoot, bRoot := t.TempDir(), t.TempDir()
	aCatalog, bCatalog := filepath.Join(aRoot, "catalog.duckdb"), filepath.Join(bRoot, "catalog.duckdb")
	if err := copyCatalog(aCatalog); err != nil {
		t.Fatal(err)
	}
	if err := copyCatalog(bCatalog); err != nil {
		t.Fatal(err)
	}
	a, err := Open(ctx, fixtureConfig(t, Config{RootDir: aRoot, CatalogPath: aCatalog, DataPath: sharedData}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	b, err := Open(ctx, fixtureConfig(t, Config{RootDir: bRoot, CatalogPath: bCatalog, DataPath: sharedData}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	if _, err := b.CurrentFileSet(ctx, "b", "model", "metrics"); err != nil {
		t.Fatalf("B initial metrics file set: %v", err)
	}
	var writes sync.WaitGroup
	writes.Add(2)
	writeErrors := make(chan error, 2)
	go func() {
		defer writes.Done()
		_, commitErr := a.Commit(ctx, "fixture-a", nil, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO model.orders VALUES (2, 'a')")
			return err
		})
		if commitErr != nil {
			commitErr = fmt.Errorf("A commit: %w", commitErr)
		}
		writeErrors <- commitErr
	}()
	go func() {
		defer writes.Done()
		_, commitErr := b.Commit(ctx, "fixture-b", nil, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO model.metrics VALUES (2, 'b')")
			return err
		})
		if commitErr != nil {
			commitErr = fmt.Errorf("B commit: %w", commitErr)
		}
		writeErrors <- commitErr
	}()
	writes.Wait()
	close(writeErrors)
	for writeErr := range writeErrors {
		if writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	// Produce a real positional delete file after the concurrent writes.
	if _, err := a.Commit(ctx, "fixture-a-delete", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM model.orders WHERE id = 2")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	aOrders, err := a.CurrentFileSet(ctx, "a", "model", "orders")
	if err != nil {
		t.Fatal(err)
	}
	aMetrics, err := a.CurrentFileSet(ctx, "a", "model", "metrics")
	if err != nil {
		t.Fatal(err)
	}
	bOrders, err := b.CurrentFileSet(ctx, "b", "model", "orders")
	if err != nil {
		t.Fatal(err)
	}
	bMetrics, err := b.CurrentFileSet(ctx, "b", "model", "metrics")
	if err != nil {
		t.Fatal(err)
	}
	if len(aOrders.DeleteFiles) == 0 {
		t.Fatal("real delete produced no delete_file reference")
	}
	if !containsAll(aMetrics.DataFiles, baseFilesMetrics.DataFiles) || !containsAll(bOrders.DataFiles, baseFilesOrders.DataFiles) {
		t.Fatalf("unchanged inherited refs missing: a metrics=%#v b orders=%#v", aMetrics, bOrders)
	}
	if overlap(aOrders.DataFiles, bMetrics.DataFiles) {
		t.Fatalf("changed outputs reused one another's physical key: a=%#v b=%#v", aOrders, bMetrics)
	}
	if got, err := countTableRows(ctx, a, "model.orders"); err != nil {
		t.Fatal(err)
	} else if got != 999 {
		t.Fatalf("same-table writer count = %d, want 999 after duplicate insert and delete", got)
	}
	if got, err := countTableRows(ctx, b, "model.orders"); err != nil {
		t.Fatal(err)
	} else if got != 1000 {
		t.Fatalf("same-table peer count = %d, want untouched 1000", got)
	}
	if got, err := countTableRows(ctx, b, "model.metrics"); err != nil {
		t.Fatal(err)
	} else if got != 1001 {
		t.Fatalf("different-table writer count = %d, want 1001", got)
	}
	if got, err := countTableRows(ctx, a, "model.metrics"); err != nil {
		t.Fatal(err)
	} else if got != 1000 {
		t.Fatalf("different-table peer count = %d, want untouched 1000", got)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	b, err = Open(ctx, fixtureConfig(t, Config{RootDir: bRoot, CatalogPath: bCatalog, DataPath: sharedData, ReadOnly: true}))
	if err != nil {
		t.Fatal(err)
	}
	if !b.ReadOnly() {
		t.Fatal("reopened sealed catalog did not report read-only status")
	}
	if err := b.Exec(ctx, "INSERT INTO model.metrics VALUES (2000, 'should-fail')"); !errors.Is(err, ErrReadOnlyEnvironment) {
		t.Fatalf("read-only INSERT error = %v, want ErrReadOnlyEnvironment", err)
	}
	if _, err := b.Commit(ctx, "read-only-write", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO model.metrics VALUES (2001, 'should-fail')")
		return err
	}); !errors.Is(err, ErrReadOnlyEnvironment) {
		t.Fatalf("read-only commit error = %v, want ErrReadOnlyEnvironment", err)
	}
	if err := b.CleanupOldFiles(ctx, true); !errors.Is(err, ErrReadOnlyEnvironment) {
		t.Fatalf("read-only maintenance error = %v, want ErrReadOnlyEnvironment", err)
	}
	liveCatalogs := []CatalogFileSet{aOrders, aMetrics, bOrders, bMetrics}
	live := CrossCatalogLiveFileUnion(liveCatalogs)
	if len(live) == 0 {
		t.Fatal("cross-catalog live union is empty")
	}
	objects := make([]PoolObject, 0, len(live)+1)
	for _, file := range live {
		objects = append(objects, PoolObject{Path: file.Path, Kind: file.Kind})
	}
	objects = append(objects, PoolObject{Path: "fixture-orphan.parquet", Kind: DataFile})
	classified := ClassifyPoolObjects(objects, liveCatalogs)
	if classified[len(classified)-1].Live {
		t.Fatal("unreferenced physical object was marked live")
	}
	compatibility := physicalpool.Compatibility{DuckDBRuntime: "duckdb:test", DuckLakeExtension: "ducklake:test", CatalogFormat: "ducklake:v1", StorageImplementation: "local", ObjectNamingContract: "uuidv7:v1"}
	checks := map[string]ConformanceCheck{
		"same_table_private_clone_isolation": func(ctx context.Context) ([]byte, error) {
			writerCount, err := countTableRows(ctx, a, "model.orders")
			if err != nil {
				return nil, err
			}
			peerCount, err := countTableRows(ctx, b, "model.orders")
			if err != nil {
				return nil, err
			}
			if writerCount != 999 || peerCount != 1000 {
				return nil, fmt.Errorf("same-table counts writer=%d peer=%d", writerCount, peerCount)
			}
			return []byte(fmt.Sprintf("a-orders=%d;b-orders=%d", writerCount, peerCount)), nil
		},
		"different_table_private_clone_isolation": func(ctx context.Context) ([]byte, error) {
			writerCount, err := countTableRows(ctx, b, "model.metrics")
			if err != nil {
				return nil, err
			}
			peerCount, err := countTableRows(ctx, a, "model.metrics")
			if err != nil {
				return nil, err
			}
			if writerCount != 1001 || peerCount != 1000 {
				return nil, fmt.Errorf("different-table counts writer=%d peer=%d", writerCount, peerCount)
			}
			return []byte(fmt.Sprintf("b-metrics=%d;a-metrics=%d", writerCount, peerCount)), nil
		},
		"unchanged_file_reference_reuse": func(context.Context) ([]byte, error) {
			if !containsAll(aMetrics.DataFiles, baseFilesMetrics.DataFiles) || !containsAll(bOrders.DataFiles, baseFilesOrders.DataFiles) {
				return nil, errors.New("inherited data-file references are missing")
			}
			return []byte(strings.Join(append(append([]string{}, baseFilesOrders.DataFiles...), baseFilesMetrics.DataFiles...), "\x00")), nil
		},
		"new_file_key_disjointness": func(context.Context) ([]byte, error) {
			if overlap(aOrders.DataFiles, bMetrics.DataFiles) {
				return nil, errors.New("new data-file keys overlap")
			}
			return []byte(strings.Join(append(aOrders.DataFiles, bMetrics.DataFiles...), "\x00")), nil
		},
		"aborted_write_isolation": func(ctx context.Context) ([]byte, error) {
			before, err := a.Snapshots(ctx)
			if err != nil {
				return nil, err
			}
			if _, err := a.Commit(ctx, "fixture-abort", nil, func(tx *sql.Tx) error {
				if _, execErr := tx.ExecContext(ctx, "INSERT INTO model.orders VALUES (99, 'aborted')"); execErr != nil {
					return execErr
				}
				return errors.New("fixture abort")
			}); err == nil {
				return nil, errors.New("aborted write succeeded")
			}
			after, err := a.Snapshots(ctx)
			if err != nil || len(after) != len(before) {
				return nil, fmt.Errorf("abort snapshot isolation: before=%d after=%d err=%v", len(before), len(after), err)
			}
			return []byte(fmt.Sprintf("snapshots=%d", len(after))), nil
		},
		"normalization_file_union_completeness": func(context.Context) ([]byte, error) {
			if len(live) == 0 || len(aOrders.DeleteFiles) == 0 {
				return nil, errors.New("data/delete closure is incomplete")
			}
			return []byte(fmt.Sprintf("live=%d;delete=%d", len(live), len(aOrders.DeleteFiles))), nil
		},
		"cross_catalog_orphan_classification": func(context.Context) ([]byte, error) {
			if classified[len(classified)-1].Live {
				return nil, errors.New("orphan classified live")
			}
			return []byte(fmt.Sprintf("objects=%d;live=%d", len(classified), len(live))), nil
		},
		"sealed_catalog_read": func(ctx context.Context) ([]byte, error) {
			count, err := countTableRows(ctx, b, "model.metrics")
			if err != nil {
				return nil, err
			}
			if count != 1001 {
				return nil, fmt.Errorf("reopened sealed catalog metrics count=%d, want 1001", count)
			}
			return []byte(fmt.Sprintf("reopened-b-metrics=%d", count)), nil
		},
		"safe_close": func(ctx context.Context) ([]byte, error) {
			if err := b.Close(); err != nil {
				return nil, err
			}
			if _, _, err := b.queryConnection(ctx); !errors.Is(err, ErrEnvironmentClosed) {
				return nil, fmt.Errorf("closed clone accepted new connection: %v", err)
			}
			return []byte("closed"), nil
		},
	}
	evidence, err := (SharedPoolConformance{Compatibility: compatibility, Checks: checks}).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := (SharedPoolConformance{Compatibility: compatibility, Checks: checks}).ValidateEvidence(evidence); err != nil {
		t.Fatal(err)
	}
}

func containsAll(have, want []string) bool {
	for _, item := range want {
		found := false
		for _, candidate := range have {
			if candidate == item {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func overlap(left, right []string) bool {
	for _, a := range left {
		for _, b := range right {
			if a == b {
				return true
			}
		}
	}
	return false
}

func countTableRows(ctx context.Context, env *Environment, table string) (int, error) {
	var count int
	if err := env.sqlDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func digestBytesForTest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func fixtureConfig(t *testing.T, config Config) Config {
	t.Helper()
	dataPath := config.DataPath
	if dataPath == "" {
		dataPath = filepath.Join(config.RootDir, "data")
	}
	storageImplementation := "local"
	if strings.Contains(dataPath, "://") {
		storageImplementation = "s3"
	}
	config.PoolContract = fixturePoolContractFor(t, storageImplementation, dataPath)
	if config.ExtensionAdmission == nil {
		config.ExtensionAdmission = extensionfixture.New(t, "ducklake", "sqlite").Admission
	}
	config.SharedPool = true
	config.PhysicalPoolID = ""
	config.Compatibility = CompatibilityTuple{}
	return config
}

func admittedConfig(t *testing.T, config Config, names ...string) Config {
	t.Helper()
	if len(names) == 0 {
		names = []string{"ducklake"}
	}
	config.ExtensionAdmission = extensionfixture.New(t, names...).Admission
	return config
}

func fixturePoolContract(t *testing.T) *PoolContract {
	return fixturePoolContractFor(t, "local", filepath.Join(t.TempDir(), "fixture-data"))
}

func fixturePoolContractFor(t *testing.T, storageImplementation, dataPath string) *PoolContract {
	t.Helper()
	storageLocation, storageNamespace := fixtureStorageIdentity(t, dataPath)
	tuple := physicalpool.Compatibility{
		DuckDBRuntime: "duckdb:test", DuckLakeExtension: "ducklake:test", CatalogFormat: "ducklake:v1",
		StorageImplementation: storageImplementation, ObjectNamingContract: "uuidv7:v1",
	}
	checks := make([]physicalpool.EvidenceCheck, 0, len(SharedPoolConformanceChecks))
	for _, name := range SharedPoolConformanceChecks {
		checks = append(checks, physicalpool.EvidenceCheck{ID: name, Passed: true, ObservationDigest: digestBytesForTest([]byte("fixture:" + name))})
	}
	evidence, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{Compatibility: tuple, ConformanceVersion: SharedPoolConformanceVersion, Checks: checks})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := physicalpool.NewPhysicalPool(physicalpool.PoolIdentity{
		StorageLocation: storageLocation, StorageNamespace: storageNamespace, IsolationBoundary: "fixture",
		EncryptionDomain: "fixture", RetentionAuthority: "fixture", Compatibility: tuple,
	})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := pool.Admit(evidence)
	if err != nil {
		t.Fatal(err)
	}
	pool, err = pool.ApplyAdmission(admission)
	if err != nil {
		t.Fatal(err)
	}
	return &PoolContract{Pool: pool, Tuple: tuple, Admission: admission, Evidence: evidence}
}

func fixtureStorageIdentity(t *testing.T, dataPath string) (string, string) {
	t.Helper()
	if strings.Contains(dataPath, "://") {
		parsed, err := url.Parse(dataPath)
		if err != nil || parsed.Host == "" || parsed.Path == "" {
			t.Fatalf("invalid fixture object path %q", dataPath)
		}
		trimmed := strings.TrimRight(parsed.Path, "/")
		namespace := path.Base(trimmed)
		parsed.Path = path.Dir(trimmed)
		parsed.RawPath = ""
		return parsed.String(), namespace
	}
	absolute, err := filepath.Abs(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(absolute), filepath.Base(absolute)
}

func TestOpenRejectsSharedPoolWithoutAdmission(t *testing.T) {
	for _, config := range []Config{
		{RootDir: t.TempDir(), PhysicalPoolID: "unadmitted"},
		{RootDir: t.TempDir(), SharedPool: true},
	} {
		if _, err := Open(context.Background(), config); err == nil || !strings.Contains(err.Error(), "admission") {
			t.Fatalf("Open(%#v) = %v, want fail-closed admission error", config, err)
		}
	}
}

func TestOpenRejectsMismatchedAdmissionIdentity(t *testing.T) {
	contract := fixturePoolContract(t)
	if _, err := Open(context.Background(), Config{RootDir: t.TempDir(), PhysicalPoolID: "wrong-pool", PoolContract: contract}); err == nil {
		t.Fatal("Open accepted a mismatched physical-pool ID")
	}
	config := Config{RootDir: t.TempDir(), PoolContract: contract, Compatibility: contract.Tuple}
	config.Compatibility.ObjectNamingContract = "different:v1"
	if _, err := Open(context.Background(), config); err == nil {
		t.Fatal("Open accepted a mismatched compatibility tuple")
	}
}

func TestPoolContractRejectsArbitraryPhysicalPoolEvidence(t *testing.T) {
	contract := fixturePoolContract(t)
	evidence, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{
		Compatibility: contract.Tuple, ConformanceVersion: SharedPoolConformanceVersion,
		Checks: []physicalpool.EvidenceCheck{{ID: "arbitrary", Passed: true, ObservationDigest: digestBytesForTest([]byte("arbitrary"))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := contract.Pool.Admit(evidence)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := contract.Pool.ApplyAdmission(admission)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&PoolContract{Pool: pool, Tuple: contract.Tuple, Admission: admission, Evidence: evidence}).Validate(); err == nil {
		t.Fatal("PoolContract accepted arbitrary non-checklist evidence")
	}
	fullEmptyChecks := make([]physicalpool.EvidenceCheck, 0, len(SharedPoolConformanceChecks))
	for _, name := range SharedPoolConformanceChecks {
		fullEmptyChecks = append(fullEmptyChecks, physicalpool.EvidenceCheck{ID: name, Passed: true})
	}
	emptyEvidence, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{
		Compatibility: contract.Tuple, ConformanceVersion: SharedPoolConformanceVersion, Checks: fullEmptyChecks,
	})
	if err != nil {
		t.Fatal(err)
	}
	emptyAdmission, err := contract.Pool.Admit(emptyEvidence)
	if err != nil {
		t.Fatal(err)
	}
	emptyPool, err := contract.Pool.ApplyAdmission(emptyAdmission)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&PoolContract{Pool: emptyPool, Tuple: contract.Tuple, Admission: emptyAdmission, Evidence: emptyEvidence}).Validate(); err == nil {
		t.Fatal("PoolContract accepted full checklist evidence without observation digests")
	}
}

func TestOpenRejectsMismatchedAdmittedDataPath(t *testing.T) {
	root := t.TempDir()
	localContract := fixturePoolContractFor(t, "local", filepath.Join(root, "admitted-data"))
	if _, err := Open(context.Background(), Config{
		RootDir: root, DataPath: filepath.Join(root, "other-data"), PoolContract: localContract,
	}); err == nil {
		t.Fatal("Open accepted a local DATA_PATH outside the admitted namespace")
	}
	s3Contract := fixturePoolContractFor(t, "s3", "s3://bucket/admitted-data/tenant")
	if _, err := Open(context.Background(), Config{
		RootDir: t.TempDir(), DataPath: "s3://bucket/other-data/tenant", PoolContract: s3Contract,
	}); err == nil {
		t.Fatal("Open accepted an S3 DATA_PATH outside the admitted namespace")
	}
	localMismatch := filepath.Join(root, "other-data")
	err := localContract.ValidateDataPathBinding(localMismatch)
	if err == nil {
		t.Fatal("local DATA_PATH binding unexpectedly validated")
	}
	if strings.Contains(err.Error(), root) || strings.Contains(err.Error(), "other-data") {
		t.Fatalf("DATA_PATH binding error leaked storage path: %v", err)
	}
}

func TestCanonicalPhysicalPoolDataPathMapping(t *testing.T) {
	localData := filepath.Join(t.TempDir(), "pool-data")
	local := fixturePoolContractFor(t, "local", localData)
	got, err := local.Pool.DataPath()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(localData)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(want) {
		t.Fatalf("local physical-pool DATA_PATH = %q, want %q", got, filepath.Clean(want))
	}
	s3 := fixturePoolContractFor(t, "s3", "s3://Bucket/base/tenant")
	got, err = s3.Pool.DataPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != "s3://bucket/base/tenant" {
		t.Fatalf("S3 physical-pool DATA_PATH = %q, want canonical path", got)
	}
	identity := local.Pool.Identity
	identity.StorageLocation = "relative-pool"
	if _, err := physicalpool.NewPhysicalPool(identity); err == nil {
		t.Fatal("relative local storage location unexpectedly admitted")
	}
}
