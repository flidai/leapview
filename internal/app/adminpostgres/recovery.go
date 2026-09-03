package adminpostgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	admincli "github.com/flidai/leapview/internal/admin/cli"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/recoveryset"
	recoverysetpostgres "github.com/flidai/leapview/internal/recoveryset/postgres"
)

// ErrRecoveryValidationFailed is returned after a validation attempt has been
// durably completed as failed.  The detail intentionally does not contain
// provider observations or parser internals; those remain operator-owned
// evidence and are stored only when the typed envelope validates.
var ErrRecoveryValidationFailed = errors.New("recovery validation failed")

// PrepareRecovery creates one immutable recovery frontier and its physical
// retention hold in the same control-plane transaction.  The provider points
// have already been selected by the operator; this method does not perform
// backup, PITR, or object-store I/O.
func (o Operations) PrepareRecovery(ctx context.Context, request admincli.RecoveryPrepareRequest) (admincli.RecoveryPrepareResult, error) {
	ctx = contextOrBackground(ctx)
	if request.ExpiresAt.IsZero() {
		return admincli.RecoveryPrepareResult{}, fmt.Errorf("recovery retention root expiry is required")
	}
	set, err := request.Set.Normalize()
	if err != nil {
		return admincli.RecoveryPrepareResult{}, err
	}
	if set.Status != recoveryset.StatusPrepared || set.PublishedValidationAttemptID != "" {
		return admincli.RecoveryPrepareResult{}, recoveryset.ErrInvalid
	}
	rootID := strings.TrimSpace(request.RootID)
	if rootID == "" {
		rootID = set.ID
	}
	pool, closePool, err := o.openRecoveryMaintenance(ctx)
	if err != nil {
		return admincli.RecoveryPrepareResult{}, err
	}
	defer closePool()
	tx, err := pool.Begin(contextOrBackground(ctx))
	if err != nil {
		return admincli.RecoveryPrepareResult{}, fmt.Errorf("begin recovery preparation transaction: %w", err)
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback(contextOrBackground(ctx))
		}
	}()

	recoveryRepo := recoverysetpostgres.New(pool)
	persistedSet, err := recoveryRepo.CreateTx(ctx, tx, set)
	if err != nil {
		return admincli.RecoveryPrepareResult{}, fmt.Errorf("create recovery set: %w", err)
	}
	rootEvidence, err := json.Marshal(struct {
		RecoverySetID  string `json:"recovery_set_id"`
		FrontierDigest string `json:"frontier_digest"`
	}{RecoverySetID: persistedSet.ID, FrontierDigest: persistedSet.FrontierDigest})
	if err != nil {
		return admincli.RecoveryPrepareResult{}, fmt.Errorf("encode recovery retention evidence: %w", err)
	}
	deliveryRepo := deploymentpostgres.New(pool)
	root, err := deliveryRepo.CreateRetentionRootTx(ctx, tx, deploymentpostgres.DeliveryRetentionRoot{
		RootID: rootID, TargetID: persistedSet.Delivery.TargetID,
		GenerationID: persistedSet.Delivery.GenerationID, SnapshotSealID: persistedSet.Serving.SealID,
		RootKind: "recovery", State: "live", ExpiresAt: request.ExpiresAt,
		Evidence: rootEvidence,
	})
	if err != nil {
		return admincli.RecoveryPrepareResult{}, fmt.Errorf("create recovery retention root: %w", err)
	}
	if err := tx.Commit(contextOrBackground(ctx)); err != nil {
		return admincli.RecoveryPrepareResult{}, fmt.Errorf("commit recovery preparation: %w", err)
	}
	rollback = false
	return admincli.RecoveryPrepareResult{Set: persistedSet, RootID: root.RootID, ExpiresAt: root.ExpiresAt}, nil
}

