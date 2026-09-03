package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	analyticsl3 "github.com/flidai/leapview/internal/analytics/cache/l3"
	cachepostgres "github.com/flidai/leapview/internal/analytics/cache/postgres"
	workloadmodule "github.com/flidai/leapview/internal/workload/module"
)

const (
	defaultL3GCInterval = time.Hour
	defaultL3GCLease    = 10 * time.Minute
)

type l3GCPageCollector interface {
	GCPage(context.Context, string) (analyticsl3.GCResult, error)
}

type l3GCAuthority interface {
	AcquireL3GCLease(context.Context, string, string, time.Duration) (cachepostgres.L3GCLease, error)
	RenewL3GCLease(context.Context, cachepostgres.L3GCLease, time.Duration) error
	ReleaseL3GCLease(context.Context, cachepostgres.L3GCLease) error
	AdvanceL3GCCursor(context.Context, cachepostgres.L3GCLease, string, bool) error
}

type l3GCWorker struct {
	interval       time.Duration
	leaseDuration  time.Duration
	securityDomain string
	ownerID        string
	authority      l3GCAuthority
	collector      l3GCPageCollector
	acquire        func(context.Context) (workloadmodule.Lease, error)
	logger         *slog.Logger

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

type l3GCWorkerConfig struct {
	Interval       time.Duration
	LeaseDuration  time.Duration
	SecurityDomain string
	OwnerID        string
	Authority      l3GCAuthority
	Collector      l3GCPageCollector
	Acquire        func(context.Context) (workloadmodule.Lease, error)
	Logger         *slog.Logger
}

func newL3GCWorker(config l3GCWorkerConfig) *l3GCWorker {
	interval := config.Interval
	if interval <= 0 {
		interval = defaultL3GCInterval
	}
	leaseDuration := config.LeaseDuration
	if leaseDuration <= 0 {
		leaseDuration = defaultL3GCLease
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &l3GCWorker{interval: interval, leaseDuration: leaseDuration, securityDomain: config.SecurityDomain,
		ownerID: config.OwnerID, authority: config.Authority, collector: config.Collector, acquire: config.Acquire, logger: logger}
}

func (w *l3GCWorker) Start(ctx context.Context) error {
	if w == nil || w.authority == nil || w.collector == nil || w.acquire == nil || w.securityDomain == "" || w.ownerID == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	w.mu.Lock()
	if w.cancel != nil {
		w.mu.Unlock()
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	w.cancel, w.done = cancel, done
	w.mu.Unlock()
	go func() {
		defer close(done)
		w.runPass(runCtx)
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				w.runPass(runCtx)
			}
		}
	}()
	return nil
}

func (w *l3GCWorker) Stop(ctx context.Context) error {
	if w == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	if cancel == nil {
		w.mu.Unlock()
		return nil
	}
	cancel()
	w.mu.Unlock()
	select {
	case <-done:
		w.mu.Lock()
		if w.done == done {
			w.cancel, w.done = nil, nil
		}
		w.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *l3GCWorker) runPass(ctx context.Context) {
	workloadLease, err := w.acquire(ctx)
	if err != nil {
		w.logger.DebugContext(ctx, "L3 cache garbage collection skipped", "error", err)
		return
	}
	if workloadLease == nil {
		w.logger.DebugContext(ctx, "L3 cache garbage collection skipped", "error", errors.New("workload lease is nil"))
		return
	}
	defer workloadLease.Release()
	passCtx := workloadLease.Context()
	if passCtx == nil {
		passCtx = ctx
	}
	// Make workload admission the direct parent so an already-expired lease is
	// observed synchronously. The scheduler context remains a second cancel
	// source for shutdown.
	combinedCtx, cancel := context.WithCancel(passCtx)
	stopLeaseWatch := context.AfterFunc(ctx, cancel)
	defer func() {
		stopLeaseWatch()
		cancel()
	}()
	if combinedCtx.Err() != nil {
		return
	}

	lease, err := w.authority.AcquireL3GCLease(combinedCtx, w.securityDomain, w.ownerID, w.leaseDuration)
	if err != nil {
		if errors.Is(err, cachepostgres.ErrBusy) {
			w.logger.DebugContext(ctx, "L3 cache garbage collection owned by another node")
			return
		}
		w.logger.WarnContext(ctx, "L3 cache garbage collection lease failed", "error", err)
		return
	}
	guard := newL3GCLeaseGuard(combinedCtx, w.authority, lease, w.leaseDuration)
	result, passErr := w.collector.GCPage(guard.ctx, lease.CursorObjectKey)
	if passErr == nil {
		passErr = guard.failure()
	}
	if passErr == nil {
		passErr = w.authority.AdvanceL3GCCursor(guard.ctx, lease, result.NextCursor, result.NextCursor == "")
	}
	releaseErr := guard.stopAndRelease(combinedCtx)
	if passErr == nil {
		passErr = releaseErr
	}
	if passErr != nil {
		w.logger.WarnContext(ctx, "L3 cache garbage collection failed", "error", passErr)
		return
	}
	w.logger.InfoContext(ctx, "L3 cache garbage collection page completed", "scanned", result.Scanned, "deleted", result.Deleted, "skipped", result.Skipped, "cycle", lease.Cycle)
}

type l3GCLeaseGuard struct {
	authority l3GCAuthority
	lease     cachepostgres.L3GCLease
	ctx       context.Context
	cancel    context.CancelFunc
	stop      chan struct{}
	renewErr  chan error
	duration  time.Duration
	renewWait time.Duration
}

func newL3GCLeaseGuard(parent context.Context, authority l3GCAuthority, lease cachepostgres.L3GCLease, duration time.Duration) *l3GCLeaseGuard {
	ctx, cancel := context.WithCancel(parent)
	guard := &l3GCLeaseGuard{authority: authority, lease: lease, ctx: ctx, cancel: cancel, stop: make(chan struct{}), renewErr: make(chan error, 1), duration: duration}
	interval := duration / 3
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	guard.renewWait = interval
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := guard.renew(); err != nil {
					select {
					case guard.renewErr <- err:
					default:
					}
					cancel()
					return
				}
			case <-guard.stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return guard
}

func (g *l3GCLeaseGuard) renew() error {
	renewCtx, cancel := context.WithTimeout(g.ctx, g.renewWait)
	defer cancel()
	return g.authority.RenewL3GCLease(renewCtx, g.lease, g.duration)
}

func (g *l3GCLeaseGuard) failure() error {
	select {
	case err := <-g.renewErr:
		return err
	case <-g.ctx.Done():
		return g.ctx.Err()
	default:
		return nil
	}
}

func (g *l3GCLeaseGuard) stopAndRelease(parent context.Context) error {
	select {
	case <-g.stop:
	default:
		close(g.stop)
	}
	g.cancel()
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	return g.authority.ReleaseL3GCLease(cleanupCtx, g.lease)
}
