package module

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/extension"
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	platformobjectstore "github.com/flidai/leapview/internal/platform/objectstore"
	"github.com/flidai/leapview/internal/project"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	releasefilesystem "github.com/flidai/leapview/internal/release/filesystem"
	releasesqlite "github.com/flidai/leapview/internal/release/sqlite"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/internal/servingstate/validate"
)

type Module struct {
	service               *release.Service
	candidateArtifacts    *candidateArtifactService
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
}

var (
	_ release.CandidateArtifactPreparer = (*Module)(nil)
	_ candidateArtifactPhases           = (*Module)(nil)
)

type Config struct {
	// Persistence is the capability-owned release authority. Production
	// composition injects a native PostgreSQL implementation; the module never
	// opens or infers a control-plane database from this interface.
	Persistence NativePersistence
	// Catalog is the project/connection read authority. Native production
	// composition injects a PostgreSQL-marked catalog independently from the
	// release mutation authority; legacy composition derives it from SQLite.
	Catalog Catalog
	// Database is retained only for explicitly selected legacy SQLite
	// development/test composition.
	Database     *sql.DB
	LegacySQLite bool
	Production   bool
	// AuditIntentRecorder is the Access-owned transaction-scoped outbox port.
	// It is required whenever release SQLite persistence is configured.
	AuditIntentRecorder access.AuditIntentRecorder
	// Deployments is optional for native authorities that expose deployment
	// linkage through the legacy cross-capability contract. SQLite composition
	// derives it from the selected repository.
	Deployments release.DeploymentLinkage
	// States is the immutable serving-state authority used by native
	// production composition. Legacy SQLite composition additionally requires
	// this value to implement ServingStateRepository below, but native paths
	// never receive that mutable lifecycle contract.
	States               ServingStateReader
	ManagedDataPins      ManagedDataPins
	ManagedDataHook      validate.Hook
	ArtifactDirectory    string
	Environment          servingstate.Environment
	API                  APIConfig
	Logger               *slog.Logger
	ExtensionPreparation extension.Preparation
	// CandidateSourceReader is the optional native object-backed source reader.
	// Native candidate inspect is enabled only when this capability is present.
	CandidateSourceReader project.CandidateSourceObjectReader
	// CandidateArtifactStore is the neutral immutable object authority used by
	// native candidate materialization and hydration. It is deliberately
	// separate from release.ArtifactStore, which belongs to embedded mode.
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
// Configured so production cannot be accidentally backed by SQLite or a nil
// handle that happens to satisfy the domain methods.
type NativePersistence interface {
	release.Repository
	release.FinalizationUnitOfWork
	release.CandidateProvenanceRepository
	release.ServingStateProvenanceRepository
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

type ServingStateRepository interface {
	validate.Repository
	Create(context.Context, servingstate.CreateInput) (servingstate.State, error)
	ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error)
	ActiveArtifact(
		context.Context,
		projectgraph.ResourceID,
		servingstate.Environment,
	) (servingstate.State, servingstate.Artifact, error)
	RecordDuckLakeSnapshot(context.Context, servingstate.ID, int64) error
}

// ServingStateReader is the read-only serving-state/artifact boundary used by
// persisted native releases. It intentionally excludes candidate creation,
// validation promotion, and failure mutation methods owned by the legacy
// SQLite lifecycle.
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
	native := config.Persistence
	if config.Production {
		if config.Database != nil || config.LegacySQLite {
			return nil, errors.New("production release module rejects SQLite database")
		}
		if native == nil {
			return nil, errors.New("production release module requires native PostgreSQL persistence")
		}
		authority, ok := native.(postgresAuthority)
		if !ok || !authority.Configured() {
			return nil, errors.New("production release module requires configured native PostgreSQL persistence")
		}
		if !authority.AuditCapable() {
			return nil, errors.New("production release module requires transactional audit capability")
		}
		if !authority.EventCapable() {
			return nil, errors.New("production release module requires durable event capability")
		}
		if !authority.WorkflowCapable() {
			return nil, errors.New("production release module requires transactional workflow capability")
		}
		catalog, ok := config.Catalog.(postgresCatalog)
		if !ok || !catalog.Configured() {
			return nil, errors.New("production release module requires configured native PostgreSQL catalog")
		}
		if config.Deployments != nil {
			deployments, ok := config.Deployments.(postgresDeployments)
			if !ok || !deployments.Configured() {
				return nil, errors.New("production release module requires configured native PostgreSQL deployments")
			}
		}
		if config.States == nil {
			return nil, errors.New("production release module requires immutable serving-state reader")
		}
		hasSourceReader := config.CandidateSourceReader != nil
		hasArtifactStore := config.CandidateArtifactStore != nil
		hasStorageDomain := config.StorageSecurityDomain != ""
		if !hasSourceReader || !hasArtifactStore || !hasStorageDomain {
			return nil, errors.New("production release module requires candidate source reader, artifact store, and storage security domain together")
		}
		if !validNativeStorageDomain(config.StorageSecurityDomain) {
			return nil, errors.New("production release module requires canonical candidate artifact storage security domain")
		}
	} else if native != nil {
		return nil, errors.New("native PostgreSQL persistence requires production release mode")
	} else if config.Database != nil {
		if !config.LegacySQLite {
			return nil, errors.New("SQLite release build requires LegacySQLite=true; inject PostgreSQL persistence for production")
		}
		if config.AuditIntentRecorder == nil {
			return nil, errors.New("release audit intent recorder is required")
		}
	} else {
		return nil, errors.New("release persistence is required; choose native repository or explicit SQLite database")
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
	auditIntentConfigured := config.Database != nil && config.AuditIntentRecorder != nil
	if native != nil {
		releases, finalization = native, native
		catalog = config.Catalog
		candidateProvenance, servingProvenance = native, native
		auditIntentConfigured = true
		deployments = config.Deployments
		if deployments == nil {
			if linked, ok := any(native).(release.DeploymentLinkage); ok {
				deployments = linked
			}
		}
	} else {
		releases, finalization, catalog, deployments, err = releaseStoresWithAudit(config.Database, config.API.Workflow, config.AuditIntentRecorder)
		if err != nil {
			return nil, err
		}
		candidateProvenance, _ = releases.(release.CandidateProvenanceRepository)
		servingProvenance, _ = releases.(release.ServingStateProvenanceRepository)
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
	if deployments == nil && native == nil {
		return nil, errors.New("release deployment linkage repository is required")
	}
	var (
		store              release.ArtifactStore
		validator          release.ArtifactValidator
		candidateArtifacts *candidateArtifactService
	)
	if native != nil {
		// Native serving state is immutable and already persisted by the graph
		// authority. The verifier only reads the admitted state and artifact;
		// there is deliberately no upload/materialization service in this mode.
		validator = immutableArtifactValidator{reader: config.States}
	} else {
		legacyStates, ok := config.States.(ServingStateRepository)
		if !ok || legacyStates == nil {
			return nil, errors.New("SQLite release build requires mutable serving-state repository")
		}
		legacyStore := releasefilesystem.NewArtifactStore(config.ArtifactDirectory)
		store = legacyStore
		hooks := []validate.Hook{}
		if config.ManagedDataHook != nil {
			hooks = append(hooks, config.ManagedDataHook)
		}
		legacyValidator := validate.NewService(legacyStates, legacyStore, releasefilesystem.Validator{}, hooks...)
		validator = legacyValidator
		candidateArtifacts = &candidateArtifactService{
			states: legacyStates, artifacts: legacyStore, validator: legacyValidator,
			environment: environment, extensionPreparation: config.ExtensionPreparation,
			pins: config.ManagedDataPins, provenance: servingProvenance,
		}
	}
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
		service:            service,
		candidateArtifacts: candidateArtifacts,
		catalog:            catalog, deployments: deployments, servingProvenance: servingProvenance,
		searchCatalog: config.API.ProjectSearchCatalog,
		environment:   string(environment), api: config.API, logger: logger,
		finalizeExecution: finalizeExecution, auditIntentConfigured: auditIntentConfigured,
	}
	if native != nil && config.CandidateSourceReader != nil {
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

func (m *Module) PrepareCandidateArtifacts(
	ctx context.Context,
	request release.CandidateArtifactRequest,
) (release.CandidateArtifactSet, error) {
	if m == nil || m.candidateArtifacts == nil {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactUnavailable
	}
	return m.candidateArtifacts.Prepare(ctx, request)
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
	if m.candidateArtifacts != nil {
		return m.candidateArtifacts.InspectCandidateArtifacts(ctx, request)
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
	if m.candidateArtifacts != nil {
		return m.candidateArtifacts.MaterializeCandidateArtifacts(ctx, request, inspected)
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
	if m.candidateArtifacts != nil {
		return m.candidateArtifacts.HydrateCandidateArtifacts(ctx, request, inspected, identity)
	}
	if m.nativeCandidatePhases == nil {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactUnavailable
	}
	return m.nativeCandidatePhases.HydrateCandidateArtifacts(ctx, request, inspected, identity)
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

func releaseStores(database *sql.DB, workflow ...jobplatform.WorkflowRecorder) (release.Repository, release.FinalizationUnitOfWork, release.CatalogRepository, release.DeploymentLinkage, error) {
	var recorder jobplatform.WorkflowRecorder
	if len(workflow) > 0 {
		recorder = workflow[0]
	}
	return releaseStoresWithAudit(database, recorder, nil)
}

func releaseStoresWithAudit(database *sql.DB, workflow jobplatform.WorkflowRecorder, audit access.AuditIntentRecorder) (release.Repository, release.FinalizationUnitOfWork, release.CatalogRepository, release.DeploymentLinkage, error) {
	if database == nil {
		return nil, nil, nil, nil, errors.New("release database is required")
	}
	owned := releasesqlite.NewRepositoryWithWorkflowAndAudit(database, workflow, audit)
	return owned, owned, owned, owned, nil
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
