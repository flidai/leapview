package deploymentpostgres

import (
	"context"
	"fmt"

	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
)

// lockNativeBuildOperationTx establishes the first lock in the global native
// build order: operation -> target lease -> delivery attempt -> DuckLake
// attempt. It is read-only and accepts only an exact durable identity in one
// of the caller-authorized states.
func lockNativeBuildOperationTx(ctx context.Context, tx deploymentnative.Tx, operations deploymentmodule.NativeBuildOperationAuthority, expected deploymentmodule.NativeOperationRecord, allowed ...deploymentmodule.NativeOperationState) (deploymentmodule.NativeOperationRecord, error) {
	if tx == nil || nativeBuildAuthorityNil(operations) {
		return deploymentmodule.NativeOperationRecord{}, deploymentmodule.ErrDeliveryInputUnavailable
	}
	locked, found, err := operations.LockOperationTx(ctx, tx, deploymentmodule.NativeOperationAcquireInput{
		Scope: expected.Scope, OperationType: expected.OperationType, IdempotencyKey: expected.IdempotencyKey,
		RequestDigest: expected.RequestDigest, OwnerID: expected.OwnerID,
	})
	if err != nil {
		return deploymentmodule.NativeOperationRecord{}, err
	}
	if !found || !sameNativeBuildOperationIdentity(locked, expected) {
		return deploymentmodule.NativeOperationRecord{}, fmt.Errorf("%w: locked native build operation identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	for _, state := range allowed {
		if locked.State == state {
			return locked, nil
		}
	}
	return deploymentmodule.NativeOperationRecord{}, fmt.Errorf("%w: locked native build operation state %q is not recoverable", deploymentdomain.ErrDeliveryConflict, locked.State)
}

func sameNativeBuildOperationIdentity(got, expected deploymentmodule.NativeOperationRecord) bool {
	return got.Scope == expected.Scope && got.OperationType == expected.OperationType && got.IdempotencyKey == expected.IdempotencyKey && got.RequestDigest == expected.RequestDigest && got.OwnerID == expected.OwnerID && got.OperationID == expected.OperationID && got.FencingGeneration == expected.FencingGeneration && got.LeaseExpiresAt.Equal(expected.LeaseExpiresAt) && got.AttemptID == expected.AttemptID && got.AttemptIdentity == expected.AttemptIdentity
}

func lockNativeBuildSettlementOperationTx(ctx context.Context, tx deploymentnative.Tx, operations deploymentmodule.NativeBuildOperationAuthority, lease deploymentmodule.NativeOperationLease, requestDigest string) (deploymentmodule.NativeOperationRecord, error) {
	if tx == nil || nativeBuildAuthorityNil(operations) {
		return deploymentmodule.NativeOperationRecord{}, deploymentmodule.ErrDeliveryInputUnavailable
	}
	locked, found, err := operations.LockOperationTx(ctx, tx, deploymentmodule.NativeOperationAcquireInput{Scope: lease.Scope, OperationType: nativeBuildOperationType, IdempotencyKey: lease.IdempotencyKey, RequestDigest: requestDigest, OwnerID: lease.OwnerID})
	if err != nil {
		return deploymentmodule.NativeOperationRecord{}, err
	}
	if !found || locked.Scope != lease.Scope || locked.OperationType != nativeBuildOperationType || locked.IdempotencyKey != lease.IdempotencyKey || locked.RequestDigest != requestDigest || locked.OwnerID != lease.OwnerID || locked.OperationID != lease.OperationID || !locked.LeaseExpiresAt.Equal(lease.LeaseExpiresAt) || locked.AttemptID != lease.AttemptID || locked.AttemptIdentity != lease.AttemptIdentity {
		return deploymentmodule.NativeOperationRecord{}, fmt.Errorf("%w: locked settlement operation identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	if locked.State == deploymentmodule.NativeOperationStatePending && locked.FencingGeneration == lease.FencingGeneration && len(locked.AttemptEvidence) == 0 {
		return locked, nil
	}
	if locked.State == deploymentmodule.NativeOperationStateIndeterminate && lease.FencingGeneration < 1<<63-1 && locked.FencingGeneration == lease.FencingGeneration+1 {
		canonical, evidenceErr := canonicalTerminationEvidence(locked.AttemptEvidence)
		if evidenceErr == nil && len(canonical) > 0 {
			return locked, nil
		}
	}
	return deploymentmodule.NativeOperationRecord{}, fmt.Errorf("%w: locked settlement operation state differs", deploymentdomain.ErrDeliveryConflict)
}

// lockNativeBuildLeaseTx establishes the second global lock and binds its
// immutable fence. Expiry may have been renewed since the caller's original
// projection, so callers that need an exact deadline validate it separately.
func lockNativeBuildLeaseTx(ctx context.Context, tx deploymentnative.Tx, repository *deploymentnative.Repository, expected deploymentnative.DeliveryLease, allowed ...string) (deploymentnative.DeliveryLease, error) {
	if tx == nil || repository == nil {
		return deploymentnative.DeliveryLease{}, deploymentmodule.ErrDeliveryInputUnavailable
	}
	locked, err := repository.LockLeaseTx(ctx, tx, expected.LeaseID)
	if err != nil {
		return deploymentnative.DeliveryLease{}, err
	}
	if locked.LeaseID != expected.LeaseID || locked.TargetID != expected.TargetID || locked.OwnerID != expected.OwnerID || locked.FencingEpoch != expected.FencingEpoch || !locked.AcquiredAt.Equal(expected.AcquiredAt) {
		return deploymentnative.DeliveryLease{}, fmt.Errorf("%w: locked native build target lease identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	for _, state := range allowed {
		if locked.State == state {
			return locked, nil
		}
	}
	return deploymentnative.DeliveryLease{}, fmt.Errorf("%w: locked native build target lease state %q is not accepted", deploymentdomain.ErrDeliveryConflict, locked.State)
}
