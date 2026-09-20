package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const serviceSecretTouchInterval = time.Minute

// DisableServicePrincipal suspends a service identity without tombstoning it.
// All currently issued credentials are revoked in the same transaction and
// therefore cannot return if the identity is later enabled.
func (r *Repository) DisableServicePrincipal(ctx context.Context, id string) (access.Principal, error) {
	return r.setServicePrincipalEnabled(ctx, id, false)
}

// EnableServicePrincipal re-enables only the identity. Credentials revoked by
// disablement are never restored; operators must issue a new secret after
// verifying the account's role assignments.
func (r *Repository) EnableServicePrincipal(ctx context.Context, id string) (access.Principal, error) {
	return r.setServicePrincipalEnabled(ctx, id, true)
}

func (r *Repository) setServicePrincipalEnabled(ctx context.Context, id string, enabled bool) (access.Principal, error) {
	if ctx == nil {
		return access.Principal{}, errors.New("service principal context is nil")
	}
	canonical, err := uuidID("service principal id", id)
	if err != nil {
		return access.Principal{}, err
	}
	_, err = r.requireDB()
	if err != nil {
		return access.Principal{}, err
	}
	parsedID, err := pgUUID(canonical)
	if err != nil {
		return access.Principal{}, err
	}
	tx, owned, err := r.txOrBegin(ctx)
	if err != nil {
		return access.Principal{}, err
	}
	if owned {
		defer func() { _ = tx.Rollback(ctx) }()
	}
	queries := accessdb.New(tx)
	var tag pgconnCommandTag
	if enabled {
		tag, err = queries.EnableServicePrincipal(ctx, parsedID)
	} else {
		tag, err = queries.SuspendServicePrincipal(ctx, parsedID)
	}
	if err != nil {
		return access.Principal{}, err
	}
	if tag.RowsAffected() != 1 {
		return access.Principal{}, pgx.ErrNoRows
	}
	if !enabled {
		if err = revokeServicePrincipalCredentials(ctx, tx, parsedID); err != nil {
			return access.Principal{}, err
		}
	}
	if owned {
		if err = tx.Commit(ctx); err != nil {
			return access.Principal{}, err
		}
	}
	// Read through the same transaction when composed inside an audited
	// mutation; a second connection would otherwise observe pre-commit state.
	reader := r
	if !owned {
		reader = &Repository{db: tx, fingerprintKey: r.fingerprintKey}
	}
	return reader.PrincipalByID(ctx, canonical)
}

// RevokeAllServicePrincipalCredentials invalidates every bearer/session
// credential class owned by one service principal. It is deliberately
// idempotent and transaction-scoped so the incident-response operation can be
// retried safely and audited by its caller.
func (r *Repository) RevokeAllServicePrincipalCredentials(ctx context.Context, id string) error {
	if ctx == nil {
		return errors.New("service principal context is nil")
	}
	canonical, err := uuidID("service principal id", id)
	if err != nil {
		return err
	}
	if _, err := r.requireDB(); err != nil {
		return err
	}
	parsedID, err := pgUUID(canonical)
	if err != nil {
		return err
	}
	tx, owned, err := r.txOrBegin(ctx)
	if err != nil {
		return err
	}
	if owned {
		defer func() { _ = tx.Rollback(ctx) }()
	}
	if err := ensureServicePrincipal(ctx, tx, parsedID); err != nil {
		return err
	}
	if err := revokeServicePrincipalCredentials(ctx, tx, parsedID); err != nil {
		return err
	}
	if owned {
		return tx.Commit(ctx)
	}
	return nil
}

func ensureServicePrincipal(ctx context.Context, db DBTX, id pgtype.UUID) error {
	row, err := accessdb.New(db).GetPrincipal(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return pgx.ErrNoRows
	}
	if err != nil {
		return err
	}
	if row.PrincipalType != "service" || !row.ID.Valid {
		return pgx.ErrNoRows
	}
	return nil
}

func revokeServicePrincipalCredentials(ctx context.Context, db DBTX, principalID pgtype.UUID) error {
	queries := accessdb.New(db)
	if err := queries.RevokePrincipalSessions(ctx, principalID); err != nil {
		return fmt.Errorf("revoke service principal sessions: %w", err)
	}
	if err := queries.RevokePrincipalTokens(ctx, principalID); err != nil {
		return fmt.Errorf("revoke service principal API tokens: %w", err)
	}
	if err := queries.RevokePrincipalSecrets(ctx, principalID); err != nil {
		return fmt.Errorf("revoke service principal secrets: %w", err)
	}
	if err := queries.RevokePrincipalAuthoringSessions(ctx, principalID); err != nil {
		return fmt.Errorf("revoke service principal authoring sessions: %w", err)
	}
	if err := queries.RevokePrincipalOAuthSessions(ctx, principalID); err != nil {
		return fmt.Errorf("revoke service principal OAuth sessions: %w", err)
	}
	return nil
}

