package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	releasedb "github.com/flidai/leapview/internal/release/postgres/internal/db"
	"github.com/flidai/leapview/internal/release/transitionoperation"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const transitionLeaseMaximum = 24 * time.Hour

type transitionOperationRow struct {
	OperationID, TargetIdentityDigest, PredecessorArtifactDigest, CandidateArtifactDigest string
	RecoveryFrontierID, RecoveryFrontierDigest, PreflightEvidenceDigest                   string
	PreflightEvidence                                                                     []byte
	IdempotencyKey, RequestDigest, Status, CurrentPhase, OwnerID                          string
	FencingGeneration                                                                     int64
	LeaseExpiresAt, CreatedAt, UpdatedAt, TerminalAt                                      time.Time
}

type transitionFenceRow struct {
	TargetIdentityDigest string
	OperationID          *string
	OwnerID              string
	FencingGeneration    int64
	LeaseExpiresAt       *time.Time
	UpdatedAt            time.Time
}

func transitionDBUUID(value string) (pgtype.UUID, error) {
	parsed, err := uuid.Parse(value)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}, nil
}

func transitionUUIDString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuid.UUID(value.Bytes).String()
}

func transitionInterval(value time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: value.Microseconds(), Valid: true}
}

func transitionTimestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}

func transitionRow(row releasedb.ReleaseReleaseTransitionOperation) transitionOperationRow {
	return transitionOperationRow{
		OperationID: transitionUUIDString(row.OperationID), TargetIdentityDigest: row.TargetIdentityDigest,
		PredecessorArtifactDigest: row.PredecessorArtifactDigest, CandidateArtifactDigest: row.CandidateArtifactDigest,
		RecoveryFrontierID: row.RecoveryFrontierID, RecoveryFrontierDigest: row.RecoveryFrontierDigest,
		PreflightEvidenceDigest: row.PreflightEvidenceDigest, PreflightEvidence: append([]byte(nil), row.PreflightEvidence...),
		IdempotencyKey: row.IdempotencyKey, RequestDigest: row.RequestDigest, Status: row.Status, CurrentPhase: row.CurrentPhase,
		OwnerID: row.OwnerID, FencingGeneration: row.FencingGeneration, LeaseExpiresAt: row.LeaseExpiresAt.Time,
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time, TerminalAt: row.TerminalAt.Time,
	}
}

func transitionFence(row releasedb.ReleaseReleaseTransitionFence) transitionFenceRow {
	out := transitionFenceRow{TargetIdentityDigest: row.TargetIdentityDigest, OwnerID: row.OwnerID, FencingGeneration: row.FencingGeneration, UpdatedAt: row.UpdatedAt.Time}
	if row.OperationID.Valid {
		value := transitionUUIDString(row.OperationID)
		out.OperationID = &value
	}
	if row.LeaseExpiresAt.Valid {
		value := row.LeaseExpiresAt.Time
		out.LeaseExpiresAt = &value
	}
	return out
}

func (r *Repository) AcquireTransition(ctx context.Context, input transitionoperation.CreateInput, ownerID string, lease time.Duration) (transitionoperation.Operation, error) {
	if r == nil || r.db == nil {
		return transitionoperation.Operation{}, transitionoperation.ErrInvalid
	}
	b, ok := r.db.(beginner)
	if !ok {
		return transitionoperation.Operation{}, errors.New("release PostgreSQL database does not support transactions")
	}
	var out transitionoperation.Operation
	err := pgx.BeginFunc(ctx, b, func(tx pgx.Tx) error {
		var err error
		out, err = r.AcquireTransitionTx(ctx, tx, input, ownerID, lease)
		return err
	})
	return out, err
}

