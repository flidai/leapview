package sqlite

import (
	"context"
	"database/sql"
	"strings"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	releasedb "github.com/flidai/leapview/internal/release/internal/db"
	"github.com/flidai/leapview/internal/servingstate"
)

func (r *Repository) GetProject(ctx context.Context, projectID string) (release.ProjectRecord, error) {
	if projectID == "" || projectID != strings.TrimSpace(projectID) || projectgraph.ResourceID(projectID).Validate() != nil {
		return release.ProjectRecord{}, release.ErrInvalid
	}
	var item release.ProjectRecord
	item.ID = projectID
	row, err := r.queries.GetAPIProject(ctx, item.ID)
	if err != nil {
		return release.ProjectRecord{}, err
	}
	item.CreatedAt, item.UpdatedAt = row.CreatedAt, row.UpdatedAt
	if item.CreatedAt == "" {
		return release.ProjectRecord{}, sql.ErrNoRows
	}
	r.populateProjectPointers(ctx, &item)
	return item, nil
}

func (r *Repository) populateProjectPointers(ctx context.Context, item *release.ProjectRecord) {
	item.LatestReleaseID, _ = r.queries.GetLatestAPIProjectReleaseID(ctx, item.ID)
	item.ActiveDeploymentID, _ = r.queries.GetActiveAPIProjectDeploymentID(ctx, item.ID)
}

func (r *Repository) ListConnections(ctx context.Context, projectID, environment string) ([]release.ConnectionRecord, error) {
	if err := validateCatalogScope(projectID, environment); err != nil {
		return nil, err
	}
	rows, err := r.queries.ListAPIProjectConnections(ctx, releasedb.ListAPIProjectConnectionsParams{Environment: environment, ProjectID: projectID})
	if err != nil {
		return nil, err
	}
	out := make([]release.ConnectionRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, release.ConnectionRecord{ID: row.ConnectionID, Title: row.Name, Description: row.Description, ActiveRevisionID: row.ActiveRevisionID})
	}
	return out, nil
}

func (r *Repository) GetConnection(ctx context.Context, projectID, connectionID, environment string) (release.ConnectionRecord, error) {
	if err := validateCatalogScope(projectID, environment); err != nil {
		return release.ConnectionRecord{}, err
	}
	if connectionID == "" || connectionID != strings.TrimSpace(connectionID) || projectgraph.ResourceID(connectionID).Validate() != nil {
		return release.ConnectionRecord{}, release.ErrInvalid
	}
	row, err := r.queries.GetAPIProjectConnection(ctx, releasedb.GetAPIProjectConnectionParams{Environment: environment, ProjectID: projectID, ConnectionID: connectionID})
	if err != nil {
		return release.ConnectionRecord{}, err
	}
	return release.ConnectionRecord{ID: connectionID, Title: row.Name, Description: row.Description, ActiveRevisionID: row.ActiveRevisionID}, nil
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
