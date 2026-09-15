package project

import (
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
