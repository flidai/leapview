//go:build fai520qualification

package postgres

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/flidai/leapview/internal/manageddata"
	managedpostgres "github.com/flidai/leapview/internal/manageddata/postgres"
	"github.com/flidai/leapview/internal/manageddata/storage"
	manageds3 "github.com/flidai/leapview/internal/manageddata/storage/s3"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/recoveryset/capture"
	"github.com/flidai/leapview/internal/recoveryset/successor"
	jobspkg "github.com/flidai/leapview/pkg/jobs"
	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	managedS3DRArtifactDirEnv = "LEAPVIEW_TEST_UBDR_MANAGED_DATA_S3_DR_EVIDENCE_DIR"
	managedS3DRScenarioID     = "fai-520-managed-data-s3-dr-seed-v1"
	managedS3DRImage          = "quay.io/minio/minio@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e"
	managedS3DRRegion         = "us-east-1"
	managedS3DRPrefix         = "recovered-data"
	managedS3DRSentinelPrefix = "unrelated-sentinel"
	managedS3DRProfileID      = "managed-data-s3-dr-seed"
)

type managedS3DRSeed struct {
	ProjectID    projectgraph.ResourceID
	CollectionID projectgraph.ResourceID
	ConnectionID projectgraph.ResourceID
	Profile      storage.ProviderProfileIdentity
	Revisions    []managedS3DRRevision
	Sentinels    []managedS3DRSentinel
}

type managedS3DRRevision struct {
	ID             manageddata.RevisionID
	ManifestDigest string
	Objects        []managedS3DRObject
}

type managedS3DRObject struct {
	Path       string
	UploadKind string
	Observed   storage.ProviderVersionObservation
}

type managedS3DRSentinel struct {
	Key       string `json:"key"`
	VersionID string `json:"version_id"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
}

type managedS3DRRecoveryPointArtifact struct {
	SchemaVersion       int      `json:"schema_version"`
	ScenarioID          string   `json:"scenario_id"`
	ScenarioFingerprint string   `json:"scenario_fingerprint"`
	RecoverySetID       string   `json:"recovery_set_id"`
	SourceAnchorDigest  string   `json:"source_anchor_digest"`
	ManifestDigest      string   `json:"manifest_digest"`
	FrontierCommitment  string   `json:"frontier_commitment"`
	CaptureStartedAt    string   `json:"capture_started_at"`
	CaptureCompletedAt  string   `json:"capture_completed_at"`
	MembershipCount     int64    `json:"membership_count"`
	ObjectCount         int64    `json:"object_count"`
	RevisionIdentities  []string `json:"revision_identities"`
}

type managedS3DRInventoryArtifact struct {
	SchemaVersion       int                            `json:"schema_version"`
	ScenarioID          string                         `json:"scenario_id"`
	ScenarioFingerprint string                         `json:"scenario_fingerprint"`
	ProviderProfileID   string                         `json:"provider_profile_id"`
	Bucket              string                         `json:"bucket"`
	RecoveredPrefix     string                         `json:"recovered_prefix"`
	SentinelPrefix      string                         `json:"sentinel_prefix"`
	Revisions           []managedS3DRInventoryRevision `json:"revisions"`
	Sentinels           []managedS3DRSentinel          `json:"sentinels"`
}

type managedS3DRInventoryRevision struct {
	RevisionID     string                       `json:"revision_id"`
	ManifestDigest string                       `json:"manifest_digest"`
	Objects        []managedS3DRInventoryObject `json:"objects"`
}

type managedS3DRInventoryObject struct {
	Path       string `json:"path"`
	UploadKind string `json:"upload_kind"`
	ObjectKey  string `json:"object_key"`
	VersionID  string `json:"version_id"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
}

type managedS3DRProvider struct {
	client   *awss3.Client
	bucket   string
	endpoint string
}

type managedS3DRProjectionSourceFunc func(context.Context) (manageddata.CapturedProjection, error)

func (f managedS3DRProjectionSourceFunc) CaptureManagedProjection(ctx context.Context) (manageddata.CapturedProjection, error) {
	return f(ctx)
}

type managedS3DRObservationReaderFunc func(context.Context, string, string) (manageddata.ProviderVersionObservation, error)

func (f managedS3DRObservationReaderFunc) ProviderVersionObservation(ctx context.Context, profileID, objectKey string) (manageddata.ProviderVersionObservation, error) {
	return f(ctx, profileID, objectKey)
}

