package module

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/extension"
	platformobjectstore "github.com/flidai/leapview/internal/platform/objectstore"
	"github.com/flidai/leapview/internal/project"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/servingstate"
)

type Module struct {
	service               *release.Service
	nativeCandidatePhases candidateArtifactPhases
	catalog               release.CatalogRepository
	searchCatalog         projectcatalogSearcher
	deployments           release.DeploymentLinkage
	servingProvenance     release.ServingStateProvenanceRepository
	environment           string
	api                   APIConfig
	logger                *slog.Logger
	extensionPreparation  extension.Preparation
	finalizeExecution     apigencommand.AsyncExecutionContract
	auditIntentConfigured bool
}

// candidateArtifactPhases is the complete phase-aware artifact surface used
// by canonical delivery. Keep the read-only inspect phase distinct from
// materialization and hydration so callers cannot accidentally prepare or
// mutate serving state while planning a delivery.
type candidateArtifactPhases interface {
	InspectCandidateArtifacts(context.Context, release.CandidateArtifactRequest) (release.CandidateArtifactSet, error)
	MaterializeCandidateArtifacts(context.Context, release.CandidateArtifactRequest, release.CandidateArtifactSet) (release.CandidateArtifactSet, error)
	HydrateCandidateArtifacts(context.Context, release.CandidateArtifactRequest, release.CandidateArtifactSet, release.CandidateArtifactIdentity) (release.CandidateArtifactSet, error)
	RecoverCandidateArtifacts(context.Context, release.CandidateArtifactRecoveryRequest) (release.CandidateArtifactSet, error)
}

var _ release.CandidateArtifactRecovery = (*Module)(nil)
var _ candidateArtifactPhases = (*Module)(nil)

type Config struct {
	// Persistence is the PostgreSQL release authority created by
	// NewPostgresPersistence. Build never infers a backend from a raw database
	// handle.
	Persistence *Persistence
	// Catalog is the project/connection read authority. Native PostgreSQL
	// composition injects a PostgreSQL-marked catalog independently
	// from the release mutation authority.
	Catalog Catalog
	// Deployments is optional for native authorities that expose deployment
	// linkage through the cross-capability contract.
	Deployments release.DeploymentLinkage
	// States is the immutable serving-state authority used by native PostgreSQL
	// composition.
	States               ServingStateReader
	ManagedDataPins      ManagedDataPins
	Environment          servingstate.Environment
	API                  APIConfig
	Logger               *slog.Logger
	ExtensionPreparation extension.Preparation
	// CandidateSourceReader is the optional native object-backed source reader.
	// Native candidate inspect is enabled only when this capability is present.
	CandidateSourceReader project.CandidateSourceObjectReader
	// CandidateArtifactStore is the neutral immutable object authority used by
	// native candidate materialization and hydration. It is deliberately
	// separate from release.ArtifactStore.
	CandidateArtifactStore platformobjectstore.ImmutableStore
	// StorageSecurityDomain is the process-bound object namespace isolation
	// identity. Native candidate artifacts reject objects from another domain.
	StorageSecurityDomain string
}

// Catalog is the release-module boundary for project and connection reads.
// It repeats the narrow domain contract so module configuration does not
// expose a repository-owned type from another package.
type Catalog interface {
	GetProject(context.Context, string) (release.ProjectRecord, error)
	ListConnections(context.Context, string, string) ([]release.ConnectionRecord, error)
	GetConnection(context.Context, string, string, string) (release.ConnectionRecord, error)
}

// NativePersistence is the capability-owned release authority consumed by the
// module. It intentionally contains only Release domain contracts; concrete
// PostgreSQL transaction and pool types stay inside the authority package.
// PostgreSQL implementations must also expose PostgreSQLAuthority and
// Configured so native PostgreSQL composition cannot be accidentally backed
// by a nil handle that happens to satisfy the domain methods.
type NativePersistence interface {
	release.Repository
	release.FinalizationUnitOfWork
	release.CandidateProvenanceRepository
	release.ServingStateProvenanceRepository
}

