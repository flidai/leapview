// Package releasemigrationowners adapts concrete PostgreSQL-era subsystem
// owners to migration-compatibility/v2 envelopes. Production constructors
// accept concrete repositories only; arbitrary evidence producers are not an
// input to this boundary.
package releasemigrationowners

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/release/migrationcapability"
	"github.com/flidai/leapview/internal/release/migrationcompatibility"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
)

var (
	ErrOwnerUnavailable = errors.New("migration compatibility v2 concrete owner is unavailable")
	ErrOwnerState       = errors.New("migration compatibility v2 concrete owner state is invalid")
	ErrOwnerStale       = errors.New("migration compatibility v2 concrete owner state changed during resolution")
	ErrOwnerMismatch    = errors.New("migration compatibility v2 concrete owner binding mismatch")
)

// SelectionV2 contains lookup selectors only. In particular, it cannot carry
// artifact-admission digests, target-identity digests, compatibility verdicts,
// version projections, or owner envelopes.
type SelectionV2 struct {
	PredecessorArtifactReference string
	CandidateArtifactReference   string
	TargetID                     string
}

type bindingOwner struct {
	artifacts    *releasepostgres.Repository
	targets      *deploymentpostgres.Repository
	capabilities *releasepostgres.MigrationCapabilityAuthority
}

type resolvedBinding struct {
	binding migrationcompatibility.BindingV2
	target  deploymentpostgres.DeliveryTarget
}

type GooseOwnerV2 struct{ binding bindingOwner }
type RiverJobsOwnerV2 struct{ binding bindingOwner }
type DuckLakeOwnerV2 struct{ binding bindingOwner }
type PhysicalPoolOwnerV2 struct{ binding bindingOwner }

func NewGooseOwnerV2(artifacts *releasepostgres.Repository, targets *deploymentpostgres.Repository, capabilities *releasepostgres.MigrationCapabilityAuthority) (*GooseOwnerV2, error) {
	binding, err := newBindingOwner(artifacts, targets, capabilities)
	if err != nil {
		return nil, err
	}
	return &GooseOwnerV2{binding: binding}, nil
}

func NewRiverJobsOwnerV2(artifacts *releasepostgres.Repository, targets *deploymentpostgres.Repository, capabilities *releasepostgres.MigrationCapabilityAuthority) (*RiverJobsOwnerV2, error) {
	binding, err := newBindingOwner(artifacts, targets, capabilities)
	if err != nil {
		return nil, err
	}
	return &RiverJobsOwnerV2{binding: binding}, nil
}

func NewDuckLakeOwnerV2(artifacts *releasepostgres.Repository, targets *deploymentpostgres.Repository, capabilities *releasepostgres.MigrationCapabilityAuthority) (*DuckLakeOwnerV2, error) {
	binding, err := newBindingOwner(artifacts, targets, capabilities)
	if err != nil {
		return nil, err
	}
	return &DuckLakeOwnerV2{binding: binding}, nil
}

func NewPhysicalPoolOwnerV2(artifacts *releasepostgres.Repository, targets *deploymentpostgres.Repository, capabilities *releasepostgres.MigrationCapabilityAuthority) (*PhysicalPoolOwnerV2, error) {
	binding, err := newBindingOwner(artifacts, targets, capabilities)
	if err != nil {
		return nil, err
	}
	return &PhysicalPoolOwnerV2{binding: binding}, nil
}

func newBindingOwner(artifacts *releasepostgres.Repository, targets *deploymentpostgres.Repository, capabilities *releasepostgres.MigrationCapabilityAuthority) (bindingOwner, error) {
	if artifacts == nil || targets == nil || capabilities == nil {
		return bindingOwner{}, ErrOwnerUnavailable
	}
	return bindingOwner{artifacts: artifacts, targets: targets, capabilities: capabilities}, nil
}

