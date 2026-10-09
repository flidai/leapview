package credential

import (
	"context"
	"encoding/json"
	"math"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/credential/encryption"
	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/google/uuid"
)

const MaxEnvelopeRotationBatch = 100

type EnvelopeRewrap struct {
	Stored   StoredVersion `json:"-"`
	Revision int64         `json:"envelopeRevision"`
}

type EnvelopeRotationRepository interface {
	encryption.Budget
	CheckKeyring(context.Context, *encryption.Keyring) error
	ListEnvelopeRewraps(context.Context, string, string, int) ([]EnvelopeRewrap, error)
	RewrapEnvelope(context.Context, EnvelopeRewrap, encryption.Envelope, access.AuditIntent) error
}

type EnvelopeRotationProgress struct {
	Rewrapped        int    `json:"rewrapped"`
	Complete         bool   `json:"complete"`
	ActiveWriteKeyID string `json:"activeWriteKeyId"`
}

// RotateEnvelopes is a bounded offline operation over a maintenance-only store.
// Each replacement preserves the logical version and exact AAD. Committed
// replacements disappear from the next selection, making interruption resumable.
// Reservations happen before replacement and cannot be reclaimed on rollback.
func RotateEnvelopes(ctx context.Context, repository EnvelopeRotationRepository, keys *encryption.Keyring, limit int) (EnvelopeRotationProgress, error) {
	progress := EnvelopeRotationProgress{}
	if ctx == nil || typednil.IsNil(repository) || keys == nil || limit < 1 || limit > MaxEnvelopeRotationBatch {
		return progress, ErrInvalid
	}
	progress.ActiveWriteKeyID = keys.ActiveWriteKeyID()
	if err := repository.CheckKeyring(ctx, keys); err != nil {
		return progress, err
	}
	rows, err := repository.ListEnvelopeRewraps(ctx, keys.DeploymentID(), keys.ActiveWriteKeyID(), limit)
	if err != nil {
		return progress, err
	}
	if len(rows) > limit {
		return progress, ErrInvalid
	}
	for _, row := range rows {
		if err = rewrapOne(ctx, repository, keys, row); err != nil {
			return progress, err
		}
		progress.Rewrapped++
	}
	progress.Complete = len(rows) < limit
	return progress, nil
}

func rewrapOne(ctx context.Context, repository EnvelopeRotationRepository, keys *encryption.Keyring, row EnvelopeRewrap) error {
	if row.Revision < 1 || row.Revision == math.MaxInt64 || row.Stored.Metadata.Binding.Validate() != nil || row.Stored.Metadata.Binding.DeploymentID != keys.DeploymentID() || row.Stored.Envelope.KeyID == keys.ActiveWriteKeyID() {
		return ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	plaintext, err := keys.Decrypt(row.Stored.Metadata.Binding, row.Stored.Envelope)
	if err != nil {
		return ErrUnavailable
	}
	defer clear(plaintext)
	replacement, err := keys.Encrypt(ctx, repository, row.Stored.Metadata.Binding, plaintext)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	audit, err := EnvelopeRewrapAudit(row, replacement, uuid.NewString())
	if err != nil {
		return err
	}
	return repository.RewrapEnvelope(ctx, row, replacement, audit)
}

func EnvelopeRewrapAudit(previous EnvelopeRewrap, replacement encryption.Envelope, eventID string) (access.AuditIntent, error) {
	binding := previous.Stored.Metadata.Binding
	if previous.Revision < 1 || previous.Revision == math.MaxInt64 || binding.Validate() != nil || replacement.KeyID == previous.Stored.Envelope.KeyID || !canonical(replacement.KeyID) {
		return access.AuditIntent{}, ErrInvalid
	}
	metadata, err := json.Marshal(map[string]any{"previousKeyId": previous.Stored.Envelope.KeyID, "keyId": replacement.KeyID, "envelopeRevision": previous.Revision + 1})
	if err != nil {
		return access.AuditIntent{}, ErrInvalid
	}
	return (access.AuditIntent{EventID: eventID, ScopeID: binding.DeploymentID, ActorID: "offline_operator", Source: "credential", Operation: "rewrapCredentialEnvelope", Action: "credential.envelope.rewrapped", ResourceKind: "credential_version", ResourceID: binding.VersionID, Outcome: "success", AggregateKey: "credential-envelope:" + binding.VersionID, AggregateSequence: previous.Revision + 1, MetadataJSON: string(metadata)}).Canonicalize()
}
