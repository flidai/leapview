package connectionbinding

import (
	"context"
	"errors"
	"fmt"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"log/slog"
	"strings"
	"sync"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	"golang.org/x/sync/singleflight"
)

type RuntimePool interface {
	HealthCheck(context.Context) error
	Close() error
}

// ContextRuntimePool is implemented by production pools whose forced
// retirement can cancel in-flight provider work and obey the caller's bound.
type ContextRuntimePool interface {
	RuntimePool
	CloseContext(context.Context) error
}

type RuntimePoolFactory interface {
	Prepare(context.Context, TargetBinding, CredentialSnapshot) (RuntimePool, error)
}

type BindingStateStore interface {
	Save(context.Context, TargetBinding, int64) (TargetBinding, error)
}

type PoolManagerConfig struct {
	Binding    TargetBinding
	Resolver   CredentialResolver
	Factory    RuntimePoolFactory
	Store      BindingStateStore
	Audit      RotationAuditRecorder
	Logger     *slog.Logger
	Now        func() time.Time
	StaleAfter time.Duration
	Schedule   RefreshSchedule
}

type PoolManager struct {
	resolver CredentialResolver
	factory  RuntimePoolFactory
	store    BindingStateStore
	audit    RotationAuditRecorder
	logger   *slog.Logger
	now      func() time.Time
	stale    time.Duration
	schedule RefreshSchedule

	refreshGroup       singleflight.Group
	refreshMu          sync.Mutex
	mu                 sync.Mutex
	binding            TargetBinding
	active             *poolGeneration
	lastRun            time.Time
	retired            bool
	retireOnce         sync.Once
	retireOnceComplete sync.Once
	retireDone         chan struct{}
	retireErr          error
	retireCompleted    bool
	retireForced       bool
	retireGen          *poolGeneration
	retireCtx          context.Context
	retireCancel       context.CancelFunc
	refreshes          int
	refreshDone        chan struct{}
}

// NoAuthProviderVersion is the canonical non-secret evidence version for a
// public connection binding. It intentionally has no credential snapshot.
const NoAuthProviderVersion = "public-no-auth:v1"

// NoAuthCredentialResolver satisfies the pool manager's infrastructure
// boundary without consulting a target credential provider.
type NoAuthCredentialResolver struct{}

func (NoAuthCredentialResolver) Resolve(_ context.Context, _ CredentialReference) (CredentialSnapshot, error) {
	return NewNoAuthCredentialSnapshot(time.Now()), nil
}

type poolGeneration struct {
	pool            RuntimePool
	version         string
	bindingRevision int64
	leases          int
	draining        bool
	closeOnce       sync.Once
	closeErr        error
}

