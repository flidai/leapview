package run

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/workload"
)

type QueueRepository interface {
	WorkflowRepository
	ListExecutableJobs(ctx context.Context, identity projectgraph.ServingIdentity, limit int) ([]JobRecord, error)
	ClaimExecutableJob(ctx context.Context, candidate JobRecord, owner string, lease time.Duration) (JobRecord, bool, error)
	RenewJobLease(ctx context.Context, job JobRecord, lease time.Duration) error
	JobQueueStats(ctx context.Context, identity projectgraph.ServingIdentity) (JobQueueStats, error)
}

type Dispatcher struct {
	Runs          QueueRepository
	Service       Service
	Admitter      workload.Admitter
	LeaseTimeout  time.Duration
	Logger        *slog.Logger
	Owner         string
	Identity      projectgraph.ServingIdentity
	WorkloadStats func() workload.Stats
	RunFinished   func(context.Context, JobRecord)
}

func (d Dispatcher) Run(ctx context.Context) {
	if d.Runs == nil {
		return
	}
	if d.Admitter == nil {
		d.Admitter, _ = workload.New(workload.DefaultConfig())
	}
	owner := d.Owner
	if owner == "" {
		owner = fmt.Sprintf("leapview-%d", time.Now().UnixNano())
	}
	for {
		queueStats, _ := d.Runs.JobQueueStats(ctx, d.Identity)
		candidates, err := d.Runs.ListExecutableJobs(ctx, d.Identity, 16)
		if err != nil {
			if d.Logger != nil {
				d.Logger.WarnContext(ctx, "list refresh job candidates failed", "error", err)
			}
			return
		}
		if len(candidates) == 0 {
			return
		}
		finished := make(chan bool, len(candidates))
		for _, candidate := range candidates {
			candidate := candidate
			go func() { finished <- d.dispatchCandidate(ctx, owner, candidate, queueStats) }()
		}
		claimed := false
		for range candidates {
			claimed = <-finished || claimed
		}
		if !claimed {
			return
		}
	}
}

func (d Dispatcher) dispatchCandidate(ctx context.Context, owner string, candidate JobRecord, queueStats JobQueueStats) bool {
	if candidate.PrincipalID == "" || candidate.EstimatedMemoryBytes <= 0 {
		if d.Logger != nil {
			d.Logger.WarnContext(ctx, "refresh job lacks durable actor or memory estimate", "job", candidate.ID)
		}
		return false
	}
	lease, err := d.admitter().Acquire(ctx, workload.Request{Class: workload.Refresh, PrincipalID: candidate.PrincipalID, GroupIDs: append([]string(nil), candidate.GroupIDs...), Operation: "materialization.refresh", EstimatedMemoryBytes: candidate.EstimatedMemoryBytes})
	if err != nil {
		if d.Logger != nil {
			d.Logger.InfoContext(ctx, "refresh admission deferred", "project", candidate.Identity.ProjectID, "generation", candidate.Identity.GenerationID, "run", candidate.RunID, "error", err)
		}
		return false
	}
	defer lease.Release()
	job, ok, err := d.Runs.ClaimExecutableJob(lease.Context(), candidate, owner, d.leaseTimeout())
	if err != nil {
		if d.Logger != nil {
			d.Logger.WarnContext(ctx, "claim refresh job failed", "job", candidate.ID, "error", err)
		}
		return false
	}
	if !ok {
		return false
	}
	if d.Logger != nil {
		stats := workload.Stats{}
		if d.WorkloadStats != nil {
			stats = d.WorkloadStats()
		}
		d.Logger.InfoContext(ctx, "dispatch refresh job",
			"project", job.Identity.ProjectID, "generation", job.Identity.GenerationID, "run", job.RunID, "kind", job.Kind,
			"queued_jobs", queueStats.QueuedJobs, "running_jobs", queueStats.RunningJobs,
			"stale_leased_jobs", queueStats.StaleLeasedJobs,
			"workload_running", stats.Running, "workload_queued", stats.Queued,
		)
	}
	executionCtx, cancelExecution := context.WithCancel(lease.Context())
	stopRenew := d.renewJobLease(executionCtx, job, cancelExecution)
	err = d.executeClaimedJob(executionCtx, job)
	stopRenew()
	cancelExecution()
	if err != nil && !errors.Is(err, ErrLeaseLost) && !errors.Is(err, context.Canceled) {
		_ = markRunFailedForWorker(context.Background(), d.Runs, job, err.Error())
	}
	lease.Release()
	d.notifyRunFinished(job)
	return true
}

func (d Dispatcher) notifyRunFinished(job JobRecord) {
	if d.RunFinished != nil {
		d.RunFinished(context.Background(), job)
	}
}

func (d Dispatcher) executeClaimedJob(ctx context.Context, job JobRecord) error {
	switch job.Kind {
	case JobKindRefreshPipeline:
		return d.Service.ExecuteClaimedJob(ctx, job)
	default:
		err := fmt.Errorf("unsupported refresh job kind %q", job.Kind)
		_ = markRunFailedForWorker(ctx, d.Runs, job, err.Error())
		return err
	}
}

func (d Dispatcher) renewJobLease(ctx context.Context, job JobRecord, cancel context.CancelFunc) func() {
	interval := d.leaseTimeout() / 2
	if interval <= 0 {
		interval = time.Second
	}
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				if err := d.Runs.RenewJobLease(context.WithoutCancel(ctx), job, d.leaseTimeout()); err != nil {
					cancel()
					if d.Logger != nil {
						d.Logger.WarnContext(ctx, "renew refresh job lease failed", "job", job.ID, "lease_revision", job.LeaseRevision, "error", err)
					}
					return
				}
			}
		}
	}()
	return func() {
		close(done)
	}
}

func (d Dispatcher) admitter() workload.Admitter {
	if d.Admitter != nil {
		return d.Admitter
	}
	controller, _ := workload.New(workload.DefaultConfig())
	return controller
}

func (d Dispatcher) leaseTimeout() time.Duration {
	if d.LeaseTimeout > 0 {
		return d.LeaseTimeout
	}
	return 2 * time.Minute
}
