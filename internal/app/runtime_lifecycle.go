package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	"github.com/flidai/leapview/internal/app/gcadapter"
)

// runtimeLifecycle adapts process-owned workers and health signaling to
// Application without retaining construction resources or HTTP routing.
type runtimeLifecycle struct {
	workers   Lifecycle
	analytics *analyticsmodule.Module
	workloads interface{ Close() }
	gc        *gcadapter.Maintenance
	fatal     chan error
	stop      sync.Once
	stopErr   error
}

func newRuntimeLifecycle(workers Lifecycle, analytics *analyticsmodule.Module, workloads interface{ Close() }, maintenance ...*gcadapter.Maintenance) *runtimeLifecycle {
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
			l.workloads.Close()
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
