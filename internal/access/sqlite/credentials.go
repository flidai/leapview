package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/flidai/leapview/internal/access"
	platformdb "github.com/flidai/leapview/internal/access/internal/db"
	"strings"
	"time"
)

func (r *Repository) CreateSession(ctx context.Context, principalID string, ttl time.Duration) (string, error) {
	token, err := newSecret()
	if err != nil {
		return "", err
	}
	fingerprint := secretFingerprint(token)
	verifier, err := newSecretVerifier(token)
	if err != nil {
		return "", err
	}
	id, err := newID("session")
	if err != nil {
		return "", err
	}
	expires := time.Now().Add(ttl).UTC().Format(time.RFC3339)
	return token, r.q.CreateSession(ctx, platformdb.CreateSessionParams{
		ID:               id,
		PrincipalID:      principalID,
		TokenFingerprint: fingerprint,
		TokenVerifier:    verifier,
		ExpiresAt:        expires,
	})
}

func (r *Repository) PrincipalForToken(ctx context.Context, token string) (access.Principal, error) {
	session, err := r.sessionForToken(ctx, token)
	if err != nil {
		return access.Principal{}, err
	}
	row, err := r.q.GetPrincipal(ctx, session.PrincipalID)
	if err != nil {
		return access.Principal{}, err
	}
	principal := mapPrincipal(row)
	if principal.DisabledAt != "" {
		return access.Principal{}, sql.ErrNoRows
	}
	return principal, nil
}

func (r *Repository) DisabledPrincipalForSessionToken(ctx context.Context, token string) (string, string, error) {
	session, err := r.sessionForAuditToken(ctx, token)
	if err != nil {
		return "", "", err
	}
	row, err := r.q.GetPrincipal(ctx, session.PrincipalID)
	if err != nil {
		return "", "", err
	}
	principal := mapPrincipal(row)
	if principal.DisabledAt == "" {
		return "", "", sql.ErrNoRows
	}
	return principal.ID, session.ID, nil
}

func (r *Repository) sessionForAuditToken(ctx context.Context, token string) (platformdb.Session, error) {
	fingerprint := secretFingerprint(token)
	session, err := r.q.GetSessionByTokenFingerprintForAudit(ctx, fingerprint)
	if err != nil {
		return platformdb.Session{}, err
	}
	if !verifySecret(token, session.TokenVerifier) {
		return platformdb.Session{}, sql.ErrNoRows
	}
	return session, nil
}

func (r *Repository) sessionForToken(ctx context.Context, token string) (platformdb.Session, error) {
	fingerprint := secretFingerprint(token)
	session, err := r.q.GetSessionByTokenFingerprint(ctx, fingerprint)
	if err != nil {
		return platformdb.Session{}, err
	}
	if !verifySecret(token, session.TokenVerifier) {
		return platformdb.Session{}, sql.ErrNoRows
	}
	_ = r.q.TouchSession(ctx, session.ID)
	return session, nil
}

func (r *Repository) CredentialForSessionToken(
	ctx context.Context,
	token string,
) (access.Session, error) {
	session, err := r.sessionForToken(ctx, token)
	if err != nil {
		return access.Session{}, err
	}
	return mapSession(session), nil
}

func (r *Repository) DeleteSession(ctx context.Context, token string) error {
	fingerprint := secretFingerprint(token)
	return r.q.DeleteSessionByTokenFingerprint(ctx, fingerprint)
}

func (r *Repository) ListSessions(ctx context.Context, principalID string) ([]access.Session, error) {
	rows, err := r.q.ListSessionsByPrincipal(ctx, principalID)
	if err != nil {
		return nil, err
	}
	sessions := make([]access.Session, 0, len(rows))
	for _, row := range rows {
		sessions = append(sessions, mapListedSession(row))
	}
	return sessions, nil
}

func (r *Repository) RevokeSession(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("session id is required")
	}
	return r.q.RevokeSession(ctx, id)
}

func (r *Repository) RevokeSessionForPrincipal(ctx context.Context, principalID, id string) error {
	if strings.TrimSpace(principalID) == "" || strings.TrimSpace(id) == "" {
		return fmt.Errorf("principal id and session id are required")
	}
	_, err := r.q.RevokeSessionForPrincipal(ctx, platformdb.RevokeSessionForPrincipalParams{
		PrincipalID: principalID,
		ID:          id,
	})
	return err
}

