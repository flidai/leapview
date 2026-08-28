package resolver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/runtimeview"
	"github.com/flidai/leapview/internal/manageddata/storage"
	"github.com/flidai/leapview/internal/manageddata/storage/filesystem"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

var testIdentity = projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: "prod", GenerationID: "state-1"}

func TestResolveManagedDataJoinsAndMaterializesMultipleBindingsDeterministically(t *testing.T) {
	ordersManifest, ordersBlobs := testManifest(map[string]string{"orders/part-1.csv": "order_id\n1\n"})
	customersManifest, customerBlobs := testManifest(map[string]string{"customers.csv": "customer_id\n1\n"})
	blobs := mergeBlobs(ordersBlobs, customerBlobs)
	repo := &fakeRepository{
		bindings: []manageddata.ServingStateBinding{
			{Identity: testIdentity, CollectionID: "customers", RevisionID: "customers-r1"},
			{Identity: testIdentity, CollectionID: "orders", RevisionID: "orders-r1"},
		},
		collections: map[string]manageddata.Collection{
			"orders":    {ID: "orders", ProjectID: "project_sales", ConnectionID: "orders", Status: manageddata.CollectionStatusActive},
			"customers": {ID: "customers", ProjectID: "project_sales", ConnectionID: "customers", Status: manageddata.CollectionStatusActive},
		},
		revisions: map[string]manageddata.Revision{
			"orders-r1":    testRevision("orders-r1", "orders", ordersManifest, manageddata.RevisionStatusReady),
			"customers-r1": testRevision("customers-r1", "customers", customersManifest, manageddata.RevisionStatusReady),
		},
		files: map[string][]manageddata.RevisionFile{
			"orders-r1":    testRevisionFiles("orders-r1", ordersManifest),
			"customers-r1": testRevisionFiles("customers-r1", customersManifest),
		},
	}
	resolver := testResolver(t, repo, &memoryBlobStore{blobs: blobs})

	got, err := resolver.ResolveManagedData(t.Context(), testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = got.Lifetime.Release() })
	wantRevisionID := aggregateForTest([]aggregateForTestInput{
		{project: "project_sales", connection: "customers", digest: customersManifest.RevisionID()},
		{project: "project_sales", connection: "orders", digest: ordersManifest.RevisionID()},
	})
	if got.RevisionID != wantRevisionID {
		t.Fatalf("RevisionID = %q, want %q", got.RevisionID, wantRevisionID)
	}
	if len(got.Roots) != 2 {
		t.Fatalf("Roots = %#v", got.Roots)
	}
	if got.Revisions["orders"] != ordersManifest.RevisionID() || got.Revisions["customers"] != customersManifest.RevisionID() {
		t.Fatalf("Revisions = %#v, want exact manifest digests", got.Revisions)
	}
	assertFileContent(t, got.Roots["orders"], "orders/part-1.csv", "order_id\n1\n")
	assertFileContent(t, got.Roots["customers"], "customers.csv", "customer_id\n1\n")

	repo.bindings[0], repo.bindings[1] = repo.bindings[1], repo.bindings[0]
	reordered, err := resolver.ResolveManagedData(t.Context(), testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reordered.Lifetime.Release() })
	if reordered.RevisionID != got.RevisionID {
		t.Fatalf("reordered RevisionID = %q, want %q", reordered.RevisionID, got.RevisionID)
	}
	for name, root := range got.Roots {
		if reordered.Roots[name] != root {
			t.Fatalf("reused root %q = %q, want %q", name, reordered.Roots[name], root)
		}
	}
}

