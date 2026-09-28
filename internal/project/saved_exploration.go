package project

import (
	"context"
	"errors"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var (
	ErrSavedExplorationInvalid  = errors.New("invalid saved exploration")
	ErrSavedExplorationNotFound = errors.New("saved exploration not found")
)

// SavedExplorationScope is derived by the authenticated browser handler. The
// request never chooses its project, environment, or owner.
type SavedExplorationScope struct {
	ProjectID   projectgraph.ResourceID
	Environment string
	PrincipalID string
}

// SavedExplorationRecord contains only the durable query command and title;
// result rows and rendered artifacts are deliberately not persisted.
type SavedExplorationRecord struct {
	ID          string
	Title       string
	CommandJSON string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// SavedExplorationStore is the private, server-backed Data Explorer store.
type SavedExplorationStore interface {
	CreateSavedExploration(context.Context, SavedExplorationScope, string, string, string) (SavedExplorationRecord, error)
	ListSavedExplorations(context.Context, SavedExplorationScope) ([]SavedExplorationRecord, error)
	GetSavedExploration(context.Context, SavedExplorationScope, string) (SavedExplorationRecord, error)
}
