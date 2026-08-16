package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/access"
	platformdb "github.com/flidai/leapview/internal/access/internal/db"
)

func (r *Repository) DeletePrincipal(ctx context.Context, id string) error {
	if _, err := r.PrincipalByID(ctx, id); err != nil {
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

func (r *Repository) DisablePrincipal(ctx context.Context, id string) (access.Principal, error) {
	return r.setPrincipalDisabled(ctx, id, false)
}

// DisableProvisionedPrincipal records a lifecycle disable owned by an external
// provisioning subsystem. It is intentionally separate from DisablePrincipal,
// which records a LeapView administrator block.
func (r *Repository) DisableProvisionedPrincipal(ctx context.Context, id string) (access.Principal, error) {
	return r.setPrincipalDisabled(ctx, id, true)
}

func (r *Repository) setPrincipalDisabled(ctx context.Context, id string, provisioned bool) (access.Principal, error) {
	if r == nil {
		return access.Principal{}, fmt.Errorf("access repository database is required")
	}
	if _, inTransaction := r.db.(*sql.Tx); inTransaction {
		return r.disablePrincipal(ctx, id, provisioned)
	}
	if r.root == nil {
		return access.Principal{}, fmt.Errorf("access repository database is required")
	}
	tx, err := r.root.BeginTx(ctx, nil)
	if err != nil {
		return access.Principal{}, err
	}
	defer func() { _ = tx.Rollback() }()
	txRepo := &Repository{root: r.root, db: tx, q: r.q.WithTx(tx)}
	principal, err := txRepo.disablePrincipal(ctx, id, provisioned)
	if err != nil {
		return access.Principal{}, err
	}
	if err := tx.Commit(); err != nil {
		return access.Principal{}, err
	}
	return principal, nil
}

func (r *Repository) disablePrincipal(ctx context.Context, id string, provisioned bool) (access.Principal, error) {
	if _, err := r.PrincipalByID(ctx, id); err != nil {
		return access.Principal{}, err
	}
	var err error
	if provisioned {
		err = r.q.DisableProvisionedPrincipal(ctx, id)
	} else {
		err = r.q.DisablePrincipal(ctx, id)
	}
	if err != nil {
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
