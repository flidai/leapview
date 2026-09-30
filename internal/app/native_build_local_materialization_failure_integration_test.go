//go:build integration && duckdb_arrow

package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	appdeploymentpostgres "github.com/flidai/leapview/internal/app/deploymentpostgres"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	manageddataresolver "github.com/flidai/leapview/internal/manageddata/resolver"
	"github.com/flidai/leapview/internal/project"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/internal/workload"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestNativeBuildPlanLocalSourceFailureSettlesIndeterminate(t *testing.T) {
	source := newLocalMaterializationSourceFixture(t)
	job := source.job
	fixture := NewPostgresJourneyFixture(t, PostgresJourneyFixtureOptions{
		TargetID: job.Authority.Target.InstanceID, ProjectID: job.Identity.ProjectID, SkipRouteAssembly: true,
	})
	catalog := newLocalNativeBuildCatalogFixture(t, source.admission)
	_, err := fixture.Graph.DuckLakeControlLedger.RegisterCatalog(t.Context(), catalog.contract.Catalog)
	require.NoError(t, err)

	connections, err := newRefreshCandidateConnections(t.Context(), candidateConnectionLeaser{module: source.module}, source.factory, job)
	require.NoError(t, err)
	var decryptUse, sourceUse atomic.Int32
	var sourceQueryFailure atomic.Pointer[nativeBuildSourceQueryFailure]
	var retainedAuth semanticmodel.ConnectionAuth
	originalUseLocal := connections.useLocal
	connections.useLocal = func(ctx context.Context, binding connectionbinding.TargetBinding, snapshot connectionbinding.CredentialSnapshot, logical semanticmodel.Connection, consume func(semanticmodel.Connection) error) error {
		decryptUse.Add(1)
		return originalUseLocal(ctx, binding, snapshot, logical, func(got semanticmodel.Connection) error {
			sourceUse.Add(1)
			retainedAuth = got.Auth
			err := consume(got)
			if err != nil {
				sourceQueryFailure.Store(&nativeBuildSourceQueryFailure{err: err})
			}
			return err
		})
	}

	bundle := nativeBuildLocalSourceBundle(t)
	projectDigest := bundle.Digest()
	bundlePlan := projectcompiler.BundlePlan{
		Connections:    []string{"connection:warehouse"},
		Sources:        []string{"source:accounts_source"},
		Models:         []string{"model:accounts"},
		SemanticModels: []string{"semantic:refresh"},
		Pipelines:      []string{"pipeline:refresh"},
	}
	_, servingDigest, err := projectbundle.PackCompiledProject(bundle, bundlePlan, io.Discard)
	require.NoError(t, err)
	connectionsRequest := deploymentmodule.CandidateConnectionRequest{
		CandidateID: uuid.NewString(), Actor: job.PrincipalID, TargetID: job.Authority.Target.InstanceID,
		Identity: job.Identity, Requirements: []deploymentmodule.CandidateConnectionRequirement{{ConnectionID: source.binding.ConnectionID, ConnectorKind: "postgres"}},
	}
	bindingEvidence, err := connections.Resolve(t.Context(), connectionsRequest)
	require.NoError(t, err)
	bindingDigest, err := deployment.BindingFingerprint(bindingEvidence)
	require.NoError(t, err)

	planID := uuid.Must(uuid.NewV7())
	created := time.Now().UTC().Truncate(time.Microsecond)
	plan, err := deployment.NewDeliveryPlan(deployment.DeliveryPlan{
		ID: planID.String(), ActorID: job.PrincipalID, SourceOwnerID: "customer-owner",
		TargetID: job.Authority.Target.InstanceID, ProjectID: job.Identity.ProjectID, Environment: job.Identity.Environment,
		Operation: deployment.DeliveryOperationCodeChange, SourceDigest: projectDigest, ServingArtifactDigest: servingDigest,
		BaseTargetRevision: 1, CreatedAt: created,
		Execution:  deployment.DeliveryExecutionInputs{SourceArtifactDigest: projectDigest, CompilerDigest: activeResultIdentityDigest('a'), ExecutableDigest: activeResultIdentityDigest('b'), DependencyDigest: activeResultIdentityDigest('c'), ConfigDigest: activeResultIdentityDigest('d'), BindingDigest: bindingDigest, RuntimeDigest: activeResultIdentityDigest('e'), CapabilityDigest: activeResultIdentityDigest('f')},
		Provenance: deployment.DeliveryProvenance{Repository: "fixture", SourceRevision: "source-revision", Builder: job.PrincipalID, AttestationDigest: activeResultIdentityDigest('1')},
		Governance: deployment.DeliveryGovernance{PolicyDigest: activeResultIdentityDigest('2'), PolicyRevision: 1, AuthorizationDigest: activeResultIdentityDigest('3'), QualificationDigest: activeResultIdentityDigest('4'), ExpiresAt: created.Add(time.Hour), ApprovalPolicyRevision: 1},
		Evidence:   deployment.DeliveryPlanEvidence{ImpactStatement: "local source failure qualification", PhysicalWorkStatement: "materialize accounts source", ReuseStatement: "new source materialization", Qualification: deployment.DeliveryQualificationEvidence{Policy: "required", Steps: []deployment.DeliveryQualificationStep{{ID: "source", Kind: "contract", Description: "read source", Required: true, Blocking: true}}}, StalePolicy: deployment.DeliveryStalePolicy{Mode: "reject"}, Rollback: deployment.DeliveryRollbackEvidence{Class: deployment.DeliveryServingSafe}},
	})
	require.NoError(t, err)
	planDoc, err := json.Marshal(plan)
	require.NoError(t, err)
	_, err = fixture.Graph.DeploymentRepository.CreateTarget(t.Context(), deploymentpostgres.TargetInput{TargetID: plan.TargetID, ProjectID: plan.ProjectID.String(), Environment: plan.Environment})
	require.NoError(t, err)
	_, err = fixture.Graph.DeploymentRepository.ClaimProject(t.Context(), deployment.ProjectClaimInput{ProjectID: plan.ProjectID, Environment: servingstate.Environment(plan.Environment), ClaimedBy: "bootstrap-admin", ClaimedAt: created})
	require.NoError(t, err)
	_, err = fixture.Graph.DeploymentRepository.CreatePlan(t.Context(), deploymentpostgres.PlanInput{
		PlanID: plan.ID, TargetID: plan.TargetID, PlanRevision: 1, PlanDigest: plan.Digest,
		CompiledGraphDigest: bundle.Graph().Digest(), CompiledConfigDigest: plan.Execution.ConfigDigest,
		SecurityDomainFingerprint: plan.Governance.AuthorizationDigest, ArtifactDigest: servingDigest,
		QualificationDigest: plan.Governance.QualificationDigest, QualificationRequired: true,
		ApprovalRequired: false, ApprovalPolicyRevision: 1, PlanDocument: planDoc, Evidence: json.RawMessage(`{}`), CreatedAt: created,
	})
	require.NoError(t, err)

	preflightErr := source.factory.checkBaseCredentials(t.Context(), job)
	require.ErrorIs(t, preflightErr, errRefreshLocalCredentialUnsupported)
	require.Zero(t, source.keys.decryptCalls, "production base preflight must remain denied")
	_, err = source.admin.Exec(t.Context(), "DROP TABLE public.accounts")
	require.NoError(t, err)

	var closes atomic.Int32
	observedConnections := &nativeBuildObservedLocalConnections{inner: connections, closes: &closes}
	operations, ok := fixture.Graph.DeploymentPersistence.Operations.(deploymentmodule.NativeBuildOperationAuthority)
	require.True(t, ok)
	attemptAdmission, err := appdeploymentpostgres.NewCandidateBuildAttemptAdmission(fixture.Graph.DeploymentRepository, fixture.Graph.DuckLakeControlLedger)
	require.NoError(t, err)
	attemptTermination, err := appdeploymentpostgres.NewAttemptTermination(fixture.Graph.DeploymentRepository)
	require.NoError(t, err)
	heartbeat, err := appdeploymentpostgres.NewNativeBuildHeartbeat(fixture.Graph.DeploymentRepository, operations)
	require.NoError(t, err)
	phases := nativeBuildLocalArtifactPhases{set: release.CandidateArtifactSet{
		Artifact: release.ProjectArtifactProvenance{SourceDigest: projectDigest, ProjectDigest: projectDigest, ContentDigest: projectDigest, CompilerVersion: projectartifact.CompilerVersion, SchemaVersion: projectartifact.Version},
		Compiler: release.CandidateCompilerEvidence{Graph: bundle.Graph(), Manifest: bundle.Manifest(), Artifact: bundle, Plan: bundlePlan},
		Generation: release.CandidateGenerationArtifact{ArtifactDigest: servingDigest, ServingArtifactID: "serving-local-source-failure", DataMode: release.GenerationDataRefreshSources, DataRevision: "sources:" + projectDigest, Deterministic: bundlePlan.Deterministic,
			Connections: []release.CandidateConnectionRequirement{{ConnectionID: source.binding.ConnectionID, ConnectorKind: "postgres"}}},
	}}
	managed := nativeBuildLocalManagedData{}
	coordinator, err := appdeploymentpostgres.NewNativeBuildCoordinator(appdeploymentpostgres.NativeBuildConfig{
		Repository: fixture.Graph.DeploymentRepository, TargetID: plan.TargetID, Environment: plan.Environment,
		Sources:   nativeBuildLocalSourceReader{snapshot: project.CandidateSourceSnapshot{ProjectID: plan.ProjectID, ArtifactDigest: projectDigest, SourceAttestationDigest: plan.Provenance.AttestationDigest, ProjectDigest: projectDigest, SourceRevision: &project.CandidateSourceRevision{Repository: "fixture", Revision: "source-revision"}}},
		Artifacts: &phases, ArtifactRecovery: nativeBuildLocalArtifactRecovery{}, BindingEvidence: observedConnections, Connections: observedConnections, ManagedData: managed,
		Contract: nativeBuildLocalContract{contract: catalog.contract}, PhysicalPoolID: catalog.contract.PhysicalPoolID, CompatibilityDigest: catalog.contract.CompatibilityDigest,
		Operations: operations, Heartbeat: heartbeat, AttemptAdmission: attemptAdmission, AttemptTermination: attemptTermination,
		GenerationAdmission: nativeBuildLocalGenerationAdmission{},
		PhysicalFactory: appdeploymentpostgres.DuckLakePhysicalBuildEnvironmentFactory{Config: catalog.config, CatalogID: catalog.contract.Catalog.CatalogID,
			MaterializerFactory: func(environment *ducklake.Environment) (analyticsmaterialization.Executor, error) {
				return source.module.ProjectMaterializerForEnvironment(environment)
			}},
		ObservationWriter: fixture.Graph.DuckLakeControlLedger, MarkerResolverFactory: nativeBuildLocalMarkerFactory{}, MarkerQuarantine: fixture.Graph.DuckLakeControlLedger,
		ObservationReader: nativeBuildLocalObservationReader{}, SnapshotFactory: nativeBuildLocalSnapshotFactory{}, QualificationFactory: nativeBuildLocalQualificationFactory{},
		RuntimeVersion: "integration-test", Events: fixture.Graph.DeploymentPersistence.Events, Audit: fixture.Graph.DeploymentPersistence.Audit,
	})
	require.NoError(t, err)
	controller, err := workload.New(workload.DefaultConfig())
	require.NoError(t, err)
	t.Cleanup(controller.Close)
	permit, err := controller.Acquire(t.Context(), workload.Request{Class: workload.Refresh, PrincipalID: job.PrincipalID, Operation: "native-build-local-source-failure", EstimatedMemoryBytes: 1})
	require.NoError(t, err)
	t.Cleanup(permit.Release)
	_, buildErr := coordinator.BuildPlan(permit.Context(), deploymentmodule.NativeDeliveryBuildRequest{
		ProjectID: plan.ProjectID, TargetID: plan.TargetID, Environment: plan.Environment, PlanID: planID,
		PrincipalID: job.PrincipalID, IdempotencyKey: "local-source-materialization-failure",
	})
	require.Error(t, buildErr)
	failure, ok := appdeploymentpostgres.NativePhysicalBuildFailureOf(buildErr)
	require.True(t, ok, "BuildPlan should return classified physical materialization failure: %v", buildErr)
	require.Equal(t, appdeploymentpostgres.NativePhysicalBuildPhaseMaterialize, failure.Phase)
	require.Equal(t, appdeploymentpostgres.NativePhysicalFailureIndeterminate, failure.Classification)
	require.NotContains(t, buildErr.Error(), source.role.Password)
	require.EqualValues(t, 1, decryptUse.Load())
	require.EqualValues(t, 1, sourceUse.Load())
	queryFailure := sourceQueryFailure.Load()
	require.NotNil(t, queryFailure, "the source consumer should report the missing relation")
	require.Error(t, queryFailure.err, "the source query should fail after the fixture relation is dropped")
	require.EqualValues(t, 1, closes.Load())
	require.Empty(t, retainedAuth)
	require.Equal(t, make([]byte, len(source.keys.lastPlaintext)), source.keys.lastPlaintext)
	require.Equal(t, 1, source.repository.calls)
	require.Equal(t, source.reference.VersionID, source.repository.versionID)

	var operationState, attemptID string
	var operationEvidence []byte
	require.NoError(t, fixture.RuntimePool.QueryRow(t.Context(), `SELECT state, attempt_id::text, attempt_evidence FROM platform.operation WHERE scope_id=$1 AND idempotency_key=$2`, plan.TargetID, "local-source-materialization-failure").Scan(&operationState, &attemptID, &operationEvidence))
	require.Equal(t, string(deploymentmodule.NativeOperationStateIndeterminate), operationState)
	attempt, err := fixture.Graph.DeploymentRepository.BuildAttempt(t.Context(), attemptID)
	require.NoError(t, err)
	require.Equal(t, deploymentpostgres.AttemptIndeterminate, attempt.State)
	var evidence struct {
		Phase          string `json:"phase"`
		Classification string `json:"classification"`
		ErrorDigest    string `json:"errorDigest"`
	}
	require.NoError(t, json.Unmarshal(attempt.TerminationEvidence, &evidence))
	require.Equal(t, "materialize", evidence.Phase)
	require.Equal(t, "indeterminate", evidence.Classification)
	require.Regexp(t, `^sha256:[0-9a-f]{64}$`, evidence.ErrorDigest)
	queryErrorJSON, err := json.Marshal(queryFailure.err.Error())
	require.NoError(t, err)
	require.NotContains(t, string(operationEvidence), string(queryErrorJSON[1:len(queryErrorJSON)-1]), "raw source-query details must not be persisted")
	require.JSONEq(t, string(attempt.TerminationEvidence), string(operationEvidence))
	candidate, err := fixture.Graph.DeploymentRepository.Candidate(t.Context(), attempt.CandidateID)
	require.NoError(t, err)
	require.Equal(t, "building", candidate.Status)
	artifactBinding, err := fixture.Graph.DeploymentRepository.BuildArtifactBinding(t.Context(), attemptID)
	require.NoError(t, err)
	require.Equal(t, servingDigest, artifactBinding.ServingArtifactDigest)
	var leaseCount, releasedCount, sealCount, generationCount int
	require.NoError(t, fixture.RuntimePool.QueryRow(t.Context(), `SELECT count(*), count(*) FILTER (WHERE state='released' AND released_at IS NOT NULL) FROM delivery.delivery_lease WHERE target_id=$1 AND owner_id=$2`, plan.TargetID, job.PrincipalID).Scan(&leaseCount, &releasedCount))
	require.Equal(t, 1, leaseCount)
	require.Equal(t, 1, releasedCount)
	require.NoError(t, fixture.RuntimePool.QueryRow(t.Context(), `SELECT count(*) FROM delivery.delivery_snapshot_seal WHERE attempt_id=$1::uuid`, attemptID).Scan(&sealCount))
	require.Zero(t, sealCount)
	require.NoError(t, fixture.RuntimePool.QueryRow(t.Context(), `SELECT count(*) FROM delivery.delivery_generation WHERE candidate_id=$1::uuid`, attempt.CandidateID).Scan(&generationCount))
	require.Zero(t, generationCount)
	require.Eventually(t, func() bool {
		var count int
		return source.admin.QueryRow(t.Context(), "SELECT count(*) FROM pg_stat_activity WHERE usename=$1", source.role.Name).Scan(&count) == nil && count == 0
	}, 3*time.Second, 10*time.Millisecond)
	require.ErrorIs(t, source.factory.checkBaseCredentials(t.Context(), job), errRefreshLocalCredentialUnsupported)
	require.EqualValues(t, 1, source.keys.decryptCalls)
}

