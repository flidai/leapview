// Package sourcework controls admission of analytics source reads and pool
// refreshes. Drained means admitted work has returned, including its cleanup;
// it does not prove idle resources closed successfully or authorize cutover.
package sourcework

import (
	"context"
	"errors"
	"sync"
)

var (
	ErrClosed        = errors.New("source work admission is closed")
	ErrPaused        = errors.New("source work admission is already paused")
	ErrInvalidPause  = errors.New("source work pause is no longer current")
	ErrWorkActive    = errors.New("source work has not drained")
	ErrReleasedLease = errors.New("source work lease has been released")
)

// Gate's zero value admits work. A Gate must not be copied after first use.
// The lifecycle coordinator owns pause/resume; ordinary callers only acquire.
type Gate struct {
	mu      sync.Mutex
	active  int
	paused  *Pause
	closed  bool
	changed chan struct{}
}

// Lease accounts for one admitted operation or its continuing cleanup. It
// must not be copied. Cancellation never releases a lease automatically.
type Lease struct {
	gate     *Gate
	released bool // protected by gate.mu
}

// Pause owns one admission pause. Only this handle can resume it.
type Pause struct{ gate *Gate }

// Acquire waits while paused. The caller releases its lease only after actual
// work and synchronous cleanup finish, even if ctx is canceled sooner.
func (g *Gate) Acquire(ctx context.Context) (*Lease, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for {
		if g.closed {
			return nil, ErrClosed
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if g.paused == nil {
			g.active++
			return &Lease{gate: g}, nil
		}
		if err := g.wait(ctx); err != nil {
			return nil, err
		}
	}
}

// Retain accounts for cleanup that outlives the original call. It continues
// already admitted work even during pause/close, and must happen before the
// parent is released. It must never be used to start an unrelated operation.
func (l *Lease) Retain() (*Lease, error) {
	if l == nil || l.gate == nil {
		return nil, ErrReleasedLease
	}
	g := l.gate
	g.mu.Lock()
	defer g.mu.Unlock()
	if l.released {
		return nil, ErrReleasedLease
	}
	g.active++
	return &Lease{gate: g}, nil
}

func (l *Lease) Release() {
	if l == nil || l.gate == nil {
		return
	}
	g := l.gate
	g.mu.Lock()
	defer g.mu.Unlock()
	if l.released {
		return
	}
	l.released = true
	g.active--
	if g.active == 0 {
		g.signal()
	}
}

// Pause stops new admission without canceling already admitted work.
func (g *Gate) Pause() (*Pause, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil, ErrClosed
	}
	if g.paused != nil {
		return nil, ErrPaused
	}
	g.paused = &Pause{gate: g}
	return g.paused, nil
}

// WaitDrained can be retried after a timeout. A timeout leaves admission
// paused and never treats cancellation as proof that work has completed.
func (p *Pause) WaitDrained(ctx context.Context) error {
	if p == nil || p.gate == nil {
		return ErrInvalidPause
	}
	g := p.gate
	g.mu.Lock()
	defer g.mu.Unlock()
	for {
		if err := p.check(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if g.active == 0 {
			return nil
		}
		if err := g.wait(ctx); err != nil {
			return err
		}
	}
}

// Resume reopens this pause only after all admitted work has finished. The
// coordinator must separately establish committed runtime readiness first.
func (p *Pause) Resume() error {
	if p == nil || p.gate == nil {
		return ErrInvalidPause
	}
	g := p.gate
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := p.check(); err != nil {
		return err
	}
	if g.active != 0 {
		return ErrWorkActive
	}
	g.paused = nil
	g.signal()
	return nil
}

// Close permanently fences admission and wakes waiters. It does not cancel
// admitted operations or wait for resource shutdown, which owners perform.
func (g *Gate) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	g.signal()
}

// check, wait, and signal require gate.mu. wait reacquires it before returning.
func (p *Pause) check() error {
	if p.gate.closed {
		return ErrClosed
	}
	if p.gate.paused != p {
		return ErrInvalidPause
	}
	return nil
}

func (g *Gate) wait(ctx context.Context) error {
	if g.changed == nil {
		g.changed = make(chan struct{})
	}
	changed := g.changed
	g.mu.Unlock()
	select {
	case <-changed:
	case <-ctx.Done():
	}
	g.mu.Lock()
	return ctx.Err()
}

func (g *Gate) signal() {
	if g.changed != nil {
		close(g.changed)
		g.changed = nil
	}
}
