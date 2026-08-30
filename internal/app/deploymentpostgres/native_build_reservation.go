package deploymentpostgres

// This file owns the first, deliberately small BuildPlan coordinator slice:
// reserving the operation lease.  Reservation does not inspect a plan,
// access an artifact, or bind an external attempt.  Those actions happen in
// later phases after the caller has retained this value-only lease.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	deployment "github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
)

const nativeBuildReservationMaxLease = 24 * time.Hour

// NativeBuildOperationReservationInput binds one BuildPlan command to its
// operation key. OwnerID is a command-attempt identity, not the authenticated
// principal; callers must retain and reuse it when reacquiring a pending
// operation. RequestDigest must be the exact digest produced by
// nativeBuildRequestDigest.
type NativeBuildOperationReservationInput struct {
	Request       deploymentmodule.NativeDeliveryBuildRequest
	RequestDigest string
	OwnerID       string
	LeaseDuration time.Duration
}

// NativeBuildOperationReservationResult is safe to retain across the
// artifact-build hand-off. Replay, busy, and indeterminate dispositions carry
// no executable lease and are never renewed by the reserver.
type NativeBuildOperationReservationResult struct {
	Disposition deploymentmodule.NativeOperationStatus
	Operation   deploymentmodule.NativeOperationRecord
	Lease       deploymentmodule.NativeOperationLease
}