type nativeBuildSourceQueryFailure struct{ err error }

type nativeBuildObservedLocalConnections struct {
	inner  *refreshCandidateConnections
	closes *atomic.Int32
}

func (c *nativeBuildObservedLocalConnections) Resolve(ctx context.Context, request deploymentmodule.CandidateConnectionRequest) ([]deploymentmodule.CandidateConnectionEvidence, error) {
	return c.inner.Resolve(ctx, request)
}

func (c *nativeBuildObservedLocalConnections) Acquire(ctx context.Context, request deploymentmodule.CandidateConnectionRequest) (deployment.CandidateConnectionLeases, error) {
	leases, err := c.inner.Acquire(ctx, request)
	if err != nil {
		return nil, err
	}
	return nativeBuildObservedCandidateLeases{CandidateConnectionLeases: leases, closes: c.closes}, nil
}

type nativeBuildObservedCandidateLeases struct {
	deployment.CandidateConnectionLeases
	closes *atomic.Int32
}

func (l nativeBuildObservedCandidateLeases) Close() error {
	l.closes.Add(1)
	return l.CandidateConnectionLeases.Close()
}

type nativeBuildLocalArtifactPhases struct{ set release.CandidateArtifactSet }

func (p *nativeBuildLocalArtifactPhases) InspectCandidateArtifacts(_ context.Context, request release.CandidateArtifactRequest) (release.CandidateArtifactSet, error) {
	set := p.set
	set.Artifact.SourceDigest = request.ArtifactDigest
	set.Generation.Identity = projectgraph.ServingIdentity{ProjectID: request.Scope.ProjectID, Environment: request.Scope.Environment, GenerationID: request.GenerationID}
	set.Generation.DataRevision, _ = release.CandidateSourcesDataRevision(request.ArtifactDigest, nil)
	return set, nil
}

