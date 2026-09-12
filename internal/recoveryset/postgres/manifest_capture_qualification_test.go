//go:build fai520qualification

package postgres

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	managedpostgres "github.com/flidai/leapview/internal/manageddata/postgres"
	"github.com/flidai/leapview/internal/manageddata/storage"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/flidai/leapview/internal/recoveryset/capture"
	"github.com/flidai/leapview/internal/recoveryset/successor"
	jobspkg "github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestFAI520ManifestCapturePostgreSQLQualification exercises the complete handoff:
// PostgreSQL-owned managed-data closure and observations are consumed by the
// capture service, then all signed v2 evidence is persisted through the
// existing exact-version successor repository.
func TestFAI520ManifestCapturePostgreSQLQualification(t *testing.T) {
	t.Setenv("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED", "1")
	fixture := providerObservationFixtureDB(t)
	ctx := t.Context()
	tx, err := fixture.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("apply recovery schema: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(ctx, SuccessorSchemaSQL()); err != nil {
		t.Fatalf("apply successor schema: %v", err)
	}
	pool, admin, reopenURL := fixture.db, fixture.db, fixture.db.Config().ConnString()

	managed := managedpostgres.New(admin)
	projectID := projectgraph.ResourceID("project_manifest_capture")
	collectionID := projectgraph.ResourceID("collection_manifest_capture")
	const (
		profileID = "profile-manifest-capture"
		objectKey = "managed/orders.parquet"
	)
	content := []byte("durable managed bytes")
	hash := sha256.Sum256(content)
	fileSHA := hex.EncodeToString(hash[:])
	manifest := manageddata.Manifest{Files: []manageddata.File{{Path: "orders.parquet", SHA256: fileSHA, Size: int64(len(content))}}}
	collection, err := managed.CreateCollection(ctx, manageddata.CreateCollectionInput{
		ID: collectionID, ProjectID: projectID, ConnectionID: projectgraph.ResourceID("connection_manifest_capture"), Name: "Manifest capture qualification",
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := managed.CreateUploadSession(ctx, manageddata.CreateUploadSessionInput{
		ID: manageddata.UploadID("upload_manifest_capture"), CollectionID: collection.ID, Manifest: manifest,
		StorageBackend: "s3", StagingPrefix: "staging/manifest-capture", ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := managed.BeginUploadFinalization(ctx, session.ID, jobspkg.WorkflowIntent{}); err != nil {
		t.Fatal(err)
	}
	if _, err := managed.CompleteUpload(ctx, manageddata.CompleteUploadInput{
		SessionID: session.ID, RevisionID: manageddata.RevisionID("revision_manifest_capture"),
		Files: []manageddata.StoredFile{{File: manifest.Files[0], StorageKey: "s3://capture-bucket/" + objectKey}},
	}); err != nil {
		t.Fatal(err)
	}

	profile := storage.ProviderProfileIdentity{
		ProfileID: profileID, Implementation: "s3", AccountIdentity: "qualification-account",
		Endpoint: "https://objects.example.test", Region: "test-region-1", Bucket: "capture-bucket", Namespace: "managed",
	}
	observed := storage.ProviderVersionObservation{
		Profile: profile, ObjectKey: objectKey, VersionID: "immutable-orders-v1", SHA256: fileSHA,
		Size: int64(len(content)), CapturedAt: time.Date(2026, 9, 9, 23, 59, 0, 0, time.UTC),
	}
	if _, err := managed.RecordProviderVersionObservation(ctx, observed); err != nil {
		t.Fatal(err)
	}

	// Use separately pooled handles for the service's source and observation
	// reads so the input is demonstrably durable, rather than process-local.
	dsn := admin.Config().ConnString()
	sourceDB := openManifestCapturePool(t, dsn)
	observationDB := openManifestCapturePool(t, dsn)
	source := managedpostgres.New(sourceDB)
	observations := managedpostgres.New(observationDB)
	durable, err := observations.ProviderVersionObservation(ctx, profileID, objectKey)
	if err != nil {
		t.Fatal(err)
	}
	if !sameManifestCaptureObservation(durable, observed) {
		t.Fatalf("durable provider observation = %#v, want %#v", durable, observed)
	}

	base := manifestCaptureBase(t)
	profiles := successor.ProviderProfileSet{ProfileVersion: successor.ProviderProfileVersion, Profiles: []successor.ProviderProfile{{
		Implementation: "s3", AccountIdentity: profile.AccountIdentity, Endpoint: profile.Endpoint, Region: profile.Region,
		Bucket: profile.Bucket, VersionSemantics: "opaque-exact-version",
		Namespaces: []successor.Namespace{{ProjectID: projectID.String(), CollectionID: collectionID.String(), Prefix: profile.Namespace}},
	}}}
	profileDigest, err := profiles.Digest()
	if err != nil {
		t.Fatal(err)
	}
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{17}, ed25519.SeedSize))
	authorities := successor.AuthorityRegistry{RegistryVersion: successor.AuthorityRegistryVersion, Keys: []successor.AuthorityKey{{
		AuthorityID: "authority-manifest-capture", KeyID: "key-manifest-capture", Algorithm: "Ed25519",
		PublicKey: base64.StdEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey)),
		NotBefore: "2026-09-09T00:00:00.000000Z", NotAfter: "2099-09-11T00:00:00.000000Z", ProviderProfileDigests: []string{profileDigest},
	}}}
	expectedRevisions := []successor.Revision{{
		ProjectID: projectID.String(), CollectionID: collectionID.String(), RevisionID: "revision_manifest_capture", RevisionManifestDigest: manifest.RevisionID(),
		Files: []successor.File{{Path: manifest.Files[0].Path, SHA256: manifest.Files[0].SHA256, Size: manifest.Files[0].Size}},
	}}
	closureDigest, err := successor.ClosureDigest(expectedRevisions)
	if err != nil {
		t.Fatal(err)
	}
	assignment := capture.TrustAssignment{
		Generation: capture.TrustGeneration{
			IncarnationID: "11111111-1111-4111-8111-111111111111", Revision: 1,
			PolicyDigest: "sha256:" + strings.Repeat("9", 64),
		},
		WorkerFence: 7,
		Deadline:    time.Date(2099, 9, 10, 2, 0, 0, 0, time.UTC),
	}
	policy := capture.TrustPolicy{
		Profiles:    profiles,
		Authorities: authorities,
		ProfileBindings: []capture.ProfileBinding{{
			ProjectID: projectID.String(), CollectionID: collectionID.String(), Prefix: profile.Namespace, ObservationProfileID: profileID,
		}},
		ExpectedScope: successor.ExpectedScope{
			SetID: base.ID, ManagedClosureDigest: closureDigest, Revisions: expectedRevisions,
		},
		Assignment: assignment,
	}
	request := capture.Request{
		Base: base, CaptureID: "capture-manifest-capture", AuthorityID: "authority-manifest-capture", KeyID: "key-manifest-capture",
		StartedAt: time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC), CompletedAt: time.Date(2026, 9, 10, 0, 1, 0, 0, time.UTC),
		Assignment: assignment,
	}
	verifier := manifestCaptureObservationVerifier{versions: map[string][]byte{observed.VersionID: bytes.Clone(content)}}
	signer := capture.SignerFunc(func(_ context.Context, signing capture.SigningRequest) ([]byte, error) {
		return ed25519.Sign(privateKey, signing.Payload), nil
	})
	resolver := capture.PolicyScopeResolverFunc(func(context.Context, capture.Request) (capture.TrustPolicy, error) { return policy, nil })
	clock := capture.ClockFunc(func() time.Time { return time.Date(2026, 9, 10, 1, 0, 0, 0, time.UTC) })
	service, err := capture.New(source, observations, verifier, signer, resolver, clock)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Capture(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence.Manifest.Revisions) != 1 || len(result.Evidence.Manifest.Revisions[0].Files) != 1 {
		t.Fatalf("captured managed closure = %#v", result.Evidence.Manifest.Revisions)
	}
	gotRevision := result.Evidence.Manifest.Revisions[0]
	if gotRevision.ProjectID != projectID.String() || gotRevision.CollectionID != collectionID.String() || gotRevision.RevisionID != "revision_manifest_capture" {
		t.Fatalf("captured managed revision identity = %#v", gotRevision)
	}
	gotFile := gotRevision.Files[0]
	if gotFile.Provider.VersionID != observed.VersionID || gotFile.Provider.Key != objectKey {
		t.Fatalf("captured exact provider identity = %#v", gotFile.Provider)
	}

	reader := &manifestCapturePayloadReader{objects: make(map[ValidatedLocator][]byte)}
	payloads := manifestCapturePayloads(t, result, reader)
	trust := TrustInput{Evidence: result.Evidence, Generation: TrustGeneration{
		IncarnationID: assignment.Generation.IncarnationID, Revision: assignment.Generation.Revision, PolicyDigest: assignment.Generation.PolicyDigest,
	}}
	if _, err := admin.Exec(ctx, `INSERT INTO recovery.successor_trust_generation(singleton,incarnation_id,revision,policy_digest) VALUES(true,$1,$2,$3)`, trust.Generation.IncarnationID, trust.Generation.Revision, trust.Generation.PolicyDigest); err != nil {
		t.Fatal(err)
	}
	repository := NewSuccessorRepository(pool, SuccessorOptions{Reader: reader,
		Trust: func(context.Context, string) (TrustInput, error) { return trust, nil }})
	input := Set3Input{Set: result.Set, Payloads: payloads}
	created, err := repository.CreateSet3(ctx, input)
	if err != nil {
		t.Fatalf("CreateSet3: %v", err)
	}
	createdBytes, err := created.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(createdBytes, payloads.Set.CanonicalBytes) {
		t.Fatal("stored set input was not the service's canonical set bytes")
	}

	// Exact retries are byte-identical and the repository converges under
	// concurrent identical requests.
	for range 2 {
		retried, retryErr := repository.CreateSet3(ctx, input)
		if retryErr != nil {
			t.Fatalf("exact CreateSet3 retry: %v", retryErr)
		}
		retriedBytes, _ := retried.CanonicalJSON()
		if !bytes.Equal(createdBytes, retriedBytes) {
			t.Fatal("exact CreateSet3 retry changed canonical set bytes")
		}
	}
	const workers = 8
	errResults := make(chan error, workers)
	start := make(chan struct{})
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, err := repository.CreateSet3(context.Background(), input)
			errResults <- err
		}()
	}
	close(start)
	group.Wait()
	close(errResults)
	for err := range errResults {
		if err != nil {
			t.Fatalf("concurrent identical CreateSet3: %v", err)
		}
	}

	// A separately valid capture for the same set identity cannot replace the
	// immutable winner, even when its own receipt and evidence graph verify.
	conflictingRequest := request
	conflictingRequest.CaptureID = "capture-manifest-conflict"
	conflicting, err := service.Capture(ctx, conflictingRequest)
	if err != nil {
		t.Fatalf("construct conflicting capture: %v", err)
	}
	conflictingTrust := trust
	conflictingTrust.Evidence = conflicting.Evidence
	conflictingRepo := NewSuccessorRepository(pool, SuccessorOptions{Reader: reader,
		Trust: func(context.Context, string) (TrustInput, error) { return conflictingTrust, nil }})
	conflictingInput := Set3Input{Set: conflicting.Set, Payloads: manifestCapturePayloads(t, conflicting, reader)}
	if _, err := conflictingRepo.CreateSet3(ctx, conflictingInput); !errors.Is(err, ErrSuccessorConflict) {
		t.Fatalf("conflicting CreateSet3 error = %v, want immutable conflict", err)
	}

	// A separately authenticated/restarted repository still reads every exact
	// payload from the in-memory versioned locator fixture.
	reopened := openManifestCapturePool(t, reopenURL)
	readRepo := NewSuccessorRepository(reopened, SuccessorOptions{Reader: reader,
		Trust: func(context.Context, string) (TrustInput, error) { return trust, nil }})
	read, err := readRepo.ReadSet3(ctx, result.Set.ID)
	if err != nil {
		t.Fatalf("separate repository ReadSet3: %v", err)
	}
	readBytes, err := read.CanonicalJSON()
	if err != nil || !bytes.Equal(readBytes, createdBytes) {
		t.Fatalf("separate repository canonical set = %q, err=%v", readBytes, err)
	}
	reader.mu.Lock()
	if reader.reads == 0 || reader.reads != reader.closes {
		t.Fatalf("exact payload readers leaked: reads=%d closes=%d", reader.reads, reader.closes)
	}
	reader.mu.Unlock()

	stale := trust
	stale.Generation.Revision++
	staleRepo := NewSuccessorRepository(reopened, SuccessorOptions{Reader: reader,
		Trust: func(context.Context, string) (TrustInput, error) { return stale, nil }})
	if _, err := staleRepo.ReadSet3(ctx, result.Set.ID); !errors.Is(err, ErrSuccessorConflict) {
		t.Fatalf("stale trust generation error = %v, want conflict", err)
	}
	badSignature := trust
	badSignature.Evidence.Receipt.Signature = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	badRepo := NewSuccessorRepository(reopened, SuccessorOptions{Reader: reader,
		Trust: func(context.Context, string) (TrustInput, error) { return badSignature, nil }})
	if _, err := badRepo.ReadSet3(ctx, result.Set.ID); !errors.Is(err, ErrSuccessorTampered) {
		t.Fatalf("receipt substitution error = %v, want tampered evidence", err)
	}
}

