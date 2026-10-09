package accesspostgres

import (
	"context"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/jackc/pgx/v5"
)

// LockedCurrentAuthorizationPolicyTx is a composition read port. It holds the
// exact current head through a caller's bounded credential transaction, while
// allowing reviewer nomination to advance the policy before that transaction.
func LockedCurrentAuthorizationPolicyTx(ctx context.Context, tx pgx.Tx, scope access.AuthorizationPolicyScope) (access.AuthorizationPolicy, error) {
	policy, err := accesspostgres.AuthorizationPolicyTx(ctx, tx, scope)
	if err != nil {
		return access.AuthorizationPolicy{}, err
	}
	return accesspostgres.ValidateAuthorizationPolicyRevisionTx(ctx, tx, scope, policy.Revision, policy.Digest)
}
