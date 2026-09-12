package project

import (
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type Asset struct {
	ID             AssetID
	SnapshotID     AssetSnapshotID
	ProjectID      projectgraph.ResourceID
	ServingStateID ServingStateID
	Type           AssetType
	Key            string
	ParentID       AssetID
	Title          string
	Description    string
	SourceFile     string `json:"sourceFile,omitempty"`
	PayloadSchema  string
	PayloadJSON    string
	ContentHash    string
}

type AssetEdge struct {
	ID             AssetEdgeID
	ProjectID      projectgraph.ResourceID
	ServingStateID ServingStateID
	FromAssetID    AssetID
	ToAssetID      AssetID
	Type           AssetEdgeType
}

type DevelopAssetGraph struct {
	Assets []Asset
	Edges  []AssetEdge
}
