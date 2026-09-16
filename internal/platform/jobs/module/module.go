// Package module composes River with LeapView's product-history and workload
// admission boundaries.
package module

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	jobpostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

type Config struct {
	Persistence *Persistence
	Production  bool
	Admission   jobs.Admitter
	// LeaseTimeout is the module fallback for handlers that do not publish a
	// narrower execution lease. It is not River's worker deadline; handlers
	// that can outlive a lease must renew their capability-owned fence.
	LeaseTimeout time.Duration
	// RiverJobTimeout is River's independent worker deadline. It must be
	// longer than renewable capability leases; zero uses River's default.
	RiverJobTimeout time.Duration
	PollInterval    time.Duration
	Logger          *slog.Logger
	OwnerID         string
}

type Module struct {
	repository  *jobpostgres.Repository
	persistence Persistence
	config      Config
	client      *river.Client[pgx.Tx]
	handlers    map[string]jobs.Handler
	// handlerLeaseTimeouts are the product execution fences for each handler.
	// River's JobTimeout is a worker-wide safety bound; it is not the lease
	// contract consumed by capability handlers.
	handlerLeaseTimeouts map[string]time.Duration
	refreshRecovery      refreshOrphanRecoveryHandler
	refreshRecoveryStop  context.CancelFunc
	refreshRecoveryDone  chan struct{}
	retryAt              sync.Map
	mu                   sync.RWMutex
	// afterRiverResultValidated is a deterministic test seam for the narrow
	// interval before River performs its separate worker-result transaction.
	afterRiverResultValidated func()
	// beforeStaleRiverWait is a deterministic test seam at the point where a
	// stale executor begins waiting for its successor to become terminal.
	beforeStaleRiverWait func()
}

var errWaitForStaleRiverClaim = errors.New("wait for stale River claim finalization")

const (
	approvalActivationKind        = "delivery.approval.activate"
	approvalActivationRescueAfter = 2 * time.Minute
	refreshPipelineKind           = "refresh_pipeline"
	riverDefaultRescueAfter       = time.Hour
	noDefaultRiverJobTimeout      = -1
)

type riverWorkerTiming struct {
	executionTimeout time.Duration
	rescueAfter      time.Duration
}

func Build(_ context.Context, config Config) (*Module, error) {
	if config.Persistence == nil {
		return nil, errors.New("jobs build requires injected PostgreSQL persistence")
	}
	if config.Production && !config.Persistence.isPostgres() {
		return nil, errors.New("production jobs build requires PostgreSQL persistence")
	}
	if err := config.Persistence.validate(); err != nil {
		return nil, err
	}
	if config.Admission == nil {
		return nil, errors.New("job admission is required")
	}
	return &Module{repository: config.Persistence.nativeRepository, persistence: *config.Persistence, config: config}, nil
}