// TestFAI520ManagedDataS3DRSeedQualification prepares one coherent recovery
// point and writes operator-facing evidence reports. It deliberately stops
// before restoration, admission, startup selection, or traffic activation.
// This validates recovery evidence binding. It does not prove successful physical disaster recovery.
func TestFAI520ManagedDataS3DRSeedQualification(t *testing.T) {
	t.Setenv("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED", "1")
	ctx := t.Context()
	fixture := providerObservationFixtureDB(t)
	applyManagedS3DRRecoverySchema(t, fixture)
	provider := startManagedS3DRProvider(t)
	managed := managedpostgres.New(fixture.db)
	seed, store := seedManagedS3DRScenario(t, ctx, managed, provider)
	beforeSentinels := append([]managedS3DRSentinel(nil), seed.Sentinels...)

	projection, err := managed.CaptureManagedProjection(ctx)
	if err != nil {
		t.Fatalf("capture managed projection: %v", err)
	}
	expected := managedS3DRExpectedRevisions(seed)
	if len(projection.Revisions) != len(expected) {
		t.Fatalf("captured revisions = %d, want %d", len(projection.Revisions), len(expected))
	}

	base := manifestCaptureBase(t)
	startedAt := time.Now().UTC().Truncate(time.Microsecond).Add(time.Second)
	completedAt := startedAt.Add(time.Second)
	assignment := capture.TrustAssignment{
		Generation: capture.TrustGeneration{
			IncarnationID: "71111111-1111-4111-8111-111111111111",
			Revision:      1,
			PolicyDigest:  "sha256:" + strings.Repeat("7", 64),
		},
		WorkerFence: 17,
		Deadline:    completedAt.Add(time.Hour),
	}
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{29}, ed25519.SeedSize))
	policy := managedS3DRPolicy(t, base.ID, seed, expected, privateKey, assignment)
	request := capture.Request{
		Base: base, CaptureID: "capture-managed-data-s3-dr-seed", AuthorityID: "authority-managed-data-s3-dr-seed",
		KeyID: "key-managed-data-s3-dr-seed", StartedAt: startedAt, CompletedAt: completedAt, Assignment: assignment,
	}
	result := captureManagedS3DREvidence(t, managed, managed, store, privateKey, policy, request)
	assertManagedS3DRManifest(t, result, seed)

	reader := &manifestCapturePayloadReader{objects: make(map[ValidatedLocator][]byte)}
	payloads := manifestCapturePayloads(t, result, reader)
	trust := TrustInput{Evidence: result.Evidence, Generation: TrustGeneration{
		IncarnationID: assignment.Generation.IncarnationID, Revision: assignment.Generation.Revision, PolicyDigest: assignment.Generation.PolicyDigest,
	}, WorkerFence: assignment.WorkerFence}
	if _, err := fixture.db.Exec(ctx, `INSERT INTO recovery.successor_trust_generation(singleton,incarnation_id,revision,policy_digest,worker_fence) VALUES(true,$1,$2,$3,$4)`, trust.Generation.IncarnationID, trust.Generation.Revision, trust.Generation.PolicyDigest, trust.WorkerFence); err != nil {
		t.Fatalf("provision successor trust generation: %v", err)
	}
	repository := NewSuccessorRepository(fixture.db, SuccessorOptions{Reader: reader, Trust: func(context.Context, string) (TrustInput, error) { return trust, nil }})
	input := Set3Input{Set: result.Set, Payloads: payloads}
	created, err := repository.CreateSet3(ctx, input)
	if err != nil {
		t.Fatalf("CreateSet3: %v", err)
	}
	retried, err := repository.CreateSet3(ctx, input)
	if err != nil {
		t.Fatalf("immutable CreateSet3 retry: %v", err)
	}
	createdBytes, err := created.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	retriedBytes, err := retried.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(createdBytes, retriedBytes) {
		t.Fatal("CreateSet3 retry changed canonical recovery evidence")
	}

	afterSentinels := listManagedS3DRSentinels(t, ctx, provider)
	if !reflect.DeepEqual(afterSentinels, beforeSentinels) {
		t.Fatalf("sentinel inventory changed during capture:\nbefore=%#v\nafter=%#v", beforeSentinels, afterSentinels)
	}

	recoveryPoint, inventory := managedS3DRArtifacts(t, seed, result)
	assertManagedS3DRArtifactDeterminism(t, recoveryPoint, inventory)
	writeManagedS3DRArtifacts(t, recoveryPoint, inventory)
	qualifyManagedS3DRFailures(t, projection, managed, store, privateKey, seed, expected, assignment, startedAt, completedAt, fixture.db)
}

func applyManagedS3DRRecoverySchema(t *testing.T, fixture *providerObservationFixture) {
	t.Helper()
	tx, err := fixture.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("apply recovery schema: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(t.Context(), SuccessorSchemaSQL()); err != nil {
		t.Fatalf("apply successor schema: %v", err)
	}
}

func startManagedS3DRProvider(t *testing.T) managedS3DRProvider {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	t.Cleanup(cancel)
	user := "q" + strings.ReplaceAll(uuid.NewString(), "-", "")
	secret := uuid.NewString()
	container, err := tcminio.Run(ctx, managedS3DRImage,
		tcminio.WithUsername(user), tcminio.WithPassword(secret),
		testcontainers.WithTmpfs(map[string]string{"/data": "rw,size=1g"}),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/minio/health/ready").WithPort("9000").WithStartupTimeout(time.Minute)),
	)
	if err != nil {
		t.Fatalf("start required MinIO qualification provider: %v", err)
	}
	testcontainers.CleanupContainer(t, container)
	address, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	transportEndpoint := "http://" + strings.TrimRight(address, "/")
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("parse MinIO connection address: %v", err)
	}
	identityEndpoint := "https://localhost:" + port
	client := awss3.New(awss3.Options{
		Region: managedS3DRRegion, BaseEndpoint: aws.String(transportEndpoint), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(user, secret, ""), RetryMaxAttempts: 1,
	})
	bucket := "qualification-" + uuid.NewString()
	if _, err := client.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: &bucket}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.PutBucketVersioning(ctx, &awss3.PutBucketVersioningInput{Bucket: &bucket, VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled}}); err != nil {
		t.Fatal(err)
	}
	return managedS3DRProvider{client: client, bucket: bucket, endpoint: identityEndpoint}
}

