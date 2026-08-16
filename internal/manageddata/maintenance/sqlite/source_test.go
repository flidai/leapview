package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/maintenance"
	managedsqlite "github.com/flidai/leapview/internal/manageddata/sqlite"
	"github.com/flidai/leapview/internal/manageddata/storage"
	"github.com/flidai/leapview/internal/platform"
)

func TestSnapshotRetainsReadyRevisionsAndNonterminalUploads(t *testing.T) {
	db, repo := testDatabase(t)
	source, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	collection := createCollection(t, repo)

	readyDigest := digest('a')
	duplicateDigest := digest('b')
	ready := createUpload(t, repo, collection.ID, "ready", manifest(
		manageddata.File{Path: "ready.csv", Size: 1, SHA256: readyDigest},
		manageddata.File{Path: "duplicate.csv", Size: 2, SHA256: duplicateDigest},
	))
	if _, err := repo.CompleteUpload(t.Context(), manageddata.CompleteUploadInput{
		SessionID: ready.ID,
		Files: []manageddata.StoredFile{
			{File: manageddata.File{Path: "ready.csv", Size: 1, SHA256: readyDigest}, StorageKey: "objects/" + readyDigest},
			{File: manageddata.File{Path: "duplicate.csv", Size: 2, SHA256: duplicateDigest}, StorageKey: "objects/" + duplicateDigest},
		},
	}); err != nil {
		t.Fatalf("complete ready upload: %v", err)
	}

	openDigest := digest('c')
	createUpload(t, repo, collection.ID, "open", manifest(
		manageddata.File{Path: "open.csv", Size: 3, SHA256: openDigest},
		manageddata.File{Path: "duplicate.csv", Size: 2, SHA256: duplicateDigest},
	))
	committingDigest := digest('d')
	committing := createUpload(t, repo, collection.ID, "committing", manifest(
		manageddata.File{Path: "committing.csv", Size: 4, SHA256: committingDigest},
	))
	if _, err := db.ExecContext(t.Context(), `UPDATE managed_data_upload_sessions SET status = 'committing' WHERE id = ?`, committing.ID); err != nil {
		t.Fatalf("mark upload committing: %v", err)
	}
	abortedDigest := digest('e')
	aborted := createUpload(t, repo, collection.ID, "aborted", manifest(
		manageddata.File{Path: "aborted.csv", Size: 5, SHA256: abortedDigest},
	))
	if err := repo.AbortUploadSession(t.Context(), aborted.ID); err != nil {
		t.Fatalf("abort upload: %v", err)
	}

	first, err := source.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := source.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{readyDigest, duplicateDigest, openDigest, committingDigest}
	if !reflect.DeepEqual(first.SHA256s, want) {
		t.Fatalf("Snapshot SHA256s = %#v, want %#v", first.SHA256s, want)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated snapshots differ: %#v != %#v", first, second)
	}
}