func NewPoolManager(config PoolManagerConfig) (*PoolManager, error) {
	if err := config.Binding.Validate(); err != nil {
		return nil, err
	}
	if config.Resolver == nil || config.Factory == nil || config.Store == nil || config.Now == nil || config.StaleAfter <= 0 {
		return nil, fmt.Errorf("%w: resolver, pool factory, binding store, clock, and stale policy are required", ErrInvalidBinding)
	}
	if config.Audit == nil {
		return nil, fmt.Errorf("%w: recorder is required", ErrRotationAuditUnavailable)
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	retireCtx, retireCancel := context.WithCancel(context.Background())
	refreshDone := make(chan struct{})
	close(refreshDone)
	return &PoolManager{
		resolver: config.Resolver, factory: config.Factory, store: config.Store,
		audit: config.Audit, logger: logger, now: config.Now, stale: config.StaleAfter,
		schedule: config.Schedule, binding: config.Binding, retireDone: make(chan struct{}),
		retireCtx: retireCtx, retireCancel: retireCancel, refreshDone: refreshDone,
	}, nil
}

func (manager *PoolManager) RefreshNow(ctx context.Context) error {
	return manager.Refresh(ctx, RefreshRequest{Actor: "runtime:" + manager.targetID(), Operation: RefreshRequested})
}

func (manager *PoolManager) Refresh(ctx context.Context, request RefreshRequest) error {
	if manager == nil {
		return ErrProviderUnavailable
	}
	if !request.valid() {
		return fmt.Errorf("%w: refresh actor and operation are required", ErrInvalidBinding)
	}
	refreshCtx, finish, admitted := manager.admitRefresh(ctx)
	if !admitted {
		return ErrProviderUnavailable
	}
	defer finish()
	_, err, _ := manager.refreshGroup.Do("refresh", func() (any, error) {
		return nil, manager.refresh(refreshCtx, request)
	})
	if manager.isRetired() {
		return ErrProviderUnavailable
	}
	return err
}

func (manager *PoolManager) Run(ctx context.Context) error {
	if manager == nil {
		return ErrProviderUnavailable
	}
	if err := manager.schedule.validate(); err != nil {
		return err
	}
	failures := 0
	for {
		err := manager.Refresh(ctx, RefreshRequest{
			Actor: "runtime:" + manager.targetID(), Operation: RefreshScheduled,
		})
		if manager.isRetired() {
			return ErrProviderUnavailable
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		var base time.Duration
		if err == nil {
			failures = 0
			base = manager.schedule.Interval
		} else {
			failures++
			base = manager.schedule.backoff(failures)
		}
		if err := manager.schedule.Wait(ctx, manager.schedule.delay(base)); err != nil {
			return err
		}
	}
}

func (manager *PoolManager) refresh(ctx context.Context, request RefreshRequest) error {
	manager.refreshMu.Lock()
	defer manager.refreshMu.Unlock()
	now := manager.now().UTC()

	manager.mu.Lock()
	if manager.retired {
		manager.mu.Unlock()
		return ErrProviderUnavailable
	}
	if !manager.binding.Enabled {
		manager.mu.Unlock()
		return ErrDisabledBinding
	}
	binding := manager.binding
	manager.mu.Unlock()

	var snapshot CredentialSnapshot
	var err error
	if binding.AuthenticationMode == AuthenticationNone {
		snapshot = NewNoAuthCredentialSnapshot(now)
	} else {
		snapshot, err = manager.resolver.Resolve(ctx, binding.CredentialReference)
	}
	if err != nil {
		manager.recordRefresh(now)
		if isContextError(err) || ctx.Err() != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return err
		}
		reason := providerFailureReason(err)
		result := manager.degrade(ctx, reason, now, err)
		return manager.withAudit(ctx, request, RotationDegraded, binding.ValidatedVersion, reason, now, result)
	}
	defer snapshot.Destroy()
	version := snapshot.ProviderVersion()

	manager.mu.Lock()
	if manager.retired {
		manager.mu.Unlock()
		return ErrProviderUnavailable
	}
	if manager.active != nil && manager.active.version == version {
		manager.mu.Unlock()
		validated, err := binding.MarkValidated(version, now)
		if err != nil {
			return err
		}
		if validated.Revision != binding.Revision {
			manager.mu.Lock()
			if manager.retired || manager.binding.Revision != binding.Revision {
				manager.mu.Unlock()
				return ErrProviderUnavailable
			}
			manager.mu.Unlock()
			saved, err := manager.store.Save(ctx, validated, binding.Revision)
			if err != nil {
				return err
			}
			manager.mu.Lock()
			if manager.retired || manager.binding.Revision != binding.Revision {
				manager.mu.Unlock()
				return ErrProviderUnavailable
			}
			manager.binding = saved
			if manager.active != nil {
				manager.active.bindingRevision = saved.Revision
			}
			manager.lastRun = now
			manager.mu.Unlock()
		} else {
			manager.recordRefresh(now)
		}
		return manager.withAudit(ctx, request, RotationUnchanged, version, "", now, nil)
	}
	manager.mu.Unlock()

	replacement, err := manager.factory.Prepare(ctx, binding, snapshot)
	if err != nil {
		if isContextError(err) || ctx.Err() != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return err
		}
		manager.recordRefresh(now)
		result := manager.degrade(ctx, "POOL_PREPARE_FAILED", now, ErrInvalidCredentialBundle)
		return manager.withAudit(ctx, request, RotationDegraded, version, "POOL_PREPARE_FAILED", now, result)
	}
	if replacement == nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		manager.recordRefresh(now)
		result := manager.degrade(ctx, "POOL_PREPARE_FAILED", now, ErrInvalidCredentialBundle)
		return manager.withAudit(ctx, request, RotationDegraded, version, "POOL_PREPARE_FAILED", now, result)
	}
	if err := replacement.HealthCheck(ctx); err != nil {
		_ = closeRuntimePool(ctx, replacement)
		if isContextError(err) || ctx.Err() != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return err
		}
		manager.recordRefresh(now)
		result := manager.degrade(ctx, "POOL_HEALTH_CHECK_FAILED", now, ErrInvalidCredentialBundle)
		return manager.withAudit(ctx, request, RotationDegraded, version, "POOL_HEALTH_CHECK_FAILED", now, result)
	}

	validated, err := binding.MarkValidated(version, now)
	if err != nil {
		_ = closeRuntimePool(ctx, replacement)
		return err
	}
	manager.mu.Lock()
	if manager.retired {
		manager.mu.Unlock()
		_ = closeRuntimePool(ctx, replacement)
		return ErrProviderUnavailable
	}
	if !manager.binding.Enabled || manager.binding.Revision != binding.Revision {
		manager.mu.Unlock()
		_ = closeRuntimePool(ctx, replacement)
		return ErrIncompatibleBinding
	}
	manager.mu.Unlock()
	saved, err := manager.store.Save(ctx, validated, binding.Revision)
	if err != nil {
		_ = closeRuntimePool(ctx, replacement)
		return err
	}
	manager.mu.Lock()
	if manager.retired || !manager.binding.Enabled || manager.binding.Revision != binding.Revision {
		manager.mu.Unlock()
		_ = closeRuntimePool(ctx, replacement)
		return ErrProviderUnavailable
	}
	previous := manager.active
	manager.binding = saved
	manager.active = &poolGeneration{
		pool: replacement, version: version, bindingRevision: saved.Revision,
	}
	manager.lastRun = now
	closePrevious := markDraining(previous)
	manager.mu.Unlock()
	if closePrevious != nil {
		_ = closeGeneration(closePrevious)
	}
	return manager.withAudit(ctx, request, RotationActivated, version, "", now, nil)
}

