package workload

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
)

type usage struct {
	running     int
	memoryBytes int64
}

type Controller struct {
	mu       sync.Mutex
	config   Config
	clock    Clock
	observer Observer
	closed   bool

	running          int
	runningMemory    int64
	runningClass     map[Class]int
	classMemory      map[Class]int64
	runningPrincipal map[string]usage
	runningGroup     map[string]usage
	queuedPrincipal  map[string]int
	queuedGroup      map[string]int
	active           map[*lease]struct{}
	queues           map[Class]*classQueue
	classCursor      int
}

type waiter struct {
	request  Request
	parent   context.Context
	enqueued time.Time
	result   chan acquireResult
	state    waiterState
}

type waiterState uint8

const (
	waiting waiterState = iota
	granted
	rejected
)

type acquireResult struct {
	lease Lease
	err   error
}

type classQueue struct {
	actors map[string][]*waiter
	order  []string
	cursor int
	queued int
}

type admissionContext struct {
	controller *Controller
	class      Class
	principal  string
	groupsKey  string
}

type admissionContextKey struct{}

type lease struct {
	controller *Controller
	request    Request
	ctx        context.Context
	cancel     context.CancelFunc
	queueWait  time.Duration
	started    time.Time
	once       sync.Once
}

type nestedLease struct{ ctx context.Context }

func New(config Config, options ...Option) (*Controller, error) {
	if config.MaxRunning == 0 && config.MaximumQueued == 0 && config.MaximumMemoryBytes == 0 && len(config.Classes) == 0 {
		config = DefaultConfig()
	}
	config.Classes = clonePolicies(config.Classes)
	if err := config.Validate(); err != nil {
		return nil, err
	}
	c := &Controller{
		config:           config,
		clock:            realClock{},
		runningClass:     make(map[Class]int, len(classOrder)),
		classMemory:      make(map[Class]int64, len(classOrder)),
		runningPrincipal: make(map[string]usage),
		runningGroup:     make(map[string]usage),
		queuedPrincipal:  make(map[string]int),
		queuedGroup:      make(map[string]int),
		active:           make(map[*lease]struct{}),
		queues:           make(map[Class]*classQueue, len(classOrder)),
	}
	for _, class := range classOrder {
		c.queues[class] = &classQueue{actors: make(map[string][]*waiter)}
	}
	for _, option := range options {
		if option != nil {
			option(c)
		}
	}
	if c.clock == nil {
		return nil, fmt.Errorf("workload clock is required")
	}
	return c, nil
}

func (c *Controller) Acquire(ctx context.Context, request Request) (Lease, error) {
	if c == nil {
		return nil, &Rejection{Reason: ControllerShutdown, Class: request.Class, PrincipalID: request.PrincipalID, GroupIDs: append([]string(nil), request.GroupIDs...), Operation: request.Operation}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request, err := c.normalize(request)
	if err != nil {
		reason := InvalidRequest
		if observed, ok := ReasonOf(err); ok {
			reason = observed
		}
		event := admissionEvent(request, "rejected", reason)
		c.observeAdmission(event)
		return nil, err
	}
	groupsKey := strings.Join(request.GroupIDs, "\x00")
	if active, _ := ctx.Value(admissionContextKey{}).(*admissionContext); active != nil {
		if active.controller == c && active.class == request.Class && active.principal == request.PrincipalID && active.groupsKey == groupsKey {
			return &nestedLease{ctx: ctx}, nil
		}
		err := c.rejection(request, ConflictingNestedAdmission, nil)
		c.observeAdmission(admissionEvent(request, "rejected", ConflictingNestedAdmission))
		return nil, err
	}

	w := &waiter{request: request, parent: ctx, enqueued: c.clock.Now(), result: make(chan acquireResult, 1), state: waiting}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		err := c.rejection(request, ControllerShutdown, nil)
		c.observeAdmission(admissionEvent(request, "rejected", ControllerShutdown))
		return nil, err
	}
	queue := c.queues[request.Class]
	queue.enqueue(w)
	c.queuedPrincipal[request.PrincipalID]++
	for _, group := range request.GroupIDs {
		c.queuedGroup[group]++
	}
	c.scheduleLocked()
	if w.state == waiting {
		reason := c.queueLimitReasonLocked(request)
		if reason != "" {
			c.removeWaiterLocked(w)
			w.state = rejected
			stats := c.statsLocked()
			c.mu.Unlock()
			err := c.rejection(request, reason, nil)
			err.(*Rejection).QueueWait = c.clock.Now().Sub(w.enqueued)
			c.observeStats(stats)
			event := admissionEvent(request, "rejected", reason)
			event.QueueWait = err.(*Rejection).QueueWait
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
			return nil, result.err
		}
		event := admissionEvent(request, "admitted", "")
		event.QueueWait = result.lease.QueueWait()
		c.observeAdmission(event)
		return result.lease, nil
	}

	policy := c.config.Classes[request.Class]
	var timer Timer
	var timeout <-chan time.Time
	if policy.QueueTimeout > 0 {
		timer = c.clock.NewTimer(policy.QueueTimeout)
		timeout = timer.C()
		defer timer.Stop()
	}
	select {
	case result := <-w.result:
		if result.err != nil {
			return nil, result.err
		}
		if err := ctx.Err(); err != nil {
			result.lease.Release()
			return nil, err
		}
		event := admissionEvent(request, "admitted", "")
		event.QueueWait = result.lease.QueueWait()
		c.observeAdmission(event)
		return result.lease, nil
	case <-ctx.Done():
		if acquired := c.cancelWaiter(w); acquired != nil {
			acquired.Release()
		} else {
			event := admissionEvent(request, "canceled", "")
			event.QueueWait = c.clock.Now().Sub(w.enqueued)
			c.observeAdmission(event)
		}
		return nil, ctx.Err()
	case <-timeout:
		if acquired := c.cancelWaiter(w); acquired != nil {
			if err := ctx.Err(); err == nil {
				event := admissionEvent(request, "admitted", "")
				event.QueueWait = acquired.QueueWait()
				c.observeAdmission(event)
				return acquired, nil
			}
			acquired.Release()
			return nil, ctx.Err()
		}
		err := c.rejection(request, QueueTimeout, context.DeadlineExceeded)
		err.(*Rejection).QueueWait = c.clock.Now().Sub(w.enqueued)
		event := admissionEvent(request, "rejected", QueueTimeout)
		event.QueueWait = err.(*Rejection).QueueWait
		c.observeAdmission(event)
		return nil, err
	}
}

