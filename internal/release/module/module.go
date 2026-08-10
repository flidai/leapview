package module

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/flidai/leapview/internal/platform/jobs"
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
	deployments        release.DeploymentLinkage
	servingProvenance  release.ServingStateProvenanceRepository
	environment        string
	api                APIConfig
	logger             *slog.Logger
}

type Config struct {
	Database          *sql.DB
	States            ServingStateRepository
	Workspaces        WorkspaceProvisioner
	ManagedDataPins   release.ManagedDataPins
	ManagedDataHook   validate.Hook
	ArtifactDirectory string
	Environment       servingstate.Environment
	API               APIConfig
	Logger            *slog.Logger
}

type ServingStateRepository interface {
	release.ServingStateRepository
	validate.Repository
	ActiveArtifact(
		context.Context,
		servingstate.WorkspaceID,
		servingstate.Environment,
	) (servingstate.State, servingstate.Artifact, error)
	RecordDuckLakeSnapshot(context.Context, servingstate.ID, int64) error
}

type WorkspaceProvisioner interface {
	release.WorkspaceRepository
}

func Build(_ context.Context, config Config) (*Module, error) {
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
		Releases: releases, Finalization: finalization, States: config.States, Workspaces: config.Workspaces,
		Artifacts: store, Validator: validator, Pins: config.ManagedDataPins, Environment: config.Environment,
		CandidateProvenance: candidateProvenance,
	})
	if err != nil {
		return nil, err
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Module{
		service: service,
		candidateArtifacts: &candidateArtifactService{
			states: config.States, workspaces: config.Workspaces,
			artifacts: store, validator: validator,
			environment: servingstate.NormalizeEnvironment(config.Environment),
			pins:        config.ManagedDataPins,
		},
		catalog: catalog, deployments: deployments, servingProvenance: servingProvenance,
		environment: string(config.Environment), api: config.API, logger: logger,
	}, nil
}

func (m *Module) ProvenanceForServingState(
	ctx context.Context,
	servingStateID string,
	workspaceID string,
) (release.Provenance, error) {
	if m == nil || m.servingProvenance == nil {
		return release.Provenance{}, release.ErrNotFound
	}
	return m.servingProvenance.ProvenanceForServingState(ctx, servingStateID, workspaceID)
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

func (m *Module) RetainCandidateProvenance(
	ctx context.Context,
	projectID string,
	provenance release.Provenance,
) (release.Provenance, error) {
	if m == nil || m.service == nil {
		return release.Provenance{}, release.ErrCandidateArtifactUnavailable
	}
	return m.service.RetainCandidateProvenance(ctx, projectID, provenance)
}

func (m *Module) CandidateProvenance(
	ctx context.Context,
	projectID,
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
