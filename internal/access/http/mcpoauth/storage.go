package mcpoauth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ory/fosite"
)

const (
	sessionAuthorizeCode = "authorize_code"
	sessionAccessToken   = "access_token"
	sessionRefreshToken  = "refresh_token"
	sessionPKCE          = "pkce"
)

type Store struct {
	db               *sql.DB
	resolveClient    func(context.Context, string) (storedClient, error)
	dynamicClients   sync.Map
	sessionRetention time.Duration
	lastPruneUnix    atomic.Int64
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) IsPostgresBacked() bool { return false }

func (s *Store) SetSessionRetention(retention time.Duration) {
	if s != nil {
		s.sessionRetention = retention
	}
}

func (s *Store) SetClientResolver(resolver func(context.Context, string) (storedClient, error)) {
	s.resolveClient = resolver
}

type cachedClient struct {
	client    storedClient
	expiresAt time.Time
}

type storedClient struct {
	ID                      string
	Name                    string
	RedirectURIs            []string
	GrantTypes              []string
	ResponseTypes           []string
	Scopes                  []string
	Audience                []string
	Public                  bool
	SecretHash              []byte
	TokenEndpointAuthMethod string
	PrincipalID             string
}

// StoredClient is the portable client record supplied by a durable OAuth
// backend. The alias keeps the existing internal representation while making
// StoreBackend implementable by other capability packages.
type StoredClient = storedClient

func (s *Store) CreateClient(ctx context.Context, client storedClient) error {
	redirects, _ := json.Marshal(client.RedirectURIs)
	grants, _ := json.Marshal(client.GrantTypes)
	responses, _ := json.Marshal(client.ResponseTypes)
	scopes, _ := json.Marshal(client.Scopes)
	audience, _ := json.Marshal(client.Audience)
	public := 0
	if client.Public {
		public = 1
	}
	var principal any
	if client.PrincipalID != "" {
		principal = client.PrincipalID
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO oauth_clients
        (id, name, redirect_uris_json, grant_types_json, response_types_json, scopes_json, audience_json, public, secret_hash, token_endpoint_auth_method, principal_id)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, client.ID, client.Name, redirects, grants, responses, scopes, audience, public, client.SecretHash, client.TokenEndpointAuthMethod, principal)
	return err
}

func (s *Store) EnsureServiceClient(ctx context.Context, principalID, principalName, resource string) error {
	redirects, _ := json.Marshal([]string{})
	grants, _ := json.Marshal([]string{"client_credentials"})
	responses, _ := json.Marshal([]string{})
	scopes, _ := json.Marshal([]string{ScopeMCPUse})
	audience, _ := json.Marshal([]string{resource})
	_, err := s.db.ExecContext(ctx, `INSERT INTO oauth_clients
        (id, name, redirect_uris_json, grant_types_json, response_types_json, scopes_json, audience_json, public, token_endpoint_auth_method, principal_id)
        VALUES (?, ?, ?, ?, ?, ?, ?, 0, 'client_secret_post', ?)
        ON CONFLICT(id) DO UPDATE SET name = excluded.name, scopes_json = excluded.scopes_json,
        audience_json = excluded.audience_json, principal_id = excluded.principal_id`,
		principalID, principalName, redirects, grants, responses, scopes, audience, principalID)
	return err
}