func (m *Module) RegisterHandlers(handlers []jobs.Handler) error {
	if m == nil || m.repository == nil {
		return errors.New("job module is not initialized")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client != nil {
		return errors.New("job handlers are already registered")
	}
	registered := make(map[string]jobs.Handler, len(handlers))
	leaseTimeouts := make(map[string]time.Duration, len(handlers))
	var refreshRecovery refreshOrphanRecoveryHandler
	workers := river.NewWorkers()
	for _, handler := range handlers {
		if handler == nil || strings.TrimSpace(handler.Kind()) == "" {
			return errors.New("job handler kind is required")
		}
		if _, exists := registered[handler.Kind()]; exists {
			return errors.New("duplicate job handler " + handler.Kind())
		}
		registered[handler.Kind()] = handler
		if timeoutHandler, ok := handler.(interface{ LeaseTimeout() time.Duration }); ok && timeoutHandler.LeaseTimeout() > 0 {
			leaseTimeouts[handler.Kind()] = timeoutHandler.LeaseTimeout()
		}
		workerTiming, err := m.riverWorkerTiming(handler)
		if err != nil {
			return err
		}
		switch handler.Kind() {
		case "agent.run":
			river.AddWorker(workers, &agentRunWorker{riverWorkerDefaults: riverWorkerDefaults[jobpostgres.AgentRunArgs]{module: m, timing: workerTiming}})
		case "upload.finalize":
			river.AddWorker(workers, &uploadFinalizeWorker{riverWorkerDefaults: riverWorkerDefaults[jobpostgres.UploadFinalizeArgs]{module: m, timing: workerTiming}})
		case "release.finalize":
			river.AddWorker(workers, &releaseFinalizeWorker{riverWorkerDefaults: riverWorkerDefaults[jobpostgres.ReleaseFinalizeArgs]{module: m, timing: workerTiming}})
		case "deployment.activate":
			river.AddWorker(workers, &deploymentActivateWorker{riverWorkerDefaults: riverWorkerDefaults[jobpostgres.DeploymentActivateArgs]{module: m, timing: workerTiming}})
		case approvalActivationKind:
			river.AddWorker(workers, &approvalActivateWorker{riverWorkerDefaults: riverWorkerDefaults[jobpostgres.ApprovalActivateArgs]{module: m, timing: workerTiming}})
		case refreshPipelineKind:
			var ok bool
			refreshRecovery, ok = handler.(refreshOrphanRecoveryHandler)
			if !ok || refreshRecovery.LeaseTimeout() <= 0 {
				return errors.New("refresh pipeline handler requires orphan recovery authority")
			}
			river.AddWorker(workers, &refreshPipelineWorker{riverWorkerDefaults: riverWorkerDefaults[jobpostgres.RefreshPipelineArgs]{module: m, timing: workerTiming}})
		default:
			return errors.Join(jobs.ErrUnknownKind, errors.New(handler.Kind()))
		}
	}
	pool, err := m.repository.NativePool()
	if err != nil {
		return err
	}
	logger := m.config.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	riverConfig := &river.Config{
		ID: strings.TrimSpace(m.config.OwnerID), Logger: logger, MaxAttempts: jobpostgres.MaxAttempts,
		Queues: map[string]river.QueueConfig{"control": {MaxWorkers: 2}, "background": {MaxWorkers: 4}}, Workers: workers,
		// River's rescuer first applies this client-wide candidate horizon, then
		// each worker's Timeout. Disable the fallback and publish the prior
		// effective rescue horizon per long-running worker; approval activation
		// alone publishes the bounded horizon needed for prompt orphan recovery.
		JobTimeout: noDefaultRiverJobTimeout, RescueStuckJobsAfter: approvalActivationRescueAfter,
	}
	if m.config.PollInterval > 0 {
		riverConfig.FetchCooldown = m.config.PollInterval
		riverConfig.FetchPollInterval = m.config.PollInterval
	}
	client, err := river.NewClient(newResultFenceRiverDriver(pool), riverConfig)
	if err != nil {
		return err
	}
	if err := m.repository.ConfigureRiver(client); err != nil {
		return err
	}
	m.handlers = registered
	m.handlerLeaseTimeouts = leaseTimeouts
	m.refreshRecovery = refreshRecovery
	m.client = client
	return nil
}

func (m *Module) Start(ctx context.Context) error {
	if m == nil || m.client == nil {
		return errors.New("job module is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := m.client.Start(ctx); err != nil {
		return err
	}
	m.startRefreshOrphanRecovery()
	return nil
}

func (m *Module) Stop(ctx context.Context) error {
	if m == nil || m.client == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.stopRefreshOrphanRecovery()
	recoveryErr := m.waitRefreshOrphanRecovery(ctx)
	return errors.Join(recoveryErr, m.client.StopAndCancel(ctx))
}

func (m *Module) work(ctx context.Context, riverJobID int64, rowAttempt int, args jobpostgres.ExecutionArgs) error {
	history, err := m.repository.Get(ctx, args.ProductJobID)
	if err != nil {
		return river.JobCancel(errors.New("ASYNC_JOB_HISTORY_MISSING"))
	}
	if history.RequestDigest != "" && history.RequestDigest != args.RequestDigest {
		return river.JobCancel(errors.New("ASYNC_JOB_IDENTITY_MISMATCH"))
	}
	releasePartition, head, err := m.repository.AcquirePartition(ctx, history)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return river.JobSnooze(250 * time.Millisecond)
	}
	if !head {
		return river.JobSnooze(50 * time.Millisecond)
	}
	defer releasePartition()
	class := history.WorkloadClass
	if history.Kind == "refresh_pipeline" {
		class = "refresh"
	}
	lease, err := m.config.Admission.Acquire(ctx, jobs.AdmissionRequest{Class: class, PrincipalID: history.PrincipalID, GroupIDs: append([]string(nil), history.GroupIDs...), EstimatedMemoryBytes: history.EstimatedMemoryBytes, Operation: history.Kind})
	if err != nil || lease == nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return river.JobSnooze(250 * time.Millisecond)
	}
	defer lease.Release()
	history, err = m.repository.MarkRunning(lease.Context(), history.ID, rowAttempt)
	if err != nil {
		if errors.Is(err, jobpostgres.ErrStaleRiverClaim) {
			return errWaitForStaleRiverClaim
		}
		if ctx.Err() != nil || lease.Context().Err() != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return lease.Context().Err()
		}
		return river.JobCancel(errors.New("ASYNC_JOB_HISTORY_FAILED"))
	}
	// MarkRunning's read projects the exact River fence carried by the worker
	// context. Keep compatibility with an admission implementation that returns
	// a context without that value by filling only fields that remain absent.
	if history.LeaseOwner == "" {
		history.LeaseOwner = strings.TrimSpace(m.config.OwnerID)
	}
	if history.LeaseGeneration == 0 {
		history.LeaseGeneration = int64(rowAttempt)
	}
	m.mu.RLock()
	handler := m.handlers[history.Kind]
	m.mu.RUnlock()
	if handler == nil {
		return m.failTerminal(ctx, history.ID, history.Fence())
	}
	err = handler.Handle(lease.Context(), history)
	if err == nil {
		if jobpostgres.RiverCompletionDone(lease.Context()) {
			terminal, getErr := m.repository.Get(context.WithoutCancel(ctx), history.ID)
			if getErr == nil && (terminal.Status == jobs.StatusFailed || terminal.Status == jobs.StatusCancelled) {
				return river.JobCancel(errors.New("ASYNC_JOB_CANCELLED"))
			}
		} else {
			if err := m.repository.Complete(lease.Context(), history.ID, history.Fence()); err != nil {
				if errors.Is(err, jobpostgres.ErrStaleRiverClaim) {
					return errWaitForStaleRiverClaim
				}
				return river.JobCancel(errors.New("ASYNC_JOB_COMPLETION_FAILED"))
			}
		}
		return nil
	}
	if errors.Is(err, jobpostgres.ErrStaleRiverClaim) {
		return errWaitForStaleRiverClaim
	}
	// River cancels a worker context on shutdown, lease loss, and explicit
	// client cancellation. Preserve the row for replay in those cases instead
	// of converting infrastructure cancellation into a product failure.
	if ctx.Err() != nil || lease.Context().Err() != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return lease.Context().Err()
	}
	var retry *jobs.RetryError
	if errors.As(err, &retry) && rowAttempt < jobpostgres.MaxAttempts {
		if err := m.repository.RequeueAfterFailure(context.WithoutCancel(ctx), history.ID, rowAttempt, []byte(`{"code":"ASYNC_JOB_RETRY"}`)); err != nil {
			if errors.Is(err, jobpostgres.ErrStaleRiverClaim) {
				return errWaitForStaleRiverClaim
			}
			// Keep River's attempt available until product history has durably
			// moved back to queued. Without that transition, cancelling the row
			// would strand the product job in running with no recovery authority.
			// Returning an ordinary error lets River retry the persistence step.
			return errors.New("ASYNC_JOB_RETRY_PERSISTENCE_FAILED")
		}
		m.retryAt.Store(riverJobID, time.Now().Add(max(retry.Delay, time.Millisecond)))
		return errors.New("ASYNC_JOB_RETRY")
	}
	return m.failTerminal(ctx, history.ID, history.Fence())
}

