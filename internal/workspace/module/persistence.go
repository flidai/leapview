package module

import (
	"context"
	"database/sql"
	"errors"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/workspace"
	workspacesqlite "github.com/flidai/leapview/internal/workspace/sqlite"
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
	repository workspace.Repository
}

func BuildDirectory(database *sql.DB, securables SecurableRegistrar) (Directory, error) {
	if database == nil {
		return nil, errors.New("workspace database is required")
	}
	return &directory{repository: workspacesqlite.NewRepositoryWithSecurables(database, securables)}, nil
}

func BuildReadModel(database *sql.DB) (ReadModel, error) {
	if database == nil {
		return nil, errors.New("workspace database is required")
	}
	return workspacesqlite.NewRepository(database), nil
}

func (p *directory) Ensure(ctx context.Context, input workspace.EnsureInput) error {
	return p.repository.Ensure(ctx, input)
}

func (p *directory) WorkspaceIDs(ctx context.Context) ([]string, error) {
	rows, err := p.repository.List(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, string(row.ID))
	}
	return ids, nil
}

func (p *directory) ActiveServingStateID(ctx context.Context, workspaceID string) (string, error) {
	row, err := p.repository.ByID(ctx, workspace.WorkspaceID(workspaceID))
	return string(row.ActiveServingStateID), err
}
