package refreshpostgres

// This adapter is the composition-owned bridge from refresh's narrow
// operation port to the shared platform operation authority. Refresh module
// code never imports the platform repository directly, while production
// composition still injects the graph's exact operation repository identity.

import (
	"context"
	"encoding/json"
	"errors"

	operationpostgres "github.com/flidai/leapview/internal/platform/operation/postgres"
	refreshoperation "github.com/flidai/leapview/internal/refresh/operation"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
)

type PostgresOperationAuthorityAdapter struct {
	Operations *operationpostgres.Repository
}

var _ refreshoperation.Authority = (*PostgresOperationAuthorityAdapter)(nil)

func NewPostgresOperationAuthorityAdapter(operations *operationpostgres.Repository) (*PostgresOperationAuthorityAdapter, error) {
	if operations == nil {
		return nil, errors.New("platform PostgreSQL operation repository is required")
	}
	return &PostgresOperationAuthorityAdapter{Operations: operations}, nil
}

func (a *PostgresOperationAuthorityAdapter) AcquireTx(ctx context.Context, tx refreshoperation.Tx, input refreshoperation.AcquireInput) (refreshoperation.AcquireResult, error) {
	if a == nil || a.Operations == nil {
		return refreshoperation.AcquireResult{}, errors.New("platform PostgreSQL operation repository is required")
	}
	result, err := a.Operations.AcquireTx(ctx, tx, operationpostgres.AcquireInput{
		Scope: input.Scope, OperationType: input.OperationType, IdempotencyKey: input.IdempotencyKey,
		RequestDigest: input.RequestDigest, OwnerID: input.OwnerID, Lease: input.Lease, Retention: input.Retention,
	})
	if err != nil {
		return refreshoperation.AcquireResult{}, mapOperationError(err)
	}
	return refreshoperation.AcquireResult{
		Status:    refreshoperation.Status(result.Status),
		Operation: projectOperation(result.Operation),
		Lease:     projectLease(result.Lease),
		Replay:    result.Replay,
	}, nil
}

func (a *PostgresOperationAuthorityAdapter) CompleteTx(ctx context.Context, tx refreshoperation.Tx, lease refreshoperation.Lease, outcome json.RawMessage) error {
	if a == nil || a.Operations == nil {
		return errors.New("platform PostgreSQL operation repository is required")
	}
	err := a.Operations.CompleteTx(ctx, tx, operationpostgres.Lease{
		Scope: lease.Scope, IdempotencyKey: lease.IdempotencyKey, OperationID: lease.OperationID,
		OwnerID: lease.OwnerID, FencingGeneration: lease.FencingGeneration, LeaseExpiresAt: lease.LeaseExpiresAt,
	}, outcome)
	return mapOperationError(err)
}

func (a *PostgresOperationAuthorityAdapter) Get(ctx context.Context, scope, key string) (refreshoperation.Record, error) {
	if a == nil || a.Operations == nil {
		return refreshoperation.Record{}, errors.New("platform PostgreSQL operation repository is required")
	}
	op, err := a.Operations.Get(ctx, scope, key)
	if err != nil {
		return refreshoperation.Record{}, mapOperationError(err)
	}
	return projectOperation(op), nil
}

func projectOperation(op operationpostgres.Operation) refreshoperation.Record {
	return refreshoperation.Record{
		Scope: op.Scope, OperationType: op.OperationType, IdempotencyKey: op.IdempotencyKey,
		RequestDigest: op.RequestDigest, OperationID: op.OperationID, OwnerID: op.OwnerID,
		State: string(op.State), FencingGeneration: op.FencingGeneration, LeaseExpiresAt: op.LeaseExpiresAt,
		Outcome: append(json.RawMessage(nil), op.Outcome...),
	}
}

func projectLease(lease operationpostgres.Lease) refreshoperation.Lease {
	return refreshoperation.Lease{
		Scope: lease.Scope, IdempotencyKey: lease.IdempotencyKey, OperationID: lease.OperationID,
		OwnerID: lease.OwnerID, FencingGeneration: lease.FencingGeneration, LeaseExpiresAt: lease.LeaseExpiresAt,
	}
}

func mapOperationError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, operationpostgres.ErrConflict):
		return refreshpostgres.ErrConflict
	case errors.Is(err, operationpostgres.ErrNotFound):
		return refreshpostgres.ErrNotFound
	default:
		return err
	}
}
