package workload

import "context"

// Fairness is enforced in two passes: reserved class capacity first, then
// borrowed capacity. Actor queues remain FIFO while the class cursor advances
// round-robin, so a blocked actor cannot starve later principals.
func sameAdmission(a, b Request) bool {
	if a.Class != b.Class || a.PrincipalID != b.PrincipalID || len(a.GroupIDs) != len(b.GroupIDs) {
		return false
	}
	for i := range a.GroupIDs {
		if a.GroupIDs[i] != b.GroupIDs[i] {
			return false
		}
	}
	return true
}

func (c *Controller) impossibleMemoryReasonLocked(request Request) RejectionReason {
	// Report the narrowest applicable bound first. Group and principal limits
	// are more specific than class and instance limits for one request.
	if c.config.MaximumMemoryBytesPerGroup > 0 && request.EstimatedMemoryBytes > c.config.MaximumMemoryBytesPerGroup && len(request.GroupIDs) > 0 {
		return GroupMemoryLimit
	}
	if c.config.MaximumMemoryBytesPerPrincipal > 0 && request.EstimatedMemoryBytes > c.config.MaximumMemoryBytesPerPrincipal {
		return PrincipalMemoryLimit
	}
	policy := c.config.Policies[request.Class]
	if policy.MaximumMemoryBytes > 0 && request.EstimatedMemoryBytes > policy.MaximumMemoryBytes {
		return ClassMemoryLimit
	}
	if c.config.MaximumMemoryBytes > 0 && request.EstimatedMemoryBytes > c.config.MaximumMemoryBytes {
		return InstanceMemoryLimit
	}
	return ""
}

func (c *Controller) queueLimitReasonLocked(request Request) RejectionReason {
	queued, overflow := c.queuedTotalLocked()
	if overflow || queued > c.config.MaximumQueued {
		return InstanceQueueFull
	}
	if queue := c.queues[request.Class]; queue.queued > c.config.Policies[request.Class].MaximumQueued {
		return ClassQueueFull
	}
	if limit := c.config.MaximumQueuedPerPrincipal; limit > 0 && c.queuedPrincipal[request.PrincipalID] > limit {
		return PrincipalQueueFull
	}
	for _, group := range request.GroupIDs {
		if limit := c.config.MaximumQueuedPerGroup; limit > 0 && c.queuedGroup[group] > limit {
			return GroupQueueFull
		}
	}
	return ""
}

func (c *Controller) queueAccountingOverflowReasonLocked(request Request) RejectionReason {
	queued, overflow := c.queuedTotalLocked()
	if overflow || queued == maxIntValue {
		return InstanceQueueFull
	}
	if c.queues[request.Class].queued == maxIntValue {
		return ClassQueueFull
	}
	if c.queuedPrincipal[request.PrincipalID] == maxIntValue {
		return PrincipalQueueFull
	}
	for _, group := range request.GroupIDs {
		if c.queuedGroup[group] == maxIntValue {
			return GroupQueueFull
		}
	}
	return ""
}

func (c *Controller) queuedTotalLocked() (int, bool) {
	total := 0
	for _, class := range c.config.Classes {
		queued := c.queues[class].queued
		if queued < 0 || queued > maxIntValue-total {
			return 0, true
		}
		total += queued
	}
	return total, false
}

const maxIntValue = int(^uint(0) >> 1)

func (c *Controller) canGrantLocked(w *waiter) bool {
	if w == nil || c.running >= c.config.MaximumRunning {
		return false
	}
	request := w.request
	policy := c.config.Policies[request.Class]
	if policy.MaximumRunning == 0 || c.runningClass[request.Class] >= policy.MaximumRunning {
		return false
	}
	if !memoryWithin(c.runningMemory, request.EstimatedMemoryBytes, c.config.MaximumMemoryBytes) ||
		!memoryWithin(c.classMemory[request.Class], request.EstimatedMemoryBytes, policy.MaximumMemoryBytes) {
		return false
	}
	principal := c.runningPrincipal[request.PrincipalID]
	if limit := c.config.MaximumRunningPerPrincipal; limit > 0 && principal.running >= limit {
		return false
	}
	if !memoryWithin(principal.memoryBytes, request.EstimatedMemoryBytes, c.config.MaximumMemoryBytesPerPrincipal) {
		return false
	}
	for _, groupID := range request.GroupIDs {
		group := c.runningGroup[groupID]
		if limit := c.config.MaximumRunningPerGroup; limit > 0 && group.running >= limit {
			return false
		}
		if !memoryWithin(group.memoryBytes, request.EstimatedMemoryBytes, c.config.MaximumMemoryBytesPerGroup) {
			return false
		}
	}
	return true
}

