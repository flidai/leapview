// Package releasemigrationowners adapts concrete PostgreSQL-era subsystem
// owners to migration-compatibility/v2 envelopes. Production constructors
// accept concrete repositories only; arbitrary evidence producers are not an
// input to this boundary.
package releasemigrationowners

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	physicalpoolpostgres "github.com/flidai/leapview/internal/analytics/physicalpool/postgres"
	"github.com/flidai/leapview/internal/deployment/extensionsupply"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	postgresmigrations "github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/release/migrationcompatibility"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
	"github.com/jackc/pgx/v5/pgxpool"
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
	PhysicalPoolID               string
}

type bindingOwner struct {
	artifacts *releasepostgres.Repository
	targets   *deploymentpostgres.Repository
}

type resolvedBinding struct {
	binding migrationcompatibility.BindingV2
	target  deploymentpostgres.DeliveryTarget
}

type GooseOwnerV2 struct {
	binding bindingOwner
	control *sql.DB
}

type RiverJobsOwnerV2 struct {
	binding bindingOwner
	control *sql.DB
	river   *pgxpool.Pool
}

type poolOwners struct {
	binding  bindingOwner
	duckLake *ducklakepostgres.Repository
	pools    *physicalpoolpostgres.Repository
	supply   *extensionsupply.Supply
}

type DuckLakeOwnerV2 struct{ owners poolOwners }
type PhysicalPoolOwnerV2 struct{ owners poolOwners }

func NewGooseOwnerV2(artifacts *releasepostgres.Repository, targets *deploymentpostgres.Repository, control *sql.DB) (*GooseOwnerV2, error) {
	if artifacts == nil || targets == nil || control == nil {
		return nil, ErrOwnerUnavailable
	}
	return &GooseOwnerV2{binding: bindingOwner{artifacts: artifacts, targets: targets}, control: control}, nil
}

func NewRiverJobsOwnerV2(artifacts *releasepostgres.Repository, targets *deploymentpostgres.Repository, control *sql.DB, river *pgxpool.Pool) (*RiverJobsOwnerV2, error) {
	if artifacts == nil || targets == nil || control == nil || river == nil {
		return nil, ErrOwnerUnavailable
	}
	return &RiverJobsOwnerV2{binding: bindingOwner{artifacts: artifacts, targets: targets}, control: control, river: river}, nil
}

func NewDuckLakeOwnerV2(artifacts *releasepostgres.Repository, targets *deploymentpostgres.Repository, duckLake *ducklakepostgres.Repository, pools *physicalpoolpostgres.Repository, supply *extensionsupply.Supply) (*DuckLakeOwnerV2, error) {
	owners, err := newPoolOwners(artifacts, targets, duckLake, pools, supply)
	if err != nil {
		return nil, err
	}
	return &DuckLakeOwnerV2{owners: owners}, nil
}

func NewPhysicalPoolOwnerV2(artifacts *releasepostgres.Repository, targets *deploymentpostgres.Repository, duckLake *ducklakepostgres.Repository, pools *physicalpoolpostgres.Repository, supply *extensionsupply.Supply) (*PhysicalPoolOwnerV2, error) {
	owners, err := newPoolOwners(artifacts, targets, duckLake, pools, supply)
	if err != nil {
		return nil, err
	}
	return &PhysicalPoolOwnerV2{owners: owners}, nil
}

func newPoolOwners(artifacts *releasepostgres.Repository, targets *deploymentpostgres.Repository, duckLake *ducklakepostgres.Repository, pools *physicalpoolpostgres.Repository, supply *extensionsupply.Supply) (poolOwners, error) {
	if artifacts == nil || targets == nil || duckLake == nil || pools == nil || supply == nil {
		return poolOwners{}, ErrOwnerUnavailable
	}
	return poolOwners{binding: bindingOwner{artifacts: artifacts, targets: targets}, duckLake: duckLake, pools: pools, supply: supply}, nil
}

func (owner *GooseOwnerV2) Resolve(ctx context.Context, selection SelectionV2) (migrationcompatibility.GooseOwnerEvidenceV2, error) {
	if owner == nil || owner.control == nil {
		return migrationcompatibility.GooseOwnerEvidenceV2{}, ErrOwnerUnavailable
	}
	resolved, err := owner.binding.resolve(ctx, selection)
	if err != nil {
		return migrationcompatibility.GooseOwnerEvidenceV2{}, err
	}
	state, err := postgresmigrations.ReadControlCompatibilityState(ctx, owner.control)
	if err != nil {
		return migrationcompatibility.GooseOwnerEvidenceV2{}, fmt.Errorf("%w: Goose: %v", ErrOwnerState, err)
	}
	compatibility := compatibilityFor(state.InstalledRevision == state.EmbeddedRevision)
	evidence, err := migrationcompatibility.NewGooseOwnerEvidenceV2(resolved.binding, compatibility, migrationcompatibility.GooseStateV2{
		PredecessorSchemaVersion: fmt.Sprintf("goose/v%d", state.InstalledRevision),
		CandidateSchemaVersion:   fmt.Sprintf("goose/v%d", state.EmbeddedRevision),
	})
	if err != nil {
		return migrationcompatibility.GooseOwnerEvidenceV2{}, err
	}
	if err := owner.binding.confirm(ctx, selection, resolved); err != nil {
		return migrationcompatibility.GooseOwnerEvidenceV2{}, err
	}
	return evidence, nil
}

