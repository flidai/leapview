package lifecycle

import (
	"context"
	"errors"
	"sync"
)

type Component struct {
	Start func(context.Context) error
	Stop  func(context.Context) error
}

type startAttempt struct {
	done chan struct{}
	err  error
}

type stopAttempt struct {
	done chan struct{}
	err  error
}

type Group struct {
	components []Component

	mu            sync.Mutex
	cancel        context.CancelFunc
	started       int
	starting      *startAttempt
	stopping      *stopAttempt
	stopRequested bool
	epoch         uint64
}

func New(components ...Component) *Group {
	return &Group{components: append([]Component(nil), components...)}
}

func (g *Group) Start(ctx context.Context) error {
	if g == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	g.mu.Lock()
	if g.cancel != nil {
		attempt := g.starting
		stopping := g.stopping
		g.mu.Unlock()
		if attempt == nil {
			if stopping != nil {
				select {
				case <-stopping.done:
					if stopping.err != nil {
						return stopping.err
					}
					return g.Start(ctx)
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		}
		select {
		case <-attempt.done:
			return attempt.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	attempt := &startAttempt{done: make(chan struct{})}
	g.epoch++
	epoch := g.epoch
	g.cancel = cancel
	g.starting = attempt
	g.mu.Unlock()

	for index, component := range g.components {
		if err := runCtx.Err(); err != nil {
			return g.rollbackStart(runCtx, cancel, attempt, epoch, err)
		}
		if component.Start == nil {
			g.mu.Lock()
			g.started = index + 1
			stopRequested := g.stopRequested
			g.mu.Unlock()
			if stopRequested || runCtx.Err() != nil {
				return g.rollbackStart(runCtx, cancel, attempt, epoch, context.Canceled)
			}
			continue
		}
		if err := component.Start(runCtx); err != nil {
			return g.rollbackStart(runCtx, cancel, attempt, epoch, err)
		}
		g.mu.Lock()
		g.started = index + 1
		stopRequested := g.stopRequested
		g.mu.Unlock()
		if stopRequested || runCtx.Err() != nil {
			return g.rollbackStart(runCtx, cancel, attempt, epoch, context.Canceled)
		}
	}
	g.mu.Lock()
	stopRequested := g.stopRequested
	runErr := runCtx.Err()
	if stopRequested || runErr != nil {
		g.mu.Unlock()
		if runErr == nil {
			runErr = context.Canceled
		}
		return g.rollbackStart(runCtx, cancel, attempt, epoch, runErr)
	}
	g.starting = nil
	close(attempt.done)
	g.mu.Unlock()
	return nil
}

func (g *Group) Stop(ctx context.Context) error {
	if g == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	g.mu.Lock()
	cancel, attempt, epoch := g.cancel, g.starting, g.epoch
	if cancel == nil {
		g.mu.Unlock()
		return nil
	}
	g.stopRequested = true
	var ownedStop *stopAttempt
	if attempt == nil && g.stopping == nil {
		ownedStop = &stopAttempt{done: make(chan struct{})}
		g.stopping = ownedStop
	}
	stopping := g.stopping
	cancel()
	g.mu.Unlock()
	if attempt != nil {
		select {
		case <-attempt.done:
		case <-ctx.Done():
			return ctx.Err()
		}
		// A stop request observed during startup is rolled back by Start before
		// the attempt is completed, so there is no active epoch left to stop.
		return nil
	}
	if ownedStop == nil {
		select {
		case <-stopping.done:
			return stopping.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return g.stopStarted(ctx, epoch, ownedStop)
}

func (g *Group) stopStarted(ctx context.Context, epoch uint64, ownedStop *stopAttempt) error {
	g.mu.Lock()
	if g.epoch != epoch {
		g.mu.Unlock()
		return nil
	}
	if ownedStop == nil {
		if stopping := g.stopping; stopping != nil {
			g.mu.Unlock()
			select {
			case <-stopping.done:
				return stopping.err
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		ownedStop = &stopAttempt{done: make(chan struct{})}
		g.stopping = ownedStop
	}
	stopping := ownedStop
	started := g.started
	g.started = 0
	g.mu.Unlock()
	var errs []error
	for index := started - 1; index >= 0; index-- {
		if stop := g.components[index].Stop; stop != nil {
			errs = append(errs, stop(ctx))
		}
	}
	err := errors.Join(errs...)
	g.mu.Lock()
	if g.epoch == epoch {
		g.cancel = nil
		g.stopRequested = false
		g.stopping = nil
	}
	stopping.err = err
	close(stopping.done)
	g.mu.Unlock()
	return err
}

func (g *Group) rollbackStart(ctx context.Context, cancel context.CancelFunc, attempt *startAttempt, epoch uint64, cause error) error {
	cancel()
	g.mu.Lock()
	started := g.started
	g.started = 0
	g.mu.Unlock()
	rollbackCtx := context.WithoutCancel(ctx)
	var errs []error
	for index := started - 1; index >= 0; index-- {
		if stop := g.components[index].Stop; stop != nil {
			errs = append(errs, stop(rollbackCtx))
		}
	}
	err := errors.Join(append([]error{cause}, errs...)...)
	g.mu.Lock()
	if g.epoch == epoch {
		g.cancel = nil
		g.stopRequested = false
		g.starting = nil
	}
	attempt.err = err
	close(attempt.done)
	g.mu.Unlock()
	return err
}

// CheckPrepared checks the lifecycle composition without starting any task.
// Dependency/role health remains owned by the composing application's live
// preparation checks; this does not claim the workers have run successfully.
func (g *Group) CheckPrepared() error {
	if g == nil || len(g.components) == 0 {
		return errors.New("worker group is not configured")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cancel != nil || g.stopRequested {
		return errors.New("worker group has already started or stopped")
	}
	for _, component := range g.components {
		if component.Start == nil || component.Stop == nil {
			return errors.New("worker lifecycle ownership is incomplete")
		}
	}
	return nil
}
