package module

import (
	"context"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// InstallSnapshot installs an already graph-bound immutable snapshot. Runtime
// assembly owns portable manifest decoding and exact ServingIdentity binding.
func InstallSnapshot(ctx context.Context, tx transaction.Transaction, snapshot accesssnapshot.AuthorizationSnapshot) error {
	return accesssqlite.InstallAuthorizationSnapshotTx(ctx, tx, snapshot)
}

func ActivateDashboardPublicationPrincipal(ctx context.Context, tx transaction.Transaction, projectID projectgraph.ResourceID, name string) error {
	return accesssqlite.ActivateDashboardPublicationPrincipalTx(ctx, tx, projectID, name)
}