// workAndWait keeps stale-successor waiting outside work's partition and
// admission scopes. work has returned before this branch runs, so all deferred
// releases have completed and a successor can acquire the same partition.
func (m *Module) workAndWait(ctx context.Context, riverJobID int64, rowAttempt int, args jobpostgres.ExecutionArgs) error {
	result := m.work(ctx, riverJobID, rowAttempt, args)
	if errors.Is(result, errWaitForStaleRiverClaim) {
		return m.waitForStaleRiverClaim(ctx, riverJobID)
	}
	return result
}

func (m *Module) failTerminal(ctx context.Context, id string, fence jobs.Fence) error {
	// Check the River fence before reading product terminal state. A stale
	// worker can otherwise observe a successor's terminal product update and
	// return JobCancel while that successor's River row is still running.
	if err := m.repository.ValidateCurrentClaim(context.WithoutCancel(ctx), id, fence); errors.Is(err, jobpostgres.ErrStaleRiverClaim) {
		return errWaitForStaleRiverClaim
	}
	history, err := m.repository.Get(context.WithoutCancel(ctx), id)
	if err == nil && (history.Status == jobs.StatusFailed || history.Status == jobs.StatusCancelled) {
		return river.JobCancel(errors.New("ASYNC_JOB_FAILED"))
	}
	if err := m.repository.Fail(context.WithoutCancel(ctx), id, fence, []byte(`{"code":"ASYNC_JOB_FAILED"}`)); err != nil {
		if errors.Is(err, jobpostgres.ErrStaleRiverClaim) {
			return errWaitForStaleRiverClaim
		}
		return river.JobCancel(errors.New("ASYNC_JOB_FAILURE_PERSISTENCE_FAILED"))
	}
	return river.JobCancel(errors.New("ASYNC_JOB_FAILED"))
}

