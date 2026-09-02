package mcpoauth

// PostgreSQL-backed fosite state.  This adapter intentionally uses the native
// pgx transaction surface and qualified access.oauth_* tables; it never opens
// database/sql or falls back to the legacy SQLite store.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flidai/leapview/internal/access/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/ory/fosite"
)

type postgresRunner interface {
	postgres.DBTX
	Begin(context.Context) (pgx.Tx, error)
}

// PostgresStore is the durable MCP OAuth state adapter for PostgreSQL.
type PostgresStore struct {
	db               postgres.DBTX
	resolveClient    func(context.Context, string) (storedClient, error)
	dynamicClients   sync.Map
	sessionRetention time.Duration
	lastPruneUnix    atomic.Int64
}

func NewPostgresStore(db postgres.DBTX) (*PostgresStore, error) {
	if db == nil {
		return nil, errors.New("MCP OAuth PostgreSQL database is required")
	}
	if _, ok := db.(postgresRunner); !ok {
		return nil, errors.New("MCP OAuth PostgreSQL database must support transactions")
	}
	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) IsPostgresBacked() bool { return s != nil && s.db != nil }

func (s *PostgresStore) SetSessionRetention(retention time.Duration) {
	if s != nil {
		s.sessionRetention = retention
	}
}

func (s *PostgresStore) SetClientResolver(resolver func(context.Context, string) (storedClient, error)) {
	s.resolveClient = resolver
}

func (s *PostgresStore) requireDB() (postgres.DBTX, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("MCP OAuth PostgreSQL database is required")
	}
	return s.db, nil
}

func (s *PostgresStore) CreateClient(ctx context.Context, client storedClient) error {
	r, err := s.requireDB()
	if err != nil {
		return err
	}
	redirects, _ := json.Marshal(client.RedirectURIs)
	grants, _ := json.Marshal(client.GrantTypes)
	responses, _ := json.Marshal(client.ResponseTypes)
	scopes, _ := json.Marshal(client.Scopes)
	audience, _ := json.Marshal(client.Audience)
	_, err = r.Exec(ctx, `INSERT INTO access.oauth_client
        (id,name,redirect_uris,grant_types,response_types,scopes,audience,public_client,secret_hash,token_endpoint_auth_method,principal_id)
        VALUES ($1,$2,$3::jsonb,$4::jsonb,$5::jsonb,$6::jsonb,$7::jsonb,$8,$9,$10,NULLIF($11,'')::uuid)`,
		client.ID, client.Name, redirects, grants, responses, scopes, audience,
		client.Public, client.SecretHash, client.TokenEndpointAuthMethod, client.PrincipalID)
	return err
}

func (s *PostgresStore) EnsureServiceClient(ctx context.Context, principalID, principalName, resource string) error {
	redirects, _ := json.Marshal([]string{})
	grants, _ := json.Marshal([]string{"client_credentials"})
	responses, _ := json.Marshal([]string{})
	scopes, _ := json.Marshal([]string{ScopeMCPUse})
	audience, _ := json.Marshal([]string{resource})
	r, err := s.requireDB()
	if err != nil {
		return err
	}
	_, err = r.Exec(ctx, `INSERT INTO access.oauth_client
        (id,name,redirect_uris,grant_types,response_types,scopes,audience,public_client,token_endpoint_auth_method,principal_id)
        VALUES ($1,$2,$3::jsonb,$4::jsonb,$5::jsonb,$6::jsonb,$7::jsonb,false,'client_secret_post',$1::uuid)
        ON CONFLICT(id) DO UPDATE SET name=EXCLUDED.name, scopes=EXCLUDED.scopes, audience=EXCLUDED.audience, principal_id=EXCLUDED.principal_id, updated_at=clock_timestamp()`,
		principalID, principalName, redirects, grants, responses, scopes, audience)
	return err
}

