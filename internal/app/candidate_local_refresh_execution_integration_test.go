//go:build integration && duckdb_arrow

package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	apprefreshpostgres "github.com/flidai/leapview/internal/app/refreshpostgres"
	"github.com/flidai/leapview/internal/credential"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshartifact "github.com/flidai/leapview/internal/refresh/artifact"
	refreshplan "github.com/flidai/leapview/internal/refresh/plan"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/internal/workload"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRefreshClaimedJobOrchestratesLocalMaterializationBeforeCompletion(t *testing.T) {
	for _, test := range []struct {
		name       string
		dropSource bool
	}{
		{name: "materializes exact source snapshot"},
		{name: "source SQL error fails before completion", dropSource: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLocalMaterializationSourceFixture(t)
			job := localRefreshExecutionJob(t, fixture.job, fixture.binding)
			definition := localRefreshExecutionDefinition(job, fixture.binding)
			require.NoError(t, job.Validate())

			ids := newLocalRefreshExecutionIDs(t, job)
			basePlan := localRefreshExecutionBasePlan(t, job, ids)
			reader := newLocalRefreshExecutionReader(job, ids, basePlan)
			events := []string{}
			reader.events = &events
			mutations := &localRefreshExecutionMutations{test: t, job: job, fixture: fixture, ids: ids, events: &events, reader: reader}
			mutationFactory := &localRefreshExecutionMutationFactory{mutations: mutations, events: &events}

			// Production intentionally rejects every local pin. Exercise that
			// boundary through the native executor before installing the narrowly
			// scoped allowance used to prove the downstream orchestration seam.
			deniedExecutor, err := apprefreshpostgres.NewPostgresNativeRefreshExecutor(
				mutationFactory, reader, fixture.resource.TargetID,
				fixture.factory.authority.revalidator,
				func(ctx context.Context, got refreshrun.JobRecord) error {
					events = append(events, "production-base-credential-check")
					return fixture.factory.checkBaseCredentials(ctx, got)
				},
			)
			require.NoError(t, err)
			_, err = deniedExecutor.Execute(t.Context(), job)
			require.ErrorIs(t, err, errRefreshLocalCredentialUnsupported)
			require.Equal(t, 0, mutationFactory.calls)
			require.Zero(t, mutations.planCompletions)
			require.Zero(t, mutations.buildCompletions)
			require.Zero(t, fixture.keys.decryptCalls)
			require.Equal(t, []string{
				"snapshot", "load-generation", "load-plan", "production-base-credential-check",
			}, events)

			// Do not carry the independent denial branch's observations into the
			// event-order assertion for the real Service execution below.
			events = nil
			reader.events = &events
			mutations.events = &events
			mutationFactory.events = &events
			root := filepath.Join(t.TempDir(), "ducklake")
			environment, err := analyticsducklake.Open(t.Context(), analyticsducklake.Config{
				RootDir: root, MaxConnections: 2, ExtensionAdmission: fixture.admission,
			})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, environment.Close()) })
			mutations.environment = environment
			controller, err := workload.New(workload.DefaultConfig())
			require.NoError(t, err)
			t.Cleanup(controller.Close)
			permit, err := controller.Acquire(t.Context(), workload.Request{
				Class: workload.Refresh, PrincipalID: job.PrincipalID,
				Operation: "candidate-local-refresh-execution-integration", EstimatedMemoryBytes: job.EstimatedMemoryBytes,
			})
			require.NoError(t, err)
			t.Cleanup(permit.Release)
			if test.dropSource {
				_, err := fixture.admin.Exec(t.Context(), "DROP TABLE public.accounts")
				require.NoError(t, err)
			}

			executor, err := apprefreshpostgres.NewPostgresNativeRefreshExecutor(
				mutationFactory, reader, fixture.resource.TargetID,
				fixture.factory.authority.revalidator,
				allowExactCapturedJobForLocalRefreshOrchestrationTest(job, &events),
			)
			require.NoError(t, err)
			runs := &localRefreshExecutionRuns{events: &events, job: job}
			publication := &localRefreshExecutionPublication{t: t, events: &events, mutations: mutations, runs: runs}
			coordinatorEntries := 0
			coordinator, err := apprefreshpostgres.NewNativeCanonicalCompletionCoordinator(
				fixture.resource.TargetID, reader,
				func(_ context.Context, candidate deployment.Deployment) error {
					events = append(events, "validate-publication-ownership")
					require.Equal(t, projectgraph.ServingIdentity{
						ProjectID: job.Identity.ProjectID, Environment: job.Identity.Environment,
						GenerationID: ids.servingState.String(),
					}, candidate.ServingIdentity)
					return nil
				},
				&localRefreshExecutionHost{events: &events, generationID: ids.servingState.String(), candidateID: ids.candidate.String()},
			)
			require.NoError(t, err)
			wrappedCoordinator := refreshrun.CanonicalCompletionCoordinator(func(ctx context.Context, got refreshrun.JobRecord, result refreshrun.CanonicalRefreshResult, complete func() error) error {
				coordinatorEntries++
				events = append(events, "completion-coordinator-enter")
				return coordinator(ctx, got, result, complete)
			})

			state, artifact := localRefreshExecutionServingState(job)
			service := refreshrun.Service{
				ServingStates: journeyRefreshStateReader{state: state, artifact: artifact},
				Artifacts:     journeyRefreshArtifactLoader{definition: definition},
				ResolveSourceDigest: func(context.Context, projectgraph.ServingIdentity) (string, error) {
					return job.PipelinePlan.ArtifactDigest, nil
				},
				AuthorityRevalidator: fixture.factory.authority.revalidator,
				RequireAuthority:     true, Runs: runs, CanonicalExecutor: executor.Execute,
				CanonicalCompletionCoordinator: wrappedCoordinator, Publication: publication,
			}

			ctx, cancel := context.WithTimeout(permit.Context(), 60*time.Second)
			defer cancel()
			err = service.ExecuteClaimedJob(ctx, job)
			if test.dropSource {
				require.Error(t, err)
				require.ErrorIs(t, err, credential.ErrUnavailable)
				require.NotContains(t, err.Error(), fixture.role.Password)
				require.Error(t, mutations.sourceReadError)
				require.Contains(t, strings.ToLower(mutations.sourceReadError.Error()), "accounts")
				require.Contains(t, events, "materialize-source-error")
				require.Equal(t, 1, fixture.keys.decryptCalls)
				require.Equal(t, 1, fixture.repository.calls)
				require.Equal(t, fixture.reference.VersionID, fixture.repository.versionID)
				require.Equal(t, refreshrun.RunStatusFailed, runs.status)
				require.Equal(t, 0, coordinatorEntries)
				require.Zero(t, mutations.buildCompletions)
				require.Zero(t, publication.calls)
				require.Zero(t, mutations.snapshotID, "a failed source read must not synthesize a successful snapshot result")
				require.True(t, mutations.registrationClosed)
				require.True(t, mutations.sourceWorkDrained)
				require.Equal(t, make([]byte, len(fixture.keys.lastPlaintext)), fixture.keys.lastPlaintext)
				require.Empty(t, mutations.retainedAuth)
				require.Contains(t, events, "source-registration-closed")
				require.Contains(t, events, "source-work-drained")
				require.NotContains(t, events, "complete-native-build")
				require.NotContains(t, events, "run-prepared")
				require.NotContains(t, events, "completion-coordinator-enter")
				require.NotContains(t, events, "prepare-runtime-host")
				require.NotContains(t, events, "canonical-publication")
				return
			}

			require.NoError(t, err)
			require.Equal(t, refreshrun.RunStatusSucceeded, runs.status)
			require.Equal(t, 1, coordinatorEntries)
			require.Equal(t, 1, publication.calls)
			require.Equal(t, mutations.snapshotID, publication.result.SnapshotID)
			require.Equal(t, ids.servingState.String(), publication.result.ServingStateID)
			require.Equal(t, publication.result.ServingStateID, publication.result.NativeGenerationID)
			require.Equal(t, mutations.build.CandidateID.String(), reader.generations[publication.result.NativeGenerationID].CandidateID)
			require.Equal(t, mutations.snapshotID, reader.attempt.SnapshotID)
			require.True(t, mutations.registrationClosed)
			require.True(t, mutations.sourceBackendGone)
			require.True(t, mutations.sourceWorkDrained)
			require.Equal(t, 1, fixture.keys.decryptCalls)
			require.Equal(t, make([]byte, len(fixture.keys.lastPlaintext)), fixture.keys.lastPlaintext)
			require.Empty(t, mutations.retainedAuth)
			require.Equal(t, fixture.reference.VersionID, mutations.connectionEvidence.CredentialVersionID)
			require.Equal(t, fixture.binding.Evidence().EndpointConfigHash, mutations.connectionEvidence.EndpointConfigHash)
			require.Equal(t, mutations.connectionEvidence, mutations.acquiredEvidence)
			requireLocalRefreshEventOrder(t, events, "run-lease-checked", "complete-plan", "materialize-source-snapshot", "source-registration-closed", "source-backend-gone", "source-work-drained", "complete-native-build", "run-prepared", "completion-coordinator-enter", "canonical-publication")
		})
	}
}

