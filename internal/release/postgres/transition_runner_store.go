package postgres

// This file is the narrow adapter consumed by transitionrunner. Keeping the
// runner's phase vocabulary at this boundary lets the durable schema retain
// the exact candidate transition milestones without importing orchestration
// into the release authority itself.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	releasedb "github.com/flidai/leapview/internal/release/postgres/internal/db"
	"github.com/flidai/leapview/internal/release/transitionoperation"
	"github.com/flidai/leapview/internal/release/transitionrunner"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// TransitionRepository is the runner-facing view of the release PostgreSQL
// authority. It embeds the existing release repository for shared DB access,
// while avoiding collisions with the release Create/Get methods.
type TransitionRepository struct{ *Repository }

func NewTransitionRepository(db DBTX) *TransitionRepository {
	return &TransitionRepository{Repository: New(db)}
}

func durablePhase(p transitionrunner.Phase) transitionoperation.Phase {
	switch p {
	case transitionrunner.PhasePreflight:
		return transitionoperation.PhasePreflight
	case transitionrunner.PhaseMigrations:
		return transitionoperation.PhaseMigrations
	case transitionrunner.PhaseStage:
		return transitionoperation.PhaseCandidateStaged
	case transitionrunner.PhaseActivate:
		return transitionoperation.PhaseCandidateActivated
	case transitionrunner.PhaseRestart:
		return transitionoperation.PhaseCandidateRestarted
	case transitionrunner.PhasePostValidate:
		return transitionoperation.PhasePostValidated
	case transitionrunner.PhaseSuccess:
		return transitionoperation.PhaseSuccess
	default:
		return ""
	}
}

func (r *TransitionRepository) Create(ctx context.Context, input transitionoperation.CreateInput) (transitionoperation.Operation, error) {
	if r == nil || r.db == nil {
		return transitionoperation.Operation{}, transitionoperation.ErrInvalid
	}
	if err := validateTransitionInput(input); err != nil {
		return transitionoperation.Operation{}, err
	}
	if input.RequestDigest == "" {
		input.RequestDigest, _ = input.Digest()
	}
	requestedOperationID := input.OperationID
	if input.OperationID == "" {
		input.OperationID = uuidString()
	}
	b, ok := r.db.(beginner)
	if !ok {
		return transitionoperation.Operation{}, transitionoperation.ErrInvalid
	}
	var out transitionoperation.Operation
	err := pgx.BeginFunc(ctx, b, func(tx pgx.Tx) error {
		id, parseErr := transitionDBUUID(input.OperationID)
		if parseErr != nil {
			return transitionoperation.ErrInvalid
		}
		_, err := releasedb.New(tx).InsertTransitionOperation(ctx, releasedb.InsertTransitionOperationParams{OperationID: id, TargetIdentityDigest: input.TargetIdentityDigest, PredecessorArtifactDigest: input.PredecessorArtifactDigest, CandidateArtifactDigest: input.CandidateArtifactDigest, RecoveryFrontierID: input.RecoveryFrontierID, RecoveryFrontierDigest: input.RecoveryFrontierDigest, PreflightEvidenceDigest: input.PreflightEvidenceDigest, PreflightEvidence: input.PreflightEvidence, IdempotencyKey: input.IdempotencyKey, RequestDigest: input.RequestDigest})
		if err != nil {
			return err
		}
		row, err := lockTransitionByIdentity(ctx, tx, input.TargetIdentityDigest, input.IdempotencyKey)
		if err != nil {
			return err
		}
		expected := input
		if requestedOperationID == "" {
			expected.OperationID = ""
		}
		if !sameTransitionIdentity(row, expected) {
			return transitionoperation.ErrConflict
		}
		out, err = readTransitionOperation(ctx, tx, row)
		return err
	})
	return out, err
}

func (r *TransitionRepository) Get(ctx context.Context, operationID string) (transitionoperation.Operation, error) {
	return r.GetTransition(ctx, operationID)
}

