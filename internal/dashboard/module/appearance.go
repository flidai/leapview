package module

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	"github.com/flidai/leapview/internal/platform/transaction"
)

// ApplyAppearancePatches applies only fields explicitly authored in a
// deployment. Omitted fields retain the last UI value; "default" clears a
// stored override so the product default is used.
func ApplyAppearancePatches(ctx context.Context, tx transaction.Transaction, projectID, workspaceID, actorID string, encoded map[string]json.RawMessage) error {
	for dashboardID, raw := range encoded {
		var patch dashboardappearance.Patch
		if err := json.Unmarshal(raw, &patch); err != nil {
			return err
		}
		if patch.Icon == nil && patch.Color == nil {
			continue
		}
		if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(dashboardID) == "" {
			return fmt.Errorf("workspace and dashboard are required")
		}
		if err := dashboardappearance.ValidatePatch(patch); err != nil {
			return err
		}
		icon, iconPresent := "", patch.Icon != nil
		if patch.Icon != nil {
			icon = dashboardappearance.StoredValue(*patch.Icon)
		}
		color, colorPresent := "", patch.Color != nil
		if patch.Color != nil {
			color = dashboardappearance.StoredValue(*patch.Color)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO dashboard_appearances (workspace_id, dashboard_id, project_id, icon, color, updated_by)
VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?)
ON CONFLICT(workspace_id, dashboard_id) DO UPDATE SET
  project_id = CASE WHEN excluded.project_id <> '' THEN excluded.project_id ELSE dashboard_appearances.project_id END,
  icon = CASE WHEN ? THEN excluded.icon ELSE dashboard_appearances.icon END,
  color = CASE WHEN ? THEN excluded.color ELSE dashboard_appearances.color END,
  revision = dashboard_appearances.revision + 1,
  updated_by = excluded.updated_by,
  updated_at = CURRENT_TIMESTAMP`,
			workspaceID, dashboardID, strings.TrimSpace(projectID), icon, color, strings.TrimSpace(actorID), iconPresent, colorPresent); err != nil {
			return err
		}
	}
	return nil
}
