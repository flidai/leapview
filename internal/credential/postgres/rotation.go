package postgres

import (
	"context"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/credential"
	"github.com/flidai/leapview/internal/credential/encryption"
	credentialdb "github.com/flidai/leapview/internal/credential/postgres/internal/db"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RotationRepository requires the independently authenticated maintenance pool.
// SQL privileges independently deny runtime callers the rewrap function and
// direct envelope mutation. No caller receives general audit append authority.
type RotationRepository struct{ repository *Repository }

func NewRotationRepository(ctx context.Context, pool *pgxpool.Pool) (*RotationRepository, error) {
	if ctx == nil || pool == nil {
		return nil, credential.ErrUnavailable
	}
	allowed, err := credentialdb.New(pool).IsCredentialRotationOperator(ctx)
	if err != nil || !allowed {
		return nil, credential.ErrForbidden
	}
	return &RotationRepository{repository: &Repository{db: pool}}, nil
}
func (r *RotationRepository) ReserveEncryption(ctx context.Context, deployment, key string, commitment encryption.KeyCommitment) error {
	if r == nil || r.repository == nil || ctx == nil {
		return credential.ErrUnavailable
	}
	return r.repository.ReserveEncryption(ctx, deployment, key, commitment)
}
func (r *RotationRepository) CheckKeyring(ctx context.Context, keys *encryption.Keyring) error {
	if r == nil || r.repository == nil || ctx == nil {
		return credential.ErrUnavailable
	}
	return r.repository.CheckKeyring(ctx, keys)
}
func (r *RotationRepository) ListEnvelopeRewraps(ctx context.Context, deployment, key string, limit int) ([]credential.EnvelopeRewrap, error) {
	if r == nil || r.repository == nil || !canonical(deployment, 255) || !canonical(key, 255) || limit < 1 || limit > credential.MaxEnvelopeRotationBatch {
		return nil, credential.ErrInvalid
	}
	rows, err := credentialdb.New(r.repository.db).ListEnvelopeRewraps(ctx, credentialdb.ListEnvelopeRewrapsParams{DeploymentID: deployment, ActiveKeyID: key, BatchSize: int32(limit)})
	if err != nil {
		return nil, normalizeDatabaseError(err)
	}
	result := make([]credential.EnvelopeRewrap, 0, len(rows))
	for _, row := range rows {
		result = append(result, credential.EnvelopeRewrap{Revision: row.EnvelopeRevision, Stored: credential.StoredVersion{
			Metadata: credential.Metadata{Binding: encryption.Binding{DeploymentID: row.DeploymentID, OwnerID: row.OwnerID, ScopeKind: row.ScopeKind, TargetID: row.TargetID, ProjectID: row.ProjectID, Environment: row.Environment, ResourceID: row.ResourceID, Purpose: row.Purpose, Provider: row.Provider, Destination: row.Destination, VersionID: row.VersionID}, ActorID: row.ActorID, CreatedAt: row.CreatedAt},
			Envelope: encryption.Envelope{Format: row.EnvelopeFormat, KeyID: row.EnvelopeKeyID, Ciphertext: row.Ciphertext},
		}})
	}
	return result, nil
}
func (r *RotationRepository) RewrapEnvelope(ctx context.Context, previous credential.EnvelopeRewrap, replacement encryption.Envelope, intent access.AuditIntent) error {
	if r == nil || r.repository == nil || ctx == nil {
		return credential.ErrUnavailable
	}
	expected, err := credential.EnvelopeRewrapAudit(previous, replacement, intent.EventID)
	if err != nil {
		return credential.ErrInvalid
	}
	supplied, err := intent.Canonicalize()
	if err != nil || supplied != expected {
		return credential.ErrInvalid
	}
	digest, err := expected.PayloadDigest()
	if err != nil {
		return credential.ErrInvalid
	}
	var auditID pgtype.UUID
	if err = auditID.Scan(expected.EventID); err != nil || !auditID.Valid {
		return credential.ErrInvalid
	}
	revision, err := credentialdb.New(r.repository.db).RewrapEnvelope(ctx, credentialdb.RewrapEnvelopeParams{
		DeploymentID: previous.Stored.Metadata.Binding.DeploymentID, VersionID: previous.Stored.Metadata.Binding.VersionID,
		PreviousRevision: previous.Revision, PreviousKeyID: previous.Stored.Envelope.KeyID,
		KeyID: replacement.KeyID, Format: replacement.Format, Ciphertext: replacement.Ciphertext,
		AuditID: auditID, IntentDigest: digest,
	})
	if err != nil {
		return normalizeDatabaseError(err)
	}
	if revision != previous.Revision+1 {
		return credential.ErrConflict
	}
	return nil
}
