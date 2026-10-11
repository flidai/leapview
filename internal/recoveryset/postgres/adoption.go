package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/recoveryset"
)

// AdoptPublishedTx reconciles an independently verified published frontier into
// restored control storage. The caller owns the transaction and must validate
// the restored service, retained evidence and original fence before committing.
// This capability neither promotes PostgreSQL nor authorizes application traffic.
func (r *Repository) AdoptPublishedTx(ctx context.Context, tx Tx, set recoveryset.RecoverySet, attempt recoveryset.ValidationAttempt, result recoveryset.ValidationResult, publisher string) (recoveryset.RecoverySet, error) {
	if tx == nil || publisher == "" || strings.TrimSpace(publisher) != publisher || len(publisher) > 255 || strings.ContainsAny(publisher, "\r\n\x00") {
		return recoveryset.RecoverySet{}, recoveryset.ErrInvalid
	}
	normalized, err := set.Normalize()
	if err != nil || normalized.SchemaVersion != recoveryset.SchemaVersion || normalized.Status != recoveryset.StatusPublished || normalized.PublishedValidationAttemptID != attempt.AttemptID {
		return recoveryset.RecoverySet{}, recoveryset.ErrInvalid
	}
	digest, err := normalized.Digest()
	if err != nil || normalized.FrontierDigest != digest {
		return recoveryset.RecoverySet{}, recoveryset.ErrInvalid
	}
	attempt.StartedAt = attempt.StartedAt.UTC().Truncate(time.Microsecond)
	attempt.CompletedAt = attempt.CompletedAt.UTC().Truncate(time.Microsecond)
	result, err = result.Normalize()
	if err != nil || attempt.Validate() != nil || attempt.Status != recoveryset.ValidationPassed || attempt.SetID != normalized.ID || attempt.FenceEpoch != normalized.FenceEpoch || attempt.ResultDigest != result.ResultDigest || attempt.AttemptID != result.AttemptID || attempt.CompletedAt.Before(attempt.StartedAt) || result.RecordedAt.Before(attempt.StartedAt) || result.RecordedAt.After(attempt.CompletedAt) {
		return recoveryset.RecoverySet{}, recoveryset.ErrInvalid
	}
	envelope, err := recoveryset.ParseValidationEvidenceEnvelope(result.Evidence)
	if err != nil || envelope.ValidateFor(normalized, attempt.AttemptID) != nil {
		return recoveryset.RecoverySet{}, recoveryset.ErrInvalid
	}
	prepared := normalized
	prepared.Status, prepared.PublishedValidationAttemptID = recoveryset.StatusPrepared, ""
	local := New(tx)
	stored, err := local.CreateTx(ctx, tx, prepared)
	if err != nil {
		return recoveryset.RecoverySet{}, err
	}
	if stored.Status != recoveryset.StatusPrepared && (stored.Status != recoveryset.StatusPublished || stored.PublishedValidationAttemptID != attempt.AttemptID) {
		return recoveryset.RecoverySet{}, fmt.Errorf("adoption publication differs: %w", recoveryset.ErrConflict)
	}
	storedAttempt, err := local.ValidationAttempt(ctx, attempt.AttemptID)
	if errors.Is(err, recoveryset.ErrNotFound) {
		running := attempt
		running.Status, running.ResultDigest, running.CompletedAt = recoveryset.ValidationRunning, "", time.Time{}
		storedAttempt, err = local.BeginValidation(ctx, running)
	}
	if err != nil {
		return recoveryset.RecoverySet{}, err
	}
	if storedAttempt.Status == recoveryset.ValidationRunning {
		expected := attempt
		expected.Status, expected.ResultDigest, expected.CompletedAt = recoveryset.ValidationRunning, "", time.Time{}
		if !reflect.DeepEqual(storedAttempt, expected) {
			return recoveryset.RecoverySet{}, fmt.Errorf("adoption running validation differs: %w", recoveryset.ErrConflict)
		}
	} else if !reflect.DeepEqual(storedAttempt, attempt) {
		return recoveryset.RecoverySet{}, fmt.Errorf("adoption completed validation differs: %w", recoveryset.ErrConflict)
	}
	storedResult, err := local.ValidationResult(ctx, attempt.AttemptID)
	if err == nil && !reflect.DeepEqual(storedResult, result) {
		return recoveryset.RecoverySet{}, fmt.Errorf("adoption recorded evidence differs: %w", recoveryset.ErrConflict)
	}
	if err != nil && !errors.Is(err, recoveryset.ErrNotFound) {
		return recoveryset.RecoverySet{}, err
	}
	if err := local.RecordValidationResult(ctx, result); err != nil {
		return recoveryset.RecoverySet{}, err
	}
	if err := local.CompleteValidation(ctx, attempt); err != nil {
		return recoveryset.RecoverySet{}, err
	}
	adopted, err := local.Publish(ctx, normalized.ID, publisher, normalized.FenceEpoch, attempt.AttemptID)
	if err != nil {
		return recoveryset.RecoverySet{}, err
	}
	if !adopted.IdentityEqual(normalized) || adopted.Status != normalized.Status || adopted.PublishedValidationAttemptID != normalized.PublishedValidationAttemptID {
		return recoveryset.RecoverySet{}, fmt.Errorf("adoption canonical frontier differs: %w", recoveryset.ErrConflict)
	}
	return adopted, nil
}
