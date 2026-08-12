package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/workspace"
	platformdb "github.com/flidai/leapview/internal/workspace/internal/db"
)

type SecurableRegistrar interface {
	UpsertSecurableObject(ctx context.Context, object access.ObjectRef, ownerPrincipalID string) (access.SecurableObject, error)
}

type Repository struct {
	db         *sql.DB
	q          *platformdb.Queries
	securables SecurableRegistrar
}

func NewRepository(sqlDB *sql.DB) *Repository {
	return &Repository{db: sqlDB, q: platformdb.New(sqlDB)}
}

func NewRepositoryWithSecurables(sqlDB *sql.DB, securables SecurableRegistrar) *Repository {
	return &Repository{db: sqlDB, q: platformdb.New(sqlDB), securables: securables}
}

func (r *Repository) Ensure(ctx context.Context, input workspace.EnsureInput) error {
	id := strings.TrimSpace(string(input.ID))
	if id == "" {
		return fmt.Errorf("workspace id is required")
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = id
	}
	if err := r.q.UpsertWorkspace(ctx, platformdb.UpsertWorkspaceParams{
		ID:          id,
		Title:       title,
		Description: input.Description,
	}); err != nil {
		return err
	}
	if r.securables == nil {
		return nil
	}
	object := access.WorkspaceObject(id)
	object.DisplayName = title
	_, err := r.securables.UpsertSecurableObject(ctx, object, "")
	return err
}

func (r *Repository) List(ctx context.Context) ([]workspace.Summary, error) {
	rows, err := r.q.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	workspaces := make([]workspace.Summary, 0, len(rows))
	for _, row := range rows {
		workspaces = append(workspaces, mapWorkspace(row))
	}
	return workspaces, nil
}

func (r *Repository) ByID(ctx context.Context, id workspace.WorkspaceID) (workspace.Summary, error) {
	row, err := r.q.GetWorkspace(ctx, string(id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return workspace.Summary{}, workspace.ErrNotFound
		}
		return workspace.Summary{}, err
	}
	return mapWorkspace(row), nil
}

func (r *Repository) ListWithActiveMetadata(ctx context.Context, environment string) ([]workspace.Summary, error) {
	rows, err := r.q.ListWorkspacesWithActiveMetadata(ctx, normalizedEnvironment(environment))
	if err != nil {
		return nil, err
	}
	out := make([]workspace.Summary, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapWorkspaceWithActiveMetadata(row.ID, queryText(row.Title), queryText(row.Description), row.ActiveServingStateID, row.CreatedAt, row.UpdatedAt))
	}
	return out, nil
}

func (r *Repository) ByIDWithActiveMetadata(ctx context.Context, id workspace.WorkspaceID, environment string) (workspace.Summary, error) {
	row, err := r.q.GetWorkspaceWithActiveMetadata(ctx, platformdb.GetWorkspaceWithActiveMetadataParams{
		Environment: normalizedEnvironment(environment),
		ID:          string(id),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return workspace.Summary{}, workspace.ErrNotFound
		}
		return workspace.Summary{}, err
	}
	return mapWorkspaceWithActiveMetadata(row.ID, queryText(row.Title), queryText(row.Description), row.ActiveServingStateID, row.CreatedAt, row.UpdatedAt), nil
}

func (r *Repository) AdministrationByID(ctx context.Context, id workspace.WorkspaceID, environment string) (workspace.AdministrationState, error) {
	environment = normalizedEnvironment(environment)
	row, err := r.q.GetWorkspaceAdministration(ctx, platformdb.GetWorkspaceAdministrationParams{
		Environment: environment,
		ID:          string(id),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return workspace.AdministrationState{}, workspace.ErrNotFound
		}
		return workspace.AdministrationState{}, err
	}
	return workspace.AdministrationState{
		Workspace: workspace.Summary{
			ID: workspace.WorkspaceID(row.ID), Title: row.Title, Description: row.Description,
			ActiveServingStateID: workspace.ServingStateID(row.ActiveServingStateID),
			CreatedAt:            row.CreatedAt, UpdatedAt: row.UpdatedAt,
		},
		Environment: environment, ActiveServingStateStatus: row.ActiveServingStateStatus,
		ActiveServingStateSince: row.ActiveServingStateSince, ProjectID: row.ProjectID,
		CurrentDeploymentID: row.CurrentDeploymentID, CurrentDeploymentStatus: row.CurrentDeploymentStatus,
		CurrentDeploymentSince: row.CurrentDeploymentSince, CurrentReleaseID: row.CurrentReleaseID,
	}, nil
}