func TestResolveRejectsInvalidRelationshipsEnvironmentAndReadiness(t *testing.T) {
	manifest, blobs := testManifest(map[string]string{"data.csv": "data"})
	valid := func() *fakeRepository {
		return &fakeRepository{
			bindings:    []manageddata.ServingStateBinding{{Identity: testIdentity, CollectionID: "collection-1", RevisionID: "revision-1"}},
			collections: map[string]manageddata.Collection{"collection-1": {ID: "collection-1", ProjectID: "project_sales", ConnectionID: "warehouse"}},
			revisions:   map[string]manageddata.Revision{"revision-1": testRevision("revision-1", "collection-1", manifest, manageddata.RevisionStatusReady)},
			files:       map[string][]manageddata.RevisionFile{"revision-1": testRevisionFiles("revision-1", manifest)},
		}
	}
	tests := []struct {
		name   string
		mutate func(*fakeRepository)
		want   error
	}{
		{name: "wrong serving state", mutate: func(repo *fakeRepository) { repo.bindings[0].Identity.GenerationID = "state-2" }, want: ErrInvalidMetadata},
		{name: "noncanonical environment", mutate: func(repo *fakeRepository) { repo.bindings[0].Identity.Environment = " prod " }, want: ErrInvalidMetadata},
		{name: "environment differs from serving state", mutate: func(repo *fakeRepository) { repo.stateEnvironment = "dev" }, want: ErrInvalidMetadata},
		{name: "mixed environments", mutate: func(repo *fakeRepository) {
			identity := testIdentity
			identity.Environment = "dev"
			repo.bindings = append(repo.bindings, manageddata.ServingStateBinding{Identity: identity, CollectionID: "collection-2", RevisionID: "revision-2"})
			repo.collections["collection-2"] = manageddata.Collection{ID: "collection-2", ProjectID: "project_sales", ConnectionID: "second"}
			repo.revisions["revision-2"] = testRevision("revision-2", "collection-2", manifest, manageddata.RevisionStatusReady)
			repo.files["revision-2"] = testRevisionFiles("revision-2", manifest)
		}, want: ErrInvalidMetadata},
		{name: "collection relationship", mutate: func(repo *fakeRepository) {
			collection := repo.collections["collection-1"]
			collection.ID = "other"
			repo.collections["collection-1"] = collection
		}, want: ErrInvalidMetadata},
		{name: "missing revision", mutate: func(repo *fakeRepository) { delete(repo.revisions, "revision-1") }, want: ErrInvalidMetadata},
		{name: "pending revision", mutate: func(repo *fakeRepository) {
			revision := repo.revisions["revision-1"]
			revision.Status = manageddata.RevisionStatusPending
			repo.revisions["revision-1"] = revision
		}, want: ErrRevisionNotReady},
		{name: "revision relationship", mutate: func(repo *fakeRepository) {
			revision := repo.revisions["revision-1"]
			revision.CollectionID = "other"
			repo.revisions["revision-1"] = revision
		}, want: ErrInvalidMetadata},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := valid()
			test.mutate(repo)
			resolver := testResolver(t, repo, &memoryBlobStore{blobs: blobs})
			_, err := resolver.ResolveManagedData(t.Context(), testIdentity)
			if !errors.Is(err, test.want) {
				t.Fatalf("ResolveManagedData() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestResolveRejectsManifestAndRevisionFileMetadataDisagreement(t *testing.T) {
	manifest, blobs := testManifest(map[string]string{"data.csv": "data"})
	valid := func() *fakeRepository {
		return &fakeRepository{
			bindings:    []manageddata.ServingStateBinding{{Identity: testIdentity, CollectionID: "collection-1", RevisionID: "revision-1"}},
			collections: map[string]manageddata.Collection{"collection-1": {ID: "collection-1", ProjectID: "project_sales", ConnectionID: "warehouse"}},
			revisions:   map[string]manageddata.Revision{"revision-1": testRevision("revision-1", "collection-1", manifest, manageddata.RevisionStatusReady)},
			files:       map[string][]manageddata.RevisionFile{"revision-1": testRevisionFiles("revision-1", manifest)},
		}
	}
	tests := []struct {
		name   string
		mutate func(*fakeRepository)
	}{
		{name: "manifest digest", mutate: func(repo *fakeRepository) {
			revision := repo.revisions["revision-1"]
			revision.Digest = "sha256:" + strings.Repeat("a", 64)
			repo.revisions["revision-1"] = revision
		}},
		{name: "manifest is not canonical JSON", mutate: func(repo *fakeRepository) {
			revision := repo.revisions["revision-1"]
			revision.ManifestJSON = " " + revision.ManifestJSON
			repo.revisions["revision-1"] = revision
		}},
		{name: "manifest has unknown metadata", mutate: func(repo *fakeRepository) {
			revision := repo.revisions["revision-1"]
			revision.ManifestJSON = `{"files":[],"secret":"do-not-show"}`
			repo.revisions["revision-1"] = revision
		}},
		{name: "revision file count", mutate: func(repo *fakeRepository) {
			revision := repo.revisions["revision-1"]
			revision.FileCount++
			repo.revisions["revision-1"] = revision
		}},
		{name: "revision size", mutate: func(repo *fakeRepository) {
			revision := repo.revisions["revision-1"]
			revision.SizeBytes++
			repo.revisions["revision-1"] = revision
		}},
		{name: "stored file count", mutate: func(repo *fakeRepository) { repo.files["revision-1"] = nil }},
		{name: "stored file relationship", mutate: func(repo *fakeRepository) {
			files := repo.files["revision-1"]
			files[0].RevisionID = "other"
			repo.files["revision-1"] = files
		}},
		{name: "stored file size", mutate: func(repo *fakeRepository) {
			files := repo.files["revision-1"]
			files[0].Size++
			files[0].StorageKey = "s3://private-bucket/key?token=secret"
			repo.files["revision-1"] = files
		}},
		{name: "stored file digest", mutate: func(repo *fakeRepository) {
			files := repo.files["revision-1"]
			files[0].SHA256 = strings.Repeat("b", 64)
			repo.files["revision-1"] = files
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := valid()
			test.mutate(repo)
			resolver := testResolver(t, repo, &memoryBlobStore{blobs: blobs})
			_, err := resolver.ResolveManagedData(t.Context(), testIdentity)
			if !errors.Is(err, ErrInvalidMetadata) {
				t.Fatalf("ResolveManagedData() error = %v, want %v", err, ErrInvalidMetadata)
			}
			for _, sensitive := range []string{"private-bucket", "token=secret", "do-not-show"} {
				if strings.Contains(err.Error(), sensitive) {
					t.Fatalf("error exposed sensitive metadata %q: %v", sensitive, err)
				}
			}
		})
	}
}

func TestResolveRejectsDuplicateConnectionIDAmbiguity(t *testing.T) {
	manifest, blobs := testManifest(map[string]string{"data.csv": "data"})
	repo := &fakeRepository{
		bindings: []manageddata.ServingStateBinding{
			{Identity: testIdentity, CollectionID: "first", RevisionID: "first-r1"},
			{Identity: testIdentity, CollectionID: "second", RevisionID: "second-r1"},
		},
		collections: map[string]manageddata.Collection{
			"first":  {ID: "first", ProjectID: "project_sales", ConnectionID: "warehouse"},
			"second": {ID: "second", ProjectID: "project_sales", ConnectionID: "warehouse"},
		},
		revisions: map[string]manageddata.Revision{
			"first-r1":  testRevision("first-r1", "first", manifest, manageddata.RevisionStatusReady),
			"second-r1": testRevision("second-r1", "second", manifest, manageddata.RevisionStatusReady),
		},
		files: map[string][]manageddata.RevisionFile{
			"first-r1":  testRevisionFiles("first-r1", manifest),
			"second-r1": testRevisionFiles("second-r1", manifest),
		},
	}
	resolver := testResolver(t, repo, &memoryBlobStore{blobs: blobs})
	_, err := resolver.ResolveManagedData(t.Context(), testIdentity)
	if !errors.Is(err, ErrAmbiguousConnection) {
		t.Fatalf("ResolveManagedData() error = %v, want %v", err, ErrAmbiguousConnection)
	}
}

func TestResolveSanitizesRepositoryErrors(t *testing.T) {
	resolver := testResolver(t, &fakeRepository{listErr: errors.New("database failed at s3://private/key?secret=value")}, &memoryBlobStore{})
	_, err := resolver.ResolveManagedData(t.Context(), testIdentity)
	if !errors.Is(err, ErrRepository) {
		t.Fatalf("ResolveManagedData() error = %v, want %v", err, ErrRepository)
	}
	if strings.Contains(err.Error(), "private/key") || strings.Contains(err.Error(), "secret=value") {
		t.Fatalf("repository error exposed sensitive data: %v", err)
	}
}

func TestResolveSanitizesMaterializationErrors(t *testing.T) {
	manifest, _ := testManifest(map[string]string{"data.csv": "data"})
	repo := &fakeRepository{
		bindings:    []manageddata.ServingStateBinding{{Identity: testIdentity, CollectionID: "collection-1", RevisionID: "revision-1"}},
		collections: map[string]manageddata.Collection{"collection-1": {ID: "collection-1", ProjectID: "project_sales", ConnectionID: "warehouse"}},
		revisions:   map[string]manageddata.Revision{"revision-1": testRevision("revision-1", "collection-1", manifest, manageddata.RevisionStatusReady)},
		files:       map[string][]manageddata.RevisionFile{"revision-1": testRevisionFiles("revision-1", manifest)},
	}
	resolver := testResolver(t, repo, &memoryBlobStore{openErr: errors.New("read s3://private/key?secret=value")})
	_, err := resolver.ResolveManagedData(t.Context(), testIdentity)
	if !errors.Is(err, ErrMaterialization) {
		t.Fatalf("ResolveManagedData() error = %v, want %v", err, ErrMaterialization)
	}
	if strings.Contains(err.Error(), "private/key") || strings.Contains(err.Error(), "secret=value") {
		t.Fatalf("materialization error exposed sensitive data: %v", err)
	}
}

func TestResolveUsesRevisionMaterializerAndReleasesPartialResolution(t *testing.T) {
	firstManifest, _ := testManifest(map[string]string{"first.csv": "first"})
	secondManifest, _ := testManifest(map[string]string{"second.csv": "second"})
	repo := &fakeRepository{
		bindings: []manageddata.ServingStateBinding{
			{Identity: testIdentity, CollectionID: "first", RevisionID: "first-r1"},
			{Identity: testIdentity, CollectionID: "second", RevisionID: "second-r1"},
		},
		collections: map[string]manageddata.Collection{
			"first":  {ID: "first", ProjectID: "project_sales", ConnectionID: "first"},
			"second": {ID: "second", ProjectID: "project_sales", ConnectionID: "second"},
		},
		revisions: map[string]manageddata.Revision{
			"first-r1":  testRevision("first-r1", "first", firstManifest, manageddata.RevisionStatusReady),
			"second-r1": testRevision("second-r1", "second", secondManifest, manageddata.RevisionStatusReady),
		},
		files: map[string][]manageddata.RevisionFile{
			"first-r1":  testRevisionFiles("first-r1", firstManifest),
			"second-r1": testRevisionFiles("second-r1", secondManifest),
		},
	}
	materializer := &recordingRevisionMaterializer{failAt: 2}
	resolver, err := New(repo, repo, materializer)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.ResolveManagedData(t.Context(), testIdentity)
	if !errors.Is(err, ErrMaterialization) {
		t.Fatalf("ResolveManagedData() error = %v, want %v", err, ErrMaterialization)
	}
	if len(materializer.leases) != 1 || materializer.leases[0].releases != 1 {
		t.Fatalf("materialized leases = %#v, want the partial result released", materializer.leases)
	}
}

func TestResolveWithFilesystemBlobStoreAndRuntimeViewIsConcurrentAndIdempotent(t *testing.T) {
	manifest, bodies := testManifest(map[string]string{
		"customers.csv":       "customer_id\n1\n",
		"partitions/2026.csv": "order_id\n1\n",
	})
	blobStore, err := filesystem.New(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	for digest, body := range bodies {
		if _, err := blobStore.Put(t.Context(), storage.Blob{SHA256: digest, Size: int64(len(body))}, bytes.NewReader(body)); err != nil {
			t.Fatal(err)
		}
	}
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	cache, err := runtimeview.New(runtimeRoot, blobStore)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { makeTestTreeWritable(runtimeRoot) })
	repo := &fakeRepository{
		bindings:    []manageddata.ServingStateBinding{{Identity: testIdentity, CollectionID: "collection-1", RevisionID: "revision-1"}},
		collections: map[string]manageddata.Collection{"collection-1": {ID: "collection-1", ProjectID: "project_sales", ConnectionID: "warehouse"}},
		revisions:   map[string]manageddata.Revision{"revision-1": testRevision("revision-1", "collection-1", manifest, manageddata.RevisionStatusReady)},
		files:       map[string][]manageddata.RevisionFile{"revision-1": testRevisionFiles("revision-1", manifest)},
	}
	resolver, err := New(repo, repo, cache)
	if err != nil {
		t.Fatal(err)
	}

	const workers = 8
	results := make(chan string, workers)
	errorsFound := make(chan error, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			resolution, resolveErr := resolver.ResolveManagedData(context.Background(), testIdentity)
			if resolveErr != nil {
				errorsFound <- resolveErr
				return
			}
			results <- resolution.Roots["warehouse"]
			if releaseErr := resolution.Lifetime.Release(); releaseErr != nil {
				errorsFound <- releaseErr
			}
		}()
	}
	wait.Wait()
	close(results)
	close(errorsFound)
	for resolveErr := range errorsFound {
		t.Fatal(resolveErr)
	}
	var root string
	for result := range results {
		if root == "" {
			root = result
		}
		if result != root {
			t.Fatalf("concurrent root = %q, want %q", result, root)
		}
	}
	assertFileContent(t, root, "customers.csv", "customer_id\n1\n")
	assertFileContent(t, root, "partitions/2026.csv", "order_id\n1\n")
}

type fakeRepository struct {
	bindings         []manageddata.ServingStateBinding
	collections      map[string]manageddata.Collection
	revisions        map[string]manageddata.Revision
	files            map[string][]manageddata.RevisionFile
	listErr          error
	stateErr         error
	stateID          servingstate.ID
	stateEnvironment servingstate.Environment
}

type recordingRevisionMaterializer struct {
	calls  int
	failAt int
	leases []*recordingRevisionLease
}

func (m *recordingRevisionMaterializer) MaterializeRevision(_ context.Context, revisionID string, _ manageddata.Manifest) (manageddata.RevisionLease, error) {
	m.calls++
	if m.calls == m.failAt {
		return nil, errors.New("materialization failed")
	}
	lease := &recordingRevisionLease{root: filepath.Join("/runtime", strings.TrimPrefix(revisionID, "sha256:"))}
	m.leases = append(m.leases, lease)
	return lease, nil
}

type recordingRevisionLease struct {
	root     string
	releases int
}

func (l *recordingRevisionLease) Root() string { return l.root }
func (l *recordingRevisionLease) Release() error {
	l.releases++
	return nil
}

func (r *fakeRepository) ListServingStateBindings(context.Context, projectgraph.ServingIdentity) ([]manageddata.ServingStateBinding, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return append([]manageddata.ServingStateBinding(nil), r.bindings...), nil
}

func (r *fakeRepository) CollectionByID(_ context.Context, id projectgraph.ResourceID) (manageddata.Collection, error) {
	collection, ok := r.collections[id.String()]
	if !ok {
		return manageddata.Collection{}, manageddata.ErrNotFound
	}
	return collection, nil
}

func (r *fakeRepository) RevisionByID(_ context.Context, id manageddata.RevisionID) (manageddata.Revision, error) {
	revision, ok := r.revisions[id.String()]
	if !ok {
		return manageddata.Revision{}, manageddata.ErrNotFound
	}
	return revision, nil
}

func (r *fakeRepository) ListRevisionFiles(_ context.Context, revisionID manageddata.RevisionID) ([]manageddata.RevisionFile, error) {
	files, ok := r.files[revisionID.String()]
	if !ok {
		return nil, manageddata.ErrNotFound
	}
	return append([]manageddata.RevisionFile(nil), files...), nil
}

func (r *fakeRepository) ByID(_ context.Context, id servingstate.ID) (servingstate.State, error) {
	if r.stateErr != nil {
		return servingstate.State{}, r.stateErr
	}
	stateID := r.stateID
	if stateID == "" {
		stateID = id
	}
	environment := r.stateEnvironment
	if environment == "" {
		environment = "prod"
	}
	return servingstate.State{ID: stateID, ProjectID: testIdentity.ProjectID, Environment: environment}, nil
}

type memoryBlobStore struct {
	mu      sync.Mutex
	blobs   map[string][]byte
	openErr error
}

func (s *memoryBlobStore) Put(context.Context, storage.Blob, io.Reader) (storage.Blob, error) {
	return storage.Blob{}, errors.New("not implemented")
}

func (s *memoryBlobStore) Stat(_ context.Context, digest string) (storage.Blob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, ok := s.blobs[digest]
	if !ok {
		return storage.Blob{}, storage.ErrNotFound
	}
	return storage.Blob{SHA256: digest, Size: int64(len(body))}, nil
}

func (s *memoryBlobStore) Open(_ context.Context, digest string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.openErr != nil {
		return nil, s.openErr
	}
	body, ok := s.blobs[digest]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), body...))), nil
}

