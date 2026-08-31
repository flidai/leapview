// Package deploymentoperation composes deployment's capability-neutral
// operation authority with the platform operation PostgreSQL repository.
package deploymentoperation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	operationpostgres "github.com/flidai/leapview/internal/platform/operation/postgres"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
)

// Adapter is stateless and safe to share between deployment requests.
type Adapter struct {
	operations *operationpostgres.Repository
}

var _ deploymentmodule.NativeOperationAuthority = (*Adapter)(nil)
var _ deploymentmodule.NativeBuildOperationAuthority = (*Adapter)(nil)

// Lookup performs a non-locking exact-idempotency read. Native planners use
// it only to bypass remote source inspection for an already-terminal replay;
// AcquireTx remains authoritative for the replay disposition.
func (a *Adapter) Lookup(ctx context.Context, input deploymentmodule.NativeOperationAcquireInput) (deploymentmodule.NativeOperationRecord, bool, error) {
	if a == nil || a.operations == nil {
		return deploymentmodule.NativeOperationRecord{}, false, fmt.Errorf("%w: deployment operation adapter is not configured", deploymentpostgres.ErrInvalid)
	}
	stored, err := a.operations.Get(ctx, input.Scope, input.IdempotencyKey)
	if errors.Is(err, operationpostgres.ErrNotFound) {
		return deploymentmodule.NativeOperationRecord{}, false, nil
	}
	if err != nil {
		return deploymentmodule.NativeOperationRecord{}, false, mapError(err)
	}
	if stored.Scope != input.Scope || stored.OperationType != input.OperationType || stored.IdempotencyKey != input.IdempotencyKey || stored.RequestDigest != input.RequestDigest {
		return deploymentmodule.NativeOperationRecord{}, false, fmt.Errorf("%w: operation identity differs", deploymentmodule.ErrNativeOperationConflict)
	}
	record, err := projectOperationRecord(stored)
	if err != nil {
		return deploymentmodule.NativeOperationRecord{}, false, err
	}
	return record, true, nil
}

// LockOperationTx performs the exact lookup while retaining the platform
// operation row lock in the caller's transaction. It is intentionally a
// narrow recovery seam rather than part of the general mutation authority.
func (a *Adapter) LockOperationTx(ctx context.Context, tx deploymentmodule.NativeOperationTx, input deploymentmodule.NativeOperationAcquireInput) (deploymentmodule.NativeOperationRecord, bool, error) {
	if a == nil || a.operations == nil || tx == nil {
		return deploymentmodule.NativeOperationRecord{}, false, fmt.Errorf("%w: deployment operation lock authority is not configured", deploymentpostgres.ErrInvalid)
	}
	stored, err := a.operations.GetTxForUpdate(ctx, tx, input.Scope, input.IdempotencyKey)
	if errors.Is(err, operationpostgres.ErrNotFound) {
		return deploymentmodule.NativeOperationRecord{}, false, nil
	}
	if err != nil {
		return deploymentmodule.NativeOperationRecord{}, false, mapError(err)
	}
	if stored.Scope != input.Scope || stored.OperationType != input.OperationType || stored.IdempotencyKey != input.IdempotencyKey || stored.RequestDigest != input.RequestDigest || stored.OwnerID != input.OwnerID {
		return deploymentmodule.NativeOperationRecord{}, false, fmt.Errorf("%w: locked operation identity differs", deploymentmodule.ErrNativeOperationConflict)
	}
	record, err := projectOperationRecord(stored)
	if err != nil {
		return deploymentmodule.NativeOperationRecord{}, false, err
	}
	return record, true, nil
}

// New returns an adapter backed by the supplied operation authority.
func New(repository *operationpostgres.Repository) *Adapter {
	return &Adapter{operations: repository}
}

// AcquireTx forwards the exact caller-owned transaction to the operation
// authority and projects every operation and lease field into deployment's
// storage-neutral contract. It never begins, commits, or rolls back tx.
func (a *Adapter) AcquireTx(ctx context.Context, tx deploymentmodule.NativeOperationTx, input deploymentmodule.NativeOperationAcquireInput) (deploymentmodule.NativeOperationAcquireResult, error) {
	if a == nil || a.operations == nil {
		return deploymentmodule.NativeOperationAcquireResult{}, fmt.Errorf("%w: deployment operation adapter is not configured", deploymentpostgres.ErrInvalid)
	}
	if tx == nil {
		return deploymentmodule.NativeOperationAcquireResult{}, fmt.Errorf("%w: deployment operation transaction is required", deploymentpostgres.ErrInvalid)
	}
	result, err := a.operations.AcquireTx(ctx, tx, operationpostgres.AcquireInput{
		Scope: input.Scope, OperationType: input.OperationType, IdempotencyKey: input.IdempotencyKey,
		RequestDigest: input.RequestDigest, OwnerID: input.OwnerID,
	})
	if err != nil {
		return deploymentmodule.NativeOperationAcquireResult{}, mapError(err)
	}
	status, err := mapStatus(result.Status)
	if err != nil {
		return deploymentmodule.NativeOperationAcquireResult{}, err
	}
	if err := validateAcquireResult(result, status, input); err != nil {
		return deploymentmodule.NativeOperationAcquireResult{}, err
	}
	record, err := projectOperationRecord(result.Operation)
	if err != nil {
		return deploymentmodule.NativeOperationAcquireResult{}, err
	}
	var lease deploymentmodule.NativeOperationLease
	if status == deploymentmodule.NativeOperationAcquired {
		lease, err = projectLease(result.Lease)
		if err != nil {
			return deploymentmodule.NativeOperationAcquireResult{}, err
		}
	}
	return deploymentmodule.NativeOperationAcquireResult{Status: status, Operation: record, Lease: lease}, nil
}

