package module

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	appearancepostgres "github.com/flidai/leapview/internal/dashboard/appearance/postgres"
	appearancesqlite "github.com/flidai/leapview/internal/dashboard/appearance/sqlite"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type Appearance = dashboardappearance.Value
type AppearanceRecord = dashboardappearance.Record

func DefaultAppearance() Appearance {
	return dashboardappearance.Default()
}

func ResolveAppearance(value Appearance) Appearance {
	return dashboardappearance.Resolve(value)
}

// NewSQLiteAppearanceStore constructs the local development/evaluation SQLite
// dashboard appearance authority. Native production composition injects the
// validated PostgreSQL repository through NativePersistence instead of
// calling this constructor.
func NewSQLiteAppearanceStore(database *sql.DB) dashboardappearance.Store {
	if database == nil {
		return nil
	}
	return appearancesqlite.NewRepository(database)
}

// NewNativeAppearanceStore accepts the capability-owned native repository.
// SQLite remains explicit through NewSQLiteAppearanceStore for local
// development, evaluation, and tests.
func NewNativeAppearanceStore(repository dashboardappearance.Store) (dashboardappearance.Store, error) {
	if repository == nil {
		return nil, fmt.Errorf("dashboard appearance native store is required")
	}
	if _, ok := repository.(*appearancepostgres.Repository); !ok {
		return nil, fmt.Errorf("dashboard appearance store must be native PostgreSQL")
	}
	return repository, nil
}

// ApplyAppearancePatchesPostgres applies deployment-owned appearance patches
// in a caller-owned pgx transaction. Audit and event appenders remain inside
// the native repository and share this same commit/rollback boundary.
func ApplyAppearancePatchesPostgres(ctx context.Context, repository *appearancepostgres.Repository, tx appearancepostgres.Tx, projectID projectgraph.ResourceID, actorID string, encoded map[string]json.RawMessage) error {
	if repository == nil || tx == nil {
		return fmt.Errorf("dashboard appearance native repository and transaction are required")
	}
	if err := projectID.Validate(); err != nil {
		return fmt.Errorf("project ID: %w", err)
	}
	for dashboardID, raw := range encoded {
		var patch dashboardappearance.Patch
		if err := json.Unmarshal(raw, &patch); err != nil {
			return err
		}
		if patch.Icon == nil && patch.Color == nil {
			continue
		}
		dashboardResourceID, err := projectgraph.NewResourceID(dashboardID)
		if err != nil {
			return fmt.Errorf("dashboard ID: %w", err)
		}
		if _, err := repository.ApplyPatchTx(ctx, tx, dashboardappearance.Key{ProjectID: projectID, DashboardID: dashboardResourceID}, actorID, patch); err != nil {
			return err
		}
	}
	return nil
}

// ApplySQLiteAppearancePatches applies only fields explicitly authored in a
// local SQLite deployment. Omitted fields retain the last UI value; "default"
// clears a stored override so the product default is used. Native production
// deployment composition must use ApplyAppearancePatchesPostgres instead.
func ApplySQLiteAppearancePatches(ctx context.Context, tx transaction.Transaction, projectID projectgraph.ResourceID, actorID string, encoded map[string]json.RawMessage) error {
	if err := projectID.Validate(); err != nil {
		return fmt.Errorf("project ID: %w", err)
	}
	for dashboardID, raw := range encoded {
		var patch dashboardappearance.Patch
		if err := json.Unmarshal(raw, &patch); err != nil {
			return err
		}
		if patch.Icon == nil && patch.Color == nil {
			continue
		}
		dashboardResourceID, err := projectgraph.NewResourceID(dashboardID)
		if err != nil {
			return fmt.Errorf("dashboard ID: %w", err)
		}
		if _, err := appearancesqlite.ApplyPatch(ctx, tx, dashboardappearance.Key{ProjectID: projectID, DashboardID: dashboardResourceID}, actorID, patch); err != nil {
			return err
		}
	}
	return nil
}
