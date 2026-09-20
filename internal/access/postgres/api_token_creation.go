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

func (r *Repository) CreateAPITokenWithMetadata(ctx context.Context, in access.APITokenInput) (string, access.APIToken, error) {
	resolvedExpiry, err := access.ResolveAPITokenExpiry(in.ExpiresAt, time.Now().UTC())
	if err != nil {
		return "", access.APIToken{}, err
	}
	in.ExpiresAt = resolvedExpiry
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
	caps, err := capabilitiesJSON(in.Capabilities)
	if err != nil {
		return "", access.APIToken{}, err
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
	tag, err := accessdb.New(db).CreateAPIToken(ctx, accessdb.CreateAPITokenParams{ID: tokenID, PrincipalID: principalID, Name: name,
		TokenFingerprint: r.secretFingerprint(tok), Verifier: ver, Capabilities: caps, ExpiresAt: pgTimestamp(in.ExpiresAt)})
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
	row, e := r.apiToken(ctx, id)
	return tok, row, e
}
