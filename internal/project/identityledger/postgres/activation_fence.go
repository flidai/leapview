package postgres

import (
	"context"
	"fmt"
	"reflect"
	"sort"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/identityledger"
	"github.com/jackc/pgx/v5"
)

// WithContractActivationFence reuses the identity ledger's instance and row
// locks to prevent lifecycle/publication drift across the delivery target CAS.
// The immutable publication remains the authority; this method only compares
// the exact references retained by the delivery plan.
func (r *Repository) WithContractActivationFence(ctx context.Context, candidateBundleID string, references []identityledger.PolicyActivationReference, activate func(context.Context) error) error {
	if r == nil || r.db == nil || candidateBundleID == "" || len(references) == 0 || activate == nil {
		return fmt.Errorf("%w: contract activation fence is incomplete", identityledger.ErrPolicyEvidenceInvalid)
	}
	references = append([]identityledger.PolicyActivationReference(nil), references...)
	sort.Slice(references, func(i, j int) bool {
		return references[i].Publication.AuthoredID < references[j].Publication.AuthoredID
	})
	instanceID := references[0].Publication.InstanceID
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin contract activation fence: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockInstance(ctx, tx, instanceID); err != nil {
		return err
	}
	for _, reference := range references {
		if err := reference.Validate(); err != nil || reference.Publication.InstanceID != instanceID {
			return fmt.Errorf("%w: contract activation reference scope is invalid", identityledger.ErrPolicyEvidenceInvalid)
		}
		identity, sequence, err := policyLifecycleTx(ctx, tx, instanceID, reference.Publication.AuthoredID, reference.Publication.ResourceKind)
		if err != nil {
			return err
		}
		// The normal identity publish transition immediately precedes this
		// fence and advances each retained ACTIVE identity exactly once. First
		// activation and restore never reach this path.
		if identity.Lifecycle != identityledger.LifecycleActive || identity.ActiveBundleID != candidateBundleID || sequence != reference.LifecycleSequence+1 {
			return fmt.Errorf("%w: contract activation lifecycle evidence changed", identityledger.ErrPolicyEvidenceConflict)
		}
		publication, found, err := latestContractPublicationTx(ctx, tx, instanceID, reference.Publication.AuthoredID, reference.Publication.ResourceKind)
		if err != nil {
			return err
		}
		if !found || publication.VersionBaseline != reference.Publication.VersionBaseline {
			return fmt.Errorf("%w: approved publication is no longer current", identityledger.ErrPolicyEvidenceConflict)
		}
		if err := reference.Matches(publication, reference.GraphDigest); err != nil {
			return err
		}
		if reference.RegistryTypes != nil {
			baseline, baselineErr := resolvePolicyBaselineTx(ctx, tx, publication, identityledger.PolicyContext{
				BaselineKind: reference.BaselineKind,
				Baseline:     reference.Baseline,
			})
			if baselineErr != nil {
				return baselineErr
			}
			expectedRegistry := identityledger.PolicyRegistryReference{
				InstanceID: reference.RegistryTypes.InstanceID, ProjectID: projectgraph.ResourceID(reference.RegistryTypes.ProjectID),
				ControlRevision: reference.RegistryTypes.ControlRevision, Profile: reference.RegistryTypes.Profile,
				Revision: reference.RegistryTypes.Revision, Digest: reference.RegistryTypes.Digest,
			}
			currentRegistry, registryErr := r.registryTypesForPublication(ctx, tx, publication, baseline, identityledger.PolicyContext{ExpectedRegistry: &expectedRegistry})
			if registryErr != nil {
				return registryErr
			}
			if currentRegistry == nil || !reflect.DeepEqual(*reference.RegistryTypes, *currentRegistry) {
				return fmt.Errorf("%w: approved registry/type evidence changed", identityledger.ErrPolicyEvidenceConflict)
			}
		}
	}
	if err := activate(ctx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit contract activation fence: %w", err)
	}
	return nil
}