func (s *Store) GetClient(ctx context.Context, id string) (fosite.Client, error) {
	if cached, ok := s.dynamicClients.Load(id); ok {
		entry := cached.(cachedClient)
		if entry.expiresAt.After(time.Now()) {
			return entry.client.fositeClient(), nil
		}
		s.dynamicClients.Delete(id)
	}
	var client storedClient
	var redirects, grants, responses, scopes, audience string
	var public int
	var principal sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, name, redirect_uris_json, grant_types_json, response_types_json,
        scopes_json, audience_json, public, secret_hash, token_endpoint_auth_method, principal_id
        FROM oauth_clients WHERE id = ?`, id).Scan(
		&client.ID, &client.Name, &redirects, &grants, &responses, &scopes, &audience,
		&public, &client.SecretHash, &client.TokenEndpointAuthMethod, &principal,
	)
	if errors.Is(err, sql.ErrNoRows) {
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
	encodedFields := []struct {
		raw    string
		target *[]string
	}{
		{redirects, &client.RedirectURIs}, {grants, &client.GrantTypes},
		{responses, &client.ResponseTypes}, {scopes, &client.Scopes},
		{audience, &client.Audience},
	}
	for _, field := range encodedFields {
		if err := json.Unmarshal([]byte(field.raw), field.target); err != nil {
			return nil, fmt.Errorf("decode OAuth client %q: %w", id, err)
		}
	}
	client.Public = public == 1
	client.PrincipalID = principal.String
	return client.fositeClient(), nil
}

func (client storedClient) fositeClient() fosite.Client {
	return &fosite.DefaultOpenIDConnectClient{
		DefaultClient: &fosite.DefaultClient{
			ID: client.ID, Secret: client.SecretHash, RedirectURIs: client.RedirectURIs,
			GrantTypes: client.GrantTypes, ResponseTypes: client.ResponseTypes,
			Scopes: client.Scopes, Audience: client.Audience, Public: client.Public,
		},
		TokenEndpointAuthMethod: client.TokenEndpointAuthMethod,
	}
}

func (s *Store) ClientName(ctx context.Context, id string) (string, error) {
	if cached, ok := s.dynamicClients.Load(id); ok {
		entry := cached.(cachedClient)
		if entry.expiresAt.After(time.Now()) {
			return entry.client.Name, nil
		}
	}
	var name string
	if err := s.db.QueryRowContext(ctx, `SELECT name FROM oauth_clients WHERE id = ?`, id).Scan(&name); err != nil {
		return "", err
	}
	return name, nil
}

func (s *Store) ClientAssertionJWTValid(ctx context.Context, jti string) error {
	var expires string
	err := s.db.QueryRowContext(ctx, `SELECT expires_at FROM oauth_client_assertions WHERE jti = ?`, jti).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return err
	}
	if expiresAt.After(time.Now()) {
		return fosite.ErrJTIKnown
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM oauth_client_assertions WHERE jti = ?`, jti)
	return err
}