func (s *PostgresStore) GetClient(ctx context.Context, id string) (fosite.Client, error) {
	if cached, ok := s.dynamicClients.Load(id); ok {
		entry := cached.(cachedClient)
		if entry.expiresAt.After(time.Now()) {
			return entry.client.fositeClient(), nil
		}
		s.dynamicClients.Delete(id)
	}
	r, err := s.requireDB()
	if err != nil {
		return nil, err
	}
	var client storedClient
	var principal *string
	var redirects, grants, responses, scopes, audience []byte
	err = r.QueryRow(ctx, `SELECT id,name,redirect_uris,grant_types,response_types,scopes,audience,public_client,secret_hash,token_endpoint_auth_method,principal_id::text FROM access.oauth_client WHERE id=$1`, id).
		Scan(&client.ID, &client.Name, &redirects, &grants, &responses, &scopes, &audience, &client.Public, &client.SecretHash, &client.TokenEndpointAuthMethod, &principal)
	if errors.Is(err, pgx.ErrNoRows) {
		if s.resolveClient == nil {
			return nil, fosite.ErrNotFound
		}
		resolved, resolveErr := s.resolveClient(ctx, id)
		if resolveErr != nil {
			return nil, fosite.ErrNotFound.WithWrap(resolveErr)
		}
		s.dynamicClients.Store(id, cachedClient{client: resolved, expiresAt: time.Now().Add(time.Hour)})
		return resolved.fositeClient(), nil
	}
	if err != nil {
		return nil, err
	}
	for _, field := range []struct {
		raw    []byte
		target *[]string
	}{{redirects, &client.RedirectURIs}, {grants, &client.GrantTypes}, {responses, &client.ResponseTypes}, {scopes, &client.Scopes}, {audience, &client.Audience}} {
		if err := json.Unmarshal(field.raw, field.target); err != nil {
			return nil, fmt.Errorf("decode OAuth client %q: %w", id, err)
		}
	}
	if principal != nil {
		client.PrincipalID = *principal
	}
	return client.fositeClient(), nil
}

func (s *PostgresStore) ClientName(ctx context.Context, id string) (string, error) {
	if cached, ok := s.dynamicClients.Load(id); ok {
		entry := cached.(cachedClient)
		if entry.expiresAt.After(time.Now()) {
			return entry.client.Name, nil
		}
	}
	r, err := s.requireDB()
	if err != nil {
		return "", err
	}
	var name string
	if err := r.QueryRow(ctx, `SELECT name FROM access.oauth_client WHERE id=$1`, id).Scan(&name); err != nil {
		return "", err
	}
	return name, nil
}

func (s *PostgresStore) ClientAssertionJWTValid(ctx context.Context, jti string) error {
	if jti != strings.TrimSpace(jti) || jti == "" || len(jti) > 512 {
		return errors.New("OAuth client assertion JTI is invalid")
	}
	r, err := s.requireDB()
	if err != nil {
		return err
	}
	var expires time.Time
	var valid bool
	err = r.QueryRow(ctx, `SELECT expires_at, expires_at > clock_timestamp() FROM access.oauth_client_assertion WHERE jti=$1`, jti).Scan(&expires, &valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if valid {
		return fosite.ErrJTIKnown
	}
	_, err = r.Exec(ctx, `DELETE FROM access.oauth_client_assertion WHERE jti=$1`, jti)
	return err
}

func (s *PostgresStore) SetClientAssertionJWT(ctx context.Context, jti string, exp time.Time) error {
	if jti != strings.TrimSpace(jti) || jti == "" || len(jti) > 512 {
		return errors.New("OAuth client assertion JTI is invalid")
	}
	r, err := s.requireDB()
	if err != nil {
		return err
	}
	var now time.Time
	if err := r.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return err
	}
	if !exp.After(now) || exp.After(now.Add(24*time.Hour)) {
		return errors.New("OAuth client assertion expiry is invalid")
	}
	_, err = r.Exec(ctx, `INSERT INTO access.oauth_client_assertion(jti,expires_at) VALUES($1,$2)`, jti, exp.UTC())
	var constraintErr *pgconn.PgError
	if errors.As(err, &constraintErr) && constraintErr.Code == "23505" {
		return fosite.ErrJTIKnown
	}
	return err
}

type postgresOAuthTxKey struct{}

func (s *PostgresStore) runner(ctx context.Context) postgres.DBTX {
	if tx, ok := ctx.Value(postgresOAuthTxKey{}).(pgx.Tx); ok {
		return tx
	}
	return s.db
}

func (s *PostgresStore) BeginTX(ctx context.Context) (context.Context, error) {
	db, err := s.requireDB()
	if err != nil {
		return ctx, err
	}
	b, ok := db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		return ctx, errors.New("MCP OAuth PostgreSQL database does not support transactions")
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, postgresOAuthTxKey{}, tx), nil
}

