package gcadapter

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// Maintenance owns the process lifecycle for the global physical-pool GC
// pass. A failed pass is retained as degraded health and retried on the next
// interval; it never falls back to serving-state or DuckLake native cleanup.
type Maintenance struct {
	run      func(context.Context) error
	interval time.Duration
	logger   *slog.Logger

	mu      sync.RWMutex
	lastErr error
	stop    chan struct{}
	done    chan struct{}
	onError func(error)
}

func NewMaintenance(run func(context.Context) error, interval time.Duration, logger *slog.Logger, onError func(error)) (*Maintenance, error) {
	if run == nil {
		return nil, errors.New("GC maintenance runner is required")
	}
	if interval <= 0 {
		interval = time.Hour
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Maintenance{run: run, interval: interval, logger: logger, stop: make(chan struct{}), done: make(chan struct{}), onError: onError}, nil
}

func (m *Maintenance) Start(ctx context.Context) error {
	if m == nil {
		return errors.New("GC maintenance is unavailable")
	}
	go func() {
		defer close(m.done)
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.runOnce(ctx)
			case <-m.stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return nil
}

func (m *Maintenance) runOnce(ctx context.Context) {
	if err := m.run(ctx); err != nil {
		m.mu.Lock()
		m.lastErr = err
		m.mu.Unlock()
		m.logger.Warn("physical-pool global GC pass failed; retaining objects and retrying", "error", err)
		if m.onError != nil {
			m.onError(err)
		}
		return
	}
	m.mu.Lock()
	m.lastErr = nil
	m.mu.Unlock()
}

func (m *Maintenance) Health() error {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastErr
}

func (m *Maintenance) Stop(context.Context) error {
	if m == nil {
		return nil
	}
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
	select {
	case <-m.done:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("timed out stopping GC maintenance")
	}
}
