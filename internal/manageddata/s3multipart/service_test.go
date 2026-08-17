package s3multipart

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/control"
	"github.com/flidai/leapview/internal/manageddata/sqlite"
	"github.com/flidai/leapview/internal/manageddata/storage"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

const (
	minPart = int64(5 * 1024 * 1024)
	nowText = "2026-07-14T10:00:00Z"
)

func TestCoordinatorCreateSignCompleteIsStrictAndRetrySafe(t *testing.T) {
	ctx, repo, session := coordinatorFixture(t, []manageddata.File{
		{Path: "large.csv", Size: minPart + 3, SHA256: strings.Repeat("a", 64)},
		{Path: "other.csv", Size: 1, SHA256: strings.Repeat("b", 64)},
	})
	provider := &fakeMultipartStore{}
	service := newTestService(t, repo, provider)
	create := CreateRequest{Project: "project-a", Connection: "warehouse", UploadSessionID: session.ID.String(), Path: "large.csv", IdempotencyKey: "create-1"}

	upload, err := service.Create(ctx, create)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if upload.Status != StatusOpen || upload.File.Path != "large.csv" || upload.Existing {
		t.Fatalf("upload = %#v", upload)
	}
	retry, err := service.Create(ctx, create)
	if err != nil || retry != upload || provider.createCalls != 1 {
		t.Fatalf("create retry = %#v, calls = %d, err = %v", retry, provider.createCalls, err)
	}
	create.Path = "other.csv"
	if _, err := service.Create(ctx, create); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("conflicting create error = %v, want conflict", err)
	}

	if _, err := service.SignPart(ctx, SignPartRequest{
		Project: "project-a", Connection: "warehouse", UploadSessionID: session.ID.String(),
		MultipartUploadID: upload.ID, PartNumber: 1, Size: minPart, SHA256: strings.Repeat("c", 64),
	}); err != nil {
		t.Fatalf("sign first part: %v", err)
	}
	signed, err := service.SignPart(ctx, SignPartRequest{
		Project: "project-a", Connection: "warehouse", UploadSessionID: session.ID.String(),
		MultipartUploadID: upload.ID, PartNumber: 2, Size: 3,
	})
	if err != nil {
		t.Fatalf("sign final part: %v", err)
	}
	if signed.UploadSessionID != session.ID.String() || signed.MultipartUploadID != upload.ID || signed.URL == "" || signed.ExpiresAt != "2026-07-14T10:15:00Z" || len(signed.Headers) != 1 {
		t.Fatalf("signed part = %#v", signed)
	}
	if _, err := service.SignPart(ctx, SignPartRequest{
		Project: "project-a", Connection: "warehouse", UploadSessionID: session.ID.String(),
		MultipartUploadID: upload.ID, PartNumber: 2, Size: 4,
	}); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("conflicting sign error = %v, want conflict", err)
	}

	complete := CompleteRequest{
		Project: "project-a", Connection: "warehouse", UploadSessionID: session.ID.String(),
		MultipartUploadID: upload.ID, IdempotencyKey: "complete-1",
		Parts: []CompletedPart{{PartNumber: 2, ETag: "etag-2"}, {PartNumber: 1, ETag: "etag-1", SHA256: strings.Repeat("c", 64)}},
	}
	completed, err := service.Complete(ctx, complete)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if completed.Status != StatusCompleted || provider.completeCalls != 1 || provider.completedParts[0].Number != 1 {
		t.Fatalf("completed = %#v, provider = %#v", completed, provider)
	}
	retry, err = service.Complete(ctx, complete)
	if err != nil || retry != completed || provider.completeCalls != 1 {
		t.Fatalf("complete retry = %#v, calls = %d, err = %v", retry, provider.completeCalls, err)
	}
}

