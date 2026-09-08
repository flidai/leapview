package jobs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

var (
	ErrConflict    = errors.New("async job conflicts with persisted work")
	ErrNotFound    = errors.New("async job not found")
	ErrUnknownKind = errors.New("async job kind is not registered")
)

// RetryError asks a durable runner to requeue the current fenced attempt
// after Delay. It is reserved for transient failures after the handler has
// established that replay is safe; ordinary errors remain terminal.
type RetryError struct {
	Err   error
	Delay time.Duration
}

// AdmissionRequest carries the LeapView-owned resource and actor dimensions
// applied around a River worker invocation.
type AdmissionRequest struct {
	Class                string
	PrincipalID          string
	GroupIDs             []string
	EstimatedMemoryBytes int64
	Operation            string
}

type AdmissionLease interface {
	Context() context.Context
	Release()
}

type Admitter interface {
	Acquire(context.Context, AdmissionRequest) (AdmissionLease, error)
}

type AdmitterFunc func(context.Context, AdmissionRequest) (AdmissionLease, error)

func (f AdmitterFunc) Acquire(ctx context.Context, request AdmissionRequest) (AdmissionLease, error) {
	return f(ctx, request)
}

// Handler owns payload decoding and capability behavior for one admitted
// River job kind.
type Handler interface {
	Kind() string
	Handle(context.Context, Job) error
}

type HandlerFunc struct {
	JobKind               string
	Run                   func(context.Context, Job) error
	ExecutionLeaseTimeout time.Duration
}

func (h HandlerFunc) Kind() string { return h.JobKind }
func (h HandlerFunc) Handle(ctx context.Context, job Job) error {
	if h.Run == nil {
		return fmt.Errorf("job handler %q is not configured", h.JobKind)
	}
	return h.Run(ctx, job)
}
func (h HandlerFunc) LeaseTimeout() time.Duration { return h.ExecutionLeaseTimeout }

func (e *RetryError) Error() string {
	if e == nil || e.Err == nil {
		return "async job retry requested"
	}
	return e.Err.Error()
}

func (e *RetryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Retryable marks err for durable replay. A nil error remains nil so callers
// can wrap return values without changing successful behavior.
func Retryable(err error, delay time.Duration) error {
	if err == nil {
		return nil
	}
	return &RetryError{Err: err, Delay: delay}
}

// Status describes the durable lifecycle state of a job.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// EnqueueInput is the durable job request written by a producer.
type EnqueueInput struct {
	ID            string
	Kind          string
	WorkloadClass string
	PrincipalID   string
	GroupIDs      []string
	// PartitionKey scopes principal FIFO/fairness. Refresh producers set this
	// to their authoritative project/environment partition; every producer must
	// provide its own stable capability/scope partition.
	PartitionKey         string
	ResourceKind         string
	ResourceID           string
	EstimatedMemoryBytes int64
	Payload              []byte
}

// Job is the durable representation returned by a Repository.
type Job struct {
	ID, Kind, WorkloadClass, PrincipalID, PartitionKey, ResourceKind, ResourceID string
	RequestDigest                                                                string
	GroupIDs                                                                     []string
	EstimatedMemoryBytes                                                         int64
	Payload                                                                      []byte
	Status                                                                       Status
	Attempts                                                                     int
	LeaseGeneration                                                              int64
	LeaseOwner, LeaseExpiresAt                                                   string
	CreatedAt, StartedAt, FinishedAt                                             string
	ErrorJSON                                                                    string
}

// CanonicalActor validates an actor identity and returns a stable sorted,
// deduplicated group projection. Identity strings are not trimmed or
// case-folded: callers must provide the canonical literal.
func CanonicalActor(principal string, groups []string) ([]string, error) {
	if !canonicalLiteral(principal, 256) {
		return nil, fmt.Errorf("principal id is not canonical")
	}
	return CanonicalGroups(groups)
}

// CanonicalGroups returns the stable actor-group projection while rejecting
// noncanonical literals. Empty groups are not meaningful actor membership.
func CanonicalGroups(groups []string) ([]string, error) {
	seen := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		if !canonicalLiteral(group, 256) {
			return nil, fmt.Errorf("group id is not canonical")
		}
		seen[group] = struct{}{}
	}
	canonical := make([]string, 0, len(seen))
	for group := range seen {
		canonical = append(canonical, group)
	}
	sort.Strings(canonical)
	return canonical, nil
}

// Fence identifies one exact durable claim. Owner identity alone is
// insufficient because a restarted worker can reuse an owner after its former
// lease has been reclaimed.
type Fence struct {
	Owner      string
	Generation int64
}

func (j Job) Fence() Fence { return Fence{Owner: j.LeaseOwner, Generation: j.LeaseGeneration} }

// Event is an append-only resource event.
type Event struct {
	ID                    int64
	ResourceKind          string
	ResourceID, EventType string
	Data                  []byte
	CreatedAt             string
}

// EventInput describes an event in a WorkflowIntent.
type EventInput struct {
	Key          string
	ResourceKind string
	ResourceID   string
	EventType    string
	Data         []byte
}

// WorkflowIntent is the durable consequence of one capability-owned state
// transition. Event keys and optional job IDs make replay safe after
// ambiguous commits. Terminal transitions record an event without scheduling
// more work.
type WorkflowIntent struct {
	Event EventInput
	Job   EnqueueInput
}

// WorkflowCommitter atomically records an event and its optional follow-up
// job when the caller does not already own a domain transaction.
type WorkflowCommitter interface {
	CommitWorkflow(context.Context, WorkflowIntent) error
}

// WorkflowCommitterFunc adapts a function to WorkflowCommitter.
type WorkflowCommitterFunc func(context.Context, WorkflowIntent) error

func (f WorkflowCommitterFunc) CommitWorkflow(ctx context.Context, intent WorkflowIntent) error {
	return f(ctx, intent)
}

// EventAppender is the event-only subset of Repository used by producers.
type EventAppender interface {
	AppendEvent(context.Context, string, string, string, []byte) (Event, error)
}

// Repository is LeapView's product-facing asynchronous-operation boundary.
// River owns candidate selection, claims, retries, leases, and worker state;
// those executor details are deliberately absent here.
type Repository interface {
	Enqueue(context.Context, EnqueueInput) (Job, error)
	Get(context.Context, string) (Job, error)
	Cancel(context.Context, string) error
	AppendEvent(context.Context, string, string, string, []byte) (Event, error)
	ListEvents(context.Context, string, string, int64, int) ([]Event, error)
}

func canonicalLiteral(value string, max int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= max && strings.IndexFunc(value, unicode.IsControl) < 0
}
