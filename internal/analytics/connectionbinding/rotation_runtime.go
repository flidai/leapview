package connectionbinding

import (
	"context"
	"errors"
	"sync"
	"time"
)

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