func TestSnapshotRejectsNoncanonicalOrInvalidDurableManifest(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
	}{
		{name: "noncanonical JSON", manifest: `{"files": []}`},
		{name: "invalid digest", manifest: `{"files":[{"path":"data.csv","size":1,"sha256":"INVALID"}]}`},
		{name: "unknown field", manifest: `{"files":[],"legacy":true}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, repo := testDatabase(t)
			source, err := New(db)
			if err != nil {
				t.Fatal(err)
			}
			collection := createCollection(t, repo)
			upload := createUpload(t, repo, collection.ID, "corrupt", manifest(
				manageddata.File{Path: "data.csv", Size: 1, SHA256: digest('a')},
			))
			if _, err := db.ExecContext(t.Context(), `UPDATE managed_data_upload_sessions SET manifest_json = ? WHERE id = ?`, test.manifest, upload.ID); err != nil {
				t.Fatalf("corrupt durable manifest: %v", err)
			}
			_, err = source.Snapshot(t.Context())
			if !errors.Is(err, storage.ErrIntegrity) {
				t.Fatalf("Snapshot error = %v, want storage.ErrIntegrity", err)
			}
		})
	}
}

func TestWithStableSnapshotRejectsStaleGeneration(t *testing.T) {
	db, repo := testDatabase(t)
	source, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	collection := createCollection(t, repo)
	initial, err := source.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	createUpload(t, repo, collection.ID, "new", manifest(
		manageddata.File{Path: "new.csv", Size: 1, SHA256: digest('a')},
	))
	called := false
	err = source.WithStableSnapshot(t.Context(), initial.Generation, func(maintenance.ReachabilitySnapshot) error {
		called = true
		return nil
	})
	if !errors.Is(err, maintenance.ErrReachabilityChanged) || called {
		t.Fatalf("WithStableSnapshot error = %v, called = %v", err, called)
	}
}

func TestWithStableSnapshotBlocksUploadFinalizationThroughCallback(t *testing.T) {
	db, repo := testDatabase(t)
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	source, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	collection := createCollection(t, repo)
	file := manageddata.File{Path: "orders.csv", Size: 6, SHA256: digest('f')}
	upload := createUpload(t, repo, collection.ID, "finalize", manifest(file))
	initial, err := source.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	callbackEntered := make(chan struct{})
	releaseCallback := make(chan struct{})
	stableDone := make(chan error, 1)
	go func() {
		stableDone <- source.WithStableSnapshot(t.Context(), initial.Generation, func(snapshot maintenance.ReachabilitySnapshot) error {
			if !reflect.DeepEqual(snapshot.SHA256s, []string{file.SHA256}) {
				return errors.New("stable snapshot lost open upload digest")
			}
			close(callbackEntered)
			<-releaseCallback
			return nil
		})
	}()
	select {
	case <-callbackEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("stable callback did not start")
	}

	finalizeDone := make(chan error, 1)
	go func() {
		_, finalizeErr := repo.CompleteUpload(context.Background(), manageddata.CompleteUploadInput{
			SessionID: upload.ID,
			Files:     []manageddata.StoredFile{{File: file, StorageKey: "objects/" + file.SHA256}},
		})
		finalizeDone <- finalizeErr
	}()
	select {
	case err := <-finalizeDone:
		t.Fatalf("finalization crossed stable callback boundary: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(releaseCallback)
	if err := <-stableDone; err != nil {
		t.Fatalf("WithStableSnapshot: %v", err)
	}
	select {
	case err := <-finalizeDone:
		if err != nil {
			t.Fatalf("finalize after stable callback: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("finalization remained blocked after stable callback")
	}
}

func TestWithStableSnapshotRollsBackAfterCancellation(t *testing.T) {
	db, repo := testDatabase(t)
	db.SetMaxOpenConns(4)
	source, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	collection := createCollection(t, repo)
	initial, err := source.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	err = source.WithStableSnapshot(ctx, initial.Generation, func(maintenance.ReachabilitySnapshot) error {
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WithStableSnapshot error = %v", err)
	}

	writeCtx, writeCancel := context.WithTimeout(t.Context(), time.Second)
	defer writeCancel()
	if _, err := repo.CreateUploadSession(writeCtx, manageddata.CreateUploadSessionInput{
		CollectionID:   collection.ID,
		Manifest:       manifest(manageddata.File{Path: "after-cancel.csv", Size: 1, SHA256: digest('a')}),
		StorageBackend: "local",
		StagingPrefix:  "staging/after-cancel",
		ExpiresAt:      time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("write after cancellation rollback: %v", err)
	}
}

func TestNewRejectsNilDatabase(t *testing.T) {
	if _, err := New(nil); !errors.Is(err, maintenance.ErrInvalidMaintenance) {
		t.Fatalf("New(nil) error = %v", err)
	}
}

func testDatabase(t *testing.T) (*sql.DB, *managedsqlite.Repository) {
	t.Helper()
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatalf("open migrated platform database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store.SQLDB(), managedsqlite.NewRepository(store.SQLDB())
}

func createCollection(t *testing.T, repo *managedsqlite.Repository) manageddata.Collection {
	t.Helper()
	collection, err := repo.CreateCollection(t.Context(), manageddata.CreateCollectionInput{
		ID:           "collection-orders",
		ProjectID:    "project-orders",
		ConnectionID: "orders",
		Name:         "Orders",
	})
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	return collection
}

func createUpload(t *testing.T, repo *managedsqlite.Repository, collectionID, suffix string, value manageddata.Manifest) manageddata.UploadSession {
	t.Helper()
	upload, err := repo.CreateUploadSession(t.Context(), manageddata.CreateUploadSessionInput{
		ID:             "upload-" + suffix,
		CollectionID:   collectionID,
		Manifest:       value,
		StorageBackend: "local",
		StagingPrefix:  "staging/" + suffix,
		ExpiresAt:      time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create upload %s: %v", suffix, err)
	}
	return upload
}

func manifest(files ...manageddata.File) manageddata.Manifest {
	return manageddata.Manifest{Files: files}
}

func digest(char byte) string {
	return strings.Repeat(string(char), 64)
}
