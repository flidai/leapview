package app

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	workloadmodule "github.com/flidai/leapview/internal/workload/module"
)

type postgresLifecycleEventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *postgresLifecycleEventLog) add(event string) {
	l.mu.Lock()
	l.events = append(l.events, event)
	l.mu.Unlock()
}

func (l *postgresLifecycleEventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

type postgresAnalyticsCloserFake struct {
	log   *postgresLifecycleEventLog
	mu    sync.Mutex
	calls int
	err   error
}

func (f *postgresAnalyticsCloserFake) Close() error {
	f.mu.Lock()
	f.calls++
	err := f.err
	f.mu.Unlock()
	f.log.add("analytics")
	return err
}

func (f *postgresAnalyticsCloserFake) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type postgresWorkloadControlFake struct {
	log   *postgresLifecycleEventLog
	mu    sync.Mutex
	calls int
}

func (f *postgresWorkloadControlFake) Acquire(context.Context, workloadmodule.Request) (workloadmodule.Lease, error) {
	return nil, errors.New("unused workload admission")
}

func (f *postgresWorkloadControlFake) Stats() workloadmodule.Stats { return workloadmodule.Stats{} }

func (f *postgresWorkloadControlFake) SetObserver(workloadmodule.Observer) {}

func (f *postgresWorkloadControlFake) Close() {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	f.log.add("workload")
}

func (f *postgresWorkloadControlFake) Drain(context.Context) error { return nil }

func (f *postgresWorkloadControlFake) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestPostgresResourceLifecycleStopClosesWorkloadBeforeAnalyticsOnce(t *testing.T) {
	log := &postgresLifecycleEventLog{}
	analytics := &postgresAnalyticsCloserFake{log: log}
	workloads := &postgresWorkloadControlFake{log: log}
	lifecycle := newPostgresResourceLifecycle(analytics, workloads)

	const callers = 16
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = lifecycle.Stop(context.Background())
		}()
	}
	wg.Wait()
	if err := lifecycle.Stop(context.Background()); err != nil {
		t.Fatalf("repeated Stop() error = %v", err)
	}

	if got := workloads.callCount(); got != 1 {
		t.Fatalf("workload Close calls = %d, want 1", got)
	}
	if got := analytics.callCount(); got != 1 {
		t.Fatalf("analytics Close calls = %d, want 1", got)
	}
	if got, want := log.snapshot(), []string{"workload", "analytics"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("close order = %v, want %v", got, want)
	}
}

type postgresBootstrapOwnerFake struct {
	mu        sync.Mutex
	startErr  error
	stopErr   error
	startCall int
	stopCall  int
}

func (f *postgresBootstrapOwnerFake) Start(context.Context) error {
	f.mu.Lock()
	f.startCall++
	err := f.startErr
	f.mu.Unlock()
	return err
}

func (f *postgresBootstrapOwnerFake) Stop(context.Context) error {
	f.mu.Lock()
	f.stopCall++
	err := f.stopErr
	f.mu.Unlock()
	return err
}

func (f *postgresBootstrapOwnerFake) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.startCall, f.stopCall
}

func TestPostgresBootstrapLifecycleStartFailureCleansUpOwnerAndResources(t *testing.T) {
	startErr := errors.New("startup ping failed")
	stopErr := errors.New("pool close failed")
	owner := &postgresBootstrapOwnerFake{startErr: startErr, stopErr: stopErr}
	lifecycle := newPostgresBootstrapLifecycle(owner)
	var cleanupCalls int
	lifecycle.onStartFailure = func() error {
		cleanupCalls++
		return nil
	}

	err := lifecycle.Start(context.Background())
	if !errors.Is(err, startErr) || !errors.Is(err, stopErr) {
		t.Fatalf("Start() error = %v, want startup and cleanup errors", err)
	}
	if err := lifecycle.Stop(context.Background()); !errors.Is(err, stopErr) {
		t.Fatalf("Stop() after failed Start error = %v, want %v", err, stopErr)
	}
	if cleanupCalls != 1 {
		t.Fatalf("onStartFailure calls = %d, want 1", cleanupCalls)
	}
	starts, stops := owner.counts()
	if starts != 1 || stops != 1 {
		t.Fatalf("owner calls = start %d, stop %d; want 1, 1", starts, stops)
	}
}

func TestPostgresBootstrapLifecycleSuccessfulStartAndRepeatedStop(t *testing.T) {
	owner := &postgresBootstrapOwnerFake{}
	lifecycle := newPostgresBootstrapLifecycle(owner)

	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := lifecycle.Stop(context.Background()); err != nil {
		t.Fatalf("first Stop() error = %v", err)
	}
	if err := lifecycle.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop() error = %v", err)
	}
	starts, stops := owner.counts()
	if starts != 1 || stops != 1 {
		t.Fatalf("owner calls = start %d, stop %d; want start 1, stop 1", starts, stops)
	}
}
