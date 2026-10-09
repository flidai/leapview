package app

import (
	"context"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/jackc/pgx/v5"
)

// newAgentCredentialAuthority requires both current platform administration and
// the authenticated credential's current attenuation at the mutation boundary.
// The credential coordinator already holds the installation's target fence.
func newAgentCredentialAuthority(instanceID string) func(context.Context, pgx.Tx, string, access.PermissionPair) error {
	expected, invalid := access.NewInstancePermissionPair(access.ActionPlatformSettingsUpdate, instanceID)
	return func(ctx context.Context, tx pgx.Tx, actor string, pair access.PermissionPair) error {
		if invalid != nil || pair != expected {
			return access.ErrForbidden
		}
		issuer, err := accessmodule.CredentialTransactionEvidence(ctx, actor)
		if err != nil {
			return access.ErrForbidden
		}
		if err = accessmodule.RecheckCredentialAuthorityTx(ctx, tx, issuer, []access.PermissionPair{pair}); err != nil {
			return access.ErrForbidden
		}
		return accessmodule.LockPlatformAdministratorTx(ctx, tx, actor)
	}
}