func seedManagedS3DRScenario(t *testing.T, ctx context.Context, repo *managedpostgres.Repository, provider managedS3DRProvider) (managedS3DRSeed, *manageds3.Store) {
	t.Helper()
	seed := managedS3DRSeed{
		ProjectID: "project_managed_s3_dr", CollectionID: "collection_managed_s3_dr", ConnectionID: "connection_managed_s3_dr",
		Profile: storage.ProviderProfileIdentity{
			ProfileID: managedS3DRProfileID, Implementation: "s3", AccountIdentity: "qualification",
			Endpoint: provider.endpoint, Region: managedS3DRRegion, Bucket: provider.bucket, Namespace: managedS3DRPrefix,
		},
	}
	store, err := manageds3.New(provider.client, awss3.NewPresignClient(provider.client), manageds3.Config{
		Bucket: provider.bucket, Prefix: managedS3DRPrefix, ObservationProfile: &seed.Profile, ObservationRecorder: repo,
	})
	if err != nil {
		t.Fatal(err)
	}
	collection, err := repo.CreateCollection(ctx, manageddata.CreateCollectionInput{
		ID: seed.CollectionID, ProjectID: seed.ProjectID, ConnectionID: seed.ConnectionID, Name: "Managed S3 DR seed",
	})
	if err != nil {
		t.Fatal(err)
	}
	seed.Revisions = append(seed.Revisions,
		seedManagedS3DRRevision(t, ctx, repo, store, provider.client, provider.bucket, collection.ID, "revision_managed_s3_dr_a", "upload_managed_s3_dr_a", []managedS3DRSeedObject{
			{Path: "orders.csv", UploadKind: "put", Body: []byte("order_id,amount\n1,10\n")},
			{Path: "customers.csv", UploadKind: "put", Body: []byte("customer_id,name\n1,Ada\n")},
		}),
		seedManagedS3DRRevision(t, ctx, repo, store, provider.client, provider.bucket, collection.ID, "revision_managed_s3_dr_b", "upload_managed_s3_dr_b", []managedS3DRSeedObject{
			{Path: "orders.csv", UploadKind: "put", Body: []byte("order_id,amount\n1,10\n2,25\n")},
			{Path: "events.parquet", UploadKind: "multipart", Body: bytes.Repeat([]byte("managed-multipart-evidence\n"), 256)},
		}),
	)

	for _, sentinel := range []struct {
		key  string
		body []byte
	}{
		{managedS3DRSentinelPrefix + "/control.json", []byte(`{"sentinel":"control"}`)},
		{managedS3DRSentinelPrefix + "/unrelated.csv", []byte("id,value\n9,unrelated\n")},
		{managedS3DRSentinelPrefix + "/unrelated.csv", []byte("id,value\n10,still-unrelated\n")},
	} {
		if _, err := provider.client.PutObject(ctx, &awss3.PutObjectInput{Bucket: &provider.bucket, Key: &sentinel.key, Body: bytes.NewReader(sentinel.body)}); err != nil {
			t.Fatalf("seed sentinel %s: %v", sentinel.key, err)
		}
	}
	seed.Sentinels = listManagedS3DRSentinels(t, ctx, provider)
	return seed, store
}

type managedS3DRSeedObject struct {
	Path       string
	UploadKind string
	Body       []byte
}