// ValidateRecovery begins one exact-fenced attempt, records the operator's
// strict typed evidence, and completes the attempt. No PostgreSQL transaction
// remains open while provider/PITR/object probes run: the probes are supplied
// as bounded evidence bytes by the caller.
func (o Operations) ValidateRecovery(ctx context.Context, request admincli.RecoveryValidateRequest) (admincli.RecoveryValidateResult, error) {
	ctx = contextOrBackground(ctx)
	validator := request.Validator
	if validator == "" || validator != strings.TrimSpace(validator) || len(validator) > 255 {
		return admincli.RecoveryValidateResult{}, fmt.Errorf("recovery validator identity is required and must be canonical")
	}
	pool, closePool, err := o.openRecoveryMaintenance(ctx)
	if err != nil {
		return admincli.RecoveryValidateResult{}, err
	}
	defer closePool()
	recoveryRepo := recoverysetpostgres.New(pool)
	set, err := recoveryRepo.ReadExact(ctx, strings.TrimSpace(request.SetID))
	if err != nil {
		return admincli.RecoveryValidateResult{}, fmt.Errorf("read exact recovery set: %w", err)
	}
	attemptID := strings.TrimSpace(request.AttemptID)
	now := o.Dependencies.withDefaults().Now().UTC().Truncate(time.Microsecond)
	if now.IsZero() {
		return admincli.RecoveryValidateResult{}, errors.New("recovery validation clock returned zero")
	}
	// Read an existing attempt first so retries reuse its repository-owned
	// StartedAt rather than manufacturing new audit metadata after process loss.
	attempt, readErr := recoveryRepo.ValidationAttempt(ctx, attemptID)
	if errors.Is(readErr, recoveryset.ErrNotFound) {
		attempt, err = recoveryRepo.BeginValidation(ctx, recoveryset.ValidationAttempt{
			AttemptID: attemptID, SetID: set.ID, OwnerID: validator,
			FenceEpoch: set.FenceEpoch, AuditIdentity: set.AuditIdentity, StartedAt: now,
		})
		if err != nil {
			return admincli.RecoveryValidateResult{}, fmt.Errorf("begin recovery validation: %w", err)
		}
	} else if readErr != nil {
		return admincli.RecoveryValidateResult{}, fmt.Errorf("read recovery validation attempt: %w", readErr)
	} else if attempt.SetID != set.ID || attempt.OwnerID != validator || attempt.FenceEpoch != set.FenceEpoch || attempt.AuditIdentity != set.AuditIdentity {
		return admincli.RecoveryValidateResult{}, fmt.Errorf("%w: validation attempt identity is fenced", recoveryset.ErrFenced)
	}
	// A retry after process loss must resume an existing running attempt. A
	// terminal attempt is immutable: passed attempts replay only when their
	// persisted typed result exactly matches the supplied evidence; failed
	// attempts replay as the same stable failure without mutation.
	if attempt.Status == recoveryset.ValidationFailed {
		return admincli.RecoveryValidateResult{Attempt: attempt}, ErrRecoveryValidationFailed
	}

	envelope, parseErr := recoveryset.ParseValidationEvidenceEnvelope(request.Evidence)
	if parseErr == nil {
		parseErr = envelope.ValidateFor(set, attempt.AttemptID)
	}
	if parseErr != nil {
		if attempt.Status == recoveryset.ValidationPassed {
			return admincli.RecoveryValidateResult{}, fmt.Errorf("%w: persisted validation evidence does not match", recoveryset.ErrConflict)
		}
		// A crash may have left a valid immutable result while the attempt is
		// still running. Do not downgrade that evidence to a failed terminal
		// state merely because this retry supplied malformed bytes; a later retry
		// can still complete the exact attempt with the persisted result.
		if _, resultErr := recoveryRepo.ValidationResult(ctx, attempt.AttemptID); resultErr == nil {
			return admincli.RecoveryValidateResult{Attempt: attempt}, fmt.Errorf("%w: typed validation evidence is invalid", ErrRecoveryValidationFailed)
		} else if !errors.Is(resultErr, recoveryset.ErrNotFound) {
			return admincli.RecoveryValidateResult{}, fmt.Errorf("read recovery validation result: %w", resultErr)
		}
		failed := recoveryset.ValidationAttempt{
			AttemptID: attempt.AttemptID, SetID: attempt.SetID, OwnerID: attempt.OwnerID,
			FenceEpoch: attempt.FenceEpoch, AuditIdentity: attempt.AuditIdentity,
			Status: recoveryset.ValidationFailed, StartedAt: attempt.StartedAt,
			CompletedAt: now, Error: "typed validation evidence is invalid",
		}
		completeErr := recoveryRepo.CompleteValidation(ctx, failed)
		if completeErr != nil {
			return admincli.RecoveryValidateResult{}, fmt.Errorf("%w: unable to record failed validation", ErrRecoveryValidationFailed)
		}
		failed, readErr := recoveryRepo.ValidationAttempt(ctx, attempt.AttemptID)
		if readErr != nil {
			return admincli.RecoveryValidateResult{}, fmt.Errorf("%w: failed validation was not readable", ErrRecoveryValidationFailed)
		}
		return admincli.RecoveryValidateResult{Attempt: failed}, fmt.Errorf("%w: typed validation evidence is invalid", ErrRecoveryValidationFailed)
	}

	// For a passed replay, compare canonical digest/evidence with the stored
	// result and preserve its original RecordedAt instead of manufacturing a
	// new timestamp that would spuriously conflict.
	if attempt.Status == recoveryset.ValidationPassed {
		stored, readErr := recoveryRepo.ValidationResult(ctx, attempt.AttemptID)
		if readErr != nil {
			return admincli.RecoveryValidateResult{}, fmt.Errorf("read persisted recovery validation result: %w", readErr)
		}
		expected, buildErr := recoveryset.NewValidationResult(envelope, stored.RecordedAt)
		if buildErr != nil || expected.ResultDigest != stored.ResultDigest || !bytes.Equal(expected.Evidence, stored.Evidence) {
			return admincli.RecoveryValidateResult{}, fmt.Errorf("%w: persisted validation evidence does not match", recoveryset.ErrConflict)
		}
		return admincli.RecoveryValidateResult{Attempt: attempt, Result: &stored}, nil
	}

	// If a process recorded evidence but crashed before terminal completion,
	// reuse that immutable result and its repository-owned RecordedAt.
	var result recoveryset.ValidationResult
	stored, readErr := recoveryRepo.ValidationResult(ctx, attempt.AttemptID)
	if readErr == nil {
		expected, buildErr := recoveryset.NewValidationResult(envelope, stored.RecordedAt)
		if buildErr != nil || expected.ResultDigest != stored.ResultDigest || !bytes.Equal(expected.Evidence, stored.Evidence) {
			return admincli.RecoveryValidateResult{}, fmt.Errorf("record recovery validation result: %w", recoveryset.ErrConflict)
		}
		result = stored
	} else if errors.Is(readErr, recoveryset.ErrNotFound) {
		result, err = recoveryset.NewValidationResult(envelope, now)
		if err != nil {
			return admincli.RecoveryValidateResult{}, fmt.Errorf("construct recovery validation result: %w", err)
		}
		if err := recoveryRepo.RecordValidationResult(ctx, result); err != nil {
			return admincli.RecoveryValidateResult{}, fmt.Errorf("record recovery validation result: %w", err)
		}
	} else {
		return admincli.RecoveryValidateResult{}, fmt.Errorf("read recovery validation result: %w", readErr)
	}
	terminal := attempt
	terminal.Status = recoveryset.ValidationPassed
	terminal.ResultDigest = result.ResultDigest
	terminal.CompletedAt = now
	if err := recoveryRepo.CompleteValidation(ctx, terminal); err != nil {
		return admincli.RecoveryValidateResult{}, fmt.Errorf("complete recovery validation: %w", err)
	}
	terminal, err = recoveryRepo.ValidationAttempt(ctx, attempt.AttemptID)
	if err != nil {
		return admincli.RecoveryValidateResult{}, fmt.Errorf("read completed recovery validation: %w", err)
	}
	return admincli.RecoveryValidateResult{Attempt: terminal, Result: &result}, nil
}

