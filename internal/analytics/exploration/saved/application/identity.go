package application

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/analytics/exploration/saved"
)

// StableExplorationID derives the retry-stable destination identity used by
// browser and REST adapters. The authenticated actor, project, idempotency
// key, and operation are all part of the domain-separated input; no authored
// title, slug, or client-supplied identifier participates in identity.
func StableExplorationID(prefix, project, actor, idempotencyKey, operation string) (string, error) {
	if strings.TrimSpace(prefix) == "" || strings.TrimSpace(project) == "" || strings.TrimSpace(actor) == "" || strings.TrimSpace(idempotencyKey) == "" || strings.TrimSpace(operation) == "" {
		return "", fmt.Errorf("%w: stable saved-exploration identity inputs are required", saved.ErrInvalid)
	}
	digest := sha256.Sum256([]byte("leapview.saved-exploration.id.v1\x00" + project + "\x00" + actor + "\x00" + idempotencyKey + "\x00" + operation))
	return prefix + hex.EncodeToString(digest[:16]), nil
}
