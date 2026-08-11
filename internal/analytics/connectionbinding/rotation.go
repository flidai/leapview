package connectionbinding

import (
	"context"
	"errors"
	"fmt"
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

	refreshGroup singleflight.Group
	refreshMu    sync.Mutex
	mu           sync.Mutex
	binding      TargetBinding
	active       *poolGeneration
	lastRun      time.Time
	retired      bool
}

type poolGeneration struct {
	pool            RuntimePool
	version         string
	bindingRevision int64
	leases          int
	draining        bool
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
	return &PoolManager{
		resolver: config.Resolver, factory: config.Factory, store: config.Store,
		audit: config.Audit, logger: logger, now: config.Now, stale: config.StaleAfter,
		schedule: config.Schedule, binding: config.Binding,
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
	_, err, _ := manager.refreshGroup.Do("refresh", func() (any, error) {
		return nil, manager.refresh(ctx, request)
	})
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

	snapshot, err := manager.resolver.Resolve(ctx, binding.CredentialReference)
	if err != nil {
		manager.recordRefresh(now)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		reason := providerFailureReason(err)
		result := manager.degrade(ctx, reason, now, err)
		return manager.withAudit(ctx, request, RotationDegraded, binding.ValidatedVersion, reason, now, result)
	}
	defer snapshot.Destroy()
	version := snapshot.ProviderVersion()

	manager.mu.Lock()
	if manager.active != nil && manager.active.version == version {
		manager.mu.Unlock()
		validated, err := binding.MarkValidated(version, now)
		if err != nil {
			return err
		}
		if validated.Revision != binding.Revision {
			saved, err := manager.store.Save(ctx, validated, binding.Revision)
			if err != nil {
				return err
			}
			manager.mu.Lock()
			if manager.binding.Revision == binding.Revision {
				manager.binding = saved
				if manager.active != nil {
					manager.active.bindingRevision = saved.Revision
				}
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
		manager.recordRefresh(now)
		result := manager.degrade(ctx, "POOL_PREPARE_FAILED", now, ErrInvalidCredentialBundle)
		return manager.withAudit(ctx, request, RotationDegraded, version, "POOL_PREPARE_FAILED", now, result)
	}
	if replacement == nil {
		manager.recordRefresh(now)
		result := manager.degrade(ctx, "POOL_PREPARE_FAILED", now, ErrInvalidCredentialBundle)
		return manager.withAudit(ctx, request, RotationDegraded, version, "POOL_PREPARE_FAILED", now, result)
	}
	if err := replacement.HealthCheck(ctx); err != nil {
		_ = replacement.Close()
		manager.recordRefresh(now)
		result := manager.degrade(ctx, "POOL_HEALTH_CHECK_FAILED", now, ErrInvalidCredentialBundle)
		return manager.withAudit(ctx, request, RotationDegraded, version, "POOL_HEALTH_CHECK_FAILED", now, result)
	}

	validated, err := binding.MarkValidated(version, now)
	if err != nil {
		_ = replacement.Close()
		return err
	}
	saved, err := manager.store.Save(ctx, validated, binding.Revision)
	if err != nil {
		_ = replacement.Close()
		return err
	}
	manager.mu.Lock()
	if !manager.binding.Enabled || manager.binding.Revision != binding.Revision {
		manager.mu.Unlock()
		_ = replacement.Close()
		return ErrIncompatibleBinding
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
		_ = closePrevious.Close()
	}
	return manager.withAudit(ctx, request, RotationActivated, version, "", now, nil)
}

func (manager *PoolManager) targetID() string {
	if manager == nil {
		return ""
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.binding.TargetID
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
		BindingID: binding.ID, TargetID: binding.TargetID, WorkspaceID: binding.Scope.WorkspaceID,
		ProviderVersion: version,
		Actor:           strings.TrimSpace(request.Actor), Operation: request.Operation,
		Timestamp: timestamp.UTC(), Outcome: outcome, Reason: reason,
	}
	operationID, command := rotationOperationID(request.Operation)
	if !command {
		if err := manager.audit.RecordCredentialRotation(context.WithoutCancel(ctx), event); err != nil {
			manager.logger.ErrorContext(ctx, "best-effort credential rotation audit failed", "operation", request.Operation, "outcome", outcome, "workspace_id", binding.Scope.WorkspaceID, "principal", strings.TrimSpace(request.Actor), "binding_id", binding.ID, "target_id", binding.TargetID, "reason", reason, "error", err)
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
		LogAttributes: []slog.Attr{
			slog.String("operation", string(request.Operation)),
			slog.String("outcome", string(outcome)),
			slog.String("workspace_id", binding.Scope.WorkspaceID),
			slog.String("principal", strings.TrimSpace(request.Actor)),
			slog.String("binding_id", binding.ID),
			slog.String("target_id", binding.TargetID),
			slog.String("reason", reason),
		},
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
	binding := manager.binding
	manager.mu.Unlock()
	degraded, err := binding.MarkDegraded(reason, now)
	if err != nil {
		return result
	}
	if degraded.Revision == binding.Revision {
		manager.recordRefresh(now)
		return result
	}
	saved, err := manager.store.Save(ctx, degraded, binding.Revision)
	if err != nil {
		return errors.Join(result, err)
	}
	manager.mu.Lock()
	if manager.binding.Revision == binding.Revision {
		manager.binding = saved
	}
	manager.mu.Unlock()
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
	manager.binding = saved
	previous := manager.active
	manager.active = nil
	closePrevious := markDraining(previous)
	manager.mu.Unlock()
	if closePrevious != nil {
		return closePrevious.Close()
	}
	return nil
}

// Retire removes this manager from service without changing persisted binding
// metadata. Existing leases may finish; the retired generation closes as soon
// as its final lease is released.
func (manager *PoolManager) Retire() error {
	if manager == nil {
		return nil
	}
	manager.refreshMu.Lock()
	defer manager.refreshMu.Unlock()
	manager.mu.Lock()
	if manager.retired {
		manager.mu.Unlock()
		return nil
	}
	manager.retired = true
	previous := manager.active
	manager.active = nil
	closePrevious := markDraining(previous)
	manager.mu.Unlock()
	if closePrevious != nil {
		return closePrevious.Close()
	}
	return nil
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
		LogicalConnection: binding.LogicalConnectionID, ConnectorKind: binding.ConnectorKind,
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
		manager.mu.Lock()
		generation := lease.generation
		if generation.leases > 0 {
			generation.leases--
		}
		var closing RuntimePool
		if generation.draining && generation.leases == 0 {
			closing = generation.pool
		}
		manager.mu.Unlock()
		if closing != nil {
			_ = closing.Close()
		}
	})
}

func markDraining(generation *poolGeneration) RuntimePool {
	if generation == nil {
		return nil
	}
	generation.draining = true
	if generation.leases == 0 {
		return generation.pool
	}
	return nil
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