func (r *Repository) AcquireTransitionTx(ctx context.Context, tx Tx, input transitionoperation.CreateInput, ownerID string, lease time.Duration) (transitionoperation.Operation, error) {
	if tx == nil || strings.TrimSpace(ownerID) == "" || len(ownerID) > 255 {
		return transitionoperation.Operation{}, transitionoperation.ErrInvalid
	}
	if lease <= 0 || lease > transitionLeaseMaximum {
		return transitionoperation.Operation{}, transitionoperation.ErrInvalid
	}
	if err := validateTransitionInput(input); err != nil {
		return transitionoperation.Operation{}, err
	}
	if input.RequestDigest == "" {
		var err error
		input.RequestDigest, err = input.Digest()
		if err != nil {
			return transitionoperation.Operation{}, err
		}
	}
	if input.OperationID == "" {
		input.OperationID = uuid.New().String()
	}
	operationID, err := transitionDBUUID(input.OperationID)
	if err != nil {
		return transitionoperation.Operation{}, transitionoperation.ErrInvalid
	}
	q := releasedb.New(tx)
	if _, err := q.InsertTransitionOperation(ctx, releasedb.InsertTransitionOperationParams{
		OperationID: operationID, TargetIdentityDigest: input.TargetIdentityDigest,
		PredecessorArtifactDigest: input.PredecessorArtifactDigest, CandidateArtifactDigest: input.CandidateArtifactDigest,
		RecoveryFrontierID: input.RecoveryFrontierID, RecoveryFrontierDigest: input.RecoveryFrontierDigest,
		PreflightEvidenceDigest: input.PreflightEvidenceDigest, PreflightEvidence: input.PreflightEvidence,
		IdempotencyKey: input.IdempotencyKey, RequestDigest: input.RequestDigest,
	}); err != nil {
		return transitionoperation.Operation{}, err
	}
	row, err := lockTransitionByIdentity(ctx, tx, input.TargetIdentityDigest, input.IdempotencyKey)
	if err != nil {
		return transitionoperation.Operation{}, err
	}
	if !sameTransitionIdentity(row, input) {
		return transitionoperation.Operation{}, transitionoperation.ErrConflict
	}
	if row.Status == string(transitionoperation.StatusCompleted) || row.Status == string(transitionoperation.StatusFailed) || row.Status == string(transitionoperation.StatusIndeterminate) {
		return readTransitionOperation(ctx, tx, row)
	}
	if _, err := q.EnsureTransitionFence(ctx, row.TargetIdentityDigest); err != nil {
		return transitionoperation.Operation{}, err
	}
	fence, err := lockTransitionFence(ctx, tx, row.TargetIdentityDigest)
	if err != nil {
		return transitionoperation.Operation{}, err
	}
	now, err := q.CurrentTransitionDatabaseTime(ctx)
	if err != nil {
		return transitionoperation.Operation{}, err
	}
	if fence.OperationID != nil && fence.LeaseExpiresAt != nil && fence.LeaseExpiresAt.After(now.Time) {
		// Even the same textual owner cannot re-enter an active claim. This
		// makes the fencing generation, not a caller-chosen string, the unique
		// holder token and prevents two processes from executing one phase.
		return transitionoperation.Operation{}, transitionoperation.ErrBusy
	}

	// A running phase whose lease elapsed has an unknown external outcome. It
	// is terminalized before a new operation can take the singleton fence.
	if fence.OperationID != nil && fence.LeaseExpiresAt != nil && !fence.LeaseExpiresAt.After(now.Time) {
		expiredID, parseErr := transitionDBUUID(*fence.OperationID)
		if parseErr != nil {
			return transitionoperation.Operation{}, transitionoperation.ErrConflict
		}
		expiredRow, lockErr := lockTransitionByID(ctx, tx, *fence.OperationID)
		if lockErr != nil {
			return transitionoperation.Operation{}, lockErr
		}
		expired, readErr := readTransitionOperation(ctx, tx, expiredRow)
		if readErr != nil {
			return transitionoperation.Operation{}, readErr
		}
		if recordedTransitionSuccess(expired) {
			// Every effect and the success marker are durable. The final completion
			// write may have failed after that marker; finish it under both locks.
			var affected int64
			affected, err = q.CompleteRecordedTransitionOperation(ctx, expiredID)
			if err != nil {
				return transitionoperation.Operation{}, err
			}
			if affected != 1 {
				return transitionoperation.Operation{}, transitionoperation.ErrConflict
			}
		} else if _, err = q.MarkTransitionIndeterminate(ctx, expiredID); err != nil {
			return transitionoperation.Operation{}, err
		}
		if *fence.OperationID == row.OperationID && row.Status == string(transitionoperation.StatusRunning) {
			if _, err := q.ClearTransitionFence(ctx, releasedb.ClearTransitionFenceParams{TargetIdentityDigest: row.TargetIdentityDigest, Column4: -1}); err != nil {
				return transitionoperation.Operation{}, err
			}
			row, err = lockTransitionByID(ctx, tx, row.OperationID)
			if err != nil {
				return transitionoperation.Operation{}, err
			}
			return readTransitionOperation(ctx, tx, row)
		}
	}
	newGeneration := fence.FencingGeneration + 1
	rowID, err := transitionDBUUID(row.OperationID)
	if err != nil {
		return transitionoperation.Operation{}, transitionoperation.ErrConflict
	}
	interval := transitionInterval(lease)
	_, err = q.ClaimTransitionFence(ctx, releasedb.ClaimTransitionFenceParams{Column1: rowID, OwnerID: ownerID, FencingGeneration: newGeneration, Column4: interval, TargetIdentityDigest: row.TargetIdentityDigest})
	if err != nil {
		return transitionoperation.Operation{}, err
	}
	affected, err := q.ClaimTransitionOperation(ctx, releasedb.ClaimTransitionOperationParams{OwnerID: ownerID, FencingGeneration: newGeneration, Column3: interval, Column4: rowID})
	if err != nil {
		return transitionoperation.Operation{}, err
	}
	if affected != 1 {
		return transitionoperation.Operation{}, transitionoperation.ErrConflict
	}
	row, err = lockTransitionByID(ctx, tx, row.OperationID)
	if err != nil {
		return transitionoperation.Operation{}, err
	}
	return readTransitionOperation(ctx, tx, row)
}