func localRefreshExecutionJob(t *testing.T, job refreshrun.JobRecord, binding connectionbinding.TargetBinding) refreshrun.JobRecord {
	t.Helper()
	definition := localRefreshExecutionDefinition(job, binding)
	selection, err := refreshplan.ForPipeline(definition, job.Identity.ProjectID, job.PipelineID)
	require.NoError(t, err)
	selection, err = selection.BindGeneration(job.Identity, job.PipelinePlan.ArtifactDigest)
	require.NoError(t, err)
	pipelinePlan, err := selection.DeliveryPipelinePlan(refreshplan.InvocationPolicy{
		InvocationSource: refreshrun.TriggerManual, RunAsPrincipalID: job.PrincipalID,
	})
	require.NoError(t, err)
	job.PipelinePlan = &pipelinePlan
	job.SemanticModelID = projectgraph.ResourceID(pipelinePlan.SemanticModelID)
	job.InvocationSource = refreshrun.TriggerManual
	return job
}

func localRefreshExecutionDefinition(job refreshrun.JobRecord, binding connectionbinding.TargetBinding) *refreshartifact.Definition {
	model := localMaterializationModel()
	return &refreshartifact.Definition{
		Models:        map[string]*semanticmodel.Model{job.SemanticModelID.String(): model},
		ModelTables:   map[string]semanticmodel.Table{"accounts": model.Tables["accounts"]},
		ConnectionIDs: map[string]string{"warehouse": binding.ConnectionID.String()},
		Pipelines: map[string]refreshschedule.Definition{job.PipelineID.String(): {
			ID: job.PipelineID, Name: "Local refresh", SemanticModelID: job.SemanticModelID,
			SelectionDigest: activeResultIdentityDigest('b'),
		}},
	}
}

