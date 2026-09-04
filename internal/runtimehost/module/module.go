package module

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/flidai/leapview/internal/servingstate"
)

type ServingStatePort interface {
	runtimehost.ServingStateRepository
}
type Lease = runtimehost.Lease

type Config struct {
	States      ServingStatePort
	ProjectID   projectgraph.ResourceID
	Environment servingstate.Environment
	// ReadClaimedProject loads the durable instance project claim. It is
	// invoked before the first reload so a restart cannot serve another scope.
	ReadClaimedProject    func(context.Context) (projectgraph.ResourceID, bool, error)
	Factory               runtimehost.RuntimeFactory
	ManagedData           runtimehost.ManagedDataResolver
	Authorization         runtimehost.AuthorizationSnapshotInstaller
	Logger                *slog.Logger
	OnDrained             func(servingstate.ID, int64)
	OnCleanupFailure      func(runtimehost.CleanupFailure)
	OnLeaseRenewalFailure func(error)
	CandidateReapInterval time.Duration
	OnCandidateReap       func(int)
	// ActiveReconcileInterval bounds how long a node may remain stale when it
	// misses a delivery notification. Reconciliation always re-reads the
	// canonical durable active pointer; notifications are only a latency hint.
	ActiveReconcileInterval time.Duration
	// ActiveReconcileTimeout bounds one durable pointer reconciliation pass so
	// a wedged database call cannot suppress all subsequent polls. Zero uses a
	// conservative default; negative values are rejected.
	ActiveReconcileTimeout time.Duration
	RequireSealedCatalog   bool
	// ResolveSealedActiveState is the authoritative delivery-pointer lookup
	// used during production startup. When sealed mode is enabled, legacy
	// serving-state active pointers are not consulted.
	ResolveSealedActiveState func(context.Context) (servingstate.ID, error)
}

type Module struct {
	registry         *runtimehost.Registry
	reapStop         chan struct{}
	reapDone         chan struct{}
	reconcileStop    chan struct{}
	reconcileDone    chan struct{}
	reconcileCancel  context.CancelFunc
	reconcileMu      sync.Mutex
	reconcileRunning bool
	logger           *slog.Logger
	closeOnce        sync.Once
	closeErr         error
}

func Build(ctx context.Context, config Config) (*Module, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if config.RequireSealedCatalog && config.ResolveSealedActiveState == nil {
		return nil, errors.New("sealed runtime host requires an authoritative active-state resolver")
	}
	if config.States == nil || config.Factory == nil {
		return nil, errors.New("serving-state repository and runtime factory are required")
	}
	if config.ActiveReconcileTimeout < 0 {
		return nil, errors.New("active reconciliation timeout must not be negative")
	}
	var registry *runtimehost.Registry
	registry = runtimehost.NewRegistryWithFactory(runtimehost.RegistryOptions{Repo: config.States, ProjectID: config.ProjectID, Environment: config.Environment, Factory: config.Factory, ManagedData: config.ManagedData, Authorization: config.Authorization, Logger: config.Logger, OnCleanupFailure: config.OnCleanupFailure, OnLeaseRenewalFailure: config.OnLeaseRenewalFailure, OnDrained: config.OnDrained, RequireSealedCatalog: config.RequireSealedCatalog})
	if config.ReadClaimedProject != nil {
		claimedProject, found, err := config.ReadClaimedProject(ctx)
		if err != nil {
			return nil, err
		}
		if found {
			if err := registry.BindClaimedProject(claimedProject, config.Environment); err != nil {
				return nil, err
			}
		}
	}
	if config.RequireSealedCatalog {
		id, err := config.ResolveSealedActiveState(ctx)
		if err == nil {
			if err := registry.ReconcileSealed(ctx, id); err != nil {
				_ = registry.Close()
				return nil, err
			}
		} else if !errors.Is(err, servingstate.ErrNotFound) {
			_ = registry.Close()
			return nil, err
		}
	} else if err := registry.Reload(ctx); err != nil {
		_ = registry.Close()
		return nil, err
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	m := &Module{registry: registry, reapStop: make(chan struct{}), reapDone: make(chan struct{}), reconcileStop: make(chan struct{}), reconcileDone: make(chan struct{}), logger: logger}
	interval := config.CandidateReapInterval
	if interval <= 0 {
		interval = time.Minute
	}
	go func() {
		defer close(m.reapDone)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if count := m.registry.ReapExpiredCandidates(time.Now().UTC()); config.OnCandidateReap != nil {
					config.OnCandidateReap(count)
				}
			case <-m.reapStop:
				return
			}
		}
	}()
	reconcileInterval := config.ActiveReconcileInterval
	if reconcileInterval <= 0 {
		reconcileInterval = time.Minute
	}
	reconcileTimeout := config.ActiveReconcileTimeout
	if reconcileTimeout <= 0 {
		reconcileTimeout = 30 * time.Second
	}
	if config.RequireSealedCatalog {
		// Do not retain the startup admission context: its workload lease is
		// released when Build returns. The reconciler owns an independent
		// lifecycle context and performs only its own bounded durable reads.
		reconcileCtx, reconcileCancel := context.WithCancel(context.Background())
		m.reconcileCancel = reconcileCancel
		go func() {
			defer close(m.reconcileDone)
			ticker := time.NewTicker(reconcileInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					m.startActiveReconcile(reconcileCtx, config.ResolveSealedActiveState, reconcileTimeout)
				case <-m.reconcileStop:
					return
				}
			}
		}()
	} else {
		// Local/evaluation hosts retain their existing explicit Reload behavior;
		// the durable active-pointer reconciler is production/sealed only.
		close(m.reconcileDone)
	}
	return m, nil
}

