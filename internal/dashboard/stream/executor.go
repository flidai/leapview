package stream

import (
	"context"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/command"
	"github.com/flidai/leapview/internal/dashboard/consumer"
)

// TargetMetrics is intentionally renderer-neutral. Query fusion, fallback,
// table progression, and filter-option execution belong to the consumer
// runtime so every delivery surface uses the same governed execution plan.
type TargetMetrics interface {
	consumer.Executor
}

type targetConcurrencyMetrics interface {
	DashboardTargetConcurrency() int
}

type refreshLeaseMetrics interface {
	WithDashboardRefreshLease(context.Context, func(context.Context) error) error
}

type WorkRequest struct {
	DashboardID string
	PageID      string
	ModelID     string
	Filters     dashboard.Filters
	Plan        command.RefreshPlan
	Before      func(context.Context) error
	// Observers may be invoked concurrently by independent consumer jobs.
	EventObserved            EventPublisher
	CacheObserved            dataquery.CacheOutcomeObserver
	CacheObservationObserved dataquery.CacheObserver
}

// TargetWork owns refresh delivery only. The consumer executor owns query
// planning and execution, which keeps SSE generation semantics independent of
// charts, tables, and future presentation types.
func TargetWork(metrics TargetMetrics, request WorkRequest) RefreshWork {
	return func(ctx context.Context, publish RefreshPublisher) {
		observedPublish := func(event RefreshEvent) bool {
			accepted := publish(event)
			if accepted && request.EventObserved != nil {
				request.EventObserved(event)
			}
			return accepted
		}
		ctx = dataquery.WithCacheOutcomeObserver(ctx, func(outcome string) {
			if observedPublish(RefreshEvent{Type: RefreshEventCacheOutcome, CacheOutcome: outcome}) && request.CacheObserved != nil {
				request.CacheObserved(outcome)
			}
		})
		ctx = dataquery.WithCacheObserver(ctx, request.CacheObservationObserved)
		if request.Before != nil {
			if err := request.Before(ctx); err != nil {
				publishRefreshError(ctx, observedPublish, err)
				return
			}
		}
		run := func(ctx context.Context) error {
			return executeConsumers(ctx, metrics, request, observedPublish)
		}
		if capability, ok := metrics.(refreshLeaseMetrics); ok {
			if err := capability.WithDashboardRefreshLease(ctx, run); err != nil {
				publishRefreshError(ctx, observedPublish, err)
			}
			return
		}
		if err := run(ctx); err != nil {
			publishRefreshError(ctx, observedPublish, err)
		}
	}
}

func publishRefreshError(ctx context.Context, publish RefreshPublisher, err error) {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		return
	}
	publish(RefreshEvent{Type: RefreshEventTargetError, Target: "refresh", Err: err})
}

func executeConsumers(ctx context.Context, executor consumer.Executor, request WorkRequest, publish RefreshPublisher) error {
	consumerRequest := consumer.Request{
		DashboardID: request.DashboardID,
		PageID:      request.PageID,
		ModelID:     request.ModelID,
		Command:     request.Plan.Command,
		Filters:     request.Filters,
		Targets:     append([]consumer.Target{}, request.Plan.Targets...),
		Concurrency: refreshConcurrencyFromExecutor(executor),
		Progress: func(progress consumer.Progress) {
			stageTimings := map[string]float64(nil)
			if progress.CriticalPathDuration > 0 {
				stageTimings = map[string]float64{
					"targetCriticalPath": float64(progress.CriticalPathDuration.Microseconds()) / 1000,
				}
			}
			publish(RefreshEvent{
				Type:            RefreshEventProgress,
				ProgressPercent: consumerProgressPercent(progress),
				Duration:        progress.WorkDuration,
				StageTimingsMs:  stageTimings,
			})
		},
	}
	return executor.ExecuteConsumersPage(ctx, consumerRequest, func(result consumer.Result) bool {
		event := RefreshEvent{
			Target:         result.Target.ID,
			Duration:       result.Duration,
			Queries:        result.Queries,
			StageTimingsMs: result.StageTimingsMs,
		}
		if result.Err != nil {
			if errors.Is(result.Err, context.Canceled) || errors.Is(result.Err, context.DeadlineExceeded) || ctx.Err() != nil {
				return false
			}
			event.Type = RefreshEventTargetError
			event.Target = result.Target.Key()
			event.Err = result.Err
			return publish(event)
		}
		switch result.Target.Kind {
		case consumer.KindVisual:
			event.Type = RefreshEventVisual
			event.Value = result.Envelope
		case consumer.KindWindow:
			if result.Metadata {
				event.Type = RefreshEventVisualMetadata
			} else {
				event.Type = RefreshEventVisual
			}
			event.Value = result.Envelope
		default:
			event.Type = RefreshEventTargetError
			event.Target = "refresh"
			event.Err = fmt.Errorf("unknown consumer target kind %q", result.Target.Kind)
		}
		return publish(event)
	})
}

func consumerProgressPercent(progress consumer.Progress) *float64 {
	percent := float64(100)
	if progress.Total > 0 {
		completed := max(0, min(progress.Completed, progress.Total))
		percent = (float64(completed) / float64(progress.Total)) * 100
	}
	return &percent
}

func refreshConcurrencyFromExecutor(executor consumer.Executor) int {
	if capability, ok := executor.(targetConcurrencyMetrics); ok {
		return max(1, capability.DashboardTargetConcurrency())
	}
	return 1
}