func localRefreshExecutionServingState(job refreshrun.JobRecord) (servingstate.State, servingstate.Artifact) {
	state := servingstate.State{
		ID: servingstate.ID(job.Identity.GenerationID), ProjectID: job.Identity.ProjectID,
		Environment: servingstate.Environment(job.Identity.Environment), Digest: job.PipelinePlan.ArtifactDigest,
		Status: servingstate.StatusActive,
	}
	return state, servingstate.Artifact{ID: "local-refresh-artifact", ServingStateID: state.ID, Digest: state.Digest}
}

func requireLocalRefreshEventOrder(t *testing.T, events []string, ordered ...string) {
	t.Helper()
	next := 0
	for _, event := range events {
		if next < len(ordered) && event == ordered[next] {
			next++
		}
	}
	require.Equal(t, len(ordered), next, "events %v must preserve order %v", events, ordered)
}

type localRefreshExecutionIDs struct {
	basePlan, baseCandidate, baseSeal                           uuid.UUID
	plan, build, candidate, seal, servingState, writerLease     uuid.UUID
	planDigest, artifactDigest, sourceDigest, attestationDigest string
}

func newLocalRefreshExecutionIDs(t *testing.T, job refreshrun.JobRecord) localRefreshExecutionIDs {
	t.Helper()
	return localRefreshExecutionIDs{
		basePlan: localRefreshExecutionUUID(t), baseCandidate: localRefreshExecutionUUID(t), baseSeal: localRefreshExecutionUUID(t),
		plan: localRefreshExecutionUUID(t), build: localRefreshExecutionUUID(t), candidate: localRefreshExecutionUUID(t),
		seal: localRefreshExecutionUUID(t), servingState: localRefreshExecutionUUID(t), writerLease: localRefreshExecutionUUID(t),
		planDigest: activeResultIdentityDigest('c'), artifactDigest: activeResultIdentityDigest('d'),
		sourceDigest: job.PipelinePlan.ArtifactDigest, attestationDigest: activeResultIdentityDigest('e'),
	}
}

func localRefreshExecutionUUID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	require.NoError(t, err)
	return id
}