func testResolver(t *testing.T, repo *fakeRepository, blobs storage.BlobStore) *Resolver {
	t.Helper()
	root := t.TempDir()
	cache, err := runtimeview.New(root, blobs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { makeTestTreeWritable(root) })
	resolver, err := New(repo, repo, cache)
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func makeTestTreeWritable(root string) {
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			_ = os.Chmod(path, 0o700)
		} else {
			_ = os.Chmod(path, 0o600)
		}
		return nil
	})
}

func testManifest(contents map[string]string) (manageddata.Manifest, map[string][]byte) {
	manifest := manageddata.Manifest{Files: make([]manageddata.File, 0, len(contents))}
	blobs := make(map[string][]byte, len(contents))
	for path, content := range contents {
		body := []byte(content)
		digest := digest(body)
		manifest.Files = append(manifest.Files, manageddata.File{Path: path, Size: int64(len(body)), SHA256: digest})
		blobs[digest] = body
	}
	canonical, err := manifest.CanonicalJSON()
	if err != nil {
		panic(err)
	}
	var sorted manageddata.Manifest
	if err := strictUnmarshalForTest(canonical, &sorted); err != nil {
		panic(err)
	}
	return sorted, blobs
}

func testRevision(id, collectionID string, manifest manageddata.Manifest, status manageddata.RevisionStatus) manageddata.Revision {
	canonical, err := manifest.CanonicalJSON()
	if err != nil {
		panic(err)
	}
	var size int64
	for _, file := range manifest.Files {
		size += file.Size
	}
	return manageddata.Revision{
		ID: manageddata.RevisionID(id), CollectionID: projectgraph.ResourceID(collectionID), Digest: manifest.RevisionID(), Status: status,
		ManifestJSON: string(canonical), FileCount: int64(len(manifest.Files)), SizeBytes: size,
	}
}

