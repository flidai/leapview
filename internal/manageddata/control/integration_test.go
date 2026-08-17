package control_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/control"
	managedsqlite "github.com/flidai/leapview/internal/manageddata/sqlite"
	"github.com/flidai/leapview/internal/manageddata/storage"
	"github.com/flidai/leapview/internal/manageddata/storage/filesystem"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

func TestFilesystemAndSQLiteConcurrentFinalizeIsAtomicIdempotentAndDetectsLoss(t *testing.T) {
	ctx := t.Context()
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "leapview.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = database.Close() })
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpContext(ctx, database, "../../platform/migrations"); err != nil {
		t.Fatalf("migrate platform store: %v", err)
	}
	repo := managedsqlite.NewRepository(database)
	blobs, err := filesystem.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := "id,total\n1,42\n"
	digestBytes := sha256.Sum256([]byte(body))
	digest := hex.EncodeToString(digestBytes[:])
	if _, err := blobs.Put(ctx, storage.Blob{SHA256: digest, Size: int64(len(body))}, strings.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	service, err := control.New(repo, blobs, control.Config{
		Limits:    manageddata.Limits{MaxFiles: 10, MaxFileBytes: 1 << 20, MaxRevisionBytes: 1 << 20},
		UploadTTL: time.Hour, VerifyConcurrency: 4, Transport: &fakeTransport{backend: "local"},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.BeginUpload(ctx, control.BeginUploadRequest{
		Project: "project-a", Connection: "orders", Actor: "principal-a", IdempotencyKey: "concurrent-finalize",
		Manifest: manageddata.Manifest{Files: []manageddata.File{{Path: "orders.csv", Size: int64(len(body)), SHA256: digest}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	const callers = 8
	results := make([]control.FinalizeResult, callers)
	errs := make([]error, callers)
	var ready sync.WaitGroup
	ready.Add(callers)
	start := make(chan struct{})
	var calls sync.WaitGroup
	for index := range callers {
		calls.Add(1)
		go func() {
			defer calls.Done()
			ready.Done()
			<-start
			results[index], errs[index] = service.FinalizeUpload(context.Background(), control.UploadRequest{
				Project: "project-a", Connection: "orders", UploadID: started.ID,
			})
		}()
	}
	ready.Wait()
	close(start)
	calls.Wait()

	revisionID := results[0].Revision.ID
	for index := range callers {
		if errs[index] != nil {
			t.Fatalf("finalize caller %d: %v", index, errs[index])
		}
		if results[index].Revision.ID != revisionID || results[index].Upload.Status != manageddata.UploadStatusComplete {
			t.Fatalf("finalize caller %d result = %#v", index, results[index])
		}
	}
	if revisionID == "" || len(results[0].Revision.Files) != 1 {
		t.Fatalf("revision = %#v", results[0].Revision)
	}
	file := results[0].Revision.Files[0]
	if file.StorageURI == "" || !strings.HasPrefix(file.StorageURI, "file://") || !strings.Contains(file.StorageURI, digest) {
		t.Fatalf("stable storage URI = %q", file.StorageURI)
	}
	if file.MediaType != "text/csv" {
		t.Fatalf("media type = %q", file.MediaType)
	}
	var revisionCount int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM managed_data_revisions WHERE collection_id = ?`, results[0].Revision.Collection.ID).Scan(&revisionCount); err != nil {
		t.Fatal(err)
	}
	if revisionCount != 1 {
		t.Fatalf("revision count = %d, want 1", revisionCount)
	}

	if err := blobs.DeleteBlobs(ctx, []string{digest}); err != nil {
		t.Fatal(err)
	}
	_, err = service.FinalizeUpload(ctx, control.UploadRequest{Project: "project-a", Connection: "orders", UploadID: started.ID})
	if !errors.Is(err, control.ErrIntegrity) {
		t.Fatalf("retry finalize after blob loss error = %v, want ErrIntegrity", err)
	}
	_, err = service.RecoverUpload(ctx, control.UploadRequest{Project: "project-a", Connection: "orders", UploadID: started.ID})
	if !errors.Is(err, control.ErrIntegrity) {
		t.Fatalf("recover completed revision after blob loss error = %v, want ErrIntegrity", err)
	}
}

func TestTerminalCleanupIsBoundedAndDoesNotStarveLaterSessions(t *testing.T) {
	ctx := t.Context()
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "cleanup.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = database.Close() })
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpContext(ctx, database, "../../platform/migrations"); err != nil {
		t.Fatal(err)
	}
	repo := managedsqlite.NewRepository(database)
	transport := &fakeTransport{backend: "local"}
	service, err := control.New(repo, &fakeBlobStore{blobs: map[string]storage.Blob{}}, control.Config{Limits: manageddata.Limits{MaxFiles: 1, MaxFileBytes: 10, MaxRevisionBytes: 10}, UploadTTL: time.Hour, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 105; i++ {
		upload, beginErr := service.BeginUpload(ctx, control.BeginUploadRequest{Project: "project-a", Connection: "cleanup", IdempotencyKey: fmt.Sprintf("cleanup-%d", i), Manifest: manageddata.Manifest{Files: []manageddata.File{{Path: "x", Size: 1, SHA256: strings.Repeat("a", 64)}}}})
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		uploadID, parseErr := manageddata.ParseUploadID(upload.ID)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if abortErr := repo.AbortUploadSession(ctx, uploadID); abortErr != nil {
			t.Fatal(abortErr)
		}
	}
	first, err := service.ExpireUploads(ctx)
	if err != nil || first.Cleaned != 100 || first.CleanupBacklog != 1 {
		t.Fatalf("first cleanup = %#v, err=%v", first, err)
	}
	second, err := service.ExpireUploads(ctx)
	if err != nil || second.Cleaned != 5 || second.CleanupBacklog != 0 {
		t.Fatalf("second cleanup = %#v, err=%v", second, err)
	}
}

func TestTerminalCleanupRetriesTransientTransportFailure(t *testing.T) {
	ctx := t.Context()
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "cleanup-retry.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = database.Close() })
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpContext(ctx, database, "../../platform/migrations"); err != nil {
		t.Fatal(err)
	}
	repo := managedsqlite.NewRepository(database)
	transport := &fakeTransport{backend: "local", abortErr: errors.New("transient cleanup")}
	service, err := control.New(repo, &fakeBlobStore{blobs: map[string]storage.Blob{}}, control.Config{Limits: manageddata.Limits{MaxFiles: 1, MaxFileBytes: 10, MaxRevisionBytes: 10}, UploadTTL: time.Hour, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	upload, err := service.BeginUpload(ctx, control.BeginUploadRequest{Project: "project-a", Connection: "retry", IdempotencyKey: "retry", Manifest: manageddata.Manifest{Files: []manageddata.File{{Path: "x", Size: 1, SHA256: strings.Repeat("a", 64)}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AbortUpload(ctx, control.UploadRequest{Project: "project-a", Connection: "retry", UploadID: upload.ID}); err == nil {
		t.Fatal("abort unexpectedly ignored transient cleanup failure")
	}
	first, err := service.ExpireUploads(ctx)
	if err != nil || first.CleanupFailures == 0 {
		t.Fatalf("first retry pass = %#v, err=%v", first, err)
	}
	transport.abortErr = nil
	second, err := service.ExpireUploads(ctx)
	if err != nil || second.Cleaned != 1 || second.CleanupFailures != 0 {
		t.Fatalf("second retry pass = %#v, err=%v", second, err)
	}
}
