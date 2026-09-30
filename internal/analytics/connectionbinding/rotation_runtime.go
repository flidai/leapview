package connectionbinding

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/analytics/sourcework"
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
		retiring := manager.retired
		manager.mu.Unlock()
		if closing && !retiring {
			_ = manager.closeDrainingGeneration(generation)
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
	<-generationCloseDone(generation)
	return generation.closeErr
}

func generationCloseDone(generation *poolGeneration) <-chan struct{} {
	if generation == nil || generation.pool == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	generation.closeOnce.Do(func() {
		generation.closeDone = make(chan struct{})
		done := generation.closeDone
		go func() {
			generation.closeErr = generation.pool.Close()
			close(done)
		}()
	})
	return generation.closeDone
}

func (manager *PoolManager) trackDrainingLocked(generation *poolGeneration) {
	if generation == nil {
		return
	}
	for _, current := range manager.draining {
		if current == generation {
			return
		}
	}
	manager.draining = append(manager.draining, generation)
}

func (manager *PoolManager) closeDrainingGeneration(generation *poolGeneration) error {
	err := closeGeneration(generation)
	manager.mu.Lock()
	for index, current := range manager.draining {
		if current == generation {
			manager.draining = append(manager.draining[:index], manager.draining[index+1:]...)
			break
		}
	}
	retired := manager.retired
	if err != nil && !retired {
		manager.generationCloseErr = errors.Join(manager.generationCloseErr, err)
	}
	manager.mu.Unlock()
	if retired {
		_ = manager.tryCompleteRetirement()
	}
	return err
}

func (manager *PoolManager) closeUncommittedPool(
	ctx context.Context,
	pool RuntimePool,
	sourceLease *sourcework.Lease,
) error {
	if pool == nil {
		return nil
	}
	var cleanupLease *sourcework.Lease
	if sourceLease != nil {
		var err error
		cleanupLease, err = sourceLease.Retain()
		if err != nil {
			// The admitted refresh still owns its root lease, so finish cleanup
			// synchronously if a child lease cannot be retained. Returning while
			// cleanup runs would let the shared source-work gate drain too early.
			closeErr := pool.Close()
			manager.mu.Lock()
			if closeErr != nil {
				manager.refreshCleanupErr = errors.Join(manager.refreshCleanupErr, closeErr)
			}
			manager.mu.Unlock()
			if closeErr != nil {
				manager.logger.ErrorContext(ctx, "failed to close discarded runtime pool")
			}
			return ErrProviderUnavailable
		}
	}
	manager.mu.Lock()
	if manager.refreshes == 0 {
		manager.refreshDone = make(chan struct{})
	}
	manager.refreshes++
	manager.mu.Unlock()

	// A discarded candidate is still owned refresh work. Its task keeps the
	// retirement barrier open after a bounded refresh caller has returned.
	done := make(chan error, 1)
	go func() {
		defer cleanupLease.Release()
		err := pool.Close()
		manager.mu.Lock()
		if err != nil {
			manager.refreshCleanupErr = errors.Join(manager.refreshCleanupErr, err)
		}
		manager.mu.Unlock()
		if err != nil {
			manager.logger.ErrorContext(ctx, "failed to close discarded runtime pool")
		}
		manager.finishRefresh()
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			// Do not expose driver close details through the refresh/API error.
			return ErrProviderUnavailable
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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
