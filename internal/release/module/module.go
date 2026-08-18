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
	"github.com/flidai/leapview/internal/platform/jobs"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	releasefilesystem "github.com/flidai/leapview/internal/release/filesystem"
	releasesqlite "github.com/flidai/leapview/internal/release/sqlite"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/internal/servingstate/validate"
)

type Module struct {
	service            *release.Service
	candidateArtifacts *candidateArtifactService
	catalog            release.CatalogRepository
	searchCatalog      projectcatalogSearcher
	deployments        release.DeploymentLinkage
	servingProvenance  release.ServingStateProvenanceRepository
	environment        string
	api                APIConfig
	logger             *slog.Logger
	// ExtensionEvidence is target-side evidence supplied by packaging/admin
	// preparation; project compilation cannot invent or fetch artifacts.
	extensionEvidence func(context.Context) ([]extension.Evidence, error)
	finalizeExecution apigencommand.AsyncExecutionContract
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
	Database          *sql.DB
	States            ServingStateRepository
	ManagedDataPins   ManagedDataPins
	ManagedDataHook   validate.Hook
	ArtifactDirectory string
	Environment       servingstate.Environment
	API               APIConfig
	Logger            *slog.Logger
	ExtensionEvidence func(context.Context) ([]extension.Evidence, error)
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

type ManagedDataPins interface {
	release.PinValidator
	ResolveCandidatePins(context.Context, projectgraph.ResourceID, []projectgraph.ResourceID, string) (map[projectgraph.ResourceID]string, error)
}

type projectcatalogSearcher interface {
	Search(context.Context, projectcatalog.SearchRequest) (projectcatalog.Page, error)
}

func Build(_ context.Context, config Config) (*Module, error) {
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
	releases, finalization, catalog, deployments, err := releaseStores(config.Database, config.API.Workflow)
	if err != nil {
		return nil, err
	}
	candidateProvenance, ok := releases.(release.CandidateProvenanceRepository)
	if !ok {
		return nil, errors.New("candidate provenance repository is required")
	}
	servingProvenance, ok := releases.(release.ServingStateProvenanceRepository)
	if !ok {
		return nil, errors.New("serving-state provenance repository is required")
	}
	store := releasefilesystem.NewArtifactStore(config.ArtifactDirectory)
	hooks := []validate.Hook{}
	if config.ManagedDataHook != nil {
		hooks = append(hooks, config.ManagedDataHook)
	}
	validator := validate.NewService(config.States, store, releasefilesystem.Validator{}, hooks...)
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
		candidateArtifacts: &candidateArtifactService{
			states:    config.States,
			artifacts: store, validator: validator,
			environment:       environment,
			extensionEvidence: config.ExtensionEvidence,
			pins:              config.ManagedDataPins, provenance: servingProvenance,
		},
		catalog: catalog, deployments: deployments, servingProvenance: servingProvenance,
		searchCatalog: config.API.ProjectSearchCatalog,
		environment:   string(environment), api: config.API, logger: logger,
		finalizeExecution: finalizeExecution,
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
	if m == nil || m.candidateArtifacts == nil {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactUnavailable
	}
	return m.candidateArtifacts.InspectCandidateArtifacts(ctx, request)
}

// MaterializeCandidateArtifacts exposes the write phase after a durable plan
// has been accepted.
func (m *Module) MaterializeCandidateArtifacts(
	ctx context.Context,
	request release.CandidateArtifactRequest,
	inspected release.CandidateArtifactSet,
) (release.CandidateArtifactSet, error) {
	if m == nil || m.candidateArtifacts == nil {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactUnavailable
	}
	return m.candidateArtifacts.MaterializeCandidateArtifacts(ctx, request, inspected)
}

// HydrateCandidateArtifacts reattaches a durable artifact for a retry without
// recompiling or writing a second serving artifact.
func (m *Module) HydrateCandidateArtifacts(
	ctx context.Context,
	request release.CandidateArtifactRequest,
	inspected release.CandidateArtifactSet,
	identity release.CandidateArtifactIdentity,
) (release.CandidateArtifactSet, error) {
	if m == nil || m.candidateArtifacts == nil {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactUnavailable
	}
	return m.candidateArtifacts.HydrateCandidateArtifacts(ctx, request, inspected, identity)
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

func releaseStores(database *sql.DB, workflow ...jobs.WorkflowRecorder) (release.Repository, release.FinalizationUnitOfWork, release.CatalogRepository, release.DeploymentLinkage, error) {
	if database == nil {
		return nil, nil, nil, nil, errors.New("release database is required")
	}
	var recorder jobs.WorkflowRecorder
	if len(workflow) > 0 {
		recorder = workflow[0]
	}
	owned := releasesqlite.NewRepositoryWithWorkflow(database, recorder)
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
