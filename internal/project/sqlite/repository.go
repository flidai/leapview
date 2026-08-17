// Package sqlite persists project identity records.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectdb "github.com/flidai/leapview/internal/project/internal/db"
)

var ErrNotFound = errors.New("project not found")

type Record struct {
	ID          projectgraph.ResourceID
	Title       string
	Description string
	CreatedAt   string
	UpdatedAt   string
}

type EnsureInput struct {
	ID          projectgraph.ResourceID
	Title       string
	Description string
}

type Repository struct {
	q *projectdb.Queries
}

func NewRepository(sqlDB *sql.DB) *Repository {
	return &Repository{q: projectdb.New(sqlDB)}
}

func (r *Repository) Ensure(ctx context.Context, input EnsureInput) error {
	id, err := projectgraph.NewResourceID(strings.TrimSpace(input.ID.String()))
	if err != nil {
		return fmt.Errorf("project id: %w", err)
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = id.String()
	}
	return r.q.UpsertProject(ctx, projectdb.UpsertProjectParams{ID: id.String(), Title: title, Description: input.Description})
}

func (r *Repository) List(ctx context.Context) ([]Record, error) {
	rows, err := r.q.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(rows))
	for _, row := range rows {
		mapped, err := mapRecord(row)
		if err != nil {
			return nil, err
		}
		out = append(out, mapped)
	}
	return out, nil
}

func (r *Repository) ByID(ctx context.Context, id projectgraph.ResourceID) (Record, error) {
	row, err := r.q.GetProject(ctx, id.String())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Record{}, ErrNotFound
		}
		return Record{}, err
	}
	return mapRecord(row)
}

func mapRecord(row projectdb.Project) (Record, error) {
	id, err := projectgraph.NewResourceID(row.ID)
	if err != nil {
		return Record{}, fmt.Errorf("stored project id: %w", err)
	}
	return Record{ID: id, Title: row.Title, Description: row.Description, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, nil
}