func (owner *GooseOwnerV2) Resolve(ctx context.Context, selection SelectionV2) (migrationcompatibility.GooseOwnerEvidenceV2, error) {
	if owner == nil {
		return migrationcompatibility.GooseOwnerEvidenceV2{}, ErrOwnerUnavailable
	}
	resolved, err := owner.binding.resolve(ctx, selection)
	if err != nil {
		return migrationcompatibility.GooseOwnerEvidenceV2{}, err
	}
	pair, err := owner.binding.resolveCapabilities(ctx, resolved, migrationcapability.SubsystemGoose)
	if err != nil {
		return migrationcompatibility.GooseOwnerEvidenceV2{}, err
	}
	predecessor, candidate := pair.predecessor.Goose, pair.candidate.Goose
	evidence, err := migrationcompatibility.NewGooseOwnerEvidenceV2(resolved.binding, compatibilityFor(
		bidirectionallyRunnable(predecessor.SchemaVersion, predecessor.RunnableSchemaVersions, candidate.SchemaVersion, candidate.RunnableSchemaVersions),
	), migrationcompatibility.GooseStateV2{
		PredecessorSchemaVersion: predecessor.SchemaVersion,
		CandidateSchemaVersion:   candidate.SchemaVersion,
	})
	if err != nil {
		return migrationcompatibility.GooseOwnerEvidenceV2{}, err
	}
	if err := owner.binding.confirm(ctx, selection, resolved, migrationcapability.SubsystemGoose, pair); err != nil {
		return migrationcompatibility.GooseOwnerEvidenceV2{}, err
	}
	return evidence, nil
}

func (owner *RiverJobsOwnerV2) Resolve(ctx context.Context, selection SelectionV2) (migrationcompatibility.RiverJobsOwnerEvidenceV2, error) {
	if owner == nil {
		return migrationcompatibility.RiverJobsOwnerEvidenceV2{}, ErrOwnerUnavailable
	}
	resolved, err := owner.binding.resolve(ctx, selection)
	if err != nil {
		return migrationcompatibility.RiverJobsOwnerEvidenceV2{}, err
	}
	pair, err := owner.binding.resolveCapabilities(ctx, resolved, migrationcapability.SubsystemRiverJobs)
	if err != nil {
		return migrationcompatibility.RiverJobsOwnerEvidenceV2{}, err
	}
	predecessor, candidate := pair.predecessor.RiverJobs, pair.candidate.RiverJobs
	evidence, err := migrationcompatibility.NewRiverJobsOwnerEvidenceV2(resolved.binding, compatibilityFor(
		bidirectionallyRunnable(predecessor.SchemaVersion, predecessor.RunnableSchemaVersions, candidate.SchemaVersion, candidate.RunnableSchemaVersions) &&
			bidirectionallyRunnable(predecessor.JobHistoryVersion, predecessor.RunnableJobHistoryVersions, candidate.JobHistoryVersion, candidate.RunnableJobHistoryVersions),
	), migrationcompatibility.RiverJobsStateV2{
		PredecessorSchemaVersion:     predecessor.SchemaVersion,
		CandidateSchemaVersion:       candidate.SchemaVersion,
		PredecessorJobHistoryVersion: predecessor.JobHistoryVersion,
		CandidateJobHistoryVersion:   candidate.JobHistoryVersion,
	})
	if err != nil {
		return migrationcompatibility.RiverJobsOwnerEvidenceV2{}, err
	}
	if err := owner.binding.confirm(ctx, selection, resolved, migrationcapability.SubsystemRiverJobs, pair); err != nil {
		return migrationcompatibility.RiverJobsOwnerEvidenceV2{}, err
	}
	return evidence, nil
}

func (owner *DuckLakeOwnerV2) Resolve(ctx context.Context, selection SelectionV2) (migrationcompatibility.DuckLakeOwnerEvidenceV2, error) {
	if owner == nil {
		return migrationcompatibility.DuckLakeOwnerEvidenceV2{}, ErrOwnerUnavailable
	}
	resolved, err := owner.binding.resolve(ctx, selection)
	if err != nil {
		return migrationcompatibility.DuckLakeOwnerEvidenceV2{}, err
	}
	pair, err := owner.binding.resolveCapabilities(ctx, resolved, migrationcapability.SubsystemDuckLake)
	if err != nil {
		return migrationcompatibility.DuckLakeOwnerEvidenceV2{}, err
	}
	predecessor, candidate := pair.predecessor.DuckLake, pair.candidate.DuckLake
	evidence, err := migrationcompatibility.NewDuckLakeOwnerEvidenceV2(resolved.binding, compatibilityFor(
		bidirectionallyRunnable(predecessor.CatalogSchemaVersion, predecessor.RunnableCatalogSchemaVersions, candidate.CatalogSchemaVersion, candidate.RunnableCatalogSchemaVersions),
	), migrationcompatibility.DuckLakeStateV2{
		Predecessor: predecessor.Compatibility, Candidate: candidate.Compatibility,
		PredecessorCatalogSchemaVersion: predecessor.CatalogSchemaVersion,
		CandidateCatalogSchemaVersion:   candidate.CatalogSchemaVersion,
	})
	if err != nil {
		return migrationcompatibility.DuckLakeOwnerEvidenceV2{}, err
	}
	if err := owner.binding.confirm(ctx, selection, resolved, migrationcapability.SubsystemDuckLake, pair); err != nil {
		return migrationcompatibility.DuckLakeOwnerEvidenceV2{}, err
	}
	return evidence, nil
}