func localRefreshExecutionBasePlan(t *testing.T, job refreshrun.JobRecord, ids localRefreshExecutionIDs) deploymentpostgres.DeliveryPlan {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	rich, err := deployment.NewDeliveryPlan(deployment.DeliveryPlan{
		ID: ids.basePlan.String(), ActorID: "source-owner", SourceOwnerID: "source-owner",
		TargetID: job.Authority.Target.InstanceID, ProjectID: job.Identity.ProjectID, Environment: job.Identity.Environment,
		Operation: deployment.DeliveryOperationCodeChange, SourceDigest: ids.sourceDigest,
		ServingArtifactDigest: ids.artifactDigest, BaseGenerationID: job.Identity.GenerationID, BaseTargetRevision: job.TargetRevision,
		Execution: deployment.DeliveryExecutionInputs{
			SourceArtifactDigest: ids.sourceDigest, CompilerDigest: ids.planDigest, ExecutableDigest: ids.planDigest,
			DependencyDigest: ids.planDigest, ConfigDigest: ids.planDigest, BindingDigest: ids.planDigest,
			RuntimeDigest: ids.planDigest, CapabilityDigest: ids.planDigest,
		},
		Provenance: deployment.DeliveryProvenance{AttestationDigest: ids.attestationDigest},
		Governance: deployment.DeliveryGovernance{
			PolicyDigest: ids.planDigest, AuthorizationDigest: ids.planDigest, QualificationDigest: ids.planDigest,
			ApprovalPolicyRevision: 1, ExpiresAt: now.Add(time.Hour),
		},
		Evidence: deployment.DeliveryPlanEvidence{
			ImpactStatement: "local refresh execution integration", PhysicalWorkStatement: "source materialization",
			ReuseStatement: "new candidate snapshot", Qualification: deployment.DeliveryQualificationEvidence{
				Policy: "default", Steps: []deployment.DeliveryQualificationStep{{ID: "source", Kind: "source", Description: "materialize source"}},
			},
			StalePolicy: deployment.DeliveryStalePolicy{Mode: "reject"}, Rollback: deployment.DeliveryRollbackEvidence{Class: deployment.DeliveryServingSafe},
		},
		CreatedAt: now,
	})
	require.NoError(t, err)
	document, err := json.Marshal(rich)
	require.NoError(t, err)
	return deploymentpostgres.DeliveryPlan{
		PlanID: rich.ID, TargetID: job.Authority.Target.InstanceID, PlanDigest: rich.Digest,
		CompiledConfigDigest: rich.Execution.ConfigDigest, SecurityDomainFingerprint: rich.Governance.AuthorizationDigest,
		ArtifactDigest: rich.ServingArtifactDigest, QualificationDigest: rich.Governance.QualificationDigest,
		ApprovalRequired: rich.Governance.RequiresApproval, ApprovalPolicyRevision: rich.Governance.ApprovalPolicyRevision,
		PlanDocument: document,
	}
}

type localRefreshExecutionReader struct {
	target      deploymentpostgres.DeliveryOperatorSnapshot
	plans       map[string]deploymentpostgres.DeliveryPlan
	generations map[string]deploymentpostgres.DeliveryGeneration
	attempt     deploymentpostgres.DeliveryBuildAttempt
	seal        deploymentpostgres.SnapshotSeal
	candidate   deploymentpostgres.DeliveryCandidate
	events      *[]string
}

func newLocalRefreshExecutionReader(job refreshrun.JobRecord, ids localRefreshExecutionIDs, base deploymentpostgres.DeliveryPlan) *localRefreshExecutionReader {
	return &localRefreshExecutionReader{
		target: deploymentpostgres.DeliveryOperatorSnapshot{
			TargetID: job.Authority.Target.InstanceID, ProjectID: job.Identity.ProjectID.String(), Environment: job.Identity.Environment,
			TargetRevision: job.TargetRevision, ActiveGenerationID: job.Identity.GenerationID,
		},
		plans: map[string]deploymentpostgres.DeliveryPlan{ids.basePlan.String(): base},
		generations: map[string]deploymentpostgres.DeliveryGeneration{
			job.Identity.GenerationID: {
				GenerationID: job.Identity.GenerationID, TargetID: job.Authority.Target.InstanceID,
				PlanID: ids.basePlan.String(), PlanDigest: base.PlanDigest, CandidateID: ids.baseCandidate.String(), SnapshotSealID: ids.baseSeal.String(),
			},
		},
	}
}

func (reader *localRefreshExecutionReader) OperatorSnapshot(_ context.Context, _ string) (deploymentpostgres.DeliveryOperatorSnapshot, error) {
	*reader.events = append(*reader.events, "snapshot")
	return reader.target, nil
}

