//go:build ducklake_minio && duckdb_arrow

package ducklake

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/app/testing/extensionfixture"
	"github.com/flidai/leapview/internal/deployment/gcstore"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/log"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"
)

// This qualification lane starts an isolated S3-compatible object store. The
// test-owned credentials are passed only through CredentialBootstrap and are
// never persisted in Config or admission evidence.
func TestSharedPoolConformanceMinIOLane(t *testing.T) {
	ctx := context.Background()
	admission := extensionfixture.New(t, "ducklake")
	endpoint := startConformanceMinIO(t, ctx)
	bucket := "leapview-conformance"
	accessKey := "leapview"
	secretKey := "leapview-conformance-secret"
	client := conformanceMinIOClient(t, ctx, endpoint, accessKey, secretKey)
	if _, err := client.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatal(err)
	}

	secretEndpoint, useSSL, err := minioSecretEndpoint(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	var bootstrapCalls atomic.Int32
	bootstrap := func(ctx context.Context, execer driver.ExecerContext) error {
		if _, err := execer.ExecContext(ctx, "INSTALL httpfs FROM core", nil); err != nil {
			return err
		}
		if _, err := execer.ExecContext(ctx, "LOAD httpfs", nil); err != nil {
			return err
		}
		statement := fmt.Sprintf("CREATE OR REPLACE SECRET leapview_minio (TYPE S3, KEY_ID '%s', SECRET '%s', ENDPOINT '%s', URL_STYLE 'path', USE_SSL %t)",
			sqlLiteral(accessKey), sqlLiteral(secretKey), sqlLiteral(secretEndpoint), useSSL)
		_, err := execer.ExecContext(ctx, statement, nil)
		if err == nil {
			bootstrapCalls.Add(1)
		}
		return err
	}

	objectPrefix := strings.NewReplacer("/", "-", " ", "-").Replace(strings.ToLower(t.Name()))
	sharedData := "s3://" + strings.Trim(bucket, "/") + "/leapview-conformance/" + objectPrefix
	contract := minioConformancePoolContract(t, sharedData)
	objectPrefixPath := strings.TrimPrefix(sharedData, "s3://"+bucket+"/")
	config := func(root, catalog string) Config {
		return Config{
			RootDir: root, CatalogPath: catalog, DataPath: sharedData,
			PoolContract: contract, SharedPool: true, MaxConnections: 3, CredentialBootstrap: bootstrap,
			ExtensionAdmission: admission.Admission,
		}
	}

	baseRoot := t.TempDir()
	baseCatalog := filepath.Join(baseRoot, "catalog.duckdb")
	base, err := Open(ctx, config(baseRoot, baseCatalog))
	if extensionUnavailable(err) {
		if minioConformanceGateRequired() {
			t.Fatalf("ducklake extension unavailable in required MinIO conformance gate: %v", err)
		}
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	var duckdbRuntime, ducklakeExtension string
	if err := base.db.QueryRowContext(ctx, "SELECT version()").Scan(&duckdbRuntime); err != nil {
		_ = base.Close()
		t.Fatalf("query DuckDB runtime version: %v", err)
	}
	if err := base.db.QueryRowContext(ctx, "SELECT coalesce(max(extension_version), 'unknown') FROM duckdb_extensions() WHERE extension_name = 'ducklake'").Scan(&ducklakeExtension); err != nil {
		_ = base.Close()
		t.Fatalf("query DuckLake extension version: %v", err)
	}
	if duckdbRuntime != "v1.5.4" {
		_ = base.Close()
		t.Fatalf("DuckDB runtime=%q, want v1.5.4", duckdbRuntime)
	}
	if ducklakeExtension != "d318a545" {
		_ = base.Close()
		t.Fatalf("DuckLake extension=%q, want d318a545", ducklakeExtension)
	}
	t.Logf("MinIO conformance runtime: duckdb=%q ducklake=%q minio_image=%q", duckdbRuntime, ducklakeExtension, conformanceMinIOImage)
	if _, err := base.Commit(ctx, "minio-base", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS model;
CREATE TABLE model.orders(id BIGINT, value VARCHAR);
CREATE TABLE model.metrics(id BIGINT, value VARCHAR);
INSERT INTO model.orders SELECT range, 'base' FROM range(1, 1001);
INSERT INTO model.metrics SELECT range, 'base' FROM range(1, 1001);`)
		return err
	}); err != nil {
		base.Close()
		t.Fatal(err)
	}
	if err := base.Exec(ctx, "CALL ducklake_set_option('lake', 'rewrite_delete_threshold', 1.0, schema => 'model', table_name => 'orders')"); err != nil {
		base.Close()
		t.Fatal(err)
	}
	baseOrders, err := base.CurrentFileSet(ctx, "base", "model", "orders")
	if err != nil {
		base.Close()
		t.Fatal(err)
	}
	baseMetrics, err := base.CurrentFileSet(ctx, "base", "model", "metrics")
	if err != nil {
		base.Close()
		t.Fatal(err)
	}
	if err := base.Close(); err != nil {
		t.Fatal(err)
	}
	baseBytes, err := os.ReadFile(baseCatalog)
	if err != nil {
		t.Fatal(err)
	}
	copyCatalog := func(path string) error { return os.WriteFile(path, baseBytes, 0o600) }
	aRoot, bRoot := t.TempDir(), t.TempDir()
	aCatalog, bCatalog := filepath.Join(aRoot, "catalog.duckdb"), filepath.Join(bRoot, "catalog.duckdb")
	if err := copyCatalog(aCatalog); err != nil {
		t.Fatal(err)
	}
	if err := copyCatalog(bCatalog); err != nil {
		t.Fatal(err)
	}
	a, err := Open(ctx, config(aRoot, aCatalog))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	b, err := Open(ctx, config(bRoot, bCatalog))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()

	var writes sync.WaitGroup
	writes.Add(2)
	writeErrors := make(chan error, 2)
	go func() {
		defer writes.Done()
		_, commitErr := a.Commit(ctx, "minio-a", nil, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO model.orders VALUES (2, 'a')")
			return err
		})
		writeErrors <- commitErr
	}()
	go func() {
		defer writes.Done()
		_, commitErr := b.Commit(ctx, "minio-b", nil, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO model.metrics VALUES (2, 'b')")
			return err
		})
		writeErrors <- commitErr
	}()
	writes.Wait()
	close(writeErrors)
	for writeErr := range writeErrors {
		if writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if _, err := a.Commit(ctx, "minio-a-delete", nil, func(tx *sql.Tx) error {
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
		t.Fatal("MinIO delete produced no delete_file reference")
	}
	if !containsAll(aMetrics.DataFiles, baseMetrics.DataFiles) || !containsAll(bOrders.DataFiles, baseOrders.DataFiles) {
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
	readonlyConfig := config(bRoot, bCatalog)
	readonlyConfig.ReadOnly = true
	b, err = Open(ctx, readonlyConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !b.ReadOnly() {
		t.Fatal("reopened sealed MinIO catalog did not report read-only status")
	}
	if err := b.Exec(ctx, "INSERT INTO model.metrics VALUES (2000, 'should-fail')"); !errors.Is(err, ErrReadOnlyEnvironment) {
		t.Fatalf("read-only MinIO INSERT error = %v, want ErrReadOnlyEnvironment", err)
	}
	if _, err := b.Commit(ctx, "read-only-write", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO model.metrics VALUES (2001, 'should-fail')")
		return err
	}); !errors.Is(err, ErrReadOnlyEnvironment) {
		t.Fatalf("read-only MinIO commit error = %v, want ErrReadOnlyEnvironment", err)
	}
	if err := b.CleanupOldFiles(ctx, true); !errors.Is(err, ErrReadOnlyEnvironment) {
		t.Fatalf("read-only MinIO maintenance error = %v, want ErrReadOnlyEnvironment", err)
	}
	// Expire an existing pooled connector and force a replacement after the
	// global configuration lock. The replacement must still run the target
	// bootstrap and reattach without exposing or persisting credentials.
	beforeReplacement := bootstrapCalls.Load()
	a.db.SetConnMaxLifetime(time.Nanosecond)
	time.Sleep(2 * time.Millisecond)
	for range 3 {
		connection, err := a.db.Conn(ctx)
		if err != nil {
			t.Fatalf("pooled connector replacement: %v", err)
		}
		if err := connection.PingContext(ctx); err != nil {
			connection.Close()
			t.Fatalf("replacement connector ping: %v", err)
		}
		if err := connection.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if got := bootstrapCalls.Load(); got <= beforeReplacement {
		t.Fatalf("credential bootstrap calls did not increase for replacement: before=%d after=%d", beforeReplacement, got)
	}
	liveCatalogs := []CatalogFileSet{aOrders, aMetrics, bOrders, bMetrics}
	live := CrossCatalogLiveFileUnion(liveCatalogs)
	objects := make([]PoolObject, 0, len(live)+1)
	for _, file := range live {
		objects = append(objects, PoolObject{Path: file.Path, Kind: file.Kind})
	}
	objects = append(objects, PoolObject{Path: "minio-orphan.parquet", Kind: DataFile})
	classified := ClassifyPoolObjects(objects, liveCatalogs)
	orphanFound := false
	for _, file := range classified {
		if file.Path == "minio-orphan.parquet" {
			orphanFound = true
			if file.Live {
				t.Fatal("MinIO orphan was marked live")
			}
		}
	}
	if !orphanFound {
		t.Fatal("MinIO orphan was not classified")
	}
	orphanKey := objectPrefixPath + "/minio-orphan.parquet"
	if _, err := client.PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(orphanKey), Body: bytes.NewReader(nil)}); err != nil {
		t.Fatal(err)
	}
	listed, err := client.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String(objectPrefixPath)})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Contents) == 0 {
		t.Fatal("MinIO object listing returned no conformance files")
	}
	listedOrphan := false
	for _, object := range listed.Contents {
		if object.Key != nil && *object.Key == orphanKey {
			listedOrphan = true
			break
		}
	}
	if !listedOrphan {
		t.Fatalf("MinIO object listing did not include orphan key %q", orphanKey)
	}
	compatibility := contract.Tuple
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
			return []byte(fmt.Sprintf("orders=%d;peer=%d", writerCount, peerCount)), nil
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
			return []byte(fmt.Sprintf("metrics=%d;peer=%d", writerCount, peerCount)), nil
		},
		"unchanged_file_reference_reuse": func(context.Context) ([]byte, error) {
			if !containsAll(aMetrics.DataFiles, baseMetrics.DataFiles) || !containsAll(bOrders.DataFiles, baseOrders.DataFiles) {
				return nil, errors.New("inherited data-file references are missing")
			}
			return []byte(strings.Join(baseMetrics.DataFiles, "\x00")), nil
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
			if _, err := a.Commit(ctx, "minio-abort", nil, func(tx *sql.Tx) error {
				if _, execErr := tx.ExecContext(ctx, "INSERT INTO model.orders VALUES (99, 'aborted')"); execErr != nil {
					return execErr
				}
				return errors.New("minio fixture abort")
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
			return []byte(fmt.Sprintf("live=%d", len(live))), nil
		},
		"cross_catalog_orphan_classification": func(context.Context) ([]byte, error) {
			for _, file := range classified {
				if file.Path == "minio-orphan.parquet" && file.Live {
					return nil, errors.New("orphan classified live")
				}
			}
			return []byte(fmt.Sprintf("objects=%d", len(classified))), nil
		},
		"sealed_catalog_read": func(ctx context.Context) ([]byte, error) {
			count, err := countTableRows(ctx, b, "model.metrics")
			if err != nil {
				return nil, err
			}
			if count != 1001 {
				return nil, fmt.Errorf("reopened sealed catalog metrics count=%d, want 1001", count)
			}
			return []byte(fmt.Sprintf("reopened-metrics=%d", count)), nil
		},
		"safe_close": func(ctx context.Context) ([]byte, error) {
			closeEnv, err := Open(ctx, config(t.TempDir(), filepath.Join(t.TempDir(), "catalog.duckdb")))
			if err != nil {
				return nil, err
			}
			if err := closeEnv.Close(); err != nil {
				return nil, err
			}
			if _, _, err := closeEnv.queryConnection(ctx); !errors.Is(err, ErrEnvironmentClosed) {
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
	// The initial contract is provisional: the runtime needs an admitted tuple
	// before it can execute the checks. Namespace deletion authority must bind
	// the final digest produced by those nine observed checks, not the
	// provisional placeholder observations.
	artifactEvidence := persistMinIOConformanceEvidence(t, evidence)
	finalContract := minioConformancePoolContractFromEvidence(t, sharedData, artifactEvidence)
	if finalContract.Pool.ID != contract.Pool.ID {
		t.Fatalf("MinIO conformance pool identity changed between provisional and observed evidence: provisional=%q final=%q", contract.Pool.ID, finalContract.Pool.ID)
	}
	if finalContract.Admission.EvidenceDigest != evidence.Digest {
		t.Fatalf("MinIO conformance admission digest=%q, want observed evidence digest %q", finalContract.Admission.EvidenceDigest, evidence.Digest)
	}
	exerciseMinIOGCStoreConformance(t, ctx, client, bucket, objectPrefixPath, finalContract)
}

func minioConformancePoolContract(t *testing.T, dataPath string) *PoolContract {
	t.Helper()
	tuple := physicalpool.Compatibility{
		DuckDBRuntime: "duckdb:v1.5.4", DuckLakeExtension: "ducklake:d318a545", CatalogFormat: "ducklake:v1",
		StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1",
	}
	checks := make([]physicalpool.EvidenceCheck, 0, len(SharedPoolConformanceChecks))
	for _, name := range SharedPoolConformanceChecks {
		checks = append(checks, physicalpool.EvidenceCheck{ID: name, Passed: true, ObservationDigest: digestBytesForTest([]byte("minio-conformance:" + name))})
	}
	evidence, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{Compatibility: tuple, ConformanceVersion: SharedPoolConformanceVersion, Checks: checks})
	if err != nil {
		t.Fatal(err)
	}
	return minioConformancePoolContractFromEvidence(t, dataPath, evidence)
}

func minioConformancePoolContractFromEvidence(t *testing.T, dataPath string, evidence physicalpool.Evidence) *PoolContract {
	t.Helper()
	tuple := evidence.Compatibility
	storageLocation, storageNamespace := fixtureStorageIdentity(t, dataPath)
	pool, err := physicalpool.NewPhysicalPool(physicalpool.PoolIdentity{
		StorageLocation: storageLocation, StorageNamespace: storageNamespace, Region: "us-east-1",
		IsolationBoundary: "minio-conformance", RetentionAuthority: "minio-conformance", Compatibility: tuple,
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

func persistMinIOConformanceEvidence(t *testing.T, evidence physicalpool.Evidence) physicalpool.Evidence {
	t.Helper()
	encoded, err := MarshalSharedPoolEvidence(evidence)
	if err != nil {
		t.Fatalf("marshal MinIO conformance evidence: %v", err)
		return physicalpool.Evidence{}
	}
	decoded, err := physicalpool.UnmarshalEvidenceArtifact(encoded)
	if err != nil {
		t.Fatalf("validate MinIO conformance evidence artifact: %v", err)
		return physicalpool.Evidence{}
	}
	if decoded.Digest != evidence.Digest {
		t.Fatalf("MinIO conformance evidence digest changed during validation: got %q want %q", decoded.Digest, evidence.Digest)
		return physicalpool.Evidence{}
	}
	path := strings.TrimSpace(os.Getenv("LEAPVIEW_CONFORMANCE_EVIDENCE_OUT"))
	if path == "" {
		if minioConformanceGateRequired() {
			t.Fatal("LEAPVIEW_CONFORMANCE_EVIDENCE_OUT is required in the MinIO conformance gate")
			return physicalpool.Evidence{}
		}
		return decoded
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create MinIO conformance evidence directory: %v", err)
		return physicalpool.Evidence{}
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("write MinIO conformance evidence: %v", err)
		return physicalpool.Evidence{}
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatalf("stat MinIO conformance evidence: %v", err)
		return physicalpool.Evidence{}
	} else if info.Size() == 0 {
		t.Fatal("MinIO conformance evidence is empty")
		return physicalpool.Evidence{}
	}
	t.Logf("MinIO conformance evidence: path=%q digest=%q bytes=%d", path, evidence.Digest, len(encoded))
	return decoded
}

func exerciseMinIOGCStoreConformance(t *testing.T, ctx context.Context, client *awss3.Client, bucket, prefix string, contract *PoolContract) {
	t.Helper()
	store, err := gcstore.NewS3(client, bucket, prefix)
	if err != nil {
		t.Fatalf("construct MinIO gcstore: %v", err)
	}
	claim := physicalpool.OwnershipClaim{
		PoolID:              contract.Pool.ID,
		CompatibilityDigest: contract.Admission.CompatibilityDigest,
		EvidenceDigest:      contract.Admission.EvidenceDigest,
		OwnerID:             "instance-minio-conformance",
	}
	if err := store.AcquireNamespaceOwnership(ctx, claim); err != nil {
		t.Fatalf("MinIO gcstore namespace ownership marker: %v", err)
	}
	markerKey := prefix + "/.leapview-pool-owner.json"
	markerHead, err := client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(markerKey)})
	if err != nil {
		t.Fatalf("read MinIO gcstore namespace ownership marker: %v", err)
	}
	if got := markerHead.Metadata["leapview-evidence-digest"]; got != contract.Evidence.Digest {
		t.Fatalf("MinIO gcstore marker evidence digest=%q, want final observed digest %q", got, contract.Evidence.Digest)
	}
	markerObject, err := client.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(markerKey)})
	if err != nil {
		t.Fatalf("read MinIO gcstore namespace ownership marker body: %v", err)
	}
	var marker struct {
		EvidenceDigest string `json:"evidence_digest"`
	}
	decodeErr := json.NewDecoder(markerObject.Body).Decode(&marker)
	closeErr := markerObject.Body.Close()
	if decodeErr != nil {
		t.Fatalf("decode MinIO gcstore namespace ownership marker: %v", decodeErr)
	}
	if closeErr != nil {
		t.Fatalf("close MinIO gcstore namespace ownership marker: %v", closeErr)
	}
	if marker.EvidenceDigest != contract.Evidence.Digest {
		t.Fatalf("MinIO gcstore marker body evidence digest=%q, want final observed digest %q", marker.EvidenceDigest, contract.Evidence.Digest)
	}
	if err := store.VerifyNamespaceOwnership(ctx, claim); err != nil {
		t.Fatalf("MinIO gcstore namespace ownership verification: %v", err)
	}
	conflict := claim
	conflict.OwnerID = "instance-minio-clone"
	if err := store.VerifyNamespaceOwnership(ctx, conflict); !errors.Is(err, physicalpool.ErrOwnershipConflict) {
		t.Fatalf("MinIO gcstore accepted a conflicting namespace owner: %v", err)
	}

	token, err := store.AcquireNamespaceDeletionLease(ctx, "instance-minio-conformance", time.Minute)
	if err != nil {
		t.Fatalf("MinIO gcstore deletion lease acquisition: %v", err)
	}
	if err := store.VerifyNamespaceDeletionLease(ctx, "instance-minio-conformance", token); err != nil {
		t.Fatalf("MinIO gcstore deletion lease verification: %v", err)
	}
	if _, err := store.AcquireNamespaceDeletionLease(ctx, "instance-minio-clone", time.Minute); !errors.Is(err, physicalpool.ErrDeletionLeaseConflict) {
		t.Fatalf("MinIO gcstore allowed a concurrent deletion lease: %v", err)
	}
	if err := store.VerifyNamespaceDeletionLease(ctx, "instance-minio-conformance", "wrong-token"); !errors.Is(err, physicalpool.ErrDeletionLeaseConflict) {
		t.Fatalf("MinIO gcstore accepted a forged deletion lease token: %v", err)
	}
	if err := store.ReleaseNamespaceDeletionLease(ctx, "instance-minio-conformance", token); err != nil {
		t.Fatalf("MinIO gcstore deletion lease release: %v", err)
	}
	if err := store.VerifyNamespaceDeletionLease(ctx, "instance-minio-conformance", token); !errors.Is(err, physicalpool.ErrDeletionLeaseConflict) {
		t.Fatalf("MinIO gcstore retained a released deletion lease: %v", err)
	}
	reacquired, err := store.AcquireNamespaceDeletionLease(ctx, "instance-minio-clone", time.Minute)
	if err != nil {
		t.Fatalf("MinIO gcstore did not release namespace deletion lease: %v", err)
	}
	if err := store.VerifyNamespaceDeletionLease(ctx, "instance-minio-clone", reacquired); err != nil {
		t.Fatalf("MinIO gcstore re-acquired deletion lease verification: %v", err)
	}
	if err := store.ReleaseNamespaceDeletionLease(ctx, "instance-minio-clone", reacquired); err != nil {
		t.Fatalf("MinIO gcstore re-acquired deletion lease release: %v", err)
	}
	t.Logf("MinIO gcstore conformance: namespace_owner=%q marker_verified=true conflicting_owner_rejected=true deletion_lease_verified=true forged_token_rejected=true release_verified=true", claim.OwnerID)
}

func minioConformanceGateRequired() bool {
	return strings.TrimSpace(os.Getenv("LEAPVIEW_MINIO_CONFORMANCE_REQUIRED")) != ""
}

func minioSecretEndpoint(raw string) (string, bool, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", false, fmt.Errorf("parse MinIO endpoint: %w", err)
	}
	if parsed.Scheme == "" {
		parsed, err = url.Parse("http://" + raw)
		if err != nil {
			return "", false, fmt.Errorf("parse MinIO endpoint: %w", err)
		}
	}
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false, fmt.Errorf("MinIO endpoint must be a host URL without credentials or query parameters")
	}
	return parsed.Host, strings.EqualFold(parsed.Scheme, "https"), nil
}

const (
	conformanceMinIOImage  = "minio/minio@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e"
	conformanceMinIOUser   = "leapview"
	conformanceMinIOSecret = "leapview-conformance-secret"
)

func startConformanceMinIO(t *testing.T, ctx context.Context) string {
	t.Helper()
	if os.Getenv("CI") == "" {
		testcontainers.SkipIfProviderIsNotHealthy(t)
	}
	container, err := tcminio.Run(ctx, conformanceMinIOImage,
		tcminio.WithUsername(conformanceMinIOUser), tcminio.WithPassword(conformanceMinIOSecret),
		testcontainers.WithLogger(log.TestLogger(t)))
	testcontainers.CleanupContainer(t, container)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return "http://" + strings.TrimRight(endpoint, "/")
}

func conformanceMinIOClient(t *testing.T, ctx context.Context, endpoint, user, secret string) *awss3.Client {
	t.Helper()
	config, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(user, secret, "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	return awss3.NewFromConfig(config, func(options *awss3.Options) {
		options.BaseEndpoint = aws.String(endpoint)
		options.UsePathStyle = true
	})
}