func projectOperationRecord(stored operationpostgres.Operation) (deploymentmodule.NativeOperationRecord, error) {
	state, err := mapState(stored.State)
	if err != nil {
		return deploymentmodule.NativeOperationRecord{}, err
	}
	if err := validateUUIDv7(stored.OperationID, "operation id"); err != nil {
		return deploymentmodule.NativeOperationRecord{}, err
	}
	if stored.Scope == "" || stored.Scope != strings.TrimSpace(stored.Scope) || len(stored.Scope) > 255 || stored.OperationType == "" || stored.OperationType != strings.TrimSpace(stored.OperationType) || len(stored.OperationType) > 255 || stored.IdempotencyKey == "" || stored.IdempotencyKey != strings.TrimSpace(stored.IdempotencyKey) || len(stored.IdempotencyKey) > 512 || stored.RequestDigest == "" || stored.RequestDigest != strings.TrimSpace(stored.RequestDigest) || platformdigest.ValidateSHA256Identity(stored.RequestDigest) != nil || stored.OwnerID == "" || stored.OwnerID != strings.TrimSpace(stored.OwnerID) || len(stored.OwnerID) > 255 || stored.FencingGeneration <= 0 || stored.LeaseExpiresAt.IsZero() {
		return deploymentmodule.NativeOperationRecord{}, fmt.Errorf("%w: operation authority returned incomplete operation identity", deploymentmodule.ErrNativeOperationInvalid)
	}
	if (stored.AttemptID == "") != (stored.AttemptIdentity == "") {
		return deploymentmodule.NativeOperationRecord{}, fmt.Errorf("%w: operation authority returned an incomplete attempt identity", deploymentmodule.ErrNativeOperationInvalid)
	}
	if stored.AttemptID != "" {
		if err := validateUUIDv7(stored.AttemptID, "attempt id"); err != nil {
			return deploymentmodule.NativeOperationRecord{}, err
		}
		if stored.AttemptIdentity != strings.TrimSpace(stored.AttemptIdentity) || len(stored.AttemptIdentity) > 512 {
			return deploymentmodule.NativeOperationRecord{}, fmt.Errorf("%w: operation authority returned an invalid attempt identity", deploymentmodule.ErrNativeOperationInvalid)
		}
	}
	outcome, err := canonicalObjectJSON(stored.Outcome, state != deploymentmodule.NativeOperationStatePending)
	if err != nil {
		return deploymentmodule.NativeOperationRecord{}, fmt.Errorf("%w: operation authority returned invalid outcome: %v", deploymentmodule.ErrNativeOperationInvalid, err)
	}
	var attemptEvidence, resolutionEvidence []byte
	if len(stored.AttemptEvidence) > 0 {
		attemptEvidence, err = canonicalObjectJSON(stored.AttemptEvidence, state == deploymentmodule.NativeOperationStateIndeterminate)
		if err != nil {
			return deploymentmodule.NativeOperationRecord{}, fmt.Errorf("%w: operation authority returned invalid attempt evidence: %v", deploymentmodule.ErrNativeOperationInvalid, err)
		}
	}
	if len(stored.ResolutionEvidence) > 0 {
		resolutionEvidence, err = canonicalObjectJSON(stored.ResolutionEvidence, false)
		if err != nil {
			return deploymentmodule.NativeOperationRecord{}, fmt.Errorf("%w: operation authority returned invalid resolution evidence: %v", deploymentmodule.ErrNativeOperationInvalid, err)
		}
	}
	if state == deploymentmodule.NativeOperationStateIndeterminate && (stored.AttemptID == "" || len(attemptEvidence) == 0) {
		return deploymentmodule.NativeOperationRecord{}, fmt.Errorf("%w: indeterminate operation is missing attempt identity or evidence", deploymentmodule.ErrNativeOperationInvalid)
	}
	return deploymentmodule.NativeOperationRecord{
		Scope: stored.Scope, OperationType: stored.OperationType, IdempotencyKey: stored.IdempotencyKey,
		RequestDigest: stored.RequestDigest, OwnerID: stored.OwnerID, OperationID: stored.OperationID,
		State: state, FencingGeneration: stored.FencingGeneration, LeaseExpiresAt: stored.LeaseExpiresAt,
		AttemptID: stored.AttemptID, AttemptIdentity: stored.AttemptIdentity,
		AttemptEvidence: append(json.RawMessage(nil), attemptEvidence...), ResolutionEvidence: append(json.RawMessage(nil), resolutionEvidence...),
		Outcome: append(json.RawMessage(nil), outcome...),
	}, nil
}

