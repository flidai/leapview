package module

import (
	"context"
	"errors"
	"time"

	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/flidai/leapview/pkg/jobs"
)

const refreshOrphanRecoveryPage = 100

type refreshPipelineJobHandler struct {
	module      *Module
	persistence *postgresRunPersistence
}

func (h *refreshPipelineJobHandler) Kind() string { return refreshrun.JobKindRefreshPipeline }
func (h *refreshPipelineJobHandler) LeaseTimeout() time.Duration {
	return h.module.leaseTimeout
}

func (h *refreshPipelineJobHandler) Handle(ctx context.Context, job jobs.Job) error {
	if h == nil || h.module == nil || h.persistence == nil || h.persistence.jobs == nil {
		return errors.New("River refresh persistence is unavailable")
	}
	claimed, err := h.persistence.jobs.ClaimRiverJob(ctx, job, h.module.leaseTimeout)
	if err != nil {
		return err
	}
	defer func() {
		if h.module.runFinishedCallback != nil {
			h.module.runFinishedCallback(context.Background(), claimed)
		}
	}()
	return executeWithLeaseHeartbeat(ctx, claimed, h.module.leaseTimeout, h.module.runs.RenewJobLease, h.module.service.ExecuteClaimedJob)
}

// RecoverOrphanedJobs is consumed only by the jobs module for this exact job
// kind. Other long-running workers retain River's ordinary rescue horizon.
func (h *refreshPipelineJobHandler) RecoverOrphanedJobs(ctx context.Context) error {
	if h == nil || h.persistence == nil || h.persistence.jobs == nil {
		return errors.New("River refresh recovery persistence is unavailable")
	}
	return h.persistence.jobs.RecoverExpiredRefreshJobs(ctx, refreshOrphanRecoveryPage)
}

func (m *Module) refreshPipelineJobHandler() jobs.Handler {
	persistence, _ := m.runs.(*postgresRunPersistence)
	return &refreshPipelineJobHandler{module: m, persistence: persistence}
}

// RecoverExpiredRefreshJobs coordinates the refresh-owned renewable lease
// with the platform-owned partition lock and River row. The advisory lock is
// held from the lease recheck through commit, so a live worker cannot race a
// rescue after renewing or reacquiring ownership.
func (a *PostgresJobsAdapter) RecoverExpiredRefreshJobs(ctx context.Context, limit int) error {
	if !a.Configured() {
		return errors.New("canonical PostgreSQL refresh jobs recovery is unavailable")
	}
	var after time.Time
	var afterID string
	for {
		candidates, err := a.Refresh.ListExpiredJobRunsAfter(ctx, limit, after, afterID)
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			return nil
		}
		after = candidates[len(candidates)-1].LeaseExpiresAt
		afterID = candidates[len(candidates)-1].RunID
		for _, candidate := range candidates {
			history, err := a.Jobs.Get(ctx, candidate.JobID)
			if err != nil {
				return err
			}
			release, head, err := a.Jobs.AcquirePartition(ctx, history)
			if err != nil {
				return err
			}
			if !head {
				continue
			}
			err = a.Refresh.InTx(ctx, func(tx refreshpostgres.Tx) error {
				locked, eligible, err := a.Refresh.LockExpiredJobRunTx(ctx, tx, candidate.RunID, candidate.JobID)
				if err != nil || !eligible {
					return err
				}
				_, err = a.Jobs.RescueExpiredRefreshJobTx(ctx, tx, locked.JobID, locked.RunID, locked.LeaseOwner, int(locked.FenceGeneration))
				return err
			})
			release()
			if err != nil {
				return err
			}
		}
		if len(candidates) < limit {
			return nil
		}
	}
}
