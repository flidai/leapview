//go:build integration && duckdb_arrow

package app

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	analyticsduckdb "github.com/flidai/leapview/internal/analytics/duckdb"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	"github.com/flidai/leapview/internal/analytics/sourcework"
	"github.com/flidai/leapview/internal/app/testing/extensionfixture"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	"github.com/flidai/leapview/internal/extension"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/internal/workload"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type localMaterializationSourceFixture struct {
	factory    refreshRuntimeCredentialReaderFactory
	job        refreshrun.JobRecord
	resource   credentialmodule.RuntimeResource
	reference  credentialmodule.RuntimeCredentialReference
	repository *foregroundRuntimeHookRepository
	keys       *runtimeReaderKeyring
	tokens     *refreshRuntimeTokenEvidence
	binding    connectionbinding.TargetBinding
	module     *analyticsmodule.Module
	admin      *pgx.Conn
	role       postgrestest.Role
	admission  extension.Admission
}

func newLocalMaterializationSourceFixture(t *testing.T) localMaterializationSourceFixture {
	t.Helper()
	harness := postgrestest.StartTLS(t)
	role := harness.EnsureRole(t, postgrestest.Role{Name: "local_materialization_source", Password: "queued-refresh-secret", Login: true})
	database := harness.NewDatabase(t, "local_materialization")
	harness.GrantDatabase(t, database.Name, role, "CONNECT")
	admin, err := pgx.Connect(t.Context(), database.AdminURL())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, admin.Close(context.Background())) })
	_, err = admin.Exec(t.Context(), "CREATE TABLE public.accounts (account_id BIGINT PRIMARY KEY, account_name TEXT NOT NULL); INSERT INTO public.accounts VALUES (7, 'Snapshot account'); GRANT SELECT ON public.accounts TO local_materialization_source")
	require.NoError(t, err)
	endpoint, err := pgx.ParseConfig(database.PrivateURL(role))
	require.NoError(t, err)

	factory, job, resource, reference, repository, keys, tokens := refreshCredentialFixture(t)
	lookup := factory.authority.bindings.(*testCredentialBindingLookup)
	binding := lookup.binding
	binding.Endpoint.Host, binding.Endpoint.Port = endpoint.Host, int(endpoint.Port)
	binding.Endpoint.Database, binding.Endpoint.SourceIdentity = endpoint.Database, role.Name
	binding.Endpoint.TLSMode = "require"
	require.NoError(t, binding.Validate())
	lookup.binding = binding
	// Retarget all immutable metadata together, before the candidate is bound.
	provenance := factory.authority.evidence.releases.(sourceSchemaProvenanceStub).provenance
	plan := provenance.Plan
	plan.Bindings[0].EndpointConfigHash = binding.Evidence().EndpointConfigHash
	gate := *plan.GateEvidence
	gate.BindingGeneration = release.BindingFingerprint(plan.Bindings)
	canonicalGate, err := gate.Canonical()
	require.NoError(t, err)
	plan.GateEvidence = &canonicalGate
	provenance, err = release.NewProvenance(release.ProvenanceInput{Artifact: provenance.Artifact, Candidate: provenance.Candidate, SourceRevision: provenance.SourceRevision, Plan: plan})
	require.NoError(t, err)
	factory.authority.evidence.releases = sourceSchemaProvenanceStub{provenance: provenance}
	factory.authority.evidence.commitments.(*committedCredentialGenerationStub).evidence.BindingFingerprint = release.BindingFingerprint(plan.Bindings)
	reference.Scope.Destination = binding.Evidence().EndpointConfigHash
	repository.versions[reference.VersionID] = runtimeReaderStoredVersion(runtimeReaderEncryptionBinding(reference.Scope, reference.VersionID), `{"password":"queued-refresh-secret"}`)

	admission := extensionfixture.New(t, "ducklake", "postgres").Admission
	module, err := analyticsmodule.Build(t.Context(), analyticsmodule.Config{
		ConnectionBindings: localMaterializationCatalog{binding: binding},
		CredentialTargetID: resource.TargetID, CredentialEnvironment: job.Identity.Environment,
		DisableProcessEnvironment: true, ExtensionAdmission: admission,
		RuntimeCacheEntries: 4, RuntimeCacheBytes: 1 << 20, NodeCacheEntries: 8, NodeCacheBytes: 2 << 20,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, module.Close()) })
	return localMaterializationSourceFixture{
		factory: factory, job: job, resource: resource, reference: reference,
		repository: repository, keys: keys, tokens: tokens, binding: binding,
		module: module, admin: admin, role: role, admission: admission,
	}
}

