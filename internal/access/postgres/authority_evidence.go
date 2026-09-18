package postgres

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/jackc/pgx/v5"
)

// CredentialForSessionToken resolves browser authentication into non-secret
// session evidence. The bearer cookie is verified here and never returned to
// callers or persisted in an async job envelope.
func (r *Repository) CredentialForSessionToken(ctx context.Context, token string) (access.Session, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.Session{}, err
	}
	fingerprint := r.secretFingerprint(token)
	row, err := accessdb.New(db).FindBrowserSession(ctx, fingerprint)
	if err != nil {
		return access.Session{}, err
	}
	if !hmac.Equal(row.TokenFingerprint, fingerprint) || !verifySecret(token, row.Verifier) {
		return access.Session{}, pgx.ErrNoRows
	}
	_ = accessdb.New(db).TouchBrowserSession(ctx, row.TokenFingerprint)
	return access.Session{
		ID: principalUUID(row.SessionID), PrincipalID: principalUUID(row.ID), Kind: access.SessionKindBrowser,
		TokenFingerprint: hex.EncodeToString(row.TokenFingerprint), ExpiresAt: principalTimestamp(row.ExpiresAt),
	}, nil
}

// SessionAuthorityEvidence revalidates a browser session by its durable ID
// and captured fingerprint. It deliberately has no bearer-token parameter;
// revocation/expiry is checked against PostgreSQL at dequeue time.
func (r *Repository) SessionAuthorityEvidence(ctx context.Context, principalID, sessionID, fingerprint string, now time.Time) (access.Session, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.Session{}, err
	}
	principalID, err = uuidID("principal id", principalID)
	if err != nil {
		return access.Session{}, access.ErrForbidden
	}
	sessionID, err = uuidID("session id", sessionID)
	if err != nil {
		return access.Session{}, access.ErrForbidden
	}
	fingerprintBytes, err := hex.DecodeString(strings.TrimSpace(fingerprint))
	if err != nil || len(fingerprintBytes) != sha256.Size {
		return access.Session{}, access.ErrForbidden
	}
	row, err := accessdb.New(db).FindBrowserSession(ctx, fingerprintBytes)
	if err != nil {
		return access.Session{}, err
	}
	if principalUUID(row.ID) != principalID || principalUUID(row.SessionID) != sessionID || !hmac.Equal(row.TokenFingerprint, fingerprintBytes) {
		return access.Session{}, access.ErrForbidden
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if !row.ExpiresAt.Valid || !row.ExpiresAt.Time.After(now.UTC()) {
		return access.Session{}, access.ErrForbidden
	}
	return access.Session{
		ID: sessionID, PrincipalID: principalID, Kind: access.SessionKindBrowser,
		TokenFingerprint: hex.EncodeToString(row.TokenFingerprint), ExpiresAt: principalTimestamp(row.ExpiresAt),
	}, nil
}

var _ access.SessionAuthorityEvidenceReader = (*Repository)(nil)
