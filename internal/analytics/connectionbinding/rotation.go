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
	"github.com/flidai/leapview/internal/analytics/sourcework"
	"golang.org/x/sync/singleflight"
)

type RuntimePool interface {
	HealthCheck(context.Context) error
	Close() error
}

// ContextRuntimePool offers context-bounded cleanup for temporary runtime-pool
// operations. Managed generations use one shared true Close task so each
// retirement caller can bound its own wait without canceling cleanup.
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
	SourceWork *sourcework.Gate
}

type PoolManager struct {
	resolver   CredentialResolver
	factory    RuntimePoolFactory
	store      BindingStateStore
	audit      RotationAuditRecorder
	logger     *slog.Logger
	now        func() time.Time
	stale      time.Duration
	schedule   RefreshSchedule
	sourceWork *sourcework.Gate

	refreshGroup       singleflight.Group
	refreshMu          sync.Mutex
	mu                 sync.Mutex
	binding            TargetBinding
	active             *poolGeneration
	draining           []*poolGeneration
	lastRun            time.Time
	retired            bool
	retireOnceComplete sync.Once
	retireCloseWait    sync.Once
	retireDone         chan struct{}
	retireErr          error
	retireCompleted    bool
	retireForced       bool
	retireGens         []*poolGeneration
	retireCtx          context.Context
	retireCancel       context.CancelFunc
	refreshes          int
	refreshCleanupErr  error
	generationCloseErr error
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
	closeDone       chan struct{}
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
		schedule: config.Schedule, sourceWork: config.SourceWork,
		binding: config.Binding, retireDone: make(chan struct{}),
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
	var sourceLease *sourcework.Lease
	if manager.sourceWork != nil {
		var err error
		sourceLease, err = manager.sourceWork.Acquire(refreshCtx)
		if err != nil {
			return err
		}
		defer sourceLease.Release()
	}
	if err := sourcework.Revalidate(refreshCtx); err != nil {
		return err
	}
	_, err, _ := manager.refreshGroup.Do("refresh", func() (any, error) {
		return nil, manager.refresh(refreshCtx, request, sourceLease)
	})
	if manager.isRetired() {
		return errors.Join(ErrProviderUnavailable, err)
	}
	// Every caller revalidates after singleflight returns. A waiter must not
	// inherit the leader's authority merely because it shared the same refresh.
	if revalidationErr := sourcework.Revalidate(refreshCtx); revalidationErr != nil {
		return errors.Join(err, revalidationErr)
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

func (manager *PoolManager) refresh(ctx context.Context, request RefreshRequest, sourceLease *sourcework.Lease) error {
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
	if err := sourcework.Revalidate(ctx); err != nil {
		return err
	}

	var snapshot CredentialSnapshot
	var err error
	if binding.AuthenticationMode == AuthenticationNone {
		snapshot = NewNoAuthCredentialSnapshot(now)
	} else {
		snapshot, err = manager.resolver.Resolve(ctx, binding.CredentialReference)
	}
	defer snapshot.Destroy()
	// Resolver calls may block while caller authority changes. Revalidate even
	// on a failed resolve before recording degradation or writing audit state.
	if revalidationErr := sourcework.Revalidate(ctx); revalidationErr != nil {
		return revalidationErr
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
	identity := snapshot.Identity()
	if err := identity.Validate(); err != nil {
		manager.recordRefresh(now)
		reason := "INVALID_CREDENTIAL_IDENTITY"
		result := manager.degrade(ctx, reason, now, ErrInvalidCredentialBundle)
		return manager.withAudit(ctx, request, RotationDegraded, binding.ValidatedVersion, reason, now, result)
	}
	if identity.CredentialVersionID != "" {
		manager.recordRefresh(now)
		reason := "LOCAL_CREDENTIAL_VERSION_UNSUPPORTED"
		result := manager.degrade(ctx, reason, now, ErrInvalidCredentialBundle)
		return manager.withAudit(ctx, request, RotationDegraded, binding.ValidatedVersion, reason, now, result)
	}
	version := identity.ProviderVersion

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
			if err := sourcework.Revalidate(ctx); err != nil {
				return err
			}
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
			if err := sourcework.Revalidate(ctx); err != nil {
				return err
			}
			manager.recordRefresh(now)
		}
		return manager.withAudit(ctx, request, RotationUnchanged, version, "", now, nil)
	}
	manager.mu.Unlock()

	if err := sourcework.Revalidate(ctx); err != nil {
		return err
	}
	replacement, err := manager.factory.Prepare(ctx, binding, snapshot)
	prepareAuthorityErr := sourcework.Revalidate(ctx)
	if prepareAuthorityErr != nil {
		return errors.Join(prepareAuthorityErr, manager.closeUncommittedPool(ctx, replacement, sourceLease))
	}
	if err != nil {
		cleanupErr := manager.closeUncommittedPool(ctx, replacement, sourceLease)
		if revalidationErr := sourcework.Revalidate(ctx); revalidationErr != nil {
			return errors.Join(revalidationErr, cleanupErr)
		}
		if isContextError(err) || ctx.Err() != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return errors.Join(ctxErr, cleanupErr)
			}
			return errors.Join(err, cleanupErr)
		}
		manager.recordRefresh(now)
		result := manager.degrade(ctx, "POOL_PREPARE_FAILED", now, ErrInvalidCredentialBundle)
		return errors.Join(
			manager.withAudit(ctx, request, RotationDegraded, version, "POOL_PREPARE_FAILED", now, result),
			cleanupErr,
		)
	}
	if replacement == nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		manager.recordRefresh(now)
		result := manager.degrade(ctx, "POOL_PREPARE_FAILED", now, ErrInvalidCredentialBundle)
		return manager.withAudit(ctx, request, RotationDegraded, version, "POOL_PREPARE_FAILED", now, result)
	}
	healthErr := replacement.HealthCheck(ctx)
	if revalidationErr := sourcework.Revalidate(ctx); revalidationErr != nil {
		return errors.Join(revalidationErr, manager.closeUncommittedPool(ctx, replacement, sourceLease))
	}
	if healthErr != nil {
		cleanupErr := manager.closeUncommittedPool(ctx, replacement, sourceLease)
		if revalidationErr := sourcework.Revalidate(ctx); revalidationErr != nil {
			return errors.Join(revalidationErr, cleanupErr)
		}
		if isContextError(healthErr) || ctx.Err() != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return errors.Join(ctxErr, cleanupErr)
			}
			return errors.Join(healthErr, cleanupErr)
		}
		manager.recordRefresh(now)
		result := manager.degrade(ctx, "POOL_HEALTH_CHECK_FAILED", now, ErrInvalidCredentialBundle)
		return errors.Join(
			manager.withAudit(ctx, request, RotationDegraded, version, "POOL_HEALTH_CHECK_FAILED", now, result),
			cleanupErr,
		)
	}

	validated, err := binding.MarkValidated(version, now)
	if err != nil {
		return errors.Join(err, manager.closeUncommittedPool(ctx, replacement, sourceLease))
	}
	manager.mu.Lock()
	if manager.retired {
		manager.mu.Unlock()
		return errors.Join(ErrProviderUnavailable, manager.closeUncommittedPool(ctx, replacement, sourceLease))
	}
	if !manager.binding.Enabled || manager.binding.Revision != binding.Revision {
		manager.mu.Unlock()
		return errors.Join(ErrIncompatibleBinding, manager.closeUncommittedPool(ctx, replacement, sourceLease))
	}
	manager.mu.Unlock()
	if err := sourcework.Revalidate(ctx); err != nil {
		return errors.Join(err, manager.closeUncommittedPool(ctx, replacement, sourceLease))
	}
	saved, err := manager.store.Save(ctx, validated, binding.Revision)
	if err != nil {
		return errors.Join(err, manager.closeUncommittedPool(ctx, replacement, sourceLease))
	}
	manager.mu.Lock()
	if manager.retired || !manager.binding.Enabled || manager.binding.Revision != binding.Revision {
		manager.mu.Unlock()
		return errors.Join(ErrProviderUnavailable, manager.closeUncommittedPool(ctx, replacement, sourceLease))
	}
	previous := manager.active
	manager.binding = saved
	manager.active = &poolGeneration{
		pool: replacement, version: version, bindingRevision: saved.Revision,
	}
	manager.lastRun = now
	closePrevious := markDraining(previous)
	manager.trackDrainingLocked(previous)
	manager.mu.Unlock()
	if closePrevious != nil {
		manager.closeDrainingGeneration(closePrevious)
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
	manager.trackDrainingLocked(previous)
	manager.mu.Unlock()
	if closePrevious != nil {
		return manager.closeDrainingGeneration(closePrevious)
	}
	return nil
}

