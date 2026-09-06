package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/project/contractversion"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/identityledger"
	"github.com/jackc/pgx/v5"
)

// ReadLifecycleEvidence reads the current identity, its append-only lifecycle
// sequence, and exact immutable contract publication evidence in one
// repeatable-read transaction. Callers can compare the returned evidence
// before reusing or writing a protected cache entry; no timestamp, hash, or
// generated identity is introduced here.
func (r *Repository) ReadLifecycleEvidence(ctx context.Context, instanceID string, authoredID projectgraph.ResourceID, kind projectgraph.Kind, version string) (identityledger.LifecycleEvidence, error) {
	if err := validateInstanceToken(instanceID, "instance id"); err != nil {
		return identityledger.LifecycleEvidence{}, err
	}
	if err := authoredID.Validate(); err != nil {
		return identityledger.LifecycleEvidence{}, fmt.Errorf("%w: authored id: %v", identityledger.ErrInvalidInput, err)
	}
	if !contractPublicationKind(kind) {
		return identityledger.LifecycleEvidence{}, identityledger.ErrContractPublicationInvalid
	}
	baseline, err := contractversion.SemverBaseline(version)
	if err != nil {
		return identityledger.LifecycleEvidence{}, fmt.Errorf("%w: %v", identityledger.ErrContractPublicationInvalid, err)
	}

	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return identityledger.LifecycleEvidence{}, fmt.Errorf("begin lifecycle evidence read: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var identity identityledger.Identity
	var storedAuthoredID, storedKind, storedLifecycle string
	var sequence int64
	err = tx.QueryRow(ctx, `
		SELECT ri.instance_id,ri.authored_id,ri.resource_kind,ri.lifecycle_state,
		       COALESCE(ri.active_bundle_id,''),ri.tombstone_reason,
		       ri.created_at,ri.updated_at,ri.tombstoned_at,ri.restored_at,
		       COALESCE((
			   SELECT max(h.sequence)
			   FROM project.resource_identity_history h
			   WHERE h.instance_id=ri.instance_id AND h.authored_id=ri.authored_id
		       ),0)
		FROM project.resource_identity ri
		WHERE ri.instance_id=$1 AND ri.authored_id=$2`, instanceID, authoredID.String()).Scan(
		&identity.InstanceID, &storedAuthoredID, &storedKind, &storedLifecycle,
		&identity.ActiveBundleID, &identity.TombstoneReason,
		&identity.CreatedAt, &identity.UpdatedAt, &identity.TombstonedAt, &identity.RestoredAt,
		&sequence)
	if errors.Is(err, pgx.ErrNoRows) {
		return identityledger.LifecycleEvidence{}, pgx.ErrNoRows
	}
	if err != nil {
		return identityledger.LifecycleEvidence{}, fmt.Errorf("read lifecycle identity: %w", err)
	}
	identity.AuthoredID = projectgraph.ResourceID(storedAuthoredID)
	identity.Kind = projectgraph.Kind(storedKind)
	identity.Lifecycle = identityledger.Lifecycle(storedLifecycle)
	if identity.InstanceID != instanceID || identity.AuthoredID != authoredID {
		return identityledger.LifecycleEvidence{}, fmt.Errorf("%w: identity scope mismatch", identityledger.ErrInvalidInput)
	}
	if identity.Kind != kind {
		return identityledger.LifecycleEvidence{}, fmt.Errorf("%w for %q: ledger=%s requested=%s", identityledger.ErrKindConflict, authoredID, identity.Kind, kind)
	}
	if sequence <= 0 {
		return identityledger.LifecycleEvidence{}, fmt.Errorf("resource identity history missing for %q: %w", authoredID, pgx.ErrNoRows)
	}

	publication, err := contractPublicationTx(ctx, tx, instanceID, authoredID, kind, strings.TrimPrefix(baseline, "v"))
	if errors.Is(err, pgx.ErrNoRows) {
		return identityledger.LifecycleEvidence{}, identityledger.ErrContractPublicationNotFound
	}
	if err != nil {
		return identityledger.LifecycleEvidence{}, err
	}
	if publication.InstanceID != instanceID || publication.AuthoredID != authoredID || publication.ResourceKind != kind {
		return identityledger.LifecycleEvidence{}, fmt.Errorf("%w: publication scope mismatch", identityledger.ErrContractPublicationInvalid)
	}

	if err := tx.Commit(ctx); err != nil {
		return identityledger.LifecycleEvidence{}, fmt.Errorf("commit lifecycle evidence read: %w", err)
	}
	return identityledger.LifecycleEvidence{Identity: identity, Sequence: sequence, Publication: publication}, nil
}
