package postgres

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	managedmaintenance "github.com/flidai/leapview/internal/manageddata/maintenance"
	"github.com/flidai/leapview/internal/manageddata/storage"
	filesystemstorage "github.com/flidai/leapview/internal/manageddata/storage/filesystem"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBND06CrossProjectManagedBlobGCProtectsSharedContent(t *testing.T) {
	runtimePool, _, database, _ := openManagedDataTestPool(t)
	repository := New(runtimePool)
	adminPool, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminPool.Close)
	retentionAuthority := New(adminPool)

	store, err := filesystemstorage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sharedContent := []byte("shared managed bytes across Project roots")
	sharedHash := sha256.Sum256(sharedContent)
	sharedBlob := storage.Blob{SHA256: hex.EncodeToString(sharedHash[:]), Size: int64(len(sharedContent))}
	if _, err := store.Put(t.Context(), sharedBlob, bytes.NewReader(sharedContent)); err != nil {
		t.Fatal(err)
	}
	orphanContent := []byte("unprotected managed bytes")
	orphanHash := sha256.Sum256(orphanContent)
	orphanBlob := storage.Blob{SHA256: hex.EncodeToString(orphanHash[:]), Size: int64(len(orphanContent))}
	if _, err := store.Put(t.Context(), orphanBlob, bytes.NewReader(orphanContent)); err != nil {
		t.Fatal(err)
	}

	createRevision := func(projectID, suffix, path string) manageddata.Revision {
		t.Helper()
		collection, err := repository.CreateCollection(t.Context(), manageddata.CreateCollectionInput{
			ID: projectgraph.ResourceID("collection_bnd06_" + suffix), ProjectID: projectgraph.ResourceID(projectID), ConnectionID: projectgraph.ResourceID("connection_bnd06_" + suffix), Name: "BND-06 " + suffix,
		})
		if err != nil {
			t.Fatal(err)
		}
		manifest := manageddata.Manifest{Files: []manageddata.File{{Path: path, Size: sharedBlob.Size, SHA256: sharedBlob.SHA256}}}
		session, err := repository.CreateUploadSession(t.Context(), manageddata.CreateUploadSessionInput{
			ID: manageddata.UploadID("upload_bnd06_" + suffix), CollectionID: collection.ID, Manifest: manifest,
			StorageBackend: "filesystem", StagingPrefix: "uploads/bnd06/" + suffix, ExpiresAt: time.Now().UTC().Add(time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		revision, err := repository.CompleteUpload(t.Context(), manageddata.CompleteUploadInput{
			SessionID: session.ID, Files: []manageddata.StoredFile{{File: manifest.Files[0], StorageKey: "blobs/sha256/" + sharedBlob.SHA256}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return revision
	}

	projectARevision := createRevision("project-bnd06-a", "a", "project-a/shared.bin")
	projectBRevision := createRevision("project-bnd06-b", "b", "project-b/shared.bin")
	projectARoot, err := repository.RecordRetentionRoot(t.Context(), RetentionRoot{
		RootID: "root-bnd06-a", ProjectID: "project-bnd06-a", Environment: "prod", RevisionID: projectARevision.ID.String(),
		Evidence: []byte(`{"qualification":"BND-06","project":"A"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	projectBRoot, err := repository.RecordRetentionRoot(t.Context(), RetentionRoot{
		RootID: "root-bnd06-b", ProjectID: "project-bnd06-b", Environment: "prod", RevisionID: projectBRevision.ID.String(),
		Evidence: []byte(`{"qualification":"BND-06","project":"B"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retentionAuthority.TransitionRetentionRoot(t.Context(), projectBRoot.RootID, "retiring"); err != nil {
		t.Fatal(err)
	}
	if _, err := retentionAuthority.TransitionRetentionRoot(t.Context(), projectBRoot.RootID, "expired"); err != nil {
		t.Fatal(err)
	}

	source, err := NewReachabilitySource(runtimePool)
	if err != nil {
		t.Fatal(err)
	}
	reachable, err := source.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(reachable.SHA256s) != 1 || reachable.SHA256s[0] != sharedBlob.SHA256 {
		t.Fatalf("reachable digests = %#v, want shared bytes protected only by Project A", reachable.SHA256s)
	}
	if root, err := repository.RetentionRootByID(t.Context(), projectARoot.RootID); err != nil || root.State != "live" {
		t.Fatalf("Project A root = %#v, %v, want live", root, err)
	}

	collector, err := managedmaintenance.NewBlobCollector(store, source, managedmaintenance.BlobGCConfig{
		GraceAge: time.Hour, BatchSize: 10, Now: func() time.Time { return time.Now().UTC().Add(24 * time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := collector.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 1 || result.ReclaimedBytes != orphanBlob.Size {
		t.Fatalf("GC result = %#v, want only the unprotected blob deleted", result)
	}
	if _, err := store.Stat(t.Context(), sharedBlob.SHA256); err != nil {
		t.Fatalf("shared bytes protected by Project A were deleted: %v", err)
	}
	if _, err := store.Stat(t.Context(), orphanBlob.SHA256); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("unprotected blob error = %v, want not found", err)
	}
}