// DisableBounded persists the disabled binding revision, fences all new pool
// work, and bounds the lifetime of leases that still hold credential-bearing
// runtime state. A timeout or cancellation requests forced cleanup, which
// continues until actual pool close and admitted refresh work finish.
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

// Retire fences this manager without changing persisted binding metadata. It
// returns after immediate cleanup when possible; nil does not prove outstanding
// readers or refresh work have finished. Use RetireBounded when callers need
// completion evidence.
func (manager *PoolManager) Retire() error {
	if manager == nil {
		return nil
	}
	started := manager.beginRetirement()
	if !started {
		return nil
	}
	if err := manager.tryCompleteRetirement(); err != nil {
		return err
	}
	manager.mu.Lock()
	ready := manager.refreshes == 0
	for _, generation := range manager.retireGens {
		if generation.leases != 0 {
			ready = false
			break
		}
	}
	manager.mu.Unlock()
	if !ready {
		return nil
	}
	<-manager.retireDone
	return manager.retirementResult()
}

// RetireBounded fences new work immediately, then waits for existing leases to
// drain until deadline. If the deadline or ctx is reached first, it requests
// forced close and returns when the caller's bound expires, even if the actual
// pool close is still running. A later caller can wait for its true result.
//
// Cancellation fences admission and requests the same single close task as
// every other caller. Timeouts and cancellation are never stored as close
// results. Repeated calls wait for actual completion within their own bounds.
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
	manager.beginRetirement()
	if err := manager.tryCompleteRetirement(); err != nil {
		return err
	}

	select {
	case <-manager.retireDone:
		return manager.retirementResult()
	default:
	}
	if err := ctx.Err(); err != nil {
		manager.forceRetired()
		return manager.forcedRetirementResult(err)
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		manager.forceRetired()
		return manager.forcedRetirementResult(context.DeadlineExceeded)
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-manager.retireDone:
		return manager.retirementResult()
	case <-ctx.Done():
		manager.forceRetired()
		return manager.forcedRetirementResult(ctx.Err())
	case <-timer.C:
		manager.forceRetired()
		return manager.forcedRetirementResult(context.DeadlineExceeded)
	}
}