// RotateServicePrincipalSecret issues a new one-time secret while retaining
// the previous one by default. RevokePrevious makes replacement atomic: both
// creation and invalidation commit or roll back together.
func (r *Repository) RotateServicePrincipalSecret(ctx context.Context, input access.ServicePrincipalSecretRotationInput) (access.ServicePrincipalSecretRotation, error) {
	if ctx == nil {
		return access.ServicePrincipalSecretRotation{}, errors.New("service principal context is nil")
	}
	principalID, err := uuidID("service principal id", input.ServicePrincipalID)
	if err != nil {
		return access.ServicePrincipalSecretRotation{}, err
	}
	previousID, err := uuidID("previous secret id", input.PreviousSecretID)
	if err != nil {
		return access.ServicePrincipalSecretRotation{}, err
	}
	if _, err := r.requireDB(); err != nil {
		return access.ServicePrincipalSecretRotation{}, err
	}
	parsedPrincipalID, err := pgUUID(principalID)
	if err != nil {
		return access.ServicePrincipalSecretRotation{}, err
	}
	parsedPreviousID, err := pgUUID(previousID)
	if err != nil {
		return access.ServicePrincipalSecretRotation{}, err
	}
	tx, owned, err := r.txOrBegin(ctx)
	if err != nil {
		return access.ServicePrincipalSecretRotation{}, err
	}
	if owned {
		defer func() { _ = tx.Rollback(ctx) }()
	}
	if err := ensureServicePrincipal(ctx, tx, parsedPrincipalID); err != nil {
		return access.ServicePrincipalSecretRotation{}, err
	}
	previousRow, err := accessdb.New(tx).GetServiceSecretForPrincipal(ctx, accessdb.GetServiceSecretForPrincipalParams{ID: parsedPreviousID, PrincipalID: parsedPrincipalID})
	if err != nil {
		return access.ServicePrincipalSecretRotation{}, err
	}
	previous := access.ServicePrincipalSecret{ID: principalUUID(previousRow.ID), ServicePrincipalID: principalUUID(previousRow.ServicePrincipalID), Name: previousRow.Name,
		ExpiresAt: principalTimestamp(previousRow.ExpiresAt), CreatedAt: principalTimestamp(previousRow.CreatedAt), LastUsedAt: principalTimestamp(previousRow.LastUsedAt), RevokedAt: principalTimestamp(previousRow.RevokedAt)}
	if previous.RevokedAt != "" {
		return access.ServicePrincipalSecretRotation{}, fmt.Errorf("previous service principal secret is already revoked")
	}
	secret, created, err := (&Repository{db: tx, fingerprintKey: r.fingerprintKey}).CreateServicePrincipalSecret(ctx, principalID, input.Secret)
	if err != nil {
		return access.ServicePrincipalSecretRotation{}, err
	}
	if input.RevokePrevious {
		tag, revokeErr := accessdb.New(tx).RevokeServiceSecret(ctx, accessdb.RevokeServiceSecretParams{ID: parsedPreviousID, PrincipalID: parsedPrincipalID})
		if revokeErr != nil {
			return access.ServicePrincipalSecretRotation{}, revokeErr
		}
		if tag.RowsAffected() != 1 {
			return access.ServicePrincipalSecretRotation{}, pgx.ErrNoRows
		}
		previous.RevokedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if owned {
		if err := tx.Commit(ctx); err != nil {
			return access.ServicePrincipalSecretRotation{}, err
		}
	}
	return access.ServicePrincipalSecretRotation{Secret: secret, Created: created, Previous: previous}, nil
}

func (r *Repository) touchServicePrincipalSecret(ctx context.Context, id pgtype.UUID) {
	tag, err := accessdb.New(r.db).TouchServiceSecret(ctx, accessdb.TouchServiceSecretParams{ID: id, MinInterval: pgInterval(serviceSecretTouchInterval)})
	if err != nil {
		slog.Default().WarnContext(ctx, "service principal secret last-used update failed", "credential_class", "service_principal_secret", "credential_id", principalUUID(id), "error", err)
		return
	}
	_ = tag
}
