package module

// PostgreSQL DuckLake composition surface.
//
// Application composition may need to assemble the native DuckLake control
// ledger and retention worker, but it must not depend on the PostgreSQL
// adapter package directly. Keep the adapter implementation behind this
// analytics-owned module surface while preserving its existing contracts.

import (
	"context"

	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
)

// PostgresDuckLakeRepository is the native DuckLake control-ledger authority
// used by production composition.
type PostgresDuckLakeRepository = ducklakepostgres.Repository

// NewPostgresDuckLakeRepository constructs a control-ledger repository over a
// caller-owned PostgreSQL query surface.
func NewPostgresDuckLakeRepository(db ducklakepostgres.DBTX) *PostgresDuckLakeRepository {
	return ducklakepostgres.New(db)
}

// Native DuckLake retention contracts used by the in-process maintenance
// worker. These aliases keep the adapter's narrow interfaces and request
// identity types intact without exposing the adapter import to app code.
type PostgresDuckLakeRetentionCoordinator = ducklakepostgres.RetentionCoordinator
type PostgresDuckLakeSnapshotOrphanCoordinator = ducklakepostgres.SnapshotOrphanCoordinator
type PostgresDuckLakeSnapshotOrphanMaintenanceRequest = ducklakepostgres.SnapshotOrphanMaintenanceRequest
type PostgresDuckLakeSnapshotCatalogPageScanner = ducklakepostgres.SnapshotCatalogPageScanner
type PostgresDuckLakeRetentionCatalogSession = ducklakepostgres.RetentionCatalogSession
type PostgresDuckLakeRetentionCatalogSessionInput = ducklakepostgres.RetentionCatalogSessionInput
type PostgresDuckLakeRetentionMaintenanceRequest = ducklakepostgres.RetentionMaintenanceRequest

const MaxPostgresDuckLakeSnapshotOrphanScanGrace = ducklakepostgres.MaxSnapshotOrphanScanGrace

// NewPostgresDuckLakeSnapshotCatalogPageScanner constructs the bounded
// PostgreSQL metadata scanner used by retention maintenance.
func NewPostgresDuckLakeSnapshotCatalogPageScanner(pool *platformpostgres.Pool, metadataSchema string) (PostgresDuckLakeSnapshotCatalogPageScanner, error) {
	return ducklakepostgres.NewPostgresSnapshotCatalogPageScanner(pool, metadataSchema)
}

// OpenPostgresDuckLakeRetentionCatalogSession opens one dedicated native
// DuckDB session for a retention pass.
func OpenPostgresDuckLakeRetentionCatalogSession(ctx context.Context, config ducklake.PostgresCatalogMaintenanceSessionConfig, contract ducklake.PostgresCatalogMaintenanceContract) (PostgresDuckLakeRetentionCatalogSession, error) {
	return ducklakepostgres.OpenPostgresRetentionCatalogSession(ctx, config, contract)
}

// SnapshotOrphanScanIDForMaintenance derives the deterministic scan identity
// bound to one retention operation.
func SnapshotOrphanScanIDForMaintenance(maintenanceID, physicalPoolID, catalogID string) string {
	return ducklakepostgres.SnapshotOrphanScanIDForMaintenance(maintenanceID, physicalPoolID, catalogID)
}