func projectLease(stored operationpostgres.Lease) (deploymentmodule.NativeOperationLease, error) {
	lease := deploymentmodule.NativeOperationLease{
		Scope: stored.Scope, IdempotencyKey: stored.IdempotencyKey, OperationID: stored.OperationID,
		OwnerID: stored.OwnerID, FencingGeneration: stored.FencingGeneration, LeaseExpiresAt: stored.LeaseExpiresAt,
		AttemptID: stored.AttemptID, AttemptIdentity: stored.AttemptIdentity,
	}
	if err := validateNativeLease(lease, false); err != nil {
		return deploymentmodule.NativeOperationLease{}, err
	}
	return lease, nil
}

func nativeLease(lease deploymentmodule.NativeOperationLease) operationpostgres.Lease {
	return operationpostgres.Lease{
		Scope: lease.Scope, IdempotencyKey: lease.IdempotencyKey, OperationID: lease.OperationID,
		OwnerID: lease.OwnerID, FencingGeneration: lease.FencingGeneration, LeaseExpiresAt: lease.LeaseExpiresAt,
		AttemptID: lease.AttemptID, AttemptIdentity: lease.AttemptIdentity,
	}
}

func validateNativeLease(lease deploymentmodule.NativeOperationLease, requireAttempt bool) error {
	if lease.Scope == "" || lease.Scope != strings.TrimSpace(lease.Scope) || len(lease.Scope) > 255 || lease.IdempotencyKey == "" || lease.IdempotencyKey != strings.TrimSpace(lease.IdempotencyKey) || len(lease.IdempotencyKey) > 512 || lease.OwnerID == "" || lease.OwnerID != strings.TrimSpace(lease.OwnerID) || len(lease.OwnerID) > 255 || lease.FencingGeneration <= 0 || lease.LeaseExpiresAt.IsZero() {
		return fmt.Errorf("%w: operation lease identity is incomplete", deploymentmodule.ErrNativeOperationInvalid)
	}
	if err := validateUUIDv7(lease.OperationID, "operation id"); err != nil {
		return err
	}
	if (lease.AttemptID == "") != (lease.AttemptIdentity == "") || (requireAttempt && lease.AttemptID == "") {
		return fmt.Errorf("%w: operation attempt identity is incomplete", deploymentmodule.ErrNativeOperationInvalid)
	}
	if lease.AttemptID != "" {
		if err := validateUUIDv7(lease.AttemptID, "attempt id"); err != nil {
			return err
		}
		if lease.AttemptIdentity != strings.TrimSpace(lease.AttemptIdentity) || len(lease.AttemptIdentity) > 512 {
			return fmt.Errorf("%w: operation attempt identity is invalid", deploymentmodule.ErrNativeOperationInvalid)
		}
	}
	return nil
}

func validateUUIDv7(value, label string) error {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value || parsed.Version() != 7 {
		return fmt.Errorf("%w: %s must be a canonical UUIDv7", deploymentmodule.ErrNativeOperationInvalid, label)
	}
	return nil
}

func mapState(state operationpostgres.State) (deploymentmodule.NativeOperationState, error) {
	switch state {
	case operationpostgres.StatePending:
		return deploymentmodule.NativeOperationStatePending, nil
	case operationpostgres.StateCompleted:
		return deploymentmodule.NativeOperationStateCompleted, nil
	case operationpostgres.StateFailed:
		return deploymentmodule.NativeOperationStateFailed, nil
	case operationpostgres.StateIndeterminate:
		return deploymentmodule.NativeOperationStateIndeterminate, nil
	default:
		return "", fmt.Errorf("%w: unknown operation state %q", deploymentmodule.ErrNativeOperationInvalid, state)
	}
}

func canonicalObjectJSON(raw []byte, required bool) ([]byte, error) {
	if len(raw) == 0 {
		if required {
			return nil, errors.New("object JSON is required")
		}
		return nil, nil
	}
	var object map[string]json.RawMessage
	if err := strictjson.DecodeWithOptions(raw, &object, strictjson.Options{MaxBytes: 32768, MaxDepth: 100, DuplicateKeys: strictjson.CaseSensitiveKeys, AllowUnknownFields: true}); err != nil || object == nil {
		if err == nil {
			err = errors.New("JSON value must be an object")
		}
		return nil, err
	}
	if required && len(object) == 0 {
		return nil, errors.New("object JSON must not be empty")
	}
	canonical, err := json.Marshal(object)
	if err != nil || len(canonical) > 32768 {
		if err == nil {
			err = errors.New("object JSON exceeds bounded size")
		}
		return nil, err
	}
	return canonical, nil
}

