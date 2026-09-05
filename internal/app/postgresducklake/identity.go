package postgresducklake

// This file is the application-owned composition seam for validating the
// separately authenticated DuckLake PostgreSQL runtime pool.  The underlying
// identity query remains implemented by the analytics PostgreSQL adapter, but
// process composition depends only on this narrow app-owned surface.

import (
	"context"

	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
)

// DatabaseIdentity is authoritative database/login/session-role evidence
// returned by PostgreSQL's identity probe.
type DatabaseIdentity = ducklakepostgres.DatabaseIdentity

// DBTX is the minimal native pgx query surface required by the identity probe.
// Pools, connections, and caller-owned transactions satisfy it.
type DBTX = ducklakepostgres.DBTX

const DefaultDuckLakeDatabase = ducklakepostgres.DefaultDuckLakeDatabase

var ErrWrongDatabaseCredential = ducklakepostgres.ErrWrongDatabaseCredential

// ReadDatabaseIdentity reads the PostgreSQL identity evidence used by app
// composition before admitting a DuckLake runtime pool.
func ReadDatabaseIdentity(ctx context.Context, db DBTX) (DatabaseIdentity, error) {
	return ducklakepostgres.ReadDatabaseIdentity(ctx, db)
}

// ValidateDatabaseIdentity rejects wrong-database or swapped-credential
// connections before serving composition proceeds.
func ValidateDatabaseIdentity(identity DatabaseIdentity, expectedDatabase, expectedRole string) error {
	return ducklakepostgres.ValidateDatabaseIdentity(identity, expectedDatabase, expectedRole)
}
