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

// Repository is the durable boundary used by producers, workers, and event
// consumers. Storage adapters implement it without exposing their database
// handle to application composition.
type Repository interface {
	Enqueue(context.Context, EnqueueInput) (Job, error)
	Get(context.Context, string) (Job, error)
	Candidates(context.Context, string, int) ([]Job, error)
	ClaimByID(context.Context, string, string, string, time.Duration) (Job, bool, error)
	Renew(context.Context, string, Fence, time.Duration) error
	Complete(context.Context, string, Fence) error
	Fail(context.Context, string, Fence, []byte) error
	Cancel(context.Context, string) error
	CancelClaimed(context.Context, string, Fence) error
	AppendEvent(context.Context, string, string, string, []byte) (Event, error)
	ListEvents(context.Context, string, string, int64, int) ([]Event, error)
}

func canonicalLiteral(value string, max int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= max && strings.IndexFunc(value, unicode.IsControl) < 0
}
