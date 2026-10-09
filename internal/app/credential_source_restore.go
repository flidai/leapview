package app

import (
	"context"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/platform/typednil"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	"github.com/flidai/leapview/internal/servingstate"
)

type sourceCredentialInstalledRuntime interface {
	runtimehostmodule.Provider
	CurrentServingStateID() servingstate.ID
	AcquireCutoverFence(context.Context) (func(), error)
	LeaseRenewalError() error
	ReconcileSealed(context.Context, servingstate.ID) error
}

func installSourceCredentialGeneration(ctx context.Context, host sourceCredentialInstalledRuntime, reader deploymentmodule.NativeDeliveryReader, evidence sourceCredentialEvidence, targetID string, record credentialmodule.ActivationRecord) error {
	target, err := reader.OperatorSnapshot(ctx, targetID)
	if err != nil {
		return err
	}
	if target.TargetID != targetID || record.Resource.ScopeKind != "connection" || record.Resource.TargetID != target.TargetID || record.Resource.ProjectID != target.ProjectID || record.Resource.Environment != target.Environment ||
		record.Status.GenerationID != target.ActiveGenerationID || record.Status.PublicationID != target.ActivePublicationID || (record.Status.State != "committed" && record.Status.State != "completed") {
		return credentialmodule.ErrValidationConflict
	}
	return restoreSourceCredentialGeneration(ctx, host, reader, evidence, target)
}

func restoreSourceCredentialGeneration(ctx context.Context, host sourceCredentialInstalledRuntime, reader deploymentmodule.NativeDeliveryReader, evidence sourceCredentialEvidence, target deploymentpostgres.DeliveryOperatorSnapshot) error {
	reused, err := installedSourceCredentialGeneration(ctx, host, reader, evidence, target)
	if err != nil || reused {
		return err
	}
	// The proof releases its local fence and reader before reconciliation takes
	// the exclusive cutover lock and prepares a different or missing runtime.
	return host.ReconcileSealed(ctx, servingstate.ID(target.ActiveGenerationID))
}

// Startup has already reconstructed the sealed generation. Reuse only its
// live, healthy lease after rechecking the exact authoritative publication,
// immutable seal and committed credential pins; the persisted pointer alone
// never proves that a failed installation is usable.
func installedSourceCredentialGeneration(ctx context.Context, host sourceCredentialInstalledRuntime, reader deploymentmodule.NativeDeliveryReader, evidence sourceCredentialEvidence, target deploymentpostgres.DeliveryOperatorSnapshot) (bool, error) {
	releaseFence, err := host.AcquireCutoverFence(ctx)
	if err != nil {
		return false, err
	}
	defer releaseFence()
	if host.CurrentServingStateID() != servingstate.ID(target.ActiveGenerationID) {
		return false, nil
	}
	lease, err := host.Acquire(ctx)
	if err != nil {
		return false, err
	}
	if typednil.IsNil(lease) {
		return false, credentialmodule.ErrValidationConflict
	}
	defer lease.Release()
	identity := lease.Identity()
	if identity.GenerationID != target.ActiveGenerationID || identity.ProjectID.String() != target.ProjectID || identity.Environment != target.Environment {
		return false, nil
	}
	if err := host.LeaseRenewalError(); err != nil {
		return false, err
	}
	state, ok := lease.(interface {
		AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot
		DuckLakeSnapshotID() int64
	})
	// Sealed factories own their pinned snapshot lease; runtimehost therefore
	// reports zero for its separate manager-owned snapshot. Inspect the live
	// factory runtime's actual pin, without transferring snapshot ownership.
	pinned, hasPin := lease.Runtime().(interface{ DuckLakeSnapshotID() int64 })
	if !ok || typednil.IsNil(lease.Runtime()) || !hasPin || pinned.DuckLakeSnapshotID() <= 0 || identity.Validate() != nil || state.AuthorizationSnapshot().ValidateBound() != nil || state.AuthorizationSnapshot().Identity() != identity ||
		(state.DuckLakeSnapshotID() > 0 && state.DuckLakeSnapshotID() != pinned.DuckLakeSnapshotID()) {
		return false, credentialmodule.ErrValidationConflict
	}
	generation, err := reader.LoadGeneration(ctx, target.ActiveGenerationID)
	if err != nil {
		return false, err
	}
	seal, err := reader.LoadSnapshotSeal(ctx, generation.SnapshotSealID)
	if err != nil {
		return false, err
	}
	publication, err := reader.LoadPublication(ctx, target.ActivePublicationID)
	if err != nil {
		return false, err
	}
	if generation.GenerationID != identity.GenerationID || generation.TargetID != target.TargetID || generation.SnapshotSealID == "" || generation.CandidateID == "" || generation.ServingArtifactDigest == "" ||
		seal.SealID != generation.SnapshotSealID || seal.CandidateID != generation.CandidateID || seal.ServingArtifactDigest != generation.ServingArtifactDigest || seal.DuckLakeSnapshotID != pinned.DuckLakeSnapshotID() || seal.QualifiedAt.IsZero() ||
		publication.PublicationID != target.ActivePublicationID || publication.State != "committed" || publication.TargetID != target.TargetID || publication.GenerationID != generation.GenerationID || publication.CandidateID != generation.CandidateID || publication.SnapshotSealID != seal.SealID || publication.ResultTargetRevision != target.TargetRevision {
		return false, credentialmodule.ErrValidationConflict
	}
	if _, err := evidence.BindingEvidence(ctx, identity.GenerationID, target.ProjectID); err != nil {
		return false, err
	}
	current, err := reader.OperatorSnapshot(ctx, target.TargetID)
	if err != nil {
		return false, err
	}
	if current != target {
		return false, credentialmodule.ErrValidationConflict
	}
	return true, nil
}