func (m *Module) startActiveReconcile(ctx context.Context, resolve func(context.Context) (servingstate.ID, error), timeout time.Duration) {
	if m == nil || m.registry == nil || resolve == nil {
		return
	}
	m.reconcileMu.Lock()
	if m.reconcileRunning {
		m.reconcileMu.Unlock()
		return
	}
	m.reconcileRunning = true
	m.reconcileMu.Unlock()
	go func() {
		defer func() {
			m.reconcileMu.Lock()
			m.reconcileRunning = false
			m.reconcileMu.Unlock()
		}()
		passCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := m.reconcileActive(passCtx, resolve); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, runtimehost.ErrRegistryClosed) {
			logger := m.logger
			if logger == nil {
				logger = slog.Default()
			}
			logger.WarnContext(ctx, "sealed active serving generation reconciliation failed", "error", err)
		}
	}()
}

func (m *Module) reconcileActive(ctx context.Context, resolve func(context.Context) (servingstate.ID, error)) error {
	if m == nil || m.registry == nil {
		return errors.New("runtime host is unavailable")
	}
	if resolve == nil {
		return errors.New("sealed active-state resolver is unavailable")
	}
	id, err := resolve(ctx)
	if err != nil {
		if errors.Is(err, servingstate.ErrNotFound) {
			// Confirm absence while serialized with local cutover before
			// retiring a generation. A concurrent activation wins the race.
			if confirmErr := m.registry.ReconcileNoActive(ctx, resolve); confirmErr != nil {
				return confirmErr
			}
			return nil
		}
		return err
	}
	if id == "" {
		return errors.New("sealed active-state resolver returned an empty generation")
	}
	if id == m.registry.CurrentServingStateID() {
		return nil
	}
	return m.registry.ReconcileSealed(ctx, id)
}
func (m *Module) Reload(ctx context.Context) error { return m.registry.Reload(ctx) }

