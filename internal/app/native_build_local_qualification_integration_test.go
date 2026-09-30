//go:build integration && duckdb_arrow

package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/catalogartifact"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/gates"
	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	appdeploymentpostgres "github.com/flidai/leapview/internal/app/deploymentpostgres"
	"github.com/flidai/leapview/internal/deployment"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/internal/workload"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// This direct physical-build qualification manually acquires and closes the
// captured candidate leases around BuildNativePhysical. It uses a generated,
// value-only running-attempt identity and omits the observation writer, so it
// persists no source-observation captures or attempt settlement and makes no
// generation-admission, publication, or activation claim. It qualifies the
// successful materialization and read-only snapshot-gate boundary only.
func TestNativeBuildLocalCredentialQualifiesExactCommittedSnapshot(t *testing.T) {
	source := newLocalMaterializationSourceFixture(t)
	job := source.job
	fixture := NewPostgresJourneyFixture(t, PostgresJourneyFixtureOptions{
		TargetID: job.Authority.Target.InstanceID, ProjectID: job.Identity.ProjectID, SkipRouteAssembly: true,
	})
	catalog := newLocalNativeBuildCatalogFixture(t, source.admission)
	bootstrapAdmin, err := pgxpool.New(t.Context(), fixture.Database.AdminURL())
	require.NoError(t, err)
	t.Cleanup(bootstrapAdmin.Close)
	catalog.bootstrapRuntimeCompatibility(t, bootstrapAdmin)
	bootstrapAdmin.Close()

	require.ErrorIs(t, source.factory.checkBaseCredentials(t.Context(), job), errRefreshLocalCredentialUnsupported)
	require.Zero(t, source.keys.decryptCalls, "production local-pin preflight must remain denied")

	newUUID := func() uuid.UUID {
		value, err := uuid.NewV7()
		require.NoError(t, err)
		return value
	}
	candidateID, generationID, attemptID, planID, deliveryID := newUUID().String(), newUUID().String(), newUUID().String(), newUUID().String(), newUUID().String()
	identity := job.Identity
	identity.GenerationID = generationID

	connections, err := newRefreshCandidateConnections(t.Context(), candidateConnectionLeaser{module: source.module}, source.factory, job)
	require.NoError(t, err)
	bindingRequest := deployment.CandidateConnectionRequest{
		CandidateID: candidateID, Actor: job.PrincipalID, TargetID: job.Authority.Target.InstanceID,
		Identity: identity, Requirements: []deployment.CandidateConnectionRequirement{{ConnectionID: source.binding.ConnectionID, ConnectorKind: "postgres"}},
	}
	plannedEvidence, err := connections.Resolve(t.Context(), bindingRequest)
	require.NoError(t, err)
	require.Len(t, plannedEvidence, 1)
	require.Equal(t, source.reference.VersionID, plannedEvidence[0].CredentialVersionID)
	require.Equal(t, source.binding.Evidence().EndpointConfigHash, plannedEvidence[0].EndpointConfigHash)
	require.Equal(t, source.binding.ID.String(), plannedEvidence[0].BindingID)
	bindingDigest, err := deployment.BindingFingerprint(plannedEvidence)
	require.NoError(t, err)
	require.Zero(t, source.repository.calls, "planning must not read the committed credential payload")
	bundle := nativeBuildLocalSourceBundle(t)

	model := localMaterializationModel()
	model.Sources["accounts_source"] = semanticmodel.Source{
		Connection: "warehouse", Object: "public.accounts", SchemaMode: "inferred",
	}
	minimumRows, maximumRows := int64(1), int64(1)
	accounts := model.Tables["accounts"]
	accounts.Checks = append(accounts.Checks, semanticmodel.ModelCheck{
		ID: "accounts-row-count", Type: "row_count", Minimum: &minimumRows, Maximum: &maximumRows, Severity: "error",
	})
	model.Tables["accounts"] = accounts

	namespace, err := deployment.DeriveRelationNamespace(deployment.RelationNamespaceInput{CandidateID: candidateID, AttemptID: attemptID, FencingEpoch: 1})
	require.NoError(t, err)
	requestDigest, planDigest := activeResultIdentityDigest('a'), activeResultIdentityDigest('b')
	marker := catalogartifact.CommitMarker{
		SchemaVersion: catalogartifact.CommitMarkerSchemaVersion, DeliveryID: deliveryID,
		GenerationID: generationID, AttemptID: attemptID, LeaseEpoch: 1, FencingToken: "1",
		RequestDigest: requestDigest, PlanDigest: planDigest, Project: identity.ProjectID.String(),
		Environment: identity.Environment, PhysicalPoolID: catalog.contract.PhysicalPoolID,
	}
	attempt := deploymentpostgres.DeliveryBuildAttempt{
		AttemptID: attemptID, PlanID: planID, CandidateID: candidateID, OwnerID: job.PrincipalID,
		PhysicalPoolID: catalog.contract.PhysicalPoolID, CatalogID: catalog.contract.Catalog.CatalogID,
		FencingEpoch: 1, State: deploymentpostgres.AttemptRunning, Namespace: namespace,
		RequestDigest: requestDigest, PlanDigest: planDigest, LeaseExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	dataPath, err := catalog.contract.PoolContract.Pool.DataPath()
	require.NoError(t, err)
	physicalInput := appdeploymentpostgres.NativePhysicalBuildInput{
		Attempt: attempt, Marker: marker, CatalogID: catalog.contract.Catalog.CatalogID, ObjectRoot: dataPath,
		Request: analyticsmaterialization.Request{
			Models: map[string]*semanticmodel.Model{"semantic:refresh": model}, ModelTables: model.Tables,
			Identity: identity, CandidateID: candidateID, RelationNamespace: namespace,
			Environment: servingstate.Environment(identity.Environment), TargetType: "refresh_pipeline", TargetID: job.PipelineID,
			SemanticDigest: activeResultIdentityDigest('c'), ArtifactDigest: activeResultIdentityDigest('d'),
			Tables: []string{"accounts"},
		},
	}
	physicalFactory := appdeploymentpostgres.DuckLakePhysicalBuildEnvironmentFactory{
		Config: catalog.config, CatalogID: catalog.contract.Catalog.CatalogID,
		MaterializerFactory: func(environment *ducklake.Environment) (analyticsmaterialization.Executor, error) {
			return source.module.ProjectMaterializerForEnvironment(environment)
		},
	}
	controller, err := workload.New(workload.DefaultConfig())
	require.NoError(t, err)
	t.Cleanup(controller.Close)
	permit, err := controller.Acquire(t.Context(), workload.Request{
		Class: workload.Refresh, PrincipalID: job.PrincipalID, Operation: "native-build-local-qualification", EstimatedMemoryBytes: 1,
	})
	require.NoError(t, err)
	t.Cleanup(permit.Release)

	var closes, decryptUse, sourceUse atomic.Int32
	observedConnections := &nativeBuildObservedLocalConnections{inner: connections, closes: &closes}
	var retainedAuth semanticmodel.ConnectionAuth
	originalUseLocal := connections.useLocal
	connections.useLocal = func(ctx context.Context, binding connectionbinding.TargetBinding, snapshot connectionbinding.CredentialSnapshot, logical semanticmodel.Connection, consume func(semanticmodel.Connection) error) error {
		decryptUse.Add(1)
		return originalUseLocal(ctx, binding, snapshot, logical, func(got semanticmodel.Connection) error {
			sourceUse.Add(1)
			retainedAuth = got.Auth
			return consume(got)
		})
	}

	leases, err := observedConnections.Acquire(t.Context(), bindingRequest)
	require.NoError(t, err)
	leaseClosed := false
	defer func() {
		if !leaseClosed {
			_ = leases.Close()
		}
	}()
	require.Equal(t, plannedEvidence, leases.Evidence())
	physical, buildErr := appdeploymentpostgres.BuildNativePhysical(permit.Context(), physicalInput, physicalFactory)
	closeErr := leases.Close()
	leaseClosed = true
	require.NoError(t, buildErr)
	require.NoError(t, closeErr)

	require.EqualValues(t, 1, closes.Load(), "candidate registration must close before qualification")
	require.EqualValues(t, 1, decryptUse.Load())
	require.EqualValues(t, 1, sourceUse.Load())
	require.Equal(t, 1, source.repository.calls)
	require.Equal(t, source.reference.VersionID, source.repository.versionID)
	require.EqualValues(t, 1, source.keys.decryptCalls)
	require.Empty(t, retainedAuth)
	require.Equal(t, make([]byte, len(source.keys.lastPlaintext)), source.keys.lastPlaintext)
	require.Equal(t, attemptID, physical.AttemptID)
	require.Equal(t, marker, physical.Marker)
	require.Equal(t, catalog.contract.Catalog.CatalogID, physical.CatalogID)
	require.Positive(t, physical.SnapshotID)
	require.Equal(t, physical.SnapshotID, physical.Seal.SnapshotID)
	require.Equal(t, namespace, physical.Closure.RelationNamespace)
	require.Equal(t, physical.SnapshotID, physical.Closure.SnapshotID)
	require.Equal(t, physical.Closure.CatalogID, physical.CatalogID)
	require.Contains(t, physical.Closure.Relations, ducklake.BaseTable{Schema: namespace, Table: "accounts"})
	require.NotEmpty(t, physical.Closure.Objects, "materialized rows must have a captured object closure")
	require.NoError(t, ducklake.VerifyNativeSnapshotClosureEvidence(physical.Closure))
	require.JSONEq(t, string(physical.CanonicalMarkerJSON), physical.Seal.CommitMarker)
	require.Len(t, physical.SourceObservations, 1)
	require.Equal(t, "accounts_source", physical.SourceObservations[0].ID)
	require.Len(t, physical.SourceObservations[0].Schema, 2)
	require.Eventually(t, func() bool {
		var count int
		return source.admin.QueryRow(t.Context(), "SELECT count(*) FROM pg_stat_activity WHERE usename=$1", source.role.Name).Scan(&count) == nil && count == 0
	}, 3*time.Second, 10*time.Millisecond)
	require.ErrorIs(t, source.factory.checkBaseCredentials(t.Context(), job), errRefreshLocalCredentialUnsupported)
	require.EqualValues(t, 1, source.keys.decryptCalls, "production local-pin preflight must remain closed")

	markerResolver, err := (appdeploymentpostgres.DuckLakePhysicalMarkerResolverFactory{Config: catalog.config}).OpenReadOnly(t.Context())
	require.NoError(t, err)
	resolution, resolveErr := markerResolver.ResolveCommittedMarker(t.Context(), marker)
	resolverCloseErr := markerResolver.Close()
	require.NoError(t, resolveErr)
	require.NoError(t, resolverCloseErr)
	require.True(t, resolution.Found)
	require.Equal(t, physical.SnapshotID, resolution.SnapshotID)

	// The read-only qualification must depend only on the committed DuckLake
	// snapshot and captured source evidence. Revoke its source authority and
	// remove the upstream relation before attaching the exact snapshot.
	source.tokens.revoked = true
	_, err = source.admin.Exec(t.Context(), "DROP TABLE public.accounts")
	require.NoError(t, err)

	manifest := bundle.Manifest()
	sourceObservation := physical.SourceObservations[0]
	sourceForGate := manifest.Sources["source:accounts_source"]
	sourceForGate.SchemaMode = "inferred"
	qualificationRequest := appdeploymentpostgres.NativeQualificationRequest{
		Build: physical, CandidateID: candidateID, SourceDigest: bundle.Digest(),
		BindingGeneration: bindingDigest, RuntimeVersion: catalog.contract.Compatibility.DuckDBRuntime,
		Compatibility: catalog.contract.Compatibility,
		Sources: []gates.SourceInput{{
			ID: "source:accounts_source", Source: sourceForGate, Observed: sourceObservation.Schema,
			Revision: sourceObservation.Revision, RevisionObserved: sourceObservation.RevisionObserved,
			FreshnessObserved: sourceObservation.FreshnessObserved, FreshnessEmpty: sourceObservation.FreshnessEmpty,
			SchemaFailure: sourceObservation.SchemaFailure, FreshnessFailure: sourceObservation.FreshnessFailure,
			ObservationQueries: sourceObservation.ObservationQueries, ObservationRows: sourceObservation.ObservationRows,
			ObservationMillis: sourceObservation.ObservationMillis,
		}},
		Models: []gates.ModelInput{{ID: "model:accounts", Model: model.Tables["accounts"]}},
		Bounds: gates.Bounds{MaxRows: 10000, MaxQueries: 128, MaxMillis: 5000}, Now: time.Now().UTC(),
	}
	qualificationFactory := appdeploymentpostgres.DuckLakeNativeQualificationEnvironmentFactory{
		Config: catalog.config, CatalogID: catalog.contract.Catalog.CatalogID,
		CompatibilityAuthority: fixture.Graph.DuckLakeControlLedger,
	}
	qualified, err := appdeploymentpostgres.QualifyNativeSnapshot(t.Context(), qualificationRequest, qualificationFactory)
	require.NoError(t, err, "catalog version=%q, extension version=%q, admitted format=%q, admitted extension=%q",
		physical.Seal.CatalogVersion, physical.Seal.ExtensionVersion,
		catalog.contract.Compatibility.CatalogFormat, catalog.contract.Compatibility.DuckLakeExtension)
	require.Equal(t, candidateID, qualified.CandidateID)
	require.Equal(t, attemptID, qualified.AttemptID)
	require.Equal(t, physical.SnapshotID, qualified.SnapshotID)
	require.Equal(t, physical.Closure.ClosureDigest, qualified.ClosureDigest)
	require.Equal(t, release.GateSuccess, qualified.Gates.Outcome)
	require.Len(t, qualified.Gates.Sources, 1)
	require.Equal(t, release.GateSuccess, qualified.Gates.Sources[0].SchemaOutcome)
	rowCountChecks := 0
	for _, check := range qualified.Gates.Checks {
		require.Equal(t, release.GateSuccess, check.Outcome)
		if check.Kind == "row_count" && check.ResourceID == "model:accounts" {
			rowCountChecks++
			require.EqualValues(t, 1, check.ObservedRows)
		}
	}
	require.Equal(t, 1, rowCountChecks, "the accounts row-count assertion must execute against the exact candidate namespace")
	require.EqualValues(t, 1, source.keys.decryptCalls, "read-only qualification must not reread credentials")
	require.Equal(t, 1, source.repository.calls)
	require.EqualValues(t, 1, decryptUse.Load())
	require.EqualValues(t, 1, sourceUse.Load())

	tamperedSnapshot := qualificationRequest
	tamperedSnapshot.Build.SnapshotID++
	_, err = appdeploymentpostgres.QualifyNativeSnapshot(t.Context(), tamperedSnapshot, qualificationFactory)
	require.ErrorIs(t, err, appdeploymentpostgres.ErrNativeQualificationInvalid, "qualification must reject evidence that changes the exact snapshot ID")
	tamperedCompatibility := qualificationRequest
	tamperedCompatibility.Compatibility.CompatibilityDigest = activeResultIdentityDigest('e')
	_, err = appdeploymentpostgres.QualifyNativeSnapshot(t.Context(), tamperedCompatibility, qualificationFactory)
	require.ErrorIs(t, err, appdeploymentpostgres.ErrNativeQualificationFailed, "qualification must reject a compatibility identity different from the persisted catalog authority")
	require.EqualValues(t, 1, source.keys.decryptCalls)
	require.Equal(t, 1, source.repository.calls)
	require.Eventually(t, func() bool {
		var count int
		return source.admin.QueryRow(t.Context(), "SELECT count(*) FROM pg_stat_activity WHERE usename=$1", source.role.Name).Scan(&count) == nil && count == 0
	}, 3*time.Second, 10*time.Millisecond)
}
