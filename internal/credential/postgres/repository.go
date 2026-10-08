// Package postgres persists encrypted credential drafts in PostgreSQL. It
// stores logical metadata separately from ciphertext and never decrypts a
// credential.
package postgres

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/credential"
	"github.com/flidai/leapview/internal/credential/encryption"
	credentialdb "github.com/flidai/leapview/internal/credential/postgres/internal/db"
	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Tx is the pgx transaction supplied to Access for the same-transaction audit.
type Tx = pgx.Tx

// AuditRepository appends the redacted audit intent through the exact
// transaction used to save the draft.
type AuditRepository interface {
	RecordAuditEvent(context.Context, Tx, access.AuditIntent) error
}

// Repository is the production draft store and encryption budget authority.
type Repository struct {
	db    *pgxpool.Pool
	audit AuditRepository
}

var _ credential.Repository = (*Repository)(nil)

//go:embed schema.sql request_schema.sql rotation_schema.sql
var schemaFS embed.FS

var schemaSQL = credentialSchemaSQL(schemaFS)

// SchemaSQL returns the capability-owned schema used by focused PostgreSQL
// tests and controlled schema preparation.
func SchemaSQL() string { return schemaSQL }

// ApplySchema executes the capability schema in a caller-owned transaction.
func ApplySchema(ctx context.Context, tx Tx) error {
	if ctx == nil || tx == nil {
		return errors.New("credential PostgreSQL transaction and context are required")
	}
	// sqlc-exception: schema-ddl. The migration runner owns transaction
	// boundaries while this capability owns its schema and role policy.
	_, err := tx.Exec(ctx, schemaSQL)
	return err
}

// New constructs the production repository over a pool, which gives
// ReserveEncryption an independent autocommit statement and SaveDraft its own
// transaction. The Access audit port is mandatory.
func New(db *pgxpool.Pool, audit AuditRepository) (*Repository, error) {
	if db == nil || typednil.IsNil(audit) {
		return nil, credential.ErrUnavailable
	}
	return &Repository{db: db, audit: audit}, nil
}

// ReserveEncryption irreversibly consumes one use for this deployment/key
// pair. The single SQL statement runs on the repository's independent handle,
// so a later SaveDraft rollback cannot reclaim the reservation.
func (r *Repository) ReserveEncryption(ctx context.Context, deploymentID, keyID string, commitment encryption.KeyCommitment) error {
	if r == nil || r.db == nil || ctx == nil {
		return credential.ErrUnavailable
	}
	if !canonical(deploymentID, 255) || !canonical(keyID, 255) || commitment == (encryption.KeyCommitment{}) {
		return credential.ErrInvalid
	}
	_, err := credentialdb.New(r.db).ReserveEncryptionBudget(ctx, credentialdb.ReserveEncryptionBudgetParams{
		DeploymentID: deploymentID, KeyID: keyID, KeyCommitment: commitment[:],
	})
	if errors.Is(err, pgx.ErrNoRows) {
		stored, lookupErr := credentialdb.New(r.db).GetEncryptionBudgetKey(ctx, credentialdb.GetEncryptionBudgetKeyParams{DeploymentID: deploymentID, KeyID: keyID})
		if lookupErr != nil {
			return normalizeDatabaseError(lookupErr)
		}
		if !bytes.Equal(stored.KeyCommitment, commitment[:]) {
			return fmt.Errorf("%w: credential key ID is bound to different key material", credential.ErrConflict)
		}
		if stored.Uses >= int64(encryption.MaxEncryptions) {
			return encryption.ErrBudgetExhausted
		}
		return fmt.Errorf("%w: credential encryption reservation was not recorded", credential.ErrUnavailable)
	}
	return normalizeDatabaseError(err)
}

