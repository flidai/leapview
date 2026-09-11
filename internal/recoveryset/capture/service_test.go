package capture

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/storage"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/flidai/leapview/internal/recoveryset/successor"
)

func TestFAI520CaptureCanonicalizesSameFixedCapturedEvidence(t *testing.T) {
	service, request, _ := captureFixture(t)
	first, err := service.Capture(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Capture(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range map[string][2][]byte{
		"set": {first.Documents.Set, second.Documents.Set}, "manifest": {first.Documents.Manifest, second.Documents.Manifest},
		"anchor": {first.Documents.Anchor, second.Documents.Anchor}, "profiles": {first.Documents.Profiles, second.Documents.Profiles},
		"receipt": {first.Documents.Receipt, second.Documents.Receipt}, "authorities": {first.Documents.Authorities, second.Documents.Authorities},
	} {
		if !bytes.Equal(pair[0], pair[1]) {
			t.Fatalf("%s bytes changed for the same fixed captured evidence", name)
		}
	}
	if err := first.Set.ValidateEvidence(first.Evidence); err != nil {
		t.Fatalf("complete evidence graph: %v", err)
	}
	if first.Evidence.Manifest.Revisions[0].Files[0].Provider.VersionID != "version-a" {
		t.Fatalf("manifest provider version = %q", first.Evidence.Manifest.Revisions[0].Files[0].Provider.VersionID)
	}
	if first.Evidence.Anchor.ClusterPoints[0].DatabaseRole != recoveryset.DatabaseControl || first.Evidence.Anchor.ClusterPoints[0].RecoveryIdentity != "postgres:777777:1:0/ABC:provider_observation_capture" {
		t.Fatalf("source anchor did not bind captured PostgreSQL point: %#v", first.Evidence.Anchor.ClusterPoints)
	}
	if bytes.Contains(first.Documents.Receipt, []byte("PRIVATE")) {
		t.Fatal("receipt persisted private key material")
	}
}

func TestCaptureNormalizesPlainClockPrecisionAtPersistenceBoundary(t *testing.T) {
	service, request, _ := captureFixture(t)
	service.clock = ClockFunc(func() time.Time {
		return time.Date(2026, 9, 10, 2, 0, 0, 123456789, time.FixedZone("plain-clock", 3600))
	})
	result, err := service.Capture(t.Context(), request)
	if err != nil {
		t.Fatalf("plain time.Now-style clock rejected: %v", err)
	}
	want := time.Date(2026, 9, 10, 1, 0, 0, 123456000, time.UTC)
	if !result.Evidence.VerificationTime.Equal(want) || result.Evidence.VerificationTime.Location() != time.UTC {
		t.Fatalf("verification time = %v (%s), want %v UTC", result.Evidence.VerificationTime, result.Evidence.VerificationTime.Location(), want)
	}
}

func TestFAI520CaptureContentMutationChangesManifestAndFrontierDigests(t *testing.T) {
	service, request, source := captureFixture(t)
	first, err := service.Capture(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	changed := sha256.Sum256([]byte("different bytes"))
	changedHash := hex.EncodeToString(changed[:])
	file := &source.projection.Revisions[0].Files[0]
	file.SHA256, file.Size = changedHash, int64(len("different bytes"))
	source.projection.Revisions[0].ManifestDigest = manageddata.Manifest{Files: []manageddata.File{{Path: file.Path, SHA256: file.SHA256, Size: file.Size}}}.RevisionID()
	reader := service.observations.(*memoryObservationReader)
	observation := reader.values[observationKey("profile-a", "managed/data.csv")]
	observation.SHA256, observation.Size = file.SHA256, file.Size
	reader.values[observationKey("profile-a", "managed/data.csv")] = observation
	service.verifier.(*memoryObservationVerifier).expected = observation
	second, err := service.Capture(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Set.ManagedObservationManifestDigest == second.Set.ManagedObservationManifestDigest || first.Set.FrontierDigest == second.Set.FrontierDigest {
		t.Fatal("changed managed content preserved manifest or frontier identity")
	}
}

func TestFAI520CaptureNormalizesClosureOrderWithoutMutatingSource(t *testing.T) {
	service, request, source := captureFixture(t)
	second := source.projection.Revisions[0]
	second.RevisionID = "revision-b"
	second.Files = append([]manageddata.CapturedProjectionFile(nil), second.Files...)
	second.Files[0].Path = "z.csv"
	second.ManifestDigest = manageddata.Manifest{Files: []manageddata.File{{Path: "z.csv", SHA256: second.Files[0].SHA256, Size: second.Files[0].Size}}}.RevisionID()
	source.projection.Revisions = append(source.projection.Revisions, second)
	request.Base.ID = "22222222-2222-4222-8222-222222222222"
	request.CaptureID = "capture-b"

	ordered, err := service.Capture(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	source.projection.Revisions[0], source.projection.Revisions[1] = source.projection.Revisions[1], source.projection.Revisions[0]
	reordered, err := service.Capture(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ordered.Documents.Manifest, reordered.Documents.Manifest) || ordered.Set.FrontierDigest != reordered.Set.FrontierDigest {
		t.Fatal("reordered closure changed canonical evidence")
	}
}

func TestFAI520CaptureRejectsIncompleteOrConflictingObservations(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*captureFixtureState)
		want   error
	}{
		{name: "missing observation", want: ErrIncomplete, mutate: func(state *captureFixtureState) { state.reader.values = nil }},
		{name: "wrong exact version", want: ErrIncomplete, mutate: func(state *captureFixtureState) { state.observation.VersionID = "version-b" }},
		{name: "digest mismatch", want: ErrConflict, mutate: func(state *captureFixtureState) { state.observation.SHA256 = strings.Repeat("b", 64) }},
		{name: "closure mismatch", want: ErrConflict, mutate: func(state *captureFixtureState) {
			scope := fixtureExpectedScope(state.resolver.base, state.resolver.source.projection, state.resolver.profiles)
			scope.ManagedClosureDigest = digestOf("other")
			state.resolver.scopeOverride = &scope
		}},
		{name: "stale capture boundary", want: ErrConflict, mutate: func(state *captureFixtureState) {
			state.observation.CapturedAt = state.request.StartedAt.Add(time.Microsecond)
		}},
		{name: "profile substitution", want: ErrConflict, mutate: func(state *captureFixtureState) { state.observation.Profile.AccountIdentity = "other-account" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service, request, source := captureFixture(t)
			state := captureFixtureState{request: request, source: source, reader: service.observations.(*memoryObservationReader), resolver: service.resolver.(*memoryTrustResolver)}
			state.observation = state.reader.values[observationKey("profile-a", "managed/data.csv")]
			tc.mutate(&state)
			if state.reader.values != nil {
				state.reader.values[observationKey("profile-a", "managed/data.csv")] = state.observation
			}
			_, err := service.Capture(t.Context(), state.request)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want category %v", err, tc.want)
			}
		})
	}
}

func TestFAI520CaptureRejectsAuthorityAndSignatureFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Request, *Service)
	}{
		{name: "unknown authority", mutate: func(request *Request, _ *Service) { request.AuthorityID = "unknown-authority" }},
		{name: "revoked authority", mutate: func(_ *Request, service *Service) {
			service.resolver.(*memoryTrustResolver).policy.Authorities.Keys[0].Revoked = true
		}},
		{name: "stale authority validity", mutate: func(_ *Request, service *Service) {
			service.resolver.(*memoryTrustResolver).policy.Authorities.Keys[0].NotAfter = "2026-09-10T00:00:30.000000Z"
		}},
		{name: "signature mismatch", mutate: func(_ *Request, service *Service) {
			service.signer = SignerFunc(func(context.Context, SigningRequest) ([]byte, error) { return make([]byte, ed25519.SignatureSize), nil })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, request, _ := captureFixture(t)
			tc.mutate(&request, service)
			if _, err := service.Capture(t.Context(), request); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want invalid evidence", err)
			}
		})
	}
}