func (r *TransitionRepository) Claim(ctx context.Context, operationID, ownerID string, leaseUntil time.Time) (transitionoperation.Operation, error) {
	op, err := r.Get(ctx, operationID)
	if err != nil {
		return transitionoperation.Operation{}, err
	}
	if op.Status.Terminal() {
		return op, nil
	}
	d := time.Until(leaseUntil)
	if d <= 0 {
		return transitionoperation.Operation{}, transitionoperation.ErrInvalid
	}
	return r.AcquireTransition(ctx, op.Input(), ownerID, d)
}

func (r *TransitionRepository) PhaseCompleted(ctx context.Context, operationID string, phase transitionrunner.Phase) (bool, error) {
	op, err := r.Get(ctx, operationID)
	if err != nil {
		return false, err
	}
	want := durablePhase(phase)
	if want == "" {
		return false, transitionoperation.ErrInvalid
	}
	for _, result := range op.PhaseResults {
		if result.Phase == want && result.Status == transitionoperation.PhaseResultSucceeded {
			return true, nil
		}
	}
	return false, nil
}

func (r *TransitionRepository) RecordPhase(ctx context.Context, input transitionrunner.PhaseRecordInput) (transitionoperation.Operation, error) {
	phase := durablePhase(input.Phase)
	if phase == "" {
		return transitionoperation.Operation{}, transitionoperation.ErrInvalid
	}
	return r.RecordTransitionPhase(ctx, input.Fence, transitionoperation.PhaseResult{Phase: phase, Status: input.Status, Result: input.Result})
}

func (r *TransitionRepository) Complete(ctx context.Context, input transitionrunner.CompleteInput) (transitionoperation.Operation, error) {
	op, err := r.Get(ctx, input.OperationID)
	if err != nil {
		return transitionoperation.Operation{}, err
	}
	if op.Status == transitionoperation.StatusCompleted {
		return op, nil
	}
	if op.Fence.OwnerID != input.OwnerID || op.Fence.FencingGeneration != input.Fence.FencingGeneration {
		return transitionoperation.Operation{}, transitionoperation.ErrStaleFence
	}
	return r.finishTransition(ctx, input.Fence, transitionoperation.StatusCompleted)
}

func (r *TransitionRepository) Fail(ctx context.Context, input transitionrunner.FailureInput) (transitionoperation.Operation, error) {
	payload, _ := json.Marshal(struct {
		Phase   transitionrunner.Phase `json:"phase"`
		Code    string                 `json:"code"`
		Summary string                 `json:"summary"`
	}{input.Phase, input.Code, input.Summary})
	phase := durablePhase(input.Phase)
	if phase == "" {
		return transitionoperation.Operation{}, transitionoperation.ErrInvalid
	}
	status := input.Status
	if status == "" {
		status = transitionoperation.PhaseResultFailed
	}
	if status != transitionoperation.PhaseResultFailed && status != transitionoperation.PhaseResultIndeterminate {
		return transitionoperation.Operation{}, transitionoperation.ErrInvalid
	}
	return r.RecordTransitionPhase(ctx, input.Fence, transitionoperation.PhaseResult{Phase: phase, Status: status, Result: payload})
}

func (r *TransitionRepository) Acquire(ctx context.Context, targetIdentityDigest, operationID, ownerID string, leaseUntil time.Time) (transitionoperation.Fence, error) {
	if r == nil || r.db == nil || strings.TrimSpace(targetIdentityDigest) == "" || strings.TrimSpace(operationID) == "" || strings.TrimSpace(ownerID) == "" {
		return transitionoperation.Fence{}, transitionoperation.ErrInvalid
	}
	operation, err := r.Get(ctx, operationID)
	if err != nil {
		return transitionoperation.Fence{}, err
	}
	if operation.TargetIdentityDigest != targetIdentityDigest {
		return transitionoperation.Fence{}, transitionoperation.ErrConflict
	}
	lease := time.Until(leaseUntil)
	if lease <= 0 {
		return transitionoperation.Fence{}, transitionoperation.ErrInvalid
	}
	claimed, err := r.AcquireTransition(ctx, operation.Input(), ownerID, lease)
	if err != nil {
		return transitionoperation.Fence{}, err
	}
	if claimed.Status.Terminal() {
		return transitionoperation.Fence{}, transitionoperation.ErrAlreadyTerminal
	}
	return claimed.Fence, nil
}

