package queryaudit

import (
	"context"
	"fmt"
	"strings"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type EventInput struct {
	// EventID is the caller-supplied durable event identity. PostgreSQL
	// persistence accepts UUID identities (UUIDv7 is recommended) and never
	// manufactures an identity from wall-clock time or process randomness.
	EventID string
	// RetryIdentity is an optional deterministic identity for callers that do
	// not have a UUID. PostgreSQL derives a stable UUID from this value so an
	// exact retry replays the original event while a changed payload conflicts.
	RetryIdentity string
	// ProjectID is the immutable project identity carried by every query event.
	ProjectID        projectgraph.ResourceID
	PrincipalID      string
	Surface          string
	Operation        string
	QueryKind        string
	ModelID          string
	Target           string
	ObjectType       string
	ObjectID         string
	RequestID        string
	CorrelationID    string
	Status           string
	DurationMS       int64
	QueueWaitMS      int64
	PlanningMS       int64
	ConnectionWaitMS int64
	DatabaseMS       int64
	ExecutionMS      int64
	ExecutionState   string
	RowsReturned     int
	BytesEstimate    int64
	Error            string
	SQL              string
	PlanText         string
	QueryJSON        string
}

type Event struct {
	ID string
	EventInput
	CreatedAt string
}

type Filter struct {
	ProjectID    projectgraph.ResourceID
	ProjectIDs   []projectgraph.ResourceID
	PrincipalID  string
	PrincipalIDs []string
	Surface      string
	Surfaces     []string
	Operation    string
	QueryKind    string
	QueryKinds   []string
	ModelID      string
	Target       string
	Status       string
	Statuses     []string
	Search       string
	From         string
	To           string
	CursorTime   string
	CursorID     string
	PageToken    string
	Limit        int
}

type FilterOption struct {
	Value string
	Count int
}

type Recorder interface {
	RecordQueryEvent(ctx context.Context, input EventInput) error
}

type Reader interface {
	GetQueryEvent(ctx context.Context, id string) (Event, error)
	ListQueryEvents(ctx context.Context, filter Filter) ([]Event, error)
	ListQueryEventFilterOptions(ctx context.Context, field, search string, limit int) ([]FilterOption, error)
}

// Store is the capability contract shared by query-history readers and
// recorders. Concrete persistence remains owned by analytics composition.
type Store interface {
	Recorder
	Reader
}

// Repository is retained as a contract alias for callers that have not yet
// adopted the storage-neutral name. Module configuration exposes Store.
type Repository = Store

func (input EventInput) Validate() error {
	if input.ProjectID == "" {
		return fmt.Errorf("query event project id is required")
	}
	if err := input.ProjectID.Validate(); err != nil {
		return fmt.Errorf("query event project id: %w", err)
	}
	if strings.TrimSpace(input.PrincipalID) == "" {
		return fmt.Errorf("query event principal id is required")
	}
	return nil
}

func (filter Filter) Validate() error {
	if filter.ProjectID != "" {
		if err := filter.ProjectID.Validate(); err != nil {
			return fmt.Errorf("query event filter project id: %w", err)
		}
	}
	for _, projectID := range filter.ProjectIDs {
		if projectID == "" {
			return fmt.Errorf("query event filter project ids cannot contain an empty id")
		}
		if err := projectID.Validate(); err != nil {
			return fmt.Errorf("query event filter project id: %w", err)
		}
	}
	return nil
}