func (owner *PhysicalPoolOwnerV2) Resolve(ctx context.Context, selection SelectionV2) (migrationcompatibility.PhysicalPoolOwnerEvidenceV2, error) {
	if owner == nil {
		return migrationcompatibility.PhysicalPoolOwnerEvidenceV2{}, ErrOwnerUnavailable
	}
	resolved, err := owner.binding.resolve(ctx, selection)
	if err != nil {
		return migrationcompatibility.PhysicalPoolOwnerEvidenceV2{}, err
	}
	pair, err := owner.binding.resolveCapabilities(ctx, resolved, migrationcapability.SubsystemPhysicalPool)
	if err != nil {
		return migrationcompatibility.PhysicalPoolOwnerEvidenceV2{}, err
	}
	predecessor, candidate := pair.predecessor.PhysicalPool, pair.candidate.PhysicalPool
	predecessorTupleDigest, err := predecessor.Compatibility.Digest()
	if err != nil {
		return migrationcompatibility.PhysicalPoolOwnerEvidenceV2{}, fmt.Errorf("%w: predecessor PhysicalPool tuple", ErrOwnerState)
	}
	candidateTupleDigest, err := candidate.Compatibility.Digest()
	if err != nil {
		return migrationcompatibility.PhysicalPoolOwnerEvidenceV2{}, fmt.Errorf("%w: candidate PhysicalPool tuple", ErrOwnerState)
	}
	evidence, err := migrationcompatibility.NewPhysicalPoolOwnerEvidenceV2(resolved.binding, compatibilityFor(
		slices.Contains(candidate.CompatibleTupleDigests, predecessorTupleDigest) && slices.Contains(predecessor.CompatibleTupleDigests, candidateTupleDigest),
	), migrationcompatibility.PhysicalPoolStateV2{Predecessor: predecessor.Compatibility, Candidate: candidate.Compatibility})
	if err != nil {
		return migrationcompatibility.PhysicalPoolOwnerEvidenceV2{}, err
	}
	if err := owner.binding.confirm(ctx, selection, resolved, migrationcapability.SubsystemPhysicalPool, pair); err != nil {
		return migrationcompatibility.PhysicalPoolOwnerEvidenceV2{}, err
	}
	return evidence, nil
}

func (owner bindingOwner) resolve(ctx context.Context, selection SelectionV2) (resolvedBinding, error) {
	if owner.artifacts == nil || owner.targets == nil || owner.capabilities == nil {
		return resolvedBinding{}, ErrOwnerUnavailable
	}
	if err := validateSelection(selection); err != nil {
		return resolvedBinding{}, err
	}
	predecessor, err := owner.artifacts.ResolveArtifact(ctx, selection.PredecessorArtifactReference)
	if err != nil {
		return resolvedBinding{}, fmt.Errorf("%w: predecessor admission: %v", ErrOwnerState, err)
	}
	candidate, err := owner.artifacts.ResolveArtifact(ctx, selection.CandidateArtifactReference)
	if err != nil {
		return resolvedBinding{}, fmt.Errorf("%w: candidate admission: %v", ErrOwnerState, err)
	}
	if predecessor.ArchitectureMarker != transitionpreflight.ArchitecturePostgreSQL || candidate.ArchitectureMarker != transitionpreflight.ArchitecturePostgreSQL || predecessor.ArtifactAdmissionDigest == candidate.ArtifactAdmissionDigest {
		return resolvedBinding{}, fmt.Errorf("%w: artifact pair", ErrOwnerMismatch)
	}
	target, err := owner.targets.Target(ctx, selection.TargetID)
	if err != nil {
		return resolvedBinding{}, fmt.Errorf("%w: deployment target: %v", ErrOwnerState, err)
	}
	targetDigest, err := (migrationcompatibility.DeploymentTargetIdentityV2{TargetID: target.TargetID, TargetRevision: target.TargetRevision}).Digest()
	if err != nil || target.TargetID != selection.TargetID {
		return resolvedBinding{}, fmt.Errorf("%w: deployment target identity", ErrOwnerMismatch)
	}
	binding := migrationcompatibility.BindingV2{
		PredecessorOCIAdmissionDigest: predecessor.ArtifactAdmissionDigest,
		CandidateOCIAdmissionDigest:   candidate.ArtifactAdmissionDigest,
		TargetIdentityDigest:          targetDigest,
	}
	if err := binding.Validate(); err != nil {
		return resolvedBinding{}, err
	}
	return resolvedBinding{binding: binding, target: target}, nil
}

