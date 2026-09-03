package project

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

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

type AssetHashInput struct {
	Type          AssetType
	Key           string
	ParentID      AssetID
	Title         string
	Description   string
	PayloadSchema string
	PayloadJSON   json.RawMessage
}

func NewAsset(projectID projectgraph.ResourceID, servingStateID ServingStateID, typ AssetType, key string, parentID AssetID, title, description, payloadSchema string, payload any) (Asset, error) {
	return NewAssetWithSourceFile(projectID, servingStateID, typ, key, parentID, title, description, "", payloadSchema, payload)
}

func NewAssetWithSourceFile(projectID projectgraph.ResourceID, servingStateID ServingStateID, typ AssetType, key string, parentID AssetID, title, description, sourceFile, payloadSchema string, payload any) (Asset, error) {
	if err := validatePayloadSchema(typ, payloadSchema); err != nil {
		return Asset{}, err
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return Asset{}, err
	}
	id := NewAssetID(typ, key)
	contentHash, err := AssetContentHash(AssetHashInput{
		Type:          typ,
		Key:           key,
		ParentID:      parentID,
		Title:         title,
		Description:   description,
		PayloadSchema: payloadSchema,
		PayloadJSON:   json.RawMessage(payloadBytes),
	})
	if err != nil {
		return Asset{}, err
	}
	return Asset{
		ID:             id,
		SnapshotID:     NewAssetSnapshotID(servingStateID, id),
		ProjectID:      projectID,
		ServingStateID: servingStateID,
		Type:           typ,
		Key:            key,
		ParentID:       parentID,
		Title:          title,
		Description:    description,
		SourceFile:     sourceFile,
		PayloadSchema:  payloadSchema,
		PayloadJSON:    string(payloadBytes),
		ContentHash:    contentHash,
	}, nil
}

