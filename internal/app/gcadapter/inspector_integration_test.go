//go:build duckdb_arrow

package gcadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/deployment/gcstore"
)

func testDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func integrationContract(t *testing.T, dataPath string) *ducklake.PoolContract {
	t.Helper()
	tuple := physicalpool.Compatibility{DuckDBRuntime: "duckdb:test", DuckLakeExtension: "ducklake:test", CatalogFormat: "ducklake:v1", StorageImplementation: "local", ObjectNamingContract: "uuidv7:v1"}
	checks := make([]physicalpool.EvidenceCheck, 0, len(ducklake.SharedPoolConformanceChecks))
	for _, name := range ducklake.SharedPoolConformanceChecks {
		checks = append(checks, physicalpool.EvidenceCheck{ID: name, Passed: true, ObservationDigest: testDigest([]byte(name))})
	}
	pool, err := physicalpool.NewPhysicalPool(physicalpool.PoolIdentity{StorageLocation: filepath.Dir(dataPath), StorageNamespace: filepath.Base(dataPath), IsolationBoundary: "gc-test", RetentionAuthority: "gc-test", Compatibility: tuple})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{Compatibility: tuple, ConformanceVersion: ducklake.SharedPoolConformanceVersion, Checks: checks})
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
	return &ducklake.PoolContract{Pool: pool, Tuple: tuple, Admission: admission, Evidence: evidence}
}

func TestInspectorRealCatalogEnumeratesDataAndDeleteFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dataPath := filepath.Join(root, "data")
	contract := integrationContract(t, dataPath)
	writerRoot := t.TempDir()
	env, err := ducklake.Open(ctx, ducklake.Config{RootDir: writerRoot, DataPath: dataPath, PhysicalPoolID: contract.Pool.ID.String(), SharedPool: true, Compatibility: contract.Tuple, PoolContract: contract})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Exec(ctx, "CREATE SCHEMA model"); err != nil {
		t.Fatal(err)
	}
	if err := env.Exec(ctx, "CREATE TABLE model.orders (id INTEGER, amount INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if err := env.Exec(ctx, "CREATE TABLE model.customers (id INTEGER, name VARCHAR)"); err != nil {
		t.Fatal(err)
	}
	if err := env.Exec(ctx, "INSERT INTO model.orders VALUES (1, 10), (2, 20)"); err != nil {
		t.Fatal(err)
	}
	if err := env.Exec(ctx, "INSERT INTO model.customers VALUES (1, 'Ada'), (2, 'Linus')"); err != nil {
		t.Fatal(err)
	}
	if err := env.Exec(ctx, "DELETE FROM model.orders WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	// Raw writes intentionally create multiple snapshots. A sealed catalog is
	// normalized before it becomes a GC root; verify the inspector rejects the
	// unnormalized artifact, then expire all historical snapshots and inspect
	// the resulting one-snapshot closure.
	snapshots, err := env.Snapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) < 2 {
		t.Fatalf("fixture expected multiple pre-normalization snapshots, got %d", len(snapshots))
	}
	catalogPath := env.Path()
	if err := env.Close(); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataPath, "catalog.duckdb"), bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := gcstore.NewLocal(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	inspector := Inspector{Store: store, PoolContract: contract, StagingRoot: t.TempDir()}
	initialRoot := deployment.DeliveryRoot{PhysicalPoolID: contract.Pool.ID.String(), Kind: "published", SourceID: "generation", CatalogDigest: testDigest(bytes), ObjectKey: "catalog.duckdb"}
	if _, err := inspector.Inspect(ctx, initialRoot); err == nil {
		t.Fatal("inspector accepted unnormalized multi-snapshot catalog")
	}
	// Reopen the private catalog and retain only its latest snapshot, matching
	// the candidate qualification/normalization contract.
	env, err = ducklake.Open(ctx, ducklake.Config{RootDir: writerRoot, CatalogPath: catalogPath, DataPath: dataPath, PhysicalPoolID: contract.Pool.ID.String(), SharedPool: true, Compatibility: contract.Tuple, PoolContract: contract})
	if err != nil {
		t.Fatal(err)
	}
	var expire []int64
	for _, snapshot := range snapshots[:len(snapshots)-1] {
		expire = append(expire, snapshot.ID)
	}
	if err := env.ExpireSnapshots(ctx, expire, false); err != nil {
		_ = env.Close()
		t.Fatal(err)
	}
	if err := env.Close(); err != nil {
		t.Fatal(err)
	}
	bytes, err = os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataPath, "catalog.duckdb"), bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	reach, err := inspector.Inspect(ctx, deployment.DeliveryRoot{PhysicalPoolID: contract.Pool.ID.String(), Kind: "published", SourceID: "generation", CatalogDigest: testDigest(bytes), ObjectKey: "catalog.duckdb"})
	if err != nil {
		t.Fatal(err)
	}
	if len(reach.DataFiles) < 2 || len(reach.DeleteFiles) == 0 {
		t.Fatalf("reachability data=%v delete=%v; expected both DuckLake file columns", reach.DataFiles, reach.DeleteFiles)
	}
}

func TestInspectorRejectsCorruptCatalogDigest(t *testing.T) {
	root := t.TempDir()
	dataPath := filepath.Join(root, "data")
	contract := integrationContract(t, dataPath)
	if err := os.MkdirAll(dataPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataPath, "catalog.duckdb"), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := gcstore.NewLocal(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = (Inspector{Store: store, PoolContract: contract, StagingRoot: t.TempDir()}).Inspect(context.Background(), deployment.DeliveryRoot{PhysicalPoolID: contract.Pool.ID.String(), Kind: "published", SourceID: "generation", CatalogDigest: testDigest([]byte("different")), ObjectKey: "catalog.duckdb"})
	if err == nil {
		t.Fatal("corrupt rooted catalog accepted")
	}
}

func TestInspectorRejectsWrongPoolAndNonZeroInlining(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dataPath := filepath.Join(root, "data")
	contract := integrationContract(t, dataPath)
	writerRoot := t.TempDir()
	env, err := ducklake.Open(ctx, ducklake.Config{RootDir: writerRoot, DataPath: dataPath, PhysicalPoolID: contract.Pool.ID.String(), SharedPool: true, Compatibility: contract.Tuple, PoolContract: contract})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Exec(ctx, "CREATE SCHEMA model"); err != nil {
		t.Fatal(err)
	}
	if err := env.Exec(ctx, "CREATE TABLE model.inline_orders (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if err := env.Exec(ctx, "CALL ducklake_set_option('lake', 'data_inlining_row_limit', 100, schema => 'model', table_name => 'inline_orders')"); err != nil {
		t.Fatal(err)
	}
	catalogPath := env.Path()
	if err := env.Close(); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataPath, "catalog.duckdb"), bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := gcstore.NewLocal(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	inspector := Inspector{Store: store, PoolContract: contract, StagingRoot: t.TempDir()}
	rootRecord := deployment.DeliveryRoot{PhysicalPoolID: contract.Pool.ID.String(), Kind: "published", SourceID: "generation", CatalogDigest: testDigest(bytes), ObjectKey: "catalog.duckdb"}
	if _, err := inspector.Inspect(ctx, rootRecord); err == nil {
		t.Fatal("non-zero data inlining policy accepted")
	}
	rootRecord.PhysicalPoolID = "different-pool"
	if _, err := inspector.Inspect(ctx, rootRecord); err == nil {
		t.Fatal("wrong pool root accepted")
	}
}