func (c *Controller) cancelWaiter(w *waiter) Lease {
	c.mu.Lock()
	if w.state == waiting {
		c.removeWaiterLocked(w)
		w.state = rejected
		c.scheduleLocked()
		stats := c.statsLocked()
		c.mu.Unlock()
		c.observeStats(stats)
		return nil
	}
	c.mu.Unlock()
	select {
	case result := <-w.result:
		return result.lease
	default:
		return nil
	}
}

func (c *Controller) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	var rejectedWaiters []*waiter
	for _, class := range classOrder {
		queue := c.queues[class]
		for _, actor := range queue.order {
			for _, w := range queue.actors[actor] {
				w.state = rejected
				rejectedWaiters = append(rejectedWaiters, w)
			}
		}
		queue.actors = make(map[string][]*waiter)
		queue.order = nil
		queue.cursor = 0
		queue.queued = 0
	}
	c.queuedPrincipal = make(map[string]int)
	c.queuedGroup = make(map[string]int)
	stats := c.statsLocked()
	active := make([]*lease, 0, len(c.active))
	for running := range c.active {
		active = append(active, running)
	}
	c.mu.Unlock()
	for _, running := range active {
		running.cancel()
	}
	for _, w := range rejectedWaiters {
		err := c.rejection(w.request, ControllerShutdown, nil)
		err.(*Rejection).QueueWait = c.clock.Now().Sub(w.enqueued)
		w.result <- acquireResult{err: err}
		event := admissionEvent(w.request, "rejected", ControllerShutdown)
		event.QueueWait = err.(*Rejection).QueueWait
		c.observeAdmission(event)
	}
	c.observeStats(stats)
}

func (c *Controller) Stats() Stats {
	if c == nil {
		return Stats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.statsLocked()
}

func (c *Controller) SetObserver(observer Observer) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.observer = observer
	stats := c.statsLocked()
	c.mu.Unlock()
	c.observeStats(stats)
}