func (reader *localRefreshExecutionReader) LoadPlan(_ context.Context, id string) (deploymentpostgres.DeliveryPlan, error) {
	*reader.events = append(*reader.events, "load-plan")
	plan, ok := reader.plans[id]
	if !ok {
		return deploymentpostgres.DeliveryPlan{}, deploymentpostgres.ErrNotFound
	}
	return plan, nil
}

func (reader *localRefreshExecutionReader) LoadGeneration(_ context.Context, id string) (deploymentpostgres.DeliveryGeneration, error) {
	*reader.events = append(*reader.events, "load-generation")
	generation, ok := reader.generations[id]
	if !ok {
		return deploymentpostgres.DeliveryGeneration{}, deploymentpostgres.ErrNotFound
	}
	return generation, nil
}

func (reader *localRefreshExecutionReader) LoadBuildAttempt(context.Context, string) (deploymentpostgres.DeliveryBuildAttempt, error) {
	*reader.events = append(*reader.events, "load-build-attempt")
	return reader.attempt, nil
}

func (reader *localRefreshExecutionReader) LoadSnapshotSeal(context.Context, string) (deploymentpostgres.SnapshotSeal, error) {
	*reader.events = append(*reader.events, "load-snapshot-seal")
	return reader.seal, nil
}

func (reader *localRefreshExecutionReader) LoadCandidate(context.Context, string) (deploymentpostgres.DeliveryCandidate, error) {
	*reader.events = append(*reader.events, "load-candidate")
	return reader.candidate, nil
}

type localRefreshExecutionMutations struct {
	test                                                     *testing.T
	job                                                      refreshrun.JobRecord
	fixture                                                  localMaterializationSourceFixture
	ids                                                      localRefreshExecutionIDs
	reader                                                   *localRefreshExecutionReader
	environment                                              *analyticsducklake.Environment
	events                                                   *[]string
	planCompletions, buildCompletions                        int
	plan                                                     deploymentmodule.NativeDeliveryPlan
	build                                                    deploymentmodule.NativeDeliveryBuild
	planDigest                                               string
	snapshotID                                               int64
	connectionEvidence                                       deploymentmodule.CandidateConnectionEvidence
	acquiredEvidence                                         deploymentmodule.CandidateConnectionEvidence
	registrationClosed, sourceBackendGone, sourceWorkDrained bool
	retainedAuth                                             semanticmodel.ConnectionAuth
	sourceReadError                                          error
}

func (mutations *localRefreshExecutionMutations) CreatePlan(_ context.Context, request deploymentmodule.NativeDeliveryPlanRequest) (deploymentmodule.NativeDeliveryPlan, error) {
	*mutations.events = append(*mutations.events, "create-plan")
	require.Equal(mutations.test, mutations.job.Identity.ProjectID, request.ProjectID)
	require.Equal(mutations.test, mutations.fixture.resource.TargetID, request.TargetID)
	require.Equal(mutations.test, mutations.job.PipelinePlan, request.PipelinePlan)
	mutations.plan = deploymentmodule.NativeDeliveryPlan{
		ID: mutations.ids.plan, ProjectID: mutations.job.Identity.ProjectID, TargetID: mutations.job.Authority.Target.InstanceID,
		Environment: mutations.job.Identity.Environment, Operation: string(deployment.DeliveryOperationRestatement),
		SourceDigest: mutations.ids.sourceDigest, SourceAttestationDigest: mutations.ids.attestationDigest,
		BaseGenerationID: uuid.MustParse(mutations.job.Identity.GenerationID), BaseTargetRevision: mutations.job.TargetRevision,
		PlanDigest: mutations.ids.planDigest, Status: "planned",
	}
	mutations.planDigest = mutations.ids.planDigest
	return mutations.plan, nil
}

func (mutations *localRefreshExecutionMutations) CompleteNativePlanCommand(context.Context, deploymentmodule.NativeDeliveryPlan) error {
	*mutations.events = append(*mutations.events, "complete-plan")
	mutations.planCompletions++
	return nil
}