func (m *Module) waitForStaleRiverClaim(ctx context.Context, riverJobID int64) error {
	// River has no attempt predicate on its worker result update. Wait for the
	// successor to reach a terminal state before this stale executor returns,
	// so the eventual ID-only result cannot finalize an active or re-claimable
	// successor. The repository keeps polling even if this worker's ordinary
	// context is cancelled while the successor is active.
	if m.beforeStaleRiverWait != nil {
		m.beforeStaleRiverWait()
	}
	return m.repository.WaitForRiverClaimFinalization(ctx, riverJobID)
}

func (m *Module) executionLeaseTimeout(kind string) time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if timeout := m.handlerLeaseTimeouts[kind]; timeout > 0 {
		return timeout
	}
	return m.config.LeaseTimeout
}

func (m *Module) riverJobTimeout() time.Duration {
	if m.config.RiverJobTimeout != 0 {
		return m.config.RiverJobTimeout
	}
	return river.JobTimeoutDefault
}

func (m *Module) riverWorkerTiming(handler jobs.Handler) (riverWorkerTiming, error) {
	executionTimeout := m.riverJobTimeout()
	if handler.Kind() != approvalActivationKind {
		rescueAfter := riverDefaultRescueAfter
		if m.config.RiverJobTimeout > 0 {
			rescueAfter = executionTimeout + riverDefaultRescueAfter
		} else if executionTimeout < 0 {
			rescueAfter = executionTimeout
		}
		return riverWorkerTiming{executionTimeout: executionTimeout, rescueAfter: rescueAfter}, nil
	}
	timeoutHandler, ok := handler.(interface{ LeaseTimeout() time.Duration })
	if !ok || timeoutHandler.LeaseTimeout() <= 0 || timeoutHandler.LeaseTimeout() > approvalActivationRescueAfter {
		return riverWorkerTiming{}, errors.New("approval activation handler requires a bounded execution lease")
	}
	return riverWorkerTiming{executionTimeout: timeoutHandler.LeaseTimeout(), rescueAfter: approvalActivationRescueAfter}, nil
}