// CheckKeyring verifies that stored envelopes retain their named keys and that
// every previously reserved key ID still names the same random key material.
func (r *Repository) CheckKeyring(ctx context.Context, keys *encryption.Keyring) error {
	if r == nil || r.db == nil || ctx == nil || keys == nil || !canonical(keys.DeploymentID(), 255) {
		return credential.ErrUnavailable
	}
	rows, err := credentialdb.New(r.db).ListEncryptionBudgetKeys(ctx, keys.DeploymentID())
	if err != nil {
		return normalizeDatabaseError(err)
	}
	storedByCommitment := make(map[string]string, len(rows))
	for _, row := range rows {
		if len(row.KeyCommitment) != len(encryption.KeyCommitment{}) {
			return fmt.Errorf("%w: stored credential key commitment is invalid", credential.ErrConflict)
		}
		storedByCommitment[string(row.KeyCommitment)] = row.KeyID
		commitment, exists := keys.KeyCommitment(row.KeyID)
		if !exists {
			if row.HasEnvelope {
				return fmt.Errorf("%w: keyring is missing a key required by saved credential drafts", credential.ErrUnavailable)
			}
			continue
		}
		if len(row.KeyCommitment) != len(commitment) || !bytes.Equal(row.KeyCommitment, commitment[:]) {
			return fmt.Errorf("%w: credential key ID is bound to different key material", credential.ErrConflict)
		}
	}
	for _, entry := range keys.KeyCommitments() {
		if storedKeyID, exists := storedByCommitment[string(entry.Commitment[:])]; exists && storedKeyID != entry.KeyID {
			return fmt.Errorf("%w: credential key material is already bound to a different key ID", credential.ErrConflict)
		}
	}
	return nil
}

// SaveDraft inserts immutable logical metadata and its encrypted envelope,
// then appends a redacted audit intent in one PostgreSQL transaction.
func (r *Repository) SaveDraft(ctx context.Context, stored credential.StoredVersion, intent access.AuditIntent) error {
	if r == nil || r.db == nil || r.audit == nil || ctx == nil {
		return credential.ErrUnavailable
	}
	if err := validateStoredVersion(stored); err != nil {
		return err
	}
	canonicalIntent, err := validateDraftAudit(stored.Metadata, intent)
	if err != nil {
		return err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return normalizeDatabaseError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	binding := stored.Metadata.Binding
	if err := credentialdb.New(tx).InsertDraftVersion(ctx, credentialdb.InsertDraftVersionParams{
		VersionID: binding.VersionID, DeploymentID: binding.DeploymentID, OwnerID: binding.OwnerID,
		ScopeKind: binding.ScopeKind, TargetID: binding.TargetID, ProjectID: binding.ProjectID,
		Environment: binding.Environment, ResourceID: binding.ResourceID, Purpose: binding.Purpose,
		Provider: binding.Provider, Destination: binding.Destination, ActorID: stored.Metadata.ActorID,
		CreatedAt: stored.Metadata.CreatedAt,
	}); err != nil {
		return normalizeDatabaseError(err)
	}
	if err := credentialdb.New(tx).InsertDraftEnvelope(ctx, credentialdb.InsertDraftEnvelopeParams{
		VersionID: binding.VersionID, DeploymentID: binding.DeploymentID, KeyID: stored.Envelope.KeyID,
		Format: stored.Envelope.Format, Ciphertext: stored.Envelope.Ciphertext,
	}); err != nil {
		return normalizeDatabaseError(err)
	}
	if err := r.audit.RecordAuditEvent(ctx, tx, canonicalIntent); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return normalizeDatabaseError(err)
	}
	return nil
}