func (r *Repository) CreateAPIToken(ctx context.Context, principalID, name string) (string, error) {
	token, _, err := r.CreateAPITokenWithMetadata(ctx, access.APITokenInput{
		PrincipalID: principalID,
		Name:        name,
	})
	return token, err
}

func (r *Repository) CreateAPITokenWithMetadata(ctx context.Context, input access.APITokenInput) (string, access.APIToken, error) {
	if strings.TrimSpace(input.PrincipalID) == "" {
		return "", access.APIToken{}, fmt.Errorf("principal id is required")
	}
	if strings.TrimSpace(input.Name) == "" {
		return "", access.APIToken{}, fmt.Errorf("token name is required")
	}
	token, err := newSecret()
	if err != nil {
		return "", access.APIToken{}, err
	}
	id, err := newID("token")
	if err != nil {
		return "", access.APIToken{}, err
	}
	privilegesJSON, err := json.Marshal(input.Privileges)
	if err != nil {
		return "", access.APIToken{}, err
	}
	if input.ExpiresAt.IsZero() {
		input.ExpiresAt = time.Now().Add(defaultAPITokenTTL)
	}
	if !input.ExpiresAt.After(time.Now()) {
		return "", access.APIToken{}, fmt.Errorf("api token expiry must be in the future")
	}
	expiresAt := sql.NullString{}
	if !input.ExpiresAt.IsZero() {
		expiresAt = sql.NullString{String: input.ExpiresAt.UTC().Format(time.RFC3339), Valid: true}
	}
	fingerprint := secretFingerprint(token)
	verifier, err := newSecretVerifier(token)
	if err != nil {
		return "", access.APIToken{}, err
	}
	if err := r.q.CreateAPIToken(ctx, platformdb.CreateAPITokenParams{
		ID:               id,
		PrincipalID:      input.PrincipalID,
		WorkspaceID:      sql.NullString{String: input.WorkspaceID, Valid: strings.TrimSpace(input.WorkspaceID) != ""},
		Name:             input.Name,
		TokenFingerprint: fingerprint,
		TokenVerifier:    verifier,
		PrivilegesJson:   string(privilegesJSON),
		ExpiresAt:        expiresAt,
	}); err != nil {
		return "", access.APIToken{}, err
	}
	tokens, err := r.q.ListAPITokensByPrincipal(ctx, input.PrincipalID)
	if err != nil {
		return "", access.APIToken{}, err
	}
	for _, row := range tokens {
		if row.ID == id {
			return token, mapAPIToken(row), nil
		}
	}
	return token, access.APIToken{ID: id, PrincipalID: input.PrincipalID, WorkspaceID: input.WorkspaceID, Name: input.Name, Privileges: input.Privileges, ExpiresAt: nullString(expiresAt)}, nil
}

func (r *Repository) PrincipalForAPIToken(ctx context.Context, token string) (access.Principal, error) {
	credential, err := r.CredentialForAPIToken(ctx, token)
	if err != nil {
		return access.Principal{}, err
	}
	return credential.Principal, nil
}

func (r *Repository) CredentialForAPIToken(ctx context.Context, token string) (access.APICredential, error) {
	apiToken, err := r.apiTokenForSecret(ctx, token)
	if err != nil {
		return access.APICredential{}, err
	}
	row, err := r.q.GetPrincipal(ctx, apiToken.PrincipalID)
	if err != nil {
		return access.APICredential{}, err
	}
	principal := mapPrincipal(row)
	if principal.DisabledAt != "" {
		return access.APICredential{}, sql.ErrNoRows
	}
	return access.APICredential{
		Principal: principal,
		Token:     mapAPIToken(apiToken),
	}, nil
}

func (r *Repository) DisabledPrincipalForAPIToken(ctx context.Context, token string) (string, string, error) {
	apiToken, err := r.apiTokenForAuditSecret(ctx, token)
	if err != nil {
		return "", "", err
	}
	row, err := r.q.GetPrincipal(ctx, apiToken.PrincipalID)
	if err != nil {
		return "", "", err
	}
	principal := mapPrincipal(row)
	if principal.DisabledAt == "" {
		return "", "", sql.ErrNoRows
	}
	return principal.ID, apiToken.ID, nil
}

