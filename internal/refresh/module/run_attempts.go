package module

import (
	"context"
	"errors"
	"strings"
	"time"

	refreshrun "github.com/flidai/leapview/internal/refresh/run"
)

const runAttemptDetailPageSize = 20

type runAttemptPersistence interface {
	ListRunAttempts(context.Context, refreshrun.ReadScope, string) (refreshrun.RunAttemptPage, error)
}

// ListRunAttempts exposes a bounded, project/environment-scoped projection of
// attempts already stored by the refresh repository. Test or alternate
// persistence adapters without attempt storage report the capability as
// unavailable instead of inventing attempts from run status.
func (m *Module) ListRunAttempts(ctx context.Context, scope refreshrun.ReadScope, runID string) (refreshrun.RunAttemptPage, error) {
	if err := scope.Validate(); err != nil {
		return refreshrun.RunAttemptPage{}, err
	}
	store, err := m.readRuns()
	if err != nil {
		return refreshrun.RunAttemptPage{}, err
	}
	reader, ok := store.(runAttemptPersistence)
	if !ok {
		return refreshrun.RunAttemptPage{}, errors.New("refresh run-attempt persistence is unavailable")
	}
	return reader.ListRunAttempts(ctx, scope, runID)
}

func (p *postgresRunPersistence) ListRunAttempts(ctx context.Context, scope refreshrun.ReadScope, runID string) (refreshrun.RunAttemptPage, error) {
	if p == nil || p.repository == nil {
		return refreshrun.RunAttemptPage{}, errors.New("refresh PostgreSQL run persistence is unavailable")
	}
	if err := scope.Validate(); err != nil {
		return refreshrun.RunAttemptPage{}, err
	}
	if runID == "" || strings.TrimSpace(runID) != runID {
		return refreshrun.RunAttemptPage{}, errors.New("refresh run id is required")
	}
	// Resolve the run through the scoped authority before using the globally
	// unique run ID for the attempts query.
	if _, err := p.getRun(ctx, scope, runID); err != nil {
		return refreshrun.RunAttemptPage{}, err
	}
	rows, err := p.repository.Attempts(ctx, runID, runAttemptDetailPageSize+1)
	if err != nil {
		return refreshrun.RunAttemptPage{}, err
	}
	truncated := len(rows) > runAttemptDetailPageSize
	if truncated {
		rows = rows[:runAttemptDetailPageSize]
	}
	page := refreshrun.RunAttemptPage{Attempts: make([]refreshrun.RunAttemptRecord, 0, len(rows)), Truncated: truncated}
	for _, row := range rows {
		attempt := refreshrun.RunAttemptRecord{
			Number: row.AttemptNumber, Status: row.Status,
			ClaimedAt: row.ClaimedAt.UTC().Format(time.RFC3339Nano), Error: row.Error,
		}
		if !row.StartedAt.IsZero() {
			attempt.StartedAt = row.StartedAt.UTC().Format(time.RFC3339Nano)
		}
		if !row.FinishedAt.IsZero() {
			attempt.FinishedAt = row.FinishedAt.UTC().Format(time.RFC3339Nano)
		}
		page.Attempts = append(page.Attempts, attempt)
	}
	// The repository returns newest first; user-facing history reads forward.
	for left, right := 0, len(page.Attempts)-1; left < right; left, right = left+1, right-1 {
		page.Attempts[left], page.Attempts[right] = page.Attempts[right], page.Attempts[left]
	}
	return page, nil
}
