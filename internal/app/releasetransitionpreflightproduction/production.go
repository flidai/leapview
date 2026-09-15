// Package releasetransitionpreflightproduction composes the concrete
// PostgreSQL authorities used by release-transition preflight.
package releasetransitionpreflightproduction

import (
	"context"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/app/releasemigrationowners"
	releasetransitionpreflight "github.com/flidai/leapview/internal/app/releasetransitionpreflight"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	recoverysetpostgres "github.com/flidai/leapview/internal/recoveryset/postgres"
	"github.com/flidai/leapview/internal/release/migrationcompatibility"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
)

var ErrProductionAuthorityUnavailable = errors.New("production transition preflight authority is unavailable")

// ProductionDependencies contains only concrete PostgreSQL authorities. It
// deliberately has no interface or projection fields through which a caller
// could inject artifact, policy, migration, target, or frontier evidence.
type ProductionDependencies struct {
	Releases              *releasepostgres.Repository
	Targets               *deploymentpostgres.Repository
	MigrationCapabilities *releasepostgres.MigrationCapabilityAuthority
	RecoverySets          *recoverysetpostgres.Repository
}

// ProductionResolver is the owner-backed transition-preflight entrypoint.
// Its request contains lookup selectors only; every evidence projection is
// resolved afresh by a concrete owner.
type ProductionResolver struct {
	resolver *transitionpreflight.Resolver
}

// NewProductionResolver composes the authoritative PostgreSQL-era owners.
func NewProductionResolver(dependencies ProductionDependencies) (*ProductionResolver, error) {
	if dependencies.Releases == nil || dependencies.Targets == nil || dependencies.MigrationCapabilities == nil || dependencies.RecoverySets == nil {
		return nil, ErrProductionAuthorityUnavailable
	}
	migrations, err := releasemigrationowners.NewAuthorityV2(dependencies.Releases, dependencies.Targets, dependencies.MigrationCapabilities)
	if err != nil {
		return nil, fmt.Errorf("%w: migration compatibility: %v", ErrProductionAuthorityUnavailable, err)
	}
	targets := &targetAuthority{repository: dependencies.Targets}
	frontiers := releasetransitionpreflight.NewPublishedRecoveryFrontierAuthority(dependencies.RecoverySets)
	return &ProductionResolver{resolver: transitionpreflight.NewResolver(dependencies.Releases, targets, &migrationAuthorityV2{authority: migrations}, dependencies.Releases, frontiers)}, nil
}

// ResolveAndEvaluate resolves authoritative owner state and emits the existing
// canonical transition-preflight evidence. It does not execute a transition.
func (r *ProductionResolver) ResolveAndEvaluate(ctx context.Context, request transitionpreflight.ResolutionRequest) (transitionpreflight.ResolutionResult, error) {
	if r == nil || r.resolver == nil {
		return transitionpreflight.ResolutionResult{}, ErrProductionAuthorityUnavailable
	}
	return r.resolver.ResolveAndEvaluate(ctx, request)
}

type targetAuthority struct {
	repository *deploymentpostgres.Repository
}

func (a *targetAuthority) ResolveTarget(ctx context.Context, reference string) (transitionpreflight.TargetIdentity, error) {
	if a == nil || a.repository == nil {
		return transitionpreflight.TargetIdentity{}, ErrProductionAuthorityUnavailable
	}
	target, err := a.repository.Target(ctx, reference)
	if err != nil {
		return transitionpreflight.TargetIdentity{}, err
	}
	identity := transitionpreflight.TargetIdentity{TargetID: target.TargetID, TargetRevision: target.TargetRevision}
	if target.TargetID != reference {
		return transitionpreflight.TargetIdentity{}, fmt.Errorf("%w: deployment target identity", transitionpreflight.ErrResolutionMismatch)
	}
	if _, err := identity.Digest(); err != nil {
		return transitionpreflight.TargetIdentity{}, fmt.Errorf("%w: deployment target identity", transitionpreflight.ErrInvalidOwnerProjection)
	}
	return identity, nil
}

type migrationAuthorityV2 struct {
	authority *releasemigrationowners.AuthorityV2
}

