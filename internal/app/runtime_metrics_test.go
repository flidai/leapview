package app

import (
	"context"
	"errors"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

func TestRuntimeMetricsQueryDashboardUsesRuntimeLease(t *testing.T) {
	provider := &leaseRecordingProvider{}
	metrics := NewRuntimeMetrics(provider, "test")

	if _, err := metrics.QueryDashboardPage(context.Background(), "executive-sales", "overview", dashboard.Filters{}); err != nil {
		t.Fatalf("query dashboard: %v", err)
	}
	if provider.lease == nil {
		t.Fatal("runtime lease was not acquired")
	}
	if !provider.runtime.called {
		t.Fatal("runtime was not queried")
	}
	if !provider.lease.released {
		t.Fatal("runtime lease was not released after query")
	}
	if provider.runtime.releasedDuringCall {
		t.Fatal("runtime lease was released before query completed")
	}
}

func TestRuntimeMetricsReleasesRuntimeLeaseWhenQueryFails(t *testing.T) {
	wantErr := errors.New("query failed")
	provider := &leaseRecordingProvider{runtime: leaseRecordingRuntime{queryErr: wantErr}}
	metrics := NewRuntimeMetrics(provider, "test")

	if _, err := metrics.QueryDashboardPage(context.Background(), "executive-sales", "overview", dashboard.Filters{}); !errors.Is(err, wantErr) {
		t.Fatalf("query dashboard error = %v, want %v", err, wantErr)
	}
	if provider.lease == nil || !provider.lease.released {
		t.Fatal("runtime lease was not released after query failure")
	}
}

func TestRuntimeMetricsDashboardRefreshLeasePinsOneRuntimeAcrossTargets(t *testing.T) {
	first := &targetLeaseRuntime{id: "first"}
	second := &targetLeaseRuntime{id: "second"}
	provider := &switchingLeaseProvider{current: first}
	metrics := NewRuntimeMetrics(provider, "test")
	refreshMetrics := metrics.(interface {
		WithDashboardRefreshLease(context.Context, func(context.Context) error) error
		ExecuteConsumersPage(context.Context, consumer.Request, consumer.Publisher) error
	})

	err := refreshMetrics.WithDashboardRefreshLease(context.Background(), func(ctx context.Context) error {
		provider.current = second
		if err := refreshMetrics.ExecuteConsumersPage(ctx, consumer.Request{DashboardID: "executive-sales", PageID: "overview", Targets: []consumer.Target{{Kind: consumer.KindVisual, ID: "one"}}}, func(consumer.Result) bool { return true }); err != nil {
			return err
		}
		if err := refreshMetrics.ExecuteConsumersPage(ctx, consumer.Request{DashboardID: "executive-sales", PageID: "overview", Targets: []consumer.Target{{Kind: consumer.KindVisual, ID: "two"}}}, func(consumer.Result) bool { return true }); err != nil {
			return err
		}
		if provider.lease.released {
			t.Fatal("refresh runtime lease released before targets completed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.acquires != 1 {
		t.Fatalf("runtime lease acquisitions = %d, want 1", provider.acquires)
	}
	if first.calls != 2 || second.calls != 0 {
		t.Fatalf("target calls first=%d second=%d, want 2/0", first.calls, second.calls)
	}
	if !provider.lease.released {
		t.Fatal("refresh runtime lease was not released")
	}
}

type switchingLeaseProvider struct {
	current  runtimehost.Runtime
	lease    *recordingLease
	acquires int
}

func (p *switchingLeaseProvider) Acquire(context.Context) (runtimehost.Lease, error) {
	p.acquires++
	p.lease = &recordingLease{runtime: p.current, snapshotID: 42}
	return p.lease, nil
}

type targetLeaseRuntime struct {
	id    string
	calls int
}

func (r *targetLeaseRuntime) Close() error { return nil }

func (r *targetLeaseRuntime) Resolver() dashboardresolver.Resolver { return fakeMetrics{}.Resolver() }

func (r *targetLeaseRuntime) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	return fakeMetrics{}.SemanticModel(modelID)
}

func (r *targetLeaseRuntime) DefaultFilters(dashboardID string) dashboard.Filters {
	return fakeMetrics{}.DefaultFilters(dashboardID)
}

func (r *targetLeaseRuntime) ExecuteConsumersPage(_ context.Context, request consumer.Request, publish consumer.Publisher) error {
	for _, target := range request.Targets {
		r.calls++
		publish(consumer.Result{Target: target})
	}
	return nil
}

type leaseRecordingProvider struct {
	runtime leaseRecordingRuntime
	lease   *recordingLease
}

func (p *leaseRecordingProvider) Acquire(context.Context) (runtimehost.Lease, error) {
	p.lease = &recordingLease{runtime: &p.runtime, snapshotID: 42}
	p.runtime.lease = p.lease
	return p.lease, nil
}

type leaseRecordingRuntime struct {
	lease              *recordingLease
	called             bool
	releasedDuringCall bool
	queryErr           error
}

func (r *leaseRecordingRuntime) Close() error {
	return nil
}

func (r *leaseRecordingRuntime) Resolver() dashboardresolver.Resolver {
	return fakeMetrics{}.Resolver()
}

func (r *leaseRecordingRuntime) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	return fakeMetrics{}.SemanticModel(modelID)
}

func (r *leaseRecordingRuntime) DefaultFilters(dashboardID string) dashboard.Filters {
	return fakeMetrics{}.DefaultFilters(dashboardID)
}

func (r *leaseRecordingRuntime) QueryDashboardPage(context.Context, string, string, dashboard.Filters) (dashboard.Patch, error) {
	r.called = true
	if r.lease != nil && r.lease.released {
		r.releasedDuringCall = true
	}
	return dashboard.Patch{}, r.queryErr
}

type recordingLease struct {
	runtime    runtimehost.Runtime
	snapshotID int64
	released   bool
}

func (l *recordingLease) Runtime() runtimehost.Runtime {
	return l.runtime
}

func (l *recordingLease) ServingStateID() servingstate.ID {
	return "dep_test"
}

func (l *recordingLease) DuckLakeSnapshotID() int64 {
	return l.snapshotID
}

func (l *recordingLease) Release() {
	l.released = true
}
