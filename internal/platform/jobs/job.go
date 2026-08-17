// Package jobs defines durable, leased background work shared by public API resources.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/flidai/leapview/internal/platform/transaction"
)

var (
	ErrConflict    = errors.New("async job conflicts with persisted work")
	ErrNotFound    = errors.New("async job not found")
	ErrUnknownKind = errors.New("async job kind is not registered")
)

type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

type EnqueueInput struct {
	ID                   string
	Kind                 string
	WorkloadClass        string
	PrincipalID          string
	GroupIDs             []string
	ResourceKind         string
	ResourceID           string
	EstimatedMemoryBytes int64
	Payload              []byte
}

type Job struct {
	ID, Kind, WorkloadClass, PrincipalID, ResourceKind, ResourceID string
	GroupIDs                                                       []string
	EstimatedMemoryBytes                                           int64
	Payload                                                        []byte
	Status                                                         Status
	Attempts                                                       int
	LeaseGeneration                                                int64
	LeaseOwner, LeaseExpiresAt                                     string
	CreatedAt, StartedAt, FinishedAt                               string
	ErrorJSON                                                      string
}

// SystemPrincipalID is used for internal work that has no end-user actor.
// It is deliberately a stable canonical identity rather than a content
// sentinel or an inferred/fallback value.
const SystemPrincipalID = "system:durable-jobs"

// CanonicalActor validates the durable actor identity and returns a stable
// sorted, deduplicated group projection. Identity strings are never trimmed or
// case-folded: callers must provide the canonical literal.
func CanonicalActor(principal string, groups []string) ([]string, error) {
	if principal == "" || principal != strings.TrimSpace(principal) || len(principal) > 256 || strings.IndexFunc(principal, unicode.IsControl) >= 0 {
		return nil, fmt.Errorf("principal id is not canonical")
	}
	return CanonicalGroups(groups)
}

// CanonicalGroups returns the stable actor-group projection while rejecting
// noncanonical literals. Empty groups are not meaningful actor membership.
func CanonicalGroups(groups []string) ([]string, error) {
	seen := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		if group == "" || group != strings.TrimSpace(group) || len(group) > 256 || strings.IndexFunc(group, unicode.IsControl) >= 0 {
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

// Fence identifies one exact durable claim. Owner identity is insufficient:
// a restarted worker can reuse an owner after its former lease was reclaimed.
type Fence struct {
	Owner      string
	Generation int64
}

func (j Job) Fence() Fence { return Fence{Owner: j.LeaseOwner, Generation: j.LeaseGeneration} }

type Event struct {
	ID                    int64
	ResourceKind          string
	ResourceID, EventType string
	Data                  []byte
	CreatedAt             string
}

type EventInput struct {
	Key          string
	ResourceKind string
	ResourceID   string
	EventType    string
	Data         []byte
}

// WorkflowIntent is the durable consequence of one capability-owned state
// transition. Event keys and optional job IDs make replay safe after ambiguous
// commits. Terminal transitions record an event without scheduling more work.
type WorkflowIntent struct {
	Event EventInput
	Job   EnqueueInput
}

// WorkflowRecorder writes an event and any required follow-up job using the
// transaction owned by the capability making the state transition.
type WorkflowRecorder interface {
	RecordWorkflow(context.Context, transaction.Transaction, WorkflowIntent) error
}

// WorkflowCommitter atomically records an event and its optional follow-up job
// when the caller does not already own a domain transaction.
type WorkflowCommitter interface {
	CommitWorkflow(context.Context, WorkflowIntent) error
}

type WorkflowRecorderFunc func(context.Context, transaction.Transaction, WorkflowIntent) error

func (f WorkflowRecorderFunc) RecordWorkflow(ctx context.Context, tx transaction.Transaction, intent WorkflowIntent) error {
	return f(ctx, tx, intent)
}

type EventAppender interface {
	AppendEvent(context.Context, string, string, string, []byte) (Event, error)
}

// Repository is the durable boundary used by async producers, workers, and
// event consumers. Storage adapters implement it without exposing their
// database handle to application composition.
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