func memoryWithin(current, requested, limit int64) bool {
	if current < 0 || requested <= 0 || current > (int64(^uint64(0)>>1)-requested) {
		return false
	}
	return limit <= 0 || current+requested <= limit
}

func (c *Controller) scheduleLocked() {
	for !c.closed && c.running < c.config.MaximumRunning {
		class, ok := c.nextClassLocked(true)
		if !ok {
			class, ok = c.nextClassLocked(false)
		}
		if !ok {
			return
		}
		queue := c.queues[class]
		w := queue.popEligible(c.canGrantLocked)
		if w == nil {
			return
		}
		if c.queuedPrincipal[w.request.PrincipalID]--; c.queuedPrincipal[w.request.PrincipalID] == 0 {
			delete(c.queuedPrincipal, w.request.PrincipalID)
		}
		for _, group := range w.request.GroupIDs {
			if c.queuedGroup[group]--; c.queuedGroup[group] == 0 {
				delete(c.queuedGroup, group)
			}
		}
		w.state = granted
		c.running++
		c.runningMemory += w.request.EstimatedMemoryBytes
		c.runningClass[class]++
		c.classMemory[class] += w.request.EstimatedMemoryBytes
		principal := c.runningPrincipal[w.request.PrincipalID]
		principal.running++
		principal.memoryBytes += w.request.EstimatedMemoryBytes
		c.runningPrincipal[w.request.PrincipalID] = principal
		for _, groupID := range w.request.GroupIDs {
			group := c.runningGroup[groupID]
			group.running++
			group.memoryBytes += w.request.EstimatedMemoryBytes
			c.runningGroup[groupID] = group
		}
		policy := c.config.Policies[class]
		wait := c.clock.Now().Sub(w.enqueued)
		var execCtx context.Context
		var cancel context.CancelFunc
		var execTimer Timer
		if policy.ExecutionTimeout > 0 {
			execTimer = c.clock.NewTimer(policy.ExecutionTimeout)
			if execTimer != nil {
				execCtx, cancel = newClockContext(w.parent, execTimer, c.clock.Now().Add(policy.ExecutionTimeout))
			} else {
				execCtx, cancel = context.WithTimeout(w.parent, policy.ExecutionTimeout)
			}
		} else {
			execCtx, cancel = context.WithCancel(w.parent)
		}
		request := w.request.Clone()
		grantedLease := &lease{controller: c, request: request, ctx: execCtx, cancel: cancel, timer: execTimer, queueWait: wait, started: c.clock.Now(), refs: 1}
		execCtx = context.WithValue(execCtx, admissionContextKey{}, &activeAdmission{controller: c, request: request, lease: grantedLease})
		grantedLease.ctx = execCtx
		c.active[grantedLease] = struct{}{}
		w.result <- acquireResult{lease: grantedLease}
	}
}

func (c *Controller) nextClassLocked(reservedOnly bool) (Class, bool) {
	if len(c.config.Classes) == 0 {
		return "", false
	}
	if c.classCursor >= len(c.config.Classes) {
		c.classCursor = 0
	}
	for offset := 0; offset < len(c.config.Classes); offset++ {
		index := (c.classCursor + offset) % len(c.config.Classes)
		class := c.config.Classes[index]
		policy := c.config.Policies[class]
		queue := c.queues[class]
		if queue.queued == 0 || c.runningClass[class] >= policy.MaximumRunning {
			continue
		}
		if reservedOnly && c.runningClass[class] >= policy.ReservedRunning {
			continue
		}
		if queue.peekEligible(c.canGrantLocked) == nil {
			continue
		}
		c.classCursor = (index + 1) % len(c.config.Classes)
		return class, true
	}
	return "", false
}

func (c *Controller) removeWaiterLocked(w *waiter) {
	queue := c.queues[w.request.Class]
	if queue == nil || !queue.remove(w) {
		return
	}
	if c.queuedPrincipal[w.request.PrincipalID]--; c.queuedPrincipal[w.request.PrincipalID] == 0 {
		delete(c.queuedPrincipal, w.request.PrincipalID)
	}
	for _, group := range w.request.GroupIDs {
		if c.queuedGroup[group]--; c.queuedGroup[group] == 0 {
			delete(c.queuedGroup, group)
		}
	}
}
