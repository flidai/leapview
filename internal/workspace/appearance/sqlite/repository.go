package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
)

type Repository struct{ db *sql.DB }

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

func (r *Repository) List(ctx context.Context) (map[dashboardappearance.Key]dashboardappearance.Record, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT workspace_id, dashboard_id, project_id, COALESCE(icon, ''), COALESCE(color, ''), revision
FROM dashboard_appearances`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[dashboardappearance.Key]dashboardappearance.Record{}
	for rows.Next() {
		var row dashboardappearance.Record
		if err := rows.Scan(&row.WorkspaceID, &row.DashboardID, &row.ProjectID, &row.Icon, &row.Color, &row.Revision); err != nil {
			return nil, err
		}
		out[row.Key] = row
	}
	return out, rows.Err()
}

func (r *Repository) ApplyPatch(ctx context.Context, key dashboardappearance.Key, projectID, actorID string, patch dashboardappearance.Patch) (dashboardappearance.Record, error) {
	if r == nil || r.db == nil {
		return dashboardappearance.Record{}, fmt.Errorf("dashboard appearance persistence is unavailable")
	}
	if err := validateKey(key); err != nil {
		return dashboardappearance.Record{}, err
	}
	if err := dashboardappearance.ValidatePatch(patch); err != nil {
		return dashboardappearance.Record{}, err
	}
	if patch.Icon == nil && patch.Color == nil {
		return dashboardappearance.Record{}, fmt.Errorf("dashboard appearance patch is empty")
	}
	icon, iconPresent := "", patch.Icon != nil
	if patch.Icon != nil {
		icon = dashboardappearance.StoredValue(*patch.Icon)
	}
	color, colorPresent := "", patch.Color != nil
	if patch.Color != nil {
		color = dashboardappearance.StoredValue(*patch.Color)
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO dashboard_appearances (workspace_id, dashboard_id, project_id, icon, color, updated_by)
VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?)
ON CONFLICT(workspace_id, dashboard_id) DO UPDATE SET
  project_id = CASE WHEN excluded.project_id <> '' THEN excluded.project_id ELSE dashboard_appearances.project_id END,
  icon = CASE WHEN ? THEN excluded.icon ELSE dashboard_appearances.icon END,
  color = CASE WHEN ? THEN excluded.color ELSE dashboard_appearances.color END,
  revision = dashboard_appearances.revision + 1,
  updated_by = excluded.updated_by,
  updated_at = CURRENT_TIMESTAMP`,
		key.WorkspaceID, key.DashboardID, strings.TrimSpace(projectID), icon, color, strings.TrimSpace(actorID), iconPresent, colorPresent)
	if err != nil {
		return dashboardappearance.Record{}, err
	}
	return r.Get(ctx, key)
}

func (r *Repository) Get(ctx context.Context, key dashboardappearance.Key) (dashboardappearance.Record, error) {
	var row dashboardappearance.Record
	err := r.db.QueryRowContext(ctx, `
SELECT workspace_id, dashboard_id, project_id, COALESCE(icon, ''), COALESCE(color, ''), revision
FROM dashboard_appearances WHERE workspace_id = ? AND dashboard_id = ?`, key.WorkspaceID, key.DashboardID).
		Scan(&row.WorkspaceID, &row.DashboardID, &row.ProjectID, &row.Icon, &row.Color, &row.Revision)
	return row, err
}

func validateKey(key dashboardappearance.Key) error {
	if strings.TrimSpace(key.WorkspaceID) == "" || strings.TrimSpace(key.DashboardID) == "" {
		return fmt.Errorf("workspace and dashboard are required")
	}
	return nil
}