func (mutations *localRefreshExecutionMutations) BuildPlan(ctx context.Context, request deploymentmodule.NativeDeliveryBuildRequest) (deploymentmodule.NativeDeliveryBuild, error) {
	*mutations.events = append(*mutations.events, "build-plan-start")
	require.Equal(mutations.test, mutations.plan.ID, request.PlanID)
	connections, err := newRefreshCandidateConnections(ctx, candidateConnectionLeaser{module: mutations.fixture.module}, mutations.fixture.factory, mutations.job)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	identity := mutations.job.Identity
	identity.GenerationID = mutations.ids.servingState.String()
	connectionRequest := deploymentmodule.CandidateConnectionRequest{
		CandidateID: mutations.ids.candidate.String(), Actor: mutations.job.PrincipalID,
		TargetID: mutations.fixture.resource.TargetID, Identity: identity,
		Requirements: []deploymentmodule.CandidateConnectionRequirement{{ConnectionID: mutations.fixture.binding.ConnectionID, ConnectorKind: "postgres"}},
	}
	*mutations.events = append(*mutations.events, "resolve-candidate-connections")
	evidence, err := connections.Resolve(ctx, connectionRequest)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	require.Len(mutations.test, mutations.job.Authority.Permissions, 2)
	require.Len(mutations.test, evidence, 1)
	mutations.connectionEvidence = evidence[0]
	require.Equal(mutations.test, mutations.fixture.reference.VersionID, evidence[0].CredentialVersionID)
	require.Equal(mutations.test, mutations.fixture.binding.Evidence().EndpointConfigHash, evidence[0].EndpointConfigHash)
	require.Equal(mutations.test, mutations.fixture.binding.ID.String(), evidence[0].BindingID)
	require.Zero(mutations.test, mutations.fixture.keys.decryptCalls, "Resolve must remain metadata-only")
	registration, err := connections.Acquire(ctx, connectionRequest)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	closed := false
	closeRegistration := func() error {
		if closed {
			return nil
		}
		closed = true
		err := registration.Close()
		if err == nil {
			mutations.registrationClosed = true
			*mutations.events = append(*mutations.events, "source-registration-closed")
		}
		return err
	}
	defer func() { _ = closeRegistration() }()
	*mutations.events = append(*mutations.events, "acquire-candidate-registration")
	require.Len(mutations.test, registration.Evidence(), 1)
	mutations.acquiredEvidence = registration.Evidence()[0]
	require.Equal(mutations.test, mutations.connectionEvidence, mutations.acquiredEvidence)
	require.Zero(mutations.test, mutations.fixture.keys.decryptCalls, "candidate registration must not decrypt")

	if mutations.environment == nil {
		return deploymentmodule.NativeDeliveryBuild{}, errors.New("DuckLake materialization environment is unavailable")
	}
	projectMaterializer, err := mutations.fixture.module.ProjectMaterializerForEnvironment(mutations.environment)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	writer, ok := projectMaterializer.(analyticsmaterialization.ObservationWriterExecutor)
	if !ok {
		return deploymentmodule.NativeDeliveryBuild{}, errors.New("project materializer does not expose observation writer")
	}
	model := localMaterializationModel()
	materializeRequest := analyticsmaterialization.Request{
		Models: map[string]*semanticmodel.Model{mutations.job.SemanticModelID.String(): model}, ModelTables: model.Tables,
		Identity: identity, CandidateID: mutations.ids.candidate.String(), RelationNamespace: "candidate_local_refresh_execution",
		Environment: servingstate.Environment(identity.Environment), TargetType: "refresh_pipeline", TargetID: mutations.job.PipelineID,
		SemanticDigest: activeResultIdentityDigest('f'), ArtifactDigest: mutations.ids.artifactDigest,
		Tables: append([]string(nil), mutations.job.PipelinePlan.MaterializationScope...),
	}
	useLocal := connections.useLocal
	connections.useLocal = func(sourceCtx context.Context, binding connectionbinding.TargetBinding, snapshot connectionbinding.CredentialSnapshot, logical semanticmodel.Connection, consume func(semanticmodel.Connection) error) error {
		return useLocal(sourceCtx, binding, snapshot, logical, func(connection semanticmodel.Connection) error {
			mutations.retainedAuth = connection.Auth
			mutations.sourceReadError = consume(connection)
			return mutations.sourceReadError
		})
	}
	materializeCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	snapshotID, _, materializeErr := writer.MaterializeWithObservationWriter(materializeCtx, materializeRequest, func(context.Context, []analyticsmaterialize.SourceObservation) error { return nil })
	if materializeErr == nil {
		mutations.snapshotID = snapshotID
		*mutations.events = append(*mutations.events, "materialize-source-snapshot")
		require.Positive(mutations.test, snapshotID)
		require.Equal(mutations.test, 1, mutations.fixture.keys.decryptCalls)
		require.Equal(mutations.test, make([]byte, len(mutations.fixture.keys.lastPlaintext)), mutations.fixture.keys.lastPlaintext, "decrypted source credential bytes must be erased")
		require.Empty(mutations.test, mutations.retainedAuth, "connector auth must be erased after the local connection callback")
	} else {
		*mutations.events = append(*mutations.events, "materialize-source-error")
	}
	if err := closeRegistration(); err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, errors.Join(materializeErr, err)
	}
	countCtx, cancelCount := context.WithTimeout(ctx, 3*time.Second)
	defer cancelCount()
	require.Eventually(mutations.test, func() bool {
		var count int
		err := mutations.fixture.admin.QueryRow(countCtx, "SELECT count(*) FROM pg_stat_activity WHERE usename = $1", mutations.fixture.role.Name).Scan(&count)
		if err == nil && count == 0 {
			mutations.sourceBackendGone = true
		}
		return mutations.sourceBackendGone
	}, 3*time.Second, 10*time.Millisecond)
	*mutations.events = append(*mutations.events, "source-backend-gone")
	pause, err := mutations.fixture.module.PauseSourceWork()
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	drainCtx, cancelDrain := context.WithTimeout(ctx, 3*time.Second)
	drainErr := pause.WaitDrained(drainCtx)
	cancelDrain()
	resumeErr := pause.Resume()
	if drainErr != nil {
		return deploymentmodule.NativeDeliveryBuild{}, drainErr
	}
	if resumeErr != nil {
		return deploymentmodule.NativeDeliveryBuild{}, resumeErr
	}
	mutations.sourceWorkDrained = true
	*mutations.events = append(*mutations.events, "source-work-drained")
	if materializeErr != nil {
		return deploymentmodule.NativeDeliveryBuild{}, materializeErr
	}

	mutations.build = deploymentmodule.NativeDeliveryBuild{
		ID: mutations.ids.build, PlanID: mutations.ids.plan, PlanDigest: mutations.planDigest,
		SourceDigest: mutations.ids.sourceDigest, BaseGenerationID: uuid.MustParse(mutations.job.Identity.GenerationID),
		ServingArtifactDigest: mutations.ids.artifactDigest, WriterLeaseID: mutations.ids.writerLease,
		ServingStateID: mutations.ids.servingState, SealID: mutations.ids.seal, CandidateID: mutations.ids.candidate,
		Status: "sealed",
	}
	mutations.reader.attempt = deploymentpostgres.DeliveryBuildAttempt{
		AttemptID: mutations.ids.build.String(), PlanID: mutations.ids.plan.String(), CandidateID: mutations.ids.candidate.String(),
		State: deploymentpostgres.AttemptCommitted, SnapshotID: snapshotID,
	}
	mutations.reader.seal = deploymentpostgres.SnapshotSeal{
		SealID: mutations.ids.seal.String(), AttemptID: mutations.ids.build.String(), CandidateID: mutations.ids.candidate.String(),
		PlanDigest: mutations.planDigest, DuckLakeSnapshotID: snapshotID,
	}
	mutations.reader.candidate = deploymentpostgres.DeliveryCandidate{
		CandidateID: mutations.ids.candidate.String(), TargetID: mutations.fixture.resource.TargetID,
		PlanID: mutations.ids.plan.String(), AttemptID: mutations.ids.build.String(), SnapshotSealID: mutations.ids.seal.String(), Status: "qualified",
	}
	mutations.reader.generations[mutations.ids.servingState.String()] = deploymentpostgres.DeliveryGeneration{
		GenerationID: mutations.ids.servingState.String(), TargetID: mutations.fixture.resource.TargetID,
		PlanID: mutations.ids.plan.String(), PlanDigest: mutations.planDigest, CandidateID: mutations.ids.candidate.String(),
		SnapshotSealID: mutations.ids.seal.String(), ServingArtifactDigest: mutations.ids.artifactDigest,
	}
	return mutations.build, nil
}

