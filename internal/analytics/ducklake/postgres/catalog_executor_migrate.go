package postgres

import (
	"context"
	"fmt"
	"strings"

	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
)

// Migrate performs the one explicitly authorized automatic-migration attach,
// then proves the durable catalog format before runtime grants or control
// compatibility can advance.
func (e *SQLCatalogExecutor) Migrate(ctx context.Context, options CatalogMigrationOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if e == nil || e.Exec == nil || options.Mode != CatalogMigrationAutomatic || e.CatalogAdmin == nil || !isSQLIdentifier(e.RuntimeRole) || !validID(options.PhysicalPoolID) || !validID(options.CatalogID) || options.Current.validate() != nil || options.Target.validate() != nil {
		return ErrCatalogExecutor
	}
	if err := e.validateCatalogAdmin(ctx); err != nil {
		return err
	}
	config := ducklake.PostgresCatalogConfig{PhysicalPoolID: options.PhysicalPoolID, DuckLakeSecret: e.DuckLakeSecret, PostgresSecret: e.PostgresSecret, MetadataSchema: ducklake.MetadataSchemaForPool(options.PhysicalPoolID), Mode: ducklake.PostgresCatalogMigrate}
	statements, err := config.MigrationStatements()
	if err != nil {
		return err
	}
	if options.Renew != nil {
		if err := options.Renew(ctx); err != nil {
			return err
		}
	}
	statements = append(statements, `DETACH "lake"`)
	if err := executeCatalogStatements(ctx, e.Exec, statements); err != nil {
		return err
	}
	registration, err := ReadCatalogRegistrationEvidence(ctx, e.CatalogAdmin, config.MetadataSchema)
	if err != nil {
		return fmt.Errorf("read migrated DuckLake catalog registration: %w", err)
	}
	expectedDatabase := strings.TrimSpace(e.CatalogDatabase)
	if expectedDatabase == "" {
		expectedDatabase = DefaultDuckLakeDatabase
	}
	if registration.CatalogDatabase != expectedDatabase || registration.CatalogSchemaVersion != options.Target.CatalogSchemaVersion {
		return fmt.Errorf("%w: migrated catalog registration differs from target", ErrCompatibilityMismatch)
	}
	return ProvisionCatalogRuntimePrivileges(ctx, e.CatalogAdmin, config.MetadataSchema, e.RuntimeRole)
}
