package workload

// This package is the LeapView policy adapter. Generic admission, queueing,
// fairness, accounting, deadlines, and lifecycle semantics live exclusively
// in pkg/workload; this file only translates LeapView's stable class/config
// names and telemetry types at the application boundary.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	genericworkload "github.com/flidai/leapview/pkg/workload"
)

type Controller struct {
	mu         sync.RWMutex
	inner      *genericworkload.Controller
	observer   Observer
	observerAd genericworkload.Observer
	clock      Clock
}

type lease struct{ inner genericworkload.Lease }

func (l *lease) Context() context.Context {
	if l == nil || l.inner == nil {
		return nil
	}
	return l.inner.Context()
}
func (l *lease) QueueWait() time.Duration {
	if l == nil || l.inner == nil {
		return 0
	}
	return l.inner.QueueWait()
}
func (l *lease) Release() {
	if l != nil && l.inner != nil {
		l.inner.Release()
	}
}

func New(config Config, options ...Option) (*Controller, error) {
	config = cloneConfig(config)
	genericConfig, err := toGenericConfig(config)
	if err != nil {
		return nil, err
	}
	c := &Controller{}
	for _, option := range options {
		if option != nil {
			option(c)
		}
	}
	genericOptions := make([]genericworkload.Option, 0, 2)
	if c.observer != nil {
		c.observerAd = observerAdapter{observer: c.observer}
		genericOptions = append(genericOptions, genericworkload.WithObserver(c.observerAd))
	}
	if c.clock != nil {
		genericOptions = append(genericOptions, genericworkload.WithClock(clockAdapter{clock: c.clock}))
	}
	controller, err := genericworkload.New(genericConfig, genericOptions...)
	if err != nil {
		return nil, err
	}
	c.inner = controller
	return c, nil
}

func (c *Controller) Acquire(ctx context.Context, request Request) (Lease, error) {
	if c == nil {
		return nil, &Rejection{Reason: ControllerShutdown, Class: request.Class, PrincipalID: request.PrincipalID, GroupIDs: append([]string(nil), request.GroupIDs...), Operation: request.Operation}
	}
	c.mu.RLock()
	inner := c.inner
	c.mu.RUnlock()
	if inner == nil {
		return nil, &Rejection{Reason: ControllerShutdown, Class: request.Class, PrincipalID: request.PrincipalID, GroupIDs: append([]string(nil), request.GroupIDs...), Operation: request.Operation}
	}
	admitted, err := inner.Acquire(ctx, genericworkload.Request{Class: genericworkload.Class(request.Class), PrincipalID: request.PrincipalID, GroupIDs: append([]string(nil), request.GroupIDs...), Operation: request.Operation, EstimatedMemoryBytes: request.EstimatedMemoryBytes})
	if err != nil {
		return nil, c.fromGenericError(err, request)
	}
	return &lease{inner: admitted}, nil
}

func (c *Controller) Stats() Stats {
	if c == nil {
		return Stats{}
	}
	c.mu.RLock()
	inner := c.inner
	c.mu.RUnlock()
	if inner == nil {
		return Stats{}
	}
	return fromGenericStats(inner.Stats())
}

func (c *Controller) SetObserver(observer Observer) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.observer = observer
	inner := c.inner
	if observer == nil {
		c.observerAd = nil
	} else {
		c.observerAd = observerAdapter{observer: observer}
	}
	adapter := c.observerAd
	c.mu.Unlock()
	if inner != nil {
		inner.SetObserver(adapter)
	}
}

func (c *Controller) Close() {
	if c == nil {
		return
	}
	c.mu.RLock()
	inner := c.inner
	c.mu.RUnlock()
	if inner != nil {
		inner.Close()
	}
}

// Drain closes admission and waits for all admitted leases to release before
// dependent application resources are torn down.
func (c *Controller) Drain(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	inner := c.inner
	c.mu.RUnlock()
	if inner == nil {
		return nil
	}
	return inner.Drain(ctx)
}

func cloneConfig(config Config) Config {
	config.Classes = clonePolicies(config.Classes)
	return config
}

