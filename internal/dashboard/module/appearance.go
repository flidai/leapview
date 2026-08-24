package module

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	appearancesqlite "github.com/flidai/leapview/internal/dashboard/appearance/sqlite"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func NewAppearanceStore(database *sql.DB) dashboardappearance.Store {
	return appearancesqlite.NewRepository(database)
}

// ApplyAppearancePatches applies only fields explicitly authored in a
// deployment. Omitted fields retain the last UI value; "default" clears a
// stored override so the product default is used.
func ApplyAppearancePatches(ctx context.Context, tx transaction.Transaction, projectID projectgraph.ResourceID, actorID string, encoded map[string]json.RawMessage) error {
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