// Persistence is the typed release storage selection passed into Build.
// native is private so callers cannot forge a PostgreSQL marker or route an
// arbitrary repository through the native PostgreSQL path.
type Persistence struct {
	Repository          release.Repository
	Finalization        release.FinalizationUnitOfWork
	CandidateProvenance release.CandidateProvenanceRepository
	ServingProvenance   release.ServingStateProvenanceRepository

	native NativePersistence
}

// NewPostgresPersistence wraps the concrete native release authority. The
// concrete PostgreSQL marker and capability checks prevent a test double from
// being labelled as native PostgreSQL persistence.
func NewPostgresPersistence(native NativePersistence) (Persistence, error) {
	if native == nil {
		return Persistence{}, errors.New("PostgreSQL release persistence is required")
	}
	authority, ok := native.(postgresAuthority)
	if !ok || !authority.Configured() {
		return Persistence{}, errors.New("PostgreSQL release persistence is not configured")
	}
	if !authority.AuditCapable() || !authority.EventCapable() || !authority.WorkflowCapable() {
		return Persistence{}, errors.New("PostgreSQL release audit, event, and workflow authorities are required")
	}
	return Persistence{Repository: native, Finalization: native, CandidateProvenance: native, ServingProvenance: native, native: native}, nil
}

func (p Persistence) isPostgres() bool {
	return p.native != nil && p.Repository == p.native
}

func (p Persistence) validate() error {
	if p.Repository == nil || p.Finalization == nil || p.CandidateProvenance == nil || p.ServingProvenance == nil {
		return errors.New("release persistence surfaces are required")
	}
	if p.isPostgres() {
		if any(p.Finalization) != any(p.native) || any(p.CandidateProvenance) != any(p.native) || any(p.ServingProvenance) != any(p.native) {
			return errors.New("PostgreSQL release persistence surfaces do not match the configured native authority")
		}
		authority, ok := p.native.(postgresAuthority)
		if !ok || !authority.Configured() || !authority.AuditCapable() || !authority.EventCapable() || !authority.WorkflowCapable() {
			return errors.New("PostgreSQL release persistence is not fully configured")
		}
		return nil
	}
	return errors.New("release persistence is not configured as PostgreSQL")
}

type postgresAuthority interface {
	PostgreSQLAuthority()
	Configured() bool
	AuditCapable() bool
	EventCapable() bool
	WorkflowCapable() bool
}

type postgresCatalog interface {
	release.CatalogRepository
	PostgreSQLAuthority()
	Configured() bool
}

type postgresDeployments interface {
	release.DeploymentLinkage
	PostgreSQLAuthority()
	Configured() bool
}

// ServingStateReader is the read-only serving-state/artifact boundary used by
// persisted native releases. It intentionally excludes candidate creation,
// validation promotion, and failure mutation methods outside the immutable
// native lifecycle.
type ServingStateReader interface {
	ByID(context.Context, servingstate.ID) (servingstate.State, error)
	ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error)
}

type ManagedDataPins interface {
	release.PinValidator
	ResolveCandidatePins(context.Context, projectgraph.ResourceID, []projectgraph.ResourceID, string) (map[projectgraph.ResourceID]string, error)
}

type projectcatalogSearcher interface {
	Search(context.Context, projectcatalog.SearchRequest) (projectcatalog.Page, error)
}

