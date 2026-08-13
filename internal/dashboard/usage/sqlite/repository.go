package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	dashboarddb "github.com/flidai/leapview/internal/dashboard/internal/db"
	"github.com/flidai/leapview/internal/dashboard/usage"
)

type Repository struct {
	db *sql.DB
	q  *dashboarddb.Queries
}

func NewRepository(database *sql.DB) *Repository {
	return &Repository{db: database, q: dashboarddb.New(database)}
}

func (repository *Repository) RecordView(ctx context.Context, view usage.View) error {
	if err := view.Validate(); err != nil {
		return err
	}
	if repository == nil || repository.db == nil {
		return fmt.Errorf("dashboard usage database is required")
	}
	viewedAt := view.ViewedAt.UTC()
	if err := repository.q.UpsertDashboardViewDay(ctx, dashboarddb.UpsertDashboardViewDayParams{
		WorkspaceID: strings.TrimSpace(view.WorkspaceID), DashboardID: strings.TrimSpace(view.DashboardID),
		PrincipalID: strings.TrimSpace(view.PrincipalID), ViewedOn: viewedAt.Format(time.DateOnly),
		PageID: strings.TrimSpace(view.PageID), ViewedAt: viewedAt.Format(time.RFC3339Nano),
	}); err != nil {
		return err
	}
	return repository.q.DeleteDashboardViewDaysBefore(ctx, viewedAt.Add(-usage.RetentionWindow).Format(time.DateOnly))
}

func (repository *Repository) ListSummaries(ctx context.Context, cutoff time.Time) ([]usage.Summary, error) {
	if repository == nil || repository.db == nil {
		return nil, fmt.Errorf("dashboard usage database is required")
	}
	rows, err := repository.q.ListDashboardUsageSummaries(ctx, cutoff.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	summaries := make([]usage.Summary, 0, len(rows))
	for _, row := range rows {
		lastViewedAt, err := time.Parse(time.RFC3339Nano, row.LastViewedAt)
		if err != nil {
			return nil, fmt.Errorf("decode dashboard usage timestamp: %w", err)
		}
		summaries = append(summaries, usage.Summary{
			Key:         usage.Key{WorkspaceID: row.WorkspaceID, DashboardID: row.DashboardID},
			ViewerCount: row.ViewerCount, ViewerDays: row.ViewerDays, LastViewedAt: lastViewedAt,
		})
	}
	return summaries, nil
}
