// Package postgres implements native PostgreSQL dashboard usage persistence.
package postgres

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/dashboard/usage"
	db "github.com/flidai/leapview/internal/dashboard/usage/postgres/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// MaintenanceDBTX is the native PostgreSQL surface for the separately
// authenticated retention connection. Runtime repositories do not retain or
// expose this destructive capability.
type MaintenanceDBTX interface {
	DBTX
}

type Tx interface {
	DBTX
	Commit(context.Context) error
	Rollback(context.Context) error
}

// Maintenance owns bounded viewer-day retention. Repository remains limited
// to recording and reading usage on the serving path.
type Maintenance struct{ db MaintenanceDBTX }

var ErrUnavailable = errors.New("dashboard PostgreSQL usage store is unavailable")

const (
	maxRetentionBatch = 10000
)

//go:embed schema.sql
var schemaSQL string

func SchemaSQL() string { return schemaSQL }

func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return ErrUnavailable
	}
	_, err := tx.Exec(ctxOrBackground(ctx), schemaSQL) // sqlc-exception: schema-ddl
	return err
}

type Repository struct {
	db     DBTX
	native bool
}

func New(db DBTX) (*Repository, error) {
	if db == nil {
		return nil, ErrUnavailable
	}
	return &Repository{db: db, native: true}, nil
}

// IsNative reports whether the repository was constructed by New.
func (r *Repository) IsNative() bool { return r != nil && r.native }

func (r *Repository) Ping(ctx context.Context) error {
	if r == nil || r.db == nil {
		return ErrUnavailable
	}
	_, err := db.New(r.db).Ping(ctxOrBackground(ctx))
	return err
}

func (r *Repository) RecordView(ctx context.Context, view usage.View) error {
	if err := view.Validate(); err != nil {
		return err
	}
	if r == nil || r.db == nil {
		return ErrUnavailable
	}
	viewedAt := view.ViewedAt.UTC()
	if err := db.New(r.db).UpsertViewDay(ctxOrBackground(ctx), db.UpsertViewDayParams{ProjectID: strings.TrimSpace(view.ProjectID.String()), DashboardID: strings.TrimSpace(view.DashboardID.String()), PrincipalID: strings.TrimSpace(view.PrincipalID), ViewedOn: viewedAt, PageID: strings.TrimSpace(view.PageID), ViewedAt: viewedAt}); err != nil {
		return err
	}
	return nil
}

func (r *Repository) ListSummaries(ctx context.Context, cutoff time.Time) ([]usage.Summary, error) {
	if r == nil || r.db == nil {
		return nil, ErrUnavailable
	}
	rows, err := db.New(r.db).ListSummaries(ctxOrBackground(ctx), cutoff.UTC())
	if err != nil {
		return nil, err
	}
	out := make([]usage.Summary, 0, len(rows))
	for _, row := range rows {
		lastViewedAt := row.LastViewedAt.UTC()
		projectID, err := projectgraph.NewResourceID(strings.TrimSpace(row.ProjectID))
		if err != nil {
			return nil, fmt.Errorf("decode dashboard usage project ID: %w", err)
		}
		dashboardID, err := projectgraph.NewResourceID(strings.TrimSpace(row.DashboardID))
		if err != nil {
			return nil, fmt.Errorf("decode dashboard usage dashboard ID: %w", err)
		}
		out = append(out, usage.Summary{Key: usage.Key{ProjectID: projectID, DashboardID: dashboardID}, ViewerCount: row.ViewerCount, ViewerDays: row.ViewerDays, LastViewedAt: lastViewedAt})
	}
	return out, nil
}

func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

var _ usage.Recorder = (*Repository)(nil)
var _ usage.Reader = (*Repository)(nil)