func (p *nativeBuildLocalArtifactPhases) MaterializeCandidateArtifacts(_ context.Context, request release.CandidateArtifactRequest, inspected release.CandidateArtifactSet) (release.CandidateArtifactSet, error) {
	inspected.Generation.Identity.GenerationID = request.GenerationID
	return inspected, nil
}

func (p *nativeBuildLocalArtifactPhases) HydrateCandidateArtifacts(context.Context, release.CandidateArtifactRequest, release.CandidateArtifactSet, release.CandidateArtifactIdentity) (release.CandidateArtifactSet, error) {
	return release.CandidateArtifactSet{}, errors.New("failure qualification does not hydrate artifacts")
}

type nativeBuildLocalSourceReader struct {
	snapshot project.CandidateSourceSnapshot
}

func (r nativeBuildLocalSourceReader) SnapshotAttestation(_ context.Context, scope project.CandidateSourceScope, sourceDigest, attestationDigest string) (project.CandidateSourceSnapshot, error) {
	if scope.ProjectID != r.snapshot.ProjectID || sourceDigest != r.snapshot.ArtifactDigest || attestationDigest != r.snapshot.SourceAttestationDigest {
		return project.CandidateSourceSnapshot{}, errors.New("source attestation identity mismatch")
	}
	return r.snapshot, nil
}

