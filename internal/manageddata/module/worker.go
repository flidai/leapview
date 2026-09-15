package module

import (
	"context"
	"log/slog"
	"time"

	"github.com/flidai/leapview/internal/manageddata/control"
	platformlifecycle "github.com/flidai/leapview/internal/platform/lifecycle"
)

type MaintenanceLease interface {
	Context() context.Context
	Release()
}

type MaintenanceWorkerConfig struct {
	Interval time.Duration
	Acquire  func(context.Context) (MaintenanceLease, error)
	Logger   *slog.Logger
}

type uploadExpirer interface {
	ExpireUploads(context.Context) (control.ExpireResult, error)
}

type maintenanceWorker struct {
	expirer   uploadExpirer
	acquire   func(context.Context) (MaintenanceLease, error)
	logger    *slog.Logger
	lifecycle *platformlifecycle.IntervalWorker
}

func newMaintenanceWorker(expirer uploadExpirer, config MaintenanceWorkerConfig) *maintenanceWorker {
	interval := config.Interval
	if interval <= 0 {
		interval = time.Hour
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	// Always return a worker, including for the disabled managed-data surface.
	// This makes Module.Start/Stop safe by construction and avoids a lifecycle
	// caller having to special-case an optional worker.
	worker := &maintenanceWorker{expirer: expirer, acquire: config.Acquire, logger: logger}
	worker.lifecycle = platformlifecycle.NewIntervalWorker(interval, worker.runPass)
	return worker
}

func (w *maintenanceWorker) Start(ctx context.Context) {
	if w == nil || w.expirer == nil || w.acquire == nil || w.lifecycle == nil {
		return
	}
	_ = w.lifecycle.Start(ctx)
}

func (w *maintenanceWorker) Stop(ctx context.Context) error {
	if w == nil {
		return nil
	}
	if w.lifecycle == nil {
		return nil
	}
	return w.lifecycle.Stop(ctx)
}

func (w *maintenanceWorker) runPass(ctx context.Context) {
	lease, err := w.acquire(ctx)
	if err != nil {
		w.logger.DebugContext(ctx, "managed-data maintenance skipped", "error", err)
		return
	}
	defer lease.Release()
	result, err := w.expirer.ExpireUploads(lease.Context())
	if err != nil {
		w.logger.WarnContext(ctx, "managed-data upload expiration failed", "error", err)
		return
	}
	if result.Expired > 0 {
		w.logger.InfoContext(ctx, "expired managed-data upload sessions", "count", result.Expired)
	}
	if result.Cleaned > 0 {
		w.logger.InfoContext(ctx, "cleaned managed-data upload staging", "count", result.Cleaned)
	}
	if result.CleanupBacklog > 0 || result.CleanupFailures > 0 {
		w.logger.WarnContext(ctx, "managed-data upload staging cleanup backlog", "backlog", result.CleanupBacklog, "failures", result.CleanupFailures)
	}
}
