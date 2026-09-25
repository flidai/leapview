package developmentprofile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// CatalogDigest identifies the logical connection contract that a local
// runtime's profile can safely bind. Dashboard and model edits do not change
// it; adding or changing a connection requires a new local runtime profile.
func CatalogDigest(catalog map[string]LogicalConnection) (string, error) {
	if catalog == nil {
		return "", errors.New("development connection catalog is unavailable")
	}
	for name, connection := range catalog {
		if name == "" || !connection.ID.Valid() || connection.ConnectorKind == "" {
			return "", errors.New("development connection catalog is invalid")
		}
	}
	encoded, err := json.Marshal(struct {
		Version     int                          `json:"version"`
		Connections map[string]LogicalConnection `json:"connections"`
	}{Version: 1, Connections: catalog})
	if err != nil {
		return "", errors.New("encode development connection catalog")
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
