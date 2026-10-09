package module

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/control"
	"github.com/flidai/leapview/internal/manageddata/maintenance"
	managedpostgres "github.com/flidai/leapview/internal/manageddata/postgres"
	"github.com/flidai/leapview/internal/manageddata/s3multipart"
	"github.com/flidai/leapview/internal/manageddata/storage"
	"github.com/flidai/leapview/internal/manageddata/storage/filesystem"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMaintenanceNativeBlobGCContinuesAfterMultipartRecoveryError(t *testing.T) {
	for _, resolution := range []string{"completed", "aborted"} {
		t.Run(resolution, func(t *testing.T) { maintenanceNativeBlobGCProgress(t, resolution) })
	}
}

func maintenanceNativeBlobGCProgress(t *testing.T, resolution string) {
	h := postgrestest.Start(t)
	role := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "runtime-secret", Login: true})
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
	if err := managedpostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	p, err := pgxpool.New(t.Context(), db.URL(role))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	r := managedpostgres.New(p)
	store, err := filesystem.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	put := func(body []byte) storage.Blob {
		t.Helper()
		sum := sha256.Sum256(body)
		blob := storage.Blob{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
		if _, err := store.Put(t.Context(), blob, bytes.NewReader(body)); err != nil {
			t.Fatal(err)
		}
		return blob
	}
	protected, orphan, shared := put([]byte("unfinished")), put([]byte("orphan")), put([]byte("held revision"))
	c, err := r.CreateCollection(t.Context(), manageddata.CreateCollectionInput{ID: "collection_gc_progress", ProjectID: "project_gc", ConnectionID: "connection_gc", Name: "GC progress"})
	if err != nil {
		t.Fatal(err)
	}
	create := func(id string, blob storage.Blob) manageddata.UploadSession {
		t.Helper()
		u, err := r.CreateUploadSession(t.Context(), manageddata.CreateUploadSessionInput{ID: manageddata.UploadID(id), CollectionID: c.ID,
			Manifest:       manageddata.Manifest{Files: []manageddata.File{{Path: "data.csv", SHA256: blob.SHA256, Size: blob.Size}}},
			StorageBackend: "s3", StagingPrefix: "uploads/" + id, ExpiresAt: time.Now().UTC().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	begin := func(session manageddata.UploadSession, blob storage.Blob, id string) manageddata.S3MultipartUpload {
		t.Helper()
		m, err := r.CreateS3MultipartUpload(t.Context(), manageddata.CreateS3MultipartUploadInput{ID: manageddata.MultipartUploadID(id), UploadSessionID: session.ID, LogicalPath: "data.csv", SHA256: blob.SHA256, SizeBytes: blob.Size, IdempotencyIdentity: "create"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.InitializeS3MultipartUpload(t.Context(), manageddata.InitializeS3MultipartUploadInput{ID: m.ID, ObjectKey: "blobs/" + blob.SHA256, ProviderUploadID: "provider-" + id}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.ReserveS3MultipartPart(t.Context(), manageddata.S3MultipartPart{MultipartUploadID: m.ID, PartNumber: 1, SizeBytes: blob.Size, SHA256: blob.SHA256}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.BeginS3MultipartCompletion(t.Context(), manageddata.BeginS3MultipartCompletionInput{ID: m.ID, IdempotencyIdentity: "complete", RequestHash: strings.Repeat("a", 64)}); err != nil {
			t.Fatal(err)
		}
		return m
	}
	u := create("upload_gc_progress", protected)
	m := begin(u, protected, "multipart_gc_progress")
	if err := r.AbortUploadSession(t.Context(), u.ID); err != nil {
		t.Fatal(err)
	}
	retaining := create("upload_gc_shared_intent", shared)
	sharedIntent := begin(retaining, shared, "multipart_gc_shared")
	if err := r.AbortUploadSession(t.Context(), retaining.ID); err != nil {
		t.Fatal(err)
	}
	held := create("upload_gc_shared", shared)
	revision, err := r.CompleteUpload(t.Context(), manageddata.CompleteUploadInput{SessionID: held.ID, Files: []manageddata.StoredFile{{File: manageddata.File{Path: "data.csv", SHA256: shared.SHA256, Size: shared.Size}, StorageKey: "blobs/" + shared.SHA256}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.RecordRetentionRoot(t.Context(), managedpostgres.RetentionRoot{RootID: "root_gc_shared", ProjectID: "project_gc", Environment: "prod", RevisionID: revision.ID.String(), Evidence: []byte(`{"fixture":"gc-progress"}`)}); err != nil {
		t.Fatal(err)
	}
	source, err := managedpostgres.NewReachabilitySource(p)
	if err != nil {
		t.Fatal(err)
	}
	collector, err := maintenance.NewBlobCollector(store, source, maintenance.BlobGCConfig{GraceAge: time.Hour, Now: func() time.Time { return time.Now().UTC().Add(24 * time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	uploads, err := control.New(r, store, control.Config{UploadTTL: time.Hour, Transport: maintenanceNativeCleanupTransport{}})
	if err != nil {
		t.Fatal(err)
	}
	provider := &maintenanceRetryableProvider{}
	multipart, err := s3multipart.New(r, provider, s3multipart.Config{Backend: "s3"})
	if err != nil {
		t.Fatal(err)
	}
	pass := Maintenance{uploads: uploads, multipart: multipart, uploadTTL: 0, collector: collector}
	_, err = pass.ExpireUploads(t.Context())
	if !errors.Is(err, control.ErrBackend) || provider.calls != 2 {
		t.Fatalf("recovery boundary not reached: calls=%d error=%v", provider.calls, err)
	}
	if _, err := store.Stat(t.Context(), orphan.SHA256); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("unrelated aged orphan survived retryable multipart error: %v", err)
	}
	for _, blob := range []storage.Blob{protected, shared} {
		reader, err := store.Open(t.Context(), blob.SHA256)
		if err != nil {
			t.Fatalf("durable protected bytes deleted: %s: %v", blob.SHA256, err)
		}
		body, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		want := map[string]string{protected.SHA256: "unfinished", shared.SHA256: "held revision"}[blob.SHA256]
		if readErr != nil || closeErr != nil || string(body) != want {
			t.Fatalf("protected bytes changed: body=%q read=%v close=%v", body, readErr, closeErr)
		}
	}
	current, err := r.S3MultipartUploadByID(t.Context(), m.ID)
	if err != nil || current.Status != manageddata.S3MultipartStatusCompleting {
		t.Fatalf("retryable intent changed: %#v, %v", current, err)
	}
	// Resolve both durable intents. Only the exclusive object is eligible: the
	// second digest still belongs to the ready revision and its live root.
	for _, intent := range []manageddata.S3MultipartUpload{m, sharedIntent} {
		if resolution == "completed" {
			if _, err := r.FinishS3MultipartCompletion(t.Context(), intent.ID); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := r.FailS3MultipartUpload(t.Context(), intent.ID, "completion rejected by provider"); err != nil {
				t.Fatal(err)
			}
			if _, err := r.BeginS3MultipartAbort(t.Context(), manageddata.BeginS3MultipartAbortInput{ID: intent.ID, IdempotencyIdentity: "abort-resolved"}); err != nil {
				t.Fatal(err)
			}
			if _, err := r.FinishS3MultipartAbort(t.Context(), intent.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := pass.ExpireUploads(t.Context()); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 2 {
		t.Fatalf("resolved intents retried provider: calls=%d", provider.calls)
	}
	if _, err := store.Stat(t.Context(), protected.SHA256); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("exclusive bytes survived authoritative %s resolution: %v", resolution, err)
	}
	reader, err := store.Open(t.Context(), shared.SHA256)
	if err != nil {
		t.Fatal("resolution deleted bytes shared with a live revision root", err)
	}
	body, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || string(body) != "held revision" {
		t.Fatalf("shared root bytes changed: body=%q read=%v close=%v", body, readErr, closeErr)
	}
}

type maintenanceRetryableProvider struct {
	s3multipart.MultipartStore
	calls int
}

type maintenanceNativeCleanupTransport struct{ control.Transport }

func (maintenanceNativeCleanupTransport) Backend() string { return "s3" }
func (maintenanceNativeCleanupTransport) Abort(context.Context, control.TransportRequest) error {
	return nil
}

func (p *maintenanceRetryableProvider) RecoverMultipart(context.Context, storage.MultipartUpload, []storage.MultipartPartRequest) (storage.Blob, error) {
	p.calls++
	return storage.Blob{}, storage.ErrBackend
}
