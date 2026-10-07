package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/jackc/pgx/v5"
)

// DevelopmentCredentials are issued once to an existing local development
// principal. prepare persists the private recovery bundle before commit.
type DevelopmentCredentials struct {
	Email                   string    `json:"email"`
	Password                string    `json:"password"`
	BootstrapToken          string    `json:"bootstrapToken"`
	PublisherToken          string    `json:"publisherToken"`
	PublisherTokenExpiresAt time.Time `json:"publisherTokenExpiresAt"`
	ClaimedProjectUID       string    `json:"claimedProjectUid,omitempty"`
	ClaimCredentialID       string    `json:"claimCredentialId,omitempty"`
	ClaimAcknowledged       bool      `json:"claimAcknowledged,omitempty"`
}

// ProvisionDevelopmentOperator attaches a real local login and a bounded
// publisher token to an existing development principal without changing its
// durable identity or project roles. Production initialization has its own
// one-shot path; this offline helper cannot replace an existing login.
func (r *Repository) ProvisionDevelopmentOperator(ctx context.Context, principalID string, bootstrapPermissions, publisherPermissions []access.PermissionPair, prepare func(DevelopmentCredentials) error) (DevelopmentCredentials, error) {
	principalID, err := uuidID("principal id", principalID)
	if err != nil {
		return DevelopmentCredentials{}, err
	}
	password, err := tokenSecret("lv_dev_")
	if err != nil {
		return DevelopmentCredentials{}, err
	}
	if err := access.ValidateLocalPassword(password); err != nil {
		return DevelopmentCredentials{}, err
	}
	verifier, err := secretVerifier(password)
	if err != nil {
		return DevelopmentCredentials{}, err
	}
	parsedID, err := pgUUID(principalID)
	if err != nil {
		return DevelopmentCredentials{}, err
	}
	var result DevelopmentCredentials
	err = r.RunAuditedMutationBatch(ctx, func(txRepo access.Repository) ([]access.AuditEventInput, error) {
		tx, ok := txRepo.(*Repository)
		if !ok {
			return nil, errors.New("development credential transaction is unavailable")
		}
		principal, err := tx.PrincipalByID(ctx, principalID)
		if err != nil {
			return nil, err
		}
		if principal.Kind != access.PrincipalKindUser || principal.AccessDisabled() {
			return nil, fmt.Errorf("development login requires an active user principal")
		}
		if _, err := tx.LocalCredential(ctx, principalID); err == nil {
			return nil, access.ErrPrincipalAlreadyExists
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if err := accessdb.New(tx.db).InsertLocalCredential(ctx, accessdb.InsertLocalCredentialParams{PrincipalID: parsedID, Verifier: verifier, MustChange: false}); err != nil {
			return nil, err
		}
		expires := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Second)
		bootstrap, bootstrapMetadata, err := tx.CreateScopedAPITokenWithMetadata(ctx, access.ScopedAPITokenInput{
			PrincipalID: principalID, Name: access.APITokenNameInitialProjectClaim, Permissions: bootstrapPermissions, ExpiresAt: expires,
		})
		if err != nil {
			return nil, err
		}
		token, metadata, err := tx.CreateScopedAPITokenWithMetadata(ctx, access.ScopedAPITokenInput{
			PrincipalID: principalID, Name: "development-publisher", Permissions: publisherPermissions, ExpiresAt: expires,
		})
		if err != nil {
			return nil, err
		}
		result = DevelopmentCredentials{Email: principal.Email, Password: password, BootstrapToken: bootstrap, PublisherToken: token, PublisherTokenExpiresAt: expires}
		if prepare != nil {
			if err := prepare(result); err != nil {
				return nil, err
			}
		}
		return []access.AuditEventInput{
			{PrincipalID: principalID, Action: "principal.local_credential.dev_provisioned", ResourceKind: "principal", ResourceID: principalID, Status: "success"},
			{PrincipalID: principalID, Action: "api_token.created", ResourceKind: "api_token", ResourceID: bootstrapMetadata.ID, Status: "success"},
			{PrincipalID: principalID, Action: "api_token.created", ResourceKind: "api_token", ResourceID: metadata.ID, Status: "success"},
		}, nil
	})
	if err != nil {
		return DevelopmentCredentials{}, err
	}
	return result, nil
}

