package connectionbinding

import (
	"context"
	"time"
)

// PoolRetirement retains one captured manager and its actual cleanup result.
// Keep the handle for retries after timeout, directory replacement or shutdown.
// It does not fence later managers or establish credential readiness.
type PoolRetirement struct {
	manager *PoolManager
}

// RetireBinding atomically matches and fences the exact current binding tuple.
// Missing entries are not cleanup evidence. The retired manager remains in the
// directory so same-revision callers stay fenced and Close retains its result;
// normal revision replacement can remove it only after successful cleanup.
//
// The lifecycle caller must pause/drain candidate and source work and serialize
// binding replacement through the transition. This does not mutate durable
// binding state, stop every credential consumer, or authorize revocation.
func (directory *PoolDirectory) RetireBinding(expected TargetBinding) (*PoolRetirement, error) {
	if directory == nil {
		return nil, ErrProviderUnavailable
	}
	if err := expected.Validate(); err != nil {
		return nil, err
	}
	directory.mu.Lock()
	if directory.closed {
		directory.mu.Unlock()
		return nil, ErrProviderUnavailable
	}
	pool := directory.pools[expected.ID]
	if pool == nil || pool.manager == nil {
		directory.mu.Unlock()
		return nil, ErrBindingNotFound
	}
	manager := pool.manager
	manager.mu.Lock()
	actual := manager.binding
	if actual.Evidence() != expected.Evidence() || actual.Enabled != expected.Enabled ||
		actual.AuthenticationMode != expected.AuthenticationMode || actual.CredentialReference != expected.CredentialReference {
		manager.mu.Unlock()
		directory.mu.Unlock()
		return nil, ErrIncompatibleBinding
	}
	cancel := manager.beginRetirementLocked()
	manager.mu.Unlock()
	directory.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	// Cleanup errors belong to the handle. They must not erase the successful
	// capture or prevent callers from retrying its wait.
	_ = manager.tryCompleteRetirement()
	return &PoolRetirement{manager: manager}, nil
}

// Wait uses the manager's bounded retirement protocol. Timeout or cancellation
// requests forced physical close and bounds only this caller's wait; retry this
// handle to observe actual completion and its retained error. Concurrent waits
// share the same close tasks. Success proves captured pool closure, not reader
// lease release, closure of later managers, or live credential readiness.
func (retirement *PoolRetirement) Wait(ctx context.Context, deadline time.Time) error {
	if retirement == nil || retirement.manager == nil || ctx == nil || deadline.IsZero() {
		return ErrInvalidBinding
	}
	return retirement.manager.RetireBounded(ctx, deadline)
}
