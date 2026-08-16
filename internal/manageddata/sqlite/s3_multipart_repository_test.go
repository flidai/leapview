package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
)

func TestS3MultipartRepositoryCreateIsIdempotentAndRejectsConflicts(t *testing.T) {
	ctx, repo, session := multipartRepositoryFixture(t)
	input := manageddata.CreateS3MultipartUploadInput{
		ID: "multipart-1", UploadSessionID: session.ID, LogicalPath: "data.csv", SHA256: strings.Repeat("a", 64),
		SizeBytes: 9, IdempotencyIdentity: strings.Repeat("1", 64),
	}

	created, err := repo.CreateS3MultipartUpload(ctx, input)
	if err != nil {
		t.Fatalf("create multipart upload: %v", err)
	}
	retry, err := repo.CreateS3MultipartUpload(ctx, input)
	if err != nil {
		t.Fatalf("retry multipart upload: %v", err)
	}
	if retry != created || retry.Status != manageddata.S3MultipartStatusCreating {
		t.Fatalf("retry = %#v, want %#v", retry, created)
	}

	input.SizeBytes++
	if _, err := repo.CreateS3MultipartUpload(ctx, input); !errors.Is(err, manageddata.ErrConflict) {
		t.Fatalf("conflicting retry error = %v, want conflict", err)
	}
	input.ID = "multipart-2"
	if _, err := repo.CreateS3MultipartUpload(ctx, input); !errors.Is(err, manageddata.ErrConflict) {
		t.Fatalf("reused identity error = %v, want conflict", err)
	}
}

func TestS3MultipartRepositoryPersistsPartsAndTransitionsAtomically(t *testing.T) {
	ctx, repo, session := multipartRepositoryFixture(t)
	upload := createMultipartRecord(t, ctx, repo, session.ID, 11)
	upload, err := repo.InitializeS3MultipartUpload(ctx, manageddata.InitializeS3MultipartUploadInput{
		ID: upload.ID, ObjectKey: "blobs/sha256/aa/" + upload.SHA256, ProviderUploadID: "provider-1",
	})
	if err != nil {
		t.Fatalf("initialize multipart upload: %v", err)
	}
	if upload.Status != manageddata.S3MultipartStatusOpen || upload.ProviderUploadID != "provider-1" {
		t.Fatalf("initialized upload = %#v", upload)
	}

	first := manageddata.S3MultipartPart{MultipartUploadID: upload.ID, PartNumber: 1, SizeBytes: 6, SHA256: strings.Repeat("b", 64)}
	if _, err := repo.ReserveS3MultipartPart(ctx, first); err != nil {
		t.Fatalf("reserve first part: %v", err)
	}
	if retry, err := repo.ReserveS3MultipartPart(ctx, first); err != nil || retry != first {
		t.Fatalf("retry first part = %#v, err = %v", retry, err)
	}
	conflict := first
	conflict.SizeBytes = 7
	if _, err := repo.ReserveS3MultipartPart(ctx, conflict); !errors.Is(err, manageddata.ErrConflict) {
		t.Fatalf("conflicting part error = %v, want conflict", err)
	}
	if _, err := repo.ReserveS3MultipartPart(ctx, manageddata.S3MultipartPart{MultipartUploadID: upload.ID, PartNumber: 2, SizeBytes: 6}); !errors.Is(err, manageddata.ErrConflict) {
		t.Fatalf("oversized reservation error = %v, want conflict", err)
	}
	second := manageddata.S3MultipartPart{MultipartUploadID: upload.ID, PartNumber: 2, SizeBytes: 5}
	if _, err := repo.ReserveS3MultipartPart(ctx, second); err != nil {
		t.Fatalf("reserve second part: %v", err)
	}

	claim := manageddata.BeginS3MultipartCompletionInput{
		ID: upload.ID, IdempotencyIdentity: strings.Repeat("2", 64), RequestHash: strings.Repeat("3", 64),
	}
	completion, err := repo.BeginS3MultipartCompletion(ctx, claim)
	if err != nil {
		t.Fatalf("begin completion: %v", err)
	}
	if !completion.Execute || completion.Upload.Status != manageddata.S3MultipartStatusCompleting || len(completion.Parts) != 2 {
		t.Fatalf("completion claim = %#v", completion)
	}
	if _, err := repo.ReserveS3MultipartPart(ctx, manageddata.S3MultipartPart{MultipartUploadID: upload.ID, PartNumber: 3, SizeBytes: 1}); !errors.Is(err, manageddata.ErrConflict) {
		t.Fatalf("part after completion claim error = %v, want conflict", err)
	}
	if retry, err := repo.BeginS3MultipartCompletion(ctx, claim); err != nil || !retry.Execute {
		t.Fatalf("retry completion claim = %#v, err = %v", retry, err)
	}
	claim.RequestHash = strings.Repeat("4", 64)
	if _, err := repo.BeginS3MultipartCompletion(ctx, claim); !errors.Is(err, manageddata.ErrConflict) {
		t.Fatalf("conflicting completion error = %v, want conflict", err)
	}

	completed, err := repo.FinishS3MultipartCompletion(ctx, upload.ID)
	if err != nil {
		t.Fatalf("finish completion: %v", err)
	}
	if completed.Status != manageddata.S3MultipartStatusCompleted || completed.CompletedAt == "" {
		t.Fatalf("completed upload = %#v", completed)
	}
	stable, err := repo.FinishS3MultipartCompletion(ctx, upload.ID)
	if err != nil || stable != completed {
		t.Fatalf("stable completion = %#v, err = %v", stable, err)
	}
}

