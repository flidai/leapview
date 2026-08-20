package workload

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// RejectionReason identifies a mechanically inspectable admission failure.
type RejectionReason string

const (
	InvalidRequest             RejectionReason = "invalid_request"
	InvalidClass               RejectionReason = "invalid_class"
	InvalidPrincipal           RejectionReason = "invalid_principal"
	InvalidGroup               RejectionReason = "invalid_group"
	InvalidOperation           RejectionReason = "invalid_operation"
	InvalidMemory              RejectionReason = "invalid_memory"
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
	AdmissionUnavailable       RejectionReason = "admission_unavailable"
)

// Rejection is a typed admission failure.  GroupIDs is always owned by the
// error and may be safely retained by callers.
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

func (e *Rejection) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}
func (e *Rejection) WorkloadRejectionReason() string {
	if e == nil {
		return ""
	}
	return string(e.Reason)
}

// ReasonOf extracts a rejection reason without parsing error text.
func ReasonOf(err error) (RejectionReason, bool) {
	var rejection *Rejection
	if !errors.As(err, &rejection) || rejection == nil {
		return "", false
	}
	return rejection.Reason, true
}

// IsReason reports whether err contains a rejection with reason.
func IsReason(err error, reason RejectionReason) bool {
	observed, ok := ReasonOf(err)
	return ok && observed == reason
}

// Lease is the handle returned for admitted work.  Release is idempotent and
// safe to call concurrently in a complete controller implementation.
type Lease interface {
	Context() context.Context
	QueueWait() time.Duration
	Release()
}

// Admitter is the narrow dependency applications may carry in a context.
type Admitter interface {
	Acquire(context.Context, Request) (Lease, error)
}

type admitterContextKey struct{}

// WithAdmitter associates an admission implementation with ctx.
func WithAdmitter(ctx context.Context, admitter Admitter) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if admitter == nil {
		return ctx
	}
	return context.WithValue(ctx, admitterContextKey{}, admitter)
}

// FromContext retrieves an admitter associated by WithAdmitter.
func FromContext(ctx context.Context) (Admitter, bool) {
	if ctx == nil {
		return nil, false
	}
	a, ok := ctx.Value(admitterContextKey{}).(Admitter)
	return a, ok && a != nil
}

type admissionContextKey struct{}

type activeAdmission struct {
	controller *Controller
	request    Request
	lease      *lease
}

// Current returns a defensive copy of the active admission carried by ctx.
// Lease.Context supplies this metadata for admitted work; an ordinary context
// has no current admission.
func Current(ctx context.Context) (Request, bool) {
	if ctx == nil {
		return Request{}, false
	}
	active, ok := ctx.Value(admissionContextKey{}).(*activeAdmission)
	if !ok || active == nil {
		return Request{}, false
	}
	return active.request.Clone(), true
}

// ActorStats describes one principal or group in a statistics snapshot.
type ActorStats struct {
	Running, Queued int
	MemoryBytes     int64
}

// ClassStats describes one configured class in a statistics snapshot.
type ClassStats struct {
	Policy          Policy
	Running, Queued int
	MemoryBytes     int64
	Borrowed        int
}

// Stats is an immutable snapshot from a Controller.  Maps and slices are
// owned by the snapshot and never expose controller state.
type Stats struct {
	MaximumRunning, MaximumQueued int
	MaximumMemoryBytes            int64
	Running, Queued               int
	MemoryBytes                   int64
	Closed                        bool
	ClassOrder                    []Class
	Classes                       map[Class]ClassStats
	Principals                    map[string]ActorStats
	Groups                        map[string]ActorStats
}

// Clone returns an independent snapshot copy.
func (s Stats) Clone() Stats {
	s.ClassOrder = append([]Class(nil), s.ClassOrder...)
	s.Classes = cloneClassStats(s.Classes)
	s.Principals = cloneActorStats(s.Principals)
	s.Groups = cloneActorStats(s.Groups)
	return s
}

// AdmissionOutcome identifies the lifecycle outcome of an observation.
type AdmissionOutcome string

const (
	OutcomeAdmitted AdmissionOutcome = "admitted"
	OutcomeRejected AdmissionOutcome = "rejected"
	OutcomeCanceled AdmissionOutcome = "canceled"
	OutcomeTimedOut AdmissionOutcome = "timed_out"
	OutcomeReleased AdmissionOutcome = "released"
)

// AdmissionEvent is an application-neutral observation of an admission
// lifecycle transition.
type AdmissionEvent struct {
	Class                Class
	PrincipalID          string
	GroupIDs             []string
	Operation            string
	EstimatedMemoryBytes int64
	Outcome              AdmissionOutcome
	Reason               RejectionReason
	QueueWait            time.Duration
	Execution            time.Duration
}

// Clone returns an independent event copy.
func (e AdmissionEvent) Clone() AdmissionEvent {
	e.GroupIDs = append([]string(nil), e.GroupIDs...)
	return e
}

// Observer receives snapshots and lifecycle events. Implementations must not
// rely on callbacks running under a controller mutex. Callback panics are
// recovered by the controller (observation is best-effort and cannot corrupt
// admission accounting).
type Observer interface {
	ObserveWorkload(Stats)
	ObserveAdmission(AdmissionEvent)
}

// Clock supplies time and timers to a controller. Hosts can inject a
// deterministic implementation for tests.
type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}

// Timer is the clock timer contract used for queue and execution deadlines.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

// Option configures optional controller integrations.
type Option func(*Controller)

// WithObserver installs an observation sink. A nil observer is a no-op.
func WithObserver(observer Observer) Option { return func(c *Controller) { c.observer = observer } }

// WithClock installs the time source used by a controller. A nil clock is
// rejected by New.
func WithClock(clock Clock) Option { return func(c *Controller) { c.clock = clock } }

// Controller is an application-neutral, process-local admission scheduler.
// It owns queueing, fairness, resource accounting, deadlines, and lease
// lifecycle for the host-supplied policy.
type Controller struct {
	mu        sync.RWMutex
	config    Config
	clock     Clock
	observer  Observer
	closed    bool
	drained   chan struct{}
	drainOnce sync.Once

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

type usage struct {
	running     int
	memoryBytes int64
}

type waiterState uint8

const (
	waiting waiterState = iota
	granted
	rejected
)

type waiter struct {
	request  Request
	parent   context.Context
	enqueued time.Time
	result   chan acquireResult
	state    waiterState
}

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

type lease struct {
	controller *Controller
	request    Request
	ctx        context.Context
	cancel     context.CancelFunc
	queueWait  time.Duration
	started    time.Time
	once       sync.Once
	refs       int
	// timer is only used for injected execution timers.  The standard
	// context.WithTimeout path is used for execution deadlines so the lease
	// context retains the usual Deadline/Err semantics.
	timer Timer
}

type nestedLease struct {
	ctx    context.Context
	parent *lease
	once   sync.Once
}
