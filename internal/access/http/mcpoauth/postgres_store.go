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
	redirects, _ := json.Marshal(client.RedirectURIs)
	grants, _ := json.Marshal(client.GrantTypes)
	responses, _ := json.Marshal(client.ResponseTypes)
	scopes, _ := json.Marshal(client.Scopes)
	audience, _ := json.Marshal(client.Audience)
	r, err := s.requireDB()
	if err != nil {
		return err
	}
	return postgres.CreateOAuthClient(ctx, r, postgres.OAuthClientInput{
		ID: client.ID, Name: client.Name, RedirectURIs: redirects, GrantTypes: grants,
		ResponseTypes: responses, Scopes: scopes, Audience: audience,
		PublicClient: client.Public, SecretHash: client.SecretHash,
		TokenEndpointAuthMethod: client.TokenEndpointAuthMethod, PrincipalID: client.PrincipalID,
	})
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
	return postgres.EnsureOAuthClient(ctx, r, principalID, principalName, redirects, grants, responses, scopes, audience)
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
	row, err := postgres.GetOAuthClient(ctx, r, id)
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
	client := storedClient{
		ID: row.ID, Name: row.Name, Public: row.PublicClient,
		SecretHash: row.SecretHash, TokenEndpointAuthMethod: row.TokenEndpointAuthMethod,
		PrincipalID: row.PrincipalID,
	}
	for _, field := range []struct {
		raw    []byte
		target *[]string
	}{{row.RedirectURIs, &client.RedirectURIs}, {row.GrantTypes, &client.GrantTypes}, {row.ResponseTypes, &client.ResponseTypes}, {row.Scopes, &client.Scopes}, {row.Audience, &client.Audience}} {
		if err := json.Unmarshal(field.raw, field.target); err != nil {
			return nil, fmt.Errorf("decode OAuth client %q: %w", id, err)
		}
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
	name, err := postgres.GetOAuthClientName(ctx, r, id)
	if err != nil {
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
	row, err := postgres.GetClientAssertion(ctx, r, jti)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if row.Valid {
		return fosite.ErrJTIKnown
	}
	return postgres.DeleteClientAssertion(ctx, r, jti)
}

func (s *PostgresStore) SetClientAssertionJWT(ctx context.Context, jti string, exp time.Time) error {
	if jti != strings.TrimSpace(jti) || jti == "" || len(jti) > 512 {
		return errors.New("OAuth client assertion JTI is invalid")
	}
	r, err := s.requireDB()
	if err != nil {
		return err
	}
	now, err := postgres.OAuthClock(ctx, r)
	if err != nil {
		return err
	}
	if !exp.After(now) || exp.After(now.Add(24*time.Hour)) {
		return errors.New("OAuth client assertion expiry is invalid")
	}
	err = postgres.InsertClientAssertion(ctx, r, jti, exp)
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
	return postgres.CreateOAuthSession(ctx, s.runner(ctx), kind, signature, request.GetID(), []byte(raw), accessSignature)
}

func (s *PostgresStore) pruneExpiredSessions(ctx context.Context) error {
	if s.sessionRetention <= 0 {
		return nil
	}
	now, err := postgres.OAuthClock(ctx, s.runner(ctx))
	if err != nil {
		return err
	}
	last := s.lastPruneUnix.Load()
	if last != 0 && now.Sub(time.Unix(last, 0)) < time.Hour {
		return nil
	}
	if err := postgres.DeleteExpiredOAuthSessions(ctx, s.runner(ctx), now.Add(-s.sessionRetention)); err != nil {
		return err
	}
	if err := postgres.DeleteExpiredClientAssertions(ctx, s.runner(ctx), now); err != nil {
		return err
	}
	s.lastPruneUnix.Store(now.Unix())
	return nil
}

func (s *PostgresStore) getSession(ctx context.Context, kind, signature string) (fosite.Requester, bool, error) {
	row, err := postgres.GetOAuthSession(ctx, s.runner(ctx), kind, signature)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fosite.ErrNotFound
	}
	if err != nil {
		return nil, false, err
	}
	request, err := s.decodeRequester(ctx, row.RequestJSON)
	return request, row.Active, err
}

func (s *PostgresStore) deleteSession(ctx context.Context, kind, signature string) error {
	return postgres.DeleteOAuthSession(ctx, s.runner(ctx), kind, signature)
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
	rows, err := postgres.InvalidateAuthorizeCode(ctx, s.runner(ctx), sessionAuthorizeCode, code)
	if err != nil {
		return err
	}
	if rows == 0 {
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
	refreshed, err := postgres.RotateRefreshToken(ctx, s.runner(ctx), sessionRefreshToken, refreshSignature, requestID, sessionAccessToken)
	if err != nil {
		return err
	}
	if refreshed == 0 {
		return fosite.ErrNotFound
	}
	return nil
}
func (s *PostgresStore) RevokeRefreshToken(ctx context.Context, requestID string) error {
	revoked, err := postgres.RevokeRefreshToken(ctx, s.runner(ctx), sessionRefreshToken, requestID, sessionAccessToken)
	if err != nil {
		return err
	}
	if revoked == 0 {
		return fosite.ErrNotFound
	}
	return nil
}
func (s *PostgresStore) RevokeAccessToken(ctx context.Context, requestID string) error {
	rows, err := postgres.RevokeAccessToken(ctx, s.runner(ctx), sessionAccessToken, requestID)
	if err != nil {
		return err
	}
	if rows == 0 {
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