func (owner *RiverJobsOwnerV2) Resolve(ctx context.Context, selection SelectionV2) (migrationcompatibility.RiverJobsOwnerEvidenceV2, error) {
	if owner == nil || owner.control == nil || owner.river == nil {
		return migrationcompatibility.RiverJobsOwnerEvidenceV2{}, ErrOwnerUnavailable
	}
	resolved, err := owner.binding.resolve(ctx, selection)
	if err != nil {
		return migrationcompatibility.RiverJobsOwnerEvidenceV2{}, err
	}
	state, err := postgresmigrations.ReadRiverJobsCompatibilityState(ctx, owner.river, owner.control)
	if err != nil {
		return migrationcompatibility.RiverJobsOwnerEvidenceV2{}, fmt.Errorf("%w: River/jobs: %v", ErrOwnerState, err)
	}
	installedRiver, err := postgresmigrations.CanonicalMigrationVersions("river", state.InstalledRiverVersions)
	if err != nil {
		return migrationcompatibility.RiverJobsOwnerEvidenceV2{}, err
	}
	embeddedRiver, err := postgresmigrations.CanonicalMigrationVersions("river", state.EmbeddedRiverVersions)
	if err != nil {
		return migrationcompatibility.RiverJobsOwnerEvidenceV2{}, err
	}
	evidence, err := migrationcompatibility.NewRiverJobsOwnerEvidenceV2(resolved.binding, compatibilityFor(
		slices.Equal(state.InstalledRiverVersions, state.EmbeddedRiverVersions) && state.InstalledJobsRevision == state.EmbeddedJobsRevision,
	), migrationcompatibility.RiverJobsStateV2{
		PredecessorSchemaVersion:     installedRiver,
		CandidateSchemaVersion:       embeddedRiver,
		PredecessorJobHistoryVersion: fmt.Sprintf("jobs/goose/v%d", state.InstalledJobsRevision),
		CandidateJobHistoryVersion:   fmt.Sprintf("jobs/goose/v%d", state.EmbeddedJobsRevision),
	})
	if err != nil {
		return migrationcompatibility.RiverJobsOwnerEvidenceV2{}, err
	}
	if err := owner.binding.confirm(ctx, selection, resolved); err != nil {
		return migrationcompatibility.RiverJobsOwnerEvidenceV2{}, err
	}
	return evidence, nil
}

func (owner *DuckLakeOwnerV2) Resolve(ctx context.Context, selection SelectionV2) (migrationcompatibility.DuckLakeOwnerEvidenceV2, error) {
	if owner == nil {
		return migrationcompatibility.DuckLakeOwnerEvidenceV2{}, ErrOwnerUnavailable
	}
	resolved, state, err := owner.owners.resolve(ctx, selection)
	if err != nil {
		return migrationcompatibility.DuckLakeOwnerEvidenceV2{}, err
	}
	evidence, err := migrationcompatibility.NewDuckLakeOwnerEvidenceV2(resolved.binding, compatibilityFor(state.predecessor == state.candidate && state.predecessorCatalogSchema == state.candidateCatalogSchema), migrationcompatibility.DuckLakeStateV2{
		Predecessor: state.predecessor, Candidate: state.candidate,
		PredecessorCatalogSchemaVersion: state.predecessorCatalogSchema,
		CandidateCatalogSchemaVersion:   state.candidateCatalogSchema,
	})
	if err != nil {
		return migrationcompatibility.DuckLakeOwnerEvidenceV2{}, err
	}
	if err := owner.owners.binding.confirm(ctx, selection, resolved); err != nil {
		return migrationcompatibility.DuckLakeOwnerEvidenceV2{}, err
	}
	return evidence, nil
}

func (owner *PhysicalPoolOwnerV2) Resolve(ctx context.Context, selection SelectionV2) (migrationcompatibility.PhysicalPoolOwnerEvidenceV2, error) {
	if owner == nil {
		return migrationcompatibility.PhysicalPoolOwnerEvidenceV2{}, ErrOwnerUnavailable
	}
	resolved, state, err := owner.owners.resolve(ctx, selection)
	if err != nil {
		return migrationcompatibility.PhysicalPoolOwnerEvidenceV2{}, err
	}
	evidence, err := migrationcompatibility.NewPhysicalPoolOwnerEvidenceV2(resolved.binding, compatibilityFor(state.predecessor == state.candidate), migrationcompatibility.PhysicalPoolStateV2{Predecessor: state.predecessor, Candidate: state.candidate})
	if err != nil {
		return migrationcompatibility.PhysicalPoolOwnerEvidenceV2{}, err
	}
	if err := owner.owners.binding.confirm(ctx, selection, resolved); err != nil {
		return migrationcompatibility.PhysicalPoolOwnerEvidenceV2{}, err
	}
	return evidence, nil
}

