package refreshpostgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	"github.com/flidai/leapview/internal/analytics/sourcework"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/google/uuid"
)

func TestNativeExecutorRevalidatesAuthorityAfterPoolSourceWorkWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		job := nativeExecutorJob()
		basePlan := nativeExecutorBasePlan(t)
		reader := nativeExecutorReaderFixture(basePlan)
		reader.generations[nativeExecutorResult] = deploymentnative.DeliveryGeneration{
			GenerationID: nativeExecutorResult, TargetID: nativeExecutorTarget, PlanID: nativeExecutorPlan,
			PlanDigest: nativeExecutorPlanDigest, CandidateID: nativeExecutorCandidate,
			SnapshotSealID: nativeExecutorSeal, ServingArtifactDigest: nativeExecutorArtifact,
		}
		reader.attempt = deploymentnative.DeliveryBuildAttempt{
			AttemptID: nativeExecutorBuild, PlanID: nativeExecutorPlan, CandidateID: nativeExecutorCandidate,
			State: deploymentnative.AttemptCommitted, SnapshotID: 42,
		}
		reader.seal = deploymentnative.SnapshotSeal{
			SealID: nativeExecutorSeal, AttemptID: nativeExecutorBuild, CandidateID: nativeExecutorCandidate,
			PlanDigest: nativeExecutorPlanDigest, DuckLakeSnapshotID: 42,
		}
		reader.candidate = deploymentnative.DeliveryCandidate{
			CandidateID: nativeExecutorCandidate, TargetID: nativeExecutorTarget, PlanID: nativeExecutorPlan,
			AttemptID: nativeExecutorBuild, SnapshotSealID: nativeExecutorSeal, Status: "qualified",
		}
		mutations := &nativeExecutorMutations{
			plan: deploymentmodule.NativeDeliveryPlan{
				ID: uuid.MustParse(nativeExecutorPlan), ProjectID: job.Identity.ProjectID,
				TargetID: nativeExecutorTarget, Environment: nativeExecutorEnvironment,
				Operation: string(deployment.DeliveryOperationRestatement), SourceDigest: nativeExecutorSourceDigest,
				SourceAttestationDigest: nativeExecutorAttestation, BaseGenerationID: uuid.MustParse(nativeExecutorBase),
				BaseTargetRevision: job.TargetRevision, PlanDigest: nativeExecutorPlanDigest, Status: "planned",
			},
			build: deploymentmodule.NativeDeliveryBuild{
				ID: uuid.MustParse(nativeExecutorBuild), PlanID: uuid.MustParse(nativeExecutorPlan),
				PlanDigest: nativeExecutorPlanDigest, SourceDigest: nativeExecutorSourceDigest,
				BaseGenerationID: uuid.MustParse(nativeExecutorBase), ServingArtifactDigest: nativeExecutorArtifact,
				WriterLeaseID: uuid.MustParse(nativeExecutorLease), ServingStateID: uuid.MustParse(nativeExecutorResult),
				SealID: uuid.MustParse(nativeExecutorSeal), CandidateID: uuid.MustParse(nativeExecutorCandidate), Status: "sealed",
			},
		}

		now := time.Now().UTC()
		binding, err := connectionbinding.NewTargetBinding(connectionbinding.TargetBindingInput{
			ID: "binding_warehouse", TargetID: connectionbinding.TargetID(nativeExecutorTarget),
			ConnectionID: "connection:warehouse", ConnectorKind: "postgres",
			AuthenticationMode:  connectionbinding.AuthenticationExternalBundle,
			Scope:               connectionbinding.BindingScope{ProjectID: job.Identity.ProjectID, Environment: job.Identity.Environment},
			Endpoint:            connectionbinding.EndpointConfig{Host: "warehouse.internal", Port: 5432, Database: "analytics", TLSMode: "verify-full"},
			CredentialReference: connectionbinding.CredentialReference{ProjectID: "credential-secret", Environment: nativeExecutorEnvironment, SecretPath: "/warehouse", SecretKey: "password"},
			Enabled:             true, Now: now,
		})
		if err != nil {
			t.Fatalf("construct target binding: %v", err)
		}
		catalog := &nativePoolAuthorityBindingCatalog{binding: binding}
		gate := &sourcework.Gate{}
		pause, err := gate.Pause()
		if err != nil {
			t.Fatalf("pause source work: %v", err)
		}
		defer func() { _ = pause.Resume() }()

		var credentialResolves, poolPrepares, bindingSaves, auditEvents atomic.Int64
		poolBuilt := make(chan struct{})
		resolver := nativePoolAuthorityCredentialResolver{calls: &credentialResolves}
		factory := nativePoolAuthorityRuntimePoolFactory{calls: &poolPrepares}
		store := nativePoolAuthorityBindingStore{catalog: catalog}
		audit := nativePoolAuthorityRotationAudit{calls: &auditEvents}
		catalog.saveCounter = &bindingSaves
		directory, err := connectionbinding.NewPoolDirectory(connectionbinding.PoolDirectoryConfig{
			Build: func(current connectionbinding.TargetBinding) (*connectionbinding.PoolManager, error) {
				close(poolBuilt)
				return connectionbinding.NewPoolManager(connectionbinding.PoolManagerConfig{
					Binding: current, Resolver: resolver, Factory: factory, Store: store, Audit: audit,
					Now: time.Now, StaleAfter: time.Hour, SourceWork: gate,
				})
			},
			RefreshTimeout: time.Minute, MaxConcurrent: 1,
		})
		if err != nil {
			t.Fatalf("construct pool directory: %v", err)
		}
		defer func() { _ = directory.Close() }()
		leaser, err := connectionbinding.NewRuntimeBindingLeaser(connectionbinding.RuntimeBindingLeaserConfig{
			Bindings: catalog, Pools: directory,
			Authorize: func(context.Context, string, connectionbinding.TargetBinding) error { return nil },
		})
		if err != nil {
			t.Fatalf("construct runtime binding leaser: %v", err)
		}

		privateRevocation := errors.New("revoked job authority with private credential details")
		var revoked atomic.Bool
		var revalidations atomic.Int64
		checker := jobs.AuthorityRevalidatorFunc(func(_ context.Context, authority jobs.AuthorityEnvelope) error {
			revalidations.Add(1)
			if !reflect.DeepEqual(authority, job.Authority) {
				t.Error("pool acquisition did not receive the exact queued authority")
			}
			if revoked.Load() {
				return privateRevocation
			}
			return nil
		})
		executor, err := NewPostgresNativeRefreshExecutor(nativeExecutorMutationFactoryFor(mutations), reader, nativeExecutorTarget, checker, nativeExecutorAllowBaseCredentials)
		if err != nil {
			t.Fatalf("construct native refresh executor: %v", err)
		}
		mutations.beforeBuild = func(ctx context.Context) error {
			leases, acquireErr := leaser.Acquire(ctx, connectionbinding.RuntimeBindingRequest{
				Actor: "candidate:" + nativeExecutorCandidate, Identity: job.Identity,
				TargetID:     binding.TargetID,
				Requirements: []connectionbinding.Requirement{{ConnectionID: binding.ConnectionID, ConnectorKind: binding.ConnectorKind}},
			})
			if acquireErr == nil {
				leases.Release()
			}
			return acquireErr
		}

		type executionResult struct {
			result refreshrun.CanonicalRefreshResult
			err    error
		}
		done := make(chan executionResult, 1)
		go func() {
			result, executeErr := executor.Execute(t.Context(), job)
			done <- executionResult{result: result, err: executeErr}
		}()
		select {
		case <-poolBuilt:
		case <-time.After(5 * time.Second):
			t.Fatal("native build did not reach the real pool directory")
		}
		// Wait until the executor is quiescent on the paused SourceWork gate,
		// then revoke and reopen admission. This avoids a sleep-based race.
		synctest.Wait()
		if credentialResolves.Load() != 0 || poolPrepares.Load() != 0 {
			t.Fatalf("pool credentials or factory were used while source work was paused: resolver=%d factory=%d", credentialResolves.Load(), poolPrepares.Load())
		}
		revoked.Store(true)
		if err := pause.Resume(); err != nil {
			t.Fatalf("resume source work after revocation: %v", err)
		}
		var outcome executionResult
		select {
		case outcome = <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("native refresh did not stop after source-work authority revocation")
		}
		if !errors.Is(outcome.err, privateRevocation) || strings.Contains(outcome.err.Error(), "private credential details") {
			t.Fatalf("native refresh pool error = %v, want redacted captured-authority denial", outcome.err)
		}
		if revalidations.Load() < 3 {
			t.Fatalf("authority checks = %d, want executor plan/build checks plus pool-boundary check", revalidations.Load())
		}
		if credentialResolves.Load() != 0 || poolPrepares.Load() != 0 || bindingSaves.Load() != 0 || auditEvents.Load() != 0 {
			t.Fatalf("revoked pool acquisition caused side effects: resolver=%d factory=%d saves=%d audits=%d", credentialResolves.Load(), poolPrepares.Load(), bindingSaves.Load(), auditEvents.Load())
		}
		if !mutations.planCompleted || mutations.buildCompleted {
			t.Fatalf("native completion state after pool revocation: plan=%t build=%t", mutations.planCompleted, mutations.buildCompleted)
		}
	})
}