func (a *migrationAuthorityV2) ResolveMigration(ctx context.Context, target transitionpreflight.TargetIdentity, predecessor, candidate transitionpreflight.ArtifactIdentity) (transitionpreflight.MigrationResolution, error) {
	if a == nil || a.authority == nil {
		return transitionpreflight.MigrationResolution{}, ErrProductionAuthorityUnavailable
	}
	selection := releasemigrationowners.SelectionV2{
		PredecessorArtifactReference: predecessor.Release.Image,
		CandidateArtifactReference:   candidate.Release.Image,
		TargetID:                     target.TargetID,
	}
	evidence, err := a.authority.Resolve(ctx, selection)
	if err != nil {
		return transitionpreflight.MigrationResolution{}, err
	}
	canonical, err := evidence.CanonicalJSON()
	if err != nil {
		return transitionpreflight.MigrationResolution{}, fmt.Errorf("canonicalize migration compatibility v2 evidence: %w", err)
	}
	parsed, err := migrationcompatibility.ParseEvidenceV2(canonical)
	if err != nil || parsed != evidence {
		return transitionpreflight.MigrationResolution{}, fmt.Errorf("validate migration compatibility v2 evidence: %w", migrationcompatibility.ErrEvidenceV2Invalid)
	}

	targetDigest, err := target.Digest()
	if err != nil {
		return transitionpreflight.MigrationResolution{}, fmt.Errorf("target identity: %w", err)
	}
	if evidence.Binding.PredecessorOCIAdmissionDigest != predecessor.ArtifactAdmissionDigest ||
		evidence.Binding.CandidateOCIAdmissionDigest != candidate.ArtifactAdmissionDigest ||
		evidence.Binding.TargetIdentityDigest != targetDigest {
		return transitionpreflight.MigrationResolution{}, fmt.Errorf("%w: migration compatibility v2 binding", transitionpreflight.ErrResolutionMismatch)
	}
	predecessorDigest, err := predecessor.Digest()
	if err != nil {
		return transitionpreflight.MigrationResolution{}, err
	}
	candidateDigest, err := candidate.Digest()
	if err != nil {
		return transitionpreflight.MigrationResolution{}, err
	}
	duckLakeCompatibility := evidence.DuckLake.Compatibility
	if evidence.PhysicalPool.Compatibility == transitionpreflight.CompatibilityIncompatible {
		duckLakeCompatibility = transitionpreflight.CompatibilityIncompatible
	}
	return transitionpreflight.MigrationResolution{
		PredecessorArtifactDigest: predecessorDigest,
		CandidateArtifactDigest:   candidateDigest,
		Ownership: transitionpreflight.MigrationOwnership{
			GooseControlSchemaOwner:     transitionpreflight.GooseControlSchemaOwnerLeapView,
			RiverOperationalSchemaOwner: transitionpreflight.RiverOperationalSchemaOwnerRiver,
			RiverJobHistoryOwner:        transitionpreflight.RiverJobHistoryOwnerLeapView,
		},
		DuckLakeOwner: transitionpreflight.OwnerLeapView,
		Control: transitionpreflight.PostgreSQLControlProjection{
			Compatibility:            evidence.Goose.Compatibility,
			PredecessorSchemaVersion: evidence.Goose.State.PredecessorSchemaVersion,
			CandidateSchemaVersion:   evidence.Goose.State.CandidateSchemaVersion,
			TargetIdentityDigest:     targetDigest,
		},
		River: transitionpreflight.RiverJobProjection{
			SchemaCompatibility:       evidence.RiverJobs.Compatibility,
			JobHistoryCompatibility:   evidence.RiverJobs.Compatibility,
			ExistingSchemaVersion:     evidence.RiverJobs.State.PredecessorSchemaVersion,
			RequiredSchemaVersion:     evidence.RiverJobs.State.CandidateSchemaVersion,
			ExistingJobHistoryVersion: evidence.RiverJobs.State.PredecessorJobHistoryVersion,
			RequiredJobHistoryVersion: evidence.RiverJobs.State.CandidateJobHistoryVersion,
			TargetIdentityDigest:      targetDigest,
		},
		DuckLake: transitionpreflight.DuckLakeProjection{
			Compatibility:        duckLakeCompatibility,
			Predecessor:          evidence.DuckLake.State.Predecessor,
			Candidate:            evidence.DuckLake.State.Candidate,
			TargetIdentityDigest: targetDigest,
		},
	}, nil
}
