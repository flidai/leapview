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
// rely on callbacks running under a controller mutex.
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

// Controller is the application-neutral admission boundary. The initial
// package contract validates and snapshots policy; the scheduler implementation
// can be added behind Acquire without changing callers.
type Controller struct {
	mu       sync.RWMutex
	config   Config
	clock    Clock
	observer Observer
	closed   bool
}

// New validates config and creates a controller. No controller state is
// allocated or exposed when validation fails.
func New(config Config, options ...Option) (*Controller, error) {
	config = config.Clone()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	c := &Controller{config: config, clock: realClock{}}
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

// Acquire validates and copies request data. Full scheduling is intentionally
// deferred to the controller implementation; valid requests currently fail
// closed with AdmissionUnavailable rather than receiving an unaccounted lease.
// AdmissionUnavailable is a temporary, explicit fail-closed contract for this
// initial extraction and must not be treated as an authorization decision.
func (c *Controller) Acquire(ctx context.Context, request Request) (Lease, error) {
	if c == nil {
		return nil, &Rejection{Reason: ControllerShutdown, Class: request.Class, PrincipalID: request.PrincipalID, Operation: request.Operation, GroupIDs: append([]string(nil), request.GroupIDs...)}
	}
	canonical, err := request.Canonicalize()
	if err != nil {
		return nil, err
	}
	c.mu.RLock()
	closed := c.closed
	configured := false
	for _, class := range c.config.Classes {
		if class == canonical.Class {
			configured = true
			break
		}
	}
	c.mu.RUnlock()
	if closed {
		return nil, &Rejection{Reason: ControllerShutdown, Class: canonical.Class, PrincipalID: canonical.PrincipalID, GroupIDs: canonical.GroupIDs, Operation: canonical.Operation}
	}
	if !configured {
		return nil, &Rejection{Reason: InvalidClass, Class: canonical.Class, PrincipalID: canonical.PrincipalID, GroupIDs: canonical.GroupIDs, Operation: canonical.Operation}
	}
	return nil, &Rejection{Reason: AdmissionUnavailable, Class: canonical.Class, PrincipalID: canonical.PrincipalID, GroupIDs: canonical.GroupIDs, Operation: canonical.Operation}
}

// Stats returns a defensive snapshot. A nil controller returns the zero
// snapshot.
func (c *Controller) Stats() Stats {
	if c == nil {
		return Stats{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	classes := append([]Class(nil), c.config.Classes...)
	classStats := make(map[Class]ClassStats, len(classes))
	for _, class := range classes {
		policy := c.config.Policies[class]
		classStats[class] = ClassStats{Policy: policy}
	}
	return Stats{MaximumRunning: c.config.MaximumRunning, MaximumQueued: c.config.MaximumQueued, MaximumMemoryBytes: c.config.MaximumMemoryBytes, ClassOrder: classes, Classes: classStats, Principals: map[string]ActorStats{}, Groups: map[string]ActorStats{}, Closed: c.closed}.Clone()
}

// SetObserver replaces the observation sink. Nil disables observation.
func (c *Controller) SetObserver(observer Observer) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.observer = observer
	c.mu.Unlock()
}

// Close idempotently prevents new admissions. Active and queued work will be
// canceled by the scheduler implementation.
func (c *Controller) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
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
