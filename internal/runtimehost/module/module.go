package module

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/flidai/leapview/internal/servingstate"
)

type ServingStatePort interface {
	runtimehost.ServingStateRepository
}

// Lease is the runtime-host module's narrow composition port. Keeping the
// alias here lets application composition avoid importing the runtime
// implementation package directly.
type Lease = runtimehost.Lease

type Config struct {
	States                ServingStatePort
	WorkspaceIDs          []servingstate.WorkspaceID
	Environment           servingstate.Environment
	Factory               runtimehost.RuntimeFactory
	ManagedData           runtimehost.ManagedDataResolver
	Logger                *slog.Logger
	OnDrained             func(servingstate.ID, int64, []int64)
	OnCleanupFailure      func(runtimehost.CleanupFailure)
	OnLeaseRenewalFailure func(error)
	CandidateReapInterval time.Duration
	OnCandidateReap       func(int)
}

type Module struct {
	registry  *runtimehost.Registry
	reapStop  chan struct{}
	reapDone  chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func Build(ctx context.Context, config Config) (*Module, error) {
	if config.States == nil || config.Factory == nil {
		return nil, errors.New("serving-state repository and runtime factory are required")
	}
	var registry *runtimehost.Registry
	registry = runtimehost.NewRegistryWithFactory(runtimehost.RegistryOptions{
		Repo: config.States, WorkspaceIDs: config.WorkspaceIDs, Environment: config.Environment,
		Factory: config.Factory, ManagedData: config.ManagedData, Logger: config.Logger,
		OnCleanupFailure:      config.OnCleanupFailure,
		OnLeaseRenewalFailure: config.OnLeaseRenewalFailure,
		OnDrained: func(id servingstate.ID, snapshot int64) {
			if config.OnDrained != nil {
				config.OnDrained(id, snapshot, registry.LeasedSnapshots())
			}
		},
	})
	if err := registry.Reload(ctx); err != nil {
		_ = registry.Close()
		return nil, err
	}
	m := &Module{registry: registry, reapStop: make(chan struct{}), reapDone: make(chan struct{})}
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
	return m, nil
}

func (m *Module) Reload(ctx context.Context) error { return m.registry.Reload(ctx) }
func (m *Module) PrepareServingState(ctx context.Context, id string) (servingstate.PreparedRuntime, error) {
	return m.registry.PrepareServingState(ctx, id)
}
func (m *Module) PrepareServingStateCandidates(ctx context.Context, inputs []runtimehost.ServingStateCandidate) (*runtimehost.PreparedSet, error) {
	return m.registry.PrepareServingStateCandidates(ctx, inputs)
}
func (m *Module) PrepareCandidate(
	ctx context.Context,
	input runtimehost.CandidatePreparation,
) (servingstate.PreparedRuntime, error) {
	return m.registry.PrepareCandidate(ctx, input)
}
func (m *Module) PrepareAndRegisterCandidate(
	ctx context.Context,
	input runtimehost.CandidatePreparation,
) error {
	return m.registry.PrepareAndRegisterCandidate(ctx, input)
}
func (m *Module) PrepareAndRegisterCandidateSet(
	ctx context.Context,
	inputs []runtimehost.CandidatePreparation,
) error {
	return m.registry.PrepareAndRegisterCandidateSet(ctx, inputs)
}
func (m *Module) RegisterPreparedCandidate(
	registration runtimehost.CandidateRegistration,
	candidate servingstate.PreparedRuntime,
) error {
	return m.registry.RegisterPreparedCandidate(registration, candidate)
}
func (m *Module) AcquireCandidate(
	ctx context.Context,
	request runtimehost.CandidateLeaseRequest,
) (runtimehost.Lease, error) {
	return m.registry.AcquireCandidate(ctx, request)
}
func (m *Module) ResolveOwnedCandidate(candidateID, ownerID string) (runtimehost.OwnedCandidateView, error) {
	return m.registry.ResolveOwnedCandidate(candidateID, ownerID)
}
func (m *Module) RetireCandidate(id string) int {
	return m.registry.RetireCandidate(id)
}
func (m *Module) ReapExpiredCandidates(now time.Time) int {
	return m.registry.ReapExpiredCandidates(now)
}
func (m *Module) ActivatePrepared(candidate servingstate.PreparedRuntime, activate func() error) error {
	return m.registry.ActivatePrepared(candidate, activate)
}
func (m *Module) ActivatePreparedSet(set *runtimehost.PreparedSet, activate func() error) error {
	return m.registry.ActivatePreparedSet(set, activate)
}

func (m *Module) VerifyPreparedSet(
	ctx context.Context,
	set *runtimehost.PreparedSet,
) (runtimehost.PreparedVerification, error) {
	return m.registry.VerifyPreparedSet(ctx, set)
}
func (m *Module) ProviderForWorkspace(id servingstate.WorkspaceID) runtimehost.Provider {
	return m.registry.ProviderForWorkspace(id)
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
		if m.reapStop != nil {
			close(m.reapStop)
			<-m.reapDone
		}
		if m.registry != nil {
			m.closeErr = m.registry.Close()
		}
	})
	return m.closeErr
}