func (owner bindingOwner) resolve(ctx context.Context, selection SelectionV2) (resolvedBinding, error) {
	if owner.artifacts == nil || owner.targets == nil {
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

func (owner bindingOwner) confirm(ctx context.Context, selection SelectionV2, before resolvedBinding) error {
	after, err := owner.resolve(ctx, selection)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrOwnerStale, err)
	}
	if after.binding != before.binding || after.target.TargetRevision != before.target.TargetRevision {
		return ErrOwnerStale
	}
	return nil
}

type resolvedPoolState struct {
	predecessor, candidate                           physicalpool.Compatibility
	predecessorCatalogSchema, candidateCatalogSchema string
}

func (owners poolOwners) resolve(ctx context.Context, selection SelectionV2) (resolvedBinding, resolvedPoolState, error) {
	resolved, err := owners.binding.resolve(ctx, selection)
	if err != nil {
		return resolvedBinding{}, resolvedPoolState{}, err
	}
	if selection.PhysicalPoolID == "" || selection.PhysicalPoolID != strings.TrimSpace(selection.PhysicalPoolID) {
		return resolvedBinding{}, resolvedPoolState{}, fmt.Errorf("%w: physical pool selector", ErrOwnerMismatch)
	}
	current, err := owners.duckLake.LoadCatalogRuntimeCompatibility(ctx, selection.PhysicalPoolID)
	if err != nil {
		return resolvedBinding{}, resolvedPoolState{}, fmt.Errorf("%w: DuckLake runtime: %v", ErrOwnerState, err)
	}
	predecessorContract, err := owners.pools.LoadAdmissionContractByCompatibilityDigest(ctx, physicalpool.PoolID(selection.PhysicalPoolID), current.CompatibilityDigest)
	if err != nil {
		return resolvedBinding{}, resolvedPoolState{}, fmt.Errorf("%w: predecessor PhysicalPool admission: %v", ErrOwnerState, err)
	}
	if predecessorContract.Pool.Identity.IsolationBoundary != resolved.target.TargetID || predecessorContract.Pool.ID.String() != selection.PhysicalPoolID ||
		predecessorContract.Pool.Compatibility.DuckDBRuntime != current.DuckDBRuntime || predecessorContract.Pool.Compatibility.DuckLakeExtension != current.DuckLakeExtension || predecessorContract.Pool.Compatibility.CatalogFormat != current.CatalogFormat {
		return resolvedBinding{}, resolvedPoolState{}, fmt.Errorf("%w: target, DuckLake, and PhysicalPool predecessor", ErrOwnerMismatch)
	}
	admitted, err := owners.supply.AdmitExtension(ctx, "ducklake")
	if err != nil {
		return resolvedBinding{}, resolvedPoolState{}, fmt.Errorf("%w: candidate DuckLake supply: %v", ErrOwnerState, err)
	}
	candidate := predecessorContract.Pool.Compatibility
	candidate.DuckDBRuntime, err = runtimeComponent("duckdb", admitted.DuckDBVersion)
	if err != nil {
		return resolvedBinding{}, resolvedPoolState{}, err
	}
	candidate.DuckLakeExtension, err = runtimeComponent("ducklake", admitted.ExtensionVersion)
	if err != nil {
		return resolvedBinding{}, resolvedPoolState{}, err
	}
	candidateDigest, err := candidate.Digest()
	if err != nil {
		return resolvedBinding{}, resolvedPoolState{}, fmt.Errorf("%w: candidate PhysicalPool tuple: %v", ErrOwnerState, err)
	}
	candidateContract, err := owners.pools.LoadAdmissionContractByCompatibilityDigest(ctx, physicalpool.PoolID(selection.PhysicalPoolID), candidateDigest)
	if err != nil {
		return resolvedBinding{}, resolvedPoolState{}, fmt.Errorf("%w: candidate PhysicalPool admission: %v", ErrOwnerState, err)
	}
	if candidateContract.Pool.ID != predecessorContract.Pool.ID || candidateContract.Pool.Compatibility != candidate {
		return resolvedBinding{}, resolvedPoolState{}, fmt.Errorf("%w: candidate PhysicalPool admission", ErrOwnerMismatch)
	}
	return resolved, resolvedPoolState{
		predecessor: predecessorContract.Pool.Compatibility, candidate: candidateContract.Pool.Compatibility,
		predecessorCatalogSchema: current.CatalogSchemaVersion,
		candidateCatalogSchema:   current.CatalogSchemaVersion,
	}, nil
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

func runtimeComponent(prefix, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\x00\r\n/\\") {
		return "", fmt.Errorf("%w: %s runtime component", ErrOwnerState, prefix)
	}
	if index := strings.IndexByte(value, ':'); index >= 0 {
		if value[:index] != prefix {
			return "", fmt.Errorf("%w: %s runtime prefix", ErrOwnerState, prefix)
		}
		value = value[index+1:]
	}
	value = strings.TrimPrefix(value, "v")
	if value == "" {
		return "", fmt.Errorf("%w: %s runtime version", ErrOwnerState, prefix)
	}
	return prefix + ":" + value, nil
}