func (mutations *localRefreshExecutionMutations) CompleteNativeBuildCommand(context.Context, deploymentmodule.NativeDeliveryBuild) error {
	require.True(mutations.test, mutations.registrationClosed, "candidate registration must be closed before native build completion")
	require.True(mutations.test, mutations.sourceBackendGone, "source backend session must be gone before native build completion")
	require.True(mutations.test, mutations.sourceWorkDrained, "source work must drain before native build completion")
	require.Positive(mutations.test, mutations.snapshotID)
	*mutations.events = append(*mutations.events, "complete-native-build")
	mutations.buildCompletions++
	return nil
}

type localRefreshExecutionMutationFactory struct {
	mutations *localRefreshExecutionMutations
	events    *[]string
	calls     int
}

func (factory *localRefreshExecutionMutationFactory) ForRefresh(_ context.Context, job refreshrun.JobRecord) (apprefreshpostgres.NativeRefreshDeliveryMutations, error) {
	if !reflect.DeepEqual(job, factory.mutations.job) {
		return nil, errors.New("native mutation scope changed the captured refresh job")
	}
	factory.mutations.job = job
	factory.calls++
	*factory.events = append(*factory.events, "scope-mutations")
	return factory.mutations, nil
}

func allowExactCapturedJobForLocalRefreshOrchestrationTest(expected refreshrun.JobRecord, events *[]string) func(context.Context, refreshrun.JobRecord) error {
	return func(_ context.Context, got refreshrun.JobRecord) error {
		*events = append(*events, "test-only-exact-job-preflight")
		if !reflect.DeepEqual(got, expected) {
			return errors.New("test-only allowance received changed captured refresh authority")
		}
		return nil
	}
}

