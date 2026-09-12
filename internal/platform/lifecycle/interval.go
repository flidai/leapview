package lifecycle

import (
	"context"
	"sync"
	"time"
)

// IntervalWorker owns the cancellation, ticker, and bounded drain lifecycle
// for one in-process periodic task. The task remains responsible for its own
// admission, logging, and error handling; Run is invoked immediately and then
// once per interval until the worker is stopped.
type IntervalWorker struct {
	interval time.Duration
	run      func(context.Context)

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// NewIntervalWorker constructs a worker for run. A non-positive interval is
// normalized to one hour, matching a safe scheduler default and preventing
// time.NewTicker from panicking on bad config.
func NewIntervalWorker(interval time.Duration, run func(context.Context)) *IntervalWorker {
	if interval <= 0 {
		interval = time.Hour
	}
	return &IntervalWorker{interval: interval, run: run}
}

// Start starts the worker once. A nil worker or task is a disabled worker and
// is deliberately safe to start, which keeps optional maintenance surfaces
// out of their callers' lifecycle special cases.
func (w *IntervalWorker) Start(ctx context.Context) error {
	if w == nil || w.run == nil {
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
		w.run(runCtx)
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				w.run(runCtx)
			}
		}
	}()
	return nil
}

// Stop cancels the active task and waits for its goroutine to drain, bounded
// by ctx. If the context expires, a later Stop can continue waiting and finish
// the cleanup after the task returns.
func (w *IntervalWorker) Stop(ctx context.Context) error {
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
