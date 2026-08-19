package workload

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Class is an opaque, host-defined workload class identifier.  The package
// deliberately does not publish a set of product classes.
type Class string

// Policy bounds one configured class. A zero memory limit means the class adds
// no memory limit. A zero running maximum disables execution for the class. A
// zero queue maximum disables queuing. A zero timeout disables that deadline.
type Policy struct {
	ReservedRunning    int
	MaximumRunning     int
	MaximumQueued      int
	MaximumMemoryBytes int64
	QueueTimeout       time.Duration
	ExecutionTimeout   time.Duration
}

// Config is the complete admission policy supplied by a host.  Classes are
// ordered and are never inferred from map iteration.  Policies must contain
// exactly one entry for every class and no entry for an undeclared class.
//
// Config has no default value: an all-zero value is invalid.  Callers may
// mutate a Config after New returns; the controller retains a defensive copy.
type Config struct {
	Classes  []Class
	Policies map[Class]Policy

	MaximumRunning                 int
	MaximumQueued                  int
	MaximumMemoryBytes             int64
	MaximumRunningPerPrincipal     int
	MaximumQueuedPerPrincipal      int
	MaximumMemoryBytesPerPrincipal int64
	MaximumRunningPerGroup         int
	MaximumQueuedPerGroup          int
	MaximumMemoryBytesPerGroup     int64
}

// Validate checks the host-supplied policy without allocating controller
// state.  Validation errors are *ConfigError values and can be inspected with
// errors.As or errors.Is.
func (c Config) Validate() error {
	if c.MaximumRunning <= 0 {
		return configError(ConfigInvalidMaximumRunning, "maximum_running", "instance maximum running must be positive")
	}
	if err := validateNonNegativeConfigLimits(c); err != nil {
		return err
	}
	if c.MaximumRunningPerPrincipal > c.MaximumRunning {
		return configError(ConfigPrincipalRunningExceedsInstance, "maximum_running_per_principal", "principal running limit exceeds instance maximum")
	}
	if c.MaximumQueuedPerPrincipal > c.MaximumQueued {
		return configError(ConfigPrincipalQueueExceedsInstance, "maximum_queued_per_principal", "principal queue limit exceeds instance maximum")
	}
	if c.MaximumRunningPerGroup > c.MaximumRunning {
		return configError(ConfigGroupRunningExceedsInstance, "maximum_running_per_group", "group running limit exceeds instance maximum")
	}
	if c.MaximumQueuedPerGroup > c.MaximumQueued {
		return configError(ConfigGroupQueueExceedsInstance, "maximum_queued_per_group", "group queue limit exceeds instance maximum")
	}
	if c.MaximumMemoryBytes > 0 && c.MaximumMemoryBytesPerPrincipal > c.MaximumMemoryBytes {
		return configError(ConfigPrincipalMemoryExceedsInstance, "maximum_memory_bytes_per_principal", "principal memory limit exceeds instance memory limit")
	}
	if c.MaximumMemoryBytes > 0 && c.MaximumMemoryBytesPerGroup > c.MaximumMemoryBytes {
		return configError(ConfigGroupMemoryExceedsInstance, "maximum_memory_bytes_per_group", "group memory limit exceeds instance memory limit")
	}
	if len(c.Classes) == 0 {
		return configError(ConfigClassesRequired, "classes", "at least one class is required")
	}

	seen := make(map[Class]struct{}, len(c.Classes))
	reservations := 0
	for _, class := range c.Classes {
		if err := validateIdentifier(string(class), "class"); err != nil {
			return configError(ConfigInvalidClass, "classes", err.Error())
		}
		if _, ok := seen[class]; ok {
			return configErrorClass(ConfigDuplicateClass, "classes", class, "class is declared more than once")
		}
		seen[class] = struct{}{}
		policy, ok := c.Policies[class]
		if !ok {
			return configErrorClass(ConfigMissingPolicy, "policies", class, "every declared class requires one policy")
		}
		if err := validatePolicy(class, policy, c.MaximumRunning, c.MaximumQueued, c.MaximumMemoryBytes); err != nil {
			return err
		}
		if policy.ReservedRunning > c.MaximumRunning-reservations {
			return configError(ConfigReservationsExceedInstance, "policies", "class reservations exceed instance maximum running")
		}
		reservations += policy.ReservedRunning
	}
	for class := range c.Policies {
		if _, ok := seen[class]; !ok {
			return configErrorClass(ConfigUndeclaredPolicy, "policies", class, "policy is supplied for an undeclared class")
		}
	}
	return nil
}

