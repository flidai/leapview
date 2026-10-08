package s3multipart

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/control"
	managedpostgres "github.com/flidai/leapview/internal/manageddata/postgres"
	"github.com/flidai/leapview/internal/manageddata/storage"
)

func TestCoordinatorRecoveryContinuesAfterIndependentProviderFailure(t *testing.T) {
	ctx, repo, blocked, healthy := recoveryProgressFixture(t)
	provider := &fakeMultipartStore{listErr: errors.New("controlled first-upload provider failure")}
	service := newTestService(t, repo, provider)
	result, err := service.RecoverOrphaned(ctx, time.Now().UTC().Add(time.Hour), 10)
	if !errors.Is(err, control.ErrBackend) {
		t.Fatalf("first recovery error = %v, want observable provider error", err)
	}
	if result.Aborted != 1 || result.Completed != 0 || result.Failed != 0 {
		t.Fatalf("independent healthy recovery result = %#v, want one abort and no fabricated terminal failures", result)
	}
	assertRecoveryStatus(t, ctx, repo, blocked, manageddata.S3MultipartStatusCreating)
	assertRecoveryStatus(t, ctx, repo, healthy, manageddata.S3MultipartStatusAborted)
	if provider.listCalls != 1 || provider.abortCalls != 1 {
		t.Fatalf("provider progress = list:%d abort:%d", provider.listCalls, provider.abortCalls)
	}
	// A transient failure remains visible and retryable rather than converting
	// the blocked intent to terminal failure or replaying the healthy abort.
	provider.listErr = nil
	result, err = service.RecoverOrphaned(ctx, time.Now().UTC().Add(time.Hour), 10)
	if err != nil {
		t.Fatalf("retry blocked recovery = %#v, %v", result, err)
	}
	assertRecoveryStatus(t, ctx, repo, blocked, manageddata.S3MultipartStatusOpen)
	if provider.abortCalls != 1 {
		t.Fatalf("terminal healthy abort replayed: calls=%d", provider.abortCalls)
	}
	t.Log("failed recovery remained retryable while an independent native abort completed")
}

func TestCoordinatorRecoveryStopsOnCallerCancellation(t *testing.T) {
	parent, repo, blocked, healthy := recoveryProgressFixture(t)
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	provider := &cancelingRecoveryStore{fakeMultipartStore: &fakeMultipartStore{}, cancel: cancel}
	result, err := newTestService(t, repo, provider).RecoverOrphaned(ctx, time.Now().UTC().Add(time.Hour), 10)
	if !errors.Is(err, context.Canceled) || result != (RecoveryResult{}) {
		t.Fatalf("canceled recovery = %#v, %v", result, err)
	}
	if provider.abortCalls != 0 || provider.createCalls != 0 {
		t.Fatalf("provider work after cancellation: abort=%d create=%d", provider.abortCalls, provider.createCalls)
	}
	assertRecoveryStatus(t, parent, repo, blocked, manageddata.S3MultipartStatusCreating)
	assertRecoveryStatus(t, parent, repo, healthy, manageddata.S3MultipartStatusAborting)
}

func recoveryProgressFixture(t *testing.T) (context.Context, *managedpostgres.Repository, manageddata.MultipartUploadID, manageddata.MultipartUploadID) {
	t.Helper()
	_, repo, session := coordinatorFixture(t, []manageddata.File{
		{Path: "blocked.csv", Size: 1, SHA256: strings.Repeat("a", 64)},
		{Path: "healthy.csv", Size: 1, SHA256: strings.Repeat("b", 64)},
	})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	for _, input := range []manageddata.CreateS3MultipartUploadInput{
		{ID: "multipart-blocked", UploadSessionID: session.ID, LogicalPath: "blocked.csv", SHA256: strings.Repeat("a", 64), SizeBytes: 1, IdempotencyIdentity: strings.Repeat("c", 64)},
		{ID: "multipart-healthy", UploadSessionID: session.ID, LogicalPath: "healthy.csv", SHA256: strings.Repeat("b", 64), SizeBytes: 1, IdempotencyIdentity: strings.Repeat("d", 64)},
	} {
		if _, err := repo.CreateS3MultipartUpload(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	healthy := manageddata.MultipartUploadID("multipart-healthy")
	if _, err := repo.InitializeS3MultipartUpload(ctx, manageddata.InitializeS3MultipartUploadInput{ID: healthy, ObjectKey: "blobs/healthy", ProviderUploadID: "provider-healthy"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.BeginS3MultipartAbort(ctx, manageddata.BeginS3MultipartAbortInput{ID: healthy, IdempotencyIdentity: strings.Repeat("e", 64)}); err != nil {
		t.Fatal(err)
	}
	rows, err := repo.ListRecoverableS3MultipartUploads(ctx, time.Now().UTC().Add(time.Hour), 10)
	if err != nil || len(rows) != 2 || rows[0].ID != "multipart-blocked" || rows[1].ID != healthy {
		t.Fatalf("native ordered recovery fixture = %#v, %v", rows, err)
	}
	return ctx, repo, rows[0].ID, healthy
}

func assertRecoveryStatus(t *testing.T, ctx context.Context, repo *managedpostgres.Repository, id manageddata.MultipartUploadID, want manageddata.S3MultipartStatus) {
	t.Helper()
	row, err := repo.S3MultipartUploadByID(ctx, id)
	if err != nil || row.Status != want {
		t.Fatalf("native recovery status %s = %q, %v; want %q", id, row.Status, err, want)
	}
}

type cancelingRecoveryStore struct {
	*fakeMultipartStore
	cancel context.CancelFunc
}

func (s *cancelingRecoveryStore) ListMultipartUploads(context.Context, storage.Blob) ([]storage.MultipartUpload, error) {
	s.cancel()
	return nil, context.Canceled
}
