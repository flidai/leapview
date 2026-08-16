package control_test

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/control"
	"github.com/flidai/leapview/internal/manageddata/storage"
	"github.com/flidai/leapview/internal/platform/jobs"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const (
	digestA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestBeginUploadEnsuresCollectionCanonicalizesManifestAndDeduplicatesMissingBlobs(t *testing.T) {
	now := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	repo := newFakeRepository()
	blobs := &fakeBlobStore{blobs: map[string]storage.Blob{
		digestA: {SHA256: digestA, Size: 3, URI: "s3://managed/blobs/" + digestA},
	}}
	transport := &fakeTransport{backend: "s3"}
	service := newService(t, repo, blobs, transport, now)

	result, err := service.BeginUpload(t.Context(), control.BeginUploadRequest{
		Project:        "project-a",
		Connection:     "orders",
		Actor:          "principal-a",
		IdempotencyKey: "request-1",
		Manifest: manageddata.Manifest{Files: []manageddata.File{
			{Path: "z.csv", Size: 7, SHA256: digestB},
			{Path: "a.csv", Size: 3, SHA256: digestA},
			{Path: "copy.csv", Size: 7, SHA256: digestB},
		}},
	})
	if err != nil {
		t.Fatalf("BeginUpload() error = %v", err)
	}
	if result.Collection.Project != "project-a" || result.Collection.Connection != "orders" {
		t.Fatalf("collection = %#v", result.Collection)
	}
	if got := filePaths(result.Manifest.Files); !slices.Equal(got, []string{"a.csv", "copy.csv", "z.csv"}) {
		t.Fatalf("canonical manifest paths = %v", got)
	}
	if len(result.MissingBlobs) != 1 || result.MissingBlobs[0].SHA256 != digestB {
		t.Fatalf("missing blobs = %#v", result.MissingBlobs)
	}
	if !slices.Equal(result.MissingBlobs[0].Paths, []string{"copy.csv", "z.csv"}) {
		t.Fatalf("missing blob paths = %v", result.MissingBlobs[0].Paths)
	}
	if result.Progress.VerifiedFiles != 1 || result.Progress.VerifiedBytes != 3 {
		t.Fatalf("progress = %#v", result.Progress)
	}
	if result.ExpiresAt != now.Add(2*time.Hour).Format(time.RFC3339Nano) {
		t.Fatalf("expires at = %q", result.ExpiresAt)
	}
	if len(transport.requests) != 1 || transport.requests[0].SHA256 != digestB {
		t.Fatalf("transport requests = %#v", transport.requests)
	}

	retry, err := service.BeginUpload(t.Context(), control.BeginUploadRequest{
		Project:        "project-a",
		Connection:     "orders",
		Actor:          "principal-a",
		IdempotencyKey: "request-1",
		Manifest:       result.Manifest,
	})
	if err != nil {
		t.Fatalf("idempotent BeginUpload() error = %v", err)
	}
	if retry.ID != result.ID || retry.RevisionID != result.RevisionID || repo.createUploadCalls != 2 {
		t.Fatalf("retry = %#v, first = %#v, create calls = %d", retry, result, repo.createUploadCalls)
	}
}

func TestBeginUploadEnforcesLimitsAndChecksBaseRevision(t *testing.T) {
	repo := newFakeRepository()
	blobs := &fakeBlobStore{blobs: make(map[string]storage.Blob)}
	service, err := control.New(repo, blobs, control.Config{
		Limits:    manageddata.Limits{MaxFiles: 1, MaxFileBytes: 4, MaxRevisionBytes: 4},
		UploadTTL: time.Hour,
		Transport: &fakeTransport{backend: "local"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.BeginUpload(t.Context(), control.BeginUploadRequest{
		Project: "project-a", Connection: "orders", IdempotencyKey: "too-large",
		Manifest: manageddata.Manifest{Files: []manageddata.File{{Path: "orders.csv", Size: 5, SHA256: digestA}}},
	})
	if !errors.Is(err, control.ErrInvalid) {
		t.Fatalf("limit error = %v, want ErrInvalid", err)
	}

	collection, err := repo.CreateCollection(t.Context(), manageddata.CreateCollectionInput{ProjectID: projectgraph.ResourceID("project-a"), ConnectionID: projectgraph.ResourceID("orders"), Name: "orders"})
	if err != nil {
		t.Fatal(err)
	}
	repo.revisions["other-revision"] = manageddata.Revision{ID: manageddata.RevisionID("other-revision"), CollectionID: projectgraph.ResourceID("other-collection"), Status: manageddata.RevisionStatusReady}
	_, err = service.BeginUpload(t.Context(), control.BeginUploadRequest{
		Project: "project-a", Connection: "orders", IdempotencyKey: "bad-base", BaseRevisionID: "other-revision",
		Manifest: manageddata.Manifest{Files: []manageddata.File{{Path: "orders.csv", Size: 4, SHA256: digestA}}},
	})
	if !errors.Is(err, control.ErrConflict) {
		t.Fatalf("foreign base revision error = %v, want ErrConflict (collection %s)", err, collection.ID)
	}
}

func TestRecoverDerivesProgressFromVerifiedBlobsInsteadOfStoredCounters(t *testing.T) {
	now := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	repo := newFakeRepository()
	blobs := &fakeBlobStore{blobs: map[string]storage.Blob{
		digestA: {SHA256: digestA, Size: 3, URI: "file:///data/blobs/" + digestA},
	}}
	service := newService(t, repo, blobs, &fakeTransport{backend: "local"}, now)
	started, err := service.BeginUpload(t.Context(), control.BeginUploadRequest{
		Project: "project-a", Connection: "orders", IdempotencyKey: "recover",
		Manifest: manageddata.Manifest{Files: []manageddata.File{
			{Path: "a.csv", Size: 3, SHA256: digestA},
			{Path: "b.csv", Size: 7, SHA256: digestB},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	session := repo.sessions[started.ID]
	session.UploadedFileCount = 2
	session.UploadedSizeBytes = 10
	repo.sessions[started.ID] = session

	recovered, err := service.RecoverUpload(t.Context(), control.UploadRequest{Project: "project-a", Connection: "orders", UploadID: started.ID})
	if err != nil {
		t.Fatalf("RecoverUpload() error = %v", err)
	}
	if recovered.Progress.VerifiedFiles != 1 || recovered.Progress.VerifiedBytes != 3 || len(recovered.MissingBlobs) != 1 {
		t.Fatalf("recovered = %#v", recovered)
	}
	stored := repo.sessions[started.ID]
	if stored.UploadedFileCount != 1 || stored.UploadedSizeBytes != 3 {
		t.Fatalf("persisted derived progress = %d files, %d bytes", stored.UploadedFileCount, stored.UploadedSizeBytes)
	}
}

func TestFinalizeRequiresEveryBlobAndRedactsBackendErrors(t *testing.T) {
	now := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	repo := newFakeRepository()
	blobs := &fakeBlobStore{blobs: make(map[string]storage.Blob)}
	service := newService(t, repo, blobs, &fakeTransport{backend: "local"}, now)
	started, err := service.BeginUpload(t.Context(), control.BeginUploadRequest{
		Project: "project-a", Connection: "orders", IdempotencyKey: "finalize",
		Manifest: manageddata.Manifest{Files: []manageddata.File{{Path: "orders.csv", Size: 3, SHA256: digestA}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.FinalizeUpload(t.Context(), control.UploadRequest{Project: "project-a", Connection: "orders", UploadID: started.ID})
	if !errors.Is(err, control.ErrIncomplete) || len(result.Upload.MissingBlobs) != 1 || repo.completeCalls != 0 {
		t.Fatalf("FinalizeUpload() result = %#v, error = %v, complete calls = %d", result, err, repo.completeCalls)
	}

	blobs.statErr = errors.New("AccessKeyId=secret-token host=private.example")
	_, err = service.RecoverUpload(t.Context(), control.UploadRequest{Project: "project-a", Connection: "orders", UploadID: started.ID})
	if !errors.Is(err, control.ErrBackend) {
		t.Fatalf("backend error = %v, want ErrBackend", err)
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "private.example") {
		t.Fatalf("backend error leaked sensitive details: %v", err)
	}
}

func TestAbortAndExpiryAreIdempotentAndCleanTransportState(t *testing.T) {
	now := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	clockNow := now
	repo := newFakeRepository()
	transport := &fakeTransport{backend: "local"}
	service, err := control.New(repo, &fakeBlobStore{blobs: make(map[string]storage.Blob)}, control.Config{
		Limits:    manageddata.Limits{MaxFiles: 100, MaxFileBytes: 100, MaxRevisionBytes: 1_000},
		UploadTTL: 2 * time.Hour, VerifyConcurrency: 2, Transport: transport,
		Clock: func() time.Time { return clockNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.BeginUpload(t.Context(), control.BeginUploadRequest{
		Project: "project-a", Connection: "orders", IdempotencyKey: "abort",
		Manifest: manageddata.Manifest{Files: []manageddata.File{{Path: "orders.csv", Size: 3, SHA256: digestA}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		aborted, abortErr := service.AbortUpload(t.Context(), control.UploadRequest{Project: "project-a", Connection: "orders", UploadID: started.ID})
		if abortErr != nil || aborted.Status != manageddata.UploadStatusAborted {
			t.Fatalf("AbortUpload() = %#v, %v", aborted, abortErr)
		}
	}
	if len(transport.aborts) != 2 {
		t.Fatalf("transport aborts = %v", transport.aborts)
	}

	expiring, err := service.BeginUpload(t.Context(), control.BeginUploadRequest{
		Project: "project-a", Connection: "orders", IdempotencyKey: "expire",
		Manifest: manageddata.Manifest{Files: []manageddata.File{{Path: "orders.csv", Size: 3, SHA256: digestA}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	clockNow = now.Add(3 * time.Hour)
	expired, err := service.ExpireUploads(t.Context())
	if err != nil || expired.Expired != 1 {
		t.Fatalf("ExpireUploads() = %#v, %v", expired, err)
	}
	recovered, err := service.RecoverUpload(t.Context(), control.UploadRequest{Project: "project-a", Connection: "orders", UploadID: expiring.ID})
	if err != nil || recovered.Status != manageddata.UploadStatusExpired {
		t.Fatalf("expired RecoverUpload() = %#v, %v", recovered, err)
	}
	if len(transport.aborts) != 3 {
		t.Fatalf("transport aborts after expiry = %v", transport.aborts)
	}
}

func newService(t *testing.T, repo *fakeRepository, blobs storage.BlobStore, transport control.Transport, now time.Time) *control.Service {
	t.Helper()
	service, err := control.New(repo, blobs, control.Config{
		Limits:            manageddata.Limits{MaxFiles: 100, MaxFileBytes: 100, MaxRevisionBytes: 1_000},
		UploadTTL:         2 * time.Hour,
		VerifyConcurrency: 2,
		Transport:         transport,
		Clock:             func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func filePaths(files []manageddata.File) []string {
	paths := make([]string, len(files))
	for i, file := range files {
		paths[i] = file.Path
	}
	return paths
}

type fakeBlobStore struct {
	mu      sync.Mutex
	blobs   map[string]storage.Blob
	statErr error
}

func (s *fakeBlobStore) Put(context.Context, storage.Blob, io.Reader) (storage.Blob, error) {
	panic("unexpected Put")
}

func (s *fakeBlobStore) Stat(_ context.Context, digest string) (storage.Blob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.statErr != nil {
		return storage.Blob{}, s.statErr
	}
	blob, ok := s.blobs[digest]
	if !ok {
		return storage.Blob{}, storage.ErrNotFound
	}
	return blob, nil
}

func (s *fakeBlobStore) Open(context.Context, string) (io.ReadCloser, error) {
	panic("unexpected Open")
}

type fakeTransport struct {
	backend  string
	requests []control.TransportRequest
	aborts   []string
	abortErr error
}

func (t *fakeTransport) Backend() string { return t.backend }

func (t *fakeTransport) Describe(_ context.Context, request control.TransportRequest) (control.TransportDescription, error) {
	t.requests = append(t.requests, request)
	return control.TransportDescription{
		Protocol: control.ProtocolS3Multipart,
		S3Multipart: &control.S3MultipartDescription{
			CreateEndpoint:  "/uploads/" + request.UploadID,
			MinimumPartSize: 5 << 20,
			MaximumPartSize: 5 << 30,
			MaximumParts:    10_000,
		},
	}, nil
}

func (t *fakeTransport) Abort(_ context.Context, request control.TransportRequest) error {
	t.aborts = append(t.aborts, request.SHA256)
	return t.abortErr
}

type fakeRepository struct {
	mu                sync.Mutex
	collections       map[string]manageddata.Collection
	sessions          map[string]manageddata.UploadSession
	revisions         map[string]manageddata.Revision
	revisionFiles     map[string][]manageddata.RevisionFile
	createUploadCalls int
	completeCalls     int
	sequence          int64
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{
		collections:   make(map[string]manageddata.Collection),
		sessions:      make(map[string]manageddata.UploadSession),
		revisions:     make(map[string]manageddata.Revision),
		revisionFiles: make(map[string][]manageddata.RevisionFile),
	}
}

func collectionKey(project, connection projectgraph.ResourceID) string {
	return project.String() + "\x00" + strings.ToLower(connection.String())
}

func (r *fakeRepository) CreateCollection(_ context.Context, input manageddata.CreateCollectionInput) (manageddata.Collection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := collectionKey(input.ProjectID, input.ConnectionID)
	if existing, ok := r.collections[key]; ok {
		return existing, nil
	}
	collection := manageddata.Collection{
		ID: projectgraph.ResourceID("collection-" + input.ProjectID.String() + "-" + input.ConnectionID.String()), ProjectID: input.ProjectID,
		ConnectionID: input.ConnectionID, Name: input.Name, Description: input.Description,
		Status: manageddata.CollectionStatusActive, CreatedBy: input.CreatedBy,
	}
	r.collections[key] = collection
	return collection, nil
}

func (r *fakeRepository) CollectionByProjectConnection(_ context.Context, project, connection projectgraph.ResourceID) (manageddata.Collection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	collection, ok := r.collections[collectionKey(project, connection)]
	if !ok {
		return manageddata.Collection{}, manageddata.ErrNotFound
	}
	return collection, nil
}

func (r *fakeRepository) CreateUploadSession(_ context.Context, input manageddata.CreateUploadSessionInput) (manageddata.UploadSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.createUploadCalls++
	if _, ok := r.sessions[input.ID.String()]; ok {
		return manageddata.UploadSession{}, manageddata.ErrConflict
	}
	canonical, err := input.Manifest.CanonicalJSON()
	if err != nil {
		return manageddata.UploadSession{}, err
	}
	var total int64
	for _, file := range input.Manifest.Files {
		total += file.Size
	}
	session := manageddata.UploadSession{
		ID: input.ID, CollectionID: input.CollectionID, BaseRevisionID: input.BaseRevisionID,
		Status: manageddata.UploadStatusOpen, ManifestJSON: string(canonical), ExpectedFileCount: int64(len(input.Manifest.Files)),
		ExpectedSizeBytes: total, StorageBackend: input.StorageBackend, StagingPrefix: input.StagingPrefix,
		CreatedBy: input.CreatedBy, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), ExpiresAt: input.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}
	r.sessions[input.ID.String()] = session
	return session, nil
}

func (r *fakeRepository) UploadSessionByID(_ context.Context, id manageddata.UploadID) (manageddata.UploadSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.sessions[id.String()]
	if !ok {
		return manageddata.UploadSession{}, manageddata.ErrNotFound
	}
	return session, nil
}

func (r *fakeRepository) UpdateUploadProgress(_ context.Context, id manageddata.UploadID, progress manageddata.UploadProgress) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.sessions[id.String()]
	if !ok {
		return manageddata.ErrNotFound
	}
	if session.Status != manageddata.UploadStatusOpen {
		return manageddata.ErrConflict
	}
	session.UploadedFileCount = progress.UploadedFileCount
	session.UploadedSizeBytes = progress.UploadedSizeBytes
	r.sessions[id.String()] = session
	return nil
}

func (r *fakeRepository) BeginUploadFinalization(_ context.Context, id manageddata.UploadID, _ jobs.WorkflowIntent) (manageddata.UploadSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.sessions[id.String()]
	if !ok {
		return manageddata.UploadSession{}, manageddata.ErrNotFound
	}
	if session.Status != manageddata.UploadStatusOpen {
		return manageddata.UploadSession{}, manageddata.ErrConflict
	}
	session.Status = manageddata.UploadStatusCommitting
	r.sessions[id.String()] = session
	return session, nil
}

func (r *fakeRepository) FailUploadFinalization(_ context.Context, id manageddata.UploadID, message string) (manageddata.UploadSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.sessions[id.String()]
	if !ok {
		return manageddata.UploadSession{}, manageddata.ErrNotFound
	}
	if session.Status != manageddata.UploadStatusCommitting {
		return manageddata.UploadSession{}, manageddata.ErrConflict
	}
	session.Status, session.Error = manageddata.UploadStatusFailed, message
	r.sessions[id.String()] = session
	return session, nil
}

func (r *fakeRepository) AbortUploadSession(_ context.Context, id manageddata.UploadID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.sessions[id.String()]
	if !ok {
		return manageddata.ErrNotFound
	}
	if session.Status != manageddata.UploadStatusOpen && session.Status != manageddata.UploadStatusCommitting {
		return manageddata.ErrConflict
	}
	session.Status = manageddata.UploadStatusAborted
	r.sessions[id.String()] = session
	return nil
}

func (r *fakeRepository) ExpireUploadSessions(_ context.Context, now time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var count int64
	for id, session := range r.sessions {
		expiresAt, _ := time.Parse(time.RFC3339Nano, session.ExpiresAt)
		if session.Status == manageddata.UploadStatusOpen && !expiresAt.After(now) {
			session.Status = manageddata.UploadStatusExpired
			r.sessions[id] = session
			count++
		}
	}
	return count, nil
}

func (r *fakeRepository) CompleteUpload(_ context.Context, input manageddata.CompleteUploadInput) (manageddata.Revision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completeCalls++
	session, ok := r.sessions[input.SessionID.String()]
	if !ok {
		return manageddata.Revision{}, manageddata.ErrNotFound
	}
	if session.Status == manageddata.UploadStatusComplete {
		return r.revisions[session.RevisionID.String()], nil
	}
	if session.Status != manageddata.UploadStatusOpen {
		return manageddata.Revision{}, manageddata.ErrConflict
	}
	r.sequence++
	revision := manageddata.Revision{
		ID: input.RevisionID, CollectionID: session.CollectionID, Sequence: r.sequence,
		Digest: decodeManifest(session.ManifestJSON).RevisionID(), Status: manageddata.RevisionStatusReady,
		ManifestJSON: session.ManifestJSON, FileCount: session.ExpectedFileCount, SizeBytes: session.ExpectedSizeBytes,
	}
	r.revisions[revision.ID.String()] = revision
	for _, file := range input.Files {
		r.revisionFiles[revision.ID.String()] = append(r.revisionFiles[revision.ID.String()], manageddata.RevisionFile{RevisionID: revision.ID, StoredFile: file})
	}
	session.Status = manageddata.UploadStatusComplete
	session.RevisionID = revision.ID
	session.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	r.sessions[input.SessionID.String()] = session
	return revision, nil
}

func (r *fakeRepository) RevisionByID(_ context.Context, id manageddata.RevisionID) (manageddata.Revision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	revision, ok := r.revisions[id.String()]
	if !ok {
		return manageddata.Revision{}, manageddata.ErrNotFound
	}
	return revision, nil
}

func (r *fakeRepository) ListRevisionFiles(_ context.Context, revisionID manageddata.RevisionID) ([]manageddata.RevisionFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]manageddata.RevisionFile(nil), r.revisionFiles[revisionID.String()]...), nil
}

func decodeManifest(value string) manageddata.Manifest {
	return mustDecodeManifest(value)
}
