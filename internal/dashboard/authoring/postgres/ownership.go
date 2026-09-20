package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	dashboarddb "github.com/flidai/leapview/internal/dashboard/authoring/postgres/internal/db"
	"github.com/flidai/leapview/internal/platform/accesslifecycle"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// ListOwnedObjects reports active dashboard authoring roots. Revisions, drafts,
// compiled revisions, and published pointers are children of the dashboard
// root and are intentionally not repeated as separate ownership records.
// Archived roots are historical tombstones and therefore do not block identity
// offboarding.
func (r *Repository) ListOwnedObjects(ctx context.Context, principalID string) (access.OwnershipReport, error) {
	principalID = strings.TrimSpace(principalID)
	ownerID, err := nativeUUID(principalID)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	principalID = uuid.UUID(ownerID.Bytes).String()
	if r == nil || r.db == nil {
		return access.OwnershipReport{}, fmt.Errorf("dashboard authoring ownership authority is unavailable")
	}
	rows, err := dashboarddb.New(r.db).ListOwnedDashboards(ctx, ownerID)
	if err != nil {
		return access.OwnershipReport{}, fmt.Errorf("list principal-owned dashboards: %w", err)
	}
	objects := make([]access.OwnedObject, 0, len(rows))
	for _, row := range rows {
		if row.OwnerPrincipalID != principalID || strings.TrimSpace(row.DashboardID) == "" {
			return access.OwnershipReport{}, fmt.Errorf("dashboard ownership authority returned an inconsistent owner")
		}
		objects = append(objects, access.OwnedObject{
			Kind: "dashboard", ID: row.ProjectID + ":" + row.DashboardID,
			Name: row.Title, OwnerPrincipalID: principalID, Lifecycle: row.Status,
			Transferable: true, TombstoneOnOffboard: true,
		})
	}
	return access.OwnershipReport{PrincipalID: principalID, Objects: objects}, nil
}

func (r *Repository) WithOwnershipDB(db access.OwnershipDBTX) access.OwnershipAuthority {
	return &Repository{db: db}
}

// WithOwnershipMutationDB binds the explicit offboarding lifecycle adapter to
// the caller-owned transaction. No transaction boundary is opened here.
func (r *Repository) WithOwnershipMutationDB(db access.OwnershipDBTX) access.OwnershipMutator {
	return &Repository{db: db}
}

// TransferOwnedObjects reassigns all non-archived dashboard roots from one
// principal to another. Child revisions, drafts, compiled revisions, and
// publication pointers remain attached to the stable dashboard identity.
// The operation is idempotent: a retry sees no rows still owned by the source.
func (r *Repository) TransferOwnedObjects(ctx context.Context, principalID, targetPrincipalID string) (access.OwnershipReport, error) {
	ownerID, owner, target, err := ownershipPrincipalIDs(principalID, targetPrincipalID)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	return r.withOwnershipTx(ctx, func(tx dashboarddb.DBTX) (access.OwnershipReport, error) {
		// Direct ownership adapters are callable outside access' aggregate
		// repository, so they must establish the same source/target lifecycle
		// boundary themselves. Access' outer transaction may already hold the
		// authority lock; PostgreSQL advisory xact locks are re-entrant.
		if err := accesslifecycle.LockOwnedPrincipal(ctx, tx, owner); err != nil {
			return access.OwnershipReport{}, err
		}
		if err := accesslifecycle.LockOwnedPrincipal(ctx, tx, targetPrincipalID); err != nil {
			return access.OwnershipReport{}, err
		}
		rows, err := dashboarddb.New(tx).TransferOwnedDashboards(ctx, dashboarddb.TransferOwnedDashboardsParams{OwnerPrincipalID: ownerID, TargetPrincipalID: target})
		if err != nil {
			return access.OwnershipReport{}, fmt.Errorf("transfer principal-owned dashboards: %w", err)
		}
		return dashboardOwnershipReport(owner, rows), nil
	})
}

