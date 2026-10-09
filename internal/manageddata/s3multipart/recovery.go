package s3multipart

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/control"
	"github.com/flidai/leapview/internal/manageddata/storage"
	"github.com/google/uuid"
)

func (s *Service) RecoverOrphaned(ctx context.Context, before time.Time, limit int64) (RecoveryResult, error) {
	if ctx == nil || before.IsZero() {
		return RecoveryResult{}, fmt.Errorf("%w: context and recovery cutoff are required", control.ErrInvalid)
	}
	uploads, err := s.repo.ListRecoverableS3MultipartUploads(ctx, before, limit)
	if err != nil {
		return RecoveryResult{}, repositoryError(err)
	}
	result := RecoveryResult{}
	var failures []error
	for _, upload := range uploads {
		if err := ctx.Err(); err != nil {
			return result, errors.Join(append(failures, err)...)
		}
		if err := s.recoverUpload(ctx, upload, &result); err != nil {
			failures = append(failures, err)
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return result, errors.Join(failures...)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		failures = append(failures, err)
	}
	return result, errors.Join(failures...)
}

// Each upload owns its claims and transaction boundaries. One retryable row
// failure must not prevent independent rows from converging.
func (s *Service) recoverUpload(ctx context.Context, upload manageddata.S3MultipartUpload, result *RecoveryResult) error {
	if upload.Status == manageddata.S3MultipartStatusCompleting {
		completionErr := func() error {
			claimCtx, release, claimErr := acquireMultipartClaim(ctx, s.repo, upload.SHA256, "recover-complete:"+uuid.NewString())
			if claimErr != nil {
				return claimErr
			}
			defer release()
			parts, partsErr := s.repo.ListS3MultipartParts(claimCtx, upload.ID)
			if partsErr != nil {
				return repositoryError(partsErr)
			}
			providerParts := make([]storage.MultipartPartRequest, len(parts))
			for i, part := range parts {
				providerParts[i] = storage.MultipartPartRequest{Number: part.PartNumber, Size: part.SizeBytes, SHA256: part.SHA256}
			}
			recovery, ok := s.store.(MultipartRecoveryStore)
			if !ok {
				return fmt.Errorf("%w: multipart completion recovery is unavailable", control.ErrBackend)
			}
			blob, completeErr := recovery.RecoverMultipart(claimCtx, providerUpload(upload), providerParts)
			if completeErr != nil {
				return storageError(completeErr)
			}
			if blob.SHA256 != upload.SHA256 || blob.Size != upload.SizeBytes {
				_, _ = s.repo.FailS3MultipartUpload(claimCtx, upload.ID, integrityTerminalError)
				result.Failed++
				return nil
			}
			if _, finishErr := s.repo.FinishS3MultipartCompletion(claimCtx, upload.ID); finishErr != nil {
				current, lookupErr := s.repo.S3MultipartUploadByID(claimCtx, upload.ID)
				if lookupErr == nil && current.Status == manageddata.S3MultipartStatusCompleted {
					result.Completed++
					return nil
				}
				return repositoryError(finishErr)
			}
			result.Completed++
			return nil
		}()
		if completionErr != nil {
			return completionErr
		}
		return nil
	}
	if upload.Status == manageddata.S3MultipartStatusCreating && upload.ProviderUploadID == "" {
		if err := s.reconcileCreating(ctx, upload); err != nil {
			return err
		}
		return nil
	}
	if upload.Status != manageddata.S3MultipartStatusAborting {
		claim, claimErr := s.repo.BeginS3MultipartAbort(ctx, manageddata.BeginS3MultipartAbortInput{
			ID: upload.ID, IdempotencyIdentity: identityHash("recovery", upload.ID.String(), upload.ID.String()),
		})
		if claimErr != nil {
			return repositoryError(claimErr)
		}
		upload = claim.Upload
	}
	if err := s.store.AbortMultipart(ctx, providerUpload(upload)); err != nil {
		return storageError(err)
	}
	if _, err := s.repo.FinishS3MultipartAbort(ctx, upload.ID); err != nil {
		current, lookupErr := s.repo.S3MultipartUploadByID(ctx, upload.ID)
		if lookupErr == nil && current.Status == manageddata.S3MultipartStatusAborted {
			result.Aborted++
			return nil
		}
		return repositoryError(err)
	}
	result.Aborted++
	return nil
}
