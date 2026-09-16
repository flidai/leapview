package postgres

import (
	"context"
	"errors"

	refreshdb "github.com/flidai/leapview/internal/refresh/postgres/internal/db"
	"github.com/jackc/pgx/v5"
)

const maxExpiredRefreshJobPage = 100

// ExpiredJobRun is refresh-owned snapshot evidence that a canonical job's
// renewable execution lease had elapsed when read.
type ExpiredJobRun struct {
	RunID           string
	JobID           string
	Status          string
	AttemptCount    int64
	FenceGeneration int64
	LeaseOwner      string
}

func expiredJobRun(runID string, jobID *string, status string, attempts, fence int64, owner string) (ExpiredJobRun, error) {
	if jobID == nil || *jobID == "" || attempts < 1 || fence < 1 || owner == "" {
		return ExpiredJobRun{}, ErrInvalid
	}
	return ExpiredJobRun{RunID: runID, JobID: *jobID, Status: status, AttemptCount: attempts, FenceGeneration: fence, LeaseOwner: owner}, nil
}

// ListExpiredJobRuns returns only active refresh runs whose product lease is
// already expired. Callers must lock and recheck each row before recovery.
func (r *Repository) ListExpiredJobRuns(ctx context.Context, limit int) ([]ExpiredJobRun, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > maxExpiredRefreshJobPage {
		return nil, ErrInvalid
	}
	rows, err := refreshdb.New(r.db).ListExpiredRefreshJobRuns(ctx, int32(limit))
	if err != nil {
		return nil, err
	}
	out := make([]ExpiredJobRun, 0, len(rows))
	for _, row := range rows {
		candidate, err := expiredJobRun(row.RunID, row.JobID, row.Status, row.AttemptCount, row.FenceGeneration, row.LeaseOwner)
		if err != nil {
			return nil, err
		}
		out = append(out, candidate)
	}
	return out, nil
}

// LockExpiredJobRunTx rechecks the lease under the refresh row lock held
// through the sibling River rescue transaction.
func (r *Repository) LockExpiredJobRunTx(ctx context.Context, tx Tx, runID, jobID string) (ExpiredJobRun, bool, error) {
	if tx == nil || runID == "" || jobID == "" {
		return ExpiredJobRun{}, false, ErrInvalid
	}
	row, err := refreshdb.New(tx).LockExpiredRefreshJobRun(ctx, refreshdb.LockExpiredRefreshJobRunParams{RunID: runID, JobID: &jobID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ExpiredJobRun{}, false, nil
	}
	if err != nil {
		return ExpiredJobRun{}, false, err
	}
	locked, err := expiredJobRun(row.RunID, row.JobID, row.Status, row.AttemptCount, row.FenceGeneration, row.LeaseOwner)
	if err != nil {
		return ExpiredJobRun{}, false, err
	}
	_, err = refreshdb.New(tx).LockCurrentRefreshAttemptForOrphanRescue(ctx, refreshdb.LockCurrentRefreshAttemptForOrphanRescueParams{
		RunID:           locked.RunID,
		AttemptNumber:   locked.AttemptCount,
		FenceGeneration: locked.FenceGeneration,
		OwnerID:         locked.LeaseOwner,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ExpiredJobRun{}, false, nil
	}
	return locked, err == nil, err
}