func (manager *PoolManager) targetID() string {
	if manager == nil {
		return ""
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.binding.TargetID.String()
}

func (manager *PoolManager) withAudit(
	ctx context.Context,
	request RefreshRequest,
	outcome RotationOutcome,
	version string,
	reason string,
	timestamp time.Time,
	result error,
) error {
	if manager.audit == nil {
		return errors.Join(result, ErrRotationAuditUnavailable)
	}
	manager.mu.Lock()
	binding := manager.binding
	manager.mu.Unlock()
	event := RotationAuditEvent{
		BindingID: binding.ID, ConnectionID: binding.ConnectionID, TargetID: binding.TargetID, ProjectID: binding.Scope.ProjectID,
		Identity:        projectgraph.ServingIdentity{ProjectID: binding.Scope.ProjectID, Environment: binding.Scope.Environment},
		ProviderVersion: version,
		Actor:           strings.TrimSpace(request.Actor), Operation: request.Operation,
		Timestamp: timestamp.UTC(), Outcome: outcome, Reason: reason,
	}
	operationID, command := rotationOperationID(request.Operation)
	if !command {
		if err := manager.audit.RecordCredentialRotation(context.WithoutCancel(ctx), event); err != nil {
			manager.logger.ErrorContext(ctx, "best-effort credential rotation audit failed", "operation", request.Operation, "outcome", outcome, "project_id", binding.Scope.ProjectID.String(), "principal", strings.TrimSpace(request.Actor), "binding_id", binding.ID.String(), "target_id", binding.TargetID.String(), "reason", reason, "error", err)
		}
		return result
	}
	executor, err := apigencommand.NewExecutor(analyticsgen.GetAPIGenCommandRuntimeContract, manager.logger)
	if err != nil {
		return errors.Join(result, err)
	}
	err = executor.Execute(ctx, operationID, apigencommand.Execution{
		BestEffortAudit: func(context.Context, apigencommand.Contract) error {
			return manager.audit.RecordCredentialRotation(context.WithoutCancel(ctx), event)
		},
		LogMessage: "best-effort credential rotation audit failed",
	})
	return errors.Join(result, err)
}

func rotationOperationID(operation RefreshOperation) (string, bool) {
	switch operation {
	case RefreshTest:
		return string(analyticsgen.GenOperationTestTargetConnectionBinding), true
	case RefreshRequested:
		return string(analyticsgen.GenOperationRefreshTargetConnectionBinding), true
	default:
		return "", false
	}
}

func (manager *PoolManager) recordRefresh(now time.Time) {
	manager.mu.Lock()
	manager.lastRun = now
	manager.mu.Unlock()
}

func (manager *PoolManager) degrade(ctx context.Context, reason string, now time.Time, result error) error {
	manager.mu.Lock()
	if manager.retired {
		manager.mu.Unlock()
		return errors.Join(result, ErrProviderUnavailable)
	}
	binding := manager.binding
	manager.mu.Unlock()
	degraded, err := binding.MarkDegraded(reason, now)
	if err != nil {
		return result
	}
	if degraded.Revision == binding.Revision {
		manager.mu.Lock()
		manager.lastRun = now
		manager.mu.Unlock()
		return result
	}
	saved, err := manager.store.Save(ctx, degraded, binding.Revision)
	if err != nil {
		return errors.Join(result, err)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.retired || manager.binding.Revision != binding.Revision {
		return errors.Join(result, ErrProviderUnavailable)
	}
	manager.binding = saved
	manager.lastRun = now
	return result
}

func (manager *PoolManager) Lease() (*PoolLease, error) {
	if manager == nil {
		return nil, ErrProviderUnavailable
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.retired {
		return nil, ErrProviderUnavailable
	}
	if !manager.binding.Enabled {
		return nil, ErrDisabledBinding
	}
	if manager.active == nil {
		return nil, ErrCredentialNotFound
	}
	if manager.binding.Health == HealthDegraded && manager.now().UTC().Sub(manager.binding.LastValidatedAt) > manager.stale {
		return nil, ErrProviderUnavailable
	}
	manager.active.leases++
	evidence := manager.binding.Evidence()
	evidence.BindingRevision = manager.active.bindingRevision
	evidence.ValidatedVersion = manager.active.version
	return &PoolLease{
		manager: manager, generation: manager.active, evidence: evidence,
	}, nil
}

func (manager *PoolManager) Disable(ctx context.Context, now time.Time) error {
	if manager == nil {
		return ErrProviderUnavailable
	}
	manager.refreshMu.Lock()
	defer manager.refreshMu.Unlock()
	manager.mu.Lock()
	binding := manager.binding
	if manager.retired {
		manager.mu.Unlock()
		return ErrProviderUnavailable
	}
	manager.mu.Unlock()
	disabled, err := binding.Disable(now)
	if err != nil {
		return err
	}
	if disabled.Revision == binding.Revision {
		return nil
	}
	saved, err := manager.store.Save(ctx, disabled, binding.Revision)
	if err != nil {
		return err
	}
	manager.mu.Lock()
	if manager.retired || manager.binding.Revision != binding.Revision {
		manager.mu.Unlock()
		return ErrProviderUnavailable
	}
	manager.binding = saved
	previous := manager.active
	manager.active = nil
	closePrevious := markDraining(previous)
	manager.mu.Unlock()
	if closePrevious != nil {
		return closeGeneration(closePrevious)
	}
	return nil
}

// DisableBounded persists the disabled binding revision, fences all new pool
// work, and bounds the lifetime of leases that still hold credential-bearing
// runtime state. A timeout or cancellation is returned only after the pool has
// been force-closed, so callers can safely persist an incomplete recovery
// checkpoint without leaving the old principal eligible for new work.
func (manager *PoolManager) DisableBounded(ctx context.Context, now, deadline time.Time) error {
	if manager == nil || ctx == nil {
		return ErrProviderUnavailable
	}
	if deadline.IsZero() {
		return fmt.Errorf("%w: retirement deadline is required", ErrInvalidBinding)
	}
	manager.beginRetirement()
	finishRetirement := func(result error) error {
		return errors.Join(result, manager.retireBounded(ctx, deadline))
	}
	if err := manager.waitForRefreshExit(ctx, deadline); err != nil {
		return finishRetirement(err)
	}
	manager.mu.Lock()
	binding := manager.binding
	disabled, err := binding.Disable(now)
	if err != nil {
		manager.mu.Unlock()
		return finishRetirement(err)
	}
	manager.mu.Unlock()
	if disabled.Revision != binding.Revision {
		saved, saveErr := manager.store.Save(ctx, disabled, binding.Revision)
		if saveErr != nil {
			return finishRetirement(saveErr)
		}
		manager.mu.Lock()
		if manager.binding.Revision != binding.Revision {
			manager.mu.Unlock()
			return finishRetirement(ErrIncompatibleBinding)
		}
		manager.binding = saved
		manager.mu.Unlock()
	}
	return finishRetirement(nil)
}

func (manager *PoolManager) waitForRefreshExit(ctx context.Context, deadline time.Time) error {
	manager.mu.Lock()
	done := manager.refreshDone
	manager.mu.Unlock()
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return context.DeadlineExceeded
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return context.DeadlineExceeded
	}
}

// Retire removes this manager from service without changing persisted binding
// metadata. Existing leases may finish; the retired generation closes as soon
// as its final lease is released.
func (manager *PoolManager) Retire() error {
	if manager == nil {
		return nil
	}
	_, started := manager.beginRetirement()
	if !started {
		// Retire historically returned immediately for an already-retired
		// manager. Keep that idempotent behavior; callers that need to await
		// readers should use RetireBounded.
		return nil
	}
	return manager.tryCompleteRetirement()
}

// RetireBounded fences new work immediately, then waits for existing leases to
// drain until deadline. If the deadline or ctx is reached first, the retired
// runtime pool is force-closed before this method returns. The explicit
// deadline is intentionally required so a caller cannot accidentally wait
// forever while credential-bearing runtime state remains reachable.
//
// A cancellation is fail-closed: the pool is force-closed and the cancellation
// error is returned after retirement has completed. Repeated calls do not
// reopen or close another generation; a call made after completion returns the
// recorded close result (or nil).
func (manager *PoolManager) RetireBounded(ctx context.Context, deadline time.Time) error {
	if manager == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if deadline.IsZero() {
		return fmt.Errorf("%w: retirement deadline is required", ErrInvalidBinding)
	}
	return manager.retireBounded(ctx, deadline)
}

func (manager *PoolManager) retireBounded(ctx context.Context, deadline time.Time) error {
	_, _ = manager.beginRetirement()
	if err := manager.tryCompleteRetirement(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return errors.Join(ctxErr, err)
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		closeErr := manager.forceRetiredContext(ctx)
		return manager.awaitForcedRetirement(err, closeErr, deadline)
	}

	select {
	case <-manager.retireDone:
		return manager.retirementResult()
	default:
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		forceContext, cancel := context.WithDeadline(context.Background(), deadline)
		defer cancel()
		closeErr := manager.forceRetiredContext(forceContext)
		return errors.Join(context.DeadlineExceeded, closeErr)
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-manager.retireDone:
		return manager.retirementResult()
	case <-ctx.Done():
		closeErr := manager.forceRetiredContext(ctx)
		return manager.awaitForcedRetirement(ctx.Err(), closeErr, deadline)
	case <-timer.C:
		forceContext, cancel := context.WithDeadline(context.Background(), deadline)
		defer cancel()
		closeErr := manager.forceRetiredContext(forceContext)
		return errors.Join(context.DeadlineExceeded, closeErr)
	}
}

func (manager *PoolManager) awaitForcedRetirement(reason, closeErr error, deadline time.Time) error {
	select {
	case <-manager.retireDone:
		return errors.Join(reason, manager.retirementResult())
	default:
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return errors.Join(reason, closeErr)
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-manager.retireDone:
		return errors.Join(reason, manager.retirementResult())
	case <-timer.C:
		return errors.Join(reason, closeErr)
	}
}

// beginRetirement is the single retirement fence. It deliberately does not
// take refreshMu: callers must stop admitting leases as soon as retirement is
// requested, even if a refresh is currently blocked in provider or pool work.
func (manager *PoolManager) beginRetirement() (*poolGeneration, bool) {
	started := false
	manager.retireOnce.Do(func() {
		started = true
		manager.mu.Lock()
		if manager.retireDone == nil {
			manager.retireDone = make(chan struct{})
		}
		manager.retired = true
		manager.retireGen = manager.active
		manager.active = nil
		if manager.retireGen != nil {
			manager.retireGen.draining = true
		}
		cancel := manager.retireCancel
		manager.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	})

	manager.mu.Lock()
	generation := manager.retireGen
	manager.mu.Unlock()
	return generation, started
}

func (manager *PoolManager) forceRetiredContext(ctx context.Context) error {
	manager.mu.Lock()
	manager.retireForced = true
	generation := manager.retireGen
	manager.mu.Unlock()
	closeErr := closeGenerationContext(ctx, generation)
	if completeErr := manager.tryCompleteRetirement(); completeErr != nil {
		return completeErr
	}
	return closeErr
}

// tryCompleteRetirement closes the retired generation only after all admitted
// refresh callers have exited. A forced retirement may close a generation
// while leases are still held, but it still waits for refresh work to stop
// before publishing completion to callers.
func (manager *PoolManager) tryCompleteRetirement() error {
	manager.mu.Lock()
	if !manager.retired || manager.retireCompleted || manager.refreshes != 0 {
		manager.mu.Unlock()
		return nil
	}
	generation := manager.retireGen
	forced := manager.retireForced
	if !forced && generation != nil && generation.leases != 0 {
		manager.mu.Unlock()
		return nil
	}
	manager.mu.Unlock()

	closeErr := closeGeneration(generation)
	manager.completeRetirement(closeErr)
	return closeErr
}

func (manager *PoolManager) completeRetirement(closeErr error) {
	// The channel is closed exactly once even when a final Release races the
	// bounded deadline or cancellation path. Store the result in that same
	// critical section so readers never observe a closed channel with a stale
	// error value.
	manager.retireOnceComplete.Do(func() {
		manager.mu.Lock()
		manager.retireErr = closeErr
		manager.retireCompleted = true
		done := manager.retireDone
		manager.mu.Unlock()
		close(done)
	})
}

func (manager *PoolManager) retirementResult() error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.retireErr
}

func (manager *PoolManager) admitRefresh(ctx context.Context) (context.Context, func(), bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	manager.mu.Lock()
	if manager.retired {
		manager.mu.Unlock()
		return nil, nil, false
	}
	if manager.refreshes == 0 {
		manager.refreshDone = make(chan struct{})
	}
	manager.refreshes++
	retireCtx := manager.retireCtx
	manager.mu.Unlock()

	refreshCtx, cancel := context.WithCancel(ctx)
	stop := func() bool { return true }
	if retireCtx != nil {
		stop = context.AfterFunc(retireCtx, cancel)
	}
	return refreshCtx, func() {
		stop()
		cancel()
		manager.finishRefresh()
	}, true
}

func (manager *PoolManager) finishRefresh() {
	manager.mu.Lock()
	if manager.refreshes > 0 {
		manager.refreshes--
	}
	if manager.refreshes == 0 && manager.refreshDone != nil {
		close(manager.refreshDone)
	}
	retired := manager.retired
	manager.mu.Unlock()
	if retired {
		_ = manager.tryCompleteRetirement()
	}
}

func (manager *PoolManager) isRetired() bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.retired
}