// TombstoneOwnedObjects archives all non-archived dashboard roots. Archiving
// is the dashboard author's durable tombstone and retains every revision and
// publication record for audit and recovery.
func (r *Repository) TombstoneOwnedObjects(ctx context.Context, principalID string) (access.OwnershipReport, error) {
	ownerID, owner, _, err := ownershipPrincipalIDs(principalID, "")
	if err != nil {
		return access.OwnershipReport{}, err
	}
	return r.withOwnershipTx(ctx, func(tx dashboarddb.DBTX) (access.OwnershipReport, error) {
		if err := accesslifecycle.LockOwnedPrincipal(ctx, tx, owner); err != nil {
			return access.OwnershipReport{}, err
		}
		rows, err := dashboarddb.New(tx).TombstoneOwnedDashboards(ctx, ownerID)
		if err != nil {
			return access.OwnershipReport{}, fmt.Errorf("tombstone principal-owned dashboards: %w", err)
		}
		objects := make([]access.OwnedObject, 0, len(rows))
		for _, row := range rows {
			objects = append(objects, access.OwnedObject{Kind: "dashboard", ID: row.ProjectID + ":" + row.DashboardID, Name: row.Title, OwnerPrincipalID: owner, Lifecycle: row.Status, Transferable: true, TombstoneOnOffboard: true})
		}
		return access.OwnershipReport{PrincipalID: owner, Objects: objects}, nil
	})
}

func ownershipPrincipalIDs(principalID, targetPrincipalID string) (pgtype.UUID, string, pgtype.UUID, error) {
	owner, err := nativeUUID(strings.TrimSpace(principalID))
	if err != nil || !owner.Valid {
		if err == nil {
			err = fmt.Errorf("principal id must be a canonical UUID")
		}
		return pgtype.UUID{}, "", pgtype.UUID{}, fmt.Errorf("principal id: %w", err)
	}
	ownerText := uuid.UUID(owner.Bytes).String()
	if strings.TrimSpace(targetPrincipalID) == "" {
		return owner, ownerText, pgtype.UUID{}, nil
	}
	target, err := nativeUUID(strings.TrimSpace(targetPrincipalID))
	if err != nil || !target.Valid {
		if err == nil {
			err = fmt.Errorf("target principal id must be a canonical UUID")
		}
		return pgtype.UUID{}, "", pgtype.UUID{}, fmt.Errorf("target principal id: %w", err)
	}
	targetText := uuid.UUID(target.Bytes).String()
	if targetText == ownerText {
		return pgtype.UUID{}, "", pgtype.UUID{}, fmt.Errorf("principal and target principal ids must differ")
	}
	return owner, ownerText, target, nil
}

func dashboardOwnershipReport(owner string, rows []dashboarddb.TransferOwnedDashboardsRow) access.OwnershipReport {
	objects := make([]access.OwnedObject, 0, len(rows))
	for _, row := range rows {
		objects = append(objects, access.OwnedObject{Kind: "dashboard", ID: row.ProjectID + ":" + row.DashboardID, Name: row.Title, OwnerPrincipalID: owner, Lifecycle: row.Status, Transferable: true, TombstoneOnOffboard: true})
	}
	return access.OwnershipReport{PrincipalID: owner, Objects: objects}
}

func (r *Repository) withOwnershipTx(ctx context.Context, fn func(dashboarddb.DBTX) (access.OwnershipReport, error)) (access.OwnershipReport, error) {
	if r == nil || r.db == nil {
		return access.OwnershipReport{}, fmt.Errorf("dashboard authoring ownership authority is unavailable")
	}
	if tx, ok := r.db.(Tx); ok {
		return fn(tx)
	}
	tx, err := r.beginTx(ctx)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	defer tx.Rollback(ctx)
	report, err := fn(tx)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return access.OwnershipReport{}, err
	}
	return report, nil
}

var _ access.OwnershipAuthority = (*Repository)(nil)
var _ access.TransactionalOwnershipAuthority = (*Repository)(nil)
var _ access.OwnershipMutator = (*Repository)(nil)
var _ access.TransactionalOwnershipMutator = (*Repository)(nil)