func (c *Controller) scheduleLocked() {
	for !c.closed && c.running < c.config.MaxRunning {
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
		c.queuedPrincipal[w.request.PrincipalID]--
		if c.queuedPrincipal[w.request.PrincipalID] == 0 {
			delete(c.queuedPrincipal, w.request.PrincipalID)
		}
		for _, group := range w.request.GroupIDs {
			c.queuedGroup[group]--
			if c.queuedGroup[group] == 0 {
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
		policy := c.config.Classes[class]
		wait := c.clock.Now().Sub(w.enqueued)
		var execCtx context.Context
		var cancel context.CancelFunc
		if policy.ExecutionTimeout > 0 {
			execCtx, cancel = context.WithTimeout(w.parent, policy.ExecutionTimeout)
		} else {
			execCtx, cancel = context.WithCancel(w.parent)
		}
		groupsKey := strings.Join(w.request.GroupIDs, "\x00")
		execCtx = context.WithValue(execCtx, admissionContextKey{}, &admissionContext{controller: c, class: class, principal: w.request.PrincipalID, groupsKey: groupsKey})
		grantedLease := &lease{controller: c, request: w.request, ctx: execCtx, cancel: cancel, queueWait: wait, started: c.clock.Now()}
		c.active[grantedLease] = struct{}{}
		w.result <- acquireResult{lease: grantedLease}
	}
}

func (c *Controller) nextClassLocked(reservedOnly bool) (Class, bool) {
	for offset := 0; offset < len(classOrder); offset++ {
		index := (c.classCursor + offset) % len(classOrder)
		class := classOrder[index]
		policy := c.config.Classes[class]
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
		c.classCursor = (index + 1) % len(classOrder)
		return class, true
	}
	return "", false
}

func (c *Controller) canGrantLocked(w *waiter) bool {
	if w == nil || c.running >= c.config.MaxRunning {
		return false
	}
	request := w.request
	policy := c.config.Classes[request.Class]
	if c.runningClass[request.Class] >= policy.MaximumRunning {
		return false
	}
	if !memoryWithin(c.runningMemory, request.EstimatedMemoryBytes, c.config.MaximumMemoryBytes) {
		return false
	}
	if !memoryWithin(c.classMemory[request.Class], request.EstimatedMemoryBytes, policy.MaximumMemoryBytes) {
		return false
	}
	if limit := c.config.MaximumRunningPerPrincipal; limit > 0 && c.runningPrincipal[request.PrincipalID].running >= limit {
		return false
	}
	if !memoryWithin(c.runningPrincipal[request.PrincipalID].memoryBytes, request.EstimatedMemoryBytes, c.config.MaximumMemoryBytesPerPrincipal) {
		return false
	}
	for _, groupID := range request.GroupIDs {
		if limit := c.config.MaximumRunningPerGroup; limit > 0 && c.runningGroup[groupID].running >= limit {
			return false
		}
		if !memoryWithin(c.runningGroup[groupID].memoryBytes, request.EstimatedMemoryBytes, c.config.MaximumMemoryBytesPerGroup) {
			return false
		}
	}
	return true
}

func memoryWithin(current, requested, limit int64) bool {
	if current < 0 || requested <= 0 || current > (int64(1<<63-1)-requested) {
		return false
	}
	return limit <= 0 || current+requested <= limit
}

func (c *Controller) queueLimitReasonLocked(request Request) RejectionReason {
	if reason := c.impossibleMemoryReason(request); reason != "" {
		return reason
	}
	total := 0
	for _, class := range classOrder {
		total += c.queues[class].queued
	}
	if total > c.config.MaximumQueued {
		return InstanceQueueFull
	}
	queue := c.queues[request.Class]
	if queue.queued > c.config.Classes[request.Class].MaximumQueued {
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

func (c *Controller) impossibleMemoryReason(request Request) RejectionReason {
	if request.EstimatedMemoryBytes <= 0 {
		return InstanceMemoryLimit
	}
	if c.config.MaximumMemoryBytes > 0 && request.EstimatedMemoryBytes > c.config.MaximumMemoryBytes {
		return InstanceMemoryLimit
	}
	policy := c.config.Classes[request.Class]
	if policy.MaximumMemoryBytes > 0 && request.EstimatedMemoryBytes > policy.MaximumMemoryBytes {
		return ClassMemoryLimit
	}
	if c.config.MaximumMemoryBytesPerPrincipal > 0 && request.EstimatedMemoryBytes > c.config.MaximumMemoryBytesPerPrincipal {
		return PrincipalMemoryLimit
	}
	for range request.GroupIDs {
		if c.config.MaximumMemoryBytesPerGroup > 0 && request.EstimatedMemoryBytes > c.config.MaximumMemoryBytesPerGroup {
			return GroupMemoryLimit
		}
	}
	return ""
}

func (c *Controller) normalize(request Request) (Request, error) {
	known := false
	for _, class := range classOrder {
		if request.Class == class {
			known = true
			break
		}
	}
	if request.PrincipalID == "" || request.PrincipalID != strings.TrimSpace(request.PrincipalID) || strings.IndexFunc(request.PrincipalID, unicode.IsControl) >= 0 ||
		request.Operation == "" || request.Operation != strings.TrimSpace(request.Operation) || strings.IndexFunc(request.Operation, unicode.IsControl) >= 0 {
		return request, c.rejection(request, InvalidRequest, nil)
	}
	groups, groupErr := normalizeGroups(request.GroupIDs)
	if groupErr != nil {
		return request, c.rejection(request, InvalidRequest, groupErr)
	}
	request.GroupIDs = groups
	if !known || len(request.Operation) > 96 || request.EstimatedMemoryBytes <= 0 {
		return request, c.rejection(request, InvalidRequest, nil)
	}
	if reason := c.impossibleMemoryReason(request); reason != "" {
		return request, c.rejection(request, reason, nil)
	}
	return request, nil
}

func (c *Controller) rejection(request Request, reason RejectionReason, cause error) error {
	return &Rejection{Reason: reason, Class: request.Class, PrincipalID: request.PrincipalID, GroupIDs: append([]string(nil), request.GroupIDs...), Operation: request.Operation, cause: cause}
}

func admissionEvent(request Request, outcome string, reason RejectionReason) AdmissionEvent {
	return AdmissionEvent{
		Class:                request.Class,
		PrincipalID:          request.PrincipalID,
		GroupIDs:             append([]string(nil), request.GroupIDs...),
		Operation:            request.Operation,
		EstimatedMemoryBytes: request.EstimatedMemoryBytes,
		Outcome:              outcome,
		Reason:               reason,
	}
}

func (c *Controller) removeWaiterLocked(w *waiter) {
	if !c.queues[w.request.Class].remove(w) {
		return
	}
	c.queuedPrincipal[w.request.PrincipalID]--
	if c.queuedPrincipal[w.request.PrincipalID] == 0 {
		delete(c.queuedPrincipal, w.request.PrincipalID)
	}
	for _, group := range w.request.GroupIDs {
		c.queuedGroup[group]--
		if c.queuedGroup[group] == 0 {
			delete(c.queuedGroup, group)
		}
	}
}

func (c *Controller) statsLocked() Stats {
	stats := Stats{MaxRunning: c.config.MaxRunning, MaximumQueued: c.config.MaximumQueued, MaximumMemoryBytes: c.config.MaximumMemoryBytes, Running: c.running, Queued: 0, MemoryBytes: c.runningMemory, Classes: make(map[Class]ClassStats, len(classOrder)), Principals: make(map[string]ActorStats), Groups: make(map[string]ActorStats)}
	for _, class := range classOrder {
		queue := c.queues[class]
		classStats := ClassStats{Policy: c.config.Classes[class], Running: c.runningClass[class], Queued: queue.queued, MemoryBytes: c.classMemory[class]}
		classStats.Borrowed = classStats.Running - classStats.Policy.ReservedRunning
		if classStats.Borrowed < 0 {
			classStats.Borrowed = 0
		}
		stats.Queued += queue.queued
		stats.Classes[class] = classStats
	}
	for principal, value := range c.runningPrincipal {
		stats.Principals[principal] = ActorStats{Running: value.running, MemoryBytes: value.memoryBytes, Queued: c.queuedPrincipal[principal]}
	}
	for principal, queued := range c.queuedPrincipal {
		value := stats.Principals[principal]
		value.Queued = queued
		stats.Principals[principal] = value
	}
	for group, value := range c.runningGroup {
		stats.Groups[group] = ActorStats{Running: value.running, MemoryBytes: value.memoryBytes, Queued: c.queuedGroup[group]}
	}
	for group, queued := range c.queuedGroup {
		value := stats.Groups[group]
		value.Queued = queued
		stats.Groups[group] = value
	}
	return stats
}

func (c *Controller) observeStats(stats Stats) {
	c.mu.Lock()
	observer := c.observer
	c.mu.Unlock()
	if observer != nil {
		observer.ObserveWorkload(stats)
	}
}

func (c *Controller) observeAdmission(event AdmissionEvent) {
	c.mu.Lock()
	observer := c.observer
	c.mu.Unlock()
	if observer != nil {
		observer.ObserveAdmission(event)
	}
}

func (l *lease) Context() context.Context { return l.ctx }
func (l *lease) QueueWait() time.Duration { return l.queueWait }
func (l *lease) Release() {
	if l == nil || l.controller == nil {
		return
	}
	l.once.Do(func() {
		contextErr := l.ctx.Err()
		l.cancel()
		c := l.controller
		c.mu.Lock()
		c.running--
		c.runningMemory -= l.request.EstimatedMemoryBytes
		c.runningClass[l.request.Class]--
		c.classMemory[l.request.Class] -= l.request.EstimatedMemoryBytes
		principal := c.runningPrincipal[l.request.PrincipalID]
		principal.running--
		principal.memoryBytes -= l.request.EstimatedMemoryBytes
		if principal.running == 0 {
			delete(c.runningPrincipal, l.request.PrincipalID)
		} else {
			c.runningPrincipal[l.request.PrincipalID] = principal
		}
		for _, groupID := range l.request.GroupIDs {
			group := c.runningGroup[groupID]
			group.running--
			group.memoryBytes -= l.request.EstimatedMemoryBytes
			if group.running == 0 {
				delete(c.runningGroup, groupID)
			} else {
				c.runningGroup[groupID] = group
			}
		}
		delete(c.active, l)
		c.scheduleLocked()
		stats := c.statsLocked()
		c.mu.Unlock()
		c.observeStats(stats)
		outcome := "completed"
		if contextErr == context.DeadlineExceeded {
			outcome = "timeout"
		} else if contextErr == context.Canceled {
			outcome = "canceled"
		}
		event := admissionEvent(l.request, outcome, "")
		event.QueueWait = l.queueWait
		event.Execution = c.clock.Now().Sub(l.started)
		c.observeAdmission(event)
	})
}

func (l *nestedLease) Context() context.Context { return l.ctx }
func (l *nestedLease) QueueWait() time.Duration { return 0 }
func (l *nestedLease) Release()                 {}

func (q *classQueue) enqueue(w *waiter) {
	actor := w.request.PrincipalID
	if _, ok := q.actors[actor]; !ok {
		q.order = append(q.order, actor)
	}
	q.actors[actor] = append(q.actors[actor], w)
	q.queued++
}

func (q *classQueue) peekEligible(eligible func(*waiter) bool) *waiter {
	if q.queued == 0 || len(q.order) == 0 {
		return nil
	}
	if q.cursor >= len(q.order) {
		q.cursor = 0
	}
	for offset := 0; offset < len(q.order); offset++ {
		index := (q.cursor + offset) % len(q.order)
		waiters := q.actors[q.order[index]]
		if len(waiters) > 0 && eligible(waiters[0]) {
			return waiters[0]
		}
	}
	return nil
}

func (q *classQueue) popEligible(eligible func(*waiter) bool) *waiter {
	if q.queued == 0 || len(q.order) == 0 {
		return nil
	}
	if q.cursor >= len(q.order) {
		q.cursor = 0
	}
	index := -1
	for offset := 0; offset < len(q.order); offset++ {
		candidate := (q.cursor + offset) % len(q.order)
		waiters := q.actors[q.order[candidate]]
		if len(waiters) > 0 && eligible(waiters[0]) {
			index = candidate
			break
		}
	}
	if index < 0 {
		return nil
	}
	actor := q.order[index]
	waiters := q.actors[actor]
	w := waiters[0]
	waiters = waiters[1:]
	q.queued--
	if len(waiters) == 0 {
		delete(q.actors, actor)
		q.order = append(q.order[:index], q.order[index+1:]...)
		if len(q.order) == 0 || index >= len(q.order) {
			q.cursor = 0
		} else {
			q.cursor = index
		}
	} else {
		q.actors[actor] = waiters
		q.cursor = (index + 1) % len(q.order)
	}
	return w
}

func (q *classQueue) remove(target *waiter) bool {
	actor := target.request.PrincipalID
	waiters := q.actors[actor]
	for i, candidate := range waiters {
		if candidate != target {
			continue
		}
		waiters = append(waiters[:i], waiters[i+1:]...)
		q.queued--
		if len(waiters) > 0 {
			q.actors[actor] = waiters
			return true
		}
		delete(q.actors, actor)
		for index, queuedActor := range q.order {
			if queuedActor != actor {
				continue
			}
			q.order = append(q.order[:index], q.order[index+1:]...)
			if index < q.cursor {
				q.cursor--
			}
			if len(q.order) == 0 || q.cursor >= len(q.order) {
				q.cursor = 0
			}
			break
		}
		return true
	}
	return false
}

func clonePolicies(source map[Class]Policy) map[Class]Policy {
	result := make(map[Class]Policy, len(classOrder))
	for _, class := range classOrder {
		result[class] = source[class]
	}
	return result
}

type realClock struct{}
type realTimer struct{ timer *time.Timer }

func (realClock) Now() time.Time { return time.Now() }
func (realClock) NewTimer(duration time.Duration) Timer {
	return realTimer{timer: time.NewTimer(duration)}
}
func (t realTimer) C() <-chan time.Time { return t.timer.C }
func (t realTimer) Stop() bool          { return t.timer.Stop() }
