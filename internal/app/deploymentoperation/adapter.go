// Package deploymentoperation composes deployment's capability-neutral
// operation authority with the platform operation PostgreSQL repository.
package deploymentoperation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	operationpostgres "github.com/flidai/leapview/internal/platform/operation/postgres"
	"github.com/google/uuid"
)

// Adapter is stateless and safe to share between deployment requests.
type Adapter struct {
	operations *operationpostgres.Repository
}

var _ deploymentmodule.NativeOperationAuthority = (*Adapter)(nil)

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
	return deploymentmodule.NativeOperationRecord{
		Scope: stored.Scope, OperationType: stored.OperationType, IdempotencyKey: stored.IdempotencyKey,
		RequestDigest: stored.RequestDigest, OwnerID: stored.OwnerID, OperationID: stored.OperationID,
		Outcome: append(json.RawMessage(nil), stored.Outcome...),
	}, true, nil
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
	return deploymentmodule.NativeOperationAcquireResult{
		Status: status,
		Operation: deploymentmodule.NativeOperationRecord{
			Scope: result.Operation.Scope, OperationType: result.Operation.OperationType,
			IdempotencyKey: result.Operation.IdempotencyKey, RequestDigest: result.Operation.RequestDigest,
			OwnerID: result.Operation.OwnerID, OperationID: result.Operation.OperationID,
			Outcome: append(json.RawMessage(nil), result.Operation.Outcome...),
		},
		Lease: deploymentmodule.NativeOperationLease{
			Scope: result.Lease.Scope, IdempotencyKey: result.Lease.IdempotencyKey,
			OperationID: result.Lease.OperationID, OwnerID: result.Lease.OwnerID,
			FencingGeneration: result.Lease.FencingGeneration, LeaseExpiresAt: result.Lease.LeaseExpiresAt,
			AttemptID: result.Lease.AttemptID, AttemptIdentity: result.Lease.AttemptIdentity,
		},
	}, nil
}

func validateAcquireResult(result operationpostgres.AcquireResult, status deploymentmodule.NativeOperationStatus, input deploymentmodule.NativeOperationAcquireInput) error {
	operationID, err := uuid.Parse(result.Operation.OperationID)
	if err != nil || operationID.String() != result.Operation.OperationID || operationID.Version() != 7 {
		return fmt.Errorf("%w: operation authority returned a non-UUIDv7 operation identity", deploymentmodule.ErrNativeOperationInvalid)
	}
	if result.Operation.Scope != input.Scope || result.Operation.OperationType != input.OperationType || result.Operation.IdempotencyKey != input.IdempotencyKey || result.Operation.RequestDigest != input.RequestDigest {
		return fmt.Errorf("%w: operation authority returned a mismatched operation identity", deploymentmodule.ErrNativeOperationConflict)
	}
	if status == deploymentmodule.NativeOperationAcquired {
		if result.Lease.Scope != input.Scope || result.Lease.IdempotencyKey != input.IdempotencyKey || result.Lease.OperationID != result.Operation.OperationID || result.Lease.OwnerID != input.OwnerID || result.Lease.FencingGeneration <= 0 || result.Lease.LeaseExpiresAt.IsZero() {
			return fmt.Errorf("%w: operation authority returned an invalid lease identity", deploymentmodule.ErrNativeOperationInvalid)
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
	err := a.operations.CompleteTx(ctx, tx, operationpostgres.Lease{
		Scope: lease.Scope, IdempotencyKey: lease.IdempotencyKey, OperationID: lease.OperationID,
		OwnerID: lease.OwnerID, FencingGeneration: lease.FencingGeneration,
		LeaseExpiresAt: lease.LeaseExpiresAt, AttemptID: lease.AttemptID, AttemptIdentity: lease.AttemptIdentity,
	}, append(json.RawMessage(nil), outcome...))
	return mapError(err)
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