func recordedTransitionSuccess(op transitionoperation.Operation) bool {
	phases := transitionoperation.PhaseNames()
	if op.Status != transitionoperation.StatusRunning || op.CurrentPhase != transitionoperation.PhaseSuccess || len(op.PhaseResults) != len(phases) {
		return false
	}
	for i, phase := range phases {
		if op.PhaseResults[i].Phase != phase || op.PhaseResults[i].Status != transitionoperation.PhaseResultSucceeded {
			return false
		}
	}
	return true
}

func validateTransitionInput(input transitionoperation.CreateInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	evidence, err := transitionpreflight.ParseEvidence(input.PreflightEvidence)
	if err != nil {
		return fmt.Errorf("%w: preflight evidence", transitionoperation.ErrInvalid)
	}
	evidenceDigest, err := evidence.Digest()
	if err != nil || evidenceDigest != input.PreflightEvidenceDigest {
		return fmt.Errorf("%w: preflight evidence digest", transitionoperation.ErrConflict)
	}
	canonicalEvidence, err := evidence.CanonicalJSON()
	if err != nil || !bytes.Equal(canonicalEvidence, input.PreflightEvidence) {
		return fmt.Errorf("%w: preflight evidence is not canonical", transitionoperation.ErrConflict)
	}
	predecessorDigest, predecessorErr := evidence.Predecessor.Digest()
	candidateDigest, candidateErr := evidence.Candidate.Digest()
	if predecessorErr != nil || candidateErr != nil || predecessorDigest != input.PredecessorArtifactDigest || candidateDigest != input.CandidateArtifactDigest || evidence.TargetIdentityDigest != input.TargetIdentityDigest || evidence.RecoveryFrontier == nil || evidence.RecoveryFrontier.SetID != input.RecoveryFrontierID || evidence.RecoveryFrontier.Digest != input.RecoveryFrontierDigest {
		return fmt.Errorf("%w: preflight evidence binding", transitionoperation.ErrConflict)
	}
	return nil
}

func (r *Repository) GetTransition(ctx context.Context, operationID string) (transitionoperation.Operation, error) {
	if r == nil || r.db == nil {
		return transitionoperation.Operation{}, transitionoperation.ErrInvalid
	}
	id, parseErr := transitionDBUUID(operationID)
	if parseErr != nil {
		return transitionoperation.Operation{}, transitionoperation.ErrInvalid
	}
	dbrow, err := releasedb.New(r.db).GetTransitionOperation(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return transitionoperation.Operation{}, transitionoperation.ErrNotFound
		}
		return transitionoperation.Operation{}, err
	}
	return readTransitionOperation(ctx, r.db, transitionRow(dbrow))
}