func (manager *PoolManager) forcedRetirementResult(reason error) error {
	select {
	case <-manager.retireDone:
		return errors.Join(reason, manager.retirementResult())
	default:
	}
	return reason
}

// beginRetirement is the single retirement fence. It deliberately does not
// take refreshMu: callers must stop admitting leases as soon as retirement is
// requested, even if a refresh is currently blocked in provider or pool work.
func (manager *PoolManager) beginRetirement() bool {
	manager.mu.Lock()
	started := !manager.retired
	cancel := manager.beginRetirementLocked()
	manager.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return started
}

// beginRetirementLocked allows exact identity comparison and the retirement
// fence to share one critical section. The caller invokes cancel after unlocking.
func (manager *PoolManager) beginRetirementLocked() context.CancelFunc {
	if manager.retired {
		return nil
	}
	if manager.retireDone == nil {
		manager.retireDone = make(chan struct{})
	}
	manager.retired = true
	active := manager.active
	manager.active = nil
	manager.retireGens = append(manager.retireGens[:0], manager.draining...)
	if active != nil {
		active.draining = true
		manager.retireGens = append(manager.retireGens, active)
	}
	for _, generation := range manager.retireGens {
		generation.draining = true
	}
	manager.draining = nil
	return manager.retireCancel
}

func (manager *PoolManager) forceRetired() {
	manager.mu.Lock()
	manager.retireForced = true
	manager.mu.Unlock()
	_ = manager.tryCompleteRetirement()
}

// tryCompleteRetirement starts the retired generations' shared close tasks
// after refresh work exits, unless a caller forces close first. Completion
// still waits for every close task and admitted refresh to finish.
func (manager *PoolManager) tryCompleteRetirement() error {
	manager.mu.Lock()
	if !manager.retired || manager.retireCompleted {
		manager.mu.Unlock()
		return nil
	}
	if manager.refreshes != 0 && !manager.retireForced {
		manager.mu.Unlock()
		return nil
	}
	generations := append([]*poolGeneration(nil), manager.retireGens...)
	forced := manager.retireForced
	if !forced {
		for _, generation := range generations {
			if generation.leases != 0 {
				manager.mu.Unlock()
				return nil
			}
		}
	}
	manager.mu.Unlock()

	dones := make([]<-chan struct{}, 0, len(generations))
	for _, generation := range generations {
		dones = append(dones, generationCloseDone(generation))
	}
	manager.retireCloseWait.Do(func() {
		go func() {
			for _, done := range dones {
				<-done
			}
			_ = manager.completeRetirementIfReady()
		}()
	})
	return manager.completeRetirementIfReady()
}

func (manager *PoolManager) completeRetirementIfReady() error {
	manager.mu.Lock()
	if !manager.retired || manager.retireCompleted || manager.refreshes != 0 {
		manager.mu.Unlock()
		return nil
	}
	if !manager.retireForced {
		for _, generation := range manager.retireGens {
			if generation.leases != 0 {
				manager.mu.Unlock()
				return nil
			}
		}
	}
	generations := append([]*poolGeneration(nil), manager.retireGens...)
	closeErr := errors.Join(manager.generationCloseErr, manager.refreshCleanupErr)
	manager.mu.Unlock()

	for _, generation := range generations {
		select {
		case <-generationCloseDone(generation):
			closeErr = errors.Join(closeErr, generation.closeErr)
		default:
			return nil
		}
	}
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
		manager.retireGens = nil
		manager.generationCloseErr = nil
		manager.refreshCleanupErr = nil
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
