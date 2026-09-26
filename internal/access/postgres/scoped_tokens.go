package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/jackc/pgx/v5"
)

func permissionsJSON(permissions []access.PermissionPair) ([]byte, error) {
	return access.EncodePermissionPairs(permissions)
}

func (r *Repository) CreateScopedAPITokenWithMetadata(ctx context.Context, in access.ScopedAPITokenInput) (string, access.APIToken, error) {
	db, err := r.requireDB()
	if err != nil {
		return "", access.APIToken{}, err
	}
	pid, err := uuidID("principal id", in.PrincipalID)
	if err != nil {
		return "", access.APIToken{}, err
	}
	name, err := bounded(strings.TrimSpace(in.Name), "token name", 255)
	if err != nil {
		return "", access.APIToken{}, err
	}
	description := strings.TrimSpace(in.Description)
	if len(description) > 1024 {
		return "", access.APIToken{}, fmt.Errorf("token description must not exceed 1024 bytes")
	}
	permissions, err := permissionsJSON(in.Permissions)
	if err != nil {
		return "", access.APIToken{}, err
	}
	if in.ExpiresAt.IsZero() {
		in.ExpiresAt = time.Now().UTC().Add(defaultAPITokenTTL)
	}
	tok, err := tokenSecret("lv_pat_")
	if err != nil {
		return "", access.APIToken{}, err
	}
	ver, err := secretVerifier(tok)
	if err != nil {
		return "", access.APIToken{}, err
	}
	id, err := newUUID()
	if err != nil {
		return "", access.APIToken{}, err
	}
	tokenID, err := pgUUID(id)
	if err != nil {
		return "", access.APIToken{}, err
	}
	principalID, err := pgUUID(pid)
	if err != nil {
		return "", access.APIToken{}, err
	}
	profile := access.PermissionCatalogProfile
	tag, err := accessdb.New(db).CreateScopedAPIToken(ctx, accessdb.CreateScopedAPITokenParams{
		ID: tokenID, PrincipalID: principalID, Name: name, Description: description,
		TokenFingerprint: r.secretFingerprint(tok), Verifier: ver,
		PermissionProfile: &profile, Permissions: permissions, ExpiresAt: pgTimestamp(in.ExpiresAt),
	})
	if err != nil {
		return "", access.APIToken{}, err
	}
	if tag.RowsAffected() == 0 {
		valid, checkErr := databaseExpiryValid(ctx, db, in.ExpiresAt)
		if checkErr != nil {
			return "", access.APIToken{}, checkErr
		}
		if !valid {
			return "", access.APIToken{}, fmt.Errorf("api token expiry is invalid")
		}
		return "", access.APIToken{}, pgx.ErrNoRows
	}
	row, err := r.apiToken(ctx, id)
	return tok, row, err
}

func (r *Repository) UpdateScopedAPITokenForPrincipal(ctx context.Context, in access.ScopedAPITokenUpdate) (access.APIToken, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.APIToken{}, err
	}
	pid, err := uuidID("principal id", in.PrincipalID)
	if err != nil {
		return access.APIToken{}, err
	}
	id, err := uuidID("api token id", in.TokenID)
	if err != nil {
		return access.APIToken{}, err
	}
	name, err := bounded(strings.TrimSpace(in.Name), "token name", 200)
	if err != nil {
		return access.APIToken{}, err
	}
	description := strings.TrimSpace(in.Description)
	if len(description) > 1024 {
		return access.APIToken{}, fmt.Errorf("token description must not exceed 1024 bytes")
	}
	permissions, err := permissionsJSON(in.Permissions)
	if err != nil {
		return access.APIToken{}, err
	}
	if in.ExpectedModifiedAt.IsZero() || in.ExpiresAt.IsZero() {
		return access.APIToken{}, fmt.Errorf("token modification and expiry are required")
	}
	tokenID, err := pgUUID(id)
	if err != nil {
		return access.APIToken{}, err
	}
	principalID, err := pgUUID(pid)
	if err != nil {
		return access.APIToken{}, err
	}
	bootstrap, err := accessdb.New(db).HasInitialPublisherOrigin(ctx, tokenID)
	if err != nil {
		return access.APIToken{}, err
	}
	if bootstrap {
		return access.APIToken{}, fmt.Errorf("%w: initial publisher credentials cannot be edited; exchange the live claim or create an ordinary token", access.ErrForbidden)
	}
	tag, err := accessdb.New(db).UpdateScopedAPITokenForPrincipal(ctx, accessdb.UpdateScopedAPITokenForPrincipalParams{
		ID: tokenID, PrincipalID: principalID, Name: name, Description: description,
		Permissions: permissions, ExpiresAt: pgTimestamp(in.ExpiresAt), ExpectedModifiedAt: pgTimestamp(in.ExpectedModifiedAt),
	})
	if err != nil {
		return access.APIToken{}, err
	}
	if tag.RowsAffected() == 0 {
		return access.APIToken{}, pgx.ErrNoRows
	}
	return r.apiToken(ctx, id)
}

