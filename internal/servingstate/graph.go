package servingstate

import projectgraph "github.com/flidai/leapview/internal/project/graph"

// AssetGraph is the immutable browser projection read from an active serving
// generation. It intentionally contains transport-neutral fields so the
// serving-state capability does not depend on project presentation packages.
type AssetGraph struct {
	Assets []Asset
	Edges  []AssetEdge
}

type Asset struct {
	ID             projectgraph.ResourceID
	SnapshotID     string
	ProjectID      projectgraph.ResourceID
	ServingStateID ID
	Type           string
	Key            string
	ParentID       projectgraph.ResourceID
	Title          string
	Description    string
	SourceFile     string
	PayloadSchema  string
	PayloadJSON    string
	ContentHash    string
}

// AssetVersion is the durable serving-state history row for one logical
// project asset. It intentionally carries only public provenance fields used
// by the browser's versions table.
type AssetVersion struct {
	ServingStateID ID
	ProjectID      projectgraph.ResourceID
	Environment    Environment
	Status         string
	Digest         string
	CreatedBy      string
	CreatedAt      string
	ActivatedAt    string
	SnapshotID     string
	AssetID        projectgraph.ResourceID
	SourceFile     string
	ContentHash    string
}

type AssetEdge struct {
	ID             string
	ProjectID      projectgraph.ResourceID
	ServingStateID ID
	FromAssetID    projectgraph.ResourceID
	ToAssetID      projectgraph.ResourceID
	Type           string
}
