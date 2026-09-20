package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/jackc/pgx/v5"
)

// RotateAPIToken issues one replacement secret and, when requested, revokes
// the previous token in the same PostgreSQL transaction. With
// RevokePrevious=false both credentials remain usable for an intentional
// overlap window.
func (r *Repository) RotateAPIToken(ctx context.Context, input access.APITokenRotationInput) (access.APITokenRotation, error) {
	if ctx == nil {
		return access.APITokenRotation{}, errors.New("api token context is nil")
	}
	if input.Token.Capabilities == nil || len(input.Token.Capabilities) == 0 {
		return access.APITokenRotation{}, errors.New("at least one explicit API token capability is required")
	}
	principalID, err := uuidID("principal id", input.PrincipalID)
	if err != nil {
		return access.APITokenRotation{}, err
	}
	previousID, err := uuidID("previous api token id", input.PreviousTokenID)
	if err != nil {
		return access.APITokenRotation{}, err
	}
	if _, err := r.requireDB(); err != nil {
		return access.APITokenRotation{}, err
	}
	parsedPrincipalID, err := pgUUID(principalID)
	if err != nil {
		return access.APITokenRotation{}, err
	}
	parsedPreviousID, err := pgUUID(previousID)
	if err != nil {
		return access.APITokenRotation{}, err
	}
	tx, owned, err := r.txOrBegin(ctx)
	if err != nil {
		return access.APITokenRotation{}, err
	}
	if owned {
		defer func() { _ = tx.Rollback(ctx) }()
	}

	principal, err := (&Repository{db: tx, fingerprintKey: r.fingerprintKey}).PrincipalByID(ctx, principalID)
	if err != nil {
		return access.APITokenRotation{}, err
	}
	if principal.Kind != access.PrincipalKindUser {
		return access.APITokenRotation{}, errors.New("personal API tokens are only available to user principals")
	}
	previousRow, err := accessdb.New(tx).LockAPITokenForRotation(ctx, accessdb.LockAPITokenForRotationParams{ID: parsedPreviousID, PrincipalID: parsedPrincipalID})
	if err != nil {
		return access.APITokenRotation{}, err
	}
	previous, err := (&Repository{db: tx, fingerprintKey: r.fingerprintKey}).apiToken(ctx, previousID)
	if err != nil {
		return access.APITokenRotation{}, err
	}
	if previousRow.RevokedAt.Valid || previous.RevokedAt != "" {
		return access.APITokenRotation{}, fmt.Errorf("previous api token is already revoked")
	}

	input.Token.PrincipalID = principalID
	secret, created, err := (&Repository{db: tx, fingerprintKey: r.fingerprintKey}).CreateAPITokenWithMetadata(ctx, input.Token)
	if err != nil {
		return access.APITokenRotation{}, err
	}
	if input.RevokePrevious {
		tag, revokeErr := accessdb.New(tx).RevokeAPITokenForPrincipal(ctx, accessdb.RevokeAPITokenForPrincipalParams{ID: parsedPreviousID, PrincipalID: parsedPrincipalID})
		if revokeErr != nil {
			return access.APITokenRotation{}, revokeErr
		}
		if tag.RowsAffected() != 1 {
			return access.APITokenRotation{}, pgx.ErrNoRows
		}
		previous.RevokedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if owned {
		if err := tx.Commit(ctx); err != nil {
			return access.APITokenRotation{}, err
		}
	}
	return access.APITokenRotation{Secret: secret, Created: created, Previous: previous}, nil
}