type nativePoolAuthorityBindingCatalog struct {
	mu          sync.Mutex
	binding     connectionbinding.TargetBinding
	saveCounter *atomic.Int64
}

func (catalog *nativePoolAuthorityBindingCatalog) Create(_ context.Context, binding connectionbinding.TargetBinding) error {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	catalog.binding = binding
	return nil
}

func (catalog *nativePoolAuthorityBindingCatalog) Binding(_ context.Context, scope connectionbinding.BindingScope, target connectionbinding.TargetID, connection projectgraph.ResourceID) (connectionbinding.TargetBinding, error) {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	if catalog.binding.Scope != scope || catalog.binding.TargetID != target || catalog.binding.ConnectionID != connection {
		return connectionbinding.TargetBinding{}, connectionbinding.ErrBindingNotFound
	}
	return catalog.binding, nil
}

func (catalog *nativePoolAuthorityBindingCatalog) Save(_ context.Context, binding connectionbinding.TargetBinding, expectedRevision int64) (connectionbinding.TargetBinding, error) {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	if catalog.binding.Revision != expectedRevision {
		return connectionbinding.TargetBinding{}, connectionbinding.ErrIncompatibleBinding
	}
	catalog.binding = binding
	if catalog.saveCounter != nil {
		catalog.saveCounter.Add(1)
	}
	return binding, nil
}

