package devloop

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"

	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	"github.com/stretchr/testify/require"
)

func TestTargetStorePlansAndRetainsContentAddressedBlobs(t *testing.T) {
	store, err := NewTargetStore(t.TempDir())
	require.NoError(t, err)
	snapshot := testSnapshotWithArtifacts("store", []Artifact{
		contentArtifact("leapview.yaml", []byte("project")),
		contentArtifact("models/orders.yaml", []byte("orders")),
	})
	request := planRequestForSnapshot(snapshot)

	missing, err := store.Missing(t.Context(), request)
	require.NoError(t, err)
	if len(missing) != 2 {
		t.Fatalf("missing blobs = %#v, want 2", missing)
	}
	for _, artifact := range snapshot.Artifacts {
		if err := store.Put(t.Context(), artifact.Digest, bytes.NewReader(artifact.Content)); err != nil {
			t.Fatal(err)
		}
	}
	missing, err = store.Missing(t.Context(), request)
	require.NoError(t, err)
	if len(missing) != 0 {
		t.Fatalf("missing blobs after upload = %#v", missing)
	}
}

func TestTargetStoreRejectsDigestMismatchWithoutRetainingBlob(t *testing.T) {
	store, err := NewTargetStore(t.TempDir())
	require.NoError(t, err)
	artifact := contentArtifact("leapview.yaml", []byte("expected"))
	if err := store.Put(t.Context(), artifact.Digest, bytes.NewReader([]byte("tampered"))); err == nil {
		t.Fatal("target store accepted bytes that do not match content digest")
	}
	request := planRequestForSnapshot(testSnapshotWithArtifacts("store", []Artifact{artifact}))
	missing, err := store.Missing(t.Context(), request)
	require.NoError(t, err)
	if len(missing) != 1 || missing[0] != artifact.Digest {
		t.Fatalf("missing blobs = %#v, want rejected digest", missing)
	}
}

func TestTargetStoreCommitsValidatedSnapshotIdempotently(t *testing.T) {
	projectPath := filepath.Join("..", "..", "..", "dashboards", "leapview.yaml")
	snapshot, err := (FilesystemBuilder{ProjectPath: projectPath}).Build(t.Context())
	require.NoError(t, err)
	root := t.TempDir()
	store, err := NewTargetStore(root)
	require.NoError(t, err)
	for _, artifact := range snapshot.Artifacts {
		if err := store.Put(t.Context(), artifact.Digest, bytes.NewReader(artifact.Content)); err != nil {
			t.Fatal(err)
		}
	}
	request := planRequestForSnapshot(snapshot)

	const workers = 6
	var wait sync.WaitGroup
	results := make(chan StoredSnapshot, workers)
	errors := make(chan error, workers)
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := store.Commit(t.Context(), request)
			results <- result
			errors <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	var committedPath string
	var artifactPath string
	var projectDigest string
	for result := range results {
		if result.ProjectID != snapshot.ProjectID || result.Digest != snapshot.Digest {
			t.Fatalf("stored snapshot = %#v", result)
		}
		if result.ProjectArtifactPath == "" || result.ProjectDigest == "" {
			t.Fatalf("stored snapshot omits immutable project artifact: %#v", result)
		}
		if committedPath == "" {
			committedPath = result.ProjectPath
			artifactPath = result.ProjectArtifactPath
			projectDigest = result.ProjectDigest
		} else if result.ProjectPath != committedPath {
			t.Fatalf("idempotent commit paths differ: %q / %q", committedPath, result.ProjectPath)
		} else if result.ProjectArtifactPath != artifactPath || result.ProjectDigest != projectDigest {
			t.Fatalf("idempotent project artifacts differ: %#v", result)
		}
	}
	if relative, err := filepath.Rel(root, committedPath); err != nil ||
		relative == ".." || filepath.IsAbs(relative) {
		t.Fatalf("committed project path %q escapes store %q", committedPath, root)
	}
	info, err := os.Stat(committedPath)
	require.NoError(t, err)
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("committed project mode = %o, want no group/world access", info.Mode().Perm())
	}
	encoded, err := os.ReadFile(artifactPath)
	require.NoError(t, err)
	compiled, err := projectartifact.Decode(encoded)
	require.NoError(t, err)
	if compiled.ProjectID() != snapshot.ProjectID || compiled.Digest() != projectDigest {
		t.Fatalf("retained project artifact = id %q digest %q, want %q %q", compiled.ProjectID(), compiled.Digest(), snapshot.ProjectID, projectDigest)
	}
	if bytes.Contains(encoded, []byte(filepath.Dir(committedPath))) {
		t.Fatalf("retained project artifact contains target filesystem path %q", filepath.Dir(committedPath))
	}
}