// This is a consumption/snapshot proof, not credential activation: the native
// refresh preflight remains closed. PostgreSQL serves real source rows, while
// the existing credential fixture supplies deterministic committed metadata.
func TestRefreshCandidateLocalMaterializationServesSnapshotWithoutCredentials(t *testing.T) {
	fixture := newLocalMaterializationSourceFixture(t)
	factory, job, resource, reference := fixture.factory, fixture.job, fixture.resource, fixture.reference
	repository, keys, tokens := fixture.repository, fixture.keys, fixture.tokens
	binding, module, admin, role, admission := fixture.binding, fixture.module, fixture.admin, fixture.role, fixture.admission
	connections, err := newRefreshCandidateConnections(t.Context(), candidateConnectionLeaser{module: module}, factory, job)
	require.NoError(t, err)
	identity := job.Identity
	identity.GenerationID = uuid.NewString()
	request := deploymentmodule.CandidateConnectionRequest{
		CandidateID: uuid.NewString(), Actor: job.PrincipalID, TargetID: resource.TargetID, Identity: identity,
		Requirements: []deploymentmodule.CandidateConnectionRequirement{{ConnectionID: binding.ConnectionID, ConnectorKind: "postgres"}},
	}
	evidence, err := connections.Resolve(t.Context(), request)
	require.NoError(t, err)
	registration, err := connections.Acquire(t.Context(), request)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, registration.Close()) })
	require.Equal(t, evidence, registration.Evidence())
	require.Len(t, evidence, 1)
	require.Equal(t, reference.VersionID, evidence[0].CredentialVersionID)
	require.Zero(t, repository.calls)
	require.Zero(t, keys.decryptCalls, "planning and registration must not decrypt")

	root := filepath.Join(t.TempDir(), "ducklake")
	environment, err := analyticsducklake.Open(t.Context(), analyticsducklake.Config{RootDir: root, MaxConnections: 2, ExtensionAdmission: admission})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, environment.Close()) })
	controller, err := workload.New(workload.DefaultConfig())
	require.NoError(t, err)
	t.Cleanup(controller.Close)
	permit, err := controller.Acquire(t.Context(), workload.Request{Class: workload.Refresh, PrincipalID: job.PrincipalID, Operation: "local-materialization-test", EstimatedMemoryBytes: 1})
	require.NoError(t, err)
	t.Cleanup(permit.Release)
	executor, err := module.ProjectMaterializerForEnvironment(environment)
	require.NoError(t, err)
	writerExecutor, ok := executor.(analyticsmaterialization.ObservationWriterExecutor)
	require.True(t, ok)
	model := localMaterializationModel()
	require.NoError(t, model.ValidateAuthored())
	materializeRequest := analyticsmaterialization.Request{
		Models: map[string]*semanticmodel.Model{"semantic:refresh": model}, ModelTables: model.Tables,
		Identity: identity, CandidateID: request.CandidateID, RelationNamespace: "candidate_local_materialization",
		Environment: servingstate.Environment(identity.Environment), TargetType: "refresh_pipeline", TargetID: job.PipelineID,
		SemanticDigest: activeResultIdentityDigest('a'), ArtifactDigest: activeResultIdentityDigest('b'), Tables: []string{"accounts"},
	}

	readStarted, allowRead := make(chan struct{}), make(chan struct{})
	var releaseRead sync.Once
	repository.afterRead = func() { close(readStarted); <-allowRead }
	var retainedAuth semanticmodel.ConnectionAuth
	var localUseErr, sourceErr error
	var localCalls, sourceCalls int
	useLocal := connections.useLocal
	connections.useLocal = func(ctx context.Context, got connectionbinding.TargetBinding, snapshot connectionbinding.CredentialSnapshot, logical semanticmodel.Connection, consume func(semanticmodel.Connection) error) error {
		localCalls++
		localUseErr = useLocal(ctx, got, snapshot, logical, func(connection semanticmodel.Connection) error {
			sourceCalls++
			retainedAuth = connection.Auth
			sourceErr = consume(connection)
			return sourceErr
		})
		return localUseErr
	}
	type outcome struct {
		snapshot     int64
		observations []analyticsmaterialize.SourceObservation
		err          error
	}
	done := make(chan outcome, 1)
	finished := make(chan struct{})
	materializeCtx, cancelMaterialize := context.WithTimeout(permit.Context(), 30*time.Second)
	var writerCalls int
	var writtenObservations []analyticsmaterialize.SourceObservation
	go func() {
		defer close(finished)
		snapshot, observations, err := writerExecutor.MaterializeWithObservationWriter(materializeCtx, materializeRequest, func(_ context.Context, observed []analyticsmaterialize.SourceObservation) error {
			writerCalls++
			writtenObservations = observed
			return nil
		})
		done <- outcome{snapshot, observations, err}
	}()
	// An assertion failure must unblock and join the worker before its resources
	// are closed by the earlier cleanup callbacks.
	t.Cleanup(func() {
		cancelMaterialize()
		releaseRead.Do(func() { close(allowRead) })
		<-finished
	})
	select {
	case <-readStarted:
	case result := <-done:
		t.Fatalf("materialization returned before credential read: %v", result.err)
	case <-time.After(15 * time.Second):
		t.Fatal("materialization did not reach the credential reader")
	}
	pause, err := module.PauseSourceWork()
	require.NoError(t, err)
	waitCtx, cancelWait := context.WithTimeout(t.Context(), 30*time.Millisecond)
	require.ErrorIs(t, pause.WaitDrained(waitCtx), context.DeadlineExceeded, "source work must remain admitted during credential use")
	cancelWait()
	require.Zero(t, keys.decryptCalls)
	releaseRead.Do(func() { close(allowRead) })
	var result outcome
	select {
	case result = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("materialization did not finish")
	}
	require.NoError(t, result.err, "local calls=%d, source calls=%d, local error=%v, source error=%v", localCalls, sourceCalls, localUseErr, sourceErr)
	require.Positive(t, result.snapshot)
	require.Equal(t, 1, writerCalls)
	require.Len(t, result.observations, 1)
	require.Equal(t, "accounts_source", result.observations[0].ID)
	require.Len(t, result.observations[0].Schema, 2)
	require.Equal(t, result.observations, writtenObservations)
	require.Equal(t, 1, repository.calls)
	require.Equal(t, reference.VersionID, repository.versionID)
	require.Equal(t, 1, keys.decryptCalls)
	require.Empty(t, retainedAuth)
	require.Equal(t, make([]byte, len(keys.lastPlaintext)), keys.lastPlaintext)
	drainCtx, cancelDrain := context.WithTimeout(t.Context(), time.Second)
	defer cancelDrain()
	require.NoError(t, pause.WaitDrained(drainCtx))
	// A real source backend must not survive the synchronous connection scope.
	require.Eventually(t, func() bool {
		var count int
		err := admin.QueryRow(t.Context(), "SELECT count(*) FROM pg_stat_activity WHERE usename = $1", role.Name).Scan(&count)
		return err == nil && count == 0
	}, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, registration.Close())
	require.NoError(t, pause.Resume())
	repository.afterRead = nil
	missingSnapshot, err := executor.Materialize(permit.Context(), materializeRequest)
	require.Error(t, err, "closed candidate registration must not fall back to provider credentials")
	require.NotContains(t, err.Error(), role.Password)
	require.Zero(t, missingSnapshot)
	require.Equal(t, 1, keys.decryptCalls)
	require.ErrorIs(t, factory.checkBaseCredentials(t.Context(), job), errRefreshLocalCredentialUnsupported)
	require.NoError(t, environment.Close())
	// Revoke the queued credential authority and remove the upstream table. A
	// reopened serving snapshot must need neither of them, nor source admission.
	tokens.revoked = true
	_, err = admin.Exec(t.Context(), "DROP TABLE public.accounts")
	require.NoError(t, err)
	reopened, err := analyticsducklake.Open(t.Context(), analyticsducklake.Config{RootDir: root, MaxConnections: 2, ExtensionAdmission: admission})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	var servingGate sourcework.Gate
	servingPause, err := servingGate.Pause()
	require.NoError(t, err)
	resolver := &localMaterializationDenyResolver{}
	partition, err := resultidentity.NewPartition(resultidentity.PartitionInput{Kind: resultidentity.PartitionProduction, TargetID: resource.TargetID, ProjectID: identity.ProjectID, Environment: identity.Environment})
	require.NoError(t, err)
	serveCtx, cancelServe := context.WithTimeout(permit.Context(), 15*time.Second)
	defer cancelServe()
	serving, err := analyticsduckdb.OpenProjectMaterializeRuntime(serveCtx, analyticsduckdb.ProjectRuntimeConfig{
		Models: map[string]*semanticmodel.Model{"semantic:refresh": localMaterializationModel()}, Database: reopened,
		ProjectID: identity.ProjectID, ServingStateID: identity.GenerationID, Environment: identity.Environment,
		RelationNamespace: materializeRequest.RelationNamespace, SnapshotID: result.snapshot, SkipInitialRefresh: true,
		ConnectionResolver: resolver, SourceWork: &servingGate, ExtensionAdmission: admission, ResultPartition: partition,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, serving.Close()) })
	require.NoError(t, serving.VerifySemantic(serveCtx, "semantic:refresh"))
	query := dataquery.ModelRows("semantic:refresh", "accounts", []string{"account_id", "account_name"}, nil, 0, 10, false)
	query.ProjectID = identity.ProjectID
	rows, err := serving.ExecuteDataQuery(serveCtx, query)
	require.NoError(t, err)
	require.Len(t, rows.Rows, 1)
	require.EqualValues(t, 7, rows.Rows[0]["account_id"])
	require.Equal(t, "Snapshot account", rows.Rows[0]["account_name"])
	require.Zero(t, resolver.calls.Load())
	require.Equal(t, 1, repository.calls)
	require.Equal(t, 1, keys.decryptCalls)
	require.NoError(t, servingPause.WaitDrained(serveCtx))
}

func localMaterializationModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name: "accounts", DefaultConnection: "warehouse",
		Connections: map[string]semanticmodel.Connection{"warehouse": {Kind: "postgres"}},
		Sources:     map[string]semanticmodel.Source{"accounts_source": {Connection: "warehouse", Object: "public.accounts"}},
		Tables: map[string]semanticmodel.Table{"accounts": {
			ModelName: "accounts", SourceDependencies: []string{"accounts_source"},
			Execution:           semanticmodel.ExecutionDefinition{SQL: "SELECT account_id, account_name FROM source.accounts_source"},
			SQLAnalysisEvidence: &semanticmodel.SQLAnalysisEvidence{Validated: true, SourceRefs: []string{"accounts_source"}},
			Entities:            map[string]semanticmodel.EntityDefinition{"account": {Type: "primary", Fields: []string{"account_id"}}}, GrainEntity: "account",
			Dimensions: map[string]semanticmodel.MetricDimension{"account_id": {Datatype: semanticmodel.DataTypeInteger}, "account_name": {Datatype: semanticmodel.DataTypeString}},
		}},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"accounts": {Model: "accounts"}},
	}
}

type localMaterializationCatalog struct {
	connectionbinding.BindingCatalog
	binding connectionbinding.TargetBinding
}

func (catalog localMaterializationCatalog) Binding(_ context.Context, scope connectionbinding.BindingScope, target connectionbinding.TargetID, id projectgraph.ResourceID) (connectionbinding.TargetBinding, error) {
	if scope != catalog.binding.Scope || target != catalog.binding.TargetID || id != catalog.binding.ConnectionID {
		return connectionbinding.TargetBinding{}, connectionbinding.ErrBindingNotFound
	}
	return catalog.binding, nil
}

type localMaterializationDenyResolver struct{ calls atomic.Int32 }

func (resolver *localMaterializationDenyResolver) WithConnection(context.Context, string, semanticmodel.Connection, func(semanticmodel.Connection) error) error {
	resolver.calls.Add(1)
	return errors.New("source resolution is unavailable after materialization")
}