func TestCoordinatorRejectsWrongScopeManifestAndMultipartShape(t *testing.T) {
	ctx, repo, session := coordinatorFixture(t, []manageddata.File{{Path: "data.csv", Size: minPart + 1, SHA256: strings.Repeat("a", 64)}})
	service := newTestService(t, repo, &fakeMultipartStore{})

	for _, request := range []CreateRequest{
		{Project: "project-b", Connection: "warehouse", UploadSessionID: session.ID.String(), Path: "data.csv", IdempotencyKey: "key"},
		{Project: " project-a", Connection: "warehouse", UploadSessionID: session.ID.String(), Path: "data.csv", IdempotencyKey: "key"},
		{Project: "project-a", Connection: "warehouse", UploadSessionID: session.ID.String(), Path: "missing.csv", IdempotencyKey: "key"},
	} {
		if _, err := service.Create(ctx, request); err == nil {
			t.Fatalf("Create(%#v) unexpectedly succeeded", request)
		}
	}

	upload, err := service.Create(ctx, CreateRequest{Project: "project-a", Connection: "warehouse", UploadSessionID: session.ID.String(), Path: "data.csv", IdempotencyKey: "create"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SignPart(ctx, SignPartRequest{Project: "project-a", Connection: "warehouse", UploadSessionID: session.ID.String(), MultipartUploadID: upload.ID, PartNumber: 1, Size: minPart + 2}); !errors.Is(err, control.ErrInvalid) {
		t.Fatalf("oversized part error = %v, want invalid", err)
	}
	if _, err := service.SignPart(ctx, SignPartRequest{Project: "project-a", Connection: "warehouse", UploadSessionID: session.ID.String(), MultipartUploadID: upload.ID, PartNumber: 1, Size: 1}); err != nil {
		t.Fatal(err)
	}
	_, err = service.Complete(ctx, CompleteRequest{
		Project: "project-a", Connection: "warehouse", UploadSessionID: session.ID.String(),
		MultipartUploadID: upload.ID, IdempotencyKey: "complete", Parts: []CompletedPart{{PartNumber: 1, ETag: "etag"}},
	})
	if !errors.Is(err, control.ErrInvalid) {
		t.Fatalf("incomplete shape error = %v, want invalid", err)
	}
}

func TestCoordinatorSanitizesIntegrityFailureAndRecoversProviderUpload(t *testing.T) {
	ctx, repo, session := coordinatorFixture(t, []manageddata.File{{Path: "data.csv", Size: 1, SHA256: strings.Repeat("a", 64)}})
	provider := &fakeMultipartStore{completeErr: fmt.Errorf("raw backend secret: %w", storage.ErrIntegrity)}
	service := newTestService(t, repo, provider)
	upload, err := service.Create(ctx, CreateRequest{Project: "project-a", Connection: "warehouse", UploadSessionID: session.ID.String(), Path: "data.csv", IdempotencyKey: "create"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SignPart(ctx, SignPartRequest{Project: "project-a", Connection: "warehouse", UploadSessionID: session.ID.String(), MultipartUploadID: upload.ID, PartNumber: 1, Size: 1}); err != nil {
		t.Fatal(err)
	}
	_, err = service.Complete(ctx, CompleteRequest{
		Project: "project-a", Connection: "warehouse", UploadSessionID: session.ID.String(),
		MultipartUploadID: upload.ID, IdempotencyKey: "complete", Parts: []CompletedPart{{PartNumber: 1, ETag: "etag"}},
	})
	if !errors.Is(err, control.ErrIntegrity) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("completion error = %v", err)
	}
	stored, err := repo.S3MultipartUploadByID(ctx, manageddata.MultipartUploadID(upload.ID))
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != manageddata.S3MultipartStatusFailed || stored.Error != integrityTerminalError || strings.Contains(stored.Error, "secret") {
		t.Fatalf("stored failure = %#v", stored)
	}

	recovery, err := service.RecoverOrphaned(ctx, time.Now().Add(time.Hour), 10)
	if err != nil || recovery.Aborted != 1 || provider.abortCalls != 1 {
		t.Fatalf("recovery = %#v, abort calls = %d, err = %v", recovery, provider.abortCalls, err)
	}
}

func TestCoordinatorRecoversOpenMultipartAfterParentAbort(t *testing.T) {
	ctx, repo, session := coordinatorFixture(t, []manageddata.File{{Path: "data.csv", Size: 1, SHA256: strings.Repeat("a", 64)}})
	provider := &fakeMultipartStore{}
	service := newTestService(t, repo, provider)
	upload, err := service.Create(ctx, CreateRequest{Project: "project-a", Connection: "warehouse", UploadSessionID: session.ID.String(), Path: "data.csv", IdempotencyKey: "create"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AbortUploadSession(ctx, session.ID); err != nil {
		t.Fatal(err)
	}

	recovery, err := service.RecoverOrphaned(ctx, time.Now().Add(time.Hour), 10)
	if err != nil || recovery.Aborted != 1 || provider.abortCalls != 1 {
		t.Fatalf("recovery = %#v, abort calls = %d, err = %v", recovery, provider.abortCalls, err)
	}
	stored, err := repo.S3MultipartUploadByID(ctx, manageddata.MultipartUploadID(upload.ID))
	if err != nil || stored.Status != manageddata.S3MultipartStatusAborted {
		t.Fatalf("stored upload = %#v, err = %v", stored, err)
	}
}

func TestCoordinatorRecoversCreatingIntentByCompensatingAndRecreatingProviderUpload(t *testing.T) {
	ctx, repo, session := coordinatorFixture(t, []manageddata.File{{Path: "data.csv", Size: 1, SHA256: strings.Repeat("a", 64)}})
	provider := &fakeMultipartStore{orphanUploads: []storage.MultipartUpload{{UploadID: "orphan", Key: "prefixed/blobs"}}}
	service := newTestService(t, repo, provider)
	_, err := repo.CreateS3MultipartUpload(ctx, manageddata.CreateS3MultipartUploadInput{ID: "multipart-crash", UploadSessionID: session.ID, LogicalPath: "data.csv", SHA256: strings.Repeat("a", 64), SizeBytes: 1, IdempotencyIdentity: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := service.RecoverOrphaned(ctx, time.Now().Add(time.Hour), 10)
	if err != nil || recovered.Aborted != 0 || provider.abortCalls != 1 || provider.createCalls != 1 {
		t.Fatalf("recovery=%#v abort=%d create=%d err=%v", recovered, provider.abortCalls, provider.createCalls, err)
	}
	stored, err := repo.S3MultipartUploadByID(ctx, "multipart-crash")
	if err != nil || stored.Status != manageddata.S3MultipartStatusOpen || stored.ProviderUploadID == "" {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
}

func TestCoordinatorDoesNotAbortConcurrentSiblingWhenOwnershipIsAmbiguous(t *testing.T) {
	ctx, repo, session := coordinatorFixture(t, []manageddata.File{{Path: "data.csv", Size: 1, SHA256: strings.Repeat("a", 64)}})
	provider := &fakeMultipartStore{orphanUploads: []storage.MultipartUpload{{UploadID: "sibling-a", Key: "blobs/shared"}, {UploadID: "sibling-b", Key: "blobs/shared"}}}
	service := newTestService(t, repo, provider)
	_, err := repo.CreateS3MultipartUpload(ctx, manageddata.CreateS3MultipartUploadInput{ID: "multipart-siblings", UploadSessionID: session.ID, LogicalPath: "data.csv", SHA256: strings.Repeat("a", 64), SizeBytes: 1, IdempotencyIdentity: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecoverOrphaned(ctx, time.Now().Add(time.Hour), 10); err != nil {
		t.Fatalf("legacy ambiguous recovery did not converge: %v", err)
	}
	if provider.abortCalls != 2 || provider.createCalls != 1 {
		t.Fatalf("legacy recovery calls: abort=%d create=%d", provider.abortCalls, provider.createCalls)
	}
}

func TestCoordinatorPreservesPersistedSiblingAndAbortsOnlyUnknownOrphan(t *testing.T) {
	ctx, repo, session := coordinatorFixture(t, []manageddata.File{{Path: "data.csv", Size: 1, SHA256: strings.Repeat("a", 64)}})
	provider := &fakeMultipartStore{orphanUploads: []storage.MultipartUpload{{UploadID: "persisted", Key: "blobs/shared"}, {UploadID: "orphan", Key: "blobs/shared"}}}
	service := newTestService(t, repo, provider)
	if _, err := repo.CreateS3MultipartUpload(ctx, manageddata.CreateS3MultipartUploadInput{ID: "multipart-sibling", UploadSessionID: session.ID, LogicalPath: "data.csv", SHA256: strings.Repeat("a", 64), SizeBytes: 1, IdempotencyIdentity: strings.Repeat("c", 64)}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.InitializeS3MultipartUpload(ctx, manageddata.InitializeS3MultipartUploadInput{ID: "multipart-sibling", ObjectKey: "blobs/shared", ProviderUploadID: "persisted"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateS3MultipartUpload(ctx, manageddata.CreateS3MultipartUploadInput{ID: "multipart-unresolved", UploadSessionID: session.ID, LogicalPath: "data.csv", SHA256: strings.Repeat("a", 64), SizeBytes: 1, IdempotencyIdentity: strings.Repeat("d", 64)}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecoverOrphaned(ctx, time.Now().Add(time.Hour), 10); err != nil {
		t.Fatal(err)
	}
	if provider.abortCalls != 1 || provider.createCalls != 1 {
		t.Fatalf("owner-aware recovery calls: abort=%d create=%d", provider.abortCalls, provider.createCalls)
	}
}

func TestCoordinatorCreationInitFailureIsRetryableAndDoesNotMultiplyProviderUploads(t *testing.T) {
	ctx, repo, session := coordinatorFixture(t, []manageddata.File{{Path: "data.csv", Size: 1, SHA256: strings.Repeat("a", 64)}})
	provider := &fakeMultipartStore{}
	failing := &failingMultipartRepository{Repository: repo, initErr: errors.New("sqlite init failed")}
	first, err := New(failing, provider, Config{Backend: "s3", Clock: func() time.Time { return time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	request := CreateRequest{Project: "project-a", Connection: "warehouse", UploadSessionID: session.ID.String(), Path: "data.csv", IdempotencyKey: "create-init-retry"}
	if _, err := first.Create(ctx, request); err == nil {
		t.Fatal("Create unexpectedly succeeded")
	}
	if provider.createCalls != 1 || provider.abortCalls != 1 {
		t.Fatalf("provider calls after init failure: create=%d abort=%d", provider.createCalls, provider.abortCalls)
	}
	second := newTestService(t, repo, provider)
	if _, err := second.Create(ctx, request); err != nil {
		t.Fatalf("retry Create: %v", err)
	}
	if provider.createCalls != 2 {
		t.Fatalf("retry multiplied provider uploads: create=%d", provider.createCalls)
	}
}

func TestCoordinatorProviderCreateFailureLeavesRetryableIntent(t *testing.T) {
	ctx, repo, session := coordinatorFixture(t, []manageddata.File{{Path: "data.csv", Size: 1, SHA256: strings.Repeat("a", 64)}})
	provider := &fakeMultipartStore{createErr: errors.New("provider create unavailable")}
	service := newTestService(t, repo, provider)
	request := CreateRequest{Project: "project-a", Connection: "warehouse", UploadSessionID: session.ID.String(), Path: "data.csv", IdempotencyKey: "create-provider-retry"}
	if _, err := service.Create(ctx, request); err == nil || !errors.Is(err, control.ErrBackend) {
		t.Fatalf("provider create error=%v", err)
	}
	intentID := manageddata.MultipartUploadID("multipart_" + identityHash("create", session.ID.String(), request.IdempotencyKey))
	stored, err := repo.S3MultipartUploadByID(ctx, intentID)
	if err != nil || stored.Status != manageddata.S3MultipartStatusCreating {
		t.Fatalf("stored intent=%#v err=%v", stored, err)
	}
	provider.createErr = nil
	if _, err := service.Create(ctx, request); err != nil {
		t.Fatalf("retry create: %v", err)
	}
	if provider.createCalls != 2 {
		t.Fatalf("provider create calls=%d, want 2", provider.createCalls)
	}
}

func TestCoordinatorProviderCompletionFailureIsVisibleAndRetryable(t *testing.T) {
	ctx, repo, session := coordinatorFixture(t, []manageddata.File{{Path: "data.csv", Size: 1, SHA256: strings.Repeat("a", 64)}})
	provider := &fakeMultipartStore{}
	service := newTestService(t, repo, provider)
	upload, err := service.Create(ctx, CreateRequest{Project: "project-a", Connection: "warehouse", UploadSessionID: session.ID.String(), Path: "data.csv", IdempotencyKey: "complete-provider-retry"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SignPart(ctx, SignPartRequest{Project: "project-a", Connection: "warehouse", UploadSessionID: session.ID.String(), MultipartUploadID: upload.ID, PartNumber: 1, Size: 1}); err != nil {
		t.Fatal(err)
	}
	request := CompleteRequest{Project: "project-a", Connection: "warehouse", UploadSessionID: session.ID.String(), MultipartUploadID: upload.ID, IdempotencyKey: "complete-provider-retry", Parts: []CompletedPart{{PartNumber: 1, ETag: "etag"}}}
	provider.completeErr = errors.New("provider completion unavailable")
	if _, err := service.Complete(ctx, request); err == nil || !errors.Is(err, control.ErrBackend) {
		t.Fatalf("provider completion error=%v", err)
	}
	stored, err := repo.S3MultipartUploadByID(ctx, manageddata.MultipartUploadID(upload.ID))
	if err != nil || stored.Status != manageddata.S3MultipartStatusCompleting {
		t.Fatalf("stored completion intent=%#v err=%v", stored, err)
	}
	provider.completeErr = nil
	if result, err := service.Complete(ctx, request); err != nil || result.Status != StatusCompleted {
		t.Fatalf("retry completion=%#v err=%v", result, err)
	}
}

func TestCoordinatorCompletionProviderSuccessBeforeSQLFailureConvergesOnRecovery(t *testing.T) {
	ctx, repo, session := coordinatorFixture(t, []manageddata.File{{Path: "data.csv", Size: 1, SHA256: strings.Repeat("a", 64)}})
	provider := &fakeMultipartStore{}
	service := newTestService(t, repo, provider)
	upload, err := service.Create(ctx, CreateRequest{Project: "project-a", Connection: "warehouse", UploadSessionID: session.ID.String(), Path: "data.csv", IdempotencyKey: "create-complete-retry"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SignPart(ctx, SignPartRequest{Project: "project-a", Connection: "warehouse", UploadSessionID: session.ID.String(), MultipartUploadID: upload.ID, PartNumber: 1, Size: 1}); err != nil {
		t.Fatal(err)
	}
	failing := &failingMultipartRepository{Repository: repo, finishErr: errors.New("finish sqlite failed")}
	first, err := New(failing, provider, Config{Backend: "s3", Clock: func() time.Time { return time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	_, err = first.Complete(ctx, CompleteRequest{Project: "project-a", Connection: "warehouse", UploadSessionID: session.ID.String(), MultipartUploadID: upload.ID, IdempotencyKey: "finish-retry", Parts: []CompletedPart{{PartNumber: 1, ETag: "etag"}}})
	if err == nil || provider.completeCalls != 1 {
		t.Fatalf("completion error=%v calls=%d", err, provider.completeCalls)
	}
	stored, err := repo.S3MultipartUploadByID(ctx, manageddata.MultipartUploadID(upload.ID))
	if err != nil || stored.Status != manageddata.S3MultipartStatusCompleting {
		t.Fatalf("stored after SQL failure=%#v err=%v", stored, err)
	}
	recovered, err := service.RecoverOrphaned(ctx, time.Now().Add(time.Hour), 10)
	if err != nil || recovered.Completed != 1 {
		t.Fatalf("recovery=%#v err=%v", recovered, err)
	}
	repeated, err := service.RecoverOrphaned(ctx, time.Now().Add(time.Hour), 10)
	if err != nil || repeated.Completed != 0 || provider.completeCalls != 2 {
		t.Fatalf("repeated recovery=%#v calls=%d err=%v", repeated, provider.completeCalls, err)
	}
}

func TestCoordinatorRecoveryErrorsRemainVisibleAndRetryable(t *testing.T) {
	ctx, repo, session := coordinatorFixture(t, []manageddata.File{{Path: "data.csv", Size: 1, SHA256: strings.Repeat("a", 64)}})
	if _, err := repo.CreateS3MultipartUpload(ctx, manageddata.CreateS3MultipartUploadInput{ID: "multipart-recovery-retry", UploadSessionID: session.ID, LogicalPath: "data.csv", SHA256: strings.Repeat("a", 64), SizeBytes: 1, IdempotencyIdentity: strings.Repeat("b", 64)}); err != nil {
		t.Fatal(err)
	}
	provider := &fakeMultipartStore{listErr: errors.New("provider listing unavailable")}
	service := newTestService(t, repo, provider)
	if _, err := service.RecoverOrphaned(ctx, time.Now().Add(time.Hour), 10); err == nil {
		t.Fatal("recovery listing failure was swallowed")
	}
	provider.listErr = nil
	if _, err := service.RecoverOrphaned(ctx, time.Now().Add(time.Hour), 10); err != nil {
		t.Fatalf("retry recovery: %v", err)
	}
}

func TestCoordinatorClaimsDigestBeforeReconcilingProviderState(t *testing.T) {
	ctx, repo, session := coordinatorFixture(t, []manageddata.File{{Path: "data.csv", Size: 1, SHA256: strings.Repeat("a", 64)}})
	if _, err := repo.CreateS3MultipartUpload(ctx, manageddata.CreateS3MultipartUploadInput{
		ID: "multipart-claimed-elsewhere", UploadSessionID: session.ID, LogicalPath: "data.csv",
		SHA256: strings.Repeat("a", 64), SizeBytes: 1, IdempotencyIdentity: strings.Repeat("b", 64),
	}); err != nil {
		t.Fatal(err)
	}
	provider := &fakeMultipartStore{orphanUploads: []storage.MultipartUpload{{UploadID: "must-not-be-touched"}}}
	service, err := New(&deniedClaimRepository{Repository: repo}, provider, Config{Backend: "s3"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecoverOrphaned(ctx, time.Now().Add(time.Hour), 10); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("recovery error = %v, want digest claim conflict", err)
	}
	if provider.listCalls != 0 || provider.abortCalls != 0 || provider.createCalls != 0 {
		t.Fatalf("provider touched before claim: list=%d abort=%d create=%d", provider.listCalls, provider.abortCalls, provider.createCalls)
	}
}

type deniedClaimRepository struct {
	*sqlite.Repository
}

func (*deniedClaimRepository) ClaimS3MultipartDigest(context.Context, string, string, time.Time) (int64, bool, error) {
	return 0, false, nil
}

func (*deniedClaimRepository) RenewS3MultipartDigest(context.Context, string, string, int64, time.Time) (bool, error) {
	return false, nil
}

func (*deniedClaimRepository) ReleaseS3MultipartDigest(context.Context, string, string, int64) error {
	return nil
}

type failingMultipartRepository struct {
	Repository
	initErr   error
	finishErr error
}

func (r *failingMultipartRepository) InitializeS3MultipartUpload(ctx context.Context, input manageddata.InitializeS3MultipartUploadInput) (manageddata.S3MultipartUpload, error) {
	if r.initErr != nil {
		return manageddata.S3MultipartUpload{}, r.initErr
	}
	return r.Repository.InitializeS3MultipartUpload(ctx, input)
}

func (r *failingMultipartRepository) FinishS3MultipartCompletion(ctx context.Context, id manageddata.MultipartUploadID) (manageddata.S3MultipartUpload, error) {
	if r.finishErr != nil {
		err := r.finishErr
		r.finishErr = nil
		return manageddata.S3MultipartUpload{}, err
	}
	return r.Repository.FinishS3MultipartCompletion(ctx, id)
}

type fakeMultipartStore struct {
	createCalls    int
	listCalls      int
	completeCalls  int
	abortCalls     int
	createErr      error
	completeErr    error
	listErr        error
	completedParts []storage.CompletedMultipartPart
	orphanUploads  []storage.MultipartUpload
}

func (f *fakeMultipartStore) ListMultipartUploads(_ context.Context, expected storage.Blob) ([]storage.MultipartUpload, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	result := append([]storage.MultipartUpload(nil), f.orphanUploads...)
	for i := range result {
		result[i].SHA256, result[i].Size = expected.SHA256, expected.Size
	}
	return result, nil
}

func (f *fakeMultipartStore) CreateMultipart(_ context.Context, blob storage.Blob) (storage.MultipartUpload, error) {
	f.createCalls++
	if f.createErr != nil {
		return storage.MultipartUpload{}, f.createErr
	}
	return storage.MultipartUpload{UploadID: "provider-1", SHA256: blob.SHA256, Size: blob.Size, Key: "blobs/" + blob.SHA256}, nil
}

func (f *fakeMultipartStore) SignPart(_ context.Context, _ storage.MultipartUpload, part storage.MultipartPartRequest) (storage.SignedMultipartPart, error) {
	return storage.SignedMultipartPart{Number: part.Number, URL: "https://s3.example/upload?signature=transient", Headers: http.Header{"X-Checksum": []string{"value"}}}, nil
}

func (f *fakeMultipartStore) CompleteMultipart(_ context.Context, upload storage.MultipartUpload, parts []storage.CompletedMultipartPart) (storage.Blob, error) {
	f.completeCalls++
	f.completedParts = append([]storage.CompletedMultipartPart(nil), parts...)
	if f.completeErr != nil {
		return storage.Blob{}, f.completeErr
	}
	return storage.Blob{SHA256: upload.SHA256, Size: upload.Size, URI: "s3://bucket/" + upload.Key}, nil
}

func (f *fakeMultipartStore) AbortMultipart(context.Context, storage.MultipartUpload) error {
	f.abortCalls++
	return nil
}

func coordinatorFixture(t *testing.T, files []manageddata.File) (context.Context, *sqlite.Repository, manageddata.UploadSession) {
	t.Helper()
	ctx := context.Background()
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "leapview.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
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
	repo := sqlite.NewRepository(database)
	collection, err := repo.CreateCollection(ctx, manageddata.CreateCollectionInput{ID: "collection-a", ProjectID: "project-a", ConnectionID: "warehouse", Name: "Warehouse"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := repo.CreateUploadSession(ctx, manageddata.CreateUploadSessionInput{
		ID: "upload-a", CollectionID: collection.ID, Manifest: manageddata.Manifest{Files: files}, StorageBackend: "s3",
		StagingPrefix: "uploads/upload-a", ExpiresAt: time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, repo, session
}

func newTestService(t *testing.T, repo *sqlite.Repository, provider MultipartStore) *Service {
	t.Helper()
	now, _ := time.Parse(time.RFC3339, nowText)
	service, err := New(repo, provider, Config{Backend: "s3", Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return service
}