func (r *Repository) apiTokenForAuditSecret(ctx context.Context, token string) (platformdb.ApiToken, error) {
	fingerprint := secretFingerprint(token)
	apiToken, err := r.q.GetAPITokenByFingerprintForAudit(ctx, fingerprint)
	if err != nil {
		return platformdb.ApiToken{}, err
	}
	if !verifySecret(token, apiToken.TokenVerifier) {
		return platformdb.ApiToken{}, sql.ErrNoRows
	}
	return apiToken, nil
}

func (r *Repository) apiTokenForSecret(ctx context.Context, token string) (platformdb.ApiToken, error) {
	fingerprint := secretFingerprint(token)
	apiToken, err := r.q.GetAPITokenByFingerprint(ctx, fingerprint)
	if err != nil {
		return platformdb.ApiToken{}, err
	}
	if !verifySecret(token, apiToken.TokenVerifier) {
		return platformdb.ApiToken{}, sql.ErrNoRows
	}
	_ = r.q.TouchAPIToken(ctx, apiToken.ID)
	return apiToken, nil
}

func (r *Repository) ListAPITokens(ctx context.Context, principalID string) ([]access.APIToken, error) {
	rows, err := r.q.ListAPITokensByPrincipal(ctx, principalID)
	if err != nil {
		return nil, err
	}
	tokens := make([]access.APIToken, 0, len(rows))
	for _, row := range rows {
		tokens = append(tokens, mapAPIToken(row))
	}
	return tokens, nil
}

func (r *Repository) RevokeAPIToken(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("api token id is required")
	}
	return r.q.RevokeAPIToken(ctx, id)
}

func (r *Repository) RevokeAPITokenForPrincipal(ctx context.Context, principalID, id string) error {
	if strings.TrimSpace(principalID) == "" || strings.TrimSpace(id) == "" {
		return fmt.Errorf("principal id and api token id are required")
	}
	_, err := r.q.RevokeAPITokenForPrincipal(ctx, platformdb.RevokeAPITokenForPrincipalParams{
		PrincipalID: principalID,
		ID:          id,
	})
	return err
}

func (r *Repository) CreateServicePrincipal(ctx context.Context, input access.ServicePrincipalInput) (access.Principal, error) {
	access.ClearAuthorizationCache(ctx)
	id := strings.TrimSpace(input.ID)
	if id == "" {
		generatedID, err := newID("sp")
		if err != nil {
			return access.Principal{}, err
		}
		id = generatedID
	}
	displayName := strings.TrimSpace(input.DisplayName)
	if displayName == "" {
		displayName = id
	}
	return r.UpsertPrincipal(ctx, access.PrincipalInput{
		ID:          id,
		Kind:        access.PrincipalKindServicePrincipal,
		DisplayName: displayName,
	})
}

func (r *Repository) ListServicePrincipals(ctx context.Context) ([]access.Principal, error) {
	rows, err := r.q.ListServicePrincipals(ctx)
	if err != nil {
		return nil, err
	}
	principals := make([]access.Principal, 0, len(rows))
	for _, row := range rows {
		principals = append(principals, mapPrincipal(row))
	}
	return principals, nil
}

func (r *Repository) UpdateServicePrincipal(ctx context.Context, id string, input access.ServicePrincipalInput) (access.Principal, error) {
	access.ClearAuthorizationCache(ctx)
	id = strings.TrimSpace(id)
	if id == "" {
		return access.Principal{}, fmt.Errorf("service principal id is required")
	}
	existing, err := r.q.GetPrincipal(ctx, id)
	if err != nil {
		return access.Principal{}, err
	}
	if access.PrincipalKind(existing.Kind) != access.PrincipalKindServicePrincipal {
		return access.Principal{}, fmt.Errorf("principal %q is not a service principal", id)
	}
	displayName := firstNonEmpty(strings.TrimSpace(input.DisplayName), existing.DisplayName, id)
	return r.UpsertPrincipal(ctx, access.PrincipalInput{
		ID:          id,
		Kind:        access.PrincipalKindServicePrincipal,
		DisplayName: displayName,
	})
}

func (r *Repository) DeleteServicePrincipal(ctx context.Context, id string) error {
	access.ClearAuthorizationCache(ctx)
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("service principal id is required")
	}
	principal, err := r.PrincipalByID(ctx, id)
	if err != nil {
		return err
	}
	if principal.Kind != access.PrincipalKindServicePrincipal {
		return sql.ErrNoRows
	}
	if err := r.preparePrincipalDeletion(ctx, id); err != nil {
		return err
	}
	return r.q.DeleteServicePrincipal(ctx, id)
}