// RotateScopedAPITokenForPrincipal replaces the bearer identity atomically.
// The old credential is revoked only after the replacement has been created,
// and any failure rolls both writes back.
func (r *Repository) RotateScopedAPITokenForPrincipal(ctx context.Context, in access.ScopedAPITokenRotation) (string, access.APIToken, error) {
	if in.ExpectedModifiedAt.IsZero() {
		return "", access.APIToken{}, fmt.Errorf("token modification time is required")
	}
	principalID, err := uuidID("principal id", in.PrincipalID)
	if err != nil {
		return "", access.APIToken{}, err
	}
	tokenID, err := uuidID("api token id", in.TokenID)
	if err != nil {
		return "", access.APIToken{}, err
	}
	tx, own, err := r.txOrBegin(ctx)
	if err != nil {
		return "", access.APIToken{}, err
	}
	if own {
		defer func() { _ = tx.Rollback(ctx) }()
	}
	transactional := *r
	transactional.db = tx
	old, err := transactional.apiToken(ctx, tokenID)
	if err != nil {
		return "", access.APIToken{}, err
	}
	if old.PrincipalID != principalID || old.RevokedAt != "" || old.PermissionProfile != access.PermissionCatalogProfile || old.Permissions == nil || old.ModifiedAt != in.ExpectedModifiedAt.UTC().Format(time.RFC3339Nano) {
		return "", access.APIToken{}, pgx.ErrNoRows
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, old.ExpiresAt)
	if err != nil || !expiresAt.After(time.Now()) {
		return "", access.APIToken{}, pgx.ErrNoRows
	}
	secret, replacement, err := transactional.CreateScopedAPITokenWithMetadata(ctx, access.ScopedAPITokenInput{
		PrincipalID: principalID, Name: old.Name, Description: old.Description,
		Permissions: old.Permissions, ExpiresAt: expiresAt,
	})
	if err != nil {
		return "", access.APIToken{}, err
	}
	id, err := pgUUID(tokenID)
	if err != nil {
		return "", access.APIToken{}, err
	}
	pid, err := pgUUID(principalID)
	if err != nil {
		return "", access.APIToken{}, err
	}
	tag, err := accessdb.New(tx).RevokeScopedAPITokenForRotation(ctx, accessdb.RevokeScopedAPITokenForRotationParams{
		ID: id, PrincipalID: pid, ExpectedModifiedAt: pgTimestamp(in.ExpectedModifiedAt),
	})
	if err != nil {
		return "", access.APIToken{}, err
	}
	if tag.RowsAffected() == 0 {
		return "", access.APIToken{}, pgx.ErrNoRows
	}
	if own {
		if err := tx.Commit(ctx); err != nil {
			return "", access.APIToken{}, err
		}
	}
	return secret, replacement, nil
}

// APITokenAuthorityEvidence resolves the immutable token identity required by
// caller-authority jobs. It never accepts a bearer secret or request-held
// capability list; lifecycle and principal state are checked from durable
// PostgreSQL rows at the supplied instant.
func (r *Repository) APITokenAuthorityEvidence(ctx context.Context, principalID, tokenID string, now time.Time) (access.APIToken, error) {
	principalID = strings.TrimSpace(principalID)
	tokenID = strings.TrimSpace(tokenID)
	if principalID == "" || tokenID == "" {
		return access.APIToken{}, access.ErrForbidden
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	token, err := r.apiToken(ctx, tokenID)
	if err != nil {
		return access.APIToken{}, err
	}
	if token.ID != tokenID || token.PrincipalID != principalID || token.TokenFingerprint == "" || token.RevokedAt != "" ||
		token.PermissionProfile != access.PermissionCatalogProfile || token.Permissions == nil {
		return access.APIToken{}, access.ErrForbidden
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, token.ExpiresAt)
	if err != nil || !expiresAt.After(now.UTC()) {
		return access.APIToken{}, access.ErrForbidden
	}
	principal, err := r.PrincipalByID(ctx, principalID)
	if err != nil {
		return access.APIToken{}, err
	}
	if principal.AccessDisabled() {
		return access.APIToken{}, access.ErrForbidden
	}
	return token, nil
}
