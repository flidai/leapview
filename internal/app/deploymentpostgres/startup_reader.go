package deploymentpostgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/deployment"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
)

// StartupTarget is the immutable target-fence projection needed by readiness
// checks. It intentionally omits mutation methods and repository internals.
type StartupTarget struct {
	TargetID, ProjectID, Environment string
	TargetRevision                   int64
	ActiveGenerationID               string
	ActivePublicationID              string
}

type StartupGeneration struct {
	GenerationID, TargetID, CandidateID, SnapshotSealID, PlanID          string
	PlanDigest, ArtifactRoot, ArtifactRootDigest, ServingArtifactDigest  string
	CompiledGraphDigest, CompiledConfigDigest, SecurityDomainFingerprint string
}

type StartupPublication struct {
	PublicationID, TargetID, GenerationID, CandidateID, SnapshotSealID string
	State                                                              string
	ExpectedTargetRevision, ResultTargetRevision                       int64
}

type StartupSnapshotSeal struct {
	SealID, AttemptID, CandidateID                                                                     string
	PhysicalPoolID, TenantDomain, Region, EncryptionDomain, ObjectNamespace                            string
	CatalogDatabase, CatalogID, CatalogUUID                                                            string
	CatalogVersion, DuckLakeSnapshotID                                                                 int64
	RelationNamespace, RelationManifestDigest, ClosureDigest                                           string
	ObjectRoot, ObjectRootDigest, ArtifactRoot, ArtifactRootDigest                                     string
	ServingArtifactID, ServingArtifactDigest                                                           string
	CompiledGraphDigest, CompiledConfigDigest, SecurityDomainFingerprint                               string
	RequestDigest, PlanDigest, CompatibilityDigest                                                     string
	DuckDBVersion, RuntimeVersion, DuckLakeExtensionVersion, DuckLakeSpecVersion, CatalogSchemaVersion string
}

// StartupReader is the composition-owned, read-only deployment authority
// used by PostgreSQL delivery startup checks.
type StartupReader interface {
	Target(context.Context, string) (StartupTarget, error)
	Generation(context.Context, string) (StartupGeneration, error)
	Publication(context.Context, string) (StartupPublication, error)
	SnapshotSeal(context.Context, string) (StartupSnapshotSeal, error)
}

type nativeStartupRepository interface {
	Target(context.Context, string) (nativepostgres.DeliveryTarget, error)
	Generation(context.Context, string) (nativepostgres.DeliveryGeneration, error)
	Publication(context.Context, string) (nativepostgres.DeliveryPublication, error)
	SnapshotSeal(context.Context, string) (nativepostgres.SnapshotSeal, error)
}

type startupReader struct{ repository nativeStartupRepository }

// NewStartupReader projects the native delivery repository onto the narrow
// startup evidence API. Composition performs this projection once and passes
// only StartupReader through the readiness configuration.
func NewStartupReader(repository *nativepostgres.Repository) StartupReader {
	return newStartupReader(repository)
}

// newStartupReader keeps the native read surface replaceable in package tests
// while production composition remains explicit about its concrete authority.
func newStartupReader(repository nativeStartupRepository) StartupReader {
	if repository == nil {
		return nil
	}
	return startupReader{repository: repository}
}

func (r startupReader) Target(ctx context.Context, id string) (StartupTarget, error) {
	if r.repository == nil {
		return StartupTarget{}, errors.New("deployment PostgreSQL startup reader is not configured")
	}
	value, err := r.repository.Target(ctx, id)
	if err != nil {
		return StartupTarget{}, startupReaderError(err)
	}
	return StartupTarget{TargetID: value.TargetID, ProjectID: value.ProjectID, Environment: value.Environment, TargetRevision: value.TargetRevision, ActiveGenerationID: value.ActiveGenerationID, ActivePublicationID: value.ActivePublicationID}, nil
}