// ReconcileSealed activates a delivery-committed generation through the
// snapshot-bound runtime factory.
func (m *Module) ReconcileSealed(ctx context.Context, id servingstate.ID) error {
	if m == nil || m.registry == nil {
		return errors.New("runtime host is unavailable")
	}
	return m.registry.ReconcileSealed(ctx, id)
}
func (m *Module) PrepareServingState(ctx context.Context, id string) (*runtimehost.Prepared, error) {
	return m.registry.PrepareServingState(ctx, id)
}
func (m *Module) PrepareSealedActivation(ctx context.Context, id, candidateID string) (*runtimehost.Prepared, error) {
	if m == nil || m.registry == nil {
		return nil, errors.New("runtime host is unavailable")
	}
	return m.registry.PrepareSealedActivation(ctx, id, candidateID)
}
func (m *Module) PrepareServingStateCandidate(ctx context.Context, input runtimehost.ServingStateCandidate) (*runtimehost.Prepared, error) {
	return m.registry.PrepareServingStateCandidate(ctx, input)
}
func (m *Module) PrepareCandidate(ctx context.Context, input runtimehost.CandidatePreparation) (servingstate.PreparedRuntime, error) {
	return m.registry.PrepareCandidate(ctx, input)
}
func (m *Module) PrepareAndRegisterCandidate(ctx context.Context, input runtimehost.CandidatePreparation) error {
	return m.registry.PrepareAndRegisterCandidate(ctx, input)
}
func (m *Module) PrepareAndRegisterCandidateSet(ctx context.Context, inputs []runtimehost.CandidatePreparation) error {
	return m.registry.PrepareAndRegisterCandidateSet(ctx, inputs)
}
func (m *Module) RegisterPreparedCandidate(reg runtimehost.CandidateRegistration, candidate servingstate.PreparedRuntime) error {
	return m.registry.RegisterPreparedCandidate(reg, candidate)
}
func (m *Module) AcquireCandidate(ctx context.Context, request runtimehost.CandidateLeaseRequest) (runtimehost.Lease, error) {
	return m.registry.AcquireCandidate(ctx, request)
}
func (m *Module) ResolveOwnedCandidate(candidateID, ownerID string) (runtimehost.OwnedCandidateView, error) {
	return m.registry.ResolveOwnedCandidate(candidateID, ownerID)
}
func (m *Module) RetireCandidate(id string) int { return m.registry.RetireCandidate(id) }
func (m *Module) ReapExpiredCandidates(now time.Time) int {
	return m.registry.ReapExpiredCandidates(now)
}
func (m *Module) ActivatePrepared(candidate *runtimehost.Prepared, activate func() error) error {
	return m.registry.ActivatePrepared(candidate, activate)
}
func (m *Module) ActivatePreparedContext(ctx context.Context, prepared *runtimehost.Prepared, activate func() error) error {
	return m.registry.ActivatePreparedContext(ctx, prepared, activate)
}
func (m *Module) VerifyPrepared(ctx context.Context, prepared *runtimehost.Prepared) (runtimehost.PreparedVerification, error) {
	return m.registry.VerifyPrepared(ctx, prepared)
}
func (m *Module) Provider() runtimehost.Provider { return m.registry.Provider() }
func (m *Module) ProjectID() projectgraph.ResourceID {
	if m == nil || m.registry == nil {
		return ""
	}
	return m.registry.ProjectID()
}

// BindClaimedProject installs the durable instance project claim before any
// active generation is loaded or published.
func (m *Module) BindClaimedProject(projectID projectgraph.ResourceID, environment servingstate.Environment) error {
	if m == nil || m.registry == nil {
		return runtimehost.ErrRegistryClosed
	}
	return m.registry.BindClaimedProject(projectID, environment)
}

// ActiveArtifact resolves the exact active generation for the module's fixed
// project/environment scope. A missing active row is returned as
// servingstate.ErrNotFound and is not conflated with runtime lease readiness.
func (m *Module) ActiveArtifact(ctx context.Context) (servingstate.State, servingstate.Artifact, error) {
	if m == nil || m.registry == nil {
		return servingstate.State{}, servingstate.Artifact{}, servingstate.ErrNotFound
	}
	return m.registry.ActiveArtifact(ctx)
}

func (m *Module) Environment() servingstate.Environment {
	if m == nil || m.registry == nil {
		return ""
	}
	return m.registry.Environment()
}
func (m *Module) Acquire(ctx context.Context) (runtimehost.Lease, error) {
	return m.registry.Acquire(ctx)
}
func (m *Module) LeasedSnapshots() []int64 { return m.registry.LeasedSnapshots() }
func (m *Module) LeaseRenewalError() error {
	if m == nil || m.registry == nil {
		return nil
	}
	return m.registry.LeaseRenewalError()
}
func (m *Module) Close() error {
	if m == nil {
		return nil
	}
	m.closeOnce.Do(func() {
		close(m.reapStop)
		<-m.reapDone
		if m.reconcileCancel != nil {
			m.reconcileCancel()
		}
		close(m.reconcileStop)
		<-m.reconcileDone
		if m.registry != nil {
			m.closeErr = m.registry.Close()
		}
	})
	return m.closeErr
}