func seedManagedS3DRRevision(t *testing.T, ctx context.Context, repo *managedpostgres.Repository, store *manageds3.Store, client *awss3.Client, bucket string, collectionID projectgraph.ResourceID, revisionID, uploadID string, objects []managedS3DRSeedObject) managedS3DRRevision {
	t.Helper()
	manifest := manageddata.Manifest{Files: make([]manageddata.File, 0, len(objects))}
	stored := make([]manageddata.StoredFile, 0, len(objects))
	revision := managedS3DRRevision{ID: manageddata.RevisionID(revisionID), Objects: make([]managedS3DRObject, 0, len(objects))}
	for _, object := range objects {
		blob := putManagedS3DRObject(t, ctx, store, client, bucket, object)
		if blob.ProviderVersion == nil {
			t.Fatalf("%s write omitted authoritative provider version", object.Path)
		}
		persisted, err := repo.ProviderVersionObservation(ctx, blob.ProviderVersion.Profile.ProfileID, blob.ProviderVersion.ObjectKey)
		if err != nil || persisted != *blob.ProviderVersion {
			t.Fatalf("durable provider observation for %s = %#v, %v", object.Path, persisted, err)
		}
		file := manageddata.File{Path: object.Path, SHA256: blob.SHA256, Size: blob.Size}
		manifest.Files = append(manifest.Files, file)
		stored = append(stored, manageddata.StoredFile{File: file, StorageKey: blob.URI, MediaType: "application/octet-stream"})
		revision.Objects = append(revision.Objects, managedS3DRObject{Path: object.Path, UploadKind: object.UploadKind, Observed: *blob.ProviderVersion})
	}
	session, err := repo.CreateUploadSession(ctx, manageddata.CreateUploadSessionInput{
		ID: manageddata.UploadID(uploadID), CollectionID: collectionID, Manifest: manifest, StorageBackend: "s3",
		StagingPrefix: "staging/" + uploadID, ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.BeginUploadFinalization(ctx, session.ID, jobspkg.WorkflowIntent{}); err != nil {
		t.Fatal(err)
	}
	created, err := repo.CompleteUpload(ctx, manageddata.CompleteUploadInput{SessionID: session.ID, RevisionID: revision.ID, Files: stored})
	if err != nil {
		t.Fatal(err)
	}
	revision.ManifestDigest = manifest.RevisionID()
	if created.Digest != revision.ManifestDigest || created.ID != revision.ID {
		t.Fatalf("revision %s identity = %#v, want manifest %s", revision.ID, created, revision.ManifestDigest)
	}
	return revision
}

func putManagedS3DRObject(t *testing.T, ctx context.Context, store *manageds3.Store, client *awss3.Client, bucket string, object managedS3DRSeedObject) storage.Blob {
	t.Helper()
	sum := sha256.Sum256(object.Body)
	expected := storage.Blob{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(object.Body))}
	if object.UploadKind == "put" {
		blob, err := store.Put(ctx, expected, bytes.NewReader(object.Body))
		if err != nil {
			t.Fatal(err)
		}
		return blob
	}
	if object.UploadKind != "multipart" {
		t.Fatalf("unknown upload kind %q", object.UploadKind)
	}
	upload, err := store.CreateMultipart(ctx, expected)
	if err != nil {
		t.Fatal(err)
	}
	partNumber := int32(1)
	part, err := client.UploadPart(ctx, &awss3.UploadPartInput{
		Bucket: &bucket, Key: &upload.Key, UploadId: &upload.UploadID, PartNumber: &partNumber,
		Body: bytes.NewReader(object.Body), ContentLength: aws.Int64(int64(len(object.Body))),
	})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := store.CompleteMultipart(ctx, upload, []storage.CompletedMultipartPart{{Number: partNumber, ETag: aws.ToString(part.ETag)}})
	if err != nil {
		t.Fatal(err)
	}
	return blob
}

func listManagedS3DRSentinels(t *testing.T, ctx context.Context, provider managedS3DRProvider) []managedS3DRSentinel {
	t.Helper()
	out, err := provider.client.ListObjectVersions(ctx, &awss3.ListObjectVersionsInput{Bucket: &provider.bucket, Prefix: aws.String(managedS3DRSentinelPrefix + "/")})
	if err != nil {
		t.Fatal(err)
	}
	if aws.ToBool(out.IsTruncated) || len(out.DeleteMarkers) != 0 {
		t.Fatal("sentinel qualification inventory is truncated or contains delete markers")
	}
	items := make([]managedS3DRSentinel, 0, len(out.Versions))
	for _, version := range out.Versions {
		if version.Key == nil || version.VersionId == nil || version.Size == nil {
			t.Fatal("sentinel version metadata is incomplete")
		}
		read, err := provider.client.GetObject(ctx, &awss3.GetObjectInput{Bucket: &provider.bucket, Key: version.Key, VersionId: version.VersionId})
		if err != nil {
			t.Fatal(err)
		}
		raw, readErr := io.ReadAll(read.Body)
		closeErr := read.Body.Close()
		if readErr != nil || closeErr != nil || aws.ToString(read.VersionId) != aws.ToString(version.VersionId) {
			t.Fatalf("read sentinel version: read=%v close=%v", readErr, closeErr)
		}
		sum := sha256.Sum256(raw)
		items = append(items, managedS3DRSentinel{Key: aws.ToString(version.Key), VersionID: aws.ToString(version.VersionId), SHA256: hex.EncodeToString(sum[:]), Size: int64(len(raw))})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Key != items[j].Key {
			return items[i].Key < items[j].Key
		}
		return items[i].VersionID < items[j].VersionID
	})
	return items
}

func managedS3DRExpectedRevisions(seed managedS3DRSeed) []successor.Revision {
	revisions := make([]successor.Revision, 0, len(seed.Revisions))
	for _, revision := range seed.Revisions {
		files := make([]successor.File, 0, len(revision.Objects))
		for _, object := range revision.Objects {
			files = append(files, successor.File{Path: object.Path, SHA256: object.Observed.SHA256, Size: object.Observed.Size})
		}
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		revisions = append(revisions, successor.Revision{
			ProjectID: seed.ProjectID.String(), CollectionID: seed.CollectionID.String(), RevisionID: revision.ID.String(),
			RevisionManifestDigest: revision.ManifestDigest, Files: files,
		})
	}
	sort.Slice(revisions, func(i, j int) bool { return revisions[i].RevisionID < revisions[j].RevisionID })
	return revisions
}