type nativeBuildLocalContract struct {
	contract appdeploymentpostgres.NativeBuildContract
}

func (r nativeBuildLocalContract) Resolve(_ context.Context, request appdeploymentpostgres.NativeBuildContractRequest) (appdeploymentpostgres.NativeBuildContract, error) {
	if request.PhysicalPoolID != r.contract.PhysicalPoolID || request.CompatibilityDigest != r.contract.CompatibilityDigest {
		return appdeploymentpostgres.NativeBuildContract{}, deploymentpostgres.ErrConflict
	}
	return r.contract, nil
}

type nativeBuildLocalManagedData struct{}

func (nativeBuildLocalManagedData) ResolveCandidateManagedData(context.Context, projectgraph.ResourceID, map[projectgraph.ResourceID]string) (manageddataresolver.Resolution, error) {
	return manageddataresolver.Resolution{}, nil
}

type nativeBuildLocalArtifactRecovery struct {
	release.CandidateArtifactRecovery
}
type nativeBuildLocalGenerationAdmission struct {
	appdeploymentpostgres.GenerationAdmission
}
type nativeBuildLocalMarkerFactory struct {
	appdeploymentpostgres.NativePhysicalMarkerResolverFactory
}
type nativeBuildLocalObservationReader struct {
	appdeploymentpostgres.NativeSourceObservationReader
}
type nativeBuildLocalSnapshotFactory struct {
	appdeploymentpostgres.NativePhysicalSnapshotInspectorFactory
}
type nativeBuildLocalQualificationFactory struct {
	appdeploymentpostgres.NativeQualificationEnvironmentFactory
}

