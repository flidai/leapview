package module

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

const staleRiverResultFenceSQLState = "LV001"

type resultFenceRiverDriver struct {
	*riverpgxv5.Driver
}

func newResultFenceRiverDriver(pool *pgxpool.Pool) riverdriver.Driver[pgx.Tx] {
	return &resultFenceRiverDriver{Driver: riverpgxv5.New(pool)}
}

func (d *resultFenceRiverDriver) GetExecutor() riverdriver.Executor {
	return resultFenceExecutor{Executor: d.Driver.GetExecutor()}
}

// resultFenceExecutor isolates River's batch finalization statements so a
// rejected stale worker result cannot roll back unrelated current results.
// The client has bounded worker concurrency, so trading completion batching
// for an exact per-job fence keeps the failure domain honest and inexpensive.
type resultFenceExecutor struct {
	riverdriver.Executor
}

func (e resultFenceExecutor) JobSetStateIfRunningMany(ctx context.Context, params *riverdriver.JobSetStateIfRunningManyParams) ([]*rivertype.JobRow, error) {
	rows := make([]*rivertype.JobRow, 0, len(params.ID))
	for i := range params.ID {
		updated, err := e.Executor.JobSetStateIfRunningMany(ctx, singleJobSetStateParams(params, i))
		if isStaleRiverResultFence(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, updated...)
	}
	return rows, nil
}

func singleJobSetStateParams(params *riverdriver.JobSetStateIfRunningManyParams, i int) *riverdriver.JobSetStateIfRunningManyParams {
	return &riverdriver.JobSetStateIfRunningManyParams{
		ID:              []int64{params.ID[i]},
		Attempt:         []*int{params.Attempt[i]},
		ErrData:         [][]byte{params.ErrData[i]},
		FinalizedAt:     []*time.Time{params.FinalizedAt[i]},
		MetadataDoMerge: []bool{params.MetadataDoMerge[i]},
		MetadataUpdates: [][]byte{params.MetadataUpdates[i]},
		Now:             params.Now,
		ScheduledAt:     []*time.Time{params.ScheduledAt[i]},
		Schema:          params.Schema,
		State:           []rivertype.JobState{params.State[i]},
	}
}

func isStaleRiverResultFence(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == staleRiverResultFenceSQLState
}
