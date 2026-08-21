package workload

import (
	"context"
	"errors"
	"time"
)

// Acquire is the controller's admission boundary. Queue insertion, deadline
// setup, and cancellation races stay in this unit so every waiter reaches one
// terminal result before its caller observes a return.
// Acquire validates and defensively copies request data, then either grants a
// lease, queues the request within the configured limits, or returns a typed
// rejection. Admission never implies application authorization.
func (c *Controller) Acquire(ctx context.Context, request Request) (Lease, error) {
	request = request.Clone()
	if c == nil {
		return nil, &Rejection{Reason: ControllerShutdown, Class: request.Class, PrincipalID: request.PrincipalID, Operation: request.Operation, GroupIDs: append([]string(nil), request.GroupIDs...)}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	canonical, err := request.Canonicalize()
	if err != nil {
		c.observeAdmission(rejectionEvent(request, err))
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		event := admissionEvent(canonical, OutcomeCanceled, "")
		c.observeAdmission(event)
		return nil, err
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		err := c.rejection(canonical, ControllerShutdown, nil)
		c.observeAdmission(admissionEvent(canonical, OutcomeRejected, ControllerShutdown))
		return nil, err
	}
	if _, configured := c.queues[canonical.Class]; !configured {
		c.mu.Unlock()
		err := c.rejection(canonical, InvalidClass, nil)
		c.observeAdmission(admissionEvent(canonical, OutcomeRejected, InvalidClass))
		return nil, err
	}
	if active, ok := ctx.Value(admissionContextKey{}).(*activeAdmission); ok && active != nil {
		if active.controller == c && sameAdmission(active.request, canonical) {
			if active.lease == nil || active.lease.refs <= 0 {
				// Contexts constructed by callers cannot forge a reference to a
				// live lease; treat those as a conflict rather than granting
				// unaccounted nested work.
				c.mu.Unlock()
				err := c.rejection(canonical, ConflictingNestedAdmission, nil)
				c.observeAdmission(admissionEvent(canonical, OutcomeRejected, ConflictingNestedAdmission))
				return nil, err
			}
			if _, live := c.active[active.lease]; !live {
				c.mu.Unlock()
				err := c.rejection(canonical, ConflictingNestedAdmission, nil)
				c.observeAdmission(admissionEvent(canonical, OutcomeRejected, ConflictingNestedAdmission))
				return nil, err
			}
			active.lease.refs++
			c.mu.Unlock()
			return &nestedLease{ctx: ctx, parent: active.lease}, nil
		}
		c.mu.Unlock()
		err := c.rejection(canonical, ConflictingNestedAdmission, nil)
		c.observeAdmission(admissionEvent(canonical, OutcomeRejected, ConflictingNestedAdmission))
		return nil, err
	}
	if reason := c.impossibleMemoryReasonLocked(canonical); reason != "" {
		c.mu.Unlock()
		err := c.rejection(canonical, reason, nil)
		c.observeAdmission(admissionEvent(canonical, OutcomeRejected, reason))
		return nil, err
	}
	if reason := c.queueAccountingOverflowReasonLocked(canonical); reason != "" {
		c.mu.Unlock()
		err := c.rejection(canonical, reason, errors.New("queue accounting capacity exhausted"))
		c.observeAdmission(admissionEvent(canonical, OutcomeRejected, reason))
		return nil, err
	}

	w := &waiter{request: canonical, parent: ctx, enqueued: c.clock.Now(), result: make(chan acquireResult, 1), state: waiting}
	queue := c.queues[canonical.Class]
	queue.enqueue(w)
	c.queuedPrincipal[canonical.PrincipalID]++
	for _, group := range canonical.GroupIDs {
		c.queuedGroup[group]++
	}
	c.scheduleLocked()
	if w.state == waiting {
		if reason := c.queueLimitReasonLocked(canonical); reason != "" {
			c.removeWaiterLocked(w)
			w.state = rejected
			stats := c.statsLocked()
			wait := c.clock.Now().Sub(w.enqueued)
			c.mu.Unlock()
			c.observeStats(stats)
			err := c.rejection(canonical, reason, nil)
			err.(*Rejection).QueueWait = wait
			event := admissionEvent(canonical, OutcomeRejected, reason)
			event.QueueWait = wait
			c.observeAdmission(event)
			return nil, err
		}
	}
	stats := c.statsLocked()
	immediate := w.state == granted
	c.mu.Unlock()
	c.observeStats(stats)

	if immediate {
		result := <-w.result
		if result.err != nil {
			c.observeAdmission(rejectionEvent(canonical, result.err))
			return nil, result.err
		}
		if err := ctx.Err(); err != nil {
			result.lease.Release()
			return nil, err
		}
		event := admissionEvent(canonical, OutcomeAdmitted, "")
		event.QueueWait = result.lease.QueueWait()
		c.observeAdmission(event)
		return result.lease, nil
	}

	policy := c.config.Policies[canonical.Class]
	var timer Timer
	var timeout <-chan time.Time
	if policy.QueueTimeout > 0 {
		timer = c.clock.NewTimer(policy.QueueTimeout)
		if timer == nil {
			acquired, terminalErr, _ := c.cancelWaiter(w)
			if acquired != nil {
				acquired.Release()
			}
			if terminalErr != nil {
				c.observeAdmission(rejectionEvent(canonical, terminalErr))
				return nil, terminalErr
			}
			err := c.rejection(canonical, AdmissionUnavailable, errors.New("clock returned a nil queue timer"))
			c.observeAdmission(admissionEvent(canonical, OutcomeRejected, AdmissionUnavailable))
			return nil, err
		}
		timeout = timer.C()
	}
	if timer != nil {
		defer timer.Stop()
	}
	select {
	case result := <-w.result:
		if result.err != nil {
			c.observeAdmission(rejectionEvent(canonical, result.err))
			return nil, result.err
		}
		if err := ctx.Err(); err != nil {
			result.lease.Release()
			return nil, err
		}
		event := admissionEvent(canonical, OutcomeAdmitted, "")
		event.QueueWait = result.lease.QueueWait()
		c.observeAdmission(event)
		return result.lease, nil
	case <-ctx.Done():
		acquired, terminalErr, removed := c.cancelWaiter(w)
		if acquired != nil {
			acquired.Release()
			return nil, ctx.Err()
		}
		if terminalErr != nil {
			c.observeAdmission(rejectionEvent(canonical, terminalErr))
			return nil, terminalErr
		}
		if !removed {
			return nil, ctx.Err()
		}
		event := admissionEvent(canonical, OutcomeCanceled, "")
		event.QueueWait = c.clock.Now().Sub(w.enqueued)
		c.observeAdmission(event)
		return nil, ctx.Err()
	case <-timeout:
		acquired, terminalErr, removed := c.cancelWaiter(w)
		if acquired != nil {
			if err := ctx.Err(); err != nil {
				acquired.Release()
				return nil, err
			}
			event := admissionEvent(canonical, OutcomeAdmitted, "")
			event.QueueWait = acquired.QueueWait()
			c.observeAdmission(event)
			return acquired, nil
		}
		if terminalErr != nil {
			c.observeAdmission(rejectionEvent(canonical, terminalErr))
			return nil, terminalErr
		}
		if !removed {
			return nil, context.DeadlineExceeded
		}
		select {
		case result := <-w.result:
			if result.err != nil {
				c.observeAdmission(rejectionEvent(canonical, result.err))
				return nil, result.err
			}
			if result.lease != nil {
				if err := ctx.Err(); err != nil {
					result.lease.Release()
					return nil, err
				}
				result.lease.Release()
			}
		default:
		}
		if err := ctx.Err(); err != nil {
			event := admissionEvent(canonical, OutcomeCanceled, "")
			event.QueueWait = c.clock.Now().Sub(w.enqueued)
			c.observeAdmission(event)
			return nil, err
		}
		wait := c.clock.Now().Sub(w.enqueued)
		err := c.rejection(canonical, QueueTimeout, context.DeadlineExceeded)
		err.(*Rejection).QueueWait = wait
		event := admissionEvent(canonical, OutcomeRejected, QueueTimeout)
		event.QueueWait = wait
		c.observeAdmission(event)
		return nil, err
	}
}
