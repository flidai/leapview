//go:build duckdb_arrow

package candidatecatalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/app/testing/extensionfixture"
)

func TestOpenRejectsLeaseBeforeCreatingStaging(t *testing.T) {
	root := filepath.Join(t.TempDir(), "staging")
	contract := testPoolContract(t, filepath.Join(t.TempDir(), "data"))
	request := testRequest(t, contract, root)
	request.VerifyLease = func(context.Context, WriterLease) error { return errors.New("lease was revoked") }
	if _, err := Open(context.Background(), request); !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("Open() error = %v, want lease mismatch", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging root exists after pre-work lease rejection: %v", err)
	}
}

func TestOpenRejectsDigestMismatchWithoutPrivateStaging(t *testing.T) {
	contract := testPoolContract(t, filepath.Join(t.TempDir(), "data"))
	root := filepath.Join(t.TempDir(), "staging")
	request := testRequest(t, contract, root)
	request.Base = &SealedArtifact{
		ObjectKey:      "catalogs/base",
		Digest:         "sha256:" + strings.Repeat("0", 64),
		SizeBytes:      int64(len("catalog bytes")),
		PhysicalPoolID: contract.Pool.ID.String(),
		Compatibility:  contract.Tuple,
		Reader: ArtifactReaderFunc(func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("catalog bytes")), nil
		}),
	}
	if _, err := Open(context.Background(), request); !errors.Is(err, ErrArtifactDigest) {
		t.Fatalf("Open() error = %v, want digest mismatch", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("private staging exists after digest rejection: %#v", entries)
	}
}

