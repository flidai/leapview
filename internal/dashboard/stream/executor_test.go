package stream

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard/command"
	"github.com/flidai/leapview/internal/dashboard/consumer"
)

type consumerExecutorStub struct {
	execute func(context.Context, consumer.Request, consumer.Publisher) error
	leases  atomic.Int32
}

func (s *consumerExecutorStub) ExecuteConsumersPage(ctx context.Context, request consumer.Request, publish consumer.Publisher) error {
	return s.execute(ctx, request, publish)
}

func (s *consumerExecutorStub) DashboardTargetConcurrency() int { return 3 }

func (s *consumerExecutorStub) WithDashboardRefreshLease(ctx context.Context, run func(context.Context) error) error {
	s.leases.Add(1)
	return run(ctx)
}

func TestTargetWorkPublishesProgressiveConsumerResultsWithoutPresentationKnowledge(t *testing.T) {
	executor := &consumerExecutorStub{execute: func(_ context.Context, request consumer.Request, publish consumer.Publisher) error {
		if request.Concurrency != 3 {
			t.Fatalf("concurrency = %d", request.Concurrency)
		}
		request.Progress(consumer.Progress{Total: 2})
		publish(consumer.Result{Target: request.Targets[1]})
		publish(consumer.Result{Target: request.Targets[1], Metadata: true})
		request.Progress(consumer.Progress{Completed: 1, Total: 2, WorkDuration: 20 * time.Millisecond})
		publish(consumer.Result{Target: request.Targets[0]})
		request.Progress(consumer.Progress{Completed: 2, Total: 2, WorkDuration: 30 * time.Millisecond, CriticalPathDuration: 40 * time.Millisecond})
		return nil
	}}
	events := runTargetWork(t, executor, command.RefreshPlan{Targets: []command.Target{
		{Kind: command.TargetVisual, ID: "revenue"},
		{Kind: command.TargetWindow, ID: "orders"},
	}})
	if len(events) != 6 ||
		events[0].Type != RefreshEventProgress || events[0].ProgressPercent == nil || *events[0].ProgressPercent != 0 ||
		events[1].Type != RefreshEventVisual || events[2].Type != RefreshEventVisualMetadata ||
		events[3].Type != RefreshEventProgress || events[3].ProgressPercent == nil || *events[3].ProgressPercent != 50 ||
		events[4].Type != RefreshEventVisual ||
		events[5].Type != RefreshEventProgress || events[5].ProgressPercent == nil || *events[5].ProgressPercent != 100 {
		t.Fatalf("progressive events = %#v", events)
	}
	if events[3].Duration != 20*time.Millisecond || events[5].Duration != 30*time.Millisecond || events[5].StageTimingsMs["targetCriticalPath"] != 40 {
		t.Fatalf("progress timing events = %#v", events)
	}
	if executor.leases.Load() != 1 {
		t.Fatalf("refresh leases = %d", executor.leases.Load())
	}
}

func TestTargetWorkScopesConsumerFailuresAndSuppressesCancellation(t *testing.T) {
	executor := &consumerExecutorStub{execute: func(_ context.Context, request consumer.Request, publish consumer.Publisher) error {
		publish(consumer.Result{Target: request.Targets[0], Err: errors.New("denied")})
		publish(consumer.Result{Target: request.Targets[1], Err: context.Canceled})
		return nil
	}}
	events := runTargetWork(t, executor, command.RefreshPlan{Targets: []command.Target{
		{Kind: command.TargetVisual, ID: "margin"},
		{Kind: command.TargetVisual, ID: "orders"},
	}})
	if len(events) != 1 || events[0].Type != RefreshEventTargetError || events[0].Target != "visual:margin" {
		t.Fatalf("events = %#v", events)
	}
}

func TestTargetWorkPublishesAcceptedCacheOutcomes(t *testing.T) {
	executor := &consumerExecutorStub{execute: func(ctx context.Context, _ consumer.Request, _ consumer.Publisher) error {
		dataquery.ObserveCacheOutcome(ctx, dataquery.CacheHit)
		dataquery.ObserveCache(ctx, dataquery.CacheObservation{Phase: dataquery.CacheObservationLookup, HitSource: dataquery.CacheHitCurrentGeneration})
		return nil
	}}
	observed := ""
	typed := dataquery.CacheObservation{}
	events := []RefreshEvent{}
	TargetWork(executor, WorkRequest{
		DashboardID: "commerce",
		PageID:      "overview",
		CacheObserved: func(outcome string) {
			observed = outcome
		},
		CacheObservationObserved: func(observation dataquery.CacheObservation) { typed = observation },
	})(context.Background(), func(event RefreshEvent) bool {
		events = append(events, event)
		return true
	})
	if observed != dataquery.CacheHit || len(events) != 1 || events[0].CacheOutcome != dataquery.CacheHit {
		t.Fatalf("observed=%q events=%#v", observed, events)
	}
	if typed.Phase != dataquery.CacheObservationLookup || typed.HitSource != dataquery.CacheHitCurrentGeneration {
		t.Fatalf("typed cache observation = %#v", typed)
	}
}

func runTargetWork(t *testing.T, executor TargetMetrics, plan command.RefreshPlan) []RefreshEvent {
	t.Helper()
	events := []RefreshEvent{}
	TargetWork(executor, WorkRequest{DashboardID: "commerce", PageID: "overview", Plan: plan})(context.Background(), func(event RefreshEvent) bool {
		events = append(events, event)
		return true
	})
	return events
}