// PublishRecovery exposes only the exact fenced publication transition. All
// provider checks must have been captured in the passed typed validation
// result before this method is called.
func (o Operations) PublishRecovery(ctx context.Context, request admincli.RecoveryPublishRequest) (admincli.RecoveryPublishResult, error) {
	ctx = contextOrBackground(ctx)
	pool, closePool, err := o.openRecoveryMaintenance(ctx)
	if err != nil {
		return admincli.RecoveryPublishResult{}, err
	}
	defer closePool()
	recoveryRepo := recoverysetpostgres.New(pool)
	setID := strings.TrimSpace(request.SetID)
	attemptID := strings.TrimSpace(request.ValidationAttemptID)
	set, err := recoveryRepo.Publish(ctx, setID, strings.TrimSpace(request.Publisher), request.FenceEpoch, attemptID)
	if err != nil {
		return admincli.RecoveryPublishResult{}, fmt.Errorf("publish recovery set: %w", err)
	}
	return admincli.RecoveryPublishResult{Set: set}, nil
}

func (o Operations) openRecoveryMaintenance(ctx context.Context) (MaintenancePool, func(), error) {
	deps := o.Dependencies.withDefaults()
	cfg, err := deps.LoadConfig()
	if err != nil {
		return nil, func() {}, err
	}
	if !cfg.Production {
		return nil, func() {}, ErrNativeMaintenanceUnavailable
	}
	if err := cfg.ValidatePostgresProduction(); err != nil {
		return nil, func() {}, fmt.Errorf("validate production PostgreSQL recovery configuration: %w", err)
	}
	maintenanceConfig := cfg.PostgresControlMaintenanceConfig()
	if strings.TrimSpace(maintenanceConfig.URL) == "" {
		return nil, func() {}, errors.New("production recovery requires LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_URL")
	}
	pool, err := deps.OpenMaintenance(contextOrBackground(ctx), maintenanceConfig)
	if err != nil {
		return nil, func() {}, fmt.Errorf("open PostgreSQL maintenance pool: %w", err)
	}
	if nilMaintenancePool(pool) {
		return nil, func() {}, errors.New("open PostgreSQL maintenance pool returned nil pool")
	}
	if err := deps.VerifyBaseline(contextOrBackground(ctx), pool); err != nil {
		pool.Close()
		return nil, func() {}, fmt.Errorf("verify PostgreSQL control baseline before recovery: %w", err)
	}
	return pool, pool.Close, nil
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
