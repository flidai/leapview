package app

import (
	"crypto/sha256"
	"errors"
	"strings"
)

const accessFingerprintPurpose = "leapview:access-fingerprint:v1:"

// postgresFingerprintKey selects the dedicated token hash secret when one is
// configured and otherwise uses the CSRF secret as the documented fallback.
// The selected material is validated before deriving a fixed-size,
// purpose-separated key for the PostgreSQL access repository.
func postgresFingerprintKey(tokenHashKey, csrfKey string) ([]byte, error) {
	selected := strings.TrimSpace(tokenHashKey)
	if selected == "" {
		selected = strings.TrimSpace(csrfKey)
	}
	if len([]byte(selected)) < 32 {
		return nil, errors.New("access fingerprint key must be at least 32 bytes")
	}
	hash := sha256.Sum256([]byte(accessFingerprintPurpose + selected))
	return hash[:], nil
}