func Build(_ context.Context, config Config) (*Module, error) {
	if config.Persistence == nil {
		return nil, errors.New("release persistence is required")
	}
	if err := config.Persistence.validate(); err != nil {
		return nil, err
	}
	native := config.Persistence.native
	if !config.Persistence.isPostgres() || native == nil {
		return nil, errors.New("native PostgreSQL release module requires native PostgreSQL persistence")
	}
	authority, ok := native.(postgresAuthority)
	if !ok || !authority.Configured() {
		return nil, errors.New("native PostgreSQL release module requires configured native PostgreSQL persistence")
	}
	if !authority.AuditCapable() {
		return nil, errors.New("native PostgreSQL release module requires transactional audit capability")
	}
	if !authority.EventCapable() {
		return nil, errors.New("native PostgreSQL release module requires durable event capability")
	}
	if !authority.WorkflowCapable() {
		return nil, errors.New("native PostgreSQL release module requires transactional workflow capability")
	}
	catalogAuthority, ok := config.Catalog.(postgresCatalog)
	if !ok || !catalogAuthority.Configured() {
		return nil, errors.New("native PostgreSQL release module requires configured native PostgreSQL catalog")
	}
	if config.Deployments != nil {
		deploymentsAuthority, ok := config.Deployments.(postgresDeployments)
		if !ok || !deploymentsAuthority.Configured() {
			return nil, errors.New("native PostgreSQL release module requires configured native PostgreSQL deployments")
		}
	}
	if config.States == nil {
		return nil, errors.New("native PostgreSQL release module requires immutable serving-state reader")
	}
	hasSourceReader := config.CandidateSourceReader != nil
	hasArtifactStore := config.CandidateArtifactStore != nil
	hasStorageDomain := config.StorageSecurityDomain != ""
	if !hasSourceReader || !hasArtifactStore || !hasStorageDomain {
		return nil, errors.New("native PostgreSQL release module requires candidate source reader, artifact store, and storage security domain together")
	}
	if !validNativeStorageDomain(config.StorageSecurityDomain) {
		return nil, errors.New("native PostgreSQL release module requires canonical candidate artifact storage security domain")
	}
	environment := config.Environment
	if string(environment) != strings.TrimSpace(string(environment)) {
		return nil, fmt.Errorf("release environment must be canonical")
	}
	if err := servingstate.ValidateEnvironment(environment); err != nil {
		return nil, err
	}
	finalizeExecution, err := loadFinalizeExecutionContract()
	if err != nil {
		return nil, err
	}
	var releases release.Repository
	var finalization release.FinalizationUnitOfWork
	var catalog release.CatalogRepository
	var deployments release.DeploymentLinkage
	var candidateProvenance release.CandidateProvenanceRepository
	var servingProvenance release.ServingStateProvenanceRepository
	releases, finalization = config.Persistence.Repository, config.Persistence.Finalization
	catalog = config.Catalog
	candidateProvenance, servingProvenance = native, native
	deployments = config.Deployments
	if deployments == nil {
		if linked, ok := any(config.Persistence.Repository).(release.DeploymentLinkage); ok {
			deployments = linked
		}
	}
	if candidateProvenance == nil {
		return nil, errors.New("candidate provenance repository is required")
	}
	if servingProvenance == nil {
		return nil, errors.New("serving-state provenance repository is required")
	}
	if catalog == nil {
		return nil, errors.New("release catalog repository is required")
	}
	var (
		store     release.ArtifactStore
		validator release.ArtifactValidator
	)
	// Native serving state is immutable and already persisted by the graph
	// authority. The verifier only reads the admitted state and artifact; there
	// is deliberately no upload/materialization service in this mode.
	validator = immutableArtifactValidator{reader: config.States}
	service, err := release.NewService(release.ServiceOptions{
		Releases: releases, Finalization: finalization,
		Artifacts: store, Validator: validator, Pins: config.ManagedDataPins, Environment: environment,
		CandidateProvenance: candidateProvenance,
	})
	if err != nil {
		return nil, err
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	module := &Module{
		service: service,
		catalog: catalog, deployments: deployments, servingProvenance: servingProvenance,
		searchCatalog: config.API.ProjectSearchCatalog,
		environment:   string(environment), api: config.API, logger: logger,
		finalizeExecution: finalizeExecution, auditIntentConfigured: true,
	}
	if config.CandidateSourceReader != nil {
		module.nativeCandidatePhases = &nativeCandidateArtifactPhases{
			reader: config.CandidateSourceReader, environment: environment,
			states: config.States, provenance: servingProvenance,
			artifacts: config.CandidateArtifactStore, storageDomain: config.StorageSecurityDomain,
			pins: config.ManagedDataPins, extensionPreparation: config.ExtensionPreparation,
		}
	}
	if err := validateFinalizeJobHandlers(finalizeExecution, module.JobHandlers()); err != nil {
		return nil, err
	}
	return module, nil
}

func (m *Module) ProvenanceForServingState(
	ctx context.Context,
	identity projectgraph.ServingIdentity,
) (release.Provenance, error) {
	if m == nil || m.servingProvenance == nil {
		return release.Provenance{}, release.ErrNotFound
	}
	return m.servingProvenance.ProvenanceForServingState(ctx, identity)
}

// SetAuthorizeConnection installs the active-snapshot connection authorizer
// once runtime composition has established the serving lease provider.
func (m *Module) SetAuthorizeConnection(authorizer func(context.Context, string, string, string, access.Capability) (bool, error)) {
	if m != nil {
		m.api.AuthorizeConnection = authorizer
	}
}

// InspectCandidateArtifacts exposes the read-only compiler-evidence phase to
// canonical delivery while retaining the module's nil-safe lifecycle guard.
func (m *Module) InspectCandidateArtifacts(
	ctx context.Context,
	request release.CandidateArtifactRequest,
) (release.CandidateArtifactSet, error) {
	if m == nil {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactUnavailable
	}
	if m.nativeCandidatePhases == nil {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactUnavailable
	}
	return m.nativeCandidatePhases.InspectCandidateArtifacts(ctx, request)
}

// MaterializeCandidateArtifacts exposes the write phase after a durable plan
// has been accepted.
func (m *Module) MaterializeCandidateArtifacts(
	ctx context.Context,
	request release.CandidateArtifactRequest,
	inspected release.CandidateArtifactSet,
) (release.CandidateArtifactSet, error) {
	if m == nil {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactUnavailable
	}
	if m.nativeCandidatePhases == nil {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactUnavailable
	}
	return m.nativeCandidatePhases.MaterializeCandidateArtifacts(ctx, request, inspected)
}

// HydrateCandidateArtifacts reattaches a durable artifact for a retry without
// recompiling or writing a second serving artifact.
func (m *Module) HydrateCandidateArtifacts(
	ctx context.Context,
	request release.CandidateArtifactRequest,
	inspected release.CandidateArtifactSet,
	identity release.CandidateArtifactIdentity,
) (release.CandidateArtifactSet, error) {
	if m == nil {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactUnavailable
	}
	if m.nativeCandidatePhases == nil {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactUnavailable
	}
	return m.nativeCandidatePhases.HydrateCandidateArtifacts(ctx, request, inspected, identity)
}

// RecoverCandidateArtifacts exposes the value-only native serving-artifact
// recovery phase. Recovered physical builds may only be reconstructed from
// immutable bundles.
func (m *Module) RecoverCandidateArtifacts(
	ctx context.Context,
	request release.CandidateArtifactRecoveryRequest,
) (release.CandidateArtifactSet, error) {
	if m == nil || m.nativeCandidatePhases == nil {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactUnavailable
	}
	return m.nativeCandidatePhases.RecoverCandidateArtifacts(ctx, request)
}

func (m *Module) RetainCandidateProvenance(
	ctx context.Context,
	projectID projectgraph.ResourceID,
	provenance release.Provenance,
) (release.Provenance, error) {
	if m == nil || m.service == nil {
		return release.Provenance{}, release.ErrCandidateArtifactUnavailable
	}
	return m.service.RetainCandidateProvenance(ctx, projectID, provenance)
}

func (m *Module) CandidateProvenance(
	ctx context.Context,
	projectID projectgraph.ResourceID,
	candidateID string,
	candidateRevision int64,
) (release.Provenance, error) {
	if m == nil || m.service == nil {
		return release.Provenance{}, release.ErrCandidateArtifactUnavailable
	}
	return m.service.CandidateProvenance(
		ctx,
		projectID,
		candidateID,
		candidateRevision,
	)
}

func (m *Module) PublishCandidate(
	ctx context.Context,
	input release.PublishCandidateInput,
) (release.Release, error) {
	if m == nil || m.service == nil {
		return release.Release{}, release.ErrCandidateArtifactUnavailable
	}
	return m.service.PublishCandidate(ctx, input)
}

type deploymentPublisher struct {
	release.DeploymentLinkage
	service *release.Service
}

func (publisher deploymentPublisher) PublishCandidate(
	ctx context.Context,
	input release.PublishCandidateInput,
) (release.Release, error) {
	return publisher.service.PublishCandidate(ctx, input)
}

func (m *Module) DeploymentLinkage() release.DeploymentPublisher {
	if m == nil || m.deployments == nil || m.service == nil {
		return nil
	}
	return deploymentPublisher{DeploymentLinkage: m.deployments, service: m.service}
}