func validateNonNegativeConfigLimits(c Config) error {
	limits := []struct {
		name string
		v    int64
	}{
		{"maximum_running", int64(c.MaximumRunning)},
		{"maximum_queued", int64(c.MaximumQueued)},
		{"maximum_memory_bytes", c.MaximumMemoryBytes},
		{"maximum_running_per_principal", int64(c.MaximumRunningPerPrincipal)},
		{"maximum_queued_per_principal", int64(c.MaximumQueuedPerPrincipal)},
		{"maximum_memory_bytes_per_principal", c.MaximumMemoryBytesPerPrincipal},
		{"maximum_running_per_group", int64(c.MaximumRunningPerGroup)},
		{"maximum_queued_per_group", int64(c.MaximumQueuedPerGroup)},
		{"maximum_memory_bytes_per_group", c.MaximumMemoryBytesPerGroup},
	}
	for _, limit := range limits {
		if limit.v < 0 {
			return configError(ConfigNegativeLimit, limit.name, "limits must not be negative")
		}
	}
	return nil
}

func validatePolicy(class Class, p Policy, maxRunning, maxQueued int, maxMemory int64) error {
	if p.ReservedRunning < 0 || p.MaximumRunning < 0 || p.MaximumQueued < 0 || p.MaximumMemoryBytes < 0 || p.QueueTimeout < 0 || p.ExecutionTimeout < 0 {
		return configErrorClass(ConfigNegativePolicy, "policies", class, "policy limits and durations must not be negative")
	}
	if p.ReservedRunning > p.MaximumRunning {
		return configErrorClass(ConfigReservationExceedsClass, "reserved_running", class, "class reservation exceeds class maximum running")
	}
	if p.MaximumRunning > maxRunning {
		return configErrorClass(ConfigClassRunningExceedsInstance, "maximum_running", class, "class running limit exceeds instance maximum")
	}
	if p.MaximumQueued > maxQueued {
		return configErrorClass(ConfigClassQueueExceedsInstance, "maximum_queued", class, "class queue limit exceeds instance maximum")
	}
	if maxMemory > 0 && p.MaximumMemoryBytes > 0 && p.MaximumMemoryBytes > maxMemory {
		return configErrorClass(ConfigClassMemoryExceedsInstance, "maximum_memory_bytes", class, "class memory limit exceeds instance memory limit")
	}
	return nil
}

// Clone returns a deep copy of the configuration.
func (c Config) Clone() Config {
	c.Classes = append([]Class(nil), c.Classes...)
	policies := c.Policies
	c.Policies = make(map[Class]Policy, len(policies))
	for class, policy := range policies {
		c.Policies[class] = policy
	}
	return c
}

// Identity identifies the principal and groups associated with a request.
// Identifiers are opaque: Canonicalize only validates, deduplicates, sorts,
// and copies groups; it does not trim or case-fold any value.
type Identity struct {
	PrincipalID string
	GroupIDs    []string
}

// MaxIdentifierLength bounds class, principal, and group identifiers. The
// values remain opaque; the bound only prevents unbounded admission metadata.
const MaxIdentifierLength = 256

// MaxOperationLength bounds operation labels carried by observations.
const MaxOperationLength = 96

// Canonicalize validates and defensively copies an identity.  Group IDs are
// sorted and duplicate IDs are removed.
func (i Identity) Canonicalize() (Identity, error) {
	if err := validateIdentifier(i.PrincipalID, "principal"); err != nil {
		return Identity{}, &Rejection{Reason: InvalidPrincipal, PrincipalID: i.PrincipalID, cause: err}
	}
	groups, err := canonicalGroups(i.GroupIDs)
	if err != nil {
		return Identity{}, &Rejection{Reason: InvalidGroup, PrincipalID: i.PrincipalID, cause: err}
	}
	return Identity{PrincipalID: i.PrincipalID, GroupIDs: groups}, nil
}

// Clone returns a defensive copy of the identity.
func (i Identity) Clone() Identity {
	i.GroupIDs = append([]string(nil), i.GroupIDs...)
	return i
}

// Request is the actor, operation, class, and resource estimate submitted for
// admission.  EstimatedMemoryBytes must be positive.
type Request struct {
	Class                Class
	PrincipalID          string
	GroupIDs             []string
	Operation            string
	EstimatedMemoryBytes int64
}

// Clone returns a defensive copy of the request.
func (r Request) Clone() Request {
	r.GroupIDs = append([]string(nil), r.GroupIDs...)
	return r
}

