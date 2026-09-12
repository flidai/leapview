//go:build fai520qualification

package observationstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/flidai/leapview/internal/manageddata"
	managedpostgres "github.com/flidai/leapview/internal/manageddata/postgres"
	"github.com/flidai/leapview/internal/manageddata/storage"
	manageds3 "github.com/flidai/leapview/internal/manageddata/storage/s3"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/recoveryset/observation"
	recoverypostgres "github.com/flidai/leapview/internal/recoveryset/postgres"
	jobspkg "github.com/flidai/leapview/pkg/jobs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const qualificationMinIOImage = "quay.io/minio/minio@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e"

// This is an opt-in provider qualification, not a restore or startup test.
// It proves that the store uses exact versions and requires Object Lock
// COMPLIANCE retention on both source and off-host evidence objects.
func TestFAI520ObservationStoreMinIOObjectLock(t *testing.T) {
	ctx, client, newClient, endpoint, sourceBucket, evidenceBucket := qualificationProvider(t)
	now := time.Now().UTC().Truncate(time.Second)
	until := now.Add(10 * time.Minute)
	store, err := New(client, Config{Endpoint: endpoint, Region: "us-east-1", SourceBucket: sourceBucket, EvidenceBucket: evidenceBucket, Prefix: "qualification", Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("retained source bytes\n")
	key := "managed/revision/file.csv"
	put, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: &sourceBucket, Key: &key, Body: bytes.NewReader(body), ObjectLockMode: types.ObjectLockModeCompliance, ObjectLockRetainUntilDate: &until})
	if err != nil {
		t.Fatal(err)
	}
	version := aws.ToString(put.VersionId)
	if version == "" || strings.EqualFold(version, "null") {
		t.Fatal("MinIO did not return an immutable source version")
	}
	sum := sha256.Sum256(body)
	object := observation.ProviderObject{Endpoint: endpoint, Region: "us-east-1", Bucket: sourceBucket, Key: key, VersionID: version, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
	if protection, err := store.verifySource(ctx, object, until); err != nil || protection.Mode != string(types.ObjectLockRetentionModeCompliance) {
		t.Fatalf("verifySource() = %#v, %v", protection, err)
	}

	evidenceBody := []byte("canonical evidence\n")
	evidenceDigest := normalizedDigest(evidenceBody)
	ref, err := store.putEvidence(ctx, "manifest", evidenceDigest, evidenceBody, until)
	if err != nil {
		t.Fatal(err)
	}
	// A separately constructed client/store must reload the exact retained
	// version; no process-local state participates in the read.
	independent, err := New(newClient(), Config{Endpoint: endpoint, Region: "us-east-1", SourceBucket: sourceBucket, EvidenceBucket: evidenceBucket, Prefix: "qualification", Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := independent.getEvidence(ctx, ref, now, until)
	if err != nil || !bytes.Equal(got, evidenceBody) {
		t.Fatalf("independent evidence reload = %q, %v", got, err)
	}
}

func TestFAI520ObservationStoreMinIOFullCaptureSaveReload(t *testing.T) {
	ctx, client, newClient, endpoint, sourceBucket, evidenceBucket := qualificationProvider(t)
	now := time.Now().UTC().Truncate(time.Second)
	until := now.Add(10 * time.Minute)
	db := qualificationPostgres(t)
	managed := managedpostgres.New(db)
	collection, err := managed.CreateCollection(ctx, manageddata.CreateCollectionInput{ID: projectgraph.ResourceID("collection_store_qualification"), ProjectID: projectgraph.ResourceID("project_store_qualification"), ConnectionID: projectgraph.ResourceID("connection_store_qualification"), Name: "Store Qualification"})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("managed source bytes\n")
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	managedStore, err := manageds3.New(client, s3.NewPresignClient(client), manageds3.Config{Bucket: sourceBucket, Prefix: "managed"})
	if err != nil {
		t.Fatal(err)
	}
	manifest := manageddata.Manifest{Files: []manageddata.File{{Path: "orders.parquet", SHA256: hash, Size: int64(len(body))}}}
	session, err := managed.CreateUploadSession(ctx, manageddata.CreateUploadSessionInput{ID: manageddata.UploadID("upload_store_qualification"), CollectionID: collection.ID, Manifest: manifest, StorageBackend: "s3", StagingPrefix: "staging/store-qualification", ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := managed.BeginUploadFinalization(ctx, session.ID, jobspkg.WorkflowIntent{}); err != nil {
		t.Fatal(err)
	}
	blob, err := managedStore.Put(ctx, storage.Blob{SHA256: hash, Size: int64(len(body))}, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	key := strings.TrimPrefix(blob.URI, "s3://"+sourceBucket+"/")
	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &sourceBucket, Key: &key})
	if err != nil {
		t.Fatal(err)
	}
	version := aws.ToString(head.VersionId)
	if version == "" || strings.EqualFold(version, "null") {
		t.Fatal("MinIO did not return an immutable source version")
	}
	if _, err := managed.CompleteUpload(ctx, manageddata.CompleteUploadInput{SessionID: session.ID, RevisionID: manageddata.RevisionID("revision_store_qualification"), Files: []manageddata.StoredFile{{File: manifest.Files[0], StorageKey: "s3://" + sourceBucket + "/" + key}}}); err != nil {
		t.Fatal(err)
	}
	captured, err := recoverypostgres.Capture(ctx, managed)
	if err != nil {
		t.Fatal(err)
	}
	store, err := New(client, Config{Endpoint: endpoint, Region: "us-east-1", SourceBucket: sourceBucket, EvidenceBucket: evidenceBucket, Prefix: "full-qualification", Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	observations := []observation.Observation{{RevisionID: "revision_store_qualification", Path: "orders.parquet", Object: observation.ProviderObject{Endpoint: endpoint, Region: "us-east-1", Bucket: sourceBucket, Key: key, VersionID: version, SHA256: hash, Size: int64(len(body))}}}
	if _, err := store.Save(ctx, &captured, observations, until); !errors.Is(err, ErrRetention) {
		t.Fatalf("Save accepted an unprotected source version or returned wrong error: %v", err)
	}
	if _, err := client.PutObjectRetention(ctx, &s3.PutObjectRetentionInput{Bucket: &sourceBucket, Key: &key, VersionId: &version, Retention: &types.ObjectLockRetention{Mode: types.ObjectLockRetentionModeCompliance, RetainUntilDate: &until}}); err != nil {
		t.Fatal(err)
	}
	// A retained newer version with the same length but different bytes must
	// fail hash verification. The subsequent positive path must still select
	// the original historical version, never this latest object.
	wrongBytes := append([]byte(nil), body...)
	wrongBytes[0] ^= 1
	wrongPut, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: &sourceBucket, Key: &key, Body: bytes.NewReader(wrongBytes), ObjectLockMode: types.ObjectLockModeCompliance, ObjectLockRetainUntilDate: &until})
	if err != nil {
		t.Fatal(err)
	}
	wrongVersion := aws.ToString(wrongPut.VersionId)
	if wrongVersion == "" || wrongVersion == version {
		t.Fatal("provider did not issue a different historical version")
	}
	wrongObservations := append([]observation.Observation(nil), observations...)
	wrongObservations[0].Object.VersionID = wrongVersion
	if _, err := store.Save(ctx, &captured, wrongObservations, until); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Save accepted wrong historical bytes or returned wrong error: %v", err)
	}
	ref, err := store.Save(ctx, &captured, observations, until)
	if err != nil {
		t.Fatalf("Save() = %v", err)
	}
	refBytes, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	var persistedRef Ref
	if err := json.Unmarshal(refBytes, &persistedRef); err != nil {
		t.Fatal(err)
	}
	t.Logf("saved evidence manifest=%s boundary=%s descriptor=%s source-version=%s", ref.Manifest.SHA256, ref.Boundary.SHA256, ref.Descriptor.SHA256, version)
	independent, err := New(newClient(), Config{Endpoint: endpoint, Region: "us-east-1", SourceBucket: sourceBucket, EvidenceBucket: evidenceBucket, Prefix: "full-qualification", Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := independent.Reload(ctx, persistedRef, until)
	if err != nil {
		t.Fatalf("independent Reload() = %v", err)
	}
	if loaded.Boundary != captured.Boundary() || len(loaded.Manifest.Objects) != 1 || loaded.Manifest.Objects[0].Object.VersionID != version {
		t.Fatalf("reloaded evidence = %#v", loaded)
	}
	boundaryDigest, err := captured.Boundary().Digest()
	if err != nil {
		t.Fatal(err)
	}
	set := regressionV2Set(t, captured.Boundary(), "sha256:"+ref.Manifest.SHA256, boundaryDigest, "sha256:"+ref.Descriptor.SHA256)
	frontier, err := store.PersistFrontier(ctx, set, ref, until)
	if err != nil {
		t.Fatalf("PersistFrontier() = %v", err)
	}
	read, err := independent.ReadFrontier(ctx, frontier, until)
	if err != nil {
		t.Fatalf("independent ReadFrontier() = %v", err)
	}
	if read.ID != set.ID || read.ManagedEvidence == nil || read.ManagedEvidence.Boundary != captured.Boundary() {
		t.Fatalf("read frontier = %#v", read)
	}
}

func qualificationProvider(t *testing.T) (context.Context, *s3.Client, func() *s3.Client, string, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	t.Cleanup(cancel)
	user, secret := "q"+strings.ReplaceAll(uuid.NewString(), "-", ""), uuid.NewString()
	container, err := tcminio.Run(ctx, qualificationMinIOImage, tcminio.WithUsername(user), tcminio.WithPassword(secret), testcontainers.WithTmpfs(map[string]string{"/data": "rw,size=1g"}), testcontainers.WithWaitStrategy(wait.ForHTTP("/minio/health/ready").WithPort("9000").WithStartupTimeout(time.Minute)))
	testcontainers.CleanupContainer(t, container)
	if err != nil {
		t.Fatal(err)
	}
	address, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + strings.TrimRight(address, "/")
	newClient := func() *s3.Client {
		return s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String(endpoint), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider(user, secret, ""), RetryMaxAttempts: 1, HTTPClient: qualificationHTTPClient{t: t}})
	}
	client := newClient()
	sourceBucket, evidenceBucket := "source-"+strings.ReplaceAll(uuid.NewString(), "-", ""), "evidence-"+strings.ReplaceAll(uuid.NewString(), "-", "")
	for _, bucket := range []string{sourceBucket, evidenceBucket} {
		if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &bucket, ObjectLockEnabledForBucket: aws.Bool(true)}); err != nil {
			t.Fatal(err)
		}
		if _, err := client.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{Bucket: &bucket, VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled}}); err != nil {
			t.Fatal(err)
		}
	}
	return ctx, client, newClient, endpoint, sourceBucket, evidenceBucket
}

func qualificationPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	t.Cleanup(cancel)
	container, err := tcpostgres.Run(ctx, postgrestest.PostgreSQL18Image,
		tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("observation-store-qualification-secret"),
		testcontainers.WithCmd("postgres", "-c", "fsync=on"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90*time.Second)),
	)
	if err != nil {
		t.Fatal(err)
	}
	testcontainers.CleanupContainer(t, container)
	adminURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	p, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if err := p.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := managedpostgres.ApplySchema(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return p
}

type qualificationHTTPClient struct{ t *testing.T }

func (c qualificationHTTPClient) Do(request *http.Request) (*http.Response, error) {
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		c.t.Log("qualification provider transport failure")
	}
	if response != nil && response.StatusCode >= 400 {
		c.t.Logf("qualification provider HTTP status %d", response.StatusCode)
	}
	return response, err
}