// ReserveNativeBuildOperation opens one delivery PostgreSQL transaction,
// acquires the exact delivery.plan.build operation, renews only an acquired
// lease to the requested bounded duration, and commits the result. It never
// starts an external attempt or performs artifact I/O. The caller owns the
// returned value; no repository or transaction escapes this function.
func ReserveNativeBuildOperation(ctx context.Context, repository *deploymentnative.Repository, operations deploymentmodule.NativeBuildOperationAuthority, input NativeBuildOperationReservationInput) (NativeBuildOperationReservationResult, error) {
	if repository == nil || !repository.Configured() || !repository.TransactionCapable() {
		return NativeBuildOperationReservationResult{}, fmt.Errorf("%w: native build operation reservation requires a configured transaction-capable delivery authority", deploymentmodule.ErrDeliveryInputUnavailable)
	}
	if nativeBuildOperationAuthorityIsNil(operations) {
		return NativeBuildOperationReservationResult{}, fmt.Errorf("%w: native build operation authority is required", deploymentmodule.ErrDeliveryInputUnavailable)
	}
	ctx = contextOrBackground(ctx)
	operationInput, err := normalizeNativeBuildReservationInput(input)
	if err != nil {
		return NativeBuildOperationReservationResult{}, err
	}

	tx, err := repository.Begin(ctx)
	if err != nil {
		return NativeBuildOperationReservationResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()

	acquired, acquireErr := operations.AcquireTx(ctx, tx, operationInput)
	if acquireErr != nil {
		// The platform authority reports a busy row as an error and may not
		// return a usable projection. Preserve that sentinel and avoid renewal.
		disposition := acquired.Status
		if errors.Is(acquireErr, deploymentmodule.ErrNativeOperationBusy) {
			disposition = deploymentmodule.NativeOperationBusy
		}
		return NativeBuildOperationReservationResult{Disposition: disposition, Operation: acquired.Operation}, acquireErr
	}
	result, err := validateNativeBuildReservationAcquisition(acquired, operationInput)
	if err != nil {
		return NativeBuildOperationReservationResult{}, err
	}
	if result.Disposition == deploymentmodule.NativeOperationAcquired {
		// Fresh reservation, same-owner reacquisition, and no-attempt takeover
		// all arrive here with an empty attempt identity. The authority itself
		// owns the expiry/fence transition; this call only extends its lease.
		renewed, renewErr := operations.RenewLeaseTx(ctx, tx, result.Lease, input.LeaseDuration)
		if renewErr != nil {
			return NativeBuildOperationReservationResult{}, renewErr
		}
		if err := validateNativeBuildRenewedLease(renewed, result.Lease); err != nil {
			return NativeBuildOperationReservationResult{}, err
		}
		result.Lease = renewed
		result.Operation.LeaseExpiresAt = renewed.LeaseExpiresAt
	}

	if err := tx.Commit(ctx); err != nil {
		return NativeBuildOperationReservationResult{}, err
	}
	committed = true
	if result.Disposition == deploymentmodule.NativeOperationBusy {
		// Busy is an expected acquisition disposition, but callers must not
		// mistake it for a usable reservation. Return the typed sentinel even
		// when an authority supplied a complete busy projection.
		return result, fmt.Errorf("%w: build operation is owned by another worker", deploymentmodule.ErrNativeOperationBusy)
	}
	return result, nil
}

func normalizeNativeBuildReservationInput(input NativeBuildOperationReservationInput) (deploymentmodule.NativeOperationAcquireInput, error) {
	if err := validateNativeBuildRequest(input.Request); err != nil {
		return deploymentmodule.NativeOperationAcquireInput{}, err
	}
	if err := platformdigest.ValidateSHA256Identity(input.RequestDigest); err != nil {
		return deploymentmodule.NativeOperationAcquireInput{}, fmt.Errorf("%w: build request digest: %v", deployment.ErrDeliveryInvalid, err)
	}
	expectedDigest, err := nativeBuildRequestDigest(input.Request)
	if err != nil {
		return deploymentmodule.NativeOperationAcquireInput{}, err
	}
	if input.RequestDigest != expectedDigest {
		return deploymentmodule.NativeOperationAcquireInput{}, fmt.Errorf("%w: build request digest differs from canonical request", deployment.ErrDeliveryConflict)
	}
	if _, err := canonicalUUIDv7(input.OwnerID); err != nil {
		return deploymentmodule.NativeOperationAcquireInput{}, fmt.Errorf("%w: build operation owner identity: %v", deployment.ErrDeliveryInvalid, err)
	}
	if input.LeaseDuration < time.Microsecond || input.LeaseDuration > nativeBuildReservationMaxLease {
		return deploymentmodule.NativeOperationAcquireInput{}, fmt.Errorf("%w: build operation lease duration must be at least 1us and at most 24h", deployment.ErrDeliveryInvalid)
	}
	return deploymentmodule.NativeOperationAcquireInput{
		Scope: input.Request.TargetID, OperationType: nativeBuildOperationType,
		IdempotencyKey: input.Request.IdempotencyKey, RequestDigest: input.RequestDigest,
		OwnerID: input.OwnerID,
	}, nil
}

func nativeBuildOperationAuthorityIsNil(authority deploymentmodule.NativeBuildOperationAuthority) bool {
	if authority == nil {
		return true
	}
	value := reflect.ValueOf(authority)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func validateNativeBuildReservationAcquisition(acquired deploymentmodule.NativeOperationAcquireResult, input deploymentmodule.NativeOperationAcquireInput) (NativeBuildOperationReservationResult, error) {
	if acquired.Status != deploymentmodule.NativeOperationAcquired && acquired.Status != deploymentmodule.NativeOperationReplay && acquired.Status != deploymentmodule.NativeOperationBusy && acquired.Status != deploymentmodule.NativeOperationIndeterminate {
		return NativeBuildOperationReservationResult{}, fmt.Errorf("%w: unknown native build operation disposition %q", deployment.ErrDeliveryConflict, acquired.Status)
	}
	if err := validateNativeBuildReservationOperation(acquired.Operation, input); err != nil {
		return NativeBuildOperationReservationResult{}, err
	}
	result := NativeBuildOperationReservationResult{Disposition: acquired.Status, Operation: acquired.Operation}
	switch acquired.Status {
	case deploymentmodule.NativeOperationAcquired:
		if acquired.Operation.State != deploymentmodule.NativeOperationStatePending {
			return NativeBuildOperationReservationResult{}, fmt.Errorf("%w: acquired build operation is not pending", deployment.ErrDeliveryConflict)
		}
		if acquired.Operation.OwnerID != input.OwnerID {
			return NativeBuildOperationReservationResult{}, fmt.Errorf("%w: acquired build operation owner differs from request", deployment.ErrDeliveryConflict)
		}
		if acquired.Operation.AttemptID != "" || acquired.Operation.AttemptIdentity != "" {
			return NativeBuildOperationReservationResult{}, fmt.Errorf("%w: build operation already has an external attempt", deployment.ErrDeliveryConflict)
		}
		if err := validateNativeBuildReservationLease(acquired.Lease, acquired.Operation, input); err != nil {
			return NativeBuildOperationReservationResult{}, err
		}
		result.Lease = acquired.Lease
	case deploymentmodule.NativeOperationReplay:
		if acquired.Operation.State != deploymentmodule.NativeOperationStateCompleted && acquired.Operation.State != deploymentmodule.NativeOperationStateFailed {
			return NativeBuildOperationReservationResult{}, fmt.Errorf("%w: replay build operation is not terminal", deployment.ErrDeliveryConflict)
		}
		if !nativeBuildLeaseIsZero(acquired.Lease) {
			return NativeBuildOperationReservationResult{}, fmt.Errorf("%w: replay build operation returned an executable lease", deployment.ErrDeliveryConflict)
		}
	case deploymentmodule.NativeOperationBusy:
		if acquired.Operation.State != deploymentmodule.NativeOperationStatePending {
			return NativeBuildOperationReservationResult{}, fmt.Errorf("%w: busy build operation is not pending", deployment.ErrDeliveryConflict)
		}
		if !nativeBuildLeaseIsZero(acquired.Lease) {
			return NativeBuildOperationReservationResult{}, fmt.Errorf("%w: busy build operation returned an executable lease", deployment.ErrDeliveryConflict)
		}
	case deploymentmodule.NativeOperationIndeterminate:
		if acquired.Operation.State != deploymentmodule.NativeOperationStateIndeterminate {
			return NativeBuildOperationReservationResult{}, fmt.Errorf("%w: indeterminate build operation has an invalid state", deployment.ErrDeliveryConflict)
		}
		if acquired.Operation.AttemptID == "" || acquired.Operation.AttemptIdentity == "" {
			return NativeBuildOperationReservationResult{}, fmt.Errorf("%w: indeterminate build operation has no attempt evidence", deployment.ErrDeliveryConflict)
		}
		if !nativeBuildLeaseIsZero(acquired.Lease) {
			return NativeBuildOperationReservationResult{}, fmt.Errorf("%w: indeterminate build operation returned an executable lease", deployment.ErrDeliveryConflict)
		}
	}
	return result, nil
}

func validateNativeBuildReservationOperation(operation deploymentmodule.NativeOperationRecord, input deploymentmodule.NativeOperationAcquireInput) error {
	if operation.Scope != input.Scope || operation.OperationType != nativeBuildOperationType || operation.IdempotencyKey != input.IdempotencyKey || operation.RequestDigest != input.RequestDigest {
		return fmt.Errorf("%w: build operation scope, key, type, or request digest differs", deployment.ErrDeliveryConflict)
	}
	if _, err := canonicalUUIDv7(operation.OperationID); err != nil {
		return fmt.Errorf("%w: build operation identity: %v", deployment.ErrDeliveryConflict, err)
	}
	if _, err := canonicalUUIDv7(operation.OwnerID); err != nil {
		return fmt.Errorf("%w: build operation owner identity: %v", deployment.ErrDeliveryConflict, err)
	}
	if operation.FencingGeneration <= 0 || operation.LeaseExpiresAt.IsZero() || !operation.LeaseExpiresAt.Equal(operation.LeaseExpiresAt.UTC()) {
		return fmt.Errorf("%w: build operation fence or lease is incomplete", deployment.ErrDeliveryConflict)
	}
	if (operation.AttemptID == "") != (operation.AttemptIdentity == "") {
		return fmt.Errorf("%w: build operation attempt identity is incomplete", deployment.ErrDeliveryConflict)
	}
	if operation.AttemptID != "" {
		if _, err := canonicalUUIDv7(operation.AttemptID); err != nil {
			return fmt.Errorf("%w: build operation attempt identity: %v", deployment.ErrDeliveryConflict, err)
		}
		if operation.AttemptIdentity == "" || operation.AttemptIdentity != strings.TrimSpace(operation.AttemptIdentity) || len(operation.AttemptIdentity) > 512 {
			return fmt.Errorf("%w: build operation attempt identity is noncanonical", deployment.ErrDeliveryConflict)
		}
	}
	return nil
}

func validateNativeBuildReservationLease(lease deploymentmodule.NativeOperationLease, operation deploymentmodule.NativeOperationRecord, input deploymentmodule.NativeOperationAcquireInput) error {
	if lease.Scope != input.Scope || lease.IdempotencyKey != input.IdempotencyKey || lease.OperationID != operation.OperationID || lease.OwnerID != operation.OwnerID || lease.FencingGeneration != operation.FencingGeneration || !lease.LeaseExpiresAt.Equal(operation.LeaseExpiresAt) {
		return fmt.Errorf("%w: acquired build operation lease differs from operation", deployment.ErrDeliveryConflict)
	}
	if lease.LeaseExpiresAt.IsZero() || !lease.LeaseExpiresAt.Equal(lease.LeaseExpiresAt.UTC()) {
		return fmt.Errorf("%w: acquired build operation lease is incomplete", deployment.ErrDeliveryConflict)
	}
	if _, err := canonicalUUIDv7(lease.OperationID); err != nil {
		return fmt.Errorf("%w: acquired build lease identity: %v", deployment.ErrDeliveryConflict, err)
	}
	if _, err := canonicalUUIDv7(lease.OwnerID); err != nil {
		return fmt.Errorf("%w: acquired build lease owner identity: %v", deployment.ErrDeliveryConflict, err)
	}
	if lease.FencingGeneration <= 0 || lease.AttemptID != "" || lease.AttemptIdentity != "" {
		return fmt.Errorf("%w: acquired build lease fence or attempt identity is invalid", deployment.ErrDeliveryConflict)
	}
	return nil
}

func validateNativeBuildRenewedLease(renewed, previous deploymentmodule.NativeOperationLease) error {
	if renewed.Scope != previous.Scope || renewed.IdempotencyKey != previous.IdempotencyKey || renewed.OperationID != previous.OperationID || renewed.OwnerID != previous.OwnerID || renewed.FencingGeneration != previous.FencingGeneration || renewed.AttemptID != previous.AttemptID || renewed.AttemptIdentity != previous.AttemptIdentity {
		return fmt.Errorf("%w: renewed build operation lease identity differs", deployment.ErrDeliveryConflict)
	}
	if renewed.LeaseExpiresAt.IsZero() || !renewed.LeaseExpiresAt.Equal(renewed.LeaseExpiresAt.UTC()) || !renewed.LeaseExpiresAt.After(previous.LeaseExpiresAt) {
		return fmt.Errorf("%w: renewed build operation lease did not advance", deployment.ErrDeliveryConflict)
	}
	if _, err := canonicalUUIDv7(renewed.OperationID); err != nil {
		return fmt.Errorf("%w: renewed build lease identity: %v", deployment.ErrDeliveryConflict, err)
	}
	if _, err := canonicalUUIDv7(renewed.OwnerID); err != nil {
		return fmt.Errorf("%w: renewed build lease owner identity: %v", deployment.ErrDeliveryConflict, err)
	}
	if renewed.FencingGeneration <= 0 {
		return fmt.Errorf("%w: renewed build lease fence is incomplete", deployment.ErrDeliveryConflict)
	}
	return nil
}

func nativeBuildLeaseIsZero(lease deploymentmodule.NativeOperationLease) bool {
	return lease.Scope == "" && lease.IdempotencyKey == "" && lease.OperationID == "" && lease.OwnerID == "" && lease.FencingGeneration == 0 && lease.LeaseExpiresAt.IsZero() && lease.AttemptID == "" && lease.AttemptIdentity == ""
}