func (manager *PoolManager) Evidence() BindingEvidence {
	if manager == nil {
		return BindingEvidence{}
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.binding.Evidence()
}

func (manager *PoolManager) HealthStatus() BindingHealthStatus {
	if manager == nil {
		return BindingHealthStatus{}
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	binding := manager.binding
	status := BindingHealthStatus{
		BindingID: binding.ID, TargetID: binding.TargetID,
		ConnectionID: binding.ConnectionID, ConnectorKind: binding.ConnectorKind,
		Scope: binding.Scope, BindingRevision: binding.Revision,
		ValidatedVersion: binding.ValidatedVersion, Health: binding.Health, DiagnosticCode: binding.HealthReason,
		LastAttemptAt: manager.lastRun, LastValidatedAt: binding.LastValidatedAt,
		HasActivePool: manager.active != nil,
	}
	if !binding.LastValidatedAt.IsZero() {
		age := manager.now().UTC().Sub(binding.LastValidatedAt)
		if age > 0 {
			status.StaleAgeSeconds = int64(age / time.Second)
		}
	}
	return status
}

type PoolLease struct {
	once       sync.Once
	manager    *PoolManager
	generation *poolGeneration
	evidence   BindingEvidence
}

func (lease *PoolLease) Pool() RuntimePool {
	if lease == nil || lease.generation == nil {
		return nil
	}
	return lease.generation.pool
}

func (lease *PoolLease) Evidence() BindingEvidence {
	if lease == nil {
		return BindingEvidence{}
	}
	return lease.evidence
}

func (lease *PoolLease) Release() {
	if lease == nil {
		return
	}
	lease.once.Do(func() {
		manager := lease.manager
		if manager == nil {
			return
		}
		manager.mu.Lock()
		generation := lease.generation
		if generation == nil {
			manager.mu.Unlock()
			return
		}
		if generation.leases > 0 {
			generation.leases--
		}
		closing := generation.draining && generation.leases == 0
		retiring := manager.retired && manager.retireGen == generation
		manager.mu.Unlock()
		if closing {
			_ = closeGeneration(generation)
		}
		if retiring {
			_ = manager.tryCompleteRetirement()
		}
	})
}

func markDraining(generation *poolGeneration) *poolGeneration {
	if generation == nil {
		return nil
	}
	generation.draining = true
	if generation.leases == 0 {
		return generation
	}
	return nil
}

func closeGeneration(generation *poolGeneration) error {
	if generation == nil || generation.pool == nil {
		return nil
	}
	generation.closeOnce.Do(func() {
		generation.closeErr = generation.pool.Close()
	})
	return generation.closeErr
}

func closeGenerationContext(ctx context.Context, generation *poolGeneration) error {
	if generation == nil || generation.pool == nil {
		return nil
	}
	generation.closeOnce.Do(func() {
		generation.closeErr = closeRuntimePool(ctx, generation.pool)
	})
	return generation.closeErr
}

func closeRuntimePool(ctx context.Context, pool RuntimePool) error {
	if pool == nil {
		return nil
	}
	if bounded, ok := pool.(ContextRuntimePool); ok {
		return bounded.CloseContext(ctx)
	}
	return pool.Close()
}

func providerFailureReason(err error) string {
	switch {
	case errors.Is(err, ErrCredentialDenied):
		return "PROVIDER_ACCESS_DENIED"
	case errors.Is(err, ErrCredentialNotFound):
		return "PROVIDER_SECRET_NOT_FOUND"
	case errors.Is(err, ErrCredentialRateLimited):
		return "PROVIDER_RATE_LIMITED"
	default:
		return "PROVIDER_UNAVAILABLE"
	}
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
