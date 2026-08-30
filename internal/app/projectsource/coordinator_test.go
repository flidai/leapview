package projectsource

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/objectstore"
	projectpostgres "github.com/flidai/leapview/internal/project/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type coordinatorTx struct {
	mu     *sync.Mutex
	flag   *bool
	active bool
	closed bool
}

func (t *coordinatorTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (t *coordinatorTx) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (t *coordinatorTx) QueryRow(context.Context, string, ...any) pgx.Row        { return nil }
func (t *coordinatorTx) Commit(context.Context) error {
	t.mu.Lock()
	t.active = false
	if t.flag != nil {
		*t.flag = false
	}
	t.closed = true
	t.mu.Unlock()
	return nil
}
func (t *coordinatorTx) Rollback(context.Context) error {
	t.mu.Lock()
	t.active = false
	if t.flag != nil {
		*t.flag = false
	}
	t.closed = true
	t.mu.Unlock()
	return nil
}

type coordinatorSource struct {
	mu       sync.Mutex
	plan     projectpostgres.SyncPlan
	blobs    map[string]projectpostgres.SourceBlob
	snapshot projectpostgres.SourceSnapshot
}

func (s *coordinatorSource) CreateSyncPlanTx(_ context.Context, _ projectpostgres.SourceTx, in projectpostgres.SyncPlanInput) (projectpostgres.SyncPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.plan.PlanID != uuid.Nil {
		if s.plan.PlanID != in.PlanID || s.plan.RequestDigest != in.RequestDigest {
			return projectpostgres.SyncPlan{}, projectpostgres.ErrConflict
		}
		return s.plan, nil
	}
	s.plan = projectpostgres.SyncPlan{PlanID: in.PlanID, OperationID: in.OperationID, ProjectID: in.ProjectID, StorageSecurityDomain: in.StorageSecurityDomain, OwnerID: in.OwnerID, CandidateKey: in.CandidateKey, SourceDigest: in.SourceDigest, ProjectFile: in.ProjectFile, RequestDigest: in.RequestDigest, State: "open", ExpiresAt: in.ExpiresAt, CreatedAt: time.Now().UTC()}
	s.plan.Entries = make([]projectpostgres.SourceSyncPlanEntry, len(in.Entries))
	for i, e := range in.Entries {
		s.plan.Entries[i] = projectpostgres.SourceSyncPlanEntry{PlanID: in.PlanID, Path: e.Path, Digest: e.Digest, SizeBytes: e.SizeBytes, Ordinal: i}
	}
	return s.plan, nil
}
func (s *coordinatorSource) SyncPlanForUpdateTx(context.Context, projectpostgres.SourceTx, uuid.UUID) (projectpostgres.SyncPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.plan.PlanID == uuid.Nil {
		return projectpostgres.SyncPlan{}, projectpostgres.ErrNotFound
	}
	return s.plan, nil
}
func (s *coordinatorSource) ListMissingPlanSourceBlobDigestsTx(context.Context, projectpostgres.SourceTx, uuid.UUID, string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	seen := map[string]bool{}
	for _, e := range s.plan.Entries {
		if s.blobs == nil || s.blobs[e.Digest].Digest == "" {
			if !seen[e.Digest] {
				out = append(out, e.Digest)
				seen[e.Digest] = true
			}
		}
	}
	return out, nil
}
func (s *coordinatorSource) InsertSourceBlobTx(_ context.Context, _ projectpostgres.SourceTx, in projectpostgres.SourceBlobInput) (projectpostgres.SourceBlob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.blobs == nil {
		s.blobs = map[string]projectpostgres.SourceBlob{}
	}
	if old, ok := s.blobs[in.Digest]; ok {
		return old, nil
	}
	blob := projectpostgres.SourceBlob{ProjectID: in.ProjectID, StorageSecurityDomain: in.StorageSecurityDomain, Digest: in.Digest, SizeBytes: in.SizeBytes, ObjectKey: in.ObjectKey, ContentType: in.ContentType, MetadataDigest: in.MetadataDigest}
	s.blobs[in.Digest] = blob
	return blob, nil
}
func (s *coordinatorSource) CommitSnapshotTx(_ context.Context, _ projectpostgres.SourceTx, in projectpostgres.CommitSnapshotInput) (projectpostgres.SourceSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot.SnapshotID != uuid.Nil {
		if s.snapshot.SnapshotID != in.SnapshotID {
			return projectpostgres.SourceSnapshot{}, projectpostgres.ErrConflict
		}
		return s.snapshot, nil
	}
	s.snapshot = projectpostgres.SourceSnapshot{SnapshotID: in.SnapshotID, ProjectID: in.ProjectID, StorageSecurityDomain: in.StorageSecurityDomain, SourceDigest: in.SourceDigest, ProjectFile: in.ProjectFile, ProjectDigest: in.ProjectDigest, ProjectArtifactObjectKey: in.ProjectArtifactObjectKey, ProjectArtifactDigest: in.ProjectArtifactDigest, ProjectArtifactSizeBytes: in.ProjectArtifactSizeBytes, ManifestObjectKey: in.ManifestObjectKey, ManifestObjectDigest: in.ManifestObjectDigest, ManifestObjectSizeBytes: in.ManifestObjectSizeBytes, CompilerVersion: in.CompilerVersion, SchemaVersion: in.SchemaVersion, CreatedAt: time.Now().UTC()}
	s.plan.State = "committed"
	return s.snapshot, nil
}
func (s *coordinatorSource) Snapshot(context.Context, string, string, string) (projectpostgres.SourceSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot.SnapshotID == uuid.Nil {
		return projectpostgres.SourceSnapshot{}, projectpostgres.ErrNotFound
	}
	return s.snapshot, nil
}

type trackingStore struct {
	store  *objectstore.MemoryStore
	mu     *sync.Mutex
	active *bool
	puts   int
}

func (s *trackingStore) assertOutsideTx() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if *s.active {
		return errors.New("object-store callback ran while PostgreSQL transaction active")
	}
	return nil
}
func (s *trackingStore) PutImmutable(ctx context.Context, key string, body io.Reader, metadata objectstore.ObjectMetadata) (objectstore.ObjectInfo, error) {
	if err := s.assertOutsideTx(); err != nil {
		return objectstore.ObjectInfo{}, err
	}
	s.mu.Lock()
	s.puts++
	s.mu.Unlock()
	return s.store.PutImmutable(ctx, key, body, metadata)
}
func (s *trackingStore) Open(ctx context.Context, key string) (objectstore.Object, error) {
	if err := s.assertOutsideTx(); err != nil {
		return objectstore.Object{}, err
	}
	return s.store.Open(ctx, key)
}
func (s *trackingStore) List(ctx context.Context, p, c string, n int) ([]objectstore.ObjectInfo, string, error) {
	return s.store.List(ctx, p, c, n)
}
func (s *trackingStore) Delete(ctx context.Context, key string) error {
	return s.store.Delete(ctx, key)
}

