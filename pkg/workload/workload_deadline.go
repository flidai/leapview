package workload

import (
	"context"
	"sync"
	"time"
)

// clockContext is a small context implementation used for execution
// deadlines so injected clocks control both queue and execution timers.
// Cancellation is idempotent and parent cancellation is propagated by the
// single watcher goroutine, which exits as soon as the context is done.
type clockContext struct {
	parent   context.Context
	done     chan struct{}
	deadline time.Time
	once     sync.Once
	errMu    sync.RWMutex
	err      error
}

func newClockContext(parent context.Context, timer Timer, deadline time.Time) (*clockContext, context.CancelFunc) {
	if parentDeadline, ok := parent.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	c := &clockContext{parent: parent, done: make(chan struct{}), deadline: deadline}
	go func() {
		var timerC <-chan time.Time
		if timer != nil {
			timerC = timer.C()
		}
		select {
		case <-parent.Done():
			c.finish(parent.Err())
		case <-timerC:
			c.finish(context.DeadlineExceeded)
		case <-c.done:
		}
	}()
	return c, func() { c.finish(context.Canceled) }
}

func (c *clockContext) finish(err error) {
	c.once.Do(func() {
		c.errMu.Lock()
		c.err = err
		c.errMu.Unlock()
		close(c.done)
	})
}
func (c *clockContext) Deadline() (time.Time, bool) { return c.deadline, true }
func (c *clockContext) Done() <-chan struct{}       { return c.done }
func (c *clockContext) Err() error {
	c.errMu.RLock()
	err := c.err
	c.errMu.RUnlock()
	return err
}
func (c *clockContext) Value(key any) any { return c.parent.Value(key) }
