package access

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func grantDigest(value any) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}