func managedS3DRPolicy(t *testing.T, setID string, seed managedS3DRSeed, expected []successor.Revision, privateKey ed25519.PrivateKey, assignment capture.TrustAssignment) capture.TrustPolicy {
	t.Helper()
	profiles := successor.ProviderProfileSet{ProfileVersion: successor.ProviderProfileVersion, Profiles: []successor.ProviderProfile{{
		Implementation: "s3", AccountIdentity: seed.Profile.AccountIdentity, Endpoint: seed.Profile.Endpoint, Region: seed.Profile.Region,
		Bucket: seed.Profile.Bucket, VersionSemantics: "opaque-exact-version",
		Namespaces: []successor.Namespace{{ProjectID: seed.ProjectID.String(), CollectionID: seed.CollectionID.String(), Prefix: seed.Profile.Namespace}},
	}}}
	profileDigest, err := profiles.Digest()
	if err != nil {
		t.Fatal(err)
	}
	closureDigest, err := successor.ClosureDigest(expected)
	if err != nil {
		t.Fatal(err)
	}
	return capture.TrustPolicy{
		Profiles: profiles,
		Authorities: successor.AuthorityRegistry{RegistryVersion: successor.AuthorityRegistryVersion, Keys: []successor.AuthorityKey{{
			AuthorityID: "authority-managed-data-s3-dr-seed", KeyID: "key-managed-data-s3-dr-seed", Algorithm: "Ed25519",
			PublicKey: base64.StdEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey)),
			NotBefore: "2020-01-01T00:00:00.000000Z", NotAfter: "2099-12-31T23:59:59.000000Z", ProviderProfileDigests: []string{profileDigest},
		}}},
		ProfileBindings: []capture.ProfileBinding{{ProjectID: seed.ProjectID.String(), CollectionID: seed.CollectionID.String(), Prefix: seed.Profile.Namespace, ObservationProfileID: seed.Profile.ProfileID}},
		ExpectedScope:   successor.ExpectedScope{SetID: setID, ManagedClosureDigest: closureDigest, Revisions: expected},
		Assignment:      assignment,
	}
}