// Canonicalize validates and copies a request.  Class membership and memory
// bounds that depend on a Config are checked by Controller.Acquire when the
// scheduler implementation is enabled.
func (r Request) Canonicalize() (Request, error) {
	identity, err := (Identity{PrincipalID: r.PrincipalID, GroupIDs: r.GroupIDs}).Canonicalize()
	if err != nil {
		return Request{}, err
	}
	if err := validateIdentifier(string(r.Class), "class"); err != nil {
		return Request{}, &Rejection{Reason: InvalidClass, Class: r.Class, PrincipalID: identity.PrincipalID, GroupIDs: identity.GroupIDs, cause: err}
	}
	if err := validateOperation(r.Operation); err != nil {
		return Request{}, &Rejection{Reason: InvalidOperation, Class: r.Class, PrincipalID: identity.PrincipalID, GroupIDs: identity.GroupIDs, Operation: r.Operation, cause: err}
	}
	if r.EstimatedMemoryBytes <= 0 {
		return Request{}, &Rejection{Reason: InvalidMemory, Class: r.Class, PrincipalID: identity.PrincipalID, GroupIDs: identity.GroupIDs, Operation: r.Operation}
	}
	r.PrincipalID, r.GroupIDs = identity.PrincipalID, identity.GroupIDs
	return r, nil
}

func validateOperation(operation string) error {
	if operation == "" {
		return errors.New("operation must not be empty")
	}
	if operation != strings.TrimSpace(operation) {
		return errors.New("operation must not be whitespace padded")
	}
	if strings.IndexFunc(operation, unicode.IsControl) >= 0 {
		return errors.New("operation must not contain control characters")
	}
	if len(operation) > MaxOperationLength {
		return fmt.Errorf("operation exceeds %d bytes", MaxOperationLength)
	}
	return nil
}

func validateIdentifier(value, kind string) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", kind)
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must not be whitespace padded", kind)
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("%s must not contain control characters", kind)
	}
	if len(value) > MaxIdentifierLength {
		return fmt.Errorf("%s exceeds %d bytes", kind, MaxIdentifierLength)
	}
	return nil
}

func canonicalGroups(groups []string) ([]string, error) {
	seen := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		if err := validateIdentifier(group, "group"); err != nil {
			return nil, err
		}
		seen[group] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for group := range seen {
		result = append(result, group)
	}
	sort.Strings(result)
	return result, nil
}

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

func (c *Controller) statsLocked() Stats {
	stats := Stats{MaximumRunning: c.config.MaximumRunning, MaximumQueued: c.config.MaximumQueued, MaximumMemoryBytes: c.config.MaximumMemoryBytes, Running: c.running, MemoryBytes: c.runningMemory, ClassOrder: append([]Class(nil), c.config.Classes...), Classes: make(map[Class]ClassStats, len(c.config.Classes)), Principals: make(map[string]ActorStats), Groups: make(map[string]ActorStats), Closed: c.closed}
	for _, class := range c.config.Classes {
		queue := c.queues[class]
		classStats := ClassStats{Policy: c.config.Policies[class], Running: c.runningClass[class], Queued: queue.queued, MemoryBytes: c.classMemory[class]}
		if borrowed := classStats.Running - classStats.Policy.ReservedRunning; borrowed > 0 {
			classStats.Borrowed = borrowed
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
	return stats.Clone()
}

func (c *Controller) observeStats(stats Stats) {
	c.mu.RLock()
	observer := c.observer
	c.mu.RUnlock()
	if observer == nil {
		return
	}
	defer func() { _ = recover() }()
	observer.ObserveWorkload(stats.Clone())
}

func (c *Controller) observeAdmission(event AdmissionEvent) {
	c.mu.RLock()
	observer := c.observer
	c.mu.RUnlock()
	if observer == nil {
		return
	}
	defer func() { _ = recover() }()
	observer.ObserveAdmission(event.Clone())
}

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
	q.queued--
	if len(waiters) == 1 {
		delete(q.actors, actor)
		q.order = append(q.order[:index], q.order[index+1:]...)
		if len(q.order) == 0 || index >= len(q.order) {
			q.cursor = 0
		} else {
			q.cursor = index
		}
	} else {
		q.actors[actor] = waiters[1:]
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
		q.queued--
		waiters = append(waiters[:i], waiters[i+1:]...)
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

func cloneActorStats(source map[string]ActorStats) map[string]ActorStats {
	result := make(map[string]ActorStats, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
func cloneClassStats(source map[Class]ClassStats) map[Class]ClassStats {
	result := make(map[Class]ClassStats, len(source))
	for key, value := range source {
		result[key] = value
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
