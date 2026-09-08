package module

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard/publication"
	dashboardstream "github.com/flidai/leapview/internal/dashboard/stream"
)

// PrewarmConfig selects durable publication row IDs, not public URL tokens.
// Empty selection disables warming. The initial implementation is serial.
type PrewarmConfig struct {
	PublicationIDs    []string
	MaxPublications   int
	MaxTargets        int
	ExecutionDeadline time.Duration
	Concurrency       int
}

func (c PrewarmConfig) validate() error {
	if len(c.PublicationIDs) == 0 {
		return nil
	}
	if c.MaxPublications <= 0 || len(c.PublicationIDs) > c.MaxPublications || c.MaxTargets <= 0 || c.ExecutionDeadline <= 0 || c.Concurrency != 1 {
		return fmt.Errorf("prewarm requires bounded publications/targets, a positive deadline, and concurrency 1")
	}
	seen := map[string]bool{}
	for _, id := range c.PublicationIDs {
		if id == "" || id != strings.TrimSpace(id) || seen[id] {
			return fmt.Errorf("prewarm publication IDs must be non-empty, canonical, and unique")
		}
		seen[id] = true
	}
	return nil
}

type prewarmVersion struct {
	generation string
	revision   int64
	publicID   string
}
type prewarmItem struct {
	version   prewarmVersion
	row       publication.Publication
	pending   bool
	attempted bool
}
type prewarmResult struct{ outcome, reason string }

// One slot per configured publication; no historical generation map or queue.
// Each publication/version gets one warmup attempt. Repeated reconciliation
// ticks do not retry failed or canceled attempts; a newly observed publication
// version (generation, revision, or public ID) may schedule another attempt.
type prewarmCoordinator struct {
	mu        sync.Mutex
	items     map[string]*prewarmItem
	order     []string
	deadline  time.Duration
	wake      chan struct{}
	runningID string
	cancel    context.CancelFunc
	closed    bool
	execute   func(context.Context, publication.Publication) prewarmResult
	observe   func(string, string)
}

func newPrewarmCoordinator(config PrewarmConfig, execute func(context.Context, publication.Publication) prewarmResult, observe func(string, string)) *prewarmCoordinator {
	if len(config.PublicationIDs) == 0 {
		return nil
	}
	c := &prewarmCoordinator{items: map[string]*prewarmItem{}, order: append([]string(nil), config.PublicationIDs...), deadline: config.ExecutionDeadline, wake: make(chan struct{}, 1), execute: execute, observe: observe}
	for _, id := range c.order {
		c.items[id] = &prewarmItem{}
	}
	return c
}

// reconcile only replaces bounded work descriptions. Execution and observer
// callbacks never run in this monitor call or under the coordinator lock.
func (c *prewarmCoordinator) reconcile(rows []publication.Publication, localGeneration string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	eligible := make(map[string]publication.Publication, len(c.items))
	for _, row := range rows {
		if _, selected := c.items[row.ID]; selected && localGeneration != "" && row.Status() == publication.StatusActive && row.ServingStateID == localGeneration {
			eligible[row.ID] = row
		}
	}
	for id, item := range c.items {
		row, ok := eligible[id]
		if !ok {
			item.pending = false
			if c.runningID == id && c.cancel != nil {
				c.cancel()
			}
			continue
		}
		version := prewarmVersion{row.ServingStateID, row.Revision, row.PublicID}
		if version == item.version {
			if !item.attempted {
				item.pending = true
			}
			continue
		}
		if c.runningID == id && c.cancel != nil {
			c.cancel()
		}
		row.DependencyAssetIDs = append([]string(nil), row.DependencyAssetIDs...)
		row.AllowedOrigins = append([]string(nil), row.AllowedOrigins...)
		*item = prewarmItem{version: version, row: row, pending: true}
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *prewarmCoordinator) stop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.closed = true
	if c.cancel != nil {
		c.cancel()
	}
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *prewarmCoordinator) run(ctx context.Context) {
	defer c.stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
		}
		for {
			c.mu.Lock()
			if c.closed || ctx.Err() != nil {
				c.mu.Unlock()
				return
			}
			var row publication.Publication
			for _, id := range c.order {
				if item := c.items[id]; item.pending {
					row = item.row
					item.pending = false
					item.attempted = true
					break
				}
			}
			if row.ID == "" {
				c.mu.Unlock()
				break
			}
			workCtx, cancel := context.WithTimeout(ctx, c.deadline)
			c.runningID, c.cancel = row.ID, cancel
			c.mu.Unlock()
			c.emit("attempted", "scheduled")
			result := c.execute(workCtx, row)
			if workCtx.Err() != nil {
				reason := "canceled"
				if workCtx.Err() == context.DeadlineExceeded {
					reason = "deadline"
				}
				result = prewarmResult{"canceled", reason}
			}
			cancel()
			c.mu.Lock()
			c.runningID, c.cancel = "", nil
			c.mu.Unlock()
			c.emit(result.outcome, result.reason)
		}
	}
}

