package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	platformdb "github.com/flidai/leapview/internal/access/internal/db"
)

// DisableServicePrincipal is the service-account lifecycle transition. It is
// distinct from DisablePrincipal (which blocks a human principal) and revokes
// all credentials in the same SQLite transaction.
func (r *Repository) DisableServicePrincipal(ctx context.Context, id string) (access.Principal, error) {
	return r.setServicePrincipalEnabled(ctx, id, false)
}

func (r *Repository) EnableServicePrincipal(ctx context.Context, id string) (access.Principal, error) {
	return r.setServicePrincipalEnabled(ctx, id, true)
}

func (r *Repository) setServicePrincipalEnabled(ctx context.Context, id string, enabled bool) (access.Principal, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return access.Principal{}, errors.New("service principal id is required")
	}
	if _, err := r.PrincipalByID(ctx, id); err != nil {
		return access.Principal{}, err
	}
	if principal, err := r.PrincipalByID(ctx, id); err != nil || principal.Kind != access.PrincipalKindServicePrincipal {
		if err != nil {
			return access.Principal{}, err
		}
		return access.Principal{}, sql.ErrNoRows
	}
	if _, inTransaction := r.db.(*sql.Tx); inTransaction {
		return r.setServicePrincipalEnabledTx(ctx, id, enabled)
	}
	if r.root == nil {
		return access.Principal{}, errors.New("access repository database is required")
	}
	tx, err := r.root.BeginTx(ctx, nil)
	if err != nil {
		return access.Principal{}, err
	}
	defer func() { _ = tx.Rollback() }()
	txRepo := &Repository{root: r.root, db: tx, q: r.q.WithTx(tx)}
	principal, err := txRepo.setServicePrincipalEnabledTx(ctx, id, enabled)
	if err != nil {
		return access.Principal{}, err
	}
	if err := tx.Commit(); err != nil {
		return access.Principal{}, err
	}
	return principal, nil
}

func (r *Repository) setServicePrincipalEnabledTx(ctx context.Context, id string, enabled bool) (access.Principal, error) {
	var err error
	if enabled {
		err = r.q.EnableServicePrincipal(ctx, id)
	} else {
		err = r.q.DisableServicePrincipal(ctx, id)
	}
	if err != nil {
		return access.Principal{}, err
	}
	if !enabled {
		if err := r.revokeServicePrincipalCredentials(ctx, id); err != nil {
			return access.Principal{}, err
		}
	}
	return r.PrincipalByID(ctx, id)
}

func (r *Repository) RevokeAllServicePrincipalCredentials(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("service principal id is required")
	}
	principal, err := r.PrincipalByID(ctx, id)
	if err != nil {
		return err
	}
	if principal.Kind != access.PrincipalKindServicePrincipal {
		return sql.ErrNoRows
	}
	if _, inTransaction := r.db.(*sql.Tx); inTransaction {
		return r.revokeServicePrincipalCredentials(ctx, id)
	}
	if r.root == nil {
		return errors.New("access repository database is required")
	}
	tx, err := r.root.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	txRepo := &Repository{root: r.root, db: tx, q: r.q.WithTx(tx)}
	if err := txRepo.revokeServicePrincipalCredentials(ctx, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) revokeServicePrincipalCredentials(ctx context.Context, id string) error {
	if err := r.q.RevokeAPITokensByPrincipal(ctx, id); err != nil {
		return fmt.Errorf("revoke service principal API tokens: %w", err)
	}
	if err := r.q.RevokeServicePrincipalSecretsByPrincipal(ctx, id); err != nil {
		return fmt.Errorf("revoke service principal secrets: %w", err)
	}
	if err := r.revokeInteractiveSessionsByPrincipal(ctx, id); err != nil {
		return fmt.Errorf("revoke service principal sessions: %w", err)
	}
	return nil
}

func (r *Repository) RotateServicePrincipalSecret(ctx context.Context, input access.ServicePrincipalSecretRotationInput) (access.ServicePrincipalSecretRotation, error) {
	principalID := strings.TrimSpace(input.ServicePrincipalID)
	previousID := strings.TrimSpace(input.PreviousSecretID)
	if principalID == "" || previousID == "" {
		return access.ServicePrincipalSecretRotation{}, errors.New("service principal id and previous secret id are required")
	}
	principal, err := r.PrincipalByID(ctx, principalID)
	if err != nil {
		return access.ServicePrincipalSecretRotation{}, err
	}
	if principal.Kind != access.PrincipalKindServicePrincipal {
		return access.ServicePrincipalSecretRotation{}, sql.ErrNoRows
	}
	if _, inTransaction := r.db.(*sql.Tx); inTransaction {
		return r.rotateServicePrincipalSecretTx(ctx, input)
	}
	if r.root == nil {
		return access.ServicePrincipalSecretRotation{}, errors.New("access repository database is required")
	}
	tx, err := r.root.BeginTx(ctx, nil)
	if err != nil {
		return access.ServicePrincipalSecretRotation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	txRepo := &Repository{root: r.root, db: tx, q: r.q.WithTx(tx)}
	rotation, err := txRepo.rotateServicePrincipalSecretTx(ctx, input)
	if err != nil {
		return access.ServicePrincipalSecretRotation{}, err
	}
	if err := tx.Commit(); err != nil {
		return access.ServicePrincipalSecretRotation{}, err
	}
	return rotation, nil
}

func (r *Repository) rotateServicePrincipalSecretTx(ctx context.Context, input access.ServicePrincipalSecretRotationInput) (access.ServicePrincipalSecretRotation, error) {
	previousRow, err := r.q.GetServicePrincipalSecretByID(ctx, platformdb.GetServicePrincipalSecretByIDParams{ServicePrincipalID: input.ServicePrincipalID, ID: input.PreviousSecretID})
	if err != nil {
		return access.ServicePrincipalSecretRotation{}, err
	}
	if previousRow.RevokedAt.Valid {
		return access.ServicePrincipalSecretRotation{}, errors.New("previous service principal secret is already revoked")
	}
	secret, created, err := r.CreateServicePrincipalSecret(ctx, input.ServicePrincipalID, input.Secret)
	if err != nil {
		return access.ServicePrincipalSecretRotation{}, err
	}
	previous := mapServicePrincipalSecret(previousRow)
	if input.RevokePrevious {
		if err := r.q.RevokeServicePrincipalSecret(ctx, platformdb.RevokeServicePrincipalSecretParams{ServicePrincipalID: input.ServicePrincipalID, ID: input.PreviousSecretID}); err != nil {
			return access.ServicePrincipalSecretRotation{}, err
		}
		previous.RevokedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return access.ServicePrincipalSecretRotation{Secret: secret, Created: created, Previous: previous}, nil
}