func (r *Repository) CreateServicePrincipalSecret(ctx context.Context, servicePrincipalID string, input access.ServicePrincipalSecretInput) (string, access.ServicePrincipalSecret, error) {
	servicePrincipalID = strings.TrimSpace(servicePrincipalID)
	if servicePrincipalID == "" {
		return "", access.ServicePrincipalSecret{}, fmt.Errorf("service principal id is required")
	}
	principal, err := r.q.GetPrincipal(ctx, servicePrincipalID)
	if err != nil {
		return "", access.ServicePrincipalSecret{}, err
	}
	if access.PrincipalKind(principal.Kind) != access.PrincipalKindServicePrincipal {
		return "", access.ServicePrincipalSecret{}, fmt.Errorf("principal %q is not a service principal", servicePrincipalID)
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = "default"
	}
	expiresAt := input.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(defaultServicePrincipalSecretTTL)
	}
	if !expiresAt.After(time.Now()) {
		return "", access.ServicePrincipalSecret{}, fmt.Errorf("service principal secret expiry must be in the future")
	}
	expiresAtValue := sql.NullString{String: expiresAt.UTC().Format(time.RFC3339), Valid: true}
	secret, err := newSecret()
	if err != nil {
		return "", access.ServicePrincipalSecret{}, err
	}
	fingerprint := secretFingerprint(secret)
	verifier, err := newSecretVerifier(secret)
	if err != nil {
		return "", access.ServicePrincipalSecret{}, err
	}
	id, err := newID("spsecret")
	if err != nil {
		return "", access.ServicePrincipalSecret{}, err
	}
	row := access.ServicePrincipalSecret{
		ID:                 id,
		ServicePrincipalID: servicePrincipalID,
		Name:               name,
		ExpiresAt:          expiresAtValue.String,
	}
	if err := r.q.CreateServicePrincipalSecret(ctx, platformdb.CreateServicePrincipalSecretParams{
		ID:                 row.ID,
		ServicePrincipalID: row.ServicePrincipalID,
		Name:               row.Name,
		SecretFingerprint:  fingerprint,
		SecretVerifier:     verifier,
		ExpiresAt:          expiresAtValue,
	}); err != nil {
		return "", access.ServicePrincipalSecret{}, err
	}
	return secret, row, nil
}

func (r *Repository) RevokeServicePrincipalSecret(ctx context.Context, servicePrincipalID, secretID string) error {
	if strings.TrimSpace(servicePrincipalID) == "" || strings.TrimSpace(secretID) == "" {
		return fmt.Errorf("service principal id and secret id are required")
	}
	return r.q.RevokeServicePrincipalSecret(ctx, platformdb.RevokeServicePrincipalSecretParams{
		ServicePrincipalID: servicePrincipalID,
		ID:                 secretID,
	})
}

func (r *Repository) PrincipalForServicePrincipalSecret(ctx context.Context, servicePrincipalID, secret string) (access.Principal, error) {
	row, err := r.servicePrincipalSecretForSecret(ctx, servicePrincipalID, secret)
	if err != nil {
		return access.Principal{}, err
	}
	principal, err := r.q.GetPrincipal(ctx, row.ServicePrincipalID)
	if err != nil {
		return access.Principal{}, err
	}
	mapped := mapPrincipal(principal)
	if mapped.DisabledAt != "" {
		return access.Principal{}, sql.ErrNoRows
	}
	return mapped, nil
}

func (r *Repository) servicePrincipalSecretForSecret(ctx context.Context, servicePrincipalID, secret string) (platformdb.ServicePrincipalSecret, error) {
	servicePrincipalID = strings.TrimSpace(servicePrincipalID)
	fingerprint := secretFingerprint(secret)
	row, err := r.q.GetServicePrincipalSecretByFingerprint(ctx, platformdb.GetServicePrincipalSecretByFingerprintParams{
		ServicePrincipalID: servicePrincipalID,
		SecretFingerprint:  fingerprint,
	})
	if err != nil {
		return platformdb.ServicePrincipalSecret{}, err
	}
	if !verifySecret(secret, row.SecretVerifier) {
		return platformdb.ServicePrincipalSecret{}, sql.ErrNoRows
	}
	return row, nil
}
