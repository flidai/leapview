package workspace

import (
	"context"
	"errors"
)

var ErrNotFound = errors.New("workspace not found")

type Summary struct {
	ID                   WorkspaceID
	Title                string
	Description          string
	ActiveServingStateID ServingStateID
	CreatedAt            string
	UpdatedAt            string
}

// AdministrationState is the workspace-owned operational projection used by
// administration clients. Deployments and releases remain owned by their
// respective domains; this projection only identifies the currently active
// resources so clients can follow their canonical API links.
type AdministrationState struct {
	Workspace                Summary
	Environment              string
	ActiveServingStateStatus string
	ActiveServingStateSince  string
	ProjectID                string
	CurrentDeploymentID      string
	CurrentDeploymentStatus  string
	CurrentDeploymentSince   string
	CurrentReleaseID         string
}

type AdministrationReadModel interface {
	AdministrationByID(ctx context.Context, id WorkspaceID, environment string) (AdministrationState, error)
}

type AssetVersion struct {
	ServingStateID ServingStateID
	WorkspaceID    WorkspaceID
	Environment    string
	Status         string
	Digest         string
	CreatedBy      string
	CreatedAt      string
	ActivatedAt    string
	SnapshotID     AssetSnapshotID
	AssetID        AssetID
	SourceFile     string
	ContentHash    string
}

type EnsureInput struct {
	ID          WorkspaceID
	Title       string
	Description string
}

type ReadModel interface {
	List(ctx context.Context) ([]Summary, error)
	ByID(ctx context.Context, id WorkspaceID) (Summary, error)
	ActiveServingStateGraph(ctx context.Context, id WorkspaceID, environment string) (AssetGraph, bool, error)
	AssetVersions(ctx context.Context, workspaceID WorkspaceID, environment string, assetID AssetID) ([]AssetVersion, error)
}

type Repository interface {
	ReadModel
	Ensure(ctx context.Context, input EnsureInput) error
}
