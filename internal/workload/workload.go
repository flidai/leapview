// Package workload owns instance-local workload admission, fairness, deadlines,
// and admission telemetry. It deliberately has no product-capability imports.
package workload

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

type Class string

const (
	Interactive Class = "interactive"
	Background  Class = "background"
	Refresh     Class = "refresh"
	Control     Class = "control"
	Maintenance Class = "maintenance"
)

var classOrder = []Class{Interactive, Background, Refresh, Control, Maintenance}

// Request is the complete actor and resource estimate for one admission.
// PrincipalID is always explicit, including for system work. GroupIDs are
// canonicalized by Controller.Acquire before they participate in admission.
type Request struct {
	Class                Class
	PrincipalID          string
	GroupIDs             []string
	Operation            string
	EstimatedMemoryBytes int64
}

type Policy struct {
	ReservedRunning    int
	MaximumRunning     int
	MaximumQueued      int
	MaximumMemoryBytes int64
	QueueTimeout       time.Duration
	ExecutionTimeout   time.Duration
}

type Config struct {
	MaxRunning                     int
	MaximumQueued                  int
	MaximumMemoryBytes             int64
	MaximumRunningPerPrincipal     int
	MaximumQueuedPerPrincipal      int
	MaximumMemoryBytesPerPrincipal int64
	MaximumRunningPerGroup         int
	MaximumQueuedPerGroup          int
	MaximumMemoryBytesPerGroup     int64
	Classes                        map[Class]Policy
}

func DefaultConfig() Config {
	return Config{
		MaxRunning:                     5,
		MaximumQueued:                  112,
		MaximumMemoryBytes:             1 << 30,
		MaximumRunningPerPrincipal:     2,
		MaximumQueuedPerPrincipal:      32,
		MaximumMemoryBytesPerPrincipal: 512 << 20,
		MaximumRunningPerGroup:         4,
		MaximumQueuedPerGroup:          64,
		MaximumMemoryBytesPerGroup:     768 << 20,
		Classes: map[Class]Policy{
			Interactive: {ReservedRunning: 3, MaximumRunning: 4, MaximumQueued: 64, MaximumMemoryBytes: 512 << 20, QueueTimeout: 30 * time.Second, ExecutionTimeout: 2 * time.Minute},
			Background:  {MaximumRunning: 1, MaximumQueued: 16, MaximumMemoryBytes: 768 << 20, QueueTimeout: 2 * time.Minute, ExecutionTimeout: 15 * time.Minute},
			Refresh:     {ReservedRunning: 1, MaximumRunning: 1, MaximumQueued: 16, MaximumMemoryBytes: 768 << 20, QueueTimeout: 2 * time.Minute},
			Control:     {ReservedRunning: 1, MaximumRunning: 1, MaximumQueued: 16, MaximumMemoryBytes: 256 << 20, QueueTimeout: 2 * time.Minute, ExecutionTimeout: 15 * time.Minute},
			Maintenance: {MaximumRunning: 1, MaximumMemoryBytes: 768 << 20, ExecutionTimeout: 30 * time.Minute},
		},
	}
}

func (c Config) Validate() error {
	if c.MaxRunning < 0 || c.MaximumQueued < 0 || c.MaximumMemoryBytes < 0 ||
		c.MaximumRunningPerPrincipal < 0 || c.MaximumQueuedPerPrincipal < 0 || c.MaximumMemoryBytesPerPrincipal < 0 ||
		c.MaximumRunningPerGroup < 0 || c.MaximumQueuedPerGroup < 0 || c.MaximumMemoryBytesPerGroup < 0 {
		return fmt.Errorf("workload limits must not be negative")
	}
	if c.MaximumRunningPerPrincipal > c.MaxRunning && c.MaximumRunningPerPrincipal != 0 {
		return fmt.Errorf("principal running limit exceeds instance maximum")
	}
	if c.MaximumQueuedPerPrincipal > c.MaximumQueued && c.MaximumQueuedPerPrincipal != 0 {
		return fmt.Errorf("principal queue limit exceeds instance maximum")
	}
	if c.MaximumRunningPerGroup > c.MaxRunning && c.MaximumRunningPerGroup != 0 {
		return fmt.Errorf("group running limit exceeds instance maximum")
	}
	if c.MaximumQueuedPerGroup > c.MaximumQueued && c.MaximumQueuedPerGroup != 0 {
		return fmt.Errorf("group queue limit exceeds instance maximum")
	}
	if c.MaximumMemoryBytes > 0 && c.MaximumMemoryBytesPerPrincipal > c.MaximumMemoryBytes {
		return fmt.Errorf("principal memory limit exceeds instance maximum")
	}
	if c.MaximumMemoryBytes > 0 && c.MaximumMemoryBytesPerGroup > c.MaximumMemoryBytes {
		return fmt.Errorf("group memory limit exceeds instance maximum")
	}
	reserved := 0
	for _, class := range classOrder {
		policy := c.Classes[class]
		if policy.ReservedRunning < 0 || policy.MaximumRunning < 0 || policy.MaximumQueued < 0 || policy.MaximumMemoryBytes < 0 || policy.QueueTimeout < 0 || policy.ExecutionTimeout < 0 {
			return fmt.Errorf("workload %s limits must not be negative", class)
		}
		if policy.ReservedRunning > policy.MaximumRunning {
			return fmt.Errorf("workload %s reserved running exceeds maximum running", class)
		}
		if policy.MaximumRunning > c.MaxRunning {
			return fmt.Errorf("workload %s maximum running exceeds instance maximum", class)
		}
		if policy.MaximumQueued > c.MaximumQueued {
			return fmt.Errorf("workload %s maximum queue exceeds instance maximum", class)
		}
		if c.MaximumMemoryBytes > 0 && policy.MaximumMemoryBytes > c.MaximumMemoryBytes {
			return fmt.Errorf("workload %s memory limit exceeds instance maximum", class)
		}
		reserved += policy.ReservedRunning
	}
	if reserved > c.MaxRunning {
		return fmt.Errorf("workload reservations exceed instance capacity")
	}
	return nil
}