func openManifestCapturePool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	p, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func manifestCaptureBase(t *testing.T) recoveryset.RecoverySet {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "successor-legacy", "recoveryset-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	base, err := recoveryset.ParseRecoverySet(bytes.TrimSpace(raw))
	if err != nil {
		t.Fatal(err)
	}
	return base
}

func manifestCapturePayloads(t *testing.T, result capture.Result, reader *manifestCapturePayloadReader) EvidencePayloads {
	t.Helper()
	setDigest, err := result.Set.Digest()
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest, err := result.Evidence.Manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	anchorDigest, err := result.Evidence.Anchor.Digest()
	if err != nil {
		t.Fatal(err)
	}
	profilesDigest, err := result.Evidence.Profiles.Digest()
	if err != nil {
		t.Fatal(err)
	}
	coreDigest, err := result.Evidence.Receipt.Core.Digest()
	if err != nil {
		t.Fatal(err)
	}
	receiptDigest, err := result.Evidence.Receipt.Digest()
	if err != nil {
		t.Fatal(err)
	}
	authorityDigest, err := result.Evidence.Authorities.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return EvidencePayloads{
		Set:       manifestCapturePayloadRef(t, PayloadFamilySet, successor.RecoverySetVersion, setDigest, result.Documents.Set, reader, "set"),
		Manifest:  manifestCapturePayloadRef(t, PayloadFamilyManifest, successor.ManagedManifestVersion, manifestDigest, result.Documents.Manifest, reader, "manifest"),
		Anchor:    manifestCapturePayloadRef(t, PayloadFamilyAnchor, successor.SourceAnchorVersion, anchorDigest, result.Documents.Anchor, reader, "anchor"),
		Profiles:  manifestCapturePayloadRef(t, PayloadFamilyProfiles, successor.ProviderProfileVersion, profilesDigest, result.Documents.Profiles, reader, "profiles"),
		Core:      manifestCapturePayloadRef(t, PayloadFamilyCore, successor.ReceiptCoreVersion, coreDigest, result.Documents.Core, reader, "core"),
		Receipt:   manifestCapturePayloadRef(t, PayloadFamilyReceipt, successor.ReceiptVersion, receiptDigest, result.Documents.Receipt, reader, "receipt"),
		Authority: manifestCapturePayloadRef(t, PayloadFamilyAuthority, successor.AuthorityRegistryVersion, authorityDigest, result.Documents.Authorities, reader, "authority"),
	}
}

