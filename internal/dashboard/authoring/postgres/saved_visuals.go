package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	dashboarddb "github.com/flidai/leapview/internal/dashboard/authoring/postgres/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) SaveVisual(ctx context.Context, project, principal string, v authoring.SavedVisual) (authoring.SavedVisual, error) {
	owner, err := nativeUUID(principal)
	if err != nil || project == "" {
		return authoring.SavedVisual{}, fmt.Errorf("saved visual owner and project are required")
	}
	id, err := uuid.NewV7()
	if err != nil {
		return authoring.SavedVisual{}, err
	}
	row, err := dashboarddb.New(r.db).SaveVisual(ctx, dashboarddb.SaveVisualParams{ProjectID: project, PrincipalID: owner, ID: nativeUUIDValue(id.String()), SourceKey: v.SourceKey, Title: v.Title, SemanticModelID: v.SemanticModelID, DefinitionJson: []byte(v.DefinitionJSON)})
	if err != nil {
		return authoring.SavedVisual{}, err
	}
	return savedVisual(row), nil
}

func (r *Repository) SavedVisuals(ctx context.Context, project, principal string) ([]authoring.SavedVisual, error) {
	owner, err := nativeUUID(principal)
	if err != nil || project == "" {
		return nil, fmt.Errorf("saved visual owner and project are required")
	}
	rows, err := dashboarddb.New(r.db).SavedVisuals(ctx, dashboarddb.SavedVisualsParams{ProjectID: project, PrincipalID: owner})
	if err != nil {
		return nil, err
	}
	result := make([]authoring.SavedVisual, 0, len(rows))
	for _, row := range rows {
		result = append(result, savedVisual(row))
	}
	return result, nil
}

func (r *Repository) SavedVisual(ctx context.Context, project, principal, id string) (authoring.SavedVisual, error) {
	owner, err := nativeUUID(principal)
	if err != nil || project == "" {
		return authoring.SavedVisual{}, fmt.Errorf("saved visual owner and project are required")
	}
	visualID, err := nativeUUID(id)
	if err != nil {
		return authoring.SavedVisual{}, authoring.ErrNotFound
	}
	row, err := dashboarddb.New(r.db).SavedVisual(ctx, dashboarddb.SavedVisualParams{ProjectID: project, PrincipalID: owner, ID: visualID})
	if errors.Is(err, pgx.ErrNoRows) {
		return authoring.SavedVisual{}, authoring.ErrNotFound
	}
	if err != nil {
		return authoring.SavedVisual{}, err
	}
	return savedVisual(row), nil
}

func savedVisual(row dashboarddb.DashboardSavedVisual) authoring.SavedVisual {
	return authoring.SavedVisual{ID: uuid.UUID(row.ID.Bytes).String(), Title: row.Title, SemanticModelID: row.SemanticModelID, SourceKey: row.SourceKey, DefinitionJSON: string(row.DefinitionJson)}
}

func (r *Repository) UnsaveVisual(ctx context.Context, project, principal, id string) error {
	owner, err := nativeUUID(principal)
	if err != nil || project == "" {
		return fmt.Errorf("saved visual owner and project are required")
	}
	visualID, err := nativeUUID(id)
	if err != nil {
		return authoring.ErrNotFound
	}
	return dashboarddb.New(r.db).UnsaveVisual(ctx, dashboarddb.UnsaveVisualParams{ProjectID: project, PrincipalID: owner, ID: visualID})
}
