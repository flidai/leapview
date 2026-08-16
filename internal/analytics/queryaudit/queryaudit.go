package queryaudit

import (
	"context"
	"fmt"
	"strings"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type EventInput struct {
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

type Repository interface {
	Recorder
	Reader
}

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
