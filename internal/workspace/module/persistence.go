package module

import (
	"context"
	"database/sql"
	"errors"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectsqlite "github.com/flidai/leapview/internal/project/sqlite"
	"github.com/flidai/leapview/internal/workspace"
)

type SecurableRegistrar interface {
	UpsertSecurableObject(context.Context, access.ObjectRef, string) (access.SecurableObject, error)
}

type Directory interface {
	Ensure(context.Context, workspace.EnsureInput) error
	WorkspaceIDs(context.Context) ([]string, error)
	ActiveServingStateID(context.Context, string) (string, error)
}

type directory struct {
	repository *projectsqlite.Repository
	securables SecurableRegistrar
}

func BuildDirectory(database *sql.DB, securables SecurableRegistrar) (Directory, error) {
	if database == nil {
		return nil, errors.New("workspace database is required")
	}
	return &directory{repository: projectsqlite.NewRepository(database), securables: securables}, nil
}

func BuildReadModel(database *sql.DB) (ReadModel, error) {
	if database == nil {
		return nil, errors.New("workspace database is required")
	}
	return &readModel{repository: projectsqlite.NewRepository(database)}, nil
}

func (p *directory) Ensure(ctx context.Context, input workspace.EnsureInput) error {
	if err := p.repository.Ensure(ctx, projectsqlite.EnsureInput{ID: projectgraph.ResourceID(input.ID), Title: input.Title, Description: input.Description}); err != nil {
		return err
	}
	if p.securables == nil {
		return nil
	}
	object := access.WorkspaceObject(string(input.ID))
	object.DisplayName = input.Title
	_, err := p.securables.UpsertSecurableObject(ctx, object, "")
	return err
}

func (p *directory) WorkspaceIDs(ctx context.Context) ([]string, error) {
	rows, err := p.repository.List(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID.String())
	}
	return ids, nil
}

func (p *directory) ActiveServingStateID(ctx context.Context, workspaceID string) (string, error) {
	_, err := p.repository.ByID(ctx, projectgraph.ResourceID(workspaceID))
	return "", err
}

type readModel struct{ repository *projectsqlite.Repository }

func (r *readModel) List(ctx context.Context) ([]workspace.Summary, error) {
	rows, err := r.repository.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]workspace.Summary, 0, len(rows))
	for _, row := range rows {
		out = append(out, workspace.Summary{ID: workspace.WorkspaceID(row.ID.String()), Title: row.Title, Description: row.Description, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt})
	}
	return out, nil
}

func (r *readModel) ByID(ctx context.Context, id workspace.WorkspaceID) (workspace.Summary, error) {
	row, err := r.repository.ByID(ctx, projectgraph.ResourceID(id))
	if err != nil {
		if errors.Is(err, projectsqlite.ErrNotFound) {
			return workspace.Summary{}, workspace.ErrNotFound
		}
		return workspace.Summary{}, err
	}
	return workspace.Summary{ID: workspace.WorkspaceID(row.ID.String()), Title: row.Title, Description: row.Description, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, nil
}

func (r *readModel) ActiveServingStateGraph(context.Context, workspace.WorkspaceID, string) (workspace.AssetGraph, bool, error) {
	return workspace.AssetGraph{}, false, nil
}

func (r *readModel) AssetVersions(context.Context, workspace.WorkspaceID, string, workspace.AssetID) ([]workspace.AssetVersion, error) {
	return nil, nil
}