func TestS3MultipartRepositoryAbortIsRetrySafeAndRecoverable(t *testing.T) {
	ctx, repo, session := multipartRepositoryFixture(t)
	upload := createMultipartRecord(t, ctx, repo, session.ID, 5)
	upload, err := repo.InitializeS3MultipartUpload(ctx, manageddata.InitializeS3MultipartUploadInput{
		ID: upload.ID, ObjectKey: "objects/a", ProviderUploadID: "provider-orphan",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FailS3MultipartUpload(ctx, upload.ID, "storage integrity verification failed"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	recoverable, err := repo.ListRecoverableS3MultipartUploads(ctx, time.Now().Add(time.Hour), 10)
	if err != nil || len(recoverable) != 1 || recoverable[0].ProviderUploadID != "provider-orphan" {
		t.Fatalf("recoverable uploads = %#v, err = %v", recoverable, err)
	}
	claim := manageddata.BeginS3MultipartAbortInput{ID: upload.ID, IdempotencyIdentity: strings.Repeat("5", 64)}
	abort, err := repo.BeginS3MultipartAbort(ctx, claim)
	if err != nil || !abort.Execute || abort.Upload.Status != manageddata.S3MultipartStatusAborting {
		t.Fatalf("abort claim = %#v, err = %v", abort, err)
	}
	if retry, err := repo.BeginS3MultipartAbort(ctx, claim); err != nil || !retry.Execute {
		t.Fatalf("retry abort claim = %#v, err = %v", retry, err)
	}

	aborted, err := repo.FinishS3MultipartAbort(ctx, upload.ID)
	if err != nil {
		t.Fatalf("finish abort: %v", err)
	}
	if aborted.Status != manageddata.S3MultipartStatusAborted || aborted.AbortedAt == "" || aborted.Error != "storage integrity verification failed" {
		t.Fatalf("aborted upload = %#v", aborted)
	}
	stable, err := repo.FinishS3MultipartAbort(ctx, upload.ID)
	if err != nil || stable != aborted {
		t.Fatalf("stable abort = %#v, err = %v", stable, err)
	}
}

func multipartRepositoryFixture(t *testing.T) (context.Context, *Repository, manageddata.UploadSession) {
	t.Helper()
	ctx, _, repo := testRepository(t)
	collection := createCollection(t, ctx, repo, "multipart", "project-a", "warehouse")
	manifest := manageddata.Manifest{Files: []manageddata.File{{Path: "data.csv", Size: 11, SHA256: strings.Repeat("a", 64)}}}
	session, err := repo.CreateUploadSession(ctx, manageddata.CreateUploadSessionInput{
		ID: "upload-multipart", CollectionID: collection.ID, Manifest: manifest, StorageBackend: "s3",
		StagingPrefix: "uploads/upload-multipart", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, repo, session
}

func createMultipartRecord(t *testing.T, ctx context.Context, repo *Repository, sessionID manageddata.UploadID, size int64) manageddata.S3MultipartUpload {
	t.Helper()
	upload, err := repo.CreateS3MultipartUpload(ctx, manageddata.CreateS3MultipartUploadInput{
		ID: "multipart-1", UploadSessionID: sessionID, LogicalPath: "data.csv", SHA256: strings.Repeat("a", 64),
		SizeBytes: size, IdempotencyIdentity: strings.Repeat("1", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	return upload
}