func (s *Store) SetClientAssertionJWT(ctx context.Context, jti string, exp time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO oauth_client_assertions (jti, expires_at) VALUES (?, ?)`, jti, exp.UTC().Format(time.RFC3339Nano))
	return err
}

type persistedRequest struct {
	ID                string                 `json:"id"`
	RequestedAt       time.Time              `json:"requestedAt"`
	ClientID          string                 `json:"clientId"`
	RequestedScope    []string               `json:"requestedScope"`
	GrantedScope      []string               `json:"grantedScope"`
	Form              map[string][]string    `json:"form"`
	Session           *fosite.DefaultSession `json:"session"`
	RequestedAudience []string               `json:"requestedAudience"`
	GrantedAudience   []string               `json:"grantedAudience"`
}

func encodeRequester(request fosite.Requester) (string, error) {
	session, ok := request.GetSession().(*fosite.DefaultSession)
	if !ok {
		return "", fmt.Errorf("unsupported OAuth session type %T", request.GetSession())
	}
	persisted := persistedRequest{
		ID: request.GetID(), RequestedAt: request.GetRequestedAt(), ClientID: request.GetClient().GetID(),
		RequestedScope: append([]string(nil), request.GetRequestedScopes()...),
		GrantedScope:   append([]string(nil), request.GetGrantedScopes()...),
		Form:           map[string][]string{}, Session: session.Clone().(*fosite.DefaultSession),
		RequestedAudience: append([]string(nil), request.GetRequestedAudience()...),
		GrantedAudience:   append([]string(nil), request.GetGrantedAudience()...),
	}
	for key, values := range request.GetRequestForm() {
		persisted.Form[key] = append([]string(nil), values...)
	}
	encoded, err := json.Marshal(persisted)
	return string(encoded), err
}

func (s *Store) decodeRequester(ctx context.Context, raw string) (fosite.Requester, error) {
	var persisted persistedRequest
	if err := json.Unmarshal([]byte(raw), &persisted); err != nil {
		return nil, err
	}
	client, err := s.GetClient(ctx, persisted.ClientID)
	if err != nil {
		return nil, err
	}
	request := fosite.NewRequest()
	request.ID = persisted.ID
	request.RequestedAt = persisted.RequestedAt
	request.Client = client
	request.RequestedScope = fosite.Arguments(persisted.RequestedScope)
	request.GrantedScope = fosite.Arguments(persisted.GrantedScope)
	request.Form = url.Values(persisted.Form)
	request.Session = persisted.Session
	request.RequestedAudience = fosite.Arguments(persisted.RequestedAudience)
	request.GrantedAudience = fosite.Arguments(persisted.GrantedAudience)
	return request, nil
}

type dbRunner interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type transactionKey struct{}

func (s *Store) runner(ctx context.Context) dbRunner {
	if tx, ok := ctx.Value(transactionKey{}).(*sql.Tx); ok {
		return tx
	}
	return s.db
}

func (s *Store) BeginTX(ctx context.Context) (context.Context, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, transactionKey{}, tx), nil
}

func (s *Store) Commit(ctx context.Context) error {
	tx, ok := ctx.Value(transactionKey{}).(*sql.Tx)
	if !ok {
		return fmt.Errorf("OAuth transaction is missing")
	}
	return tx.Commit()
}

func (s *Store) Rollback(ctx context.Context) error {
	tx, ok := ctx.Value(transactionKey{}).(*sql.Tx)
	if !ok {
		return fmt.Errorf("OAuth transaction is missing")
	}
	return tx.Rollback()
}

func (s *Store) createSession(ctx context.Context, kind, signature, accessSignature string, request fosite.Requester) error {
	if err := s.pruneExpiredSessions(ctx); err != nil {
		return err
	}
	raw, err := encodeRequester(request)
	if err != nil {
		return err
	}
	_, err = s.runner(ctx).ExecContext(ctx, `INSERT INTO oauth_sessions
        (kind, signature, request_id, request_json, access_signature) VALUES (?, ?, ?, ?, ?)`,
		kind, signature, request.GetID(), raw, accessSignature)
	return err
}

func (s *Store) pruneExpiredSessions(ctx context.Context) error {
	if s.sessionRetention <= 0 {
		return nil
	}
	now := time.Now().UTC()
	last := s.lastPruneUnix.Load()
	if last != 0 && now.Sub(time.Unix(last, 0)) < time.Hour {
		return nil
	}
	threshold := now.Add(-s.sessionRetention).Format("2006-01-02 15:04:05")
	if _, err := s.runner(ctx).ExecContext(ctx, `DELETE FROM oauth_sessions WHERE created_at < ?`, threshold); err != nil {
		return err
	}
	if _, err := s.runner(ctx).ExecContext(ctx, `DELETE FROM oauth_client_assertions WHERE expires_at < ?`, now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	s.lastPruneUnix.Store(now.Unix())
	return nil
}

func (s *Store) getSession(ctx context.Context, kind, signature string) (fosite.Requester, bool, error) {
	var raw string
	var active int
	err := s.runner(ctx).QueryRowContext(ctx, `SELECT request_json, active FROM oauth_sessions WHERE kind = ? AND signature = ?`, kind, signature).Scan(&raw, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, fosite.ErrNotFound
	}
	if err != nil {
		return nil, false, err
	}
	request, err := s.decodeRequester(ctx, raw)
	return request, active == 1, err
}

func (s *Store) deleteSession(ctx context.Context, kind, signature string) error {
	_, err := s.runner(ctx).ExecContext(ctx, `DELETE FROM oauth_sessions WHERE kind = ? AND signature = ?`, kind, signature)
	return err
}

func (s *Store) CreateAuthorizeCodeSession(ctx context.Context, code string, request fosite.Requester) error {
	return s.createSession(ctx, sessionAuthorizeCode, code, "", request)
}

func (s *Store) GetAuthorizeCodeSession(ctx context.Context, code string, _ fosite.Session) (fosite.Requester, error) {
	request, active, err := s.getSession(ctx, sessionAuthorizeCode, code)
	if err != nil {
		return nil, err
	}
	if !active {
		return request, fosite.ErrInvalidatedAuthorizeCode
	}
	return request, nil
}

func (s *Store) InvalidateAuthorizeCodeSession(ctx context.Context, code string) error {
	result, err := s.runner(ctx).ExecContext(ctx, `UPDATE oauth_sessions SET active = 0 WHERE kind = ? AND signature = ? AND active = 1`, sessionAuthorizeCode, code)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return fosite.ErrNotFound
	}
	return nil
}

func (s *Store) CreateAccessTokenSession(ctx context.Context, signature string, request fosite.Requester) error {
	return s.createSession(ctx, sessionAccessToken, signature, "", request)
}

func (s *Store) GetAccessTokenSession(ctx context.Context, signature string, _ fosite.Session) (fosite.Requester, error) {
	request, active, err := s.getSession(ctx, sessionAccessToken, signature)
	if err != nil {
		return nil, err
	}
	if !active {
		return request, fosite.ErrInactiveToken
	}
	return request, nil
}

func (s *Store) DeleteAccessTokenSession(ctx context.Context, signature string) error {
	return s.deleteSession(ctx, sessionAccessToken, signature)
}

func (s *Store) CreateRefreshTokenSession(ctx context.Context, signature, accessSignature string, request fosite.Requester) error {
	return s.createSession(ctx, sessionRefreshToken, signature, accessSignature, request)
}

func (s *Store) GetRefreshTokenSession(ctx context.Context, signature string, _ fosite.Session) (fosite.Requester, error) {
	request, active, err := s.getSession(ctx, sessionRefreshToken, signature)
	if err != nil {
		return nil, err
	}
	if !active {
		return request, fosite.ErrInactiveToken
	}
	return request, nil
}

func (s *Store) DeleteRefreshTokenSession(ctx context.Context, signature string) error {
	return s.deleteSession(ctx, sessionRefreshToken, signature)
}

func (s *Store) RotateRefreshToken(ctx context.Context, requestID, refreshSignature string) error {
	result, err := s.runner(ctx).ExecContext(ctx, `UPDATE oauth_sessions SET active = 0 WHERE kind = ? AND signature = ? AND request_id = ? AND active = 1`, sessionRefreshToken, refreshSignature, requestID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return fosite.ErrNotFound
	}
	_, err = s.runner(ctx).ExecContext(ctx, `UPDATE oauth_sessions SET active = 0 WHERE kind = ? AND request_id = ?`, sessionAccessToken, requestID)
	return err
}

func (s *Store) RevokeRefreshToken(ctx context.Context, requestID string) error {
	result, err := s.runner(ctx).ExecContext(ctx, `UPDATE oauth_sessions SET active = 0 WHERE kind = ? AND request_id = ? AND active = 1`, sessionRefreshToken, requestID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return fosite.ErrNotFound
	}
	_, err = s.runner(ctx).ExecContext(ctx, `UPDATE oauth_sessions SET active = 0 WHERE kind = ? AND request_id = ?`, sessionAccessToken, requestID)
	return err
}

func (s *Store) RevokeAccessToken(ctx context.Context, requestID string) error {
	result, err := s.runner(ctx).ExecContext(ctx, `UPDATE oauth_sessions SET active = 0 WHERE kind = ? AND request_id = ? AND active = 1`, sessionAccessToken, requestID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return fosite.ErrNotFound
	}
	return nil
}

func (s *Store) CreatePKCERequestSession(ctx context.Context, signature string, request fosite.Requester) error {
	return s.createSession(ctx, sessionPKCE, signature, "", request)
}

func (s *Store) GetPKCERequestSession(ctx context.Context, signature string, _ fosite.Session) (fosite.Requester, error) {
	request, active, err := s.getSession(ctx, sessionPKCE, signature)
	if err != nil {
		return nil, err
	}
	if !active {
		return request, fosite.ErrInactiveToken
	}
	return request, nil
}

func (s *Store) DeletePKCERequestSession(ctx context.Context, signature string) error {
	return s.deleteSession(ctx, sessionPKCE, signature)
}