func workTyped[T river.JobArgs](ctx context.Context, m *Module, job *river.Job[T], kind string, args jobpostgres.ExecutionArgs) error {
	owner := strings.TrimSpace(m.config.OwnerID)
	if owner == "" && len(job.AttemptedBy) > 0 {
		owner = strings.TrimSpace(job.AttemptedBy[len(job.AttemptedBy)-1])
	}
	if err := jobpostgres.SetRiverResultFence(ctx, job.Attempt, owner); err != nil {
		// A result without the metadata fence cannot be allowed to reach River's
		// ID-only finalizer. Fail safe until the row is terminal.
		return m.waitForStaleRiverClaim(ctx, job.ID)
	}
	executionCtx := jobpostgres.ContextWithRiverExecution(ctx, job, owner, m.executionLeaseTimeout(kind))
	result := m.workAndWait(executionCtx, job.ID, job.Attempt, args)
	return m.protectRiverResult(ctx, job.ID, jobs.Fence{Owner: owner, Generation: int64(job.Attempt)}, result)
}

func (m *Module) protectRiverResult(ctx context.Context, riverJobID int64, fence jobs.Fence, result error) error {
	// River v0.47 reports worker results by job ID and running state, without
	// an attempt predicate. Recheck every result at the outermost worker
	// boundary so cancellation and indeterminate persistence errors cannot
	// bypass the capability-specific stale-claim branches above.
	waitCtx := context.WithoutCancel(ctx)
	for {
		probeCtx, cancel := context.WithTimeout(waitCtx, time.Second)
		err := m.repository.ValidateRiverResultClaim(probeCtx, riverJobID, fence)
		cancel()
		if err == nil {
			if m.afterRiverResultValidated != nil {
				m.afterRiverResultValidated()
			}
			return result
		}
		if errors.Is(err, jobpostgres.ErrStaleRiverClaim) {
			return m.waitForStaleRiverClaim(ctx, riverJobID)
		}
		// The current state is unknown. Returning would permit an ID-only
		// finalizer to race a recovered connection, so retry the proof.
		time.Sleep(50 * time.Millisecond)
	}
}

type riverWorkerDefaults[T river.JobArgs] struct {
	river.WorkerDefaults[T]
	module *Module
	timing riverWorkerTiming
}

func (w riverWorkerDefaults[T]) NextRetry(job *river.Job[T]) time.Time {
	if retryAt, ok := w.module.retryAt.LoadAndDelete(job.ID); ok {
		return retryAt.(time.Time)
	}
	return time.Time{}
}

func (w riverWorkerDefaults[T]) Timeout(*river.Job[T]) time.Duration { return w.timing.rescueAfter }

func (w riverWorkerDefaults[T]) work(ctx context.Context, job *river.Job[T], kind string, args jobpostgres.ExecutionArgs) error {
	if w.timing.executionTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, w.timing.executionTimeout)
		defer cancel()
	}
	return workTyped(ctx, w.module, job, kind, args)
}

type agentRunWorker struct {
	riverWorkerDefaults[jobpostgres.AgentRunArgs]
}

func (w *agentRunWorker) Work(ctx context.Context, j *river.Job[jobpostgres.AgentRunArgs]) error {
	return w.work(ctx, j, "agent.run", jobpostgres.ExecutionArgs(j.Args))
}

type uploadFinalizeWorker struct {
	riverWorkerDefaults[jobpostgres.UploadFinalizeArgs]
}