type localRefreshExecutionRuns struct {
	refreshrun.WorkflowRepository
	refreshrun.LeaseFencedRunRepository
	events *[]string
	status string
	job    refreshrun.JobRecord
}

func (runs *localRefreshExecutionRuns) RunMayPublish(_ context.Context, job refreshrun.JobRecord) (bool, error) {
	*runs.events = append(*runs.events, "run-lease-checked")
	return reflect.DeepEqual(job, runs.job), nil
}

func (runs *localRefreshExecutionRuns) MarkRunPrepared(_ context.Context, job refreshrun.JobRecord) (refreshrun.RunRecord, error) {
	runs.status = refreshrun.RunStatusPrepared
	*runs.events = append(*runs.events, "run-prepared")
	return refreshrun.RunRecord{ID: job.RunID, Status: runs.status}, nil
}

func (runs *localRefreshExecutionRuns) MarkRunTreeFailedClaimed(_ context.Context, _ refreshrun.JobRecord, _ string) error {
	runs.status = refreshrun.RunStatusFailed
	*runs.events = append(*runs.events, "run-failed")
	return nil
}

type localRefreshExecutionPublication struct {
	t         *testing.T
	events    *[]string
	mutations *localRefreshExecutionMutations
	runs      *localRefreshExecutionRuns
	calls     int
	result    refreshrun.CanonicalRefreshResult
}

func (publication *localRefreshExecutionPublication) CompleteCanonicalRefresh(_ context.Context, job refreshrun.JobRecord, result refreshrun.CanonicalRefreshResult) error {
	publication.calls++
	publication.result = result
	publication.runs.status = refreshrun.RunStatusSucceeded
	*publication.events = append(*publication.events, "canonical-publication")
	require.Equal(publication.t, publication.mutations.snapshotID, result.SnapshotID)
	require.Equal(publication.t, publication.mutations.ids.servingState.String(), result.ServingStateID)
	require.Equal(publication.t, result.ServingStateID, result.NativeGenerationID)
	require.Equal(publication.t, job.Identity.GenerationID, publication.mutations.job.Identity.GenerationID)
	require.True(publication.t, publication.mutations.registrationClosed)
	require.True(publication.t, publication.mutations.sourceBackendGone)
	require.True(publication.t, publication.mutations.sourceWorkDrained)
	return nil
}

type localRefreshExecutionHost struct {
	events       *[]string
	generationID string
	candidateID  string
}

func (host *localRefreshExecutionHost) PrepareSealedActivation(_ context.Context, generationID, candidateID string) (*runtimehost.Prepared, error) {
	*host.events = append(*host.events, "prepare-runtime-host")
	if generationID != host.generationID || candidateID != host.candidateID {
		return nil, errors.New("unexpected sealed runtime identity")
	}
	return &runtimehost.Prepared{}, nil
}

func (host *localRefreshExecutionHost) ActivatePreparedContext(_ context.Context, prepared *runtimehost.Prepared, complete func() error) error {
	if prepared == nil {
		return errors.New("prepared runtime is required")
	}
	*host.events = append(*host.events, "activate-runtime-host")
	if err := complete(); err != nil {
		return err
	}
	return nil
}

var _ apprefreshpostgres.NativeRefreshDeliveryReader = (*localRefreshExecutionReader)(nil)
var _ apprefreshpostgres.NativeRefreshDeliveryMutations = (*localRefreshExecutionMutations)(nil)