func TestCoordinatorSequencesTransactionsOutsideCallbacks(t *testing.T) {
	ctx := context.Background()
	source := &coordinatorSource{}
	mu := &sync.Mutex{}
	active := false
	begin := BeginFunc(func(context.Context) (Tx, error) {
		mu.Lock()
		active = true
		mu.Unlock()
		return &coordinatorTx{mu: mu, flag: &active, active: true}, nil
	})
	store, err := objectstore.NewMemoryStore(objectstore.MemoryStoreConfig{StorageSecurityDomain: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	tracked := &trackingStore{store: store, mu: mu, active: &active}
	compiledCalls := 0
	compiler := CompileFunc(func(_ context.Context, in CompileInput) (CompileOutput, error) {
		mu.Lock()
		defer mu.Unlock()
		if active {
			return CompileOutput{}, errors.New("compiler callback ran while PostgreSQL transaction active")
		}
		compiledCalls++
		return CompileOutput{ProjectDigest: testDigest("a"), CompilerVersion: "compiler:v1", SchemaVersion: 1, ProjectArtifact: []byte("artifact"), ProjectArtifactObjectKey: "artifacts/project", Manifest: []byte("manifest"), ManifestObjectKey: "manifests/source"}, nil
	})
	coordinator, err := New(begin, source, tracked, compiler)
	if err != nil {
		t.Fatal(err)
	}
	input := testAdmissionInput()
	result, err := coordinator.Admit(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.SnapshotID != input.SnapshotID || compiledCalls != 1 || tracked.puts != 3 {
		t.Fatalf("result=%#v compile=%d puts=%d", result, compiledCalls, tracked.puts)
	}
	if _, err := coordinator.Admit(ctx, input); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if compiledCalls != 1 {
		t.Fatalf("replay recompiled source: %d calls", compiledCalls)
	}
}

func TestCoordinatorReconcilesAmbiguousPutAndRejectsMismatch(t *testing.T) {
	ctx := context.Background()
	source := &coordinatorSource{}
	mu := &sync.Mutex{}
	active := false
	begin := BeginFunc(func(context.Context) (Tx, error) {
		mu.Lock()
		active = true
		mu.Unlock()
		return &coordinatorTx{mu: mu, flag: &active}, nil
	})
	store, _ := objectstore.NewMemoryStore(objectstore.MemoryStoreConfig{StorageSecurityDomain: "runtime"})
	store.SimulateLostCommitAcknowledgement()
	tracked := &trackingStore{store: store, mu: mu, active: &active}
	compiler := CompileFunc(func(context.Context, CompileInput) (CompileOutput, error) {
		return CompileOutput{ProjectDigest: testDigest("a"), CompilerVersion: "compiler:v1", SchemaVersion: 1, ProjectArtifact: []byte("a"), ProjectArtifactObjectKey: "artifacts/project", Manifest: []byte("m"), ManifestObjectKey: "manifests/source"}, nil
	})
	coordinator, err := New(begin, source, tracked, compiler)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Admit(ctx, testAdmissionInput()); err != nil {
		t.Fatalf("ambiguous put did not converge: %v", err)
	}
	bad := testAdmissionInput()
	bad.PlanID = uuid.New()
	bad.OperationID = uuid.New()
	bad.SnapshotID = uuid.New()
	bad.Files[0].Bytes = []byte("different")
	// Reuse the committed source key while changing its content. The immutable
	// store must reject the identity mismatch after plan creation.
	bad.Files[0].ObjectKey = "source-blobs/" + strings.TrimPrefix(sha256Identity([]byte("source")), "sha256:")
	badSource := &coordinatorSource{}
	badCoordinator, err := New(begin, badSource, tracked, compiler)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := badCoordinator.Admit(ctx, bad); err == nil {
		t.Fatal("mismatched immutable object unexpectedly admitted")
	}
}

func TestCoordinatorCancellationLeavesPlanUncommitted(t *testing.T) {
	source := &coordinatorSource{}
	mu := &sync.Mutex{}
	active := false
	begin := BeginFunc(func(context.Context) (Tx, error) {
		mu.Lock()
		active = true
		mu.Unlock()
		return &coordinatorTx{mu: mu, flag: &active}, nil
	})
	store, _ := objectstore.NewMemoryStore(objectstore.MemoryStoreConfig{StorageSecurityDomain: "runtime"})
	tracked := &trackingStore{store: store, mu: mu, active: &active}
	canceled := errors.New("compile canceled")
	coordinator, err := New(begin, source, tracked, CompileFunc(func(context.Context, CompileInput) (CompileOutput, error) { return CompileOutput{}, canceled }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Admit(context.Background(), testAdmissionInput()); !errors.Is(err, canceled) {
		t.Fatalf("cancel error=%v", err)
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.plan.State == "committed" || source.snapshot.SnapshotID != uuid.Nil {
		t.Fatalf("canceled admission published: plan=%#v snapshot=%#v", source.plan, source.snapshot)
	}
}

func TestCoordinatorCanonicalizesPathsBeforeSorting(t *testing.T) {
	input := testAdmissionInput()
	input.Files = []SourceFile{
		{Path: " z.sql", Bytes: []byte("z")},
		{Path: "a.sql", Bytes: []byte("a")},
	}
	normalized, files, err := normalizeInput(input, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if files[0].Path != "a.sql" || files[1].Path != "z.sql" {
		t.Fatalf("normalized file order = %q, %q", files[0].Path, files[1].Path)
	}
	entries := []projectpostgres.SourceSnapshotEntryInput{
		{Path: files[0].Path, Digest: files[0].Digest, SizeBytes: int64(len(files[0].Bytes)), Ordinal: 0},
		{Path: files[1].Path, Digest: files[1].Digest, SizeBytes: int64(len(files[1].Bytes)), Ordinal: 1},
	}
	if want := projectpostgres.CanonicalSourceDigest(normalized.ProjectID, normalized.ProjectFile, entries); normalized.SourceDigest != want {
		t.Fatalf("source digest = %q, want %q", normalized.SourceDigest, want)
	}
}

func testAdmissionInput() AdmissionInput {
	body := []byte("source")
	return AdmissionInput{PlanID: uuid.New(), OperationID: uuid.New(), SnapshotID: uuid.New(), ProjectID: "project:test", StorageSecurityDomain: "runtime", OwnerID: "owner", CandidateKey: "default", ProjectFile: "leapview.yaml", ExpiresAt: time.Now().Add(time.Minute), Files: []SourceFile{{Path: "leapview.yaml", Bytes: body}}, Attestation: projectpostgres.SourceAttestationInput{AttestationID: uuid.New(), Payload: []byte(`{"source":true}`)}}
}
func testDigest(ch string) string { return "sha256:" + strings.Repeat(ch, 64) }
