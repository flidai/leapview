package postgres

import (
	"context"
	"errors"
	"fmt"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/identityledger"
	"github.com/jackc/pgx/v5"
)

func policyLifecycleTx(ctx context.Context, tx pgx.Tx, instanceID string, authoredID projectgraph.ResourceID, kind projectgraph.Kind) (identityledger.Identity, int64, error) {
	var identity identityledger.Identity
	var storedID, storedKind, lifecycle string
	var sequence int64
	err := tx.QueryRow(ctx, `
		SELECT ri.instance_id,ri.authored_id,ri.resource_kind,ri.lifecycle_state,
		       COALESCE(ri.active_bundle_id,''),ri.tombstone_reason,
		       ri.created_at,ri.updated_at,ri.tombstoned_at,ri.restored_at,
		       COALESCE((SELECT max(h.sequence) FROM project.resource_identity_history h
		                  WHERE h.instance_id=ri.instance_id AND h.authored_id=ri.authored_id),0)
		FROM project.resource_identity ri
		WHERE ri.instance_id=$1 AND ri.authored_id=$2
		FOR UPDATE`, instanceID, authoredID.String()).Scan(
		&identity.InstanceID, &storedID, &storedKind, &lifecycle, &identity.ActiveBundleID,
		&identity.TombstoneReason, &identity.CreatedAt, &identity.UpdatedAt,
		&identity.TombstonedAt, &identity.RestoredAt, &sequence)
	if errors.Is(err, pgx.ErrNoRows) {
		return identityledger.Identity{}, 0, fmt.Errorf("%w: resource identity is missing", identityledger.ErrPolicyEvidenceConflict)
	}
	if err != nil {
		return identityledger.Identity{}, 0, fmt.Errorf("read policy lifecycle identity: %w", err)
	}
	identity.AuthoredID = projectgraph.ResourceID(storedID)
	identity.Kind = projectgraph.Kind(storedKind)
	identity.Lifecycle = identityledger.Lifecycle(lifecycle)
	if identity.InstanceID != instanceID || identity.AuthoredID != authoredID {
		return identityledger.Identity{}, 0, fmt.Errorf("%w: lifecycle identity scope mismatch", identityledger.ErrPolicyEvidenceInvalid)
	}
	if identity.Kind != kind {
		return identityledger.Identity{}, 0, fmt.Errorf("%w: lifecycle kind=%s requested=%s", identityledger.ErrKindConflict, identity.Kind, kind)
	}
	if sequence <= 0 {
		return identityledger.Identity{}, 0, fmt.Errorf("%w: lifecycle history is missing", identityledger.ErrPolicyEvidenceConflict)
	}
	return identity, sequence, nil
}

func resolvePolicyBaselineTx(ctx context.Context, tx pgx.Tx, candidate identityledger.ContractPublication, context identityledger.PolicyContext) (*identityledger.ContractPublication, error) {
	if context.BaselineKind == identityledger.PolicyBaselineGenesis {
		return nil, nil
	}
	if context.Baseline == nil {
		return nil, fmt.Errorf("%w: existing baseline is missing", identityledger.ErrPolicyEvidenceInvalid)
	}
	baseline, err := contractPublicationTx(ctx, tx, candidate.InstanceID, candidate.AuthoredID, candidate.ResourceKind, context.Baseline.VersionBaseline)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: expected baseline publication is missing", identityledger.ErrPolicyEvidenceConflict)
	}
	if err != nil {
		return nil, err
	}
	actual := identityledger.PolicyPublicationIdentity{
		InstanceID: baseline.InstanceID, AuthoredID: baseline.AuthoredID, ResourceKind: baseline.ResourceKind,
		Version: baseline.Version, VersionBaseline: baseline.VersionBaseline,
		ProjectionProfile: baseline.ProjectionProfile, Digest: baseline.Digest,
	}
	if !identityledger.EqualPolicyPublicationIdentity(actual, *context.Baseline) {
		return nil, fmt.Errorf("%w: expected baseline publication identity changed", identityledger.ErrPolicyEvidenceConflict)
	}
	return &baseline, nil
}

func derivePolicyValidation(prepared identityledger.ContractPublication, baseline *identityledger.ContractPublication, sequence int64, activeBundleID string, context identityledger.PolicyContext) (identityledger.ValidationEvidence, error) {
	evidence, err := identityledger.DerivePolicyEvidence(context, baseline, prepared, sequence, activeBundleID)
	if err != nil {
		// Preserve the historical contract publication conflict surface for
		// version transitions while retaining the narrower policy sentinel.
		return identityledger.ValidationEvidence{}, fmt.Errorf("%w: %w: %w", identityledger.ErrContractPublicationConflict, identityledger.ErrPolicyEvidenceConflict, err)
	}
	return identityledger.ValidationEvidence{Version: 1, Checks: append([]identityledger.ValidationCheck(nil), prepared.Validation.Checks...), PolicyEvidence: &evidence}, nil
}
