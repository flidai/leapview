//go:build ducklake_minio && duckdb_arrow

package candidatecatalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/app/testing/extensionfixture"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/log"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"
)

// TestCandidateCatalogMinIOSealedBaseRetainsUnchangedRefs exercises the
// production candidatecatalog path against an object-backed DuckLake pool.
// The sealed catalog is fetched through an S3 object reader, then one table is
// changed while an untouched table retains the exact inherited references.
func TestCandidateCatalogMinIOSealedBaseRetainsUnchangedRefs(t *testing.T) {
	ctx := context.Background()
	admission := extensionfixture.New(t, "ducklake")
	endpoint := startCandidateMinIO(t, ctx)
	const bucket = "leapview-candidatecatalog"
	const accessKey = "leapview"
	const secretKey = "leapview-conformance-secret"
	client := candidateMinIOClient(t, ctx, endpoint, accessKey, secretKey)
	if _, err := client.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatal(err)
	}

	secretEndpoint, useSSL, err := candidateSecretEndpoint(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := func(ctx context.Context, execer driver.ExecerContext) error {
		if _, err := execer.ExecContext(ctx, "INSTALL httpfs FROM core", nil); err != nil {
			return err
		}
		if _, err := execer.ExecContext(ctx, "LOAD httpfs", nil); err != nil {
			return err
		}
		_, err := execer.ExecContext(ctx, fmt.Sprintf("CREATE OR REPLACE SECRET candidate_minio (TYPE S3, KEY_ID '%s', SECRET '%s', ENDPOINT '%s', URL_STYLE 'path', USE_SSL %t)", sqlLiteralCandidate(accessKey), sqlLiteralCandidate(secretKey), sqlLiteralCandidate(secretEndpoint), useSSL), nil)
		return err
	}
	prefix := strings.NewReplacer("/", "-", " ", "-").Replace(strings.ToLower(t.Name()))
	dataPath := "s3://" + bucket + "/" + prefix
	contract := candidateMinIOPoolContract(t, bucket, prefix)

	baseRoot := t.TempDir()
	base, err := ducklake.Open(ctx, ducklake.Config{RootDir: baseRoot, DataPath: dataPath, PhysicalPoolID: contract.Pool.ID.String(), SharedPool: true, Compatibility: contract.Tuple, PoolContract: contract, ExtensionAdmission: admission.Admission, CredentialBootstrap: bootstrap})
	if extensionUnavailableCandidate(err) {
		if minioConformanceGateRequired() {
			t.Fatalf("ducklake extension unavailable in required MinIO conformance gate: %v", err)
		}
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.Commit(ctx, "base", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS model; CREATE TABLE model.orders(id INTEGER, value VARCHAR); CREATE TABLE model.metrics(id INTEGER, value VARCHAR); INSERT INTO model.orders VALUES (1, 'base'); INSERT INTO model.metrics VALUES (1, 'base')`)
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
	baseDigest := candidateDigest(baseBytes)
	const catalogKey = "catalogs/sealed-base"
	if _, err := client.PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(catalogKey), Body: bytes.NewReader(baseBytes)}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	working, err := Open(ctx, Request{
		AttemptID: "attempt-minio", StagingRoot: t.TempDir(), PoolContract: contract, ExtensionAdmission: admission.Admission, CredentialBootstrap: bootstrap,
		Lease:       WriterLease{ID: "lease-minio", AttemptID: "attempt-minio", PhysicalPoolID: contract.Pool.ID.String(), Epoch: 1, ExpiresAt: now.Add(time.Hour), Status: LeaseActive},
		VerifyLease: func(context.Context, WriterLease) error { return nil },
		Base:        &SealedArtifact{ObjectKey: catalogKey, Digest: baseDigest, SizeBytes: int64(len(baseBytes)), PhysicalPoolID: contract.Pool.ID.String(), Compatibility: contract.Tuple, Reader: ObjectReader{Store: candidateMinIOObjectStore{client: client, bucket: bucket}, Key: catalogKey}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer working.Close()
	baseMetrics, err := working.CurrentFileSet(ctx, "base", "model", "metrics")
	if err != nil {
		t.Fatal(err)
	}
	baseOrders, err := working.CurrentFileSet(ctx, "base", "model", "orders")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := working.Commit(ctx, "candidate-minio", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO model.orders VALUES (2, 'changed')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	metrics, err := working.CurrentFileSet(ctx, "candidate-minio", "model", "metrics")
	if err != nil {
		t.Fatal(err)
	}
	orders, err := working.CurrentFileSet(ctx, "candidate-minio", "model", "orders")
	if err != nil {
		t.Fatal(err)
	}
	if !containsAllCandidate(metrics.DataFiles, baseMetrics.DataFiles) {
		t.Fatalf("MinIO unchanged metrics refs were not retained: base=%#v candidate=%#v", baseMetrics, metrics)
	}
	changed := subtractCandidate(orders.DataFiles, baseOrders.DataFiles)
	if len(changed) == 0 || overlapsCandidate(changed, baseOrders.DataFiles) {
		t.Fatalf("MinIO changed orders refs were not disjoint: base=%#v candidate=%#v", baseOrders, orders)
	}
}

type candidateMinIOObjectStore struct {
	client *awss3.Client
	bucket string
}

func (s candidateMinIOObjectStore) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	output, err := s.client.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	return output.Body, nil
}

func candidateMinIOPoolContract(t *testing.T, bucket, prefix string) *ducklake.PoolContract {
	t.Helper()
	tuple := physicalpool.Compatibility{DuckDBRuntime: "duckdb:v1.5.4", DuckLakeExtension: "ducklake:d318a545", CatalogFormat: "ducklake:v1", StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1"}
	identity := physicalpool.PoolIdentity{StorageLocation: "s3://" + bucket, StorageNamespace: prefix, Region: "us-east-1", IsolationBoundary: "candidate-minio", RetentionAuthority: "candidate-minio", Compatibility: tuple}
	pool, err := physicalpool.NewPhysicalPool(identity)
	if err != nil {
		t.Fatal(err)
	}
	checks := make([]physicalpool.EvidenceCheck, 0, len(ducklake.SharedPoolConformanceChecks))
	for _, id := range ducklake.SharedPoolConformanceChecks {
		checks = append(checks, physicalpool.EvidenceCheck{ID: id, Passed: true, ObservationDigest: candidateDigest([]byte(id))})
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

func startCandidateMinIO(t *testing.T, ctx context.Context) string {
	t.Helper()
	if os.Getenv("CI") == "" {
		testcontainers.SkipIfProviderIsNotHealthy(t)
	}
	container, err := tcminio.Run(ctx, "minio/minio@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e", tcminio.WithUsername("leapview"), tcminio.WithPassword("leapview-conformance-secret"), testcontainers.WithLogger(log.TestLogger(t)))
	testcontainers.CleanupContainer(t, container)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return "http://" + strings.TrimRight(connection, "/")
}

func candidateMinIOClient(t *testing.T, ctx context.Context, endpoint, user, secret string) *awss3.Client {
	t.Helper()
	config, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-east-1"), awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(user, secret, "")))
	if err != nil {
		t.Fatal(err)
	}
	return awss3.NewFromConfig(config, func(options *awss3.Options) { options.BaseEndpoint = aws.String(endpoint); options.UsePathStyle = true })
}

func candidateSecretEndpoint(raw string) (string, bool, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false, fmt.Errorf("invalid MinIO endpoint")
	}
	return parsed.Host, strings.EqualFold(parsed.Scheme, "https"), nil
}

func sqlLiteralCandidate(value string) string { return strings.ReplaceAll(value, "'", "''") }

func candidateDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func containsAllCandidate(got, want []string) bool {
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

func subtractCandidate(values, inherited []string) []string {
	set := make(map[string]struct{}, len(inherited))
	for _, value := range inherited {
		set[value] = struct{}{}
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := set[value]; !ok {
			result = append(result, value)
		}
	}
	return result
}

func overlapsCandidate(left, right []string) bool {
	set := make(map[string]struct{}, len(left))
	for _, value := range left {
		set[value] = struct{}{}
	}
	for _, value := range right {
		if _, ok := set[value]; ok {
			return true
		}
	}
	return false
}

func extensionUnavailableCandidate(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "extension") && (strings.Contains(text, "not found") || strings.Contains(text, "failed to download") || strings.Contains(text, "failed to install") || strings.Contains(text, "not be loaded"))
}

func minioConformanceGateRequired() bool {
	return strings.TrimSpace(os.Getenv("LEAPVIEW_MINIO_CONFORMANCE_REQUIRED")) != ""
}