func manifestCapturePayloadRef(t *testing.T, family string, version int32, digest string, raw []byte, reader *manifestCapturePayloadReader, name string) PayloadReference {
	t.Helper()
	sum := sha256.Sum256(raw)
	sha := hex.EncodeToString(sum[:])
	locator := ValidatedLocator{
		Backend: "s3", StorageProfileID: "22222222-2222-4222-8222-222222222222", StorageProfileRevision: 1,
		AccountIdentity: "qualification", Endpoint: "https://evidence.example.test", Region: "test-region-1", Bucket: "evidence-bucket", Namespace: "evidence",
		Key:       "evidence/" + strings.TrimPrefix(family, "leapview.") + "/v" + strconv.Itoa(int(version)) + "/sha256/" + sha,
		VersionID: "immutable-" + name, PayloadFamily: family, PayloadVersion: version, PayloadDigest: digest, PayloadSHA256: sha, PayloadSize: int64(len(raw)),
	}
	if err := locator.Validate(); err != nil {
		t.Fatalf("%s locator: %v", name, err)
	}
	reader.mu.Lock()
	reader.objects[locator] = bytes.Clone(raw)
	reader.mu.Unlock()
	return PayloadReference{Locator: locator, CanonicalBytes: bytes.Clone(raw)}
}