func (r *TransitionRepository) Renew(ctx context.Context, fence transitionoperation.Fence, leaseUntil time.Time) (transitionoperation.Fence, error) {
	lease := time.Until(leaseUntil)
	if lease <= 0 {
		return transitionoperation.Fence{}, transitionoperation.ErrInvalid
	}
	return r.RenewTransitionFence(ctx, fence, lease)
}

func (r *TransitionRepository) Validate(ctx context.Context, fence transitionoperation.Fence) error {
	if r == nil || r.db == nil {
		return transitionoperation.ErrInvalid
	}
	id, parseErr := transitionDBUUID(fence.OperationID)
	if parseErr != nil {
		return transitionoperation.ErrInvalid
	}
	active, err := releasedb.New(r.db).ValidateTransitionFence(ctx, releasedb.ValidateTransitionFenceParams{Column1: id, OwnerID: fence.OwnerID, FencingGeneration: fence.FencingGeneration})
	if errors.Is(err, pgx.ErrNoRows) {
		return transitionoperation.ErrStaleFence
	}
	if err != nil {
		return err
	}
	if !active {
		return transitionoperation.ErrLeaseExpired
	}
	return nil
}

func (r *TransitionRepository) Release(ctx context.Context, fence transitionoperation.Fence) error {
	if r == nil || r.db == nil {
		return transitionoperation.ErrInvalid
	}
	id, parseErr := transitionDBUUID(fence.OperationID)
	if parseErr != nil {
		return transitionoperation.ErrInvalid
	}
	_, err := releasedb.New(r.db).ReleaseTransitionFence(ctx, releasedb.ReleaseTransitionFenceParams{Column1: id, OwnerID: fence.OwnerID, FencingGeneration: fence.FencingGeneration})
	return err
}

func (r *TransitionRepository) finishTransition(ctx context.Context, fence transitionoperation.Fence, status transitionoperation.Status) (transitionoperation.Operation, error) {
	b, ok := r.db.(beginner)
	if !ok {
		return transitionoperation.Operation{}, transitionoperation.ErrInvalid
	}
	var out transitionoperation.Operation
	err := pgx.BeginFunc(ctx, b, func(tx pgx.Tx) error {
		row, err := lockTransitionByID(ctx, tx, fence.OperationID)
		if err != nil {
			return err
		}
		if row.OwnerID != fence.OwnerID || row.FencingGeneration != fence.FencingGeneration {
			return transitionoperation.ErrStaleFence
		}
		id, parseErr := transitionDBUUID(fence.OperationID)
		if parseErr != nil {
			return transitionoperation.ErrInvalid
		}
		q := releasedb.New(tx)
		affected, err := q.CompleteTransitionOperation(ctx, releasedb.CompleteTransitionOperationParams{Status: string(status), Column2: id, OwnerID: fence.OwnerID, FencingGeneration: fence.FencingGeneration})
		if err != nil {
			return err
		}
		if affected != 1 {
			return transitionoperation.ErrLeaseExpired
		}
		if _, err := q.ClearTransitionFence(ctx, releasedb.ClearTransitionFenceParams{TargetIdentityDigest: row.TargetIdentityDigest, Column2: id, Column3: fence.OwnerID, Column4: fence.FencingGeneration}); err != nil {
			return err
		}
		row, err = lockTransitionByID(ctx, tx, fence.OperationID)
		if err != nil {
			return err
		}
		out, err = readTransitionOperation(ctx, tx, row)
		return err
	})
	return out, err
}

func uuidString() string { return uuid.New().String() }
