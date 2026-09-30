package runtimehost

import (
	"context"
	"errors"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// WithRetiredRuntimeCleanup waits for this manager's retired serving runtimes
// to finish their synchronous cleanup, then calls complete while a cutover read
// fence holds the exact current identity stable. A previous cleanup failure is
// retained for the manager's lifetime, even after its retired entry is removed.
// Cancellation observed before the callback stops waiting without cancelling
// cleanup. A later call can retry the wait for the same installed identity.
// Once the callback starts, its outcome is returned even if cancellation races
// with it; an ambiguous durable write requires a durable reread by the caller.
//
// This is one completion prerequisite, not a live readiness or revocation
// acknowledgment. Private candidates and shared pools have separate lifetimes;
// snapshot-lease releases may still be queued. The caller must own source-work
// pause/drain and lifecycle mutation serialization, and recheck durable identity
// and current authority in complete and confirm its transaction committed. The
// callback must not perform provider I/O,
// activate/close this host, or reacquire its cutover fence. Neither the caller
// nor its callback may retain an old-generation lease that this wait must drain.
func (m *Manager) WithRetiredRuntimeCleanup(ctx context.Context, expected projectgraph.ServingIdentity, complete func() error) error {
	if m == nil {
		return ErrRegistryClosed
	}
	if ctx == nil || complete == nil || expected.Validate() != nil {
		return errors.New("runtime cleanup completion requires a context, exact identity and callback")
	}
	release, err := m.AcquireCutoverFence(ctx)
	if err != nil {
		return err
	}
	defer release()

	m.mu.RLock()
	current := m.current
	if current == nil || current.closing {
		m.mu.RUnlock()
		return ErrNoActiveServingState
	}
	if current.identity != expected {
		m.mu.RUnlock()
		return ErrPreparedStale
	}
	cleanupErr := m.retiredCleanupErr
	waiting := m.scheduledCleanupLocked()
	m.mu.RUnlock()
	if cleanupErr != nil {
		return cleanupErr
	}
	for _, runtime := range waiting {
		select {
		case <-runtime.cleanupDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Workers record failures before closing cleanupDone. The cutover fence
	// prevents another serving runtime from being retired while we wait.
	m.mu.RLock()
	cleanupErr = m.retiredCleanupErr
	m.mu.RUnlock()
	if cleanupErr != nil {
		return cleanupErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return complete()
}

// WithRetiredRuntimeCleanup exposes the serving-manager cleanup boundary.
// It does not wait for private candidates in this registry.
func (r *Registry) WithRetiredRuntimeCleanup(ctx context.Context, expected projectgraph.ServingIdentity, complete func() error) error {
	if r == nil || r.manager == nil {
		return ErrRegistryClosed
	}
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return ErrRegistryClosed
	}
	return r.manager.WithRetiredRuntimeCleanup(ctx, expected, complete)
}
