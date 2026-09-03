package postgres

// This file is the package boundary around the private sqlc output.  The
// generated package intentionally remains inaccessible to sibling capabilities;
// these small adapters expose domain-friendly values while preserving pgtype
// validity and caller-owned transaction handles.

import (
	"context"
	"errors"
	"fmt"
	"time"

	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

type OAuthClientInput struct {
	ID                      string
	Name                    string
	RedirectURIs            []byte
	GrantTypes              []byte
	ResponseTypes           []byte
	Scopes                  []byte
	Audience                []byte
	PublicClient            bool
	SecretHash              []byte
	TokenEndpointAuthMethod string
	PrincipalID             string
}

type OAuthClientRecord struct {
	ID                      string
	Name                    string
	RedirectURIs            []byte
	GrantTypes              []byte
	ResponseTypes           []byte
	Scopes                  []byte
	Audience                []byte
	PublicClient            bool
	SecretHash              []byte
	TokenEndpointAuthMethod string
	PrincipalID             string
}

type OAuthSessionRecord struct {
	RequestJSON []byte
	Active      bool
}

type OAuthAssertionRecord struct {
	ExpiresAt time.Time
	Valid     bool
}

func oauthQueries(db DBTX) (*accessdb.Queries, error) {
	if db == nil {
		return nil, errors.New("access PostgreSQL database is required")
	}
	return accessdb.New(db), nil
}

func oauthUUID(value string) (pgtype.UUID, error) {
	if value == "" {
		return pgtype.UUID{}, nil
	}
	id, err := uuid.Parse(value)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("OAuth principal id must be a UUID: %w", err)
	}
	return pgtype.UUID{Bytes: id, Valid: true}, nil
}

func oauthTimestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}

func CreateOAuthClient(ctx context.Context, db DBTX, input OAuthClientInput) error {
	q, err := oauthQueries(db)
	if err != nil {
		return err
	}
	principalID, err := oauthUUID(input.PrincipalID)
	if err != nil {
		return err
	}
	return q.CreateOAuthClient(ctx, accessdb.CreateOAuthClientParams{
		ID: input.ID, Name: input.Name, RedirectUris: input.RedirectURIs,
		GrantTypes: input.GrantTypes, ResponseTypes: input.ResponseTypes,
		Scopes: input.Scopes, Audience: input.Audience,
		PublicClient: input.PublicClient, SecretHash: input.SecretHash,
		TokenEndpointAuthMethod: input.TokenEndpointAuthMethod,
		PrincipalID:             principalID,
	})
}

func EnsureOAuthClient(ctx context.Context, db DBTX, id, name string, redirectURIs, grantTypes, responseTypes, scopes, audience []byte) error {
	q, err := oauthQueries(db)
	if err != nil {
		return err
	}
	return q.EnsureOAuthClient(ctx, accessdb.EnsureOAuthClientParams{
		ID: id, Name: name, RedirectUris: redirectURIs, GrantTypes: grantTypes,
		ResponseTypes: responseTypes, Scopes: scopes, Audience: audience,
	})
}

func GetOAuthClient(ctx context.Context, db DBTX, id string) (OAuthClientRecord, error) {
	q, err := oauthQueries(db)
	if err != nil {
		return OAuthClientRecord{}, err
	}
	row, err := q.GetOAuthClient(ctx, id)
	if err != nil {
		return OAuthClientRecord{}, err
	}
	principalID := ""
	if row.PrincipalID.Valid {
		principalID = uuid.UUID(row.PrincipalID.Bytes).String()
	}
	return OAuthClientRecord{
		ID: row.ID, Name: row.Name, RedirectURIs: row.RedirectUris,
		GrantTypes: row.GrantTypes, ResponseTypes: row.ResponseTypes,
		Scopes: row.Scopes, Audience: row.Audience, PublicClient: row.PublicClient,
		SecretHash: row.SecretHash, TokenEndpointAuthMethod: row.TokenEndpointAuthMethod,
		PrincipalID: principalID,
	}, nil
}

func GetOAuthClientName(ctx context.Context, db DBTX, id string) (string, error) {
	q, err := oauthQueries(db)
	if err != nil {
		return "", err
	}
	return q.GetOAuthClientName(ctx, id)
}

func GetClientAssertion(ctx context.Context, db DBTX, jti string) (OAuthAssertionRecord, error) {
	q, err := oauthQueries(db)
	if err != nil {
		return OAuthAssertionRecord{}, err
	}
	row, err := q.GetClientAssertion(ctx, jti)
	if err != nil {
		return OAuthAssertionRecord{}, err
	}
	return OAuthAssertionRecord{ExpiresAt: row.ExpiresAt.Time.UTC(), Valid: row.Valid}, nil
}