func validateAcquireResult(result operationpostgres.AcquireResult, status deploymentmodule.NativeOperationStatus, input deploymentmodule.NativeOperationAcquireInput) error {
	operationID, err := uuid.Parse(result.Operation.OperationID)
	if err != nil || operationID.String() != result.Operation.OperationID || operationID.Version() != 7 {
		return fmt.Errorf("%w: operation authority returned a non-UUIDv7 operation identity", deploymentmodule.ErrNativeOperationInvalid)
	}
	if result.Operation.Scope != input.Scope || result.Operation.OperationType != input.OperationType || result.Operation.IdempotencyKey != input.IdempotencyKey || result.Operation.RequestDigest != input.RequestDigest {
		return fmt.Errorf("%w: operation authority returned a mismatched operation identity", deploymentmodule.ErrNativeOperationConflict)
	}
	state, stateErr := mapState(result.Operation.State)
	if stateErr != nil {
		return stateErr
	}
	switch status {
	case deploymentmodule.NativeOperationAcquired, deploymentmodule.NativeOperationBusy:
		if state != deploymentmodule.NativeOperationStatePending {
			return fmt.Errorf("%w: operation authority returned an invalid pending disposition", deploymentmodule.ErrNativeOperationConflict)
		}
	case deploymentmodule.NativeOperationReplay:
		if state != deploymentmodule.NativeOperationStateCompleted && state != deploymentmodule.NativeOperationStateFailed {
			return fmt.Errorf("%w: operation authority returned an invalid replay state", deploymentmodule.ErrNativeOperationConflict)
		}
	case deploymentmodule.NativeOperationIndeterminate:
		if state != deploymentmodule.NativeOperationStateIndeterminate {
			return fmt.Errorf("%w: operation authority returned an invalid indeterminate state", deploymentmodule.ErrNativeOperationConflict)
		}
	}
	if status == deploymentmodule.NativeOperationAcquired {
		if result.Lease.Scope != input.Scope || result.Lease.IdempotencyKey != input.IdempotencyKey || result.Lease.OperationID != result.Operation.OperationID || result.Lease.OwnerID != input.OwnerID || result.Lease.FencingGeneration <= 0 || result.Lease.LeaseExpiresAt.IsZero() {
			return fmt.Errorf("%w: operation authority returned an invalid lease identity", deploymentmodule.ErrNativeOperationInvalid)
		}
		if result.Operation.AttemptID != result.Lease.AttemptID || result.Operation.AttemptIdentity != result.Lease.AttemptIdentity {
			return fmt.Errorf("%w: operation authority returned mismatched operation and lease attempts", deploymentmodule.ErrNativeOperationConflict)
		}
		leaseID, leaseErr := uuid.Parse(result.Lease.OperationID)
		if leaseErr != nil || leaseID.String() != result.Lease.OperationID || leaseID.Version() != 7 {
			return fmt.Errorf("%w: operation authority returned a non-UUIDv7 lease identity", deploymentmodule.ErrNativeOperationInvalid)
		}
		if (result.Lease.AttemptID == "") != (result.Lease.AttemptIdentity == "") {
			return fmt.Errorf("%w: operation authority returned an incomplete attempt identity", deploymentmodule.ErrNativeOperationInvalid)
		}
		if result.Lease.AttemptID != "" {
			attemptID, attemptErr := uuid.Parse(result.Lease.AttemptID)
			if attemptErr != nil || attemptID.String() != result.Lease.AttemptID || attemptID.Version() != 7 {
				return fmt.Errorf("%w: operation authority returned a non-UUIDv7 attempt identity", deploymentmodule.ErrNativeOperationInvalid)
			}
		}
	}
	return nil
}

// CompleteTx forwards the exact lease and outcome projection through the
// caller-owned transaction. No transaction lifecycle method is called here.
func (a *Adapter) CompleteTx(ctx context.Context, tx deploymentmodule.NativeOperationTx, lease deploymentmodule.NativeOperationLease, outcome json.RawMessage) error {
	if a == nil || a.operations == nil {
		return fmt.Errorf("%w: deployment operation adapter is not configured", deploymentpostgres.ErrInvalid)
	}
	if tx == nil {
		return fmt.Errorf("%w: deployment operation transaction is required", deploymentpostgres.ErrInvalid)
	}
	if err := validateNativeLease(lease, false); err != nil {
		return err
	}
	canonical, err := canonicalObjectJSON(outcome, true)
	if err != nil {
		return fmt.Errorf("%w: operation outcome: %v", deploymentmodule.ErrNativeOperationInvalid, err)
	}
	err = a.operations.CompleteTx(ctx, tx, nativeLease(lease), canonical)
	return mapError(err)
}

