package postgres

import (
	"context"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/jackc/pgx/v5"
)

// RecheckCredentialAuthorityTx locks the current principal and authenticated
// credential until the caller's transaction ends, then reads the token's exact
// typed ceiling. It grants no principal/resource authority itself. The caller
// must establish that authority and hold its target fence in the same transaction.
func RecheckCredentialAuthorityTx(ctx context.Context, tx pgx.Tx, issuer access.GrantIssuerEvidence, requested []access.PermissionPair) error {
	if ctx == nil || typednil.IsNil(tx) || issuer.Validate() != nil || len(requested) == 0 || access.ValidatePermissionPairs(requested) != nil {
		return access.ErrForbidden
	}
	principal, err := pgUUID(issuer.PrincipalID)
	if err != nil || !principal.Valid {
		return access.ErrForbidden
	}
	if _, err = accessdb.New(tx).LockCurrentCredentialPrincipal(ctx, principal); err != nil {
		return access.ErrForbidden
	}
	repository := &Repository{db: tx}
	if err = repository.checkCredentialEvidence(ctx, tx, issuer); err != nil {
		return access.ErrForbidden
	}
	if err = repository.checkCredentialPermissionCeiling(ctx, tx, issuer, requested); err != nil {
		return access.ErrForbidden
	}
	return nil
}

// LockPlatformAdministratorTx binds the durable instance-wide role and active
// principal to the caller's commit boundary. An earlier request snapshot cannot
// substitute for this check; concurrent role revocation waits for this lock.
func LockPlatformAdministratorTx(ctx context.Context, tx pgx.Tx, actor string) error {
	if ctx == nil || typednil.IsNil(tx) {
		return access.ErrForbidden
	}
	principal, err := pgUUID(actor)
	if err != nil || !principal.Valid {
		return access.ErrForbidden
	}
	if _, err = accessdb.New(tx).LockCurrentPlatformAdministrator(ctx, principal); err != nil {
		return access.ErrForbidden
	}
	return nil
}
