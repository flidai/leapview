package postgres

import (
	"context"
	"crypto/hmac"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) PrincipalForToken(ctx context.Context, token string) (access.Principal, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.Principal{}, err
	}
	row, err := accessdb.New(db).FindBrowserSession(ctx, r.secretFingerprint(token))
	if err != nil {
		return access.Principal{}, err
	}
	if !hmac.Equal(row.TokenFingerprint, r.secretFingerprint(token)) || !verifySecret(token, row.Verifier) {
		return access.Principal{}, pgx.ErrNoRows
	}
	_ = accessdb.New(db).TouchBrowserSession(ctx, row.TokenFingerprint)
	return r.PrincipalByID(ctx, principalUUID(row.ID))
}

// CredentialForSessionToken resolves the complete server-owned session behind
// a session cookie. Callers use the returned identity and timestamps to bind
// privileged browser actions to the authenticated actor and evaluate recent
// interactive authentication. The HMAC lookup is followed by verification of
// the durable Argon2 secret, and disabled principals are excluded in SQL.
func (r *Repository) CredentialForSessionToken(ctx context.Context, token string) (access.Session, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.Session{}, err
	}
	fingerprint := r.secretFingerprint(token)
	row, err := accessdb.New(db).FindBrowserSessionCredential(ctx, fingerprint)
	if err != nil {
		return access.Session{}, err
	}
	if !hmac.Equal(row.TokenFingerprint, fingerprint) || !verifySecret(token, row.Verifier) {
		return access.Session{}, pgx.ErrNoRows
	}
	// Activity is a projection and must not turn a valid credential lookup into
	// an authentication failure if the best-effort touch cannot be recorded.
	_ = accessdb.New(db).TouchBrowserSession(ctx, row.TokenFingerprint)
	return access.Session{
		ID: principalUUID(row.ID), PrincipalID: principalUUID(row.PrincipalID),
		Kind: access.SessionKind(row.Kind), InstanceID: row.InstanceID,
		ProfileID: row.ProfileID, ClientID: row.ClientID,
		ExpiresAt:         principalTimestamp(row.ExpiresAt),
		AbsoluteExpiresAt: principalTimestamp(row.AbsoluteExpiresAt),
		CreatedAt:         principalTimestamp(row.CreatedAt), LastSeenAt: principalTimestamp(row.LastSeenAt),
		RevokedAt: principalTimestamp(row.RevokedAt),
	}, nil
}
