package postgres

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	bindingdb "github.com/flidai/leapview/internal/analytics/connectionbinding/postgres/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
)

// BindingForShareTx pins the exact endpoint/configuration row until the caller
// commits credential activation. Ordinary binding updates need this row's
// conflicting lock even when they do not use the delivery target fence.
func (r *Repository) BindingForShareTx(ctx context.Context, tx pgx.Tx, scope connectionbinding.BindingScope, target connectionbinding.TargetID, connection projectgraph.ResourceID) (connectionbinding.TargetBinding, error) {
	if r == nil || tx == nil {
		return connectionbinding.TargetBinding{}, connectionbinding.ErrProviderUnavailable
	}
	row, err := bindingdb.New(tx).LockCredentialTargetConnectionBinding(ctx, bindingdb.LockCredentialTargetConnectionBindingParams{TargetID: target.String(), ProjectID: scope.ProjectID.String(), Environment: scope.Environment, ConnectionID: connection.String()})
	if errors.Is(err, pgx.ErrNoRows) {
		return connectionbinding.TargetBinding{}, connectionbinding.ErrBindingNotFound
	}
	if err != nil {
		return connectionbinding.TargetBinding{}, err
	}
	return bindingFromRow(row)
}
