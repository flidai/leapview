//go:build fai981qualification

package providerrestore_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/app/providerrestore"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	postgresmigrations "github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/internal/recoveryset"
	recoverypg "github.com/flidai/leapview/internal/recoveryset/postgres"
	refreshpg "github.com/flidai/leapview/internal/refresh/postgres"
	"github.com/flidai/leapview/internal/refresh/recovery"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const qualificationMinIOImage = "quay.io/minio/minio@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e"

func TestFAI981CoordinatedProviderRestoreQualification(t *testing.T) {
	t.Setenv("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED", "1")
	evidenceRoot := strings.TrimSpace(os.Getenv("LEAPVIEW_TEST_FAI981_PROVIDER_RESTORE_EVIDENCE_DIR"))
	if evidenceRoot == "" {
		t.Fatal("LEAPVIEW_TEST_FAI981_PROVIDER_RESTORE_EVIDENCE_DIR is required")
	}
	objects := startQualificationObjects(t)
	objectPoints := objects.seedAndDamage(t)
	postgres := startQualificationPostgres(t)
	backups := postgres.seedBackupAndDamage(t, objectPoints)
	authorityPool := postgres.createAuthorityDatabase(t)

	set := qualificationRecoverySet(t, postgres.clusterIdentity, backups, objects, objectPoints)
	canonicalSet, err := set.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(canonicalSet, []byte(`\u0000`)) {
		t.Fatalf("recovery set contains a NUL string: %s", canonicalSet)
	}
	setRepository := recoverypg.New(authorityPool)
	created, err := setRepository.Create(t.Context(), set)
	if err != nil {
		t.Fatalf("create exact recovery set: %v", err)
	}
	ledger := refreshpg.NewRecoveryLedger(authorityPool)
	occurrence := qualificationOccurrence(t, ledger, created)
	store := providerrestore.FileEvidenceStore{Root: filepath.Join(evidenceRoot, "checkpoints")}
	databaseProvider := &qualificationDatabaseProvider{fixture: postgres, backups: backups, restoredURLs: map[recoveryset.DatabaseRole]string{}}
	objectProvider := &qualificationObjectProvider{fixture: objects, points: objectPoints}
	verifier := &qualificationVerifier{databases: databaseProvider, objects: objects, points: objectPoints, expected: backups}
	coordinator, err := providerrestore.New(providerrestore.Dependencies{
		Ledger: ledger, Sets: setRepository, Databases: databaseProvider, Objects: objectProvider,
		Verifier: verifier, Evidence: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := providerrestore.Request{
		OccurrenceID: occurrence.ID, Fence: occurrence.Fence, RecoverySetID: created.ID,
		TargetID: created.Delivery.TargetID, ValidationAttemptID: uuid.NewString(),
		Validator: "fai981-provider-validator", Publisher: "fai981-provider-publisher",
	}
	report, err := coordinator.Run(t.Context(), request)
	if err != nil {
		t.Fatalf("coordinate physical provider restore: %v", err)
	}
	finalOccurrence, err := ledger.Occurrence(t.Context(), occurrence.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := ledger.Attempts(t.Context(), occurrence.ID)
	if err != nil {
		t.Fatal(err)
	}
	published, err := setRepository.ReadExact(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertQualificationResult(t, report, finalOccurrence, published, objectPoints)

	summary := struct {
		SchemaVersion int                     `json:"schemaVersion"`
		Qualification string                  `json:"qualification"`
		Providers     map[string]string       `json:"providers"`
		Report        providerrestore.Report  `json:"report"`
		Occurrence    recovery.Occurrence     `json:"occurrence"`
		Attempts      []recovery.Attempt      `json:"attempts"`
		RecoverySet   recoveryset.RecoverySet `json:"recoverySet"`
	}{
		SchemaVersion: 1, Qualification: "FAI-981 coordinated physical provider restoration",
		Providers: map[string]string{
			"postgresql": "PostgreSQL 18 physical pg_basebackup restored as an independent server in an isolated provider container",
			"objects":    "MinIO S3 immutable-version recovery in an isolated provider container",
		},
		Report: report, Occurrence: finalOccurrence, Attempts: attempts, RecoverySet: published,
	}
	writeQualificationEvidence(t, evidenceRoot, summary)
	t.Logf("FAI-981 coordinated provider restore evidence: %s", filepath.Join(evidenceRoot, "coordinated-provider-restore.json"))
}

type qualificationPostgresFixture struct {
	container       *tcpostgres.PostgresContainer
	admin           *pgxpool.Pool
	password        string
	clusterIdentity string
	timeline        string
	backupIdentity  string
}

type qualificationBackup struct {
	point       recoveryset.ClusterRecoveryPoint
	stateDigest string
}

func startQualificationPostgres(t *testing.T) *qualificationPostgresFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx, postgrestest.PostgreSQL18Image,
		tcpostgres.WithDatabase("postgres"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("fai981-provider-secret"),
		testcontainers.WithCmd("postgres"),
		testcontainers.WithExposedPorts("55432/tcp"),
		testcontainers.WithTmpfs(map[string]string{"/var/lib/postgresql": "rw,size=1g", "/tmp": "rw,size=512m"}),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90*time.Second)),
	)
	if err != nil {
		t.Fatalf("start required PostgreSQL restore provider: %v", err)
	}
	testcontainers.CleanupContainer(t, container)
	adminURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	var systemID, timeline string
	if err := admin.QueryRow(ctx, `SELECT system_identifier::text FROM pg_control_system()`).Scan(&systemID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `SELECT timeline_id::text FROM pg_control_checkpoint()`).Scan(&timeline); err != nil {
		t.Fatal(err)
	}
	return &qualificationPostgresFixture{container: container, admin: admin, password: "fai981-provider-secret", clusterIdentity: "postgres-system:" + systemID, timeline: timeline}
}

func (fixture *qualificationPostgresFixture) seedBackupAndDamage(t *testing.T, objects map[string]qualificationObjectPoint) map[recoveryset.DatabaseRole]qualificationBackup {
	t.Helper()
	for _, name := range []string{"fai981_control_source", "fai981_ducklake_source"} {
		fixture.createDatabase(t, name)
	}
	controlURL := fixture.databaseURL(t, "fai981_control_source")
	control := openQualificationPool(t, controlURL)
	for _, statement := range []string{
		`CREATE TABLE application_state(id bigint PRIMARY KEY, state_key text NOT NULL, state_value text NOT NULL)`,
		`INSERT INTO application_state VALUES (1,'tenant','tenant-a'),(2,'dashboard','orders-dashboard'),(3,'policy','governed')`,
	} {
		if _, err := control.Exec(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	duckURL := fixture.databaseURL(t, "fai981_ducklake_source")
	duck := openQualificationPool(t, duckURL)
	duckRoot := objects[recoveryset.ObjectRootDuckLake]
	artifactRoot := objects[recoveryset.ObjectRootServingArtifact]
	for _, statement := range []string{
		`CREATE SCHEMA ducklake_catalog`,
		`CREATE TABLE ducklake_catalog.ducklake_metadata(key text PRIMARY KEY,value text NOT NULL)`,
		`CREATE TABLE ducklake_catalog.ducklake_snapshot(snapshot_id bigint PRIMARY KEY,catalog_version bigint NOT NULL,committed_at timestamptz NOT NULL)`,
		`CREATE TABLE ducklake_catalog.ducklake_data_file(snapshot_id bigint NOT NULL,object_uri text NOT NULL,object_version text NOT NULL,object_digest text NOT NULL,PRIMARY KEY(snapshot_id,object_uri))`,
		`INSERT INTO ducklake_catalog.ducklake_metadata VALUES ('catalog_id','catalog-a'),('catalog_uuid','0198f2c0-7c7a-7f00-8a11-000000009981'),('format','ducklake:v1')`,
		`INSERT INTO ducklake_catalog.ducklake_snapshot VALUES (42,9,'2026-09-21T12:00:00Z')`,
	} {
		if _, err := duck.Exec(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := duck.Exec(t.Context(), `INSERT INTO ducklake_catalog.ducklake_data_file VALUES (42,$1,$2,$3),(42,$4,$5,$6)`, duckRoot.uri, duckRoot.version, duckRoot.digest, artifactRoot.uri, artifactRoot.version, artifactRoot.digest); err != nil {
		t.Fatal(err)
	}

	runContainerCommand(t, fixture.container, "su", "postgres", "-c", "pg_basebackup -U postgres -h /var/run/postgresql -D /tmp/fai981-physical-base -Fp -X stream -c fast")
	backupLabelDigest := strings.TrimSpace(runContainerCommand(t, fixture.container, "sha256sum", "/tmp/fai981-physical-base/backup_label"))
	backupLabelDigest, _, _ = strings.Cut(backupLabelDigest, " ")
	fixture.backupIdentity = fmt.Sprintf("pg-basebackup:sha256:%s:timeline:%s", backupLabelDigest, fixture.timeline)
	backups := map[recoveryset.DatabaseRole]qualificationBackup{
		recoveryset.DatabaseControl: {
			stateDigest: qualificationDatabaseStateDigest(t, control, recoveryset.DatabaseControl),
			point:       recoveryset.ClusterRecoveryPoint{DatabaseRole: recoveryset.DatabaseControl, ClusterIdentity: fixture.clusterIdentity, DatabaseIdentity: "fai981_control_source", RecoveryIdentity: fixture.backupIdentity},
		},
		recoveryset.DatabaseDuckLake: {
			stateDigest: qualificationDatabaseStateDigest(t, duck, recoveryset.DatabaseDuckLake),
			point:       recoveryset.ClusterRecoveryPoint{DatabaseRole: recoveryset.DatabaseDuckLake, ClusterIdentity: fixture.clusterIdentity, DatabaseIdentity: "fai981_ducklake_source", RecoveryIdentity: fixture.backupIdentity},
		},
	}
	if _, err := control.Exec(t.Context(), `UPDATE application_state SET state_value='corrupted-after-backup'`); err != nil {
		t.Fatal(err)
	}
	if _, err := duck.Exec(t.Context(), `DELETE FROM ducklake_catalog.ducklake_data_file`); err != nil {
		t.Fatal(err)
	}
	return backups
}

func (fixture *qualificationPostgresFixture) createAuthorityDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	fixture.createDatabase(t, "fai981_recovery_authority")
	pool := openQualificationPool(t, fixture.databaseURL(t, "fai981_recovery_authority"))
	if err := postgresmigrations.ApplyRiver(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := recoverypg.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := jobspostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := refreshpg.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return pool
}

func (fixture *qualificationPostgresFixture) createDatabase(t *testing.T, name string) {
	t.Helper()
	if _, err := fixture.admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
}

func (fixture *qualificationPostgresFixture) databaseURL(t *testing.T, name string) string {
	t.Helper()
	value, err := fixture.container.ConnectionString(t.Context(), "sslmode=disable", "dbname="+name)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

type qualificationDatabaseProvider struct {
	fixture            *qualificationPostgresFixture
	backups            map[recoveryset.DatabaseRole]qualificationBackup
	mu                 sync.Mutex
	restoredURLs       map[recoveryset.DatabaseRole]string
	restoreStartedAt   time.Time
	restoreCompletedAt time.Time
	restoreStarted     bool
}

func (provider *qualificationDatabaseProvider) RestoreDatabase(ctx context.Context, request providerrestore.DatabaseRequest) (providerrestore.DatabaseResult, error) {
	backup, ok := provider.backups[request.Point.DatabaseRole]
	if !ok || backup.point != request.Point {
		return providerrestore.DatabaseResult{}, fmt.Errorf("unknown PostgreSQL recovery point")
	}
	provider.mu.Lock()
	if !provider.restoreStarted {
		provider.restoreStartedAt = time.Now().UTC()
		if err := runContainerCommandError(ctx, provider.fixture.container, "su", "postgres", "-c", `pg_ctl -D /tmp/fai981-physical-base -o "-p 55432 -k /tmp -c listen_addresses='*'" -w start`); err != nil {
			provider.mu.Unlock()
			return providerrestore.DatabaseResult{}, err
		}
		provider.restoreCompletedAt = time.Now().UTC()
		provider.restoreStarted = true
	}
	started, completed := provider.restoreStartedAt, provider.restoreCompletedAt
	provider.mu.Unlock()
	restoredURL, err := provider.fixture.restoredDatabaseURL(ctx, request.Point.DatabaseIdentity)
	if err != nil {
		return providerrestore.DatabaseResult{}, err
	}
	pool, err := pgxpool.New(ctx, restoredURL)
	if err != nil {
		return providerrestore.DatabaseResult{}, err
	}
	stateDigest, err := qualificationDatabaseStateDigestContext(ctx, pool, request.Point.DatabaseRole)
	pool.Close()
	if err != nil || stateDigest != backup.stateDigest {
		return providerrestore.DatabaseResult{}, fmt.Errorf("restored PostgreSQL state mismatch: %w", err)
	}
	provider.mu.Lock()
	provider.restoredURLs[request.Point.DatabaseRole] = restoredURL
	provider.mu.Unlock()
	result := providerrestore.DatabaseResult{
		Provider: "postgresql-physical-basebackup", OperationID: "postgres-physical-restore:" + digestText(request.Point.RecoveryIdentity),
		DatabaseRole: request.Point.DatabaseRole, ClusterIdentity: request.Point.ClusterIdentity,
		DatabaseIdentity: request.Point.DatabaseIdentity, RecoveryIdentity: request.Point.RecoveryIdentity,
		StateDigest: stateDigest, StartedAt: started, CompletedAt: completed,
	}
	if request.Point.DatabaseRole == recoveryset.DatabaseDuckLake {
		catalog := request.Catalog
		result.Catalog = &catalog
	}
	return result, nil
}

func (fixture *qualificationPostgresFixture) restoredDatabaseURL(ctx context.Context, database string) (string, error) {
	host, err := fixture.container.Host(ctx)
	if err != nil {
		return "", err
	}
	port, err := fixture.container.MappedPort(ctx, "55432/tcp")
	if err != nil {
		return "", err
	}
	return (&url.URL{Scheme: "postgres", User: url.UserPassword("postgres", fixture.password), Host: net.JoinHostPort(host, port.Port()), Path: "/" + database, RawQuery: "sslmode=disable"}).String(), nil
}

type qualificationObjectPoint struct {
	uri, key, version, digest string
	damageVersions            []string
}

type qualificationObjects struct {
	client *awss3.Client
	bucket string
}

func startQualificationObjects(t *testing.T) *qualificationObjects {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	user, secret := "fai981"+strings.ReplaceAll(uuid.NewString(), "-", ""), uuid.NewString()
	container, err := tcminio.Run(ctx, qualificationMinIOImage, tcminio.WithUsername(user), tcminio.WithPassword(secret),
		testcontainers.WithTmpfs(map[string]string{"/data": "rw,size=1g"}),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/minio/health/ready").WithPort("9000").WithStartupTimeout(time.Minute)))
	if err != nil {
		t.Fatalf("start required versioned object provider: %v", err)
	}
	testcontainers.CleanupContainer(t, container)
	address, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	client := awss3.New(awss3.Options{Region: "us-east-1", BaseEndpoint: aws.String("http://" + strings.TrimRight(address, "/")), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider(user, secret, ""), RetryMaxAttempts: 1})
	bucket := "fai981-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	if _, err := client.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: &bucket}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.PutBucketVersioning(ctx, &awss3.PutBucketVersioningInput{Bucket: &bucket, VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled}}); err != nil {
		t.Fatal(err)
	}
	return &qualificationObjects{client: client, bucket: bucket}
}

func (objects *qualificationObjects) seedAndDamage(t *testing.T) map[string]qualificationObjectPoint {
	t.Helper()
	points := map[string]qualificationObjectPoint{}
	for kind, key := range map[string]string{recoveryset.ObjectRootDuckLake: "ducklake/snapshot-42/root.json", recoveryset.ObjectRootServingArtifact: "serving/generation/root.json"} {
		body := []byte(fmt.Sprintf(`{"kind":%q,"recoveryPoint":"fai981","snapshot":42}`, kind))
		version := objects.put(t, key, body)
		point := qualificationObjectPoint{uri: "s3://" + objects.bucket + "/" + key, key: key, version: version, digest: digestBytes(body)}
		corruptVersion := objects.put(t, key, []byte("corrupted-after-recovery-point"))
		deleted, err := objects.client.DeleteObject(t.Context(), &awss3.DeleteObjectInput{Bucket: &objects.bucket, Key: &key})
		if err != nil {
			t.Fatal(err)
		}
		deleteVersion := aws.ToString(deleted.VersionId)
		if !aws.ToBool(deleted.DeleteMarker) || deleteVersion == "" {
			t.Fatal("versioned object provider did not create a delete marker")
		}
		point.damageVersions = []string{deleteVersion, corruptVersion}
		points[kind] = point
	}
	return points
}

func (objects *qualificationObjects) put(t *testing.T, key string, body []byte) string {
	t.Helper()
	out, err := objects.client.PutObject(t.Context(), &awss3.PutObjectInput{Bucket: &objects.bucket, Key: &key, Body: bytes.NewReader(body)})
	if err != nil {
		t.Fatal(err)
	}
	version := aws.ToString(out.VersionId)
	if version == "" || version == "null" {
		t.Fatal("object provider did not return an immutable version")
	}
	return version
}

type qualificationObjectProvider struct {
	fixture *qualificationObjects
	points  map[string]qualificationObjectPoint
	mu      sync.Mutex
	done    map[string]bool
}

func (provider *qualificationObjectProvider) RestoreObject(ctx context.Context, request providerrestore.ObjectRequest) (providerrestore.ObjectResult, error) {
	started := time.Now().UTC()
	point, ok := provider.points[request.Root.Kind]
	if !ok || point.uri != request.Root.URI || point.version != request.Root.VersionID || point.digest != request.Root.Digest {
		return providerrestore.ObjectResult{}, fmt.Errorf("unknown object recovery point")
	}
	provider.mu.Lock()
	if provider.done == nil {
		provider.done = map[string]bool{}
	}
	alreadyDone := provider.done[request.IdempotencyKey]
	provider.mu.Unlock()
	if !alreadyDone {
		for _, version := range point.damageVersions {
			if _, err := provider.fixture.client.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: &provider.fixture.bucket, Key: &point.key, VersionId: &version}); err != nil {
				return providerrestore.ObjectResult{}, err
			}
		}
	}
	observedVersion, observedDigest, err := provider.fixture.current(ctx, point.key)
	if err != nil {
		return providerrestore.ObjectResult{}, err
	}
	if observedVersion != point.version || observedDigest != point.digest {
		return providerrestore.ObjectResult{}, fmt.Errorf("restored object does not match exact retained version")
	}
	provider.mu.Lock()
	provider.done[request.IdempotencyKey] = true
	provider.mu.Unlock()
	return providerrestore.ObjectResult{Provider: "minio-s3-versioning", OperationID: "object-restore:" + request.IdempotencyKey, Kind: request.Root.Kind, URI: request.Root.URI, RequiredVersionID: request.Root.VersionID, ObservedVersionID: observedVersion, Digest: observedDigest, StartedAt: started, CompletedAt: time.Now().UTC()}, nil
}

func (objects *qualificationObjects) current(ctx context.Context, key string) (string, string, error) {
	out, err := objects.client.GetObject(ctx, &awss3.GetObjectInput{Bucket: &objects.bucket, Key: &key})
	if err != nil {
		return "", "", err
	}
	if out.Body == nil {
		return "", "", fmt.Errorf("object provider returned no body")
	}
	body, readErr := io.ReadAll(out.Body)
	closeErr := out.Body.Close()
	if readErr != nil || closeErr != nil {
		return "", "", fmt.Errorf("read restored object: %w", errorsJoin(readErr, closeErr))
	}
	return aws.ToString(out.VersionId), digestBytes(body), nil
}

type qualificationVerifier struct {
	databases *qualificationDatabaseProvider
	objects   *qualificationObjects
	points    map[string]qualificationObjectPoint
	expected  map[recoveryset.DatabaseRole]qualificationBackup
}

func (verifier *qualificationVerifier) Verify(ctx context.Context, request providerrestore.VerificationRequest) (providerrestore.VerificationResult, error) {
	digests := map[recoveryset.DatabaseRole]string{}
	for _, role := range []recoveryset.DatabaseRole{recoveryset.DatabaseControl, recoveryset.DatabaseDuckLake} {
		verifier.databases.mu.Lock()
		databaseURL := verifier.databases.restoredURLs[role]
		verifier.databases.mu.Unlock()
		pool, err := pgxpool.New(ctx, databaseURL)
		if err != nil {
			return providerrestore.VerificationResult{}, err
		}
		digest, err := qualificationDatabaseStateDigestContext(ctx, pool, role)
		pool.Close()
		if err != nil || digest != verifier.expected[role].stateDigest {
			return providerrestore.VerificationResult{}, fmt.Errorf("post-restore database verification failed: %w", err)
		}
		digests[role] = digest
	}
	for _, point := range verifier.points {
		version, digest, err := verifier.objects.current(ctx, point.key)
		if err != nil || version != point.version || digest != point.digest {
			return providerrestore.VerificationResult{}, fmt.Errorf("post-restore object verification failed: %w", err)
		}
	}
	return providerrestore.VerificationResult{ProviderOperationID: "coordinated-verification:" + request.Set.FrontierDigest, ControlStateDigest: digests[recoveryset.DatabaseControl], DuckLakeStateDigest: digests[recoveryset.DatabaseDuckLake], Catalog: request.Set.Catalog, ObjectsConsistent: true, Ready: true, VerifiedAt: time.Now().UTC()}, nil
}

func qualificationRecoverySet(t *testing.T, cluster string, backups map[recoveryset.DatabaseRole]qualificationBackup, objects *qualificationObjects, points map[string]qualificationObjectPoint) recoveryset.RecoverySet {
	t.Helper()
	compatibility := physicalpool.Compatibility{DuckDBRuntime: "duckdb:1", DuckLakeExtension: "ducklake:1", CatalogFormat: "ducklake:v1", StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1"}
	compatibilityDigest, err := compatibility.Digest()
	if err != nil {
		t.Fatal(err)
	}
	duck, artifact := points[recoveryset.ObjectRootDuckLake], points[recoveryset.ObjectRootServingArtifact]
	set := recoveryset.RecoverySet{
		ID: uuid.NewString(), SchemaVersion: recoveryset.SchemaVersion,
		ClusterPoints: []recoveryset.ClusterRecoveryPoint{backups[recoveryset.DatabaseControl].point, backups[recoveryset.DatabaseDuckLake].point},
		Delivery:      recoveryset.DeliveryPointer{TargetID: "target-fai981-isolated", GenerationID: uuid.NewString(), PublicationID: uuid.NewString(), TargetRevision: 42},
		Serving:       recoveryset.SnapshotSeal{SealID: uuid.NewString(), PhysicalPoolID: "pool-fai981", TenantDomain: "tenant-a", Region: "isolated-provider", EncryptionDomain: "qualification-key-reference", ObjectNamespace: objects.bucket, CatalogDatabase: backups[recoveryset.DatabaseDuckLake].point.DatabaseIdentity, CatalogID: "catalog-a", CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000009981", CatalogVersion: 9, DuckLakeSnapshotID: 42, RelationManifestDigest: digestText("relation-manifest"), RelationNamespace: "snapshot/42", ClosureDigest: digestText("closure"), ObjectRoot: duck.uri, ObjectRootDigest: duck.digest, ArtifactRoot: artifact.uri, ArtifactRootDigest: artifact.digest, ServingArtifactID: "artifact-fai981", ServingArtifactDigest: digestText("serving-artifact"), CompiledGraphDigest: digestText("graph"), CompiledConfigDigest: digestText("config"), SecurityDomainFingerprint: digestText("security"), RequestDigest: digestText("request"), PlanDigest: digestText("plan"), CompatibilityDigest: compatibilityDigest, DuckDBVersion: "1", RuntimeVersion: "1", DuckLakeExtensionVersion: "1", DuckLakeSpecVersion: "1", CatalogSchemaVersion: "1"},
		Catalog:       recoveryset.CatalogCommit{CatalogID: "catalog-a", CatalogDatabase: backups[recoveryset.DatabaseDuckLake].point.DatabaseIdentity, CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000009981", CatalogVersion: 9, SnapshotID: 42},
		ObjectRoots:   []recoveryset.ObjectRoot{{Kind: recoveryset.ObjectRootDuckLake, URI: duck.uri, VersionID: duck.version, Digest: duck.digest, ProviderRecoveryFrontier: "minio-version:" + duck.version}, {Kind: recoveryset.ObjectRootServingArtifact, URI: artifact.uri, VersionID: artifact.version, Digest: artifact.digest, ProviderRecoveryFrontier: "minio-version:" + artifact.version}},
		Compatibility: compatibility, FenceEpoch: 1, AuditIdentity: "fai981-qualification", Status: recoveryset.StatusPrepared, CreatedBy: "fai981-qualification", CreatedAt: time.Now().UTC(),
	}
	normalized, err := set.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}

func qualificationOccurrence(t *testing.T, ledger *refreshpg.RecoveryLedger, set recoveryset.RecoverySet) recovery.Occurrence {
	t.Helper()
	now := time.Now().UTC()
	definition := recovery.Definition{ScheduleID: "fai981-provider-restore", Scenario: "coordinated-provider-restore", Operation: recovery.OperationRestore, PolicyVersion: "ubdr-provider-restore-v1", PolicySHA256: strings.Repeat("a", 64), TargetScope: set.Delivery.TargetID, ArtifactIdentity: "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64), Cron: "@daily", Timezone: "UTC", StaleAfter: 24 * time.Hour, Enabled: true}
	if err := ledger.ReconcileSchedule(t.Context(), definition, now); err != nil {
		t.Fatal(err)
	}
	revision, err := recovery.ScheduleRevisionID(definition)
	if err != nil {
		t.Fatal(err)
	}
	input := recovery.EnqueueInput{ScheduleID: definition.ScheduleID, ScheduleRevision: revision, Scenario: definition.Scenario, Operation: definition.Operation, PolicyVersion: definition.PolicyVersion, PolicySHA256: definition.PolicySHA256, TargetScope: definition.TargetScope, ArtifactIdentity: definition.ArtifactIdentity, PlannedAt: now.Add(-5 * time.Minute), StaleAfter: definition.StaleAfter}
	enqueued, created, err := ledger.Enqueue(t.Context(), input, now)
	if err != nil || !created {
		t.Fatalf("enqueue provider restore occurrence: created=%v err=%v", created, err)
	}
	claimed, ok, err := ledger.ClaimNext(t.Context(), recovery.ClaimInput{WorkerID: "fai981-provider-worker", Actor: "fai981-qualification", Now: time.Now().UTC(), Lease: 20 * time.Minute})
	if err != nil || !ok || claimed.ID != enqueued.ID {
		t.Fatalf("claim provider restore occurrence: occurrence=%q ok=%v err=%v", claimed.ID, ok, err)
	}
	if err := ledger.Start(t.Context(), claimed.ID, claimed.Fence, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	started, err := ledger.Occurrence(t.Context(), claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	return started
}

func qualificationDatabaseStateDigest(t *testing.T, pool *pgxpool.Pool, role recoveryset.DatabaseRole) string {
	t.Helper()
	digest, err := qualificationDatabaseStateDigestContext(t.Context(), pool, role)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func qualificationDatabaseStateDigestContext(ctx context.Context, pool *pgxpool.Pool, role recoveryset.DatabaseRole) (string, error) {
	query := `SELECT id::text,state_key,state_value FROM application_state ORDER BY id`
	if role == recoveryset.DatabaseDuckLake {
		query = `
SELECT 'metadata',key,value FROM ducklake_catalog.ducklake_metadata
UNION ALL SELECT 'snapshot',snapshot_id::text,catalog_version::text FROM ducklake_catalog.ducklake_snapshot
UNION ALL SELECT 'object',object_uri,object_version||':'||object_digest FROM ducklake_catalog.ducklake_data_file
ORDER BY 1,2,3`
	}
	rows, err := pool.Query(ctx, query)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	hash := sha256.New()
	for rows.Next() {
		var first, second, third string
		if err := rows.Scan(&first, &second, &third); err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\n", first, second, third)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func assertQualificationResult(t *testing.T, report providerrestore.Report, occurrence recovery.Occurrence, set recoveryset.RecoverySet, objects map[string]qualificationObjectPoint) {
	t.Helper()
	if report.Status != providerrestore.StatusSucceeded || occurrence.Status != recovery.StatusSucceeded || set.Status != recoveryset.StatusPublished {
		t.Fatalf("coordinated result incomplete: report=%q occurrence=%q set=%q", report.Status, occurrence.Status, set.Status)
	}
	if len(report.Databases) != 2 || len(report.Objects) != 2 || !report.Verification.Ready || !report.Verification.ObjectsConsistent {
		t.Fatalf("coordinated provider evidence incomplete: %#v", report)
	}
	if occurrence.RestoreStartedAt.IsZero() || occurrence.RestoreCompletedAt.IsZero() || occurrence.RestoreCompletedAt.Before(occurrence.RestoreStartedAt) || len(occurrence.Evidence) != 1 {
		t.Fatalf("durable occurrence phase/evidence incomplete: %#v", occurrence)
	}
	for _, result := range report.Objects {
		point := objects[result.Kind]
		if result.ObservedVersionID != point.version || result.Digest != point.digest {
			t.Fatalf("object result is not exact retained version: %#v", result)
		}
	}
}

func writeQualificationEvidence(t *testing.T, root string, value any) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if bytes.Contains(encoded, []byte("fai981-provider-secret")) || bytes.Contains(encoded, []byte("postgres://")) {
		t.Fatal("credential-bearing data reached qualification evidence")
	}
	path := filepath.Join(root, "coordinated-provider-restore.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if bytes.Contains(raw, []byte("fai981-provider-secret")) || bytes.Contains(raw, []byte("postgres://")) || bytes.Contains(bytes.ToLower(raw), []byte("password")) {
			return fmt.Errorf("credential-bearing content in %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func openQualificationPool(t *testing.T, databaseURL string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func runContainerCommand(t *testing.T, container *tcpostgres.PostgresContainer, command ...string) string {
	t.Helper()
	output, err := runContainerCommandOutput(t.Context(), container, command...)
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func runContainerCommandError(ctx context.Context, container *tcpostgres.PostgresContainer, command ...string) error {
	_, err := runContainerCommandOutput(ctx, container, command...)
	return err
}

func runContainerCommandOutput(ctx context.Context, container *tcpostgres.PostgresContainer, command ...string) (string, error) {
	exitCode, reader, err := container.Exec(ctx, command, tcexec.Multiplexed())
	if err != nil {
		return "", err
	}
	output, readErr := io.ReadAll(reader)
	if readErr != nil {
		return "", readErr
	}
	if exitCode != 0 {
		return "", fmt.Errorf("provider command %q exited %d: %s", command[0], exitCode, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func digestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func digestText(value string) string { return digestBytes([]byte(value)) }

func errorsJoin(values ...error) error {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func parseObjectURI(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "s3" || parsed.Host == "" || strings.TrimPrefix(parsed.Path, "/") == "" {
		return nil, fmt.Errorf("invalid object URI")
	}
	return parsed, nil
}