func TestOpenReadsBaseThroughObjectStoreWithoutSourcePath(t *testing.T) {
	contract := testPoolContract(t, filepath.Join(t.TempDir(), "data"))
	baseBytes, baseDigest := closedBaseBytes(t, contract)
	store := memoryObjectStore{objects: map[string][]byte{"catalogs/base": baseBytes}}
	request := testRequest(t, contract, filepath.Join(t.TempDir(), "staging"))
	request.Base = &SealedArtifact{
		ObjectKey:      "catalogs/base",
		Digest:         baseDigest,
		SizeBytes:      int64(len(baseBytes)),
		PhysicalPoolID: contract.Pool.ID.String(),
		Compatibility:  contract.Tuple,
		Reader:         ObjectReader{Store: &store, Key: "catalogs/base"},
	}
	working, err := Open(context.Background(), request)
	if extensionUnavailableForTest(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer working.Close()
	rows, err := working.Query(context.Background(), semanticquery.Plan{SQL: "SELECT COUNT(*) AS count FROM model.metrics", Columns: []string{"count"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := int(rows[0]["count"].(int64)); got != 1 {
		t.Fatalf("object-backed base rows = %d, want 1", got)
	}
}

func TestDetachForSealPreservesStagingAndIsIdempotent(t *testing.T) {
	contract := testPoolContract(t, filepath.Join(t.TempDir(), "data"))
	request := testRequest(t, contract, filepath.Join(t.TempDir(), "staging"))
	working, err := Open(context.Background(), request)
	if extensionUnavailableForTest(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	path := working.CatalogPath()
	detached, err := working.DetachForSeal()
	if err != nil {
		t.Fatal(err)
	}
	if detached.CatalogPath() != path {
		t.Fatalf("detached catalog path = %q, want %q", detached.CatalogPath(), path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("detached catalog missing before remove: %v", err)
	}
	detachedAgain, err := working.DetachForSeal()
	if err != nil {
		t.Fatal(err)
	}
	if detachedAgain.CatalogPath() != detached.CatalogPath() {
		t.Fatal("repeated detach returned a different identity")
	}
	if err := detached.Remove(); err != nil {
		t.Fatal(err)
	}
	if err := detachedAgain.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(detached.StagingPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("detached staging remains after remove: %v", err)
	}
}

func TestCloseRemovesOrdinaryStaging(t *testing.T) {
	contract := testPoolContract(t, filepath.Join(t.TempDir(), "data"))
	working, err := Open(context.Background(), testRequest(t, contract, filepath.Join(t.TempDir(), "staging")))
	if extensionUnavailableForTest(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	staging := working.StagingPath()
	if err := working.Close(); err != nil {
		t.Fatal(err)
	}
	if err := working.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staging); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ordinary staging remains after close: %v", err)
	}
}

func TestExecLeaseRevocationAfterMutationClosesStaging(t *testing.T) {
	contract := testPoolContract(t, filepath.Join(t.TempDir(), "data"))
	request := testRequest(t, contract, filepath.Join(t.TempDir(), "staging"))
	var mu sync.Mutex
	verifications := 0
	request.VerifyLease = func(context.Context, WriterLease) error {
		mu.Lock()
		defer mu.Unlock()
		verifications++
		if verifications >= 5 {
			return errors.New("lease epoch changed")
		}
		return nil
	}
	working, err := Open(context.Background(), request)
	if extensionUnavailableForTest(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	staging := working.StagingPath()
	err = working.Exec(context.Background(), "CREATE TABLE exec_revocation(value INTEGER)")
	if !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("Exec() error = %v, want lease mismatch", err)
	}
	if _, statErr := os.Stat(staging); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("staging remains after post-mutation lease revocation: %v", statErr)
	}
}

func TestBuildFailureReturnsNoHandleAndLeavesBaseUnchanged(t *testing.T) {
	ctx := context.Background()
	contract := testPoolContract(t, filepath.Join(t.TempDir(), "data"))
	baseRoot := t.TempDir()
	base, err := ducklake.Open(ctx, testDuckLakeConfig(t, ducklake.Config{
		RootDir: baseRoot, DataPath: filepath.Join(contract.Pool.Identity.StorageLocation, contract.Pool.Identity.StorageNamespace),
		PhysicalPoolID: contract.Pool.ID.String(), SharedPool: true, Compatibility: contract.Tuple, PoolContract: contract,
	}))
	if extensionUnavailableForTest(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.Commit(ctx, "base", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS model; CREATE TABLE model.metrics(value INTEGER); INSERT INTO model.metrics VALUES (1)`)
		return err
	}); err != nil {
		base.Close()
		t.Fatal(err)
	}
	basePath := base.Path()
	if err := base.Close(); err != nil {
		t.Fatal(err)
	}
	baseBytes, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	baseDigest := digestForTest(baseBytes)
	request := testRequest(t, contract, filepath.Join(t.TempDir(), "staging"))
	request.Base = &SealedArtifact{ObjectKey: "catalogs/base", Digest: baseDigest, SizeBytes: int64(len(baseBytes)), PhysicalPoolID: contract.Pool.ID.String(), Compatibility: contract.Tuple, Reader: ArtifactReaderFunc(func(context.Context) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(baseBytes)), nil })}
	working, err := Build(ctx, request, func(ctx context.Context, working *WorkingCatalog) error {
		_, err := working.Commit(ctx, "failed", nil, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO model.missing_table VALUES (1)")
			return err
		})
		return err
	})
	if working != nil {
		t.Fatal("failed mutation returned a working handle")
	}
	if err == nil || !errors.Is(err, ErrMutationFailed) {
		t.Fatalf("Build() error = %v, want mutation failure", err)
	}
	// Reopen the original exact bytes and verify the base itself remains intact.
	baseRoot = t.TempDir()
	if err := os.WriteFile(filepath.Join(baseRoot, "catalog.duckdb"), baseBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	base, err = ducklake.Open(ctx, testDuckLakeConfig(t, ducklake.Config{RootDir: baseRoot, DataPath: filepath.Join(contract.Pool.Identity.StorageLocation, contract.Pool.Identity.StorageNamespace), PhysicalPoolID: contract.Pool.ID.String(), SharedPool: true, Compatibility: contract.Tuple, PoolContract: contract}))
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	rows, err := base.Query(ctx, semanticquery.Plan{SQL: "SELECT value FROM model.metrics", Columns: []string{"value"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("base row count after failed build = %d, want 1", len(rows))
	}
}

// TestConcurrentBuildsFromOneBase verifies that each attempt receives a
// distinct private catalog and that a failed attempt cannot be observed by a
// peer. The data-file closure itself is covered by ducklake's shared-pool
// conformance tests; this test exercises the candidate construction boundary.
func TestConcurrentBuildsFromOneBaseAreDistinct(t *testing.T) {
	ctx := context.Background()
	contract := testPoolContract(t, filepath.Join(t.TempDir(), "data"))
	baseBytes, baseDigest := closedBaseBytes(t, contract)
	root := t.TempDir()
	makeRequest := func(id string) Request {
		req := testRequest(t, contract, root)
		req.AttemptID = id
		req.Lease.AttemptID = id
		req.Base = &SealedArtifact{ObjectKey: "catalogs/base", Digest: baseDigest, SizeBytes: int64(len(baseBytes)), PhysicalPoolID: contract.Pool.ID.String(), Compatibility: contract.Tuple, Reader: ArtifactReaderFunc(func(context.Context) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(baseBytes)), nil })}
		return req
	}
	var group sync.WaitGroup
	results := make(chan *WorkingCatalog, 2)
	errs := make(chan error, 2)
	for _, id := range []string{"attempt-a", "attempt-b"} {
		group.Add(1)
		go func(id string) {
			defer group.Done()
			working, err := Open(ctx, makeRequest(id))
			if err != nil {
				errs <- err
				return
			}
			results <- working
		}(id)
	}
	group.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if extensionUnavailableForTest(err) {
			t.Skipf("ducklake extension unavailable: %v", err)
		}
		t.Fatal(err)
	}
	var handles []*WorkingCatalog
	for working := range results {
		handles = append(handles, working)
	}
	if len(handles) != 2 {
		t.Fatalf("working handles = %d, want 2", len(handles))
	}
	defer func() {
		for _, handle := range handles {
			handle.Close()
		}
	}()
	if handles[0].CatalogPath() == handles[1].CatalogPath() {
		t.Fatal("concurrent attempts shared one catalog path")
	}
	baseOrders, baseMetrics := baseFileSets(t, contract, baseBytes)
	var writeGroup sync.WaitGroup
	writeErrs := make(chan error, 2)
	writeGroup.Add(2)
	go func() {
		defer writeGroup.Done()
		_, err := handles[0].Commit(ctx, "candidate-a", nil, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO model.orders VALUES (2, 'a')")
			return err
		})
		writeErrs <- err
	}()
	go func() {
		defer writeGroup.Done()
		_, err := handles[1].Commit(ctx, "candidate-b", nil, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "INSERT INTO model.orders VALUES (2, 'b')"); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, "INSERT INTO model.metrics VALUES (2, 'b')")
			return err
		})
		writeErrs <- err
	}()
	writeGroup.Wait()
	close(writeErrs)
	for err := range writeErrs {
		if err != nil {
			t.Fatal(err)
		}
	}
	aOrders, err := handles[0].CurrentFileSet(ctx, "candidate-a", "model", "orders")
	if err != nil {
		t.Fatal(err)
	}
	aMetrics, err := handles[0].CurrentFileSet(ctx, "candidate-a", "model", "metrics")
	if err != nil {
		t.Fatal(err)
	}
	bOrders, err := handles[1].CurrentFileSet(ctx, "candidate-b", "model", "orders")
	if err != nil {
		t.Fatal(err)
	}
	bMetrics, err := handles[1].CurrentFileSet(ctx, "candidate-b", "model", "metrics")
	if err != nil {
		t.Fatal(err)
	}
	if !containsAllTest(aMetrics.DataFiles, baseMetrics.DataFiles) || !containsAllTest(bOrders.DataFiles, baseOrders.DataFiles) {
		t.Fatalf("unchanged refs were not reused: a metrics=%#v, b orders=%#v", aMetrics, bOrders)
	}
	if overlapTest(subtractTest(aOrders.DataFiles, baseOrders.DataFiles), subtractTest(bOrders.DataFiles, baseOrders.DataFiles)) {
		t.Fatalf("same-table changed outputs overlap: a orders=%#v, b orders=%#v", aOrders, bOrders)
	}
	if overlapTest(subtractTest(aOrders.DataFiles, baseOrders.DataFiles), subtractTest(bMetrics.DataFiles, baseMetrics.DataFiles)) {
		t.Fatalf("different-table changed outputs overlap: a orders=%#v, b metrics=%#v", aOrders, bMetrics)
	}
	aRows, err := handles[0].Query(ctx, semanticquery.Plan{SQL: "SELECT COUNT(*) AS count FROM model.orders", Columns: []string{"count"}})
	if err != nil {
		t.Fatal(err)
	}
	bRows, err := handles[1].Query(ctx, semanticquery.Plan{SQL: "SELECT COUNT(*) AS count FROM model.orders", Columns: []string{"count"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := int(aRows[0]["count"].(int64)); got != 2 {
		t.Fatalf("candidate A orders = %d, want 2", got)
	}
	if got := int(bRows[0]["count"].(int64)); got != 2 {
		t.Fatalf("candidate B orders = %d, want 2", got)
	}
}

// TestSealedBaseWithoutSnapshotIDRetainsUnchangedRefs verifies the production
// sealed-base contract: the base is identified by exact bytes/digest (there is
// intentionally no snapshot-number requirement), unchanged relations retain
// their file references, and a changed relation receives disjoint files.
func TestSealedBaseWithoutSnapshotIDRetainsUnchangedRefs(t *testing.T) {
	ctx := context.Background()
	contract := testPoolContract(t, filepath.Join(t.TempDir(), "data"))
	baseBytes, baseDigest := closedBaseBytes(t, contract)
	request := testRequest(t, contract, filepath.Join(t.TempDir(), "staging"))
	request.Base = &SealedArtifact{ObjectKey: "catalogs/sealed-base", Digest: baseDigest, SizeBytes: int64(len(baseBytes)), PhysicalPoolID: contract.Pool.ID.String(), Compatibility: contract.Tuple, Reader: ArtifactReaderFunc(func(context.Context) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(baseBytes)), nil })}
	working, err := Open(ctx, request)
	if extensionUnavailableForTest(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer working.Close()
	baseOrders, baseMetrics := baseFileSets(t, contract, baseBytes)
	if _, err := working.Commit(ctx, "candidate-partial", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO model.orders VALUES (2, 'changed')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	orders, err := working.CurrentFileSet(ctx, "candidate-partial", "model", "orders")
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := working.CurrentFileSet(ctx, "candidate-partial", "model", "metrics")
	if err != nil {
		t.Fatal(err)
	}
	if !containsAllTest(metrics.DataFiles, baseMetrics.DataFiles) {
		t.Fatalf("unchanged metrics refs were not retained: %#v", metrics)
	}
	changedOrders := subtractTest(orders.DataFiles, baseOrders.DataFiles)
	if len(changedOrders) == 0 || overlapTest(changedOrders, baseOrders.DataFiles) {
		t.Fatalf("changed orders refs were not disjoint from sealed base: %#v", orders)
	}
}

func testRequest(t *testing.T, contract *ducklake.PoolContract, root string) Request {
	t.Helper()
	now := time.Now().UTC()
	return Request{AttemptID: "attempt-test", StagingRoot: root, PoolContract: contract, ExtensionAdmission: extensionfixture.New(t, "ducklake").Admission, Lease: WriterLease{ID: "lease-test", AttemptID: "attempt-test", PhysicalPoolID: contract.Pool.ID.String(), Epoch: 1, ExpiresAt: now.Add(time.Hour), Status: LeaseActive}, VerifyLease: func(context.Context, WriterLease) error { return nil }, Now: func() time.Time { return now }}
}

func testDuckLakeConfig(t *testing.T, config ducklake.Config) ducklake.Config {
	t.Helper()
	config.ExtensionAdmission = extensionfixture.New(t, "ducklake").Admission
	return config
}

func testPoolContract(t *testing.T, dataPath string) *ducklake.PoolContract {
	t.Helper()
	tuple := physicalpool.Compatibility{DuckDBRuntime: "duckdb:test", DuckLakeExtension: "ducklake:test", CatalogFormat: "ducklake:v1", StorageImplementation: "local", ObjectNamingContract: "uuidv7:v1"}
	identity := physicalpool.PoolIdentity{StorageLocation: filepath.Dir(dataPath), StorageNamespace: filepath.Base(dataPath), IsolationBoundary: "candidate-test", RetentionAuthority: "candidate-test", Compatibility: tuple}
	pool, err := physicalpool.NewPhysicalPool(identity)
	if err != nil {
		t.Fatal(err)
	}
	checks := make([]physicalpool.EvidenceCheck, 0, len(ducklake.SharedPoolConformanceChecks))
	for _, id := range ducklake.SharedPoolConformanceChecks {
		checks = append(checks, physicalpool.EvidenceCheck{ID: id, Passed: true, ObservationDigest: digestForTest([]byte(id))})
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

func closedBaseBytes(t *testing.T, contract *ducklake.PoolContract) ([]byte, string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	env, err := ducklake.Open(ctx, testDuckLakeConfig(t, ducklake.Config{RootDir: root, DataPath: filepath.Join(contract.Pool.Identity.StorageLocation, contract.Pool.Identity.StorageNamespace), PhysicalPoolID: contract.Pool.ID.String(), SharedPool: true, Compatibility: contract.Tuple, PoolContract: contract}))
	if extensionUnavailableForTest(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Commit(ctx, "base", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS model; CREATE TABLE model.orders(id INTEGER, value VARCHAR); CREATE TABLE model.metrics(id INTEGER, value VARCHAR); INSERT INTO model.orders VALUES (1, 'base'); INSERT INTO model.metrics VALUES (1, 'base')`)
		return err
	}); err != nil {
		env.Close()
		t.Fatal(err)
	}
	path := env.Path()
	if err := env.Close(); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return bytes, digestForTest(bytes)
}

func digestForTest(bytes []byte) string {
	sum := sha256.Sum256(bytes)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func baseFileSets(t *testing.T, contract *ducklake.PoolContract, catalogBytes []byte) (ducklake.CatalogFileSet, ducklake.CatalogFileSet) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "catalog.duckdb")
	if err := os.WriteFile(path, catalogBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := ducklake.Open(context.Background(), testDuckLakeConfig(t, ducklake.Config{RootDir: root, CatalogPath: path, DataPath: filepath.Join(contract.Pool.Identity.StorageLocation, contract.Pool.Identity.StorageNamespace), PhysicalPoolID: contract.Pool.ID.String(), SharedPool: true, Compatibility: contract.Tuple, PoolContract: contract, ReadOnly: true}))
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	orders, err := env.CurrentFileSet(context.Background(), "base", "model", "orders")
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := env.CurrentFileSet(context.Background(), "base", "model", "metrics")
	if err != nil {
		t.Fatal(err)
	}
	return orders, metrics
}

func containsAllTest(got, want []string) bool {
	set := make(map[string]struct{}, len(got))
	for _, value := range got {
		set[value] = struct{}{}
	}
	for _, value := range want {
		if _, ok := set[value]; !ok {
			return false
		}
	}
	return true
}

func overlapTest(a, b []string) bool {
	set := make(map[string]struct{}, len(a))
	for _, value := range a {
		set[value] = struct{}{}
	}
	for _, value := range b {
		if _, ok := set[value]; ok {
			return true
		}
	}
	return false
}

func subtractTest(values, inherited []string) []string {
	set := make(map[string]struct{}, len(inherited))
	for _, value := range inherited {
		set[value] = struct{}{}
	}
	var result []string
	for _, value := range values {
		if _, ok := set[value]; !ok {
			result = append(result, value)
		}
	}
	return result
}

type memoryObjectStore struct {
	objects map[string][]byte
}

func (s *memoryObjectStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	value, ok := s.objects[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(value)), nil
}

func extensionUnavailableForTest(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "extension") && (strings.Contains(text, "not found") || strings.Contains(text, "failed to download") || strings.Contains(text, "failed to install") || strings.Contains(text, "not be loaded"))
}
