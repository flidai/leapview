package cursorsigning

import (
	"crypto/sha256"
	"encoding/hex"
)

// These fingerprints identify publicly exposed canonical HMAC-SHA256 key
// blocks, independently of key IDs or equivalent zero-padded encodings.
// Never put the exposed secret itself in this repository.
// This registry is immutable after initialization.
var revokedKeyDigests = map[string]bool{
	// Development SQLite database committed on 2026-07-22 at 1b48c00c4.
	"f2f3ac41f231007dc97df714e15de1ee5310ad75082ed88314976f5240e2bca2": true,
}

func isRevokedKey(key []byte) bool {
	return revokedKeyDigests[keyFingerprint(key)]
}

func keyFingerprint(key []byte) string {
	var block [sha256.BlockSize]byte
	if len(key) > len(block) {
		digest := sha256.Sum256(key)
		copy(block[:], digest[:])
	} else {
		copy(block[:], key)
	}
	digest := sha256.Sum256(block[:])
	return hex.EncodeToString(digest[:])
}
