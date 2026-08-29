//go:build ducklake_minio

package l3_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	analyticsl3 "github.com/flidai/leapview/internal/analytics/cache/l3"
	cachepostgres "github.com/flidai/leapview/internal/analytics/cache/postgres"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/app/gcadapter"
	"github.com/flidai/leapview/internal/app/runtimefactory"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/log"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"
)

// TestTargetL3CacheMinIOPostgresComposition exercises the production target
// constructor with a real SQL authority and the real object adapter. Deleting
// the exact object retires only its manifest; no namespace-wide invalidation
// or in-memory authority is involved.
func TestTargetL3CacheMinIOPostgresComposition(t *testing.T) {
	ctx := context.Background()
	endpoint := startTargetL3MinIO(t, ctx)
	client := targetL3MinIOClient(t, ctx, endpoint)
	const bucket = "leapview-target-l3"
	if _, err := client.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatal(err)
	}
	prefix := "target-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	contract := targetL3Contract(t, bucket, prefix)
	db := targetL3PostgresDB(t)
	repo := cachepostgres.New(db)
	ns := cachepostgres.Namespace{PartitionKind: cachepostgres.PartitionProduction, ProjectID: "project", Environment: "prod"}
	cache, err := runtimefactory.NewTargetL3Cache(ctx, contract, repo, gcadapter.S3Config{Region: "us-east-1", AccessKeyID: targetL3User, SecretAccessKey: targetL3Secret, Endpoint: endpoint, PathStyle: true}, runtimefactory.TargetL3CacheConfig{Namespace: ns, Enabled: true, Prefix: "cache"})
	if err != nil {
		t.Fatal(err)
	}
	key := cachepostgres.ManifestKey{PartitionKind: cachepostgres.PartitionProduction, ProjectID: "project", Environment: "prod", PartitionFormatVersion: 1, DependencyDigest: targetL3Digest('a'), PolicyFingerprint: targetL3Digest('b'), CanonicalQueryDigest: targetL3Digest('c'), KeyFormatVersion: 1}
	lease, err := cache.AcquireFill(ctx, key, "minio-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := cache.Publish(ctx, analyticsl3.PublishInput{Key: key, Lease: lease, Body: strings.NewReader("target-result")})
	if err != nil {
		t.Fatal(err)
	}
	hit, err := cache.Read(ctx, manifest, key)
	if err != nil || !hit.Hit {
		t.Fatalf("composed target cache read=%+v err=%v", hit, err)
	}
	fullObjectKey := strings.Trim(prefix, "/") + "/" + manifest.ObjectKey
	if _, err := client.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(fullObjectKey)}); err != nil {
		t.Fatal(err)
	}
	missing, err := cache.Read(ctx, manifest, key)
	if err != nil || missing.Hit || !missing.Reconciled {
		t.Fatalf("composed missing read=%+v err=%v", missing, err)
	}
	if _, found, err := repo.Lookup(ctx, key); err != nil || found {
		t.Fatalf("exact retired manifest found=%v err=%v", found, err)
	}
}

const (
	targetL3Image  = "minio/minio@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e"
	targetL3User   = "leapview"
	targetL3Secret = "leapview-conformance-secret"
	targetL3KMS    = "leapview-sse-key:MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
)

func startTargetL3MinIO(t *testing.T, ctx context.Context) string {
	t.Helper()
	required := os.Getenv("LEAPVIEW_MINIO_CONFORMANCE_REQUIRED") != "" || os.Getenv("CI") != ""
	if !required {
		testcontainers.SkipIfProviderIsNotHealthy(t)
	}
	container, err := tcminio.Run(ctx, targetL3Image, tcminio.WithUsername(targetL3User), tcminio.WithPassword(targetL3Secret), testcontainers.WithEnv(map[string]string{"MINIO_KMS_SECRET_KEY": targetL3KMS}), testcontainers.WithLogger(log.TestLogger(t)))
	if err != nil {
		if required {
			t.Fatalf("required MinIO target cache container: %v", err)
		}
		t.Skipf("MinIO target cache container unavailable: %v", err)
	}
	testcontainers.CleanupContainer(t, container)
	connection, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return "http://" + strings.TrimRight(connection, "/")
}

func targetL3MinIOClient(t *testing.T, ctx context.Context, endpoint string) *awss3.Client {
	t.Helper()
	config, err := awsconfigForTargetL3(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return awss3.NewFromConfig(config, func(options *awss3.Options) { options.BaseEndpoint = aws.String(endpoint); options.UsePathStyle = true })
}

func awsconfigForTargetL3(ctx context.Context) (aws.Config, error) {
	return awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-east-1"), awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(targetL3User, targetL3Secret, "")))
}

func targetL3PostgresDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "target_l3_test")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := cachepostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return db
}

func targetL3Contract(t *testing.T, bucket, prefix string) *ducklake.PoolContract {
	t.Helper()
	tuple := physicalpool.Compatibility{DuckDBRuntime: "duckdb:test", DuckLakeExtension: "ducklake:test", CatalogFormat: "ducklake:v1", StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1"}
	pool, err := physicalpool.NewPhysicalPool(physicalpool.PoolIdentity{StorageLocation: "s3://" + bucket, StorageNamespace: prefix, Region: "us-east-1", IsolationBoundary: "target-l3", RetentionAuthority: "target-l3", Compatibility: tuple})
	if err != nil {
		t.Fatal(err)
	}
	checks := make([]physicalpool.EvidenceCheck, 0, len(ducklake.SharedPoolConformanceChecks))
	for _, id := range ducklake.SharedPoolConformanceChecks {
		checks = append(checks, physicalpool.EvidenceCheck{ID: id, Passed: true, ObservationDigest: targetL3DigestBytes([]byte(id))})
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

func targetL3Digest(ch byte) string {
	return targetL3DigestBytes([]byte(strings.Repeat(string(ch), 32)))
}

func targetL3DigestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}
