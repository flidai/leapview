package module

import (
	"context"
	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/jackc/pgx/v5"
)

// RecheckCredentialAuthorityTx holds the authenticated identity and current
// attenuation stable through the application's credential mutation transaction.
func RecheckCredentialAuthorityTx(ctx context.Context, tx pgx.Tx, issuer access.GrantIssuerEvidence, pairs []access.PermissionPair) error {
	return accesspostgres.RecheckCredentialAuthorityTx(ctx, tx, issuer, pairs)
}

// LockPlatformAdministratorTx holds the current instance administrator role.
func LockPlatformAdministratorTx(ctx context.Context, tx pgx.Tx, actor string) error {
	return accesspostgres.LockPlatformAdministratorTx(ctx, tx, actor)
}

// IsPlatformAdministratorTx reads the independent instance role through the
// retained transaction. A positive result must also be locked before issuance.
func IsPlatformAdministratorTx(ctx context.Context, tx pgx.Tx, actor string) (bool, error) {
	return accesspostgres.IsPlatformAdministratorTx(ctx, tx, actor)
}

// LockCredentialAuthorizationSubjectsTx holds the current principal and group
// memberships used by the application's canonical serving-policy check.
func LockCredentialAuthorizationSubjectsTx(ctx context.Context, tx pgx.Tx, actor string) ([]access.SubjectRef, error) {
	return accesspostgres.LockCredentialAuthorizationSubjectsTx(ctx, tx, actor)
}