func (r *Repository) ActiveServingStateGraph(ctx context.Context, id workspace.WorkspaceID, environment string) (workspace.AssetGraph, bool, error) {
	activeServingState, err := r.q.GetActiveServingState(ctx, platformdb.GetActiveServingStateParams{
		WorkspaceID: string(id),
		Environment: normalizedEnvironment(environment),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return workspace.AssetGraph{}, false, nil
		}
		return workspace.AssetGraph{}, false, err
	}
	assetRows, err := r.q.ListAssetsByServingState(ctx, activeServingState.ID)
	if err != nil {
		return workspace.AssetGraph{}, false, err
	}
	edgeRows, err := r.q.ListAssetEdgesByServingState(ctx, activeServingState.ID)
	if err != nil {
		return workspace.AssetGraph{}, false, err
	}
	graph := workspace.AssetGraph{
		Assets: make([]workspace.Asset, 0, len(assetRows)),
		Edges:  make([]workspace.AssetEdge, 0, len(edgeRows)),
	}
	for _, row := range assetRows {
		graph.Assets = append(graph.Assets, mapAsset(row))
	}
	for _, row := range edgeRows {
		graph.Edges = append(graph.Edges, mapAssetEdge(row))
	}
	return graph, true, nil
}

func (r *Repository) AssetVersions(ctx context.Context, workspaceID workspace.WorkspaceID, environment string, assetID workspace.AssetID) ([]workspace.AssetVersion, error) {
	rows, err := r.q.ListAssetVersions(ctx, platformdb.ListAssetVersionsParams{
		WorkspaceID:    string(workspaceID),
		Environment:    normalizedEnvironment(environment),
		LogicalAssetID: string(assetID),
	})
	if err != nil {
		return nil, err
	}
	versions := make([]workspace.AssetVersion, 0, len(rows))
	for _, row := range rows {
		version := workspace.AssetVersion{
			ServingStateID: workspace.ServingStateID(row.ServingStateID),
			WorkspaceID:    workspace.WorkspaceID(row.WorkspaceID),
			Environment:    row.Environment,
			Status:         row.Status,
			Digest:         row.Digest,
			CreatedBy:      row.CreatedBy,
			CreatedAt:      row.CreatedAt,
			SnapshotID:     workspace.AssetSnapshotID(row.SnapshotID),
			AssetID:        workspace.AssetID(row.LogicalAssetID),
			SourceFile:     row.SourceFile,
			ContentHash:    row.ContentHash,
		}
		if row.ActivatedAt.Valid {
			version.ActivatedAt = row.ActivatedAt.String
		}
		versions = append(versions, version)
	}
	return versions, nil
}

func normalizedEnvironment(environment string) string {
	if environment == "" {
		return "dev"
	}
	return environment
}

func mapWorkspace(row platformdb.Workspace) workspace.Summary {
	out := workspace.Summary{
		ID:          workspace.WorkspaceID(row.ID),
		Title:       row.Title,
		Description: row.Description,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
	return out
}

func mapWorkspaceWithActiveMetadata(id, title, description, activeServingStateID, createdAt, updatedAt string) workspace.Summary {
	return workspace.Summary{
		ID:                   workspace.WorkspaceID(id),
		Title:                title,
		Description:          description,
		ActiveServingStateID: workspace.ServingStateID(activeServingStateID),
		CreatedAt:            createdAt,
		UpdatedAt:            updatedAt,
	}
}

func queryText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return fmt.Sprint(typed)
	}
}

func mapAsset(row platformdb.Asset) workspace.Asset {
	return workspace.Asset{
		ID:             workspace.AssetID(row.LogicalAssetID),
		SnapshotID:     workspace.AssetSnapshotID(row.SnapshotID),
		WorkspaceID:    workspace.WorkspaceID(row.WorkspaceID),
		ServingStateID: workspace.ServingStateID(row.ServingStateID),
		Type:           workspace.AssetType(row.AssetType),
		Key:            row.AssetKey,
		ParentID:       workspace.AssetID(row.ParentLogicalAssetID),
		Title:          row.Title,
		Description:    row.Description,
		SourceFile:     row.SourceFile,
		PayloadSchema:  row.PayloadSchema,
		PayloadJSON:    row.PayloadJson,
		ContentHash:    row.ContentHash,
	}
}

func mapAssetEdge(row platformdb.AssetEdge) workspace.AssetEdge {
	return workspace.AssetEdge{
		ID:             workspace.AssetEdgeID(row.ID),
		WorkspaceID:    workspace.WorkspaceID(row.WorkspaceID),
		ServingStateID: workspace.ServingStateID(row.ServingStateID),
		FromAssetID:    workspace.AssetID(row.FromLogicalAssetID),
		ToAssetID:      workspace.AssetID(row.ToLogicalAssetID),
		Type:           workspace.AssetEdgeType(row.EdgeType),
	}
}
