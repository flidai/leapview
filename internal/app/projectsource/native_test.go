package projectsource

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/objectstore"
	"github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectpostgres "github.com/flidai/leapview/internal/project/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type nativeTestRepo struct {
	mu           sync.Mutex
	plans        map[uuid.UUID]projectpostgres.SyncPlan
	blobs        map[string]projectpostgres.SourceBlob
	snapshots    map[string]projectpostgres.SourceSnapshot
	snapshotRefs map[string][]projectpostgres.SourceSnapshotObjectRef
	attestations map[string]projectpostgres.SourceAttestation
	refsCalls    int
	active       *bool
	expired      bool
}

func newNativeTestRepo() *nativeTestRepo {
	return &nativeTestRepo{plans: map[uuid.UUID]projectpostgres.SyncPlan{}, blobs: map[string]projectpostgres.SourceBlob{}, snapshots: map[string]projectpostgres.SourceSnapshot{}, snapshotRefs: map[string][]projectpostgres.SourceSnapshotObjectRef{}, attestations: map[string]projectpostgres.SourceAttestation{}}
}
func (r *nativeTestRepo) checkTx() {
	if r.active != nil && !*r.active {
		panic("test transaction unexpectedly inactive")
	}
}
func (r *nativeTestRepo) checkOutsideTx() {
	if r.active != nil && *r.active {
		panic("database read unexpectedly occurred while transaction active")
	}
}
func (r *nativeTestRepo) CreateSyncPlanTx(_ context.Context, _ projectpostgres.SourceTx, in projectpostgres.SyncPlanInput) (projectpostgres.SyncPlan, error) {
	r.checkTx()
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.plans[in.PlanID]; ok {
		if old.RequestDigest != in.RequestDigest || old.ProjectID != in.ProjectID || old.StorageSecurityDomain != in.StorageSecurityDomain || old.OwnerID != in.OwnerID || old.SourceDigest != in.SourceDigest {
			return projectpostgres.SyncPlan{}, projectpostgres.ErrConflict
		}
		return old, nil
	}
	p := projectpostgres.SyncPlan{PlanID: in.PlanID, OperationID: in.OperationID, ProjectID: in.ProjectID, StorageSecurityDomain: in.StorageSecurityDomain, OwnerID: in.OwnerID, CandidateKey: in.CandidateKey, SourceDigest: in.SourceDigest, ProjectFile: in.ProjectFile, RequestDigest: in.RequestDigest, State: "open", ExpiresAt: in.ExpiresAt, CreatedAt: time.Now().UTC()}
	p.Entries = make([]projectpostgres.SourceSyncPlanEntry, len(in.Entries))
	for i, e := range in.Entries {
		p.Entries[i] = projectpostgres.SourceSyncPlanEntry{PlanID: in.PlanID, Path: e.Path, Digest: e.Digest, SizeBytes: e.SizeBytes, Ordinal: i}
	}
	r.plans[in.PlanID] = p
	return p, nil
}
func (r *nativeTestRepo) SyncPlanForUpdateTx(_ context.Context, _ projectpostgres.SourceTx, id uuid.UUID) (projectpostgres.SyncPlan, error) {
	r.checkTx()
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.plans[id]
	if !ok {
		return projectpostgres.SyncPlan{}, projectpostgres.ErrNotFound
	}
	return p, nil
}
func (r *nativeTestRepo) ListMissingPlanSourceBlobDigestsTx(_ context.Context, _ projectpostgres.SourceTx, id uuid.UUID, owner string) ([]string, error) {
	r.checkTx()
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.plans[id]
	if !ok {
		return nil, projectpostgres.ErrNotFound
	}
	if p.OwnerID != owner {
		return nil, projectpostgres.ErrSourceWrongOwner
	}
	if r.expired {
		return nil, projectpostgres.ErrSourceExpired
	}
	seen := map[string]bool{}
	out := []string{}
	for _, e := range p.Entries {
		if _, ok := r.blobs[e.Digest]; !ok && !seen[e.Digest] {
			out = append(out, e.Digest)
			seen[e.Digest] = true
		}
	}
	return out, nil
}
func (r *nativeTestRepo) InsertSourceBlobTx(_ context.Context, _ projectpostgres.SourceTx, in projectpostgres.SourceBlobInput) (projectpostgres.SourceBlob, error) {
	r.checkTx()
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.plans[in.PlanID]
	if !ok || p.OwnerID != in.OwnerID {
		return projectpostgres.SourceBlob{}, projectpostgres.ErrSourceWrongOwner
	}
	if r.expired {
		return projectpostgres.SourceBlob{}, projectpostgres.ErrSourceExpired
	}
	if old, ok := r.blobs[in.Digest]; ok {
		return old, nil
	}
	b := projectpostgres.SourceBlob{ProjectID: in.ProjectID, StorageSecurityDomain: in.StorageSecurityDomain, Digest: in.Digest, SizeBytes: in.SizeBytes, ObjectKey: in.ObjectKey, ContentType: in.ContentType, MetadataDigest: in.MetadataDigest}
	r.blobs[in.Digest] = b
	return b, nil
}
func (r *nativeTestRepo) PlanSourceObjectRefsTx(_ context.Context, _ projectpostgres.SourceTx, id uuid.UUID, owner string) ([]projectpostgres.SourceSyncPlanObjectRef, error) {
	r.checkTx()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refsCalls++
	p, ok := r.plans[id]
	if !ok || p.OwnerID != owner {
		return nil, projectpostgres.ErrSourceWrongOwner
	}
	if r.expired {
		return nil, projectpostgres.ErrSourceExpired
	}
	out := make([]projectpostgres.SourceSyncPlanObjectRef, len(p.Entries))
	for i, e := range p.Entries {
		b, ok := r.blobs[e.Digest]
		if !ok {
			return nil, projectpostgres.ErrSourceConflict
		}
		out[i] = projectpostgres.SourceSyncPlanObjectRef{PlanID: id, ProjectID: p.ProjectID, StorageSecurityDomain: p.StorageSecurityDomain, Path: e.Path, Digest: e.Digest, SizeBytes: e.SizeBytes, Ordinal: i, ObjectKey: b.ObjectKey, ContentType: b.ContentType, MetadataDigest: b.MetadataDigest}
	}
	return out, nil
}
func (r *nativeTestRepo) CommitSnapshotTx(_ context.Context, _ projectpostgres.SourceTx, in projectpostgres.CommitSnapshotInput) (projectpostgres.SourceSnapshot, error) {
	r.checkTx()
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.plans[in.PlanID]
	if p.OwnerID != in.OwnerID {
		return projectpostgres.SourceSnapshot{}, projectpostgres.ErrSourceWrongOwner
	}
	if r.expired {
		return projectpostgres.SourceSnapshot{}, projectpostgres.ErrSourceExpired
	}
	key := in.ProjectID + "\x00" + in.StorageSecurityDomain + "\x00" + in.SourceDigest
	if old, ok := r.snapshots[key]; ok {
		if old.SnapshotID != in.SnapshotID || old.ProjectDigest != in.ProjectDigest {
			return projectpostgres.SourceSnapshot{}, projectpostgres.ErrSourceConflict
		}
		p.State = "committed"
		r.plans[in.PlanID] = p
		r.attestations[in.Attestation.AttestationDigest] = projectpostgres.SourceAttestation{AttestationID: in.Attestation.AttestationID, SnapshotID: in.SnapshotID, SourceDigest: in.SourceDigest, AttestationDigest: in.Attestation.AttestationDigest, Payload: in.Attestation.Payload, Revision: in.Attestation.Revision, Repository: in.Attestation.Repository, Ref: in.Attestation.Ref, ChangeID: in.Attestation.ChangeID}
		return old, nil
	}
	snap := projectpostgres.SourceSnapshot{SnapshotID: in.SnapshotID, ProjectID: in.ProjectID, StorageSecurityDomain: in.StorageSecurityDomain, SourceDigest: in.SourceDigest, ProjectFile: in.ProjectFile, ProjectDigest: in.ProjectDigest, ProjectArtifactObjectKey: in.ProjectArtifactObjectKey, ProjectArtifactDigest: in.ProjectArtifactDigest, ProjectArtifactSizeBytes: in.ProjectArtifactSizeBytes, ManifestObjectKey: in.ManifestObjectKey, ManifestObjectDigest: in.ManifestObjectDigest, ManifestObjectSizeBytes: in.ManifestObjectSizeBytes, CompilerVersion: in.CompilerVersion, SchemaVersion: in.SchemaVersion}
	r.snapshots[key] = snap
	refs := make([]projectpostgres.SourceSnapshotObjectRef, len(in.Entries))
	for i, e := range in.Entries {
		b := r.blobs[e.Digest]
		refs[i] = projectpostgres.SourceSnapshotObjectRef{SnapshotID: in.SnapshotID, ProjectID: in.ProjectID, StorageSecurityDomain: in.StorageSecurityDomain, Path: e.Path, Digest: e.Digest, SizeBytes: e.SizeBytes, Ordinal: i, ObjectKey: b.ObjectKey, ContentType: b.ContentType, MetadataDigest: b.MetadataDigest}
	}
	r.snapshotRefs[key] = refs
	p.State = "committed"
	r.plans[in.PlanID] = p
	r.attestations[in.Attestation.AttestationDigest] = projectpostgres.SourceAttestation{AttestationID: in.Attestation.AttestationID, SnapshotID: in.SnapshotID, SourceDigest: in.SourceDigest, AttestationDigest: in.Attestation.AttestationDigest, Payload: in.Attestation.Payload, Revision: in.Attestation.Revision, Repository: in.Attestation.Repository, Ref: in.Attestation.Ref, ChangeID: in.Attestation.ChangeID}
	return snap, nil
}
func (r *nativeTestRepo) Snapshot(_ context.Context, projectID, domain, digest string) (projectpostgres.SourceSnapshot, error) {
	r.checkOutsideTx()
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.snapshots[projectID+"\x00"+domain+"\x00"+digest]
	if !ok {
		return projectpostgres.SourceSnapshot{}, projectpostgres.ErrNotFound
	}
	return s, nil
}
func (r *nativeTestRepo) SourceBlob(_ context.Context, _, _, digest string) (projectpostgres.SourceBlob, error) {
	r.checkOutsideTx()
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.blobs[digest]
	if !ok {
		return projectpostgres.SourceBlob{}, projectpostgres.ErrNotFound
	}
	return b, nil
}
func (r *nativeTestRepo) SnapshotSourceObjectRefs(_ context.Context, projectID, domain, digest string) ([]projectpostgres.SourceSnapshotObjectRef, error) {
	r.checkOutsideTx()
	r.mu.Lock()
	defer r.mu.Unlock()
	refs, ok := r.snapshotRefs[projectID+"\x00"+domain+"\x00"+digest]
	if !ok {
		return nil, projectpostgres.ErrNotFound
	}
	return append([]projectpostgres.SourceSnapshotObjectRef(nil), refs...), nil
}
func (r *nativeTestRepo) SnapshotAttestation(_ context.Context, _ uuid.UUID, digest string) (projectpostgres.SourceAttestation, error) {
	r.checkOutsideTx()
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.attestations[digest]
	if !ok {
		return projectpostgres.SourceAttestation{}, projectpostgres.ErrNotFound
	}
	return a, nil
}