type RejectionReason string

const (
	InstanceQueueFull          RejectionReason = "instance_queue_full"
	ClassQueueFull             RejectionReason = "class_queue_full"
	PrincipalQueueFull         RejectionReason = "principal_queue_full"
	GroupQueueFull             RejectionReason = "group_queue_full"
	InstanceMemoryLimit        RejectionReason = "instance_memory_limit"
	ClassMemoryLimit           RejectionReason = "class_memory_limit"
	PrincipalMemoryLimit       RejectionReason = "principal_memory_limit"
	GroupMemoryLimit           RejectionReason = "group_memory_limit"
	QueueTimeout               RejectionReason = "queue_timeout"
	ConflictingNestedAdmission RejectionReason = "conflicting_nested_admission"
	ControllerShutdown         RejectionReason = "controller_shutdown"
	InvalidRequest             RejectionReason = "invalid_request"
)

type Rejection struct {
	Reason      RejectionReason
	Class       Class
	PrincipalID string
	GroupIDs    []string
	Operation   string
	QueueWait   time.Duration
	cause       error
}

func (e *Rejection) Error() string {
	if e == nil {
		return "workload admission rejected"
	}
	return fmt.Sprintf("workload admission rejected: %s (class=%s principal=%s)", e.Reason, e.Class, e.PrincipalID)
}

func (e *Rejection) Unwrap() error                   { return e.cause }
func (e *Rejection) WorkloadRejectionReason() string { return string(e.Reason) }

func ReasonOf(err error) (RejectionReason, bool) {
	var rejection *Rejection
	if !errors.As(err, &rejection) {
		return "", false
	}
	return rejection.Reason, true
}

type Lease interface {
	Context() context.Context
	QueueWait() time.Duration
	Release()
}

type Admitter interface {
	Acquire(context.Context, Request) (Lease, error)
}

type admitterContextKey struct{}

func WithAdmitter(ctx context.Context, admitter Admitter) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if admitter == nil {
		return ctx
	}
	return context.WithValue(ctx, admitterContextKey{}, admitter)
}

func FromContext(ctx context.Context) (Admitter, bool) {
	if ctx == nil {
		return nil, false
	}
	admitter, ok := ctx.Value(admitterContextKey{}).(Admitter)
	return admitter, ok && admitter != nil
}

// Current returns the currently admitted class and explicit principal.
func Current(ctx context.Context) (Class, string, bool) {
	if ctx == nil {
		return "", "", false
	}
	active, ok := ctx.Value(admissionContextKey{}).(*admissionContext)
	if !ok || active == nil {
		return "", "", false
	}
	return active.class, active.principal, true
}

type ActorStats struct {
	Running     int
	Queued      int
	MemoryBytes int64
}

type ClassStats struct {
	Policy      Policy
	Running     int
	Queued      int
	MemoryBytes int64
	Borrowed    int
}

type Stats struct {
	MaxRunning         int
	MaximumQueued      int
	MaximumMemoryBytes int64
	Running            int
	Queued             int
	MemoryBytes        int64
	Classes            map[Class]ClassStats
	Principals         map[string]ActorStats
	Groups             map[string]ActorStats
}

type AdmissionEvent struct {
	Class                Class
	PrincipalID          string
	GroupIDs             []string
	Operation            string
	EstimatedMemoryBytes int64
	Outcome              string
	Reason               RejectionReason
	QueueWait            time.Duration
	Execution            time.Duration
}

type Observer interface {
	ObserveWorkload(Stats)
	ObserveAdmission(AdmissionEvent)
}

type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}

type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

type Option func(*Controller)

func WithObserver(observer Observer) Option { return func(c *Controller) { c.observer = observer } }
func WithClock(clock Clock) Option          { return func(c *Controller) { c.clock = clock } }

func normalizeGroups(groups []string) ([]string, error) {
	seen := make(map[string]struct{}, len(groups))
	for _, raw := range groups {
		if raw == "" || raw != strings.TrimSpace(raw) || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("group id contains control characters")
		}
		seen[raw] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for group := range seen {
		result = append(result, group)
	}
	sort.Strings(result)
	return result, nil
}