func nativeBuildLocalSourceBundle(t *testing.T) projectartifact.SourceBundle {
	t.Helper()
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "connection:warehouse", Kind: projectgraph.KindConnection, Name: "warehouse"},
		{ID: "source:accounts_source", Kind: projectgraph.KindSource, Name: "accounts_source"},
		{ID: "model:accounts", Kind: projectgraph.KindModel, Name: "accounts"},
		{ID: "semantic:refresh", Kind: projectgraph.KindSemanticModel, Name: "refresh_semantic"},
		{ID: "pipeline:refresh", Kind: projectgraph.KindPipeline, Name: "refresh"},
	}, []projectgraph.Edge{
		{From: "source:accounts_source", To: "connection:warehouse"}, {From: "model:accounts", To: "source:accounts_source"},
		{From: "semantic:refresh", To: "model:accounts"}, {From: "pipeline:refresh", To: "semantic:refresh"},
	})
	require.NoError(t, err)
	model := localMaterializationModel()
	manifestModel := model.Tables["accounts"]
	manifestModel.SourceDependencies = []string{"source:accounts_source"}
	bundle, err := projectartifact.NewSourceBundle(graph, projectmanifest.ResourceManifest{
		Connections:      map[string]semanticmodel.Connection{"connection:warehouse": {Kind: "postgres"}},
		Sources:          map[string]semanticmodel.Source{"source:accounts_source": {Connection: "connection:warehouse", Object: "public.accounts"}},
		Models:           map[string]semanticmodel.Table{"model:accounts": manifestModel},
		SemanticModels:   map[string]*semanticmodel.Model{"semantic:refresh": model},
		RefreshPipelines: map[string]refreshschedule.Definition{"pipeline:refresh": {ID: "pipeline:refresh", Name: "refresh", SemanticModelID: "semantic:refresh"}},
		NameIndex:        projectmanifest.NameIndex{Connections: map[string]string{"warehouse": "connection:warehouse"}, Sources: map[string]string{"accounts_source": "source:accounts_source"}, Models: map[string]string{"accounts": "model:accounts"}, SemanticModels: map[string]string{"refresh_semantic": "semantic:refresh"}, Pipelines: map[string]string{"refresh": "pipeline:refresh"}},
	})
	require.NoError(t, err)
	return bundle
}