type nativeTestTx struct{ active *bool }

func (t *nativeTestTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (t *nativeTestTx) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (t *nativeTestTx) QueryRow(context.Context, string, ...any) pgx.Row        { return nil }
func (t *nativeTestTx) Commit(context.Context) error                            { *t.active = false; return nil }
func (t *nativeTestTx) Rollback(context.Context) error                          { *t.active = false; return nil }

func nativeTestRequest() (project.CandidateSynchronizationRequest, []byte) {
	body := []byte("source")
	digest := sha256Identity(body)
	return project.CandidateSynchronizationRequest{ProjectFile: "leapview.yaml", ArtifactDigest: "", IdempotencyKey: "idem-1", Artifacts: []project.CandidateSourceArtifact{{Path: "leapview.yaml", Digest: digest, SizeBytes: int64(len(body))}}}, body
}
func newNativeTestAdapter(repo *nativeTestRepo, store objectstore.ImmutableStore, compile CompileFunc, now *time.Time) *NativeCandidateSourceSynchronizer {
	var active bool
	begin := func(context.Context) (Tx, error) {
		active = true
		repo.active = &active
		return &nativeTestTx{active: &active}, nil
	}
	tracked := &nativeTrackingStore{inner: store, active: func() bool { return active }}
	config := NativeCandidateSourceConfig{Begin: begin, Sources: repo, Objects: tracked, Compiler: compile, StorageSecurityDomain: "runtime"}
	if now != nil {
		config.Now = func() time.Time { return *now }
	}
	adapter, err := NewNativeCandidateSourceSynchronizer(config)
	if err != nil {
		panic(err)
	}
	return adapter
}

type nativeTrackingStore struct {
	inner  objectstore.ImmutableStore
	active func() bool
}

func (s *nativeTrackingStore) guard() {
	if s.active != nil && s.active() {
		panic("object-store I/O occurred while transaction active")
	}
}
func (s *nativeTrackingStore) PutImmutable(ctx context.Context, key string, body io.Reader, metadata objectstore.ObjectMetadata) (objectstore.ObjectInfo, error) {
	s.guard()
	return s.inner.PutImmutable(ctx, key, body, metadata)
}
func (s *nativeTrackingStore) Open(ctx context.Context, key string) (objectstore.Object, error) {
	s.guard()
	return s.inner.Open(ctx, key)
}
func (s *nativeTrackingStore) List(ctx context.Context, prefix, cursor string, limit int) ([]objectstore.ObjectInfo, string, error) {
	s.guard()
	return s.inner.List(ctx, prefix, cursor, limit)
}
func (s *nativeTrackingStore) Delete(ctx context.Context, key string) error {
	s.guard()
	return s.inner.Delete(ctx, key)
}

func TestNativePlanDeterministicAndDrift(t *testing.T) {
	request, _ := nativeTestRequest()
	store, _ := objectstore.NewMemoryStore(objectstore.MemoryStoreConfig{StorageSecurityDomain: "runtime"})
	scope := project.CandidateSourceScope{ProjectID: projectgraph.ResourceID("project:sales"), OwnerID: "owner", CandidateKey: "default"}
	r1, r2 := newNativeTestRepo(), newNativeTestRepo()
	a1 := newNativeTestAdapter(r1, store, CompileFunc(func(context.Context, CompileInput) (CompileOutput, error) {
		return CompileOutput{}, errors.New("unused")
	}), nil)
	a2 := newNativeTestAdapter(r2, store, CompileFunc(func(context.Context, CompileInput) (CompileOutput, error) {
		return CompileOutput{}, errors.New("unused")
	}), nil)
	p1, err := a1.Plan(context.Background(), scope, request)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := a2.Plan(context.Background(), scope, request)
	if err != nil {
		t.Fatal(err)
	}
	if p1.PlanID != p2.PlanID {
		t.Fatalf("plan ids differ: %q %q", p1.PlanID, p2.PlanID)
	}
	request.Artifacts[0].SizeBytes++
	if _, err := a1.Plan(context.Background(), scope, request); !errors.Is(err, project.ErrCandidateSourceConflict) && !errors.Is(err, project.ErrCandidateSourceInvalid) {
		t.Fatalf("drift error = %v", err)
	}
}

func TestNativePlanUsesDatabaseExpiryResult(t *testing.T) {
	repo := newNativeTestRepo()
	store, _ := objectstore.NewMemoryStore(objectstore.MemoryStoreConfig{StorageSecurityDomain: "runtime"})
	adapter := newNativeTestAdapter(repo, store, CompileFunc(func(context.Context, CompileInput) (CompileOutput, error) {
		return CompileOutput{}, errors.New("unused")
	}), nil)
	request, _ := nativeTestRequest()
	scope := project.CandidateSourceScope{ProjectID: projectgraph.ResourceID("project:sales"), OwnerID: "owner", CandidateKey: "default"}
	if _, err := adapter.Plan(context.Background(), scope, request); err != nil {
		t.Fatal(err)
	}
	repo.expired = true // PostgreSQL clock says the plan is expired.
	if _, err := adapter.Plan(context.Background(), scope, request); !errors.Is(err, project.ErrCandidateSourceConflict) {
		t.Fatalf("expired replay error = %v", err)
	}
}

func TestNativeExpiryIsDatabaseAuthoritativeAcrossPhases(t *testing.T) {
	repo := newNativeTestRepo()
	store, _ := objectstore.NewMemoryStore(objectstore.MemoryStoreConfig{StorageSecurityDomain: "runtime"})
	adapter := newNativeTestAdapter(repo, store, CompileFunc(func(context.Context, CompileInput) (CompileOutput, error) {
		return CompileOutput{ProjectDigest: sha256Identity([]byte("p")), CompilerVersion: "test", SchemaVersion: 1, ProjectArtifact: []byte("a"), Manifest: []byte("m")}, nil
	}), nil)
	request, body := nativeTestRequest()
	scope := project.CandidateSourceScope{ProjectID: projectgraph.ResourceID("project:sales"), OwnerID: "owner", CandidateKey: "default"}
	plan, err := adapter.Plan(context.Background(), scope, request)
	if err != nil {
		t.Fatal(err)
	}
	repo.expired = true
	if err := adapter.Upload(context.Background(), scope, plan.PlanID, request.Artifacts[0].Digest, bytes.NewReader(body)); !errors.Is(err, project.ErrCandidateSourceConflict) {
		t.Fatalf("expired upload error = %v", err)
	}
	request.PlanID = plan.PlanID
	if _, err := adapter.Commit(context.Background(), scope, request); !errors.Is(err, project.ErrCandidateSourceConflict) {
		t.Fatalf("expired commit error = %v", err)
	}
}

func TestNativeUploadCommitReplayAndReaders(t *testing.T) {
	repo := newNativeTestRepo()
	store, _ := objectstore.NewMemoryStore(objectstore.MemoryStoreConfig{StorageSecurityDomain: "runtime"})
	store.SimulateLostCommitAcknowledgement()
	compileCalls := 0
	adapter := newNativeTestAdapter(repo, store, CompileFunc(func(_ context.Context, in CompileInput) (CompileOutput, error) {
		compileCalls++
		if repo.active != nil && *repo.active {
			t.Fatal("compiler callback ran while transaction active")
		}
		if in.Files[0].ObjectKey == "" || len(in.Files[0].Bytes) == 0 {
			t.Fatal("compiler did not receive object-backed bytes")
		}
		return CompileOutput{ProjectDigest: sha256Identity([]byte("project")), CompilerVersion: "test", SchemaVersion: 1, ProjectArtifact: []byte("artifact"), Manifest: []byte("manifest")}, nil
	}), nil)
	request, body := nativeTestRequest()
	request.SourceRevision = &project.CandidateSourceRevision{Revision: "abc", Repository: "repo", Ref: "main", ChangeID: "c1"}
	scope := project.CandidateSourceScope{ProjectID: projectgraph.ResourceID("project:sales"), OwnerID: "owner", CandidateKey: "default"}
	plan, err := adapter.Plan(context.Background(), scope, request)
	if err != nil {
		t.Fatal(err)
	}
	digest := request.Artifacts[0].Digest
	if err := adapter.Upload(context.Background(), scope, plan.PlanID, digest, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Upload(context.Background(), scope, plan.PlanID, digest, bytes.NewReader(body)); err != nil {
		t.Fatalf("upload replay: %v", err)
	}
	request.PlanID = plan.PlanID
	request.IdempotencyKey = ""
	snap, err := adapter.Commit(context.Background(), scope, request)
	if err != nil {
		t.Fatal(err)
	}
	if snap.ProjectPath != "" || snap.ProjectArtifactPath != "" || snap.ProjectFile != "leapview.yaml" || snap.SourceAttestationDigest == "" {
		t.Fatalf("snapshot path/provenance contract: %#v", snap)
	}
	if compileCalls != 1 {
		t.Fatalf("compile calls = %d", compileCalls)
	}
	if repo.refsCalls != 1 {
		t.Fatalf("planned object refs calls = %d, want one join", repo.refsCalls)
	}
	replay, err := adapter.Commit(context.Background(), scope, request)
	if err != nil {
		t.Fatalf("commit replay: %v", err)
	}
	if replay.SourceAttestationDigest != snap.SourceAttestationDigest || compileCalls != 1 {
		t.Fatalf("replay=%#v calls=%d", replay, compileCalls)
	}
	neutral, err := adapter.Snapshot(context.Background(), scope, snap.ArtifactDigest)
	if err != nil {
		t.Fatal(err)
	}
	if neutral.SourceAttestationDigest != "" || neutral.SourceRevision != nil {
		t.Fatalf("snapshot was not provenance neutral: %#v", neutral)
	}
	exact, err := adapter.SnapshotAttestation(context.Background(), scope, snap.ArtifactDigest, snap.SourceAttestationDigest)
	if err != nil {
		t.Fatal(err)
	}
	if exact.SourceAttestationDigest == "" || exact.SourceRevision == nil || exact.SourceRevision.Revision != "abc" {
		t.Fatalf("exact attestation: %#v", exact)
	}
	reader, ok := interface{}(adapter).(project.CandidateSourceObjectReader)
	if !ok {
		t.Fatal("object reader contract missing")
	}
	refs, err := reader.SourceObjectRefs(context.Background(), scope, snap.ArtifactDigest)
	if err != nil || len(refs) != 1 {
		t.Fatalf("source refs = %#v err=%v", refs, err)
	}
	sourceReader, err := reader.OpenSourceObject(context.Background(), scope, refs[0])
	if err != nil {
		t.Fatalf("open source object: %v", err)
	}
	_ = sourceReader.Close()
	forged := refs[0]
	forged.ObjectKey = "source-blobs/" + strings.Repeat("0", 64)
	if _, err := reader.OpenSourceObject(context.Background(), scope, forged); !errors.Is(err, project.ErrCandidateSourceConflict) && !errors.Is(err, project.ErrCandidateSourceInvalid) {
		t.Fatalf("forged source ref error = %v", err)
	}
	artifact, err := reader.OpenProjectArtifact(context.Background(), scope, snap.ArtifactDigest)
	if err != nil {
		t.Fatalf("open object-backed artifact: %v", err)
	}
	defer artifact.Close()
	got, _ := io.ReadAll(artifact)
	if string(got) != "artifact" {
		t.Fatalf("artifact bytes = %q", got)
	}
	// A distinct idempotency key for identical bytes converges on the same
	// deterministic snapshot while appending separate revision evidence.
	secondRequest, secondBody := nativeTestRequest()
	secondRequest.IdempotencyKey = "idem-2"
	secondRequest.SourceRevision = &project.CandidateSourceRevision{Revision: "def", Repository: "repo", Ref: "main", ChangeID: "c2"}
	secondPlan, err := adapter.Plan(context.Background(), scope, secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Upload(context.Background(), scope, secondPlan.PlanID, secondRequest.Artifacts[0].Digest, bytes.NewReader(secondBody)); err != nil {
		t.Fatal(err)
	}
	secondRequest.PlanID = secondPlan.PlanID
	secondRequest.IdempotencyKey = ""
	second, err := adapter.Commit(context.Background(), scope, secondRequest)
	if err != nil {
		t.Fatalf("second revision commit: %v", err)
	}
	if second.ArtifactDigest != snap.ArtifactDigest || second.SourceAttestationDigest == snap.SourceAttestationDigest {
		t.Fatalf("second revision did not append distinct attestation: %#v %#v", snap, second)
	}
	replaySecond, err := adapter.Commit(context.Background(), scope, secondRequest)
	if err != nil || replaySecond.SourceAttestationDigest != second.SourceAttestationDigest || compileCalls != 2 {
		t.Fatalf("second replay=%#v err=%v compile calls=%d", replaySecond, err, compileCalls)
	}
}
