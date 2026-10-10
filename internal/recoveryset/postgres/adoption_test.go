package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/stretchr/testify/require"
)

func TestAdoptPublishedRecoveryFrontierAtomicRetry(t *testing.T) {
	set, attempt, result := publishedAdoptionFixture(t)
	db := recoverySetDB(t)
	repository := New(db)
	tx, err := db.Begin(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	adopted, err := repository.AdoptPublishedTx(t.Context(), tx, set, attempt, result, "replacement-operator")
	require.NoError(t, err)
	require.Equal(t, set, adopted)
	_, err = repository.ReadExact(t.Context(), set.ID)
	require.ErrorIs(t, err, recoveryset.ErrNotFound, "publication must remain invisible until the caller commits its fencing/service checks")
	require.NoError(t, tx.Rollback(t.Context()), "a lost fence after adoption must roll back every imported row")
	_, err = repository.ReadExact(t.Context(), set.ID)
	require.ErrorIs(t, err, recoveryset.ErrNotFound)
	_, err = repository.ValidationAttempt(t.Context(), attempt.AttemptID)
	require.ErrorIs(t, err, recoveryset.ErrNotFound)
	for range 2 {
		tx, err := db.Begin(t.Context())
		require.NoError(t, err)
		defer tx.Rollback(context.Background())
		adopted, err := repository.AdoptPublishedTx(t.Context(), tx, set, attempt, result, "replacement-operator")
		require.NoError(t, err)
		require.Equal(t, set, adopted)
		require.NoError(t, tx.Commit(t.Context()))
	}
	storedAttempt, err := repository.ValidationAttempt(t.Context(), attempt.AttemptID)
	require.NoError(t, err)
	require.Equal(t, attempt, storedAttempt)
	storedResult, err := repository.ValidationResult(t.Context(), attempt.AttemptID)
	require.NoError(t, err)
	require.Equal(t, result, storedResult)
}

func TestAdoptPublishedRecoveryFrontierRejectsSubstitutionAndPartialConflict(t *testing.T) {
	set, attempt, result := publishedAdoptionFixture(t)
	for _, scenario := range []string{"partial", "partial-running", "partial-evidence", "foreign-frontier", "foreign-target", "foreign-validation", "foreign-fence", "tampered-result", "failed-validation", "terminal-conflict"} {
		t.Run(scenario, func(t *testing.T) {
			db := recoverySetDB(t)
			repository := New(db)
			prepared := set
			prepared.Status, prepared.PublishedValidationAttemptID = recoveryset.StatusPrepared, ""
			if scenario == "foreign-frontier" {
				prepared.AuditIdentity = "other-frontier-authority"
			}
			_, err := repository.Create(t.Context(), prepared)
			require.NoError(t, err)
			incomingSet, incomingAttempt, incomingResult := set, attempt, result
			switch scenario {
			case "foreign-target":
				incomingSet.Delivery.TargetID = "different-target"
			case "foreign-validation":
				incomingAttempt.SetID = "018f3f83-7b2f-7b37-9f9e-000000000999"
			case "foreign-fence":
				incomingAttempt.FenceEpoch++
			case "tampered-result":
				incomingResult.Evidence = []byte(`{}`)
			case "failed-validation":
				incomingAttempt.Status, incomingAttempt.Error = recoveryset.ValidationFailed, "failed"
			case "partial-running", "partial-evidence", "terminal-conflict":
				running := attempt
				running.Status, running.ResultDigest, running.CompletedAt = recoveryset.ValidationRunning, "", time.Time{}
				_, err := repository.BeginValidation(t.Context(), running)
				require.NoError(t, err)
				if scenario == "partial-evidence" {
					require.NoError(t, repository.RecordValidationResult(t.Context(), result))
				}
				if scenario == "terminal-conflict" {
					failed := attempt
					failed.Status, failed.ResultDigest, failed.Error = recoveryset.ValidationFailed, "", "failed"
					require.NoError(t, repository.CompleteValidation(t.Context(), failed))
				}
			}
			tx, err := db.Begin(t.Context())
			require.NoError(t, err)
			defer tx.Rollback(context.Background())
			_, err = repository.AdoptPublishedTx(t.Context(), tx, incomingSet, incomingAttempt, incomingResult, "replacement-operator")
			if scenario == "partial" || scenario == "partial-running" || scenario == "partial-evidence" {
				require.NoError(t, err, "a prepared restore must resume through exact validation and publication")
				require.NoError(t, tx.Commit(t.Context()))
			} else {
				require.Error(t, err)
				require.NoError(t, tx.Rollback(t.Context()))
				stored, err := repository.ReadExact(t.Context(), set.ID)
				require.NoError(t, err)
				require.Equal(t, recoveryset.StatusPrepared, stored.Status)
			}
		})
	}
}

func publishedAdoptionFixture(t *testing.T) (recoveryset.RecoverySet, recoveryset.ValidationAttempt, recoveryset.ValidationResult) {
	t.Helper()
	r := New(recoverySetDB(t))
	set, err := r.Create(t.Context(), recoverySetFixture(t))
	require.NoError(t, err)
	started := time.Now().UTC().Truncate(time.Microsecond)
	attempt := recoveryset.ValidationAttempt{AttemptID: "018f3f83-7b2f-7b37-9f9e-000000000700", SetID: set.ID, OwnerID: "independent-validator", FenceEpoch: set.FenceEpoch, AuditIdentity: "recovery-drill", Status: recoveryset.ValidationRunning, StartedAt: started}
	_, err = r.BeginValidation(t.Context(), attempt)
	require.NoError(t, err)
	envelope, err := recoveryset.NewValidationEvidenceEnvelope(set, attempt.AttemptID)
	require.NoError(t, err)
	result, err := recoveryset.NewValidationResult(envelope, started.Add(time.Second))
	require.NoError(t, err)
	require.NoError(t, r.RecordValidationResult(t.Context(), result))
	attempt.Status, attempt.ResultDigest, attempt.CompletedAt = recoveryset.ValidationPassed, result.ResultDigest, started.Add(2*time.Second)
	require.NoError(t, r.CompleteValidation(t.Context(), attempt))
	set, err = r.Publish(t.Context(), set.ID, "independent-publisher", set.FenceEpoch, attempt.AttemptID)
	require.NoError(t, err)
	storedAttempt, err := r.ValidationAttempt(t.Context(), attempt.AttemptID)
	require.NoError(t, err)
	storedResult, err := r.ValidationResult(t.Context(), attempt.AttemptID)
	require.NoError(t, err)
	return set, storedAttempt, storedResult
}