func (c *prewarmCoordinator) emit(outcome, reason string) {
	if c.observe != nil {
		c.observe(outcome, reason)
	}
}

func (m *Module) executePrewarm(ctx context.Context, scheduled publication.Publication) prewarmResult {
	row, err := m.ResolvePublic(ctx, scheduled.PublicID)
	if err != nil || row.ID != scheduled.ID || row.Revision != scheduled.Revision || row.ServingStateID != scheduled.ServingStateID {
		return prewarmResult{"skipped", "publication_changed"}
	}
	metrics := m.handler.Metrics
	leaser, ok := metrics.(interface {
		WithDashboardRefreshLease(context.Context, func(context.Context) error) error
	})
	if !ok {
		return prewarmResult{"skipped", "runtime_unavailable"}
	}
	result := prewarmResult{"failed", "execution"}
	ctx = PublicationExecutionContext(ctx, row, "")
	err = leaser.WithDashboardRefreshLease(ctx, func(ctx context.Context) error {
		request, prepared, err := preparePublicDashboardRefresh(ctx, row)
		if err != nil {
			result = prewarmResult{"skipped", "preparation"}
			return err
		}
		if len(prepared.Plan.Targets) == 0 || len(prepared.Plan.Targets) > m.prewarmConfig.MaxTargets {
			result = prewarmResult{"skipped", "target_limit"}
			return nil
		}
		ctx = PublicationExecutionContext(ctx, row, request.ModelID)
		var mu sync.Mutex
		failed := false
		bypassed, rejected := false, false
		work := dashboardstream.TargetWork(metrics, dashboardstream.WorkRequest{
			DashboardID: request.DashboardID, PageID: request.PageID, ModelID: request.ModelID, Filters: prepared.Filters, Plan: prepared.Plan,
			CacheObservationObserved: func(observation dataquery.CacheObservation) {
				mu.Lock()
				defer mu.Unlock()
				if observation.Phase == dataquery.CacheObservationAdmission && observation.Decision == dataquery.CacheAdmissionBypassed {
					bypassed = true
				}
				if observation.Phase == dataquery.CacheObservationStore && observation.StoreOutcome != dataquery.CacheStoreStored {
					rejected = true
				}
			},
		})
		work(ctx, func(event dashboardstream.RefreshEvent) bool {
			mu.Lock()
			defer mu.Unlock()
			if event.Err != nil {
				failed = true
			}
			return ctx.Err() == nil
		})
		mu.Lock()
		defer mu.Unlock()
		switch {
		case failed:
			result = prewarmResult{"failed", "execution"}
		case bypassed:
			result = prewarmResult{"completed", "cache_bypassed"}
		case rejected:
			result = prewarmResult{"completed", "store_rejected"}
		default:
			result = prewarmResult{"completed", "executed"}
		}
		return ctx.Err()
	})
	if err != nil && result.outcome == "completed" {
		return prewarmResult{"failed", "execution"}
	}
	return result
}
