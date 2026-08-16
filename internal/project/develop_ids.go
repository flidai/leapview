package project

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

type ServingStateID string
type AssetID string
type AssetSnapshotID string
type AssetEdgeID string

func NewAssetID(typ AssetType, key string) AssetID {
	return AssetID(strings.ToLower(string(typ) + ":" + strings.TrimSpace(key)))
}

func NewAssetSnapshotID(servingStateID ServingStateID, logicalID AssetID) AssetSnapshotID {
	return AssetSnapshotID("asset_" + stableID(string(servingStateID)+"|"+string(logicalID)))
}

func NewAssetEdgeID(servingStateID ServingStateID, fromID, toID AssetID, typ AssetEdgeType) AssetEdgeID {
	return AssetEdgeID("edge_" + stableID(string(servingStateID)+"|"+string(fromID)+"|"+string(toID)+"|"+string(typ)))
}

func stableID(value string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(value)))
	return hex.EncodeToString(sum[:])[:32]
}