func (s *PostgresStore) Commit(ctx context.Context) error {
	tx, ok := ctx.Value(postgresOAuthTxKey{}).(pgx.Tx)
	if !ok {
		return errors.New("OAuth transaction is missing")
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) Rollback(ctx context.Context) error {
	tx, ok := ctx.Value(postgresOAuthTxKey{}).(pgx.Tx)
	if !ok {
		return errors.New("OAuth transaction is missing")
	}
	return tx.Rollback(ctx)
}

func (s *PostgresStore) decodeRequester(ctx context.Context, raw []byte) (fosite.Requester, error) {
	var persisted persistedRequest
	if err := json.Unmarshal(raw, &persisted); err != nil {
		return nil, err
	}
	client, err := s.GetClient(ctx, persisted.ClientID)
	if err != nil {
		return nil, err
	}
	request := fosite.NewRequest()
	request.ID, request.RequestedAt, request.Client = persisted.ID, persisted.RequestedAt, client
	request.RequestedScope, request.GrantedScope = fosite.Arguments(persisted.RequestedScope), fosite.Arguments(persisted.GrantedScope)
	request.Form, request.Session = url.Values(persisted.Form), persisted.Session
	request.RequestedAudience, request.GrantedAudience = fosite.Arguments(persisted.RequestedAudience), fosite.Arguments(persisted.GrantedAudience)
	return request, nil
}

func (s *PostgresStore) createSession(ctx context.Context, kind, signature, accessSignature string, request fosite.Requester) error {
	if err := s.pruneExpiredSessions(ctx); err != nil {
		return err
	}
	raw, err := encodeRequester(request)
	if err != nil {
		return err
	}
	_, err = s.runner(ctx).Exec(ctx, `INSERT INTO access.oauth_session(kind,signature,request_id,request_json,access_signature) VALUES($1,$2,$3,$4::jsonb,$5)`, kind, signature, request.GetID(), raw, accessSignature)
	return err
}

func (s *PostgresStore) pruneExpiredSessions(ctx context.Context) error {
	if s.sessionRetention <= 0 {
		return nil
	}
	var now time.Time
	if err := s.runner(ctx).QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return err
	}
	now = now.UTC()
	last := s.lastPruneUnix.Load()
	if last != 0 && now.Sub(time.Unix(last, 0)) < time.Hour {
		return nil
	}
	_, err := s.runner(ctx).Exec(ctx, `DELETE FROM access.oauth_session WHERE created_at < $1`, now.Add(-s.sessionRetention))
	if err != nil {
		return err
	}
	if _, err = s.runner(ctx).Exec(ctx, `DELETE FROM access.oauth_client_assertion WHERE expires_at < $1`, now); err != nil {
		return err
	}
	s.lastPruneUnix.Store(now.Unix())
	return nil
}

func (s *PostgresStore) getSession(ctx context.Context, kind, signature string) (fosite.Requester, bool, error) {
	var raw []byte
	var active bool
	err := s.runner(ctx).QueryRow(ctx, `SELECT request_json,active FROM access.oauth_session WHERE kind=$1 AND signature=$2`, kind, signature).Scan(&raw, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fosite.ErrNotFound
	}
	if err != nil {
		return nil, false, err
	}
	request, err := s.decodeRequester(ctx, raw)
	return request, active, err
}

func (s *PostgresStore) deleteSession(ctx context.Context, kind, signature string) error {
	_, err := s.runner(ctx).Exec(ctx, `DELETE FROM access.oauth_session WHERE kind=$1 AND signature=$2`, kind, signature)
	return err
}