func toGenericConfig(config Config) (genericworkload.Config, error) {
	classes := make([]genericworkload.Class, 0, len(classOrder))
	policies := make(map[genericworkload.Class]genericworkload.Policy, len(classOrder))
	known := make(map[Class]struct{}, len(classOrder))
	for _, class := range classOrder {
		known[class] = struct{}{}
	}
	for class := range config.Classes {
		if _, ok := known[class]; !ok {
			return genericworkload.Config{}, fmt.Errorf("workload class %q is undeclared", class)
		}
	}
	for _, class := range classOrder {
		classes = append(classes, genericworkload.Class(class))
		// Legacy application configs may omit disabled classes. The adapter
		// materializes an explicit zero policy for each such class before
		// handing the ordered graph to the generic package.
		policy := config.Classes[class]
		policies[genericworkload.Class(class)] = genericworkload.Policy{ReservedRunning: policy.ReservedRunning, MaximumRunning: policy.MaximumRunning, MaximumQueued: policy.MaximumQueued, MaximumMemoryBytes: policy.MaximumMemoryBytes, QueueTimeout: policy.QueueTimeout, ExecutionTimeout: policy.ExecutionTimeout}
	}
	return genericworkload.Config{
		Classes: classes, Policies: policies,
		MaximumRunning: config.MaxRunning, MaximumQueued: config.MaximumQueued, MaximumMemoryBytes: config.MaximumMemoryBytes,
		MaximumRunningPerPrincipal: config.MaximumRunningPerPrincipal, MaximumQueuedPerPrincipal: config.MaximumQueuedPerPrincipal, MaximumMemoryBytesPerPrincipal: config.MaximumMemoryBytesPerPrincipal,
		MaximumRunningPerGroup: config.MaximumRunningPerGroup, MaximumQueuedPerGroup: config.MaximumQueuedPerGroup, MaximumMemoryBytesPerGroup: config.MaximumMemoryBytesPerGroup,
	}, nil
}

func (c *Controller) fromGenericError(err error, request Request) error {
	if err == nil {
		return nil
	}
	var genericRejection *genericworkload.Rejection
	if !errors.As(err, &genericRejection) || genericRejection == nil {
		return err
	}
	reason := RejectionReason(genericRejection.Reason)
	if genericRejection.Reason == genericworkload.InvalidPrincipal || genericRejection.Reason == genericworkload.InvalidGroup || genericRejection.Reason == genericworkload.InvalidClass || genericRejection.Reason == genericworkload.InvalidOperation || genericRejection.Reason == genericworkload.InvalidMemory {
		reason = InvalidRequest
	}
	return &Rejection{Reason: reason, Class: Class(genericRejection.Class), PrincipalID: genericRejection.PrincipalID, GroupIDs: append([]string(nil), genericRejection.GroupIDs...), Operation: genericRejection.Operation, QueueWait: genericRejection.QueueWait, cause: genericRejection}
}

func fromGenericStats(source genericworkload.Stats) Stats {
	stats := Stats{MaxRunning: source.MaximumRunning, MaximumQueued: source.MaximumQueued, MaximumMemoryBytes: source.MaximumMemoryBytes, Running: source.Running, Queued: source.Queued, MemoryBytes: source.MemoryBytes, Classes: make(map[Class]ClassStats, len(source.Classes)), Principals: make(map[string]ActorStats, len(source.Principals)), Groups: make(map[string]ActorStats, len(source.Groups))}
	for class, value := range source.Classes {
		stats.Classes[Class(class)] = ClassStats{Policy: Policy{ReservedRunning: value.Policy.ReservedRunning, MaximumRunning: value.Policy.MaximumRunning, MaximumQueued: value.Policy.MaximumQueued, MaximumMemoryBytes: value.Policy.MaximumMemoryBytes, QueueTimeout: value.Policy.QueueTimeout, ExecutionTimeout: value.Policy.ExecutionTimeout}, Running: value.Running, Queued: value.Queued, MemoryBytes: value.MemoryBytes, Borrowed: value.Borrowed}
	}
	for principal, value := range source.Principals {
		stats.Principals[principal] = ActorStats{Running: value.Running, Queued: value.Queued, MemoryBytes: value.MemoryBytes}
	}
	for group, value := range source.Groups {
		stats.Groups[group] = ActorStats{Running: value.Running, Queued: value.Queued, MemoryBytes: value.MemoryBytes}
	}
	return stats
}

type observerAdapter struct{ observer Observer }

type clockAdapter struct{ clock Clock }

func (a clockAdapter) Now() time.Time { return a.clock.Now() }
func (a clockAdapter) NewTimer(duration time.Duration) genericworkload.Timer {
	timer := a.clock.NewTimer(duration)
	if timer == nil {
		return nil
	}
	return timerAdapter{timer: timer}
}

type timerAdapter struct{ timer Timer }

func (t timerAdapter) C() <-chan time.Time { return t.timer.C() }
func (t timerAdapter) Stop() bool          { return t.timer.Stop() }

func (a observerAdapter) ObserveWorkload(stats genericworkload.Stats) {
	if a.observer != nil {
		a.observer.ObserveWorkload(fromGenericStats(stats))
	}
}
func (a observerAdapter) ObserveAdmission(event genericworkload.AdmissionEvent) {
	if a.observer != nil {
		outcome := string(event.Outcome)
		if event.Outcome == genericworkload.OutcomeReleased {
			outcome = "completed"
		} else if event.Outcome == genericworkload.OutcomeTimedOut {
			outcome = "timeout"
		}
		a.observer.ObserveAdmission(AdmissionEvent{Class: Class(event.Class), PrincipalID: event.PrincipalID, GroupIDs: append([]string(nil), event.GroupIDs...), Operation: event.Operation, EstimatedMemoryBytes: event.EstimatedMemoryBytes, Outcome: outcome, Reason: RejectionReason(event.Reason), QueueWait: event.QueueWait, Execution: event.Execution})
	}
}