func (r *Repository) RecordTransitionPhase(ctx context.Context, fence transitionoperation.Fence, result transitionoperation.PhaseResult) (transitionoperation.Operation, error) {
	if r == nil || r.db == nil {
		return transitionoperation.Operation{}, transitionoperation.ErrInvalid
	}
	b, ok := r.db.(beginner)
	if !ok {
		return transitionoperation.Operation{}, errors.New("release PostgreSQL database does not support transactions")
	}
	var out transitionoperation.Operation
	err := pgx.BeginFunc(ctx, b, func(tx pgx.Tx) error {
		var err error
		out, err = r.RecordTransitionPhaseTx(ctx, tx, fence, result)
		return err
	})
	return out, err
}

func (r *Repository) RecordTransitionPhaseTx(ctx context.Context, tx Tx, fence transitionoperation.Fence, result transitionoperation.PhaseResult) (transitionoperation.Operation, error) {
	if tx == nil || !result.Phase.Valid() || !result.Status.Valid() || result.Phase == "" || len(result.Result) == 0 || !json.Valid(result.Result) {
		return transitionoperation.Operation{}, transitionoperation.ErrInvalid
	}
	if len(result.Result) > transitionoperation.MaxEvidenceBytes {
		return transitionoperation.Operation{}, transitionoperation.ErrInvalid
	}
	row, err := lockTransitionByID(ctx, tx, fence.OperationID)
	if err != nil {
		return transitionoperation.Operation{}, err
	}
	if row.OwnerID != fence.OwnerID || row.FencingGeneration != fence.FencingGeneration {
		return transitionoperation.Operation{}, transitionoperation.ErrStaleFence
	}
	if row.Status == string(transitionoperation.StatusCompleted) || row.Status == string(transitionoperation.StatusFailed) || row.Status == string(transitionoperation.StatusIndeterminate) {
		return transitionoperation.Operation{}, transitionoperation.ErrAlreadyTerminal
	}
	if transitionoperation.Phase(row.CurrentPhase) != result.Phase {
		return transitionoperation.Operation{}, transitionoperation.ErrConflict
	}
	digest := result.ResultDigest
	if digest == "" {
		digest = phaseResultDigest(result.Result)
	}
	rowID, err := transitionDBUUID(row.OperationID)
	if err != nil {
		return transitionoperation.Operation{}, transitionoperation.ErrConflict
	}
	startedAt := result.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	q := releasedb.New(tx)
	if _, err := q.InsertTransitionPhaseResult(ctx, releasedb.InsertTransitionPhaseResultParams{OperationID: rowID, Phase: string(result.Phase), ResultStatus: string(result.Status), ResultDigest: digest, ResultBytes: result.Result, StartedAt: transitionTimestamp(startedAt), CompletedAt: transitionTimestamp(time.Now().UTC())}); err != nil {
		return transitionoperation.Operation{}, err
	}
	existing, err := q.GetTransitionPhaseResult(ctx, releasedb.GetTransitionPhaseResultParams{Column1: rowID, Phase: string(result.Phase)})
	if err != nil {
		return transitionoperation.Operation{}, err
	}
	if existing.ResultStatus != string(result.Status) || existing.ResultDigest != digest || !bytes.Equal(existing.ResultBytes, result.Result) {
		return transitionoperation.Operation{}, transitionoperation.ErrConflict
	}
	next := transitionoperation.Phase(row.CurrentPhase)
	status := transitionoperation.StatusRunning
	if result.Status == transitionoperation.PhaseResultSucceeded {
		names := transitionoperation.PhaseNames()
		rank := phaseIndex(result.Phase)
		if rank == len(names)-1 {
			// The runner validates the durable fence once more before calling
			// Complete; retain the lease through that boundary.
			status = transitionoperation.StatusRunning
		} else {
			next = names[rank+1]
		}
	} else if result.Status == transitionoperation.PhaseResultFailed {
		status = transitionoperation.StatusFailed
	} else {
		status = transitionoperation.StatusIndeterminate
	}
	terminal := status.Terminal()
	affected, err := q.AdvanceTransitionOperation(ctx, releasedb.AdvanceTransitionOperationParams{Status: string(status), CurrentPhase: string(next), Column3: rowID, OwnerID: fence.OwnerID, FencingGeneration: fence.FencingGeneration})
	if err != nil {
		return transitionoperation.Operation{}, err
	}
	if affected != 1 {
		return transitionoperation.Operation{}, transitionoperation.ErrLeaseExpired
	}
	if terminal {
		if _, err := q.ClearTransitionFence(ctx, releasedb.ClearTransitionFenceParams{TargetIdentityDigest: row.TargetIdentityDigest, Column2: rowID, Column3: fence.OwnerID, Column4: fence.FencingGeneration}); err != nil {
			return transitionoperation.Operation{}, err
		}
	}
	row, err = lockTransitionByID(ctx, tx, row.OperationID)
	if err != nil {
		return transitionoperation.Operation{}, err
	}
	return readTransitionOperation(ctx, tx, row)
}