func (s *PostgresStore) CreateAuthorizeCodeSession(ctx context.Context, code string, request fosite.Requester) error {
	return s.createSession(ctx, sessionAuthorizeCode, code, "", request)
}
func (s *PostgresStore) GetAuthorizeCodeSession(ctx context.Context, code string, _ fosite.Session) (fosite.Requester, error) {
	r, active, err := s.getSession(ctx, sessionAuthorizeCode, code)
	if err != nil {
		return nil, err
	}
	if !active {
		return r, fosite.ErrInvalidatedAuthorizeCode
	}
	return r, nil
}
func (s *PostgresStore) InvalidateAuthorizeCodeSession(ctx context.Context, code string) error {
	command, err := s.runner(ctx).Exec(ctx, `UPDATE access.oauth_session SET active=false WHERE kind=$1 AND signature=$2 AND active=true`, sessionAuthorizeCode, code)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return fosite.ErrNotFound
	}
	return nil
}
func (s *PostgresStore) CreateAccessTokenSession(ctx context.Context, signature string, request fosite.Requester) error {
	return s.createSession(ctx, sessionAccessToken, signature, "", request)
}
func (s *PostgresStore) GetAccessTokenSession(ctx context.Context, signature string, _ fosite.Session) (fosite.Requester, error) {
	r, active, err := s.getSession(ctx, sessionAccessToken, signature)
	if err != nil {
		return nil, err
	}
	if !active {
		return r, fosite.ErrInactiveToken
	}
	return r, nil
}
func (s *PostgresStore) DeleteAccessTokenSession(ctx context.Context, signature string) error {
	return s.deleteSession(ctx, sessionAccessToken, signature)
}
func (s *PostgresStore) CreateRefreshTokenSession(ctx context.Context, signature, accessSignature string, request fosite.Requester) error {
	return s.createSession(ctx, sessionRefreshToken, signature, accessSignature, request)
}
func (s *PostgresStore) GetRefreshTokenSession(ctx context.Context, signature string, _ fosite.Session) (fosite.Requester, error) {
	r, active, err := s.getSession(ctx, sessionRefreshToken, signature)
	if err != nil {
		return nil, err
	}
	if !active {
		return r, fosite.ErrInactiveToken
	}
	return r, nil
}
func (s *PostgresStore) DeleteRefreshTokenSession(ctx context.Context, signature string) error {
	return s.deleteSession(ctx, sessionRefreshToken, signature)
}
func (s *PostgresStore) RotateRefreshToken(ctx context.Context, requestID, refreshSignature string) error {
	var refreshed int64
	err := s.runner(ctx).QueryRow(ctx, `WITH refresh AS (
        UPDATE access.oauth_session
        SET active=false
        WHERE kind=$1 AND signature=$2 AND request_id=$3 AND active=true
        RETURNING request_id
    ), access_tokens AS (
        UPDATE access.oauth_session AS access_session
        SET active=false
        FROM refresh
        WHERE access_session.kind=$4 AND access_session.request_id=refresh.request_id
        RETURNING access_session.signature
    )
    SELECT count(*) FROM refresh`, sessionRefreshToken, refreshSignature, requestID, sessionAccessToken).Scan(&refreshed)
	if err != nil {
		return err
	}
	if refreshed == 0 {
		return fosite.ErrNotFound
	}
	return nil
}
func (s *PostgresStore) RevokeRefreshToken(ctx context.Context, requestID string) error {
	var revoked int64
	err := s.runner(ctx).QueryRow(ctx, `WITH refresh AS (
        UPDATE access.oauth_session
        SET active=false
        WHERE kind=$1 AND request_id=$2 AND active=true
        RETURNING request_id
    ), access_tokens AS (
        UPDATE access.oauth_session AS access_session
        SET active=false
        FROM refresh
        WHERE access_session.kind=$3 AND access_session.request_id=refresh.request_id
        RETURNING access_session.signature
    )
    SELECT count(*) FROM refresh`, sessionRefreshToken, requestID, sessionAccessToken).Scan(&revoked)
	if err != nil {
		return err
	}
	if revoked == 0 {
		return fosite.ErrNotFound
	}
	return nil
}
func (s *PostgresStore) RevokeAccessToken(ctx context.Context, requestID string) error {
	command, err := s.runner(ctx).Exec(ctx, `UPDATE access.oauth_session SET active=false WHERE kind=$1 AND request_id=$2 AND active=true`, sessionAccessToken, requestID)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return fosite.ErrNotFound
	}
	return nil
}
func (s *PostgresStore) CreatePKCERequestSession(ctx context.Context, signature string, request fosite.Requester) error {
	return s.createSession(ctx, sessionPKCE, signature, "", request)
}
func (s *PostgresStore) GetPKCERequestSession(ctx context.Context, signature string, _ fosite.Session) (fosite.Requester, error) {
	r, active, err := s.getSession(ctx, sessionPKCE, signature)
	if err != nil {
		return nil, err
	}
	if !active {
		return r, fosite.ErrInactiveToken
	}
	return r, nil
}
func (s *PostgresStore) DeletePKCERequestSession(ctx context.Context, signature string) error {
	return s.deleteSession(ctx, sessionPKCE, signature)
}

var _ StoreBackend = (*PostgresStore)(nil)
