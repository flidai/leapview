//go:build fai520qualification

package s3_test

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/flidai/leapview/internal/manageddata"
	managedpg "github.com/flidai/leapview/internal/manageddata/postgres"
	"github.com/flidai/leapview/internal/manageddata/storage"
	manageds3 "github.com/flidai/leapview/internal/manageddata/storage/s3"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This qualifies a retained manifest's blob closure, not a physical database
// restore or recovery activation. Provider observations are test inputs only.
func TestFAI520HistoricalManagedRevisionClosure(t *testing.T) {
	t.Setenv("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED", "1")
	repo := closureRepository(t)
	ctx, client, clientFor, bucket, endpoint := historicalProvider(t)
	store, err := manageds3.New(client, awss3.NewPresignClient(client), manageds3.Config{Bucket: bucket, Prefix: "project-a"})
	if err != nil {
		t.Fatal(err)
	}
	collection, err := repo.CreateCollection(ctx, manageddata.CreateCollectionInput{ProjectID: "project_a", ConnectionID: "connection_a", Name: "closure"})
	if err != nil {
		t.Fatal(err)
	}
	var manifest manageddata.Manifest
	var stored []manageddata.StoredFile
	observations := map[string]historicalSelection{}
	wrongVersions := map[string]string{}
	// Metadata is an ordinary explicitly declared file here. The canonical
	// revision manifest itself remains PostgreSQL-owned, not an invented object.
	for i, body := range [][]byte{[]byte("id,value\n1,one\n"), []byte("id,value\n2,two\n"), []byte(`{"dataset":"fixture"}`)} {
		path := []string{"a.csv", "b.csv", "metadata.json"}[i]
		blob, err := store.Put(ctx, blobFor(body), bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		file := manageddata.File{Path: path, SHA256: blob.SHA256, Size: blob.Size}
		manifest.Files = append(manifest.Files, file)
		stored = append(stored, manageddata.StoredFile{File: file, StorageKey: blob.URI})
		key := strings.TrimPrefix(blob.URI, "s3://"+bucket+"/")
		head, err := client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: &bucket, Key: &key})
		if err != nil {
			t.Fatal(err)
		}
		version := aws.ToString(head.VersionId)
		if version == "" || version == "null" {
			t.Fatal("missing immutable provider baseline")
		}
		observations[path] = historicalSelection{Endpoint: endpoint, Region: "us-east-1", Bucket: bucket, Key: key, Version: version}
	}
	// Create the immutable revision through the existing repository write path.
	upload, err := repo.CreateUploadSession(ctx, manageddata.CreateUploadSessionInput{ID: "upload_closure", CollectionID: collection.ID, Manifest: manifest, StorageBackend: "s3", StagingPrefix: "staging/closure", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := repo.CompleteUpload(ctx, manageddata.CompleteUploadInput{SessionID: upload.ID, Files: stored})
	if err != nil {
		t.Fatal(err)
	}
	if revision.Digest != manifest.RevisionID() {
		t.Fatal("persisted revision identity differs")
	}
	for _, file := range manifest.Files {
		selection := observations[file.Path]
		out, err := client.PutObject(ctx, &awss3.PutObjectInput{Bucket: &bucket, Key: &selection.Key, Body: bytes.NewReader(bytes.Repeat([]byte("x"), int(file.Size)))})
		if err != nil {
			t.Fatal(err)
		}
		wrongVersions[file.Path] = aws.ToString(out.VersionId)
		if wrongVersions[file.Path] == "" || wrongVersions[file.Path] == "null" {
			t.Fatal("missing replacement version")
		}
		if _, err := client.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: &bucket, Key: &selection.Key}); err != nil {
			t.Fatal(err)
		}
	}
	sentinelKey, sentinelBody := "project-b/sentinel", []byte("unrelated")
	_, err = store.Put(ctx, blobFor(sentinelBody), bytes.NewReader(sentinelBody))
	if err != nil {
		t.Fatal(err)
	}
	put, err := client.PutObject(ctx, &awss3.PutObjectInput{Bucket: &bucket, Key: &sentinelKey, Body: bytes.NewReader(sentinelBody)})
	if err != nil {
		t.Fatal(err)
	}
	sentinelSelection := historicalSelection{Endpoint: endpoint, Region: "us-east-1", Bucket: bucket, Key: sentinelKey, Version: aws.ToString(put.VersionId)}
	inventory := func() *awss3.ListObjectVersionsOutput {
		t.Helper()
		out, err := client.ListObjectVersions(ctx, &awss3.ListObjectVersionsInput{Bucket: &bucket})
		if err != nil {
			t.Fatal(err)
		}
		if aws.ToBool(out.IsTruncated) {
			t.Fatal("fixture inventory unexpectedly truncated")
		}
		return out
	}
	before := inventory()
	good := func(int) *awss3.Client { return client }
	qualify := func(t *testing.T, selected map[string]historicalSelection, clients func(int) *awss3.Client, want string, completed int) []string {
		t.Helper()
		digests, reason := observeHistoricalClosure(t, ctx, repo, revision.ID, endpoint, bucket, selected, clients)
		if reason != want || len(digests) != completed {
			t.Fatalf("closure: %s, %d members; want %s, %d", reason, len(digests), want, completed)
		}
		return digests
	}
	first := qualify(t, observations, good, "verified", 3)
	for _, tc := range []struct {
		name, path, mode, reason string
		completed                int
	}{
		{"missing dependency", "a.csv", "remove", "missing_dependency", 0},
		{"partial closure", "metadata.json", "remove", "missing_dependency", 2},
		{"missing historical version", "b.csv", "missing", "NoSuchVersion", 1},
		{"wrong version", "b.csv", "wrong", "integrity_mismatch", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected := maps.Clone(observations)
			entry := selected[tc.path]
			switch tc.mode {
			case "remove":
				delete(selected, tc.path)
			case "missing":
				entry.Version = uuid.NewString()
				selected[tc.path] = entry
			case "wrong":
				entry.Version = wrongVersions[tc.path]
				selected[tc.path] = entry
			}
			qualify(t, selected, good, tc.reason, tc.completed)
		})
	}
	t.Run("unrelated object ignored", func(t *testing.T) {
		selected := maps.Clone(observations)
		selected["not-in-manifest"] = sentinelSelection
		if !reflect.DeepEqual(first, qualify(t, selected, good, "verified", 3)) {
			t.Fatal("extra object altered closure")
		}
	})
	t.Run("credential failure mid closure", func(t *testing.T) {
		bad := clientFor(uuid.NewString())
		qualify(t, observations, func(i int) *awss3.Client {
			if i == 1 {
				return bad
			}
			return client
		}, "SignatureDoesNotMatch", 1)
		for range 2 {
			if !reflect.DeepEqual(first, qualify(t, observations, good, "verified", 3)) {
				t.Fatal("repair retry changed closure")
			}
		}
	})
	if _, reason := observeHistoricalBytes(ctx, client, sentinelSelection, sentinelSelection, blobFor(sentinelBody)); reason != "verified" {
		t.Fatal("sentinel changed:", reason)
	}
	after := inventory()
	if !reflect.DeepEqual(before.Versions, after.Versions) || !reflect.DeepEqual(before.DeleteMarkers, after.DeleteMarkers) {
		t.Fatal("closure retrieval changed provider state")
	}
}