func (w *uploadFinalizeWorker) Work(ctx context.Context, j *river.Job[jobpostgres.UploadFinalizeArgs]) error {
	return w.work(ctx, j, "upload.finalize", jobpostgres.ExecutionArgs(j.Args))
}

type releaseFinalizeWorker struct {
	riverWorkerDefaults[jobpostgres.ReleaseFinalizeArgs]
}

func (w *releaseFinalizeWorker) Work(ctx context.Context, j *river.Job[jobpostgres.ReleaseFinalizeArgs]) error {
	return w.work(ctx, j, "release.finalize", jobpostgres.ExecutionArgs(j.Args))
}

type deploymentActivateWorker struct {
	riverWorkerDefaults[jobpostgres.DeploymentActivateArgs]
}

func (w *deploymentActivateWorker) Work(ctx context.Context, j *river.Job[jobpostgres.DeploymentActivateArgs]) error {
	return w.work(ctx, j, "deployment.activate", jobpostgres.ExecutionArgs(j.Args))
}

type approvalActivateWorker struct {
	riverWorkerDefaults[jobpostgres.ApprovalActivateArgs]
}

func (w *approvalActivateWorker) Work(ctx context.Context, j *river.Job[jobpostgres.ApprovalActivateArgs]) error {
	return w.work(ctx, j, approvalActivationKind, jobpostgres.ExecutionArgs(j.Args))
}

type refreshPipelineWorker struct {
	riverWorkerDefaults[jobpostgres.RefreshPipelineArgs]
}

func (w *refreshPipelineWorker) Work(ctx context.Context, j *river.Job[jobpostgres.RefreshPipelineArgs]) error {
	return w.work(ctx, j, refreshPipelineKind, jobpostgres.ExecutionArgs(j.Args))
}

func (m *Module) Enqueue(ctx context.Context, input jobs.EnqueueInput) (jobs.Job, error) {
	m.mu.RLock()
	_, ok := m.handlers[input.Kind]
	configured := m.handlers != nil
	m.mu.RUnlock()
	if !configured || !ok {
		return jobs.Job{}, errors.Join(jobs.ErrUnknownKind, errors.New(input.Kind))
	}
	return m.repository.Enqueue(ctx, input)
}
func (m *Module) Get(ctx context.Context, id string) (jobs.Job, error) {
	return m.repository.Get(ctx, id)
}
func (m *Module) Cancel(ctx context.Context, id string) error { return m.repository.Cancel(ctx, id) }
func (m *Module) AppendEvent(ctx context.Context, kind, id, event string, data []byte) (jobs.Event, error) {
	return m.repository.AppendEvent(ctx, kind, id, event, data)
}
func (m *Module) ListEvents(ctx context.Context, kind, id string, after int64, limit int) ([]jobs.Event, error) {
	return m.repository.ListEvents(ctx, kind, id, after, limit)
}

func (m *Module) validateWorkflowJob(intent jobs.WorkflowIntent) error {
	if intent.Job.ID == "" {
		return nil
	}
	m.mu.RLock()
	_, ok := m.handlers[intent.Job.Kind]
	configured := m.handlers != nil
	m.mu.RUnlock()
	if !configured || !ok {
		return errors.Join(jobs.ErrUnknownKind, errors.New(intent.Job.Kind))
	}
	return nil
}
func (m *Module) RecordWorkflowTx(ctx context.Context, tx jobpostgres.Tx, intent jobs.WorkflowIntent) error {
	if m == nil {
		return errors.New("native PostgreSQL workflow is unavailable")
	}
	if err := m.validateWorkflowJob(intent); err != nil {
		return err
	}
	return m.repository.RecordWorkflow(ctx, tx, intent)
}
func (m *Module) CommitWorkflow(ctx context.Context, intent jobs.WorkflowIntent) error {
	if m == nil {
		return jobs.ErrStoreRequired
	}
	if err := m.validateWorkflowJob(intent); err != nil {
		return err
	}
	return m.repository.CommitWorkflow(ctx, intent)
}
