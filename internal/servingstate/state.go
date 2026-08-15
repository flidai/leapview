package servingstate

import (
	"errors"
	"fmt"
	"time"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var ErrSnapshotLeaseLost = errors.New("snapshot lease is no longer active")

var ErrNotFound = errors.New("serving state not found")

var ErrActivationConflict = errors.New("serving-state activation compare-and-swap conflict")

type ID string

type Environment string

type ActiveScope struct {
	ProjectID   projectgraph.ResourceID
	Environment Environment
}

type Status string

const (
	StatusPending         Status = "pending"
	StatusValidated       Status = "validated"
	StatusActive          Status = "active"
	StatusDraining        Status = "draining"
	StatusInactive        Status = "inactive"
	StatusFailed          Status = "failed"
	StatusExpired         Status = "expired"
	StatusDeleteScheduled Status = "delete_scheduled"
	StatusDeleted         Status = "deleted"
)

const DefaultEnvironment Environment = "dev"

type Source string

const (
	SourcePublish   Source = "publish"
	SourceRefresh   Source = "refresh"
	SourceCandidate Source = "candidate"
)

type State struct {
	ID                        ID
	ProjectID                 projectgraph.ResourceID
	ProjectDigest             string
	AccessPolicyJSON          string
	DashboardPublicationsJSON string
	DashboardAppearancesJSON  string
	Environment               Environment
	Status                    Status
	Source                    Source
	Digest                    string
	ManifestJSON              string
	CreatedBy                 string
	CreatedAt                 string
	ActivatedAt               string
	SupersededAt              string
	Error                     string
	DuckLakeSnapshotID        int64
}

func (d State) CanActivate() bool {
	return d.Status == StatusValidated || d.Status == StatusInactive || d.Status == StatusActive
}

type CreateInput struct {
	ProjectID   projectgraph.ResourceID
	Environment Environment
	CreatedBy   string
	Source      Source
}

type Artifact struct {
	ID             string
	ServingStateID ID
	Digest         string
	Format         string
	Path           string
	ManifestJSON   string
	SizeBytes      int64
	CreatedAt      string
}

type SnapshotLeaseInput struct {
	ServingStateID     ID
	DuckLakeSnapshotID int64
	OwnerID            string
	ExpiresAt          time.Time
}

type Validation struct {
	Digest                    string
	ManifestJSON              string
	RootDir                   string
	ProjectID                 projectgraph.ResourceID
	ProjectDigest             string
	AccessPolicy              accesssnapshot.AccessPolicy
	DashboardPublicationsJSON string
	DashboardAppearancesJSON  string
	ManagedDataRevisions      map[string]string
	Graph                     projectgraph.ProjectGraph
}

type PreparedRuntime interface {
	Close() error
}

func NormalizeEnvironment(value Environment) Environment {
	if value == "" {
		return DefaultEnvironment
	}
	return value
}

// ValidateEnvironment checks the canonical serving-scope environment grammar.
// Callers that accept an omitted environment should normalize it first; query
// paths should reject an omitted scope instead of silently selecting dev.
func ValidateEnvironment(value Environment) error {
	if value == "" {
		return fmt.Errorf("environment is required")
	}
	if _, err := projectgraph.NewServingIdentity(projectgraph.ResourceID("project"), string(value), "generation"); err != nil {
		return fmt.Errorf("invalid environment %q: %w", value, err)
	}
	return nil
}

func NormalizeSource(value Source) Source {
	if value == "" {
		return SourcePublish
	}
	return value
}