// manifestCaptureObservationVerifier models the exact-version provider
// boundary: the observed VersionID chooses immutable bytes, and the digest
// and size returned by PostgreSQL must agree with those bytes.
type manifestCaptureObservationVerifier struct {
	versions map[string][]byte
}

func (v manifestCaptureObservationVerifier) VerifyExact(_ context.Context, observation storage.ProviderVersionObservation) error {
	raw, ok := v.versions[observation.VersionID]
	if !ok {
		return errors.New("exact provider version is unavailable")
	}
	sum := sha256.Sum256(raw)
	if observation.Size != int64(len(raw)) || observation.SHA256 != hex.EncodeToString(sum[:]) {
		return errors.New("exact provider bytes do not match durable observation")
	}
	return nil
}

func sameManifestCaptureObservation(left, right storage.ProviderVersionObservation) bool {
	return left.Profile == right.Profile && left.ObjectKey == right.ObjectKey && left.VersionID == right.VersionID &&
		left.SHA256 == right.SHA256 && left.Size == right.Size && left.CapturedAt.Equal(right.CapturedAt)
}

type manifestCapturePayloadReader struct {
	mu      sync.Mutex
	objects map[ValidatedLocator][]byte
	reads   int
	closes  int
}

type manifestCapturePayloadBody struct {
	io.Reader
	close func()
}

func (b *manifestCapturePayloadBody) Close() error {
	b.close()
	return nil
}

func (r *manifestCapturePayloadReader) ReadExact(ctx context.Context, locator ValidatedLocator) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	raw, ok := r.objects[locator]
	if ok {
		raw = bytes.Clone(raw)
		r.reads++
	}
	r.mu.Unlock()
	if !ok {
		return nil, errors.New("exact payload version unavailable")
	}
	return &manifestCapturePayloadBody{Reader: bytes.NewReader(raw), close: func() {
		r.mu.Lock()
		r.closes++
		r.mu.Unlock()
	}}, nil
}