// BeginAttemptTx binds an external attempt to an operation through the
// caller-owned transaction. No transaction lifecycle method is called here.
func (a *Adapter) BeginAttemptTx(ctx context.Context, tx deploymentmodule.NativeOperationTx, input deploymentmodule.NativeOperationBeginAttemptInput) (deploymentmodule.NativeOperationAttempt, error) {
	if a == nil || a.operations == nil {
		return deploymentmodule.NativeOperationAttempt{}, fmt.Errorf("%w: deployment operation adapter is not configured", deploymentpostgres.ErrInvalid)
	}
	if tx == nil {
		return deploymentmodule.NativeOperationAttempt{}, fmt.Errorf("%w: deployment operation transaction is required", deploymentpostgres.ErrInvalid)
	}
	if err := validateNativeLease(input.Lease, false); err != nil {
		return deploymentmodule.NativeOperationAttempt{}, err
	}
	if input.AttemptID != "" {
		if err := validateUUIDv7(input.AttemptID, "attempt id"); err != nil {
			return deploymentmodule.NativeOperationAttempt{}, err
		}
	}
	if input.AttemptIdentity == "" || input.AttemptIdentity != strings.TrimSpace(input.AttemptIdentity) || len(input.AttemptIdentity) > 512 {
		return deploymentmodule.NativeOperationAttempt{}, fmt.Errorf("%w: attempt identity is invalid", deploymentmodule.ErrNativeOperationInvalid)
	}
	attempt, err := a.operations.BeginAttemptTx(ctx, tx, operationpostgres.BeginAttemptInput{
		Lease: nativeLease(input.Lease), AttemptID: input.AttemptID, AttemptIdentity: input.AttemptIdentity,
	})
	if err != nil {
		return deploymentmodule.NativeOperationAttempt{}, mapError(err)
	}
	lease, err := projectLease(attempt.Lease)
	if err != nil {
		return deploymentmodule.NativeOperationAttempt{}, err
	}
	if attempt.AttemptID == "" || attempt.AttemptID != strings.TrimSpace(attempt.AttemptID) {
		return deploymentmodule.NativeOperationAttempt{}, fmt.Errorf("%w: operation authority returned an invalid attempt id", deploymentmodule.ErrNativeOperationInvalid)
	}
	if err := validateUUIDv7(attempt.AttemptID, "attempt id"); err != nil {
		return deploymentmodule.NativeOperationAttempt{}, err
	}
	if attempt.AttemptIdentity != input.AttemptIdentity {
		return deploymentmodule.NativeOperationAttempt{}, fmt.Errorf("%w: operation authority returned a mismatched attempt identity", deploymentmodule.ErrNativeOperationConflict)
	}
	if lease.Scope != input.Lease.Scope || lease.IdempotencyKey != input.Lease.IdempotencyKey || lease.OperationID != input.Lease.OperationID || lease.OwnerID != input.Lease.OwnerID || lease.FencingGeneration != input.Lease.FencingGeneration || !lease.LeaseExpiresAt.Equal(input.Lease.LeaseExpiresAt) || lease.AttemptID != attempt.AttemptID || lease.AttemptIdentity != attempt.AttemptIdentity {
		return deploymentmodule.NativeOperationAttempt{}, fmt.Errorf("%w: operation authority returned a mismatched attempt lease", deploymentmodule.ErrNativeOperationConflict)
	}
	return deploymentmodule.NativeOperationAttempt{AttemptID: attempt.AttemptID, AttemptIdentity: attempt.AttemptIdentity, Lease: lease}, nil
}

// ReconcileAttemptTx resolves an indeterminate operation through the
// caller-owned transaction. The adapter validates the storage-neutral request,
// canonicalizes JSON, and projects the returned platform operation without
// exposing platform-owned values across the deployment boundary.
func (a *Adapter) ReconcileAttemptTx(ctx context.Context, tx deploymentmodule.NativeOperationTx, input deploymentmodule.NativeOperationReconcileAttemptInput) (deploymentmodule.NativeOperationReconcileAttemptResult, error) {
	if a == nil || a.operations == nil {
		return deploymentmodule.NativeOperationReconcileAttemptResult{}, fmt.Errorf("%w: deployment operation adapter is not configured", deploymentpostgres.ErrInvalid)
	}
	if tx == nil {
		return deploymentmodule.NativeOperationReconcileAttemptResult{}, fmt.Errorf("%w: deployment operation transaction is required", deploymentpostgres.ErrInvalid)
	}
	platformInput, outcome, evidence, err := normalizeReconcileAttemptInput(input)
	if err != nil {
		return deploymentmodule.NativeOperationReconcileAttemptResult{}, err
	}
	stored, err := a.operations.ReconcileAttemptTx(ctx, tx, platformInput)
	if err != nil {
		return deploymentmodule.NativeOperationReconcileAttemptResult{}, mapError(err)
	}
	record, err := projectOperationRecord(stored)
	if err != nil {
		return deploymentmodule.NativeOperationReconcileAttemptResult{}, err
	}
	if err := validateReconcileAttemptResult(record, input, outcome, evidence); err != nil {
		return deploymentmodule.NativeOperationReconcileAttemptResult{}, err
	}
	return deploymentmodule.NativeOperationReconcileAttemptResult{Operation: record}, nil
}