// GetDraft returns the exact saved envelope only to the credential service.
// It filters by deployment, every resource scope field, and version ID.
func (r *Repository) GetStoredDraft(ctx context.Context, deploymentID, ownerID string, resource credential.Resource, versionID string) (credential.StoredVersion, error) {
	if r == nil || r.db == nil || ctx == nil {
		return credential.StoredVersion{}, credential.ErrUnavailable
	}
	if !canonical(deploymentID, 255) || !canonical(ownerID, 255) || !canonical(versionID, 255) || resource.Validate() != nil {
		return credential.StoredVersion{}, credential.ErrInvalid
	}
	row, err := credentialdb.New(r.db).GetStoredDraft(ctx, credentialdb.GetStoredDraftParams{
		DeploymentID: deploymentID, OwnerID: ownerID, ScopeKind: resource.ScopeKind, TargetID: resource.TargetID,
		ProjectID: resource.ProjectID, Environment: resource.Environment, ResourceID: resource.ResourceID,
		VersionID: versionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return credential.StoredVersion{}, credential.ErrNotFound
	}
	if err != nil {
		return credential.StoredVersion{}, normalizeDatabaseError(err)
	}
	return credential.StoredVersion{
		Metadata: credential.Metadata{
			Binding: encryption.Binding{
				DeploymentID: row.DeploymentID, OwnerID: row.OwnerID, ScopeKind: row.ScopeKind,
				TargetID: row.TargetID, ProjectID: row.ProjectID, Environment: row.Environment,
				ResourceID: row.ResourceID, Purpose: row.Purpose, Provider: row.Provider,
				Destination: row.Destination, VersionID: row.VersionID,
			},
			ActorID: row.ActorID, CreatedAt: row.CreatedAt,
		},
		Envelope: encryption.Envelope{Format: row.EnvelopeFormat, KeyID: row.EnvelopeKeyID, Ciphertext: row.Ciphertext},
	}, nil
}

// GetDraftMetadata reads only the logical row; it never selects the encrypted
// envelope or ciphertext.
func (r *Repository) GetDraftMetadata(ctx context.Context, deploymentID, ownerID string, resource credential.Resource, versionID string) (credential.Metadata, error) {
	if r == nil || r.db == nil || ctx == nil {
		return credential.Metadata{}, credential.ErrUnavailable
	}
	if !canonical(deploymentID, 255) || !canonical(ownerID, 255) || !canonical(versionID, 255) || resource.Validate() != nil {
		return credential.Metadata{}, credential.ErrInvalid
	}
	row, err := credentialdb.New(r.db).GetDraftMetadata(ctx, credentialdb.GetDraftMetadataParams{
		DeploymentID: deploymentID, OwnerID: ownerID, ScopeKind: resource.ScopeKind,
		TargetID: resource.TargetID, ProjectID: resource.ProjectID, Environment: resource.Environment,
		ResourceID: resource.ResourceID, VersionID: versionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return credential.Metadata{}, credential.ErrNotFound
	}
	if err != nil {
		return credential.Metadata{}, normalizeDatabaseError(err)
	}
	return credential.Metadata{Binding: encryption.Binding{
		DeploymentID: row.DeploymentID, OwnerID: row.OwnerID, ScopeKind: row.ScopeKind,
		TargetID: row.TargetID, ProjectID: row.ProjectID, Environment: row.Environment,
		ResourceID: row.ResourceID, Purpose: row.Purpose, Provider: row.Provider,
		Destination: row.Destination, VersionID: row.VersionID,
	}, ActorID: row.ActorID, CreatedAt: row.CreatedAt}, nil
}

// ListDrafts returns one newest-first keyset page for the supplied exact
// deployment, owner, and resource tuple. Provider and destination stay in each
// immutable row as historical metadata and are not treated as current proof.
func (r *Repository) ListDrafts(ctx context.Context, deploymentID, ownerID string, resource credential.Resource, limit int, beforeVersionID string) (credential.DraftPage, error) {
	if r == nil || r.db == nil || ctx == nil {
		return credential.DraftPage{}, credential.ErrUnavailable
	}
	if !canonical(deploymentID, 255) || !canonical(ownerID, 255) || resource.Validate() != nil || limit < 1 || limit > credential.MaxDraftPageSize {
		return credential.DraftPage{}, credential.ErrInvalid
	}
	beforeCreatedAt := pgtype.Timestamptz{}
	beforeID := pgtype.Text{}
	if beforeVersionID != "" {
		id, parseErr := uuid.Parse(beforeVersionID)
		if parseErr != nil || id.String() != beforeVersionID {
			return credential.DraftPage{}, credential.ErrInvalidCursor
		}
		anchor, getErr := r.GetDraftMetadata(ctx, deploymentID, ownerID, resource, beforeVersionID)
		if errors.Is(getErr, credential.ErrNotFound) {
			return credential.DraftPage{}, credential.ErrInvalidCursor
		}
		if getErr != nil {
			return credential.DraftPage{}, getErr
		}
		beforeCreatedAt = pgtype.Timestamptz{Time: anchor.CreatedAt, Valid: true}
		beforeID = pgtype.Text{String: beforeVersionID, Valid: true}
	}
	rows, err := credentialdb.New(r.db).ListDrafts(ctx, credentialdb.ListDraftsParams{
		DeploymentID: deploymentID, OwnerID: ownerID, ScopeKind: resource.ScopeKind,
		TargetID: resource.TargetID, ProjectID: resource.ProjectID, Environment: resource.Environment,
		ResourceID: resource.ResourceID, BeforeCreatedAt: beforeCreatedAt,
		BeforeVersionID: beforeID, PageSize: int32(limit + 1),
	})
	if err != nil {
		return credential.DraftPage{}, normalizeDatabaseError(err)
	}
	page := credential.DraftPage{Items: make([]credential.Metadata, 0, min(len(rows), limit))}
	for _, row := range rows[:min(len(rows), limit)] {
		page.Items = append(page.Items, credential.Metadata{Binding: encryption.Binding{
			DeploymentID: row.DeploymentID, OwnerID: row.OwnerID, ScopeKind: row.ScopeKind,
			TargetID: row.TargetID, ProjectID: row.ProjectID, Environment: row.Environment,
			ResourceID: row.ResourceID, Purpose: row.Purpose, Provider: row.Provider,
			Destination: row.Destination, VersionID: row.VersionID,
		}, ActorID: row.ActorID, CreatedAt: row.CreatedAt})
	}
	if len(rows) > limit {
		page.NextBeforeVersionID = page.Items[len(page.Items)-1].Binding.VersionID
	}
	return page, nil
}

func validateStoredVersion(stored credential.StoredVersion) error {
	metadata, envelope := stored.Metadata, stored.Envelope
	binding := metadata.Binding
	if binding.Validate() != nil || !canonical(metadata.ActorID, 255) || metadata.CreatedAt.IsZero() ||
		envelope.Format != "aes-256-gcm-random-nonce-v1" || !canonical(envelope.KeyID, 255) ||
		len(envelope.Ciphertext) < 28 || len(envelope.Ciphertext) > encryption.MaxPlaintextSize+28 {
		return credential.ErrInvalid
	}
	return nil
}

func validateDraftAudit(metadata credential.Metadata, intent access.AuditIntent) (access.AuditIntent, error) {
	canonicalIntent, err := intent.Canonicalize()
	if err != nil {
		return access.AuditIntent{}, fmt.Errorf("%w: invalid credential audit intent", credential.ErrInvalid)
	}
	binding := metadata.Binding
	scopeID, resourceKind := binding.ProjectID, "connection"
	if binding.ScopeKind == "agent" {
		scopeID, resourceKind = binding.DeploymentID, "instance"
	}
	parsedEventID, eventErr := uuid.Parse(canonicalIntent.EventID)
	actorUUID, actorErr := uuid.Parse(metadata.ActorID)
	expectedPrincipal := ""
	if actorErr == nil && actorUUID.String() == metadata.ActorID {
		expectedPrincipal = metadata.ActorID
	}
	var auditMetadata map[string]json.RawMessage
	if json.Unmarshal([]byte(canonicalIntent.MetadataJSON), &auditMetadata) != nil || len(auditMetadata) != 2 {
		return access.AuditIntent{}, credential.ErrInvalid
	}
	var purpose, versionID string
	if json.Unmarshal(auditMetadata["purpose"], &purpose) != nil || json.Unmarshal(auditMetadata["version_id"], &versionID) != nil {
		return access.AuditIntent{}, credential.ErrInvalid
	}
	if eventErr != nil || parsedEventID.String() != canonicalIntent.EventID ||
		canonicalIntent.ScopeID != scopeID || canonicalIntent.ActorID != metadata.ActorID || canonicalIntent.PrincipalID != expectedPrincipal ||
		canonicalIntent.Source != "credential" || canonicalIntent.Operation != "saveCredentialDraft" ||
		canonicalIntent.Action != "credential.draft.saved" || canonicalIntent.ResourceKind != resourceKind ||
		canonicalIntent.ResourceID != binding.ResourceID || canonicalIntent.Outcome != "success" ||
		canonicalIntent.AggregateKey != "credential:"+binding.VersionID || canonicalIntent.AggregateSequence != 1 ||
		canonicalIntent.DomainEventID != "" || canonicalIntent.RequestDigest != "" || canonicalIntent.Capability != "" ||
		purpose != binding.Purpose || versionID != binding.VersionID {
		return access.AuditIntent{}, credential.ErrInvalid
	}
	return canonicalIntent, nil
}

func canonical(value string, limit int) bool {
	if value == "" || len(value) > limit || strings.TrimSpace(value) != value {
		return false
	}
	return strings.IndexFunc(value, unicode.IsControl) < 0
}

func normalizeDatabaseError(err error) error {
	if err == nil {
		return nil
	}
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		switch pgError.Code {
		case "23505", "23503":
			return fmt.Errorf("%w: credential version identity already exists or references an unreserved key", credential.ErrConflict)
		case "23514", "22001", "22P02":
			return fmt.Errorf("%w: PostgreSQL rejected credential storage fields", credential.ErrInvalid)
		}
	}
	return fmt.Errorf("%w: PostgreSQL credential storage operation failed", credential.ErrUnavailable)
}
