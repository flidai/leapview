//go:build integration && duckdb_arrow

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/analytics/catalogartifact"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	appdeploymentpostgres "github.com/flidai/leapview/internal/app/deploymentpostgres"
	"github.com/flidai/leapview/internal/app/projectsource"
	"github.com/flidai/leapview/internal/app/runtimefactory"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/extension"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	"github.com/flidai/leapview/internal/platform/objectstore"
	project "github.com/flidai/leapview/internal/project"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectdevloop "github.com/flidai/leapview/internal/project/devloop"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmodule "github.com/flidai/leapview/internal/project/module"
	"github.com/flidai/leapview/internal/release"
	releasemodule "github.com/flidai/leapview/internal/release/module"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/internal/workload"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestNativeBuildPlanLocalSourceAdmissionAndReplay(t *testing.T) {
	source := newLocalMaterializationSourceFixture(t)
	job := source.job
	targetID := job.Authority.Target.InstanceID
	projectID := job.Identity.ProjectID
	environment := job.Identity.Environment
	fixture := NewPostgresJourneyFixture(t, PostgresJourneyFixtureOptions{
		TargetID: targetID, ProjectID: projectID, SkipRouteAssembly: true,
	})
	catalog := newLocalNativeBuildCatalogFixture(t, source.admission)
	_, err := fixture.Graph.DuckLakeControlLedger.RegisterCatalog(t.Context(), catalog.contract.Catalog)
	require.NoError(t, err)
	admin, err := pgxpool.New(t.Context(), fixture.Database.AdminURL())
	require.NoError(t, err)
	t.Cleanup(admin.Close)
	catalog.bootstrapRuntimeCompatibility(t, admin)
	admin.Close()
	require.NoError(t, fixture.Graph.Bootstrap.EnsureInstanceID(t.Context(), targetID))
	require.NoError(t, fixture.Graph.Bootstrap.BindInstanceEnvironment(t.Context(), environment))

	initialTarget, err := fixture.Graph.DeploymentRepository.CreateTarget(t.Context(), deploymentpostgres.TargetInput{
		TargetID: targetID, ProjectID: projectID.String(), Environment: environment,
	})
	require.NoError(t, err)
	claimedAt := time.Now().UTC().Truncate(time.Microsecond)
	_, err = fixture.Graph.DeploymentRepository.ClaimProject(t.Context(), deployment.ProjectClaimInput{
		ProjectID: projectID, Environment: servingstate.Environment(environment), ClaimedBy: job.PrincipalID, ClaimedAt: claimedAt,
	})
	require.NoError(t, err)
	require.NoError(t, projectmodule.EnsureIdentity(t.Context(), fixture.Graph.Project, projectID))
	seedNativeLocalBuildAuthorization(t, fixture, targetID, projectID, environment, uuid.NewString())
	extensionPreparation, ok := source.admission.(extension.Preparation)
	require.True(t, ok)

	domain := uuid.NewString()
	objects, err := objectstore.NewMemoryStore(objectstore.MemoryStoreConfig{StorageSecurityDomain: domain})
	require.NoError(t, err)
	synchronizer, err := projectsource.NewNativeCandidateSourceSynchronizer(projectsource.NativeCandidateSourceConfig{
		Begin:   func(ctx context.Context) (projectsource.Tx, error) { return fixture.RuntimePool.Begin(ctx) },
		Sources: fixture.Graph.Project, Objects: objects, Compiler: projectsource.Compiler{}, StorageSecurityDomain: domain,
	})
	require.NoError(t, err)

	sourceRoot := writeNativeLocalAdmissionProject(t, source.binding.ConnectionID)
	snapshot, err := (projectdevloop.FilesystemBuilder{
		SourceRoot: sourceRoot, ProjectID: projectID, CandidateKey: uuid.NewString(),
	}).Build(t.Context())
	require.NoError(t, err)
	sourceRevision := &project.CandidateSourceRevision{Repository: "local-integration", Revision: snapshot.Digest}
	syncRequest := project.CandidateSynchronizationRequest{
		ArtifactDigest: snapshot.Digest, CandidateKey: snapshot.CandidateKey,
		IdempotencyKey: uuid.NewString(), SourceRevision: sourceRevision,
	}
	for _, artifact := range snapshot.Artifacts {
		syncRequest.Artifacts = append(syncRequest.Artifacts, project.CandidateSourceArtifact{
			Path: artifact.Path, Digest: artifact.Digest, SizeBytes: artifact.SizeBytes,
		})
	}
	syncScope := project.CandidateSourceScope{ProjectID: projectID, OwnerID: source.reference.Scope.OwnerID, CandidateKey: snapshot.CandidateKey}
	syncPlan, err := synchronizer.Plan(t.Context(), syncScope, syncRequest)
	require.NoError(t, err)
	contentsByDigest := make(map[string][]byte, len(snapshot.Artifacts))
	for _, artifact := range snapshot.Artifacts {
		contentsByDigest[artifact.Digest] = artifact.Content
	}
	for _, digest := range syncPlan.MissingDigests {
		content, ok := contentsByDigest[digest]
		require.True(t, ok, "the synchronizer requested a captured source digest")
		require.NoError(t, synchronizer.Upload(t.Context(), syncScope, syncPlan.PlanID, digest, bytes.NewReader(content)))
	}
	syncRequest.PlanID = syncPlan.PlanID
	committed, err := synchronizer.Commit(t.Context(), syncScope, syncRequest)
	require.NoError(t, err)
	require.Equal(t, snapshot.Digest, committed.ArtifactDigest)
	require.NotEmpty(t, committed.SourceAttestationDigest)

	connections, err := newRefreshCandidateConnections(t.Context(), candidateConnectionLeaser{module: source.module}, source.factory, job)
	require.NoError(t, err)
	var closeCount atomic.Int32
	var retainedAuth semanticmodel.ConnectionAuth
	originalUseLocal := connections.useLocal
	connections.useLocal = func(ctx context.Context, binding connectionbinding.TargetBinding, snapshot connectionbinding.CredentialSnapshot, logical semanticmodel.Connection, consume func(semanticmodel.Connection) error) error {
		return originalUseLocal(ctx, binding, snapshot, logical, func(got semanticmodel.Connection) error {
			retainedAuth = got.Auth
			return consume(got)
		})
	}
	observedConnections := &nativeBuildObservedLocalConnections{inner: connections, closes: &closeCount}
	persistence, err := releasemodule.NewPostgresPersistence(fixture.Graph.Release)
	require.NoError(t, err)
	releaseArtifacts, err := releasemodule.Build(t.Context(), releasemodule.Config{
		Persistence: &persistence, Catalog: fixture.Graph.ReleaseCatalog, States: fixture.Graph.ServingState,
		TargetID: targetID, AuthorizationPolicies: fixture.Graph.Access,
		ExtensionPreparation: extensionPreparation, Environment: servingstate.Environment(environment),
		CandidateSourceReader: synchronizer, CandidateArtifactStore: objects, StorageSecurityDomain: domain,
	})
	require.NoError(t, err)
	buildIdentity := buildinfo.Current()
	runtimeVersion := buildIdentity.Version + ":" + buildIdentity.Revision
	policy := runtimefactory.CandidateDeliveryPolicy{
		ApprovalPolicyRevision: runtimefactory.CurrentApprovalPolicyRevision,
		RollbackClass:          deployment.DeliveryServingSafe,
	}
	planCoordinator, err := appdeploymentpostgres.NewNativeCreatePlanCoordinator(appdeploymentpostgres.NativeCreatePlanConfig{
		Repository: fixture.Graph.DeploymentRepository, TargetID: targetID, Environment: environment,
		Sources: synchronizer, Artifacts: releaseArtifacts, BindingEvidence: observedConnections,
		RuntimeVersion: runtimeVersion, Policy: policy,
		SemanticActivation: func(context.Context, projectartifact.SourceBundle) (*deployment.SemanticActivationEvidence, error) {
			return nil, nil
		},
		Events: fixture.Graph.DeploymentPersistence.Events, Audit: fixture.Graph.DeploymentPersistence.Audit,
		Workflow: fixture.Graph.DeploymentPersistence.Workflow, Operations: fixture.Graph.DeploymentPersistence.Operations,
	})
	require.NoError(t, err)
	plan, err := planCoordinator.CreatePlan(t.Context(), deploymentmodule.NativeDeliveryPlanRequest{
		ProjectID: projectID, TargetID: targetID, Environment: environment, PrincipalID: job.PrincipalID,
		SourceOwnerID: source.reference.Scope.OwnerID, Operation: string(deployment.DeliveryOperationCodeChange),
		SourceDigest: committed.ArtifactDigest, SourceAttestationDigest: committed.SourceAttestationDigest,
		IdempotencyKey: uuid.NewString(),
	})
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, plan.ID)

	operations, ok := fixture.Graph.DeploymentPersistence.Operations.(deploymentmodule.NativeBuildOperationAuthority)
	require.True(t, ok)
	attemptAdmission, err := appdeploymentpostgres.NewCandidateBuildAttemptAdmission(fixture.Graph.DeploymentRepository, fixture.Graph.DuckLakeControlLedger)
	require.NoError(t, err)
	attemptTermination, err := appdeploymentpostgres.NewAttemptTermination(fixture.Graph.DeploymentRepository)
	require.NoError(t, err)
	managedDataAdmission, err := appdeploymentpostgres.NewNativeManagedDataBindingAdmission(fixture.Graph.ManagedDataRepository)
	require.NoError(t, err)
	generationAdmission, err := appdeploymentpostgres.NewGenerationAdmission(
		fixture.Graph.DeploymentRepository, fixture.Graph.ServingState, fixture.Graph.Lineage,
		fixture.Graph.DuckLakeControlLedger, managedDataAdmission, fixture.Graph.Release,
	)
	require.NoError(t, err)
	heartbeat, err := appdeploymentpostgres.NewNativeBuildHeartbeat(fixture.Graph.DeploymentRepository, operations)
	require.NoError(t, err)
	qualificationFactory := appdeploymentpostgres.DuckLakeNativeQualificationEnvironmentFactory{
		Config: catalog.config, CatalogID: catalog.contract.Catalog.CatalogID,
		CompatibilityAuthority: fixture.Graph.DuckLakeControlLedger,
	}
	coordinator, err := appdeploymentpostgres.NewNativeBuildCoordinator(appdeploymentpostgres.NativeBuildConfig{
		Repository: fixture.Graph.DeploymentRepository, TargetID: targetID, Environment: environment,
		Sources: synchronizer, Artifacts: releaseArtifacts, ArtifactRecovery: releaseArtifacts,
		BindingEvidence: observedConnections, Connections: observedConnections,
		ManagedData: nativeBuildLocalManagedData{}, Contract: nativeBuildLocalContract{contract: catalog.contract},
		PhysicalPoolID: catalog.contract.PhysicalPoolID, CompatibilityDigest: catalog.contract.CompatibilityDigest,
		Operations: operations, Heartbeat: heartbeat, AttemptAdmission: attemptAdmission,
		AttemptTermination: attemptTermination, GenerationAdmission: generationAdmission,
		PhysicalFactory: appdeploymentpostgres.DuckLakePhysicalBuildEnvironmentFactory{
			Config: catalog.config, CatalogID: catalog.contract.Catalog.CatalogID,
			MaterializerFactory: func(environment *ducklake.Environment) (analyticsmaterialization.Executor, error) {
				return source.module.ProjectMaterializerForEnvironment(environment)
			},
		},
		ObservationWriter:     fixture.Graph.DuckLakeControlLedger,
		MarkerResolverFactory: appdeploymentpostgres.DuckLakePhysicalMarkerResolverFactory{Config: catalog.config},
		MarkerQuarantine:      fixture.Graph.DuckLakeControlLedger, ObservationReader: fixture.Graph.DuckLakeControlLedger,
		SnapshotFactory:      appdeploymentpostgres.NativeQualificationSnapshotInspectorFactory{QualificationFactory: qualificationFactory},
		QualificationFactory: qualificationFactory, RuntimeVersion: runtimeVersion,
		Events: fixture.Graph.DeploymentPersistence.Events, Audit: fixture.Graph.DeploymentPersistence.Audit,
	})
	require.NoError(t, err)
	controller, err := workload.New(workload.DefaultConfig())
	require.NoError(t, err)
	t.Cleanup(controller.Close)
	permit, err := controller.Acquire(t.Context(), workload.Request{
		Class: workload.Refresh, PrincipalID: job.PrincipalID, Operation: "native-build-local-admission", EstimatedMemoryBytes: 1,
	})
	require.NoError(t, err)
	t.Cleanup(permit.Release)

	buildRequest := deploymentmodule.NativeDeliveryBuildRequest{
		ProjectID: projectID, TargetID: targetID, Environment: environment, PlanID: plan.ID,
		PrincipalID: job.PrincipalID, IdempotencyKey: uuid.NewString(),
	}
	require.ErrorIs(t, source.factory.checkBaseCredentials(t.Context(), job), errRefreshLocalCredentialUnsupported)
	require.Zero(t, source.keys.decryptCalls, "planning and denied preflight must not decrypt")
	result, err := coordinator.BuildPlan(permit.Context(), buildRequest)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, result.ID)
	require.NotEqual(t, uuid.Nil, result.SealID)
	require.NotEqual(t, uuid.Nil, result.CandidateID)
	require.NotEqual(t, uuid.Nil, result.ServingStateID)
	require.Equal(t, plan.ID, result.PlanID)
	require.Equal(t, "sealed", result.Status)
	require.Equal(t, snapshot.Digest, result.SourceDigest)
	require.NotEmpty(t, result.ServingArtifactDigest)
	require.Equal(t, 1, source.keys.decryptCalls)
	require.Equal(t, 1, source.repository.calls)
	require.Equal(t, source.reference.VersionID, source.repository.versionID)
	require.EqualValues(t, 1, closeCount.Load())
	require.Empty(t, retainedAuth)
	require.Equal(t, make([]byte, len(source.keys.lastPlaintext)), source.keys.lastPlaintext)
	require.Eventually(t, func() bool {
		var count int
		return source.admin.QueryRow(t.Context(), "SELECT count(*) FROM pg_stat_activity WHERE usename=$1", source.role.Name).Scan(&count) == nil && count == 0
	}, 3*time.Second, 10*time.Millisecond)

	attempt, err := fixture.Graph.DeploymentRepository.BuildAttempt(t.Context(), result.ID.String())
	require.NoError(t, err)
	require.Equal(t, deploymentpostgres.AttemptCommitted, attempt.State)
	require.Equal(t, result.CandidateID.String(), attempt.CandidateID)
	require.Positive(t, attempt.SnapshotID)
	seal, err := fixture.Graph.DeploymentRepository.SnapshotSeal(t.Context(), result.SealID.String())
	require.NoError(t, err)
	require.Equal(t, attempt.AttemptID, seal.AttemptID)
	require.Equal(t, attempt.SnapshotID, seal.DuckLakeSnapshotID)
	require.Equal(t, result.ServingArtifactDigest, seal.ServingArtifactDigest)
	require.Equal(t, catalog.contract.PhysicalPoolID, seal.PhysicalPoolID)
	var qualification appdeploymentpostgres.NativeQualificationEvidence
	require.NoError(t, json.Unmarshal(seal.QualificationEvidence, &qualification))
	require.Equal(t, attempt.SnapshotID, qualification.SnapshotID)
	rowCountChecks := 0
	for _, check := range qualification.Gates.Checks {
		require.Equal(t, release.GateSuccess, check.Outcome)
		if check.Kind == "row_count" && check.ResourceID == "model:accounts" {
			rowCountChecks++
			require.EqualValues(t, 1, check.ObservedRows)
		}
	}
	require.Equal(t, 1, rowCountChecks)
	candidate, err := fixture.Graph.DeploymentRepository.Candidate(t.Context(), result.CandidateID.String())
	require.NoError(t, err)
	require.Equal(t, "qualified", candidate.Status)
	generation, err := fixture.Graph.DeploymentRepository.Generation(t.Context(), result.ServingStateID.String())
	require.NoError(t, err)
	require.Equal(t, result.CandidateID.String(), generation.CandidateID)
	require.Equal(t, seal.SealID, generation.SnapshotSealID)
	require.Equal(t, result.ServingArtifactDigest, generation.ServingArtifactDigest)
	state, err := fixture.Graph.ServingState.ByID(t.Context(), servingstate.ID(result.ServingStateID.String()))
	require.NoError(t, err)
	require.Equal(t, servingstate.StatusValidated, state.Status)
	artifact, err := fixture.Graph.ServingState.ArtifactByServingState(t.Context(), state.ID)
	require.NoError(t, err)
	require.Equal(t, result.ServingArtifactDigest, artifact.Digest)
	require.Equal(t, domain, artifact.StorageSecurityDomain)
	provenance, err := fixture.Graph.Release.CandidateProvenance(t.Context(), projectID, result.CandidateID.String(), candidate.CandidateRevision)
	require.NoError(t, err)
	require.Len(t, provenance.Plan.Bindings, 1)
	require.Equal(t, source.binding.ID.String(), provenance.Plan.Bindings[0].BindingID)
	require.Equal(t, source.reference.VersionID, provenance.Plan.Bindings[0].CredentialVersionID)
	require.Equal(t, source.binding.Evidence().EndpointConfigHash, provenance.Plan.Bindings[0].EndpointConfigHash)
	require.Equal(t, committed.ArtifactDigest, provenance.Artifact.SourceDigest)
	capture, err := fixture.Graph.DuckLakeControlLedger.LoadSourceObservationCapture(t.Context(), result.ID.String())
	require.NoError(t, err)
	observations, err := capture.Observations()
	require.NoError(t, err)
	require.Len(t, observations, 1)
	require.Equal(t, "accounts_source", observations[0].ID)
	marker, err := catalogartifact.DecodeCommitMarker(capture.CommitMarker)
	require.NoError(t, err)
	require.Equal(t, result.ID.String(), marker.AttemptID)
	require.Equal(t, result.ServingStateID.String(), marker.GenerationID)
	require.Equal(t, result.OperationID.String(), marker.DeliveryID)
	require.JSONEq(t, string(attempt.CommitMarker), string(capture.CommitMarker))
	var operationState string
	require.NoError(t, fixture.RuntimePool.QueryRow(t.Context(), `SELECT state FROM platform.operation WHERE scope_id=$1 AND idempotency_key=$2`, targetID, buildRequest.IdempotencyKey).Scan(&operationState))
	require.Equal(t, string(deploymentmodule.NativeOperationStateCompleted), operationState)
	target, err := fixture.Graph.DeploymentRepository.Target(t.Context(), targetID)
	require.NoError(t, err)
	require.Empty(t, target.ActiveGenerationID, "BuildPlan admission must not publish or activate the validated generation")
	require.Empty(t, target.ActivePublicationID)
	require.Equal(t, initialTarget.TargetRevision, target.TargetRevision)
	require.ErrorIs(t, source.factory.checkBaseCredentials(t.Context(), job), errRefreshLocalCredentialUnsupported)
	require.Equal(t, 1, source.keys.decryptCalls, "the production base preflight remains closed")

	readsBeforeReplay, decryptsBeforeReplay, tokenCallsBeforeReplay, closesBeforeReplay := source.repository.calls, source.keys.decryptCalls, source.tokens.calls, closeCount.Load()
	source.tokens.revoked = true
	_, err = source.admin.Exec(t.Context(), "DROP TABLE public.accounts")
	require.NoError(t, err)
	replayed, err := coordinator.BuildPlan(permit.Context(), buildRequest)
	require.NoError(t, err)
	require.Equal(t, result, replayed)
	require.Equal(t, readsBeforeReplay, source.repository.calls)
	require.Equal(t, decryptsBeforeReplay, source.keys.decryptCalls)
	require.Equal(t, tokenCallsBeforeReplay, source.tokens.calls)
	require.Equal(t, closesBeforeReplay, closeCount.Load())
	replayedTarget, err := fixture.Graph.DeploymentRepository.Target(t.Context(), targetID)
	require.NoError(t, err)
	require.Equal(t, target, replayedTarget)
}

