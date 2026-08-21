package workload

import (
	"context"
	"fmt"
	"time"
)

// New validates config and creates a controller. No controller state is
// allocated or exposed when validation fails.
func New(config Config, options ...Option) (*Controller, error) {
	config = config.Clone()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	c := &Controller{
		config:           config,
		clock:            realClock{},
		drained:          make(chan struct{}),
		runningClass:     make(map[Class]int, len(config.Classes)),
		classMemory:      make(map[Class]int64, len(config.Classes)),
		runningPrincipal: make(map[string]usage),
		runningGroup:     make(map[string]usage),
		queuedPrincipal:  make(map[string]int),
		queuedGroup:      make(map[string]int),
		active:           make(map[*lease]struct{}),
		queues:           make(map[Class]*classQueue, len(config.Classes)),
	}
	for _, class := range config.Classes {
		c.queues[class] = &classQueue{actors: make(map[string][]*waiter)}
	}
	for _, option := range options {
		if option != nil {
			option(c)
		}
	}
	if c.clock == nil {
		return nil, &ConfigError{Code: ConfigClockRequired, Field: "clock", message: "clock is required"}
	}
	return c, nil
}

// Config returns a defensive copy of the live policy.
func (c *Controller) Config() Config {
	if c == nil {
		return Config{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.config.Clone()
}

// Stats returns a defensive snapshot. A nil controller returns the zero
// snapshot.
func (c *Controller) Stats() Stats {
	if c == nil {
		return Stats{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.statsLocked()
}

// SetObserver replaces the observation sink. Nil disables observation.
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

// Close idempotently prevents new admissions, rejects queued work, and cancels
// active lease contexts. Active accounting remains until each lease is
// released, preserving exact ownership and release semantics.
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
	var waiters []*waiter
	for _, class := range c.config.Classes {
		queue := c.queues[class]
		for _, actor := range queue.order {
			for _, w := range queue.actors[actor] {
				if w.state == waiting {
					w.state = rejected
					waiters = append(waiters, w)
				}
			}
		}
		queue.actors = make(map[string][]*waiter)
		queue.order = nil
		queue.cursor = 0
		queue.queued = 0
	}
	c.queuedPrincipal = make(map[string]int)
	c.queuedGroup = make(map[string]int)
	active := make([]*lease, 0, len(c.active))
	for running := range c.active {
		active = append(active, running)
	}
	c.signalDrainedLocked()
	stats := c.statsLocked()
	c.mu.Unlock()
	for _, running := range active {
		running.cancel()
		if running.timer != nil {
			running.timer.Stop()
		}
	}
	for _, w := range waiters {
		wait := c.clock.Now().Sub(w.enqueued)
		err := c.rejection(w.request, ControllerShutdown, nil)
		err.(*Rejection).QueueWait = wait
		w.result <- acquireResult{err: err}
	}
	c.observeStats(stats)
}

// Drain closes the controller and waits until every admitted lease has been
// released. Close remains nonblocking; Drain is the explicit bounded shutdown
// operation for owners that must join admitted work before dependent cleanup.
func (c *Controller) Drain(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if c.drained == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.Close()
	select {
	case <-c.drained:
		return nil
	default:
	}
	select {
	case <-c.drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Controller) signalDrainedLocked() {
	if c.drained != nil && c.closed && len(c.active) == 0 {
		c.drainOnce.Do(func() { close(c.drained) })
	}
}

// cancelWaiter removes a still-queued waiter. If scheduling won the race, it
// drains and returns the granted lease so the caller can release it exactly
// once. The returned lease is never returned while the waiter remains queued.
func (c *Controller) cancelWaiter(w *waiter) (Lease, error, bool) {
	c.mu.Lock()
	if w.state == waiting {
		c.removeWaiterLocked(w)
		w.state = rejected
		c.scheduleLocked()
		stats := c.statsLocked()
		c.mu.Unlock()
		c.observeStats(stats)
		return nil, nil, true
	}
	state := w.state
	c.mu.Unlock()
	if state == rejected {
		result := <-w.result
		return result.lease, result.err, false
	}
	if state != granted {
		return nil, nil, false
	}
	result := <-w.result
	return result.lease, result.err, false
}

func (c *Controller) rejection(request Request, reason RejectionReason, cause error) error {
	return &Rejection{Reason: reason, Class: request.Class, PrincipalID: request.PrincipalID, GroupIDs: append([]string(nil), request.GroupIDs...), Operation: request.Operation, cause: cause}
}

func rejectionEvent(request Request, err error) AdmissionEvent {
	event := admissionEvent(request, OutcomeRejected, InvalidRequest)
	if rejection, ok := err.(*Rejection); ok {
		event.Reason = rejection.Reason
		if rejection.Class != "" {
			event.Class = rejection.Class
		}
		if rejection.PrincipalID != "" {
			event.PrincipalID = rejection.PrincipalID
		}
		if rejection.GroupIDs != nil {
			event.GroupIDs = append([]string(nil), rejection.GroupIDs...)
		}
		if rejection.Operation != "" {
			event.Operation = rejection.Operation
		}
		event.QueueWait = rejection.QueueWait
	}
	return event
}

func admissionEvent(request Request, outcome AdmissionOutcome, reason RejectionReason) AdmissionEvent {
	return AdmissionEvent{Class: request.Class, PrincipalID: request.PrincipalID, GroupIDs: append([]string(nil), request.GroupIDs...), Operation: request.Operation, EstimatedMemoryBytes: request.EstimatedMemoryBytes, Outcome: outcome, Reason: reason}
}

type realClock struct{}
type realTimer struct{ timer *time.Timer }

func (realClock) Now() time.Time { return time.Now() }
func (realClock) NewTimer(duration time.Duration) Timer {
	return realTimer{timer: time.NewTimer(duration)}
}
func (t realTimer) C() <-chan time.Time { return t.timer.C }
func (t realTimer) Stop() bool          { return t.timer.Stop() }

// ConfigErrorCode identifies a configuration validation failure.
type ConfigErrorCode string

const (
	ConfigInvalidMaximumRunning           ConfigErrorCode = "invalid_maximum_running"
	ConfigNegativeLimit                   ConfigErrorCode = "negative_limit"
	ConfigClassesRequired                 ConfigErrorCode = "classes_required"
	ConfigInvalidClass                    ConfigErrorCode = "invalid_class"
	ConfigDuplicateClass                  ConfigErrorCode = "duplicate_class"
	ConfigMissingPolicy                   ConfigErrorCode = "missing_policy"
	ConfigUndeclaredPolicy                ConfigErrorCode = "undeclared_policy"
	ConfigNegativePolicy                  ConfigErrorCode = "negative_policy"
	ConfigReservationExceedsClass         ConfigErrorCode = "reservation_exceeds_class"
	ConfigReservationsExceedInstance      ConfigErrorCode = "reservations_exceed_instance"
	ConfigClassRunningExceedsInstance     ConfigErrorCode = "class_running_exceeds_instance"
	ConfigClassQueueExceedsInstance       ConfigErrorCode = "class_queue_exceeds_instance"
	ConfigClassMemoryExceedsInstance      ConfigErrorCode = "class_memory_exceeds_instance"
	ConfigPrincipalRunningExceedsInstance ConfigErrorCode = "principal_running_exceeds_instance"
	ConfigPrincipalQueueExceedsInstance   ConfigErrorCode = "principal_queue_exceeds_instance"
	ConfigPrincipalMemoryExceedsInstance  ConfigErrorCode = "principal_memory_exceeds_instance"
	ConfigGroupRunningExceedsInstance     ConfigErrorCode = "group_running_exceeds_instance"
	ConfigGroupQueueExceedsInstance       ConfigErrorCode = "group_queue_exceeds_instance"
	ConfigGroupMemoryExceedsInstance      ConfigErrorCode = "group_memory_exceeds_instance"
	ConfigClockRequired                   ConfigErrorCode = "clock_required"
)

// ConfigError is a typed configuration validation error.
type ConfigError struct {
	Code    ConfigErrorCode
	Field   string
	Class   Class
	message string
}

func (e *ConfigError) Error() string {
	if e == nil {
		return ""
	}
	if e.Class != "" {
		return fmt.Sprintf("invalid workload configuration: %s (%s class=%s)", e.message, e.Code, e.Class)
	}
	return fmt.Sprintf("invalid workload configuration: %s (%s)", e.message, e.Code)
}
func (e *ConfigError) Is(target error) bool {
	other, ok := target.(*ConfigError)
	return ok && e != nil && other != nil && e.Code == other.Code
}
func configError(code ConfigErrorCode, field, message string) *ConfigError {
	return &ConfigError{Code: code, Field: field, message: message}
}
func configErrorClass(code ConfigErrorCode, field string, class Class, message string) *ConfigError {
	return &ConfigError{Code: code, Field: field, Class: class, message: message}
}
