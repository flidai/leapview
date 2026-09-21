package module

import (
	"context"
	"log/slog"
	"time"
)

const maxRefreshOrphanRecoveryInterval = 30 * time.Second

type refreshOrphanRecoveryHandler interface {
	RecoverOrphanedJobs(context.Context) error
	LeaseTimeout() time.Duration
}

func refreshOrphanRecoveryInterval(lease time.Duration) time.Duration {
	interval := lease / 4
	if interval > maxRefreshOrphanRecoveryInterval {
		return maxRefreshOrphanRecoveryInterval
	}
	return max(interval, time.Millisecond)
}

func (m *Module) startRefreshOrphanRecovery() {
	m.mu.Lock()
	if m.refreshRecovery == nil || m.refreshRecoveryStop != nil {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m.refreshRecoveryStop = cancel
	m.refreshRecoveryDone = done
	recovery := m.refreshRecovery
	m.mu.Unlock()
	go m.runRefreshOrphanRecovery(ctx, done, recovery)
}

func (m *Module) runRefreshOrphanRecovery(ctx context.Context, done chan<- struct{}, recovery refreshOrphanRecoveryHandler) {
	defer close(done)
	logger := m.config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	run := func() {
		if err := recovery.RecoverOrphanedJobs(ctx); err != nil && ctx.Err() == nil {
			logger.WarnContext(ctx, "recover orphaned refresh pipeline jobs failed", "error", err)
		}
	}
	run()
	ticker := time.NewTicker(refreshOrphanRecoveryInterval(recovery.LeaseTimeout()))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func (m *Module) stopRefreshOrphanRecovery() {
	m.mu.RLock()
	cancel := m.refreshRecoveryStop
	m.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

func (m *Module) waitRefreshOrphanRecovery(ctx context.Context) error {
	m.mu.RLock()
	done := m.refreshRecoveryDone
	m.mu.RUnlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
