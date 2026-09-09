package postgres

import (
	"context"
	"fmt"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/identityledger"
	"github.com/jackc/pgx/v5"
)

// ReadLatestLifecycleEvidence resolves the current immutable publication for
// an already-active identity in one snapshot. It does not create an identity,
// publication, or lifecycle transition.
func (r *Repository) ReadLatestLifecycleEvidence(ctx context.Context, instanceID string, authoredID projectgraph.ResourceID, kind projectgraph.Kind) (identityledger.LifecycleEvidence, error) {
	if err := validateInstanceToken(instanceID, "instance id"); err != nil {
		return identityledger.LifecycleEvidence{}, err
	}
	if err := authoredID.Validate(); err != nil {
		return identityledger.LifecycleEvidence{}, err
	}
	if !contractPublicationKind(kind) {
		return identityledger.LifecycleEvidence{}, identityledger.ErrContractPublicationInvalid
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return identityledger.LifecycleEvidence{}, fmt.Errorf("begin latest lifecycle evidence read: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	identity, sequence, err := policyLifecycleTx(ctx, tx, instanceID, authoredID, kind)
	if err != nil {
		return identityledger.LifecycleEvidence{}, err
	}
	if identity.Lifecycle != identityledger.LifecycleActive || identity.ActiveBundleID == "" {
		return identityledger.LifecycleEvidence{}, fmt.Errorf("%w: contract activation identity is not active", identityledger.ErrPolicyEvidenceConflict)
	}
	publication, found, err := latestContractPublicationTx(ctx, tx, instanceID, authoredID, kind)
	if err != nil {
		return identityledger.LifecycleEvidence{}, err
	}
	if !found {
		return identityledger.LifecycleEvidence{}, identityledger.ErrContractPublicationNotFound
	}
	if _, err := publication.PolicyDecision(); err != nil {
		return identityledger.LifecycleEvidence{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return identityledger.LifecycleEvidence{}, err
	}
	return identityledger.LifecycleEvidence{Identity: identity, Sequence: sequence, Publication: publication}, nil
}
