package postgres

import (
	"context"

	agentdb "github.com/flidai/leapview/internal/agent/postgres/internal/db"
)

// CredentialVersionReferencesTx includes every retained configuration because
// interrupted and resumable agent runs select exact historical revisions.
func (r *Repository) CredentialVersionReferencesTx(ctx context.Context, tx Tx, version string) ([]int64, error) {
	return agentdb.New(tx).CredentialVersionReferences(ctx, version)
}
