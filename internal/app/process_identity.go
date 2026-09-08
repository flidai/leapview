package app

import (
	"fmt"

	"github.com/google/uuid"
)

// newProcessNodeID returns one opaque incarnation identity for a production
// Build. It is intentionally not durable: every process restart and every
// independent node gets a different owner, so durable fences cannot treat
// those workers as the same re-entrant owner. Target identity remains the
// durable instance ID supplied by PostgreSQL.
func newProcessNodeID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate process node identity: %w", err)
	}
	if id == uuid.Nil {
		return "", fmt.Errorf("generate process node identity: nil UUID")
	}
	return id.String(), nil
}
