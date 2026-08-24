package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/access"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	"github.com/flidai/leapview/internal/app/gcadapter"
)

// These limits intentionally remain local until audit delivery policy has a
// public configuration contract. They are high enough to avoid transient
// readiness flaps while still surfacing a stuck outbox before it becomes an
// unbounded operational backlog.
const (
	auditOutboxReadinessMaxUndelivered = int64(10_000)
	auditOutboxReadinessMaxAge         = time.Hour
)

// auditOutboxReadiness reports only aggregate outbox state. In particular, it
// never includes event identity or metadata in the readiness response.
func auditOutboxReadiness(ctx context.Context, store access.AuditOutboxStatsReader) error {
	if store == nil {
		return nil
	}
	stats, err := store.AuditOutboxStats(ctx, time.Now().UTC())
	if err != nil {
		return errors.New("audit outbox unavailable")
	}
	if stats.Poison > 0 || stats.Quarantined > 0 {
		return fmt.Errorf("audit outbox has terminal intents (poison=%d quarantined=%d)", stats.Poison, stats.Quarantined)
	}
	undelivered := stats.Pending + stats.Retry + stats.Leased
	terminal := stats.Poison + stats.Quarantined
	undelivered += terminal
	if stats.Capacity > 0 && undelivered >= stats.Capacity {
		return fmt.Errorf("audit outbox capacity exhausted (count=%d capacity=%d)", undelivered, stats.Capacity)
	}
	if undelivered > auditOutboxReadinessMaxUndelivered {
		return fmt.Errorf("audit outbox backlog exceeds %d intents (count=%d)", auditOutboxReadinessMaxUndelivered, undelivered)
	}
	if stats.OldestUndeliveredAge > auditOutboxReadinessMaxAge {
		return fmt.Errorf("audit outbox backlog exceeds %s (age=%s)", auditOutboxReadinessMaxAge, stats.OldestUndeliveredAge.Round(time.Second))
	}
	return nil
}

// runtimeLifecycle adapts process-owned workers and health signaling to
// Application without retaining construction resources or HTTP routing.
type runtimeLifecycle struct {
	workers   Lifecycle
	analytics *analyticsmodule.Module
	workloads interface {
		Close()
		Drain(context.Context) error
	}
	gc      *gcadapter.Maintenance
	fatal   chan error
	stop    sync.Once
	stopErr error
}

func newRuntimeLifecycle(workers Lifecycle, analytics *analyticsmodule.Module, workloads interface {
	Close()
	Drain(context.Context) error
}, maintenance ...*gcadapter.Maintenance) *runtimeLifecycle {
	var gcMaintenance *gcadapter.Maintenance
	if len(maintenance) > 0 {
		gcMaintenance = maintenance[0]
	}
	return &runtimeLifecycle{workers: workers, analytics: analytics, workloads: workloads, gc: gcMaintenance, fatal: make(chan error, 1)}
}

func (l *runtimeLifecycle) Start(ctx context.Context) error {
	if l == nil {
		return errors.New("runtime is not initialized")
	}
	if l.workers != nil {
		if err := l.workers.Start(ctx); err != nil {
			return err
		}
	}
	if l.gc != nil {
		if err := l.gc.Start(ctx); err != nil {
			if l.workers != nil {
				_ = l.workers.Stop(context.WithoutCancel(ctx))
			}
			return err
		}
	}
	if l.analytics == nil {
		return nil
	}
	analyticalFatal := l.analytics.Fatal()
	if analyticalFatal == nil {
		return nil
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-analyticalFatal:
			if l.workloads != nil {
				l.workloads.Close()
			}
			err := l.analytics.Healthy()
			if err == nil {
				err = errors.New("analytical environment became unhealthy")
			}
			select {
			case l.fatal <- fmt.Errorf("analytical environment became unhealthy: %w", err):
			default:
			}
		}
	}()
	return nil
}

func (l *runtimeLifecycle) Stop(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.stop.Do(func() {
		if l.workers != nil {
			l.stopErr = l.workers.Stop(ctx)
		}
		if l.gc != nil {
			l.stopErr = errors.Join(l.stopErr, l.gc.Stop(ctx))
		}
		if l.workloads != nil {
			l.stopErr = errors.Join(l.stopErr, l.workloads.Drain(ctx))
		}
	})
	return l.stopErr
}

func (l *runtimeLifecycle) Fatal() <-chan error {
	if l == nil {
		return nil
	}
	return l.fatal
}
