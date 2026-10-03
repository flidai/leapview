package app

import (
	"context"
	"errors"
	"sync"
)

// postgresResourceLifecycle owns capability resources that are safe to start
// during composition but must close after workers and runtimehost have
// drained. Start is intentionally a no-op because construction already
// completed all resource initialization.
type postgresResourceLifecycle struct {
	analytics postgresAnalyticsCloser
	workloads workloadControl
	stop      sync.Once
	err       error
}

type postgresAnalyticsCloser interface {
	Close() error
}

func newPostgresResourceLifecycle(analytics postgresAnalyticsCloser, workloads workloadControl) *postgresResourceLifecycle {
	return &postgresResourceLifecycle{analytics: analytics, workloads: workloads}
}

func (l *postgresResourceLifecycle) Start(context.Context) error { return nil }

func (l *postgresResourceLifecycle) Stop(_ context.Context) error {
	if l == nil {
		return nil
	}
	l.stop.Do(func() {
		if l.workloads != nil {
			l.workloads.Close()
		}
		if l.analytics != nil {
			l.err = l.analytics.Close()
		}
	})
	return l.err
}

// postgresBootstrapLifecycle wraps the pool owner so a failed startup ping
// cannot leak serving pools: Application only marks a component started after
// Start succeeds and therefore would otherwise skip its Stop method.
type postgresBootstrapLifecycle struct {
	owner          Lifecycle
	onStartFailure func() error
	stop           sync.Once
	stopErr        error
}

func newPostgresBootstrapLifecycle(owner Lifecycle) *postgresBootstrapLifecycle {
	return &postgresBootstrapLifecycle{owner: owner}
}

func (l *postgresBootstrapLifecycle) Start(ctx context.Context) error {
	if l == nil || l.owner == nil {
		return errors.New("PostgreSQL bootstrap lifecycle is not initialized")
	}
	if err := l.owner.Start(ctx); err != nil {
		cleanupErr := error(nil)
		if l.onStartFailure != nil {
			cleanupErr = l.onStartFailure()
		}
		return errors.Join(err, cleanupErr, l.Stop(context.Background()))
	}
	return nil
}

func (l *postgresBootstrapLifecycle) Stop(ctx context.Context) error {
	if l == nil || l.owner == nil {
		return nil
	}
	l.stop.Do(func() {
		l.stopErr = l.owner.Stop(ctx)
	})
	return l.stopErr
}