func (r startupReader) Generation(ctx context.Context, id string) (StartupGeneration, error) {
	if r.repository == nil {
		return StartupGeneration{}, errors.New("deployment PostgreSQL startup reader is not configured")
	}
	value, err := r.repository.Generation(ctx, id)
	if err != nil {
		return StartupGeneration{}, startupReaderError(err)
	}
	return StartupGeneration{GenerationID: value.GenerationID, TargetID: value.TargetID, CandidateID: value.CandidateID, SnapshotSealID: value.SnapshotSealID, PlanID: value.PlanID, PlanDigest: value.PlanDigest, ArtifactRoot: value.ArtifactRoot, ArtifactRootDigest: value.ArtifactRootDigest, ServingArtifactDigest: value.ServingArtifactDigest, CompiledGraphDigest: value.CompiledGraphDigest, CompiledConfigDigest: value.CompiledConfigDigest, SecurityDomainFingerprint: value.SecurityDomainFingerprint}, nil
}

func (r startupReader) Publication(ctx context.Context, id string) (StartupPublication, error) {
	if r.repository == nil {
		return StartupPublication{}, errors.New("deployment PostgreSQL startup reader is not configured")
	}
	value, err := r.repository.Publication(ctx, id)
	if err != nil {
		return StartupPublication{}, startupReaderError(err)
	}
	return StartupPublication{PublicationID: value.PublicationID, TargetID: value.TargetID, GenerationID: value.GenerationID, CandidateID: value.CandidateID, SnapshotSealID: value.SnapshotSealID, State: value.State, ExpectedTargetRevision: value.ExpectedTargetRevision, ResultTargetRevision: value.ResultTargetRevision}, nil
}

func (r startupReader) SnapshotSeal(ctx context.Context, id string) (StartupSnapshotSeal, error) {
	if r.repository == nil {
		return StartupSnapshotSeal{}, errors.New("deployment PostgreSQL startup reader is not configured")
	}
	value, err := r.repository.SnapshotSeal(ctx, id)
	if err != nil {
		return StartupSnapshotSeal{}, startupReaderError(err)
	}
	return StartupSnapshotSeal{
		SealID: value.SealID, AttemptID: value.AttemptID, CandidateID: value.CandidateID,
		PhysicalPoolID: value.PhysicalPoolID, TenantDomain: value.TenantDomain, Region: value.Region,
		EncryptionDomain: value.EncryptionDomain, ObjectNamespace: value.ObjectNamespace,
		CatalogDatabase: value.CatalogDatabase, CatalogID: value.CatalogID, CatalogUUID: value.CatalogUUID,
		CatalogVersion: value.CatalogVersion, DuckLakeSnapshotID: value.DuckLakeSnapshotID,
		RelationNamespace: value.RelationNamespace, RelationManifestDigest: value.RelationManifestDigest,
		ClosureDigest: value.ClosureDigest, ObjectRoot: value.ObjectRoot, ObjectRootDigest: value.ObjectRootDigest,
		ArtifactRoot: value.ArtifactRoot, ArtifactRootDigest: value.ArtifactRootDigest,
		ServingArtifactID: value.ServingArtifactID, ServingArtifactDigest: value.ServingArtifactDigest,
		CompiledGraphDigest: value.CompiledGraphDigest, CompiledConfigDigest: value.CompiledConfigDigest,
		SecurityDomainFingerprint: value.SecurityDomainFingerprint, RequestDigest: value.RequestDigest,
		PlanDigest: value.PlanDigest, CompatibilityDigest: value.CompatibilityDigest,
		DuckDBVersion: value.DuckDBVersion, RuntimeVersion: value.RuntimeVersion,
		DuckLakeExtensionVersion: value.DuckLakeExtensionVersion, DuckLakeSpecVersion: value.DuckLakeSpecVersion,
		CatalogSchemaVersion: value.CatalogSchemaVersion,
	}, nil
}

func startupReaderError(err error) error {
	if errors.Is(err, nativepostgres.ErrNotFound) {
		return fmt.Errorf("%w: %v", deployment.ErrNotFound, err)
	}
	return err
}
