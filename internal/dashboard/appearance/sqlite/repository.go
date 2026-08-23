package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type Repository struct{ db *sql.DB }

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

func (r *Repository) ListProject(ctx context.Context, projectID projectgraph.ResourceID) (map[projectgraph.ResourceID]dashboardappearance.Record, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("dashboard appearance persistence is unavailable")
	}
	if err := projectID.Validate(); err != nil {
		return nil, fmt.Errorf("project ID: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT dashboard_id, COALESCE(icon, ''), COALESCE(color, ''), revision
FROM project_dashboard_appearances
WHERE project_id = ?`, projectID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[projectgraph.ResourceID]dashboardappearance.Record{}
	for rows.Next() {
		var dashboardID string
		var row dashboardappearance.Record
		if err := rows.Scan(&dashboardID, &row.Icon, &row.Color, &row.Revision); err != nil {
			return nil, err
		}
		id, err := projectgraph.NewResourceID(dashboardID)
		if err != nil {
			return nil, fmt.Errorf("stored dashboard ID: %w", err)
		}
		row.Key = dashboardappearance.Key{ProjectID: projectID, DashboardID: id}
		out[id] = row
	}
	return out, rows.Err()
}

func (r *Repository) ApplyPatch(ctx context.Context, key dashboardappearance.Key, actorID string, patch dashboardappearance.Patch) (dashboardappearance.Record, error) {
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
INSERT INTO project_dashboard_appearances (project_id, dashboard_id, icon, color, updated_by)
VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), ?)
ON CONFLICT(project_id, dashboard_id) DO UPDATE SET
  icon = CASE WHEN ? THEN excluded.icon ELSE project_dashboard_appearances.icon END,
  color = CASE WHEN ? THEN excluded.color ELSE project_dashboard_appearances.color END,
  revision = project_dashboard_appearances.revision + 1,
  updated_by = excluded.updated_by,
  updated_at = CURRENT_TIMESTAMP`,
		key.ProjectID.String(), key.DashboardID.String(), icon, color, strings.TrimSpace(actorID), iconPresent, colorPresent)
	if err != nil {
		return dashboardappearance.Record{}, err
	}
	return r.Get(ctx, key)
}

func (r *Repository) Get(ctx context.Context, key dashboardappearance.Key) (dashboardappearance.Record, error) {
	if r == nil || r.db == nil {
		return dashboardappearance.Record{}, fmt.Errorf("dashboard appearance persistence is unavailable")
	}
	if err := validateKey(key); err != nil {
		return dashboardappearance.Record{}, err
	}
	row := dashboardappearance.Record{Key: key}
	err := r.db.QueryRowContext(ctx, `
SELECT COALESCE(icon, ''), COALESCE(color, ''), revision
FROM project_dashboard_appearances
WHERE project_id = ? AND dashboard_id = ?`, key.ProjectID.String(), key.DashboardID.String()).
		Scan(&row.Icon, &row.Color, &row.Revision)
	return row, err
}

func validateKey(key dashboardappearance.Key) error {
	if err := key.ProjectID.Validate(); err != nil {
		return fmt.Errorf("project ID: %w", err)
	}
	if err := key.DashboardID.Validate(); err != nil {
		return fmt.Errorf("dashboard ID: %w", err)
	}
	return nil
}
