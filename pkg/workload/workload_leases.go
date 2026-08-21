package workload

import (
	"context"
	"errors"
	"time"
)

// Lease release owns the accounting decrement and shutdown drain signal. The
// lease's once gate makes release exactly-once even when cancellation races.
func (l *lease) Context() context.Context {
	if l == nil {
		return nil
	}
	return l.ctx
}
func (l *lease) QueueWait() time.Duration {
	if l == nil {
		return 0
	}
	return l.queueWait
}
func (l *lease) Release() {
	if l == nil || l.controller == nil {
		return
	}
	l.once.Do(func() {
		l.releaseRef()
	})
}

func (l *lease) releaseRef() {
	c := l.controller
	c.mu.Lock()
	if l.refs > 0 {
		l.refs--
	}
	if l.refs > 0 {
		c.mu.Unlock()
		return
	}
	contextErr := l.ctx.Err()
	l.cancel()
	if l.timer != nil {
		l.timer.Stop()
	}
	if _, ok := c.active[l]; !ok {
		c.mu.Unlock()
		return
	}
	delete(c.active, l)
	if c.running > 0 {
		c.running--
	}
	c.runningMemory -= l.request.EstimatedMemoryBytes
	c.runningClass[l.request.Class]--
	c.classMemory[l.request.Class] -= l.request.EstimatedMemoryBytes
	principal := c.runningPrincipal[l.request.PrincipalID]
	principal.running--
	principal.memoryBytes -= l.request.EstimatedMemoryBytes
	if principal.running <= 0 {
		delete(c.runningPrincipal, l.request.PrincipalID)
	} else {
		c.runningPrincipal[l.request.PrincipalID] = principal
	}
	for _, groupID := range l.request.GroupIDs {
		group := c.runningGroup[groupID]
		group.running--
		group.memoryBytes -= l.request.EstimatedMemoryBytes
		if group.running <= 0 {
			delete(c.runningGroup, groupID)
		} else {
			c.runningGroup[groupID] = group
		}
	}
	c.scheduleLocked()
	c.signalDrainedLocked()
	stats := c.statsLocked()
	c.mu.Unlock()
	c.observeStats(stats)
	outcome := OutcomeReleased
	if errors.Is(contextErr, context.DeadlineExceeded) {
		outcome = OutcomeTimedOut
	} else if contextErr != nil {
		outcome = OutcomeCanceled
	}
	event := admissionEvent(l.request, outcome, "")
	event.QueueWait = l.queueWait
	event.Execution = c.clock.Now().Sub(l.started)
	c.observeAdmission(event)
}

func (l *nestedLease) Context() context.Context {
	if l == nil {
		return nil
	}
	return l.ctx
}
func (*nestedLease) QueueWait() time.Duration { return 0 }
func (l *nestedLease) Release() {
	if l == nil || l.parent == nil {
		return
	}
	l.once.Do(l.parent.releaseRef)
}