func TestTargetStoreRejectsTamperedRetainedProjectArtifact(t *testing.T) {
	projectPath := filepath.Join("..", "..", "..", "dashboards", "leapview.yaml")
	snapshot, err := (FilesystemBuilder{ProjectPath: projectPath}).Build(t.Context())
	require.NoError(t, err)
	store, err := NewTargetStore(t.TempDir())
	require.NoError(t, err)
	for _, artifact := range snapshot.Artifacts {
		if err := store.Put(t.Context(), artifact.Digest, bytes.NewReader(artifact.Content)); err != nil {
			t.Fatal(err)
		}
	}
	request := planRequestForSnapshot(snapshot)
	stored, err := store.Commit(t.Context(), request)
	require.NoError(t, err)
	if err := os.WriteFile(stored.ProjectArtifactPath, []byte(`{"version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Commit(t.Context(), request); err == nil {
		t.Fatal("TargetStore.Commit() accepted a tampered retained project artifact")
	}
}

func TestTargetStoreRepairsLegacySnapshotMissingRetainedProjectArtifact(t *testing.T) {
	projectPath := filepath.Join("..", "..", "..", "dashboards", "leapview.yaml")
	snapshot, err := (FilesystemBuilder{ProjectPath: projectPath}).Build(t.Context())
	require.NoError(t, err)
	store, err := NewTargetStore(t.TempDir())
	require.NoError(t, err)
	for _, artifact := range snapshot.Artifacts {
		if err := store.Put(t.Context(), artifact.Digest, bytes.NewReader(artifact.Content)); err != nil {
			t.Fatal(err)
		}
	}
	request := planRequestForSnapshot(snapshot)
	stored, err := store.Commit(t.Context(), request)
	require.NoError(t, err)
	if err := os.Remove(stored.ProjectArtifactPath); err != nil {
		t.Fatal(err)
	}
	repaired, err := store.Commit(t.Context(), request)
	require.NoError(t, err)
	if repaired.ProjectDigest == "" {
		t.Fatalf("repaired snapshot = %#v", repaired)
	}
	if _, err := os.Stat(repaired.ProjectArtifactPath); err != nil {
		t.Fatalf("repaired project artifact: %v", err)
	}
}

func TestTargetStoreCannotCommitWithMissingBlobs(t *testing.T) {
	store, err := NewTargetStore(t.TempDir())
	require.NoError(t, err)
	snapshot := testSnapshot("missing")
	if _, err := store.Commit(t.Context(), planRequestForSnapshot(snapshot)); err == nil {
		t.Fatal("target store committed a snapshot with missing source blobs")
	}
}

func planRequestForSnapshot(snapshot Snapshot) SynchronizationPlanRequest {
	request := SynchronizationPlanRequest{
		ProjectID: snapshot.ProjectID, ProjectFile: snapshot.ProjectFile,
		ArtifactDigest: snapshot.Digest, Artifacts: make([]ArtifactReference, len(snapshot.Artifacts)),
	}
	for index, artifact := range snapshot.Artifacts {
		request.Artifacts[index] = ArtifactReference{Path: artifact.Path, Digest: artifact.Digest}
	}
	return request
}