func closureRepository(t *testing.T) *managedpg.Repository {
	t.Helper()
	h := postgrestest.Start(t)
	role := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: uuid.NewString(), Login: true})
	db := h.NewDatabase(t, "")
	admin, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	tx, err := admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if err := managedpg.ApplySchema(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(t.Context(), db.URL(role))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return managedpg.New(pool)
}

func observeHistoricalClosure(t *testing.T, ctx context.Context, repo *managedpg.Repository, id manageddata.RevisionID, endpoint, bucket string, selected map[string]historicalSelection, clients func(int) *awss3.Client) (digests []string, reason string) {
	t.Helper()
	started := time.Now().UTC()
	defer func() {
		record, err := json.Marshal(struct {
			Revision           manageddata.RevisionID
			Started, Completed time.Time
			VerifiedMembers    int
			Result             string
		}{id, started, time.Now().UTC(), len(digests), reason})
		if err != nil {
			t.Fatal(err)
		}
		t.Log("closure result", string(record))
	}()
	revision, err := repo.RevisionByID(ctx, id)
	if err != nil {
		return nil, "revision_unavailable"
	}
	files, err := repo.ListRevisionFiles(ctx, id)
	if err != nil {
		return nil, "dependencies_unavailable"
	}
	var manifest manageddata.Manifest
	if err := json.Unmarshal([]byte(revision.ManifestJSON), &manifest); err != nil {
		return nil, "invalid_manifest"
	}
	// Test assertions compose the existing manifest identity contract. Do not
	// infer completeness from how many provider observations happen to exist.
	if manifest.RevisionID() == "" || manifest.RevisionID() != revision.Digest || len(manifest.Files) == 0 || int64(len(manifest.Files)) != revision.FileCount {
		return nil, "invalid_manifest"
	}
	var discovered manageddata.Manifest
	for _, file := range files {
		if file.RevisionID != id {
			return nil, "dependency_identity_mismatch"
		}
		discovered.Files = append(discovered.Files, file.File)
	}
	if discovered.RevisionID() != revision.Digest {
		return nil, "incomplete_manifest"
	}
	inventory, err := manifest.CanonicalJSON()
	if err != nil {
		return nil, "invalid_manifest"
	}
	t.Log("closure inventory", id, revision.Digest, string(inventory))
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	for i, file := range files {
		observation, ok := selected[file.Path]
		if !ok {
			return digests, "missing_dependency"
		}
		prefix := "s3://" + bucket + "/"
		if !strings.HasPrefix(file.StorageKey, prefix) {
			return digests, "namespace_mismatch"
		}
		boundary := historicalSelection{Endpoint: endpoint, Region: "us-east-1", Bucket: bucket, Key: strings.TrimPrefix(file.StorageKey, prefix)}
		digest, result := observeHistoricalBytes(ctx, clients(i), boundary, observation, storage.Blob{SHA256: file.SHA256, Size: file.Size})
		record, err := json.Marshal(struct {
			Revision               manageddata.RevisionID
			ManifestDigest         string
			Order                  int
			Dependency             manageddata.File
			Provider               historicalSelection
			ObservedSHA256, Result string
		}{id, revision.Digest, i, file.File, observation, digest, result})
		if err != nil {
			t.Fatal(err)
		}
		t.Log("closure member", string(record))
		if result != "verified" {
			return digests, result
		}
		digests = append(digests, digest)
	}
	return digests, "verified"
}
