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
	if token.ID != tokenID || token.PrincipalID != principalID || token.TokenFingerprint == "" || token.RevokedAt != "" {
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
