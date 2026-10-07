package application

import "github.com/flidai/leapview/internal/analytics/exploration/saved"

// StableExplorationID derives the retry-stable destination identity used by
// browser and REST adapters. The authenticated actor, project, idempotency
// key, and operation are all part of the domain-separated input; no authored
// title, slug, or client-supplied identifier participates in identity.
func StableExplorationID(prefix, project, actor, idempotencyKey, operation string) (string, error) {
	return saved.StableExplorationID(prefix, project, actor, idempotencyKey, operation)
}
