package sqlite

import (
	"context"
	"database/sql"
	"strings"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/servingstate"
)

func (r *Repository) ListProjects(ctx context.Context) ([]release.ProjectRecord, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT project_id, CAST(MIN(created_at) AS TEXT), CAST(MAX(updated_at) AS TEXT) FROM (
SELECT project_id, created_at, COALESCE(finalized_at, created_at) AS updated_at FROM api_releases
UNION ALL SELECT project_id, created_at, updated_at FROM managed_data_collections
) GROUP BY project_id ORDER BY project_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []release.ProjectRecord
	for rows.Next() {
		var item release.ProjectRecord
		if err := rows.Scan(&item.ID, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		r.populateProjectPointers(ctx, &item)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *Repository) GetProject(ctx context.Context, projectID string) (release.ProjectRecord, error) {
	if projectID == "" || projectID != strings.TrimSpace(projectID) || projectgraph.ResourceID(projectID).Validate() != nil {
		return release.ProjectRecord{}, release.ErrInvalid
	}
	var item release.ProjectRecord
	item.ID = projectID
	err := r.db.QueryRowContext(ctx, `SELECT CAST(COALESCE(MIN(created_at), '') AS TEXT), CAST(COALESCE(MAX(updated_at), '') AS TEXT) FROM (
SELECT created_at, COALESCE(finalized_at, created_at) AS updated_at FROM api_releases WHERE project_id = ?
UNION ALL SELECT created_at, updated_at FROM managed_data_collections WHERE project_id = ?
)`, item.ID, item.ID).Scan(&item.CreatedAt, &item.UpdatedAt)
	if err != nil || item.CreatedAt == "" {
		return release.ProjectRecord{}, sql.ErrNoRows
	}
	r.populateProjectPointers(ctx, &item)
	return item, nil
}

func (r *Repository) populateProjectPointers(ctx context.Context, item *release.ProjectRecord) {
	_ = r.db.QueryRowContext(ctx, `SELECT id FROM api_releases WHERE project_id = ? ORDER BY created_at DESC,id DESC LIMIT 1`, item.ID).Scan(&item.LatestReleaseID)
	_ = r.db.QueryRowContext(ctx, `SELECT id FROM project_deployments WHERE project_id = ? AND status = 'active' ORDER BY activated_at DESC LIMIT 1`, item.ID).Scan(&item.ActiveDeploymentID)
}

func (r *Repository) ListConnections(ctx context.Context, projectID, environment string) ([]release.ConnectionRecord, error) {
	if err := validateCatalogScope(projectID, environment); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT c.connection_id,c.name,c.description,COALESCE(rev.id,'')
FROM managed_data_collections c LEFT JOIN managed_data_environment_pointers ptr ON ptr.collection_id=c.id AND ptr.environment=?
LEFT JOIN managed_data_revisions rev ON rev.id=ptr.revision_id WHERE c.project_id=? AND c.status='active' ORDER BY c.connection_id`, environment, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []release.ConnectionRecord
	for rows.Next() {
		var item release.ConnectionRecord
		if err := rows.Scan(&item.ID, &item.Title, &item.Description, &item.ActiveRevisionID); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *Repository) GetConnection(ctx context.Context, projectID, connectionID, environment string) (release.ConnectionRecord, error) {
	if err := validateCatalogScope(projectID, environment); err != nil {
		return release.ConnectionRecord{}, err
	}
	if connectionID == "" || connectionID != strings.TrimSpace(connectionID) || projectgraph.ResourceID(connectionID).Validate() != nil {
		return release.ConnectionRecord{}, release.ErrInvalid
	}
	var item release.ConnectionRecord
	item.ID = connectionID
	err := r.db.QueryRowContext(ctx, `SELECT c.name,c.description,COALESCE(rev.id,'') FROM managed_data_collections c LEFT JOIN managed_data_environment_pointers ptr ON ptr.collection_id=c.id AND ptr.environment=? LEFT JOIN managed_data_revisions rev ON rev.id=ptr.revision_id WHERE c.project_id=? AND c.connection_id=? AND c.status='active'`, environment, projectID, connectionID).Scan(&item.Title, &item.Description, &item.ActiveRevisionID)
	return item, err
}

func validateCatalogScope(projectID, environment string) error {
	if projectID == "" || projectID != strings.TrimSpace(projectID) || projectgraph.ResourceID(projectID).Validate() != nil {
		return release.ErrInvalid
	}
	if environment == "" || environment != strings.TrimSpace(environment) || servingstate.ValidateEnvironment(servingstate.Environment(environment)) != nil {
		return release.ErrInvalid
	}
	return nil
}