func AssetContentHash(input AssetHashInput) (string, error) {
	hashBytes, err := json.Marshal(assetHashPayload{
		Type:          input.Type,
		Key:           input.Key,
		ParentID:      input.ParentID,
		Title:         input.Title,
		Description:   input.Description,
		PayloadSchema: input.PayloadSchema,
		PayloadJSON:   input.PayloadJSON,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(hashBytes)
	return hex.EncodeToString(sum[:]), nil
}

type assetHashPayload struct {
	Type          AssetType       `json:"type"`
	Key           string          `json:"key"`
	ParentID      AssetID         `json:"parentId,omitempty"`
	Title         string          `json:"title"`
	Description   string          `json:"description"`
	PayloadSchema string          `json:"payloadSchema"`
	PayloadJSON   json.RawMessage `json:"payload"`
}

func NewAssetEdge(projectID projectgraph.ResourceID, servingStateID ServingStateID, fromID, toID AssetID, typ AssetEdgeType) AssetEdge {
	return AssetEdge{
		ID:             NewAssetEdgeID(servingStateID, fromID, toID, typ),
		ProjectID:      projectID,
		ServingStateID: servingStateID,
		FromAssetID:    fromID,
		ToAssetID:      toID,
		Type:           typ,
	}
}

func ValidateAssetGraphForServingState(graph DevelopAssetGraph, projectID projectgraph.ResourceID, servingStateID ServingStateID) error {
	assetIDs := make(map[AssetID]struct{}, len(graph.Assets))
	for _, asset := range graph.Assets {
		if asset.ID == "" {
			return fmt.Errorf("asset logical id is required")
		}
		if _, ok := assetIDs[asset.ID]; ok {
			return fmt.Errorf("asset %s is duplicated", asset.ID)
		}
		assetIDs[asset.ID] = struct{}{}
		if asset.ProjectID != projectID {
			return fmt.Errorf("asset %s project = %q, want %q", asset.ID, asset.ProjectID, projectID)
		}
		if asset.ServingStateID != servingStateID {
			return fmt.Errorf("asset %s serving state = %q, want %q", asset.ID, asset.ServingStateID, servingStateID)
		}
		if want := NewAssetSnapshotID(servingStateID, asset.ID); asset.SnapshotID != want {
			return fmt.Errorf("asset %s snapshot id = %q, want %q", asset.ID, asset.SnapshotID, want)
		}
		if err := validatePayloadSchema(asset.Type, asset.PayloadSchema); err != nil {
			return fmt.Errorf("asset %s: %w", asset.ID, err)
		}
		if asset.SourceFile == "" {
			return fmt.Errorf("asset %s source file is required", asset.ID)
		}
	}
	for _, asset := range graph.Assets {
		if asset.ParentID == "" {
			continue
		}
		if _, ok := assetIDs[asset.ParentID]; !ok {
			return fmt.Errorf("asset %s parent %s is not in graph", asset.ID, asset.ParentID)
		}
	}

	edgeKeys := make(map[assetEdgeKey]struct{}, len(graph.Edges))
	for _, edge := range graph.Edges {
		if edge.ProjectID != projectID {
			return fmt.Errorf("asset edge %s project = %q, want %q", edge.ID, edge.ProjectID, projectID)
		}
		if edge.ServingStateID != servingStateID {
			return fmt.Errorf("asset edge %s serving state = %q, want %q", edge.ID, edge.ServingStateID, servingStateID)
		}
		if want := NewAssetEdgeID(servingStateID, edge.FromAssetID, edge.ToAssetID, edge.Type); edge.ID != want {
			return fmt.Errorf("asset edge %s id = %q, want %q", edge.Type, edge.ID, want)
		}
		if _, ok := assetIDs[edge.FromAssetID]; !ok {
			return fmt.Errorf("asset edge %s from asset %s is not in graph", edge.ID, edge.FromAssetID)
		}
		if _, ok := assetIDs[edge.ToAssetID]; !ok {
			return fmt.Errorf("asset edge %s to asset %s is not in graph", edge.ID, edge.ToAssetID)
		}
		key := assetEdgeKey{from: edge.FromAssetID, to: edge.ToAssetID, typ: edge.Type}
		if _, ok := edgeKeys[key]; ok {
			return fmt.Errorf("asset edge %s -> %s (%s) is duplicated", edge.FromAssetID, edge.ToAssetID, edge.Type)
		}
		edgeKeys[key] = struct{}{}
	}
	return nil
}

type assetEdgeKey struct {
	from AssetID
	to   AssetID
	typ  AssetEdgeType
}

func validatePayloadSchema(typ AssetType, schema string) error {
	want := PayloadSchemaForAssetType(typ)
	if want == "" {
		return fmt.Errorf("asset %s payload schema is not registered", typ)
	}
	if schema == "" {
		return fmt.Errorf("asset %s payload schema is required", typ)
	}
	if schema != want {
		return fmt.Errorf("asset %s payload schema = %q, want %q", typ, schema, want)
	}
	return nil
}

func PayloadSchemaForAssetType(typ AssetType) string {
	switch typ {
	case AssetTypeCatalog:
		return "catalog.v1"
	case AssetTypeConnection:
		return "connection.v1"
	case AssetTypeSource:
		return "source.v1"
	case AssetTypeModel:
		return "model.v1"
	case AssetTypeSemanticModel:
		return "semantic_model.v1"
	case AssetTypeField:
		return "field.v1"
	case AssetTypeMetric:
		return "metric.v1"
	case AssetTypeRelationship:
		return "relationship.v1"
	case AssetTypeDashboard:
		return "dashboard.v1"
	case AssetTypePage:
		return "page.v1"
	case AssetTypePageItem:
		return "page_item.v1"
	case AssetTypeFilter:
		return "filter.v2"
	case AssetTypeVisual:
		return "visual.v1"
	case AssetTypeRefreshPipeline:
		return "refresh_pipeline.v1"
	default:
		return ""
	}
}