func testRevisionFiles(revisionID string, manifest manageddata.Manifest) []manageddata.RevisionFile {
	files := make([]manageddata.RevisionFile, 0, len(manifest.Files))
	for _, file := range manifest.Files {
		files = append(files, manageddata.RevisionFile{
			RevisionID: manageddata.RevisionID(revisionID),
			StoredFile: manageddata.StoredFile{File: file, StorageKey: "opaque-storage-key"},
		})
	}
	return files
}

func strictUnmarshalForTest(raw []byte, target any) error {
	return json.Unmarshal(raw, target)
}

func digest(body []byte) string {
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:])
}

func mergeBlobs(groups ...map[string][]byte) map[string][]byte {
	merged := map[string][]byte{}
	for _, group := range groups {
		for digest, body := range group {
			merged[digest] = append([]byte(nil), body...)
		}
	}
	return merged
}

func assertFileContent(t *testing.T, root, relativePath, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relativePath)))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s content = %q, want %q", relativePath, got, want)
	}
}

type aggregateForTestInput struct {
	project    string
	connection string
	digest     string
}

func aggregateForTest(bindings []aggregateForTestInput) string {
	var payload strings.Builder
	payload.WriteByte('[')
	for index, binding := range bindings {
		if index > 0 {
			payload.WriteByte(',')
		}
		fmt.Fprintf(&payload, `{"project":%q,"connection":%q,"manifest_digest":%q}`, binding.project, binding.connection, binding.digest)
	}
	payload.WriteByte(']')
	hash := sha256.Sum256([]byte(payload.String()))
	return "sha256:" + hex.EncodeToString(hash[:])
}