func (catalog *nativePoolAuthorityBindingCatalog) List(_ context.Context, scope connectionbinding.BindingScope, target connectionbinding.TargetID) ([]connectionbinding.TargetBinding, error) {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	if catalog.binding.Scope != scope || catalog.binding.TargetID != target {
		return nil, nil
	}
	return []connectionbinding.TargetBinding{catalog.binding}, nil
}

type nativePoolAuthorityCredentialResolver struct{ calls *atomic.Int64 }

func (resolver nativePoolAuthorityCredentialResolver) Resolve(_ context.Context, _ connectionbinding.CredentialReference) (connectionbinding.CredentialSnapshot, error) {
	resolver.calls.Add(1)
	now := time.Now().UTC()
	return connectionbinding.NewCredentialSnapshot(map[string]string{"password": "not-for-error-output"}, "provider:v1", now, now.Add(time.Hour))
}

type nativePoolAuthorityRuntimePoolFactory struct{ calls *atomic.Int64 }

func (factory nativePoolAuthorityRuntimePoolFactory) Prepare(context.Context, connectionbinding.TargetBinding, connectionbinding.CredentialSnapshot) (connectionbinding.RuntimePool, error) {
	factory.calls.Add(1)
	return nativePoolAuthorityRuntimePool{}, nil
}

type nativePoolAuthorityRuntimePool struct{}

func (nativePoolAuthorityRuntimePool) HealthCheck(context.Context) error { return nil }
func (nativePoolAuthorityRuntimePool) Close() error                      { return nil }

type nativePoolAuthorityBindingStore struct {
	catalog *nativePoolAuthorityBindingCatalog
}

func (store nativePoolAuthorityBindingStore) Save(ctx context.Context, binding connectionbinding.TargetBinding, expectedRevision int64) (connectionbinding.TargetBinding, error) {
	return store.catalog.Save(ctx, binding, expectedRevision)
}

type nativePoolAuthorityRotationAudit struct{ calls *atomic.Int64 }

func (audit nativePoolAuthorityRotationAudit) RecordCredentialRotation(context.Context, connectionbinding.RotationAuditEvent) error {
	audit.calls.Add(1)
	return nil
}
