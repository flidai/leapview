package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	jobdb "github.com/flidai/leapview/internal/platform/jobs/postgres/internal/db"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river/riverdriver"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

const (
	refreshPipelineKind         = "refresh_pipeline"
	refreshPipelineResourceKind = "refresh_run"
)

func refreshOrphanReclaimable(history jobdb.LockJobForRefreshOrphanRescueRow, riverJob jobdb.LockRiverJobForRefreshOrphanRescueRow, runID, owner string, attempt int) bool {
	return history.Kind == refreshPipelineKind && history.ResourceKind == refreshPipelineResourceKind && history.ResourceID == runID &&
		history.Status == string(jobs.StatusRunning) && int(history.AttemptCount) == attempt && history.RiverJobID != nil &&
		riverJob.ID == *history.RiverJobID && riverJob.Kind == refreshPipelineKind && rivertype.JobState(riverJob.State) == rivertype.JobStateRunning &&
		int(riverJob.Attempt) == attempt && riverJob.Attempt < riverJob.MaxAttempts && len(riverJob.AttemptedBy) > 0 &&
		riverJob.AttemptedBy[len(riverJob.AttemptedBy)-1] == owner
}

// RescueExpiredRefreshJobTx applies River's supported rescue transition to
// the exact operational row after the refresh authority has locked and
// revalidated its expired product lease in the same transaction.
func (r *Repository) RescueExpiredRefreshJobTx(ctx context.Context, tx Tx, productJobID, runID, owner string, attempt int) (bool, error) {
	if tx == nil || productJobID == "" || runID == "" || owner == "" || attempt < 1 {
		return false, errors.New("refresh orphan rescue evidence is invalid")
	}
	history, err := queries(tx).LockJobForRefreshOrphanRescue(ctx, productJobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if history.RiverJobID == nil {
		return false, nil
	}
	riverJob, err := queries(tx).LockRiverJobForRefreshOrphanRescue(ctx, *history.RiverJobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !refreshOrphanReclaimable(history, riverJob, runID, owner, attempt) {
		return false, nil
	}
	now := time.Now().UTC()
	attemptError, err := json.Marshal(rivertype.AttemptError{At: now, Attempt: attempt, Error: "Orphaned refresh job rescued after product lease expiry"})
	if err != nil {
		return false, err
	}
	pgxTx, err := nativeTransaction(tx)
	if err != nil {
		return false, err
	}
	executor := riverpgxv5.New(nil).UnwrapExecutor(pgxTx)
	_, err = executor.JobRescueMany(ctx, &riverdriver.JobRescueManyParams{
		ID: []int64{riverJob.ID}, Error: [][]byte{attemptError}, FinalizedAt: []*time.Time{nil},
		ScheduledAt: []time.Time{now}, State: []string{string(rivertype.JobStateRetryable)}, StuckHorizon: now,
	})
	return err == nil, err
}
