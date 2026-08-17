package project

import (
	"context"
	"encoding/json"
	"fmt"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type DevelopCatalog struct {
	Assets []DevelopAssetRecord
	Edges  []DevelopEdgeRecord
}

type DevelopAssetRecord struct {
	ID             AssetID
	SnapshotID     AssetSnapshotID
	ProjectID      projectgraph.ResourceID
	ServingStateID ServingStateID
	Type           AssetType
	Key            string
	ParentID       AssetID
	Title          string
	Description    string
	SourceFile     string
	PayloadSchema  string
	Payload        map[string]any
	ContentHash    string
}

type DevelopEdgeRecord struct {
	ID             AssetEdgeID
	ProjectID      projectgraph.ResourceID
	ServingStateID ServingStateID
	FromAssetID    AssetID
	ToAssetID      AssetID
	Type           AssetEdgeType
}

// DevelopGraphReader exposes the active serving graph for the server-bound
// project.  It is intentionally a narrow canonical read port and does not
// reintroduce the removed container repository/read-model abstraction.
type DevelopGraphReader interface {
	ActiveServingStateGraph(context.Context, projectgraph.ResourceID, string) (DevelopAssetGraph, bool, error)
}

type DevelopCatalogService struct {
	repo DevelopGraphReader
}

func NewDevelopCatalogService(repo DevelopGraphReader) *DevelopCatalogService {
	return &DevelopCatalogService{repo: repo}
}

type DevelopCatalogReader interface {
	ActiveAssetCatalog(ctx context.Context, id projectgraph.ResourceID, environment string) (DevelopCatalog, bool, error)
}

func (s *DevelopCatalogService) ActiveAssetCatalog(ctx context.Context, id projectgraph.ResourceID, environment string) (DevelopCatalog, bool, error) {
	if s == nil {
		return DevelopCatalog{}, false, nil
	}
	if s.repo == nil {
		return DevelopCatalog{}, false, nil
	}
	graph, ok, err := s.repo.ActiveServingStateGraph(ctx, id, environment)
	if err != nil {
		return DevelopCatalog{}, false, err
	}
	if !ok {
		return DevelopCatalog{}, false, nil
	}
	catalog, err := DecodeDevelopCatalog(graph)
	return catalog, true, err
}

func DecodeDevelopCatalog(graph DevelopAssetGraph) (DevelopCatalog, error) {
	catalog := DevelopCatalog{
		Assets: make([]DevelopAssetRecord, 0, len(graph.Assets)),
		Edges:  make([]DevelopEdgeRecord, 0, len(graph.Edges)),
	}
	for _, asset := range graph.Assets {
		payload := map[string]any{}
		if asset.PayloadJSON != "" {
			if err := json.Unmarshal([]byte(asset.PayloadJSON), &payload); err != nil {
				return DevelopCatalog{}, fmt.Errorf("decode asset %s payload: %w", asset.ID, err)
			}
		}
		catalog.Assets = append(catalog.Assets, DevelopAssetRecord{
			ID:             asset.ID,
			SnapshotID:     asset.SnapshotID,
			ProjectID:      asset.ProjectID,
			ServingStateID: asset.ServingStateID,
			Type:           asset.Type,
			Key:            asset.Key,
			ParentID:       asset.ParentID,
			Title:          asset.Title,
			Description:    asset.Description,
			SourceFile:     asset.SourceFile,
			PayloadSchema:  asset.PayloadSchema,
			Payload:        payload,
			ContentHash:    asset.ContentHash,
		})
	}
	for _, edge := range graph.Edges {
		catalog.Edges = append(catalog.Edges, DevelopEdgeRecord{
			ID:             edge.ID,
			ProjectID:      edge.ProjectID,
			ServingStateID: edge.ServingStateID,
			FromAssetID:    edge.FromAssetID,
			ToAssetID:      edge.ToAssetID,
			Type:           edge.Type,
		})
	}
	return catalog, nil
}