// RestoreDevelopmentLogin explicitly re-synchronizes a local development
// principal with its existing private credential bundle. It does not issue a
// new password, change roles, or rotate API tokens. As with any password reset,
// existing browser sessions are revoked.
func (r *Repository) RestoreDevelopmentLogin(ctx context.Context, principalID, password string) error {
	if err := access.ValidateLocalPassword(password); err != nil {
		return err
	}
	return r.RunAuditedMutationBatch(ctx, func(tx access.Repository) ([]access.AuditEventInput, error) {
		principal, err := tx.PrincipalByID(ctx, principalID)
		if err != nil {
			return nil, err
		}
		if principal.Kind != access.PrincipalKindUser || principal.AccessDisabled() {
			return nil, errors.New("development login requires an active user principal")
		}
		temporary, err := tx.ResetLocalPassword(ctx, principalID)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ChangeLocalPassword(ctx, principalID, temporary.Password, password); err != nil {
			return nil, err
		}
		return []access.AuditEventInput{{PrincipalID: principalID, Action: "principal.local_credential.dev_restored", ResourceKind: "principal", ResourceID: principalID, Status: "success"}}, nil
	})
}

func (r *Repository) ProvisionDevelopmentPublisherToken(ctx context.Context, principalID string, permissions []access.PermissionPair, replaceID string, prepare func(string) error) error {
	return r.RunAuditedMutationBatch(ctx, func(txRepo access.Repository) ([]access.AuditEventInput, error) {
		tx, ok := txRepo.(*Repository)
		if !ok {
			return nil, errors.New("development credential transaction is unavailable")
		}
		secret, metadata, err := tx.CreateScopedAPITokenWithMetadata(ctx, access.ScopedAPITokenInput{
			PrincipalID: principalID, Name: "development-publisher", Permissions: permissions,
			ExpiresAt: time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Second),
		})
		if err != nil {
			return nil, err
		}
		if err := prepare(secret); err != nil {
			return nil, err
		}
		events := []access.AuditEventInput{{PrincipalID: principalID, Action: "api_token.created", ResourceKind: "api_token", ResourceID: metadata.ID, Status: "success"}}
		if replaceID != "" {
			if err := tx.RevokeAPITokenForPrincipal(ctx, principalID, replaceID); err != nil {
				return nil, err
			}
			events = append(events, access.AuditEventInput{PrincipalID: principalID, Action: "api_token.revoked", ResourceKind: "api_token", ResourceID: replaceID, Status: "success"})
		}
		return events, nil
	})
}

// ProvisionDevelopmentBootstrapToken upgrades an existing private bundle
// without resetting the browser login or silently widening its publisher.
func (r *Repository) ProvisionDevelopmentBootstrapToken(ctx context.Context, principalID string, permissions []access.PermissionPair, replaceID string, prepare func(string) error) error {
	return r.RunAuditedMutationBatch(ctx, func(txRepo access.Repository) ([]access.AuditEventInput, error) {
		tx, ok := txRepo.(*Repository)
		if !ok {
			return nil, errors.New("development credential transaction is unavailable")
		}
		secret, metadata, err := tx.CreateScopedAPITokenWithMetadata(ctx, access.ScopedAPITokenInput{
			PrincipalID: principalID, Name: access.APITokenNameInitialProjectClaim, Permissions: permissions,
			ExpiresAt: time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Second),
		})
		if err != nil {
			return nil, err
		}
		if err := prepare(secret); err != nil {
			return nil, err
		}
		events := []access.AuditEventInput{{PrincipalID: principalID, Action: "api_token.created", ResourceKind: "api_token", ResourceID: metadata.ID, Status: "success"}}
		if replaceID != "" {
			if err := tx.RevokeAPITokenForPrincipal(ctx, principalID, replaceID); err != nil {
				return nil, err
			}
			events = append(events, access.AuditEventInput{PrincipalID: principalID, Action: "api_token.revoked", ResourceKind: "api_token", ResourceID: replaceID, Status: "success"})
		}
		return events, nil
	})
}