func normalizeReconcileAttemptInput(input deploymentmodule.NativeOperationReconcileAttemptInput) (operationpostgres.ReconcileAttemptInput, []byte, []byte, error) {
	if input.Scope == "" || input.Scope != strings.TrimSpace(input.Scope) || len(input.Scope) > 255 || input.IdempotencyKey == "" || input.IdempotencyKey != strings.TrimSpace(input.IdempotencyKey) || len(input.IdempotencyKey) > 512 {
		return operationpostgres.ReconcileAttemptInput{}, nil, nil, fmt.Errorf("%w: reconciliation operation identity is invalid", deploymentmodule.ErrNativeOperationInvalid)
	}
	if err := validateUUIDv7(input.AttemptID, "attempt id"); err != nil {
		return operationpostgres.ReconcileAttemptInput{}, nil, nil, err
	}
	if input.AttemptIdentity == "" || input.AttemptIdentity != strings.TrimSpace(input.AttemptIdentity) || len(input.AttemptIdentity) > 512 {
		return operationpostgres.ReconcileAttemptInput{}, nil, nil, fmt.Errorf("%w: reconciliation attempt identity is invalid", deploymentmodule.ErrNativeOperationInvalid)
	}
	state, err := reconcileState(input.State)
	if err != nil {
		return operationpostgres.ReconcileAttemptInput{}, nil, nil, err
	}
	// Reconciliation outcomes may be an empty object, matching the platform
	// authority's canonicalObjectJSON semantics, but the request itself must
	// contain an actual JSON value.
	outcome, err := canonicalObjectJSON(input.Outcome, false)
	if err != nil || len(outcome) == 0 {
		if err == nil {
			err = errors.New("object JSON is required")
		}
		return operationpostgres.ReconcileAttemptInput{}, nil, nil, fmt.Errorf("%w: reconciliation outcome: %v", deploymentmodule.ErrNativeOperationInvalid, err)
	}
	evidence, err := canonicalObjectJSON(input.Evidence, true)
	if err != nil {
		return operationpostgres.ReconcileAttemptInput{}, nil, nil, fmt.Errorf("%w: reconciliation evidence: %v", deploymentmodule.ErrNativeOperationInvalid, err)
	}
	return operationpostgres.ReconcileAttemptInput{
		Scope: input.Scope, IdempotencyKey: input.IdempotencyKey, AttemptID: input.AttemptID,
		AttemptIdentity: input.AttemptIdentity, State: state, Outcome: outcome, Evidence: evidence,
	}, outcome, evidence, nil
}

func reconcileState(state deploymentmodule.NativeOperationState) (operationpostgres.State, error) {
	switch state {
	case deploymentmodule.NativeOperationStateCompleted:
		return operationpostgres.StateCompleted, nil
	case deploymentmodule.NativeOperationStateFailed:
		return operationpostgres.StateFailed, nil
	default:
		return "", fmt.Errorf("%w: reconciliation state must be completed or failed", deploymentmodule.ErrNativeOperationInvalid)
	}
}

func validateReconcileAttemptResult(record deploymentmodule.NativeOperationRecord, input deploymentmodule.NativeOperationReconcileAttemptInput, outcome, evidence []byte) error {
	if input.State != deploymentmodule.NativeOperationStateCompleted && input.State != deploymentmodule.NativeOperationStateFailed {
		return fmt.Errorf("%w: reconciliation state must be completed or failed", deploymentmodule.ErrNativeOperationInvalid)
	}
	if record.Scope != input.Scope || record.IdempotencyKey != input.IdempotencyKey || record.AttemptID != input.AttemptID || record.AttemptIdentity != input.AttemptIdentity {
		return fmt.Errorf("%w: operation authority returned a mismatched reconciliation identity", deploymentmodule.ErrNativeOperationConflict)
	}
	if record.State != input.State {
		return fmt.Errorf("%w: operation authority returned a mismatched reconciliation state", deploymentmodule.ErrNativeOperationConflict)
	}
	storedEvidence, err := canonicalObjectJSON(record.ResolutionEvidence, true)
	if err != nil || len(storedEvidence) == 0 || !sameOperationJSON(storedEvidence, evidence) {
		return fmt.Errorf("%w: operation authority returned mismatched reconciliation evidence", deploymentmodule.ErrNativeOperationConflict)
	}
	storedOutcome, err := canonicalObjectJSON(record.Outcome, false)
	if err != nil || !sameOperationJSON(storedOutcome, outcome) {
		return fmt.Errorf("%w: operation authority returned mismatched reconciliation outcome", deploymentmodule.ErrNativeOperationConflict)
	}
	return nil
}