type resolvedCapabilities struct {
	predecessor       migrationcapability.Capability
	candidate         migrationcapability.Capability
	predecessorDigest string
	candidateDigest   string
}

func (owner bindingOwner) resolveCapabilities(ctx context.Context, binding resolvedBinding, subsystem migrationcapability.Subsystem) (resolvedCapabilities, error) {
	if owner.capabilities == nil {
		return resolvedCapabilities{}, ErrOwnerUnavailable
	}
	predecessor, predecessorDigest, err := owner.resolveCapability(ctx, binding.binding.PredecessorOCIAdmissionDigest, binding.binding.TargetIdentityDigest, subsystem)
	if err != nil {
		return resolvedCapabilities{}, fmt.Errorf("%w: predecessor %s capability: %v", ErrOwnerState, subsystem, err)
	}
	candidate, candidateDigest, err := owner.resolveCapability(ctx, binding.binding.CandidateOCIAdmissionDigest, binding.binding.TargetIdentityDigest, subsystem)
	if err != nil {
		return resolvedCapabilities{}, fmt.Errorf("%w: candidate %s capability: %v", ErrOwnerState, subsystem, err)
	}
	return resolvedCapabilities{
		predecessor: predecessor, candidate: candidate,
		predecessorDigest: predecessorDigest, candidateDigest: candidateDigest,
	}, nil
}

func (owner bindingOwner) resolveCapability(ctx context.Context, artifactDigest, targetDigest string, subsystem migrationcapability.Subsystem) (migrationcapability.Capability, string, error) {
	capability, err := owner.capabilities.ResolveMigrationCapability(ctx, artifactDigest, targetDigest, subsystem)
	if err != nil {
		return migrationcapability.Capability{}, "", err
	}
	if capability.ArtifactAdmissionDigest != artifactDigest || capability.TargetIdentityDigest != targetDigest || capability.Subsystem != subsystem {
		return migrationcapability.Capability{}, "", ErrOwnerMismatch
	}
	if err := capability.Validate(); err != nil {
		return migrationcapability.Capability{}, "", fmt.Errorf("%w: %v", ErrOwnerState, err)
	}
	digest, err := capability.Digest()
	if err != nil {
		return migrationcapability.Capability{}, "", fmt.Errorf("%w: capability digest: %v", ErrOwnerState, err)
	}
	return capability, digest, nil
}

func (owner bindingOwner) confirm(ctx context.Context, selection SelectionV2, before resolvedBinding, subsystem migrationcapability.Subsystem, capabilities resolvedCapabilities) error {
	after, err := owner.resolve(ctx, selection)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrOwnerStale, err)
	}
	if after.binding != before.binding || after.target.TargetRevision != before.target.TargetRevision {
		return ErrOwnerStale
	}
	confirmed, err := owner.resolveCapabilities(ctx, after, subsystem)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrOwnerStale, err)
	}
	if confirmed.predecessorDigest != capabilities.predecessorDigest || confirmed.candidateDigest != capabilities.candidateDigest {
		return ErrOwnerStale
	}
	return nil
}

func compatibilityFor(equal bool) transitionpreflight.CompatibilityState {
	if equal {
		return transitionpreflight.CompatibilityBackwardCompatible
	}
	return transitionpreflight.CompatibilityIncompatible
}

func validateSelection(selection SelectionV2) error {
	for name, value := range map[string]string{
		"predecessor artifact": selection.PredecessorArtifactReference,
		"candidate artifact":   selection.CandidateArtifactReference,
		"target":               selection.TargetID,
	} {
		if value == "" || value != strings.TrimSpace(value) || len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("%w: %s selector", ErrOwnerMismatch, name)
		}
	}
	if selection.PredecessorArtifactReference == selection.CandidateArtifactReference {
		return fmt.Errorf("%w: artifact selectors", ErrOwnerMismatch)
	}
	return nil
}

func bidirectionallyRunnable(predecessor string, predecessorRunnable []string, candidate string, candidateRunnable []string) bool {
	return slices.Contains(candidateRunnable, predecessor) && slices.Contains(predecessorRunnable, candidate)
}