func captureManagedS3DREvidence(t *testing.T, source manageddata.ProjectionCaptureSource, observations capture.ObservationReader, verifier capture.ObservationVerifier, privateKey ed25519.PrivateKey, policy capture.TrustPolicy, request capture.Request) capture.Result {
	t.Helper()
	service, err := capture.New(source, observations, verifier,
		capture.SignerFunc(func(_ context.Context, signing capture.SigningRequest) ([]byte, error) {
			return ed25519.Sign(privateKey, signing.Payload), nil
		}),
		capture.PolicyScopeResolverFunc(func(context.Context, capture.Request) (capture.TrustPolicy, error) { return policy, nil }),
		capture.ClockFunc(func() time.Time { return request.CompletedAt.Add(time.Second) }),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Capture(t.Context(), request)
	if err != nil {
		t.Fatalf("capture successor evidence: %v", err)
	}
	return result
}

func assertManagedS3DRManifest(t *testing.T, result capture.Result, seed managedS3DRSeed) {
	t.Helper()
	manifest := result.Evidence.Manifest
	if len(manifest.Revisions) != 2 {
		t.Fatalf("manifest revisions = %d, want 2", len(manifest.Revisions))
	}
	memberships, objects, err := manifest.ObservationCounts()
	if err != nil {
		t.Fatal(err)
	}
	if memberships != 4 || objects != 4 {
		t.Fatalf("manifest observation counts = memberships %d objects %d, want 4/4", memberships, objects)
	}
	multipartFound := false
	for _, revision := range manifest.Revisions {
		for _, file := range revision.Files {
			if !strings.HasPrefix(file.Provider.Key, managedS3DRPrefix+"/") || file.Provider.VersionID == "" {
				t.Fatalf("manifest provider identity is incomplete: %#v", file.Provider)
			}
			for _, seededRevision := range seed.Revisions {
				for _, seeded := range seededRevision.Objects {
					if seeded.Path == file.Path && seededRevision.ID.String() == revision.RevisionID && seeded.UploadKind == "multipart" {
						multipartFound = file.Provider.VersionID == seeded.Observed.VersionID && file.Size == seeded.Observed.Size && file.SHA256 == seeded.Observed.SHA256
					}
				}
			}
		}
	}
	if !multipartFound {
		t.Fatal("multipart membership did not preserve its exact provider observation")
	}
}

func managedS3DRArtifacts(t *testing.T, seed managedS3DRSeed, result capture.Result) (managedS3DRRecoveryPointArtifact, managedS3DRInventoryArtifact) {
	t.Helper()
	fingerprint := managedS3DRScenarioFingerprint(t, seed)
	memberships, objects, err := result.Evidence.Manifest.ObservationCounts()
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest, err := result.Evidence.Manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	revisionIDs := make([]string, 0, len(seed.Revisions))
	inventoryRevisions := make([]managedS3DRInventoryRevision, 0, len(seed.Revisions))
	for _, revision := range seed.Revisions {
		revisionIDs = append(revisionIDs, revision.ID.String())
		entry := managedS3DRInventoryRevision{RevisionID: revision.ID.String(), ManifestDigest: revision.ManifestDigest}
		for _, object := range revision.Objects {
			entry.Objects = append(entry.Objects, managedS3DRInventoryObject{
				Path: object.Path, UploadKind: object.UploadKind, ObjectKey: object.Observed.ObjectKey, VersionID: object.Observed.VersionID,
				SHA256: object.Observed.SHA256, Size: object.Observed.Size,
			})
		}
		sort.Slice(entry.Objects, func(i, j int) bool { return entry.Objects[i].Path < entry.Objects[j].Path })
		inventoryRevisions = append(inventoryRevisions, entry)
	}
	sort.Strings(revisionIDs)
	sort.Slice(inventoryRevisions, func(i, j int) bool { return inventoryRevisions[i].RevisionID < inventoryRevisions[j].RevisionID })
	sentinels := append([]managedS3DRSentinel(nil), seed.Sentinels...)
	sort.Slice(sentinels, func(i, j int) bool {
		if sentinels[i].Key != sentinels[j].Key {
			return sentinels[i].Key < sentinels[j].Key
		}
		return sentinels[i].VersionID < sentinels[j].VersionID
	})
	return managedS3DRRecoveryPointArtifact{
			SchemaVersion: 1, ScenarioID: managedS3DRScenarioID, ScenarioFingerprint: fingerprint,
			RecoverySetID: result.Set.ID, SourceAnchorDigest: result.Set.SourceFrontierAnchorDigest, ManifestDigest: manifestDigest,
			FrontierCommitment: result.Set.FrontierDigest, CaptureStartedAt: result.Evidence.Manifest.Capture.StartedAt,
			CaptureCompletedAt: result.Evidence.Manifest.Capture.CompletedAt, MembershipCount: memberships, ObjectCount: objects,
			RevisionIdentities: revisionIDs,
		}, managedS3DRInventoryArtifact{
			SchemaVersion: 1, ScenarioID: managedS3DRScenarioID, ScenarioFingerprint: fingerprint,
			ProviderProfileID: seed.Profile.ProfileID, Bucket: seed.Profile.Bucket, RecoveredPrefix: managedS3DRPrefix,
			SentinelPrefix: managedS3DRSentinelPrefix, Revisions: inventoryRevisions, Sentinels: sentinels,
		}
}

func managedS3DRScenarioFingerprint(t *testing.T, seed managedS3DRSeed) string {
	t.Helper()
	type logicalObject struct {
		Path, UploadKind, ObjectKey, SHA256 string
		Size                                int64
	}
	type logicalRevision struct {
		RevisionID, ManifestDigest string
		Objects                    []logicalObject
	}
	type logicalSentinel struct {
		Key, SHA256 string
		Size        int64
	}
	logical := struct {
		ScenarioID, RecoveredPrefix, SentinelPrefix string
		Revisions                                   []logicalRevision
		Sentinels                                   []logicalSentinel
	}{ScenarioID: managedS3DRScenarioID, RecoveredPrefix: managedS3DRPrefix, SentinelPrefix: managedS3DRSentinelPrefix}
	for _, revision := range seed.Revisions {
		entry := logicalRevision{RevisionID: revision.ID.String(), ManifestDigest: revision.ManifestDigest}
		for _, object := range revision.Objects {
			entry.Objects = append(entry.Objects, logicalObject{Path: object.Path, UploadKind: object.UploadKind, ObjectKey: object.Observed.ObjectKey, SHA256: object.Observed.SHA256, Size: object.Observed.Size})
		}
		sort.Slice(entry.Objects, func(i, j int) bool { return entry.Objects[i].Path < entry.Objects[j].Path })
		logical.Revisions = append(logical.Revisions, entry)
	}
	sort.Slice(logical.Revisions, func(i, j int) bool { return logical.Revisions[i].RevisionID < logical.Revisions[j].RevisionID })
	seenSentinels := make(map[string]logicalSentinel)
	for _, sentinel := range seed.Sentinels {
		seenSentinels[sentinel.Key+"\x00"+sentinel.SHA256] = logicalSentinel{Key: sentinel.Key, SHA256: sentinel.SHA256, Size: sentinel.Size}
	}
	for _, sentinel := range seenSentinels {
		logical.Sentinels = append(logical.Sentinels, sentinel)
	}
	sort.Slice(logical.Sentinels, func(i, j int) bool {
		if logical.Sentinels[i].Key != logical.Sentinels[j].Key {
			return logical.Sentinels[i].Key < logical.Sentinels[j].Key
		}
		return logical.Sentinels[i].SHA256 < logical.Sentinels[j].SHA256
	})
	raw, err := json.Marshal(logical)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func assertManagedS3DRArtifactDeterminism(t *testing.T, point managedS3DRRecoveryPointArtifact, inventory managedS3DRInventoryArtifact) {
	t.Helper()
	pointRaw := managedS3DRCanonicalArtifact(t, point)
	inventoryRaw := managedS3DRCanonicalInventory(t, inventory)
	reorderedPoint := point
	reorderedPoint.RevisionIdentities = reverseStrings(point.RevisionIdentities)
	reorderedInventory := inventory
	reorderedInventory.Revisions = append([]managedS3DRInventoryRevision(nil), inventory.Revisions...)
	sort.Slice(reorderedInventory.Revisions, func(i, j int) bool {
		return reorderedInventory.Revisions[i].RevisionID > reorderedInventory.Revisions[j].RevisionID
	})
	for i := range reorderedInventory.Revisions {
		reorderedInventory.Revisions[i].Objects = append([]managedS3DRInventoryObject(nil), reorderedInventory.Revisions[i].Objects...)
		sort.Slice(reorderedInventory.Revisions[i].Objects, func(a, b int) bool {
			return reorderedInventory.Revisions[i].Objects[a].Path > reorderedInventory.Revisions[i].Objects[b].Path
		})
	}
	reorderedInventory.Sentinels = append([]managedS3DRSentinel(nil), inventory.Sentinels...)
	sort.Slice(reorderedInventory.Sentinels, func(i, j int) bool { return reorderedInventory.Sentinels[i].Key > reorderedInventory.Sentinels[j].Key })
	if !bytes.Equal(pointRaw, managedS3DRCanonicalArtifact(t, reorderedPoint)) {
		t.Fatal("reordered recovery-point input changed canonical artifact")
	}
	if !bytes.Equal(inventoryRaw, managedS3DRCanonicalInventory(t, reorderedInventory)) {
		t.Fatal("reordered inventory input changed canonical artifact")
	}
}

func reverseStrings(values []string) []string {
	out := append([]string(nil), values...)
	for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
		out[left], out[right] = out[right], out[left]
	}
	return out
}

func managedS3DRCanonicalArtifact(t *testing.T, value managedS3DRRecoveryPointArtifact) []byte {
	t.Helper()
	sort.Strings(value.RevisionIdentities)
	return managedS3DRMarshalArtifact(t, value)
}

func managedS3DRCanonicalInventory(t *testing.T, value managedS3DRInventoryArtifact) []byte {
	t.Helper()
	sort.Slice(value.Revisions, func(i, j int) bool { return value.Revisions[i].RevisionID < value.Revisions[j].RevisionID })
	for i := range value.Revisions {
		sort.Slice(value.Revisions[i].Objects, func(a, b int) bool { return value.Revisions[i].Objects[a].Path < value.Revisions[i].Objects[b].Path })
	}
	sort.Slice(value.Sentinels, func(i, j int) bool {
		if value.Sentinels[i].Key != value.Sentinels[j].Key {
			return value.Sentinels[i].Key < value.Sentinels[j].Key
		}
		return value.Sentinels[i].VersionID < value.Sentinels[j].VersionID
	})
	return managedS3DRMarshalArtifact(t, value)
}

func managedS3DRMarshalArtifact(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}

func writeManagedS3DRArtifacts(t *testing.T, point managedS3DRRecoveryPointArtifact, inventory managedS3DRInventoryArtifact) {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv(managedS3DRArtifactDirEnv))
	if dir == "" {
		dir = t.TempDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	artifacts := []struct {
		name string
		raw  []byte
	}{
		{name: "recovery-point.json", raw: managedS3DRCanonicalArtifact(t, point)},
		{name: "baseline-object-inventory.json", raw: managedS3DRCanonicalInventory(t, inventory)},
	}
	for _, artifact := range artifacts {
		path := filepath.Join(dir, artifact.name)
		writeManagedS3DRArtifact(t, path, artifact.raw)
		stored, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(stored, artifact.raw) || !json.Valid(stored) {
			t.Fatalf("qualification artifact %s did not survive atomic readback", artifact.name)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("qualification artifact %s mode = %o, want 600", artifact.name, info.Mode().Perm())
		}
	}
}

func writeManagedS3DRArtifact(t *testing.T, target string, raw []byte) {
	t.Helper()
	dir := filepath.Dir(target)
	temporary, err := os.CreateTemp(dir, "."+filepath.Base(target)+".*")
	if err != nil {
		t.Fatal(err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		t.Fatal(err)
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		t.Fatal(err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		t.Fatal(err)
	}
	if err := temporary.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporaryName, target); err != nil {
		t.Fatal(err)
	}
}

func qualifyManagedS3DRFailures(t *testing.T, projection manageddata.CapturedProjection, observations *managedpostgres.Repository, verifier *manageds3.Store, privateKey ed25519.PrivateKey, seed managedS3DRSeed, expected []successor.Revision, assignment capture.TrustAssignment, startedAt, completedAt time.Time, db DBTX) {
	t.Helper()
	firstKey := seed.Revisions[0].Objects[0].Observed.ObjectKey
	baseReader := managedS3DRObservationReaderFunc(func(ctx context.Context, profileID, objectKey string) (manageddata.ProviderVersionObservation, error) {
		return observations.ProviderVersionObservation(ctx, profileID, objectKey)
	})
	baseSource := managedS3DRProjectionSourceFunc(func(context.Context) (manageddata.CapturedProjection, error) {
		return cloneManagedS3DRProjection(projection), nil
	})
	cases := []struct {
		name        string
		setID       string
		want        error
		source      manageddata.ProjectionCaptureSource
		reader      capture.ObservationReader
		mutateScope func([]successor.Revision) []successor.Revision
	}{
		{name: "missing observation", setID: "80000000-0000-4000-8000-000000000001", want: capture.ErrIncomplete, source: baseSource, reader: managedS3DRObservationReaderFunc(func(ctx context.Context, profileID, objectKey string) (manageddata.ProviderVersionObservation, error) {
			if objectKey == firstKey {
				return manageddata.ProviderVersionObservation{}, manageddata.ErrNotFound
			}
			return baseReader.ProviderVersionObservation(ctx, profileID, objectKey)
		})},
		{name: "wrong version ID", setID: "80000000-0000-4000-8000-000000000002", want: capture.ErrIncomplete, source: baseSource, reader: managedS3DRObservationReaderFunc(func(ctx context.Context, profileID, objectKey string) (manageddata.ProviderVersionObservation, error) {
			observation, err := baseReader.ProviderVersionObservation(ctx, profileID, objectKey)
			if err == nil && objectKey == firstKey {
				observation.VersionID = "unavailable-exact-version"
			}
			return observation, err
		})},
		{name: "missing revision member", setID: "80000000-0000-4000-8000-000000000003", want: capture.ErrConflict, source: baseSource, reader: baseReader, mutateScope: func(revisions []successor.Revision) []successor.Revision { return revisions[:1] }},
		{name: "sentinel contamination", setID: "80000000-0000-4000-8000-000000000004", want: capture.ErrConflict, source: managedS3DRProjectionSourceFunc(func(context.Context) (manageddata.CapturedProjection, error) {
			contaminated := cloneManagedS3DRProjection(projection)
			contaminated.Revisions[0].Files[0].StorageKey = "s3://" + seed.Profile.Bucket + "/" + managedS3DRSentinelPrefix + "/control.json"
			return contaminated, nil
		}), reader: baseReader},
		{name: "digest mismatch", setID: "80000000-0000-4000-8000-000000000005", want: capture.ErrConflict, source: baseSource, reader: managedS3DRObservationReaderFunc(func(ctx context.Context, profileID, objectKey string) (manageddata.ProviderVersionObservation, error) {
			observation, err := baseReader.ProviderVersionObservation(ctx, profileID, objectKey)
			if err == nil && objectKey == firstKey {
				observation.SHA256 = strings.Repeat("0", 64)
			}
			return observation, err
		})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := manifestCaptureBase(t)
			base.ID = tc.setID
			base.FrontierDigest = ""
			scope := append([]successor.Revision(nil), expected...)
			if tc.mutateScope != nil {
				scope = tc.mutateScope(scope)
			}
			policy := managedS3DRPolicy(t, base.ID, seed, scope, privateKey, assignment)
			request := capture.Request{Base: base, CaptureID: "capture-" + strings.ReplaceAll(tc.name, " ", "-"), AuthorityID: "authority-managed-data-s3-dr-seed", KeyID: "key-managed-data-s3-dr-seed", StartedAt: startedAt, CompletedAt: completedAt, Assignment: assignment}
			service, err := capture.New(tc.source, tc.reader, verifier,
				capture.SignerFunc(func(_ context.Context, signing capture.SigningRequest) ([]byte, error) {
					return ed25519.Sign(privateKey, signing.Payload), nil
				}),
				capture.PolicyScopeResolverFunc(func(context.Context, capture.Request) (capture.TrustPolicy, error) { return policy, nil }),
				capture.ClockFunc(func() time.Time { return completedAt.Add(time.Second) }),
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Capture(t.Context(), request); !errors.Is(err, tc.want) {
				t.Fatalf("capture error = %v, want %v", err, tc.want)
			}
			var count int
			if err := db.QueryRow(t.Context(), `SELECT count(*) FROM recovery.recovery_set_v3 WHERE set_id=$1::uuid`, tc.setID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("failed capture left %d usable RecoverySet v3 records", count)
			}
		})
	}
}

func cloneManagedS3DRProjection(projection manageddata.CapturedProjection) manageddata.CapturedProjection {
	clone := projection
	clone.Revisions = append([]manageddata.CapturedProjectionRevision(nil), projection.Revisions...)
	for i := range clone.Revisions {
		clone.Revisions[i].Files = append([]manageddata.CapturedProjectionFile(nil), projection.Revisions[i].Files...)
	}
	return clone
}
