package module

import (
	"context"
	"errors"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// ActivateDashboardPublicationPrincipal is the narrow local SQLite activation
// port retained for the development/evaluation composition. Native production
// composition uses ActivateDashboardPublicationThroughPersistence instead,
// which dispatches through the configured authority bundle.
func ActivateDashboardPublicationPrincipal(ctx context.Context, tx transaction.Transaction, projectID projectgraph.ResourceID, name string) error {
	return accesssqlite.ActivateDashboardPublicationPrincipalTx(ctx, tx, projectID, name)
}

// InstallSnapshotThroughPersistence dispatches immutable authorization
// installation through the configured capability port. It fails closed when
// composition omitted the port instead of selecting a dialect implicitly.
func (m *Module) InstallSnapshotThroughPersistence(ctx context.Context, tx any, snapshot accesssnapshot.AuthorizationSnapshot) error {
	if m == nil || m.persistence == nil || m.persistence.Snapshot == nil {
		return errors.New("authorization snapshot installer is not configured")
	}
	return m.persistence.Snapshot.InstallSnapshot(ctx, tx, snapshot)
}

func (m *Module) ActivateDashboardPublicationThroughPersistence(ctx context.Context, tx any, projectID projectgraph.ResourceID, name string) error {
	if m == nil || m.persistence == nil || m.persistence.Publication == nil {
		return errors.New("dashboard publication activator is not configured")
	}
	return m.persistence.Publication.ActivateDashboardPublicationPrincipal(ctx, tx, projectID, name)
}