func (r *Repository) RenewTransitionFence(ctx context.Context, fence transitionoperation.Fence, lease time.Duration) (transitionoperation.Fence, error) {
	if r == nil || r.db == nil {
		return transitionoperation.Fence{}, transitionoperation.ErrInvalid
	}
	b, ok := r.db.(beginner)
	if !ok {
		return transitionoperation.Fence{}, errors.New("release PostgreSQL database does not support transactions")
	}
	var out transitionoperation.Fence
	err := pgx.BeginFunc(ctx, b, func(tx pgx.Tx) error {
		var err error
		out, err = r.RenewTransitionFenceTx(ctx, tx, fence, lease)
		return err
	})
	return out, err
}

func (r *Repository) RenewTransitionFenceTx(ctx context.Context, tx Tx, fence transitionoperation.Fence, lease time.Duration) (transitionoperation.Fence, error) {
	if tx == nil || strings.TrimSpace(fence.OperationID) == "" || strings.TrimSpace(fence.OwnerID) == "" || lease <= 0 || lease > transitionLeaseMaximum {
		return transitionoperation.Fence{}, transitionoperation.ErrInvalid
	}
	row, err := lockTransitionByID(ctx, tx, fence.OperationID)
	if err != nil {
		return transitionoperation.Fence{}, err
	}
	if row.OwnerID != fence.OwnerID || row.FencingGeneration != fence.FencingGeneration {
		return transitionoperation.Fence{}, transitionoperation.ErrStaleFence
	}
	id, err := transitionDBUUID(fence.OperationID)
	if err != nil {
		return transitionoperation.Fence{}, transitionoperation.ErrInvalid
	}
	q := releasedb.New(tx)
	renewed, err := q.RenewTransitionFence(ctx, releasedb.RenewTransitionFenceParams{Column1: transitionInterval(lease), Column2: id, OwnerID: fence.OwnerID, FencingGeneration: fence.FencingGeneration, TargetIdentityDigest: row.TargetIdentityDigest})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return transitionoperation.Fence{}, transitionoperation.ErrStaleFence
		}
		return transitionoperation.Fence{}, err
	}
	expires := renewed.LeaseExpiresAt.Time
	affected, err := q.UpdateTransitionOperationLease(ctx, releasedb.UpdateTransitionOperationLeaseParams{LeaseExpiresAt: renewed.LeaseExpiresAt, Column2: id, OwnerID: fence.OwnerID, FencingGeneration: fence.FencingGeneration})
	if err != nil {
		return transitionoperation.Fence{}, err
	}
	if affected != 1 {
		return transitionoperation.Fence{}, transitionoperation.ErrStaleFence
	}
	return transitionoperation.Fence{OperationID: fence.OperationID, OwnerID: fence.OwnerID, FencingGeneration: fence.FencingGeneration, LeaseExpiresAt: expires}, nil
}