func TestFAI520CaptureRejectsStaleGenerationAndWorkerFence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*memoryTrustResolver)
	}{
		{name: "generation", mutate: func(resolver *memoryTrustResolver) { resolver.policy.Assignment.Generation.Revision++ }},
		{name: "worker fence", mutate: func(resolver *memoryTrustResolver) { resolver.policy.Assignment.WorkerFence++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, request, _ := captureFixture(t)
			resolver := service.resolver.(*memoryTrustResolver)
			tc.mutate(resolver)
			if _, err := service.Capture(t.Context(), request); !errors.Is(err, ErrConflict) {
				t.Fatalf("error = %v, want stale assignment conflict", err)
			}
		})
	}
}

func TestFAI520CaptureRejectsTrustChangeBeforeSigning(t *testing.T) {
	service, request, source := captureFixture(t)
	resolver := service.resolver.(*memoryTrustResolver)
	second := resolver.policy
	second.Authorities = successor.AuthorityRegistry{RegistryVersion: successor.AuthorityRegistryVersion, Keys: append([]successor.AuthorityKey(nil), resolver.policy.Authorities.Keys...)}
	second.Authorities.Keys[0].Revoked = true
	second.ExpectedScope = fixtureExpectedScope(resolver.base, source.projection, resolver.profiles)
	resolver.second = &second
	if _, err := service.Capture(t.Context(), request); !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want trust-change conflict", err)
	}
}

