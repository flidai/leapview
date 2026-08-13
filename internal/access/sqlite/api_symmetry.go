package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/access"
	platformdb "github.com/flidai/leapview/internal/access/internal/db"
)

func (r *Repository) UpdateGrant(ctx context.Context, workspaceID, id string, input access.GrantInput) (access.Grant, error) {
	access.ClearAuthorizationCache(ctx)
	if _, err := r.GetGrant(ctx, workspaceID, id); err != nil {
		return access.Grant{}, err
	}
	objectID, err := r.ensureSecurableObject(ctx, input.Object)
	if err != nil {
		return access.Grant{}, err
	}
	result, err := r.q.UpdateGrantByID(ctx, platformdb.UpdateGrantByIDParams{
		ObjectID: objectID, SubjectType: string(input.SubjectType), SubjectID: input.SubjectID,
		Privilege: string(input.Privilege), ID: id,
	})
	if err != nil {
		return access.Grant{}, err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return access.Grant{}, err
		}
		return access.Grant{}, sql.ErrNoRows
	}
	return r.GetGrant(ctx, workspaceID, id)
}

func (r *Repository) DeletePrincipal(ctx context.Context, id string) error {
	access.ClearAuthorizationCache(ctx)
	if _, err := r.PrincipalByID(ctx, id); err != nil {
		return err
	}
	if err := r.preparePrincipalDeletion(ctx, id); err != nil {
		return err
	}
	result, err := r.q.DeletePrincipalByID(ctx, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *Repository) preparePrincipalDeletion(ctx context.Context, id string) error {
	owned, err := r.q.PrincipalOwnsSecurableObject(ctx, id)
	if err != nil {
		return err
	}
	if owned != 0 {
		return access.ErrPrincipalOwnsSecurableObject
	}
	return r.q.DeleteDirectPrincipalGrants(ctx, id)
}

func (r *Repository) DisablePrincipal(ctx context.Context, id string) (access.Principal, error) {
	access.ClearAuthorizationCache(ctx)
	if r == nil {
		return access.Principal{}, fmt.Errorf("access repository database is required")
	}
	if _, inTransaction := r.db.(*sql.Tx); inTransaction {
		return r.disablePrincipal(ctx, id)
	}
	if r.root == nil {
		return access.Principal{}, fmt.Errorf("access repository database is required")
	}
	tx, err := r.root.BeginTx(ctx, nil)
	if err != nil {
		return access.Principal{}, err
	}
	defer func() { _ = tx.Rollback() }()
	txRepo := &Repository{root: r.root, db: tx, q: r.q.WithTx(tx), policyCache: r.policyCache}
	principal, err := txRepo.disablePrincipal(ctx, id)
	if err != nil {
		return access.Principal{}, err
	}
	if err := tx.Commit(); err != nil {
		return access.Principal{}, err
	}
	return principal, nil
}

func (r *Repository) disablePrincipal(ctx context.Context, id string) (access.Principal, error) {
	if _, err := r.PrincipalByID(ctx, id); err != nil {
		return access.Principal{}, err
	}
	if err := r.q.DisablePrincipal(ctx, id); err != nil {
		return access.Principal{}, err
	}
	if err := r.q.RevokeSessionsByPrincipal(ctx, id); err != nil {
		return access.Principal{}, err
	}
	if err := r.q.RevokeAPITokensByPrincipal(ctx, id); err != nil {
		return access.Principal{}, err
	}
	if err := r.q.DeactivateOAuthSessionsByPrincipal(ctx, id); err != nil {
		return access.Principal{}, err
	}
	now := nullableTime(time.Now().UTC())
	if err := r.q.DeactivateAuthoringCredentialsByPrincipal(ctx, platformdb.DeactivateAuthoringCredentialsByPrincipalParams{
		ReplacedAt: now, PrincipalID: id,
	}); err != nil {
		return access.Principal{}, err
	}
	if err := r.q.RevokeAuthoringSessionsByPrincipal(ctx, platformdb.RevokeAuthoringSessionsByPrincipalParams{
		RevokedAt: now, PrincipalID: id,
	}); err != nil {
		return access.Principal{}, err
	}
	return r.PrincipalByID(ctx, id)
}

func (r *Repository) EnablePrincipal(ctx context.Context, id string) (access.Principal, error) {
	access.ClearAuthorizationCache(ctx)
	if _, err := r.PrincipalByID(ctx, id); err != nil {
		return access.Principal{}, err
	}
	if err := r.q.EnablePrincipal(ctx, id); err != nil {
		return access.Principal{}, err
	}
	return r.PrincipalByID(ctx, id)
}

func (r *Repository) ListServicePrincipalSecrets(ctx context.Context, principalID string) ([]access.ServicePrincipalSecret, error) {
	rows, err := r.q.ListServicePrincipalSecretsByPrincipal(ctx, principalID)
	if err != nil {
		return nil, err
	}
	result := make([]access.ServicePrincipalSecret, 0, len(rows))
	for _, row := range rows {
		result = append(result, mapServicePrincipalSecret(row))
	}
	return result, nil
}

func (r *Repository) GetServicePrincipalSecret(ctx context.Context, principalID, secretID string) (access.ServicePrincipalSecret, error) {
	row, err := r.q.GetServicePrincipalSecretByID(ctx, platformdb.GetServicePrincipalSecretByIDParams{
		ServicePrincipalID: principalID, ID: secretID,
	})
	if err != nil {
		return access.ServicePrincipalSecret{}, err
	}
	return mapServicePrincipalSecret(row), nil
}

func mapServicePrincipalSecret(row platformdb.ServicePrincipalSecret) access.ServicePrincipalSecret {
	return access.ServicePrincipalSecret{
		ID: row.ID, ServicePrincipalID: row.ServicePrincipalID, Name: row.Name,
		ExpiresAt: row.ExpiresAt.String, CreatedAt: row.CreatedAt, RevokedAt: row.RevokedAt.String,
	}
}