func DeleteClientAssertion(ctx context.Context, db DBTX, jti string) error {
	q, err := oauthQueries(db)
	if err != nil {
		return err
	}
	return q.DeleteClientAssertion(ctx, jti)
}

func InsertClientAssertion(ctx context.Context, db DBTX, jti string, expiresAt time.Time) error {
	q, err := oauthQueries(db)
	if err != nil {
		return err
	}
	return q.InsertClientAssertion(ctx, accessdb.InsertClientAssertionParams{Jti: jti, ExpiresAt: oauthTimestamp(expiresAt)})
}

func OAuthClock(ctx context.Context, db DBTX) (time.Time, error) {
	q, err := oauthQueries(db)
	if err != nil {
		return time.Time{}, err
	}
	row, err := q.OAuthClock(ctx)
	if err != nil {
		return time.Time{}, err
	}
	if !row.Valid {
		return time.Time{}, errors.New("OAuth database clock returned NULL")
	}
	return row.Time.UTC(), nil
}

func CreateOAuthSession(ctx context.Context, db DBTX, kind, signature, requestID string, requestJSON []byte, accessSignature string) error {
	q, err := oauthQueries(db)
	if err != nil {
		return err
	}
	return q.CreateOAuthSession(ctx, accessdb.CreateOAuthSessionParams{
		Kind: kind, Signature: signature, RequestID: requestID,
		RequestJson: requestJSON, AccessSignature: accessSignature,
	})
}

func DeleteExpiredOAuthSessions(ctx context.Context, db DBTX, before time.Time) error {
	q, err := oauthQueries(db)
	if err != nil {
		return err
	}
	return q.DeleteExpiredOAuthSessions(ctx, oauthTimestamp(before))
}

func DeleteExpiredClientAssertions(ctx context.Context, db DBTX, before time.Time) error {
	q, err := oauthQueries(db)
	if err != nil {
		return err
	}
	return q.DeleteExpiredClientAssertions(ctx, oauthTimestamp(before))
}

func GetOAuthSession(ctx context.Context, db DBTX, kind, signature string) (OAuthSessionRecord, error) {
	q, err := oauthQueries(db)
	if err != nil {
		return OAuthSessionRecord{}, err
	}
	row, err := q.GetOAuthSession(ctx, accessdb.GetOAuthSessionParams{Kind: kind, Signature: signature})
	if err != nil {
		return OAuthSessionRecord{}, err
	}
	return OAuthSessionRecord{RequestJSON: row.RequestJson, Active: row.Active}, nil
}

func DeleteOAuthSession(ctx context.Context, db DBTX, kind, signature string) error {
	q, err := oauthQueries(db)
	if err != nil {
		return err
	}
	return q.DeleteOAuthSession(ctx, accessdb.DeleteOAuthSessionParams{Kind: kind, Signature: signature})
}

func InvalidateAuthorizeCode(ctx context.Context, db DBTX, kind, signature string) (int64, error) {
	q, err := oauthQueries(db)
	if err != nil {
		return 0, err
	}
	return q.InvalidateAuthorizeCode(ctx, accessdb.InvalidateAuthorizeCodeParams{Kind: kind, Signature: signature})
}

func RotateRefreshToken(ctx context.Context, db DBTX, kind, signature, requestID, accessKind string) (int64, error) {
	q, err := oauthQueries(db)
	if err != nil {
		return 0, err
	}
	return q.RotateRefreshToken(ctx, accessdb.RotateRefreshTokenParams{Kind: kind, Signature: signature, RequestID: requestID, AccessKind: accessKind})
}

func RevokeRefreshToken(ctx context.Context, db DBTX, kind, requestID, accessKind string) (int64, error) {
	q, err := oauthQueries(db)
	if err != nil {
		return 0, err
	}
	return q.RevokeRefreshToken(ctx, accessdb.RevokeRefreshTokenParams{Kind: kind, RequestID: requestID, AccessKind: accessKind})
}

func RevokeAccessToken(ctx context.Context, db DBTX, kind, requestID string) (int64, error) {
	q, err := oauthQueries(db)
	if err != nil {
		return 0, err
	}
	return q.RevokeAccessToken(ctx, accessdb.RevokeAccessTokenParams{Kind: kind, RequestID: requestID})
}