func seedNativeLocalBuildAuthorization(t *testing.T, fixture *PostgresJourneyFixture, targetID string, projectID projectgraph.ResourceID, environment, principalID string) {
	t.Helper()
	_, err := fixture.RuntimePool.Exec(t.Context(), `INSERT INTO access.principal (id, principal_type, status) VALUES ($1::uuid, 'user', 'active') ON CONFLICT (id) DO NOTHING`, principalID)
	require.NoError(t, err)
	scope := access.AuthorizationPolicyScope{TargetID: targetID, ProjectID: projectID.String(), Environment: environment}
	repository, err := accesspostgres.NewAuthorizationPolicyRepository(fixture.RuntimePool, scope)
	require.NoError(t, err)
	binding := access.RoleBinding{
		ID: "binding-local-native-build-viewer", Name: "Local native build viewer",
		Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID},
		Role:    access.ProjectRoleViewer, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleViewer),
	}
	_, err = repository.UpsertAuthorizationRoleBinding(t.Context(), access.AuthorizationRoleBindingInput{
		Scope: scope, Binding: binding, ExpectedRevision: 0, IdempotencyKey: "local-native-build-policy",
	})
	require.NoError(t, err)
}

func writeNativeLocalAdmissionProject(t *testing.T, connectionID projectgraph.ResourceID) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"connections/warehouse.yaml": fmt.Sprintf(`apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: %s, name: warehouse}
spec: {type: postgres}
`, connectionID),
		"sources/accounts_source.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:accounts_source, name: accounts_source}
spec:
  connection: warehouse
  location: {type: relation, schema: public, name: accounts}
`,
		"models/accounts.yaml": `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:accounts, name: accounts}
spec:
  definition:
    type: sql
    sql: SELECT account_id, account_name FROM source.accounts_source
  entities:
    - {name: account, type: primary, fields: [account_id]}
  grain: {entity: account}
  checks:
    - {id: accounts_row_count, type: row_count, minimum: 1, maximum: 1, severity: error}
  fields:
    - {name: account_id, datatype: Integer}
    - {name: account_name, datatype: String}
`,
		"semantic-models/refresh.yaml": `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic-model:refresh, name: accounts_semantic}
spec:
  datasets:
    - {name: accounts, model: accounts}
`,
		"pipelines/refresh.yaml": `apiVersion: leapview.dev/v1
kind: Pipeline
metadata: {id: pipeline:refresh, name: refresh}
spec:
  selection: {semanticModel: accounts_semantic}
`,
	}
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}
	return root
}
