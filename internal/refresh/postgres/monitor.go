package postgres

import (
	"context"
	"errors"
	"time"

	refreshdb "github.com/flidai/leapview/internal/refresh/postgres/internal/db"
)

// MonitorFilter is the SQL-side equivalent of the cross-pipeline monitor
// request. Every row and time-scoped count uses the same base predicate.
type MonitorFilter struct {
	Since, Until            time.Time
	Search, Status, Trigger string
	PipelineIDs             []string
	AllowedPipelineIDs      []string
	Limit                   int
	Offset                  int64
}

type MonitorPage struct {
	Runs                             []Run
	Total, Failed, Completed, Active int64
}

// MonitorRuns filters before pagination. Time-scoped counts use the same
// filters as rows; Active deliberately ignores time and status because it
// represents jobs executing or waiting now.
func (r *Repository) MonitorRuns(ctx context.Context, scope Scope, filter MonitorFilter) (MonitorPage, error) {
	if err := r.requireDB(); err != nil {
		return MonitorPage{}, err
	}
	if err := validateScope(scope.ProjectID, scope.Environment); err != nil {
		return MonitorPage{}, err
	}
	if filter.Limit < 1 || filter.Limit > MaxPageSize || filter.Offset < 0 || filter.Since.IsZero() || filter.Until.IsZero() || !filter.Since.Before(filter.Until) {
		return MonitorPage{}, errors.New("invalid run monitor page or time range")
	}
	queries := refreshdb.New(r.db)
	page := MonitorPage{}
	counts, err := queries.MonitorRunsCounts(ctx, refreshdb.MonitorRunsCountsParams{
		ProjectID: scope.ProjectID, Environment: scope.Environment, AllowedPipelineIds: filter.AllowedPipelineIDs,
		Search: filter.Search, MatchedPipelineIds: filter.PipelineIDs, Trigger: filter.Trigger,
		Status: filter.Status, SinceAt: filter.Since, UntilAt: filter.Until,
	})
	if err != nil {
		return MonitorPage{}, err
	}
	page.Total, page.Failed, page.Completed = counts.Total, counts.Failed, counts.Completed
	page.Active, err = queries.MonitorActiveRunsCount(ctx, refreshdb.MonitorActiveRunsCountParams{
		ProjectID: scope.ProjectID, Environment: scope.Environment, AllowedPipelineIds: filter.AllowedPipelineIDs,
		Search: filter.Search, MatchedPipelineIds: filter.PipelineIDs, Trigger: filter.Trigger,
	})
	if err != nil {
		return MonitorPage{}, err
	}
	ids, err := queries.MonitorRunIDs(ctx, refreshdb.MonitorRunIDsParams{
		ProjectID: scope.ProjectID, Environment: scope.Environment, AllowedPipelineIds: filter.AllowedPipelineIDs,
		Search: filter.Search, MatchedPipelineIds: filter.PipelineIDs, Trigger: filter.Trigger,
		Status: filter.Status, SinceAt: filter.Since, UntilAt: filter.Until,
		PageLimit: int32(filter.Limit), PageOffset: filter.Offset,
	})
	if err != nil {
		return MonitorPage{}, err
	}
	page.Runs = make([]Run, 0, len(ids))
	for _, id := range ids {
		run, err := r.runByID(ctx, r.db, id)
		if err != nil {
			return MonitorPage{}, err
		}
		page.Runs = append(page.Runs, run)
	}
	return page, nil
}