func lockTransitionByIdentity(ctx context.Context, db DBTX, target, key string) (transitionOperationRow, error) {
	row, err := releasedb.New(db).LockTransitionOperationByIdempotency(ctx, releasedb.LockTransitionOperationByIdempotencyParams{TargetIdentityDigest: target, IdempotencyKey: key})
	return transitionRow(row), err
}
func lockTransitionByID(ctx context.Context, db DBTX, id string) (transitionOperationRow, error) {
	parsed, parseErr := transitionDBUUID(id)
	if parseErr != nil {
		return transitionOperationRow{}, transitionoperation.ErrInvalid
	}
	row, err := releasedb.New(db).LockTransitionOperation(ctx, parsed)
	return transitionRow(row), err
}
func lockTransitionFence(ctx context.Context, db DBTX, targetIdentityDigest string) (transitionFenceRow, error) {
	row, err := releasedb.New(db).LockTransitionFence(ctx, targetIdentityDigest)
	return transitionFence(row), err
}
func sameTransitionIdentity(row transitionOperationRow, in transitionoperation.CreateInput) bool {
	digest := in.RequestDigest
	if digest == "" {
		digest, _ = in.Digest()
	}
	return (in.OperationID == "" || row.OperationID == in.OperationID) &&
		row.TargetIdentityDigest == in.TargetIdentityDigest &&
		row.PredecessorArtifactDigest == in.PredecessorArtifactDigest &&
		row.CandidateArtifactDigest == in.CandidateArtifactDigest &&
		row.RecoveryFrontierID == in.RecoveryFrontierID &&
		row.RecoveryFrontierDigest == in.RecoveryFrontierDigest &&
		row.PreflightEvidenceDigest == in.PreflightEvidenceDigest &&
		bytes.Equal(row.PreflightEvidence, in.PreflightEvidence) &&
		row.IdempotencyKey == in.IdempotencyKey && row.RequestDigest == digest
}
func readTransitionOperation(ctx context.Context, db DBTX, row transitionOperationRow) (transitionoperation.Operation, error) {
	evidence := append([]byte(nil), row.PreflightEvidence...)
	op := transitionoperation.Operation{OperationID: row.OperationID, TargetIdentityDigest: row.TargetIdentityDigest, PredecessorArtifactDigest: row.PredecessorArtifactDigest, CandidateArtifactDigest: row.CandidateArtifactDigest, RecoveryFrontierID: row.RecoveryFrontierID, RecoveryFrontierDigest: row.RecoveryFrontierDigest, PreflightEvidenceDigest: row.PreflightEvidenceDigest, PreflightEvidence: evidence, IdempotencyKey: row.IdempotencyKey, RequestDigest: row.RequestDigest, Status: transitionoperation.Status(row.Status), CurrentPhase: transitionoperation.Phase(row.CurrentPhase), Fence: transitionoperation.Fence{OperationID: row.OperationID, OwnerID: row.OwnerID, FencingGeneration: row.FencingGeneration, LeaseExpiresAt: row.LeaseExpiresAt}, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, TerminalAt: row.TerminalAt}
	if err := validateTransitionInput(op.Input()); err != nil {
		return transitionoperation.Operation{}, fmt.Errorf("%w: stored operation identity", transitionoperation.ErrConflict)
	}
	id, err := transitionDBUUID(row.OperationID)
	if err != nil {
		return transitionoperation.Operation{}, transitionoperation.ErrConflict
	}
	rows, err := releasedb.New(db).ListTransitionPhaseResults(ctx, id)
	if err != nil {
		return transitionoperation.Operation{}, err
	}
	for _, stored := range rows {
		item := transitionoperation.PhaseResult{Phase: transitionoperation.Phase(stored.Phase), Status: transitionoperation.PhaseResultStatus(stored.ResultStatus), ResultDigest: stored.ResultDigest, Result: append([]byte(nil), stored.ResultBytes...), StartedAt: stored.StartedAt.Time, CompletedAt: stored.CompletedAt.Time}
		if !json.Valid(item.Result) || phaseResultDigest(item.Result) != item.ResultDigest {
			return transitionoperation.Operation{}, fmt.Errorf("%w: stored phase evidence", transitionoperation.ErrConflict)
		}
		op.PhaseResults = append(op.PhaseResults, item)
	}
	return op, nil
}
func phaseIndex(phase transitionoperation.Phase) int {
	for i, candidate := range transitionoperation.PhaseNames() {
		if candidate == phase {
			return i
		}
	}
	return -1
}
func phaseResultDigest(result []byte) string {
	sum := sha256.Sum256(append([]byte("leapview/release-transition-phase/v1\n"), result...))
	return "sha256:" + hex.EncodeToString(sum[:])
}