func TestFAI520CaptureObservationErrorsAreStaticButTyped(t *testing.T) {
	service, request, _ := captureFixture(t)
	cause := &secretObservationCause{}
	service.observations = failingObservationReader{cause: cause}
	_, err := service.Capture(t.Context(), request)
	if !errors.Is(err, ErrIncomplete) || !errors.Is(err, cause) {
		t.Fatalf("error = %v, want incomplete and typed observation cause", err)
	}
	if strings.Contains(err.Error(), "SECRET-DO-NOT-LEAK") {
		t.Fatalf("observation error leaked provider diagnostic: %v", err)
	}
}

func TestFAI520CaptureProviderPrefixUsesCanonicalRelativeSegments(t *testing.T) {
	for value, want := range map[string]bool{
		"": true, "managed": true, "managed/orders": true,
		"/managed": false, "managed/": false, "managed//orders": false,
		"managed/./orders": false, "managed/../orders": false, `managed\orders`: false,
	} {
		if got := canonicalPrefix(value); got != want {
			t.Errorf("canonicalPrefix(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestFAI520CaptureSignerReceivesFrozenTrustContext(t *testing.T) {
	service, request, _ := captureFixture(t)
	original := service.signer
	var received SigningRequest
	service.signer = SignerFunc(func(ctx context.Context, signing SigningRequest) ([]byte, error) {
		received = signing
		return original.Sign(ctx, signing)
	})
	if _, err := service.Capture(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if len(received.Payload) == 0 || received.AuthorityID != request.AuthorityID || received.KeyID != request.KeyID || received.Generation != request.Assignment.Generation || received.WorkerFence != request.Assignment.WorkerFence || !received.Deadline.Equal(request.Assignment.Deadline) {
		t.Fatalf("signing request omitted frozen trust context: %#v", received)
	}
	if received.Authority.PublicKey == "" || len(received.Authority.ProviderProfileDigests) == 0 {
		t.Fatalf("signing request omitted immutable authority key: %#v", received.Authority)
	}
}

func TestFAI520CaptureConcurrentIdenticalRequestsRemainImmutable(t *testing.T) {
	service, request, _ := captureFixture(t)
	const workers = 8
	results := make([]Result, workers)
	errs := make([]error, workers)
	start := make(chan struct{})
	var group sync.WaitGroup
	for i := range results {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			results[i], errs[i] = service.Capture(context.Background(), request)
		}()
	}
	close(start)
	group.Wait()
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("capture %d: %v", i, errs[i])
		}
		if !bytes.Equal(results[0].Documents.Manifest, results[i].Documents.Manifest) || results[0].Set.FrontierDigest != results[i].Set.FrontierDigest {
			t.Fatalf("capture %d differs from immutable winner", i)
		}
	}
}

type captureFixtureState struct {
	request     Request
	source      *staticProjectionSource
	reader      *memoryObservationReader
	resolver    *memoryTrustResolver
	observation storage.ProviderVersionObservation
}

type staticProjectionSource struct {
	projection manageddata.CapturedProjection
}

func (s *staticProjectionSource) CaptureManagedProjection(context.Context) (manageddata.CapturedProjection, error) {
	return s.projection, nil
}

type memoryObservationReader struct {
	mu     sync.RWMutex
	values map[string]storage.ProviderVersionObservation
}

type memoryObservationVerifier struct {
	expected storage.ProviderVersionObservation
}

func (v *memoryObservationVerifier) VerifyExact(_ context.Context, observation storage.ProviderVersionObservation) error {
	if observation != v.expected {
		return errors.New("exact provider version did not match verified bytes")
	}
	return nil
}

type memoryTrustResolver struct {
	mu            sync.RWMutex
	policy        TrustPolicy
	second        *TrustPolicy
	calls         int
	source        *staticProjectionSource
	base          recoveryset.RecoverySet
	profiles      successor.ProviderProfileSet
	scopeOverride *successor.ExpectedScope
}

func (r *memoryTrustResolver) Resolve(_ context.Context, request Request) (TrustPolicy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	policy := r.policy
	r.calls++
	if r.calls > 1 && r.second != nil {
		policy = *r.second
	}
	if r.scopeOverride != nil {
		policy.ExpectedScope = *r.scopeOverride
	} else {
		policy.ExpectedScope = fixtureExpectedScope(request.Base, r.source.projection, r.profiles)
	}
	return policy, nil
}

type fixedClock struct{ value time.Time }

func (c fixedClock) Now() time.Time { return c.value }

type secretObservationCause struct{}

func (*secretObservationCause) Error() string { return "provider token SECRET-DO-NOT-LEAK" }

type failingObservationReader struct{ cause error }

func (r failingObservationReader) ProviderVersionObservation(context.Context, string, string) (storage.ProviderVersionObservation, error) {
	return storage.ProviderVersionObservation{}, r.cause
}

func fixtureExpectedScope(base recoveryset.RecoverySet, projection manageddata.CapturedProjection, _ successor.ProviderProfileSet) successor.ExpectedScope {
	revisions := make([]successor.Revision, 0, len(projection.Revisions))
	for _, captured := range projection.Revisions {
		revision := successor.Revision{ProjectID: captured.ProjectID, CollectionID: captured.CollectionID, RevisionID: captured.RevisionID, RevisionManifestDigest: captured.ManifestDigest, Files: make([]successor.File, 0, len(captured.Files))}
		for _, file := range captured.Files {
			revision.Files = append(revision.Files, successor.File{Path: file.Path, SHA256: file.SHA256, Size: file.Size})
		}
		revisions = append(revisions, revision)
	}
	closure, _ := successor.ClosureDigest(revisions)
	return successor.ExpectedScope{SetID: base.ID, ManagedClosureDigest: closure, Revisions: revisions, EmptyScopeVerified: len(revisions) == 0}
}

func (r *memoryObservationReader) ProviderVersionObservation(_ context.Context, profileID, objectKey string) (storage.ProviderVersionObservation, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, ok := r.values[observationKey(profileID, objectKey)]
	if !ok {
		return storage.ProviderVersionObservation{}, errors.New("observation not found")
	}
	return value, nil
}

func observationKey(profileID, objectKey string) string { return profileID + "\x00" + objectKey }

func captureFixture(t *testing.T) (*Service, Request, *staticProjectionSource) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "successor-legacy", "recoveryset-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	base, err := recoveryset.ParseRecoverySet(bytes.TrimSpace(raw))
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("historical bytes")
	hash := sha256.Sum256(content)
	fileHash := hex.EncodeToString(hash[:])
	manifestDigest := manageddata.Manifest{Files: []manageddata.File{{Path: "data.csv", SHA256: fileHash, Size: int64(len(content))}}}.RevisionID()
	source := &staticProjectionSource{projection: manageddata.CapturedProjection{
		DatabaseIdentity: "control-db", SystemIdentity: "777777", Timeline: 1, LSN: "0/ABC", RestorePointName: "provider_observation_capture",
		Revisions: []manageddata.CapturedProjectionRevision{{ProjectID: "project-a", CollectionID: "collection-a", RevisionID: "revision-a", ManifestDigest: manifestDigest,
			Files: []manageddata.CapturedProjectionFile{{Path: "data.csv", SHA256: fileHash, StorageKey: "s3://bucket-a/managed/data.csv", Size: int64(len(content))}}}},
	}}
	profile := storage.ProviderProfileIdentity{ProfileID: "profile-a", Implementation: "s3", AccountIdentity: "account-a", Endpoint: "https://objects.example.com", Region: "us-east-1", Bucket: "bucket-a", Namespace: "managed"}
	observation := storage.ProviderVersionObservation{Profile: profile, ObjectKey: "managed/data.csv", VersionID: "version-a", SHA256: fileHash, Size: int64(len(content)), CapturedAt: time.Date(2026, 9, 9, 23, 59, 0, 0, time.UTC)}
	reader := &memoryObservationReader{values: map[string]storage.ProviderVersionObservation{observationKey(profile.ProfileID, observation.ObjectKey): observation}}
	profiles := successor.ProviderProfileSet{ProfileVersion: successor.ProviderProfileVersion, Profiles: []successor.ProviderProfile{{
		Implementation: "s3", AccountIdentity: profile.AccountIdentity, Endpoint: profile.Endpoint, Region: profile.Region, Bucket: profile.Bucket, VersionSemantics: "opaque-exact-version",
		Namespaces: []successor.Namespace{{ProjectID: "project-a", CollectionID: "collection-a", Prefix: "managed"}},
	}}}
	profileDigest, err := profiles.Digest()
	if err != nil {
		t.Fatal(err)
	}
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, ed25519.SeedSize))
	authorities := successor.AuthorityRegistry{RegistryVersion: successor.AuthorityRegistryVersion, Keys: []successor.AuthorityKey{{
		AuthorityID: "authority-a", KeyID: "key-a", Algorithm: "Ed25519", PublicKey: base64.StdEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey)),
		NotBefore: "2026-09-09T00:00:00.000000Z", NotAfter: "2026-09-11T00:00:00.000000Z", ProviderProfileDigests: []string{profileDigest},
	}}}
	signer := SignerFunc(func(_ context.Context, request SigningRequest) ([]byte, error) {
		return ed25519.Sign(privateKey, request.Payload), nil
	})
	verifier := &memoryObservationVerifier{expected: observation}
	request := Request{
		Base: base, CaptureID: "capture-a", AuthorityID: "authority-a", KeyID: "key-a",
		StartedAt: time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC), CompletedAt: time.Date(2026, 9, 10, 0, 1, 0, 0, time.UTC),
		Assignment: TrustAssignment{Generation: TrustGeneration{IncarnationID: "11111111-1111-4111-8111-111111111111", Revision: 1, PolicyDigest: "sha256:" + strings.Repeat("9", 64)}, WorkerFence: 7, Deadline: time.Date(2099, 9, 10, 2, 0, 0, 0, time.UTC)},
	}
	resolver := &memoryTrustResolver{source: source, base: base, profiles: profiles, policy: TrustPolicy{Profiles: profiles, Authorities: authorities, ProfileBindings: []ProfileBinding{{ProjectID: "project-a", CollectionID: "collection-a", Prefix: "managed", ObservationProfileID: profile.ProfileID}}}}
	service, err := New(source, reader, verifier, signer, resolver, fixedClock{value: time.Date(2026, 9, 10, 1, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	resolver.policy.Assignment = request.Assignment
	// The resolver computes expected scope from its independent closure view.
	resolver.policy.ExpectedScope = fixtureExpectedScope(base, source.projection, profiles)
	return service, request, source
}

func digestOf(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}