func sameOperationJSON(left, right []byte) bool {
	var leftValue, rightValue any
	leftDecoder := json.NewDecoder(bytes.NewReader(left))
	leftDecoder.UseNumber()
	rightDecoder := json.NewDecoder(bytes.NewReader(right))
	rightDecoder.UseNumber()
	if leftDecoder.Decode(&leftValue) != nil || rightDecoder.Decode(&rightValue) != nil {
		return false
	}
	var trailing any
	if !errors.Is(leftDecoder.Decode(&trailing), io.EOF) || !errors.Is(rightDecoder.Decode(&trailing), io.EOF) {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

// RenewLeaseTx extends an operation lease through the caller-owned
// transaction. A positive duration is required and the platform authority
// enforces the maximum bounded lease window.
func (a *Adapter) RenewLeaseTx(ctx context.Context, tx deploymentmodule.NativeOperationTx, lease deploymentmodule.NativeOperationLease, duration time.Duration) (deploymentmodule.NativeOperationLease, error) {
	if a == nil || a.operations == nil {
		return deploymentmodule.NativeOperationLease{}, fmt.Errorf("%w: deployment operation adapter is not configured", deploymentpostgres.ErrInvalid)
	}
	if tx == nil {
		return deploymentmodule.NativeOperationLease{}, fmt.Errorf("%w: deployment operation transaction is required", deploymentpostgres.ErrInvalid)
	}
	if err := validateNativeLease(lease, false); err != nil {
		return deploymentmodule.NativeOperationLease{}, err
	}
	if duration <= 0 {
		return deploymentmodule.NativeOperationLease{}, fmt.Errorf("%w: lease renewal duration must be positive", deploymentmodule.ErrNativeOperationInvalid)
	}
	renewed, err := a.operations.RenewLeaseTx(ctx, tx, nativeLease(lease), duration)
	if err != nil {
		return deploymentmodule.NativeOperationLease{}, mapError(err)
	}
	projected, err := projectLease(renewed)
	if err != nil {
		return deploymentmodule.NativeOperationLease{}, err
	}
	if projected.Scope != lease.Scope || projected.IdempotencyKey != lease.IdempotencyKey || projected.OperationID != lease.OperationID || projected.OwnerID != lease.OwnerID || projected.FencingGeneration != lease.FencingGeneration || projected.AttemptID != lease.AttemptID || projected.AttemptIdentity != lease.AttemptIdentity || !projected.LeaseExpiresAt.After(lease.LeaseExpiresAt) {
		return deploymentmodule.NativeOperationLease{}, fmt.Errorf("%w: operation authority returned a mismatched renewed lease", deploymentmodule.ErrNativeOperationConflict)
	}
	return projected, nil
}

// FailTx transitions an operation to a deterministic failed terminal state
// through the caller-owned transaction.
func (a *Adapter) FailTx(ctx context.Context, tx deploymentmodule.NativeOperationTx, lease deploymentmodule.NativeOperationLease, outcome json.RawMessage) error {
	if a == nil || a.operations == nil {
		return fmt.Errorf("%w: deployment operation adapter is not configured", deploymentpostgres.ErrInvalid)
	}
	if tx == nil {
		return fmt.Errorf("%w: deployment operation transaction is required", deploymentpostgres.ErrInvalid)
	}
	if err := validateNativeLease(lease, false); err != nil {
		return err
	}
	canonical, err := canonicalObjectJSON(outcome, true)
	if err != nil {
		return fmt.Errorf("%w: operation failure outcome: %v", deploymentmodule.ErrNativeOperationInvalid, err)
	}
	return mapError(a.operations.FailTx(ctx, tx, nativeLease(lease), canonical))
}

// MarkIndeterminateTx records bounded evidence that an external attempt may
// have committed while transitioning the operation to indeterminate.
func (a *Adapter) MarkIndeterminateTx(ctx context.Context, tx deploymentmodule.NativeOperationTx, lease deploymentmodule.NativeOperationLease, evidence json.RawMessage) error {
	if a == nil || a.operations == nil {
		return fmt.Errorf("%w: deployment operation adapter is not configured", deploymentpostgres.ErrInvalid)
	}
	if tx == nil {
		return fmt.Errorf("%w: deployment operation transaction is required", deploymentpostgres.ErrInvalid)
	}
	if err := validateNativeLease(lease, true); err != nil {
		return err
	}
	canonical, err := canonicalObjectJSON(evidence, true)
	if err != nil {
		return fmt.Errorf("%w: operation indeterminate evidence: %v", deploymentmodule.ErrNativeOperationInvalid, err)
	}
	return mapError(a.operations.MarkIndeterminateTx(ctx, tx, nativeLease(lease), canonical))
}

// ExpireAttemptTx settles a bound external attempt whose operation lease has
// expired. Every operation and attempt identity is forwarded through the
// caller-owned transaction; the operation authority fences the row only when
// its pending lease is actually expired. No transaction lifecycle method is
// called here.
func (a *Adapter) ExpireAttemptTx(ctx context.Context, tx deploymentmodule.NativeOperationTx, lease deploymentmodule.NativeOperationLease, evidence json.RawMessage) error {
	if a == nil || a.operations == nil {
		return fmt.Errorf("%w: deployment operation adapter is not configured", deploymentpostgres.ErrInvalid)
	}
	if tx == nil {
		return fmt.Errorf("%w: deployment operation transaction is required", deploymentpostgres.ErrInvalid)
	}
	if err := validateNativeLease(lease, true); err != nil {
		return err
	}
	canonical, err := canonicalObjectJSON(evidence, true)
	if err != nil {
		return fmt.Errorf("%w: operation expiry evidence: %v", deploymentmodule.ErrNativeOperationInvalid, err)
	}
	return mapError(a.operations.ExpireAttemptTx(ctx, tx, nativeLease(lease), canonical))
}

// ConfirmExpiredAttemptTx locks and projects the exact indeterminate
// operation produced by expiry fencing. The expected generation is required
// to be the predecessor lease generation plus one, preventing confirmation of
// a later takeover or any other terminal state. The caller owns tx.
func (a *Adapter) ConfirmExpiredAttemptTx(ctx context.Context, tx deploymentmodule.NativeOperationTx, lease deploymentmodule.NativeOperationLease, expectedFencingGeneration int64) (deploymentmodule.NativeOperationRecord, error) {
	if a == nil || a.operations == nil {
		return deploymentmodule.NativeOperationRecord{}, fmt.Errorf("%w: deployment operation adapter is not configured", deploymentpostgres.ErrInvalid)
	}
	if tx == nil {
		return deploymentmodule.NativeOperationRecord{}, fmt.Errorf("%w: deployment operation transaction is required", deploymentpostgres.ErrInvalid)
	}
	if err := validateNativeLease(lease, true); err != nil {
		return deploymentmodule.NativeOperationRecord{}, err
	}
	if expectedFencingGeneration <= 0 || lease.FencingGeneration == 1<<63-1 || expectedFencingGeneration != lease.FencingGeneration+1 {
		return deploymentmodule.NativeOperationRecord{}, fmt.Errorf("%w: expected expiry fencing generation must be predecessor plus one", deploymentmodule.ErrNativeOperationInvalid)
	}
	stored, err := a.operations.ConfirmExpiredAttemptTx(ctx, tx, nativeLease(lease), expectedFencingGeneration)
	if err != nil {
		return deploymentmodule.NativeOperationRecord{}, mapError(err)
	}
	record, err := projectOperationRecord(stored)
	if err != nil {
		return deploymentmodule.NativeOperationRecord{}, err
	}
	if record.State != deploymentmodule.NativeOperationStateIndeterminate || record.Scope != lease.Scope || record.IdempotencyKey != lease.IdempotencyKey || record.OperationID != lease.OperationID || record.OwnerID != lease.OwnerID || record.FencingGeneration != expectedFencingGeneration || record.AttemptID != lease.AttemptID || record.AttemptIdentity != lease.AttemptIdentity {
		return deploymentmodule.NativeOperationRecord{}, fmt.Errorf("%w: operation authority returned a mismatched expired-attempt confirmation", deploymentmodule.ErrNativeOperationConflict)
	}
	return record, nil
}

func mapStatus(status operationpostgres.AcquireStatus) (deploymentmodule.NativeOperationStatus, error) {
	switch status {
	case operationpostgres.StatusAcquired:
		return deploymentmodule.NativeOperationAcquired, nil
	case operationpostgres.StatusReplay:
		return deploymentmodule.NativeOperationReplay, nil
	case operationpostgres.StatusBusy:
		return deploymentmodule.NativeOperationBusy, nil
	case operationpostgres.StatusIndeterminate:
		return deploymentmodule.NativeOperationIndeterminate, nil
	default:
		return "", fmt.Errorf("%w: unknown operation acquisition status %q", deploymentmodule.ErrNativeOperationInvalid, status)
	}
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, operationpostgres.ErrConflict):
		return fmt.Errorf("%w: %v", deploymentmodule.ErrNativeOperationConflict, err)
	case errors.Is(err, operationpostgres.ErrBusy):
		return fmt.Errorf("%w: %v", deploymentmodule.ErrNativeOperationBusy, err)
	case errors.Is(err, operationpostgres.ErrStaleFence):
		return fmt.Errorf("%w: %v", deploymentmodule.ErrNativeOperationStaleFence, err)
	case errors.Is(err, operationpostgres.ErrLeaseExpired):
		return fmt.Errorf("%w: %v", deploymentmodule.ErrNativeOperationLeaseExpired, err)
	case errors.Is(err, operationpostgres.ErrAlreadyTerminal):
		return fmt.Errorf("%w: %v", deploymentmodule.ErrNativeOperationAlreadyTerminal, err)
	case errors.Is(err, operationpostgres.ErrInvalid):
		return fmt.Errorf("%w: %v", deploymentmodule.ErrNativeOperationInvalid, err)
	case errors.Is(err, operationpostgres.ErrNotFound):
		return fmt.Errorf("%w: %v", deploymentmodule.ErrNativeOperationNotFound, err)
	default:
		return err
	}
}
