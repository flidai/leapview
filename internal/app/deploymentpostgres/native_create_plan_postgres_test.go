package deploymentpostgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	deploymentaudit "github.com/flidai/leapview/internal/app/deploymentaudit"
	deploymentevents "github.com/flidai/leapview/internal/app/deploymentevents"
	deploymentoperation "github.com/flidai/leapview/internal/app/deploymentoperation"
	"github.com/flidai/leapview/internal/app/runtimefactory"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	platformbootstrappostgres "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	"github.com/flidai/leapview/internal/platform/events/postgres"
	operationpostgres "github.com/flidai/leapview/internal/platform/operation/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	project "github.com/flidai/leapview/internal/project"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/release"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type nativePlanSourceReader struct {
	mu    sync.Mutex
	calls int
	snap  project.CandidateSourceSnapshot
}

func (r *nativePlanSourceReader) SnapshotAttestation(_ context.Context, _ project.CandidateSourceScope, _, _ string) (project.CandidateSourceSnapshot, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return r.snap, nil
}

func (r *nativePlanSourceReader) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type nativePlanArtifactInspector struct {
	mu        sync.Mutex
	calls     int
	set       release.CandidateArtifactSet
	entered   chan struct{}
	continueC chan struct{}
}

type nativePlanOperationLookupFunc func(context.Context, deploymentmodule.NativeOperationAcquireInput) (deploymentmodule.NativeOperationRecord, bool, error)

func (f nativePlanOperationLookupFunc) Lookup(ctx context.Context, input deploymentmodule.NativeOperationAcquireInput) (deploymentmodule.NativeOperationRecord, bool, error) {
	return f(ctx, input)
}

func (r *nativePlanArtifactInspector) InspectCandidateArtifacts(_ context.Context, request release.CandidateArtifactRequest) (release.CandidateArtifactSet, error) {
	r.mu.Lock()
	r.calls++
	entered, continueC, set := r.entered, r.continueC, r.set
	r.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if continueC != nil {
		<-continueC
	}
	digest := sha256.Sum256([]byte(request.CandidateID))
	set.Generation.Identity = projectgraph.ServingIdentity{ProjectID: request.Scope.ProjectID, Environment: request.Scope.Environment, GenerationID: "inspect-" + hex.EncodeToString(digest[:])}
	manifest, bundleDigest, err := projectbundle.PackCompiledProject(set.Compiler.Artifact, set.Compiler.Plan, io.Discard)
	if err != nil {
		return release.CandidateArtifactSet{}, err
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return release.CandidateArtifactSet{}, err
	}
	set.Artifact.ContentDigest = bundleDigest
	set.Generation.ArtifactDigest = bundleDigest
	set.Generation.ServingArtifactID = "artifact-" + strings.TrimPrefix(bundleDigest, "sha256:")
	set.Generation.BundleManifestJSON = string(manifestJSON)
	return set, nil
}

func (r *nativePlanArtifactInspector) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func nativePlanPostgresDB(t *testing.T) (*pgxpool.Pool, *deploymentnative.Repository) {
	t.Helper()
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "native_create_plan")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := accesspostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("apply access schema: %v", err)
	}
	if _, err := tx.Exec(t.Context(), postgres.SchemaSQL()); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("apply event schema: %v", err)
	}
	if err := operationpostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("apply operation schema: %v", err)
	}
	if err := platformbootstrappostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("apply platform bootstrap schema: %v", err)
	}
	if err := deploymentnative.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("apply deployment schema: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return db, deploymentnative.New(db)
}

func nativePlanPostgresFixture(t *testing.T, sourceDigest, attestationDigest string) (project.CandidateSourceSnapshot, release.CandidateArtifactSet) {
	t.Helper()
	projectID := projectgraph.ResourceID("project_native_plan")
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: projectID, Kind: projectgraph.KindProject, Name: "native_plan"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := projectartifact.NewProject(graph, projectmanifest.Project{ID: projectID.String(), Name: "native_plan"})
	if err != nil {
		t.Fatal(err)
	}
	source := project.CandidateSourceSnapshot{ProjectID: projectID, ArtifactDigest: sourceDigest, SourceAttestationDigest: attestationDigest, ProjectFile: "leapview.yaml", ProjectDigest: artifact.Digest()}
	set := release.CandidateArtifactSet{
		Artifact:                 release.ProjectArtifactProvenance{SourceDigest: sourceDigest, ProjectDigest: artifact.Digest(), CompilerVersion: projectartifact.CompilerVersion, SchemaVersion: projectartifact.Version},
		AuthorizationFingerprint: createPlanTestDigest('c'),
		Generation:               release.CandidateGenerationArtifact{DataRevision: "sources:1", DataMode: release.GenerationDataRefreshSources, Deterministic: true},
		Compiler:                 release.CandidateCompilerEvidence{Graph: graph, Manifest: artifact.Manifest(), Artifact: artifact, Plan: projectcompiler.ProjectPlan{Project: projectID.String(), Deterministic: true}},
	}
	return source, set
}

func nativePlanCoordinator(t *testing.T, db *pgxpool.Pool, source *nativePlanSourceReader, inspector *nativePlanArtifactInspector) *NativeCreatePlanCoordinator {
	t.Helper()
	eventRepo := postgres.New()
	operationRepo := operationpostgres.NewWithConfig(db, time.Second, time.Hour)
	coord, err := NewNativeCreatePlanCoordinator(NativeCreatePlanConfig{
		Repository: deploymentnative.New(db), Sources: source, Artifacts: inspector, RuntimeVersion: "runtime-native-test",
		Policy: runtimefactory.CandidateDeliveryPolicy{ApprovalPolicyRevision: runtimefactory.CurrentApprovalPolicyRevision},
		Events: deploymentevents.NewWithRepository(eventRepo), Audit: deploymentaudit.NewWithRepository(accesspostgres.New()), Operations: deploymentoperation.New(operationRepo),
		Clock: func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return coord
}

func nativePlanRequest() deploymentmodule.NativeDeliveryPlanRequest {
	return deploymentmodule.NativeDeliveryPlanRequest{ProjectID: projectgraph.ResourceID("project_native_plan"), TargetID: "target_native_plan", Environment: "prod", PrincipalID: "principal-native", SourceOwnerID: "owner-native", Operation: string(deployment.DeliveryOperationCodeChange), SourceDigest: createPlanTestDigest('a'), SourceAttestationDigest: createPlanTestDigest('b'), IdempotencyKey: "native-plan-key"}
}

func TestNativeCreatePlanPostgresAtomicallyBootstrapsFreshTargetAndClaim(t *testing.T) {
	db, repo := nativePlanPostgresDB(t)
	snapshot, artifacts := nativePlanPostgresFixture(t, createPlanTestDigest('a'), createPlanTestDigest('b'))
	coord := nativePlanCoordinator(t, db, &nativePlanSourceReader{snap: snapshot}, &nativePlanArtifactInspector{set: artifacts})
	request := nativePlanRequest()

	created, err := coord.CreatePlan(t.Context(), request)
	if err != nil {
		t.Fatalf("create first native plan: %v", err)
	}
	if created.TargetID != request.TargetID || created.ProjectID != request.ProjectID || created.BaseTargetRevision != 1 || created.BaseGenerationID != uuid.Nil {
		t.Fatalf("fresh plan target projection = %#v", created)
	}
	target, err := repo.Target(t.Context(), request.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	if target.TargetID != request.TargetID || target.ProjectID != request.ProjectID.String() || target.Environment != request.Environment || target.TargetRevision != 1 || target.ActiveGenerationID != "" || target.ActivePublicationID != "" {
		t.Fatalf("fresh target = %#v", target)
	}
	claim, err := repo.GetProjectClaim(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if claim.ProjectID != request.ProjectID || string(claim.Environment) != request.Environment || claim.ClaimedBy != request.PrincipalID || claim.ClaimedAt.IsZero() {
		t.Fatalf("fresh project claim = %#v", claim)
	}
	stored, err := repo.Plan(t.Context(), created.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	if stored.TargetID != target.TargetID || stored.PlanRevision != 1 {
		t.Fatalf("fresh target plan = %#v", stored)
	}

	replayed, err := coord.CreatePlan(t.Context(), request)
	if err != nil {
		t.Fatalf("replay first native plan: %v", err)
	}
	if !reflect.DeepEqual(created, replayed) {
		t.Fatalf("fresh target replay differs:\ncreated=%#v\nreplayed=%#v", created, replayed)
	}
}

func TestNativeCreatePlanPostgresSuccessCompletionAndExactReplay(t *testing.T) {
	db, repo := nativePlanPostgresDB(t)
	if _, err := repo.CreateTarget(t.Context(), deploymentnative.TargetInput{TargetID: "target_native_plan", ProjectID: "project_native_plan", Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	snapshot, artifacts := nativePlanPostgresFixture(t, createPlanTestDigest('a'), createPlanTestDigest('b'))
	artifacts.Generation.Connections = []release.CandidateConnectionRequirement{{ConnectionID: projectgraph.ResourceID("warehouse"), ConnectorKind: "postgres"}}
	source := &nativePlanSourceReader{snap: snapshot}
	inspector := &nativePlanArtifactInspector{set: artifacts}
	coord := nativePlanCoordinator(t, db, source, inspector)
	bindingEvidence := []deployment.CandidateConnectionEvidence{{BindingID: "binding_warehouse", ConnectionID: projectgraph.ResourceID("warehouse"), ConnectorKind: "postgres", Revision: 7, ProviderVersion: "provider:v3", EndpointConfigHash: createPlanTestDigest('7')}}
	bindingLeases := &nativeConnectionLeases{evidence: bindingEvidence}
	bindingLeaser := &nativeConnectionLeaser{leases: bindingLeases}
	coord.bindingEvidence = bindingLeaser
	policyCalls := 0
	coord.policyResolver = func(operation deployment.DeliveryOperationKind) (runtimefactory.CandidateDeliveryPolicy, error) {
		policyCalls++
		if operation != deployment.DeliveryOperationCodeChange {
			t.Fatalf("policy operation = %q, want code change", operation)
		}
		return runtimefactory.CandidateDeliveryPolicy{RequiresApproval: true, ApprovalPolicyRevision: runtimefactory.CurrentApprovalPolicyRevision}, nil
	}
	request := nativePlanRequest()
	first, err := coord.CreatePlan(t.Context(), request)
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if first.ID == uuid.Nil || first.Status != "planned" || first.EventID == uuid.Nil || first.AuditID == uuid.Nil {
		t.Fatalf("incomplete plan projection: %+v", first)
	}
	if err := coord.CompleteNativePlanCommand(t.Context(), first); err != nil {
		t.Fatalf("complete plan command: %v", err)
	}
	originalLookup := coord.operationLookup
	coord.operationLookup = nativePlanOperationLookupFunc(func(ctx context.Context, input deploymentmodule.NativeOperationAcquireInput) (deploymentmodule.NativeOperationRecord, bool, error) {
		record, found, err := originalLookup.Lookup(ctx, input)
		record.Outcome = nil
		return record, found, err
	})
	if err := coord.CompleteNativePlanCommand(t.Context(), first); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("completion accepted missing operation outcome: %v", err)
	}
	coord.operationLookup = originalLookup
	if source.count() != 1 || inspector.count() != 1 || policyCalls != 1 || bindingLeaser.resolveCalls != 1 || bindingLeaser.calls != 0 || bindingLeases.closeCalls != 0 {
		t.Fatalf("initial source/artifact/policy/resolve/acquire/close reads = %d/%d/%d/%d/%d/%d, want 1/1/1/1/0/0", source.count(), inspector.count(), policyCalls, bindingLeaser.resolveCalls, bindingLeaser.calls, bindingLeases.closeCalls)
	}
	second, err := coord.CreatePlan(t.Context(), request)
	if err != nil {
		t.Fatalf("replay plan: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("replay projection differs:\nfirst=%+v\nsecond=%+v", first, second)
	}
	if source.count() != 1 || inspector.count() != 1 || policyCalls != 1 || bindingLeaser.resolveCalls != 1 || bindingLeaser.calls != 0 || bindingLeases.closeCalls != 0 {
		t.Fatalf("replay performed source/artifact/policy/resolve/acquire/close reads = %d/%d/%d/%d/%d/%d, want unchanged 1/1/1/1/0/0", source.count(), inspector.count(), policyCalls, bindingLeaser.resolveCalls, bindingLeaser.calls, bindingLeases.closeCalls)
	}
	conflict := request
	conflict.SourceAttestationDigest = createPlanTestDigest('d')
	if _, err := coord.CreatePlan(t.Context(), conflict); !errors.Is(err, deploymentmodule.ErrNativeOperationConflict) {
		t.Fatalf("changed request with reused key error = %v, want operation conflict", err)
	}
	if source.count() != 1 || inspector.count() != 1 {
		t.Fatalf("conflicting replay performed source/artifact reads = %d/%d, want unchanged 1/1", source.count(), inspector.count())
	}
	var operations, plans, events, audits int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM platform.operation`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM delivery.delivery_plan`).Scan(&plans); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM event.event_log`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if operations != 1 || plans != 1 || events != 1 || audits != 1 {
		t.Fatalf("durable consequence counts operation/plan/event/audit = %d/%d/%d/%d, want 1/1/1/1", operations, plans, events, audits)
	}
	var plannedServingDigest string
	if err := db.QueryRow(t.Context(), `SELECT artifact_digest FROM delivery.delivery_plan`).Scan(&plannedServingDigest); err != nil {
		t.Fatal(err)
	}
	if err := deployment.ValidateDeliveryDigest(plannedServingDigest); err != nil {
		t.Fatalf("planned serving digest = %q: %v", plannedServingDigest, err)
	}
	if plannedServingDigest == request.SourceDigest {
		t.Fatalf("planned serving digest unexpectedly reused source digest %q", plannedServingDigest)
	}
	_, expectedServingDigest, err := projectbundle.PackCompiledProject(artifacts.Compiler.Artifact, artifacts.Compiler.Plan, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if plannedServingDigest != expectedServingDigest {
		t.Fatalf("planned serving digest = %q, want deterministic bundle digest %q", plannedServingDigest, expectedServingDigest)
	}
	persistedRow, err := repo.Plan(t.Context(), first.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	persistedRich, err := persistedRow.RichPlan()
	if err != nil {
		t.Fatal(err)
	}
	if persistedRich.SourceDigest != request.SourceDigest || persistedRich.ServingArtifactDigest != plannedServingDigest {
		t.Fatalf("persisted source/serving identities = %q/%q, want %q/%q", persistedRich.SourceDigest, persistedRich.ServingArtifactDigest, request.SourceDigest, plannedServingDigest)
	}
	if !persistedRich.Governance.RequiresApproval {
		t.Fatal("persisted plan dropped operation-sensitive approval policy")
	}
	wantBindingDigest, err := deployment.BindingFingerprint(bindingEvidence)
	if err != nil {
		t.Fatal(err)
	}
	if persistedRich.Execution.BindingDigest != wantBindingDigest {
		t.Fatalf("persisted binding digest = %q, want exact lease evidence %q", persistedRich.Execution.BindingDigest, wantBindingDigest)
	}
}

func TestNativeCreatePlanPostgresRejectsTargetRevisionDriftBeforeConsequences(t *testing.T) {
	db, repo := nativePlanPostgresDB(t)
	if _, err := repo.CreateTarget(t.Context(), deploymentnative.TargetInput{TargetID: "target_native_plan", ProjectID: "project_native_plan", Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	snapshot, artifacts := nativePlanPostgresFixture(t, createPlanTestDigest('a'), createPlanTestDigest('b'))
	source := &nativePlanSourceReader{snap: snapshot}
	entered := make(chan struct{}, 1)
	continueC := make(chan struct{})
	inspector := &nativePlanArtifactInspector{set: artifacts, entered: entered, continueC: continueC}
	coord := nativePlanCoordinator(t, db, source, inspector)
	request := nativePlanRequest()
	resultC := make(chan error, 1)
	go func() {
		_, err := coord.CreatePlan(t.Context(), request)
		resultC <- err
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("artifact inspection did not block")
	}
	if _, err := db.Exec(t.Context(), `UPDATE delivery.delivery_target SET target_revision=target_revision+1 WHERE target_id=$1`, request.TargetID); err != nil {
		t.Fatal(err)
	}
	close(continueC)
	err := <-resultC
	if !errors.Is(err, deployment.ErrDeliveryConflict) || !strings.Contains(err.Error(), "target fence changed") {
		t.Fatalf("drift error = %v, want target fence conflict", err)
	}
	var operations, plans, events, audits int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM platform.operation`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM delivery.delivery_plan`).Scan(&plans); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM event.event_log`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if operations != 0 || plans != 0 || events != 0 || audits != 0 {
		t.Fatalf("drift consequences operation/plan/event/audit = %d/%d/%d/%d, want 0/0/0/0", operations, plans, events, audits)
	}
}

func TestNativeCreatePlanPostgresConcurrentSameKeyConverges(t *testing.T) {
	db, repo := nativePlanPostgresDB(t)
	if _, err := repo.CreateTarget(t.Context(), deploymentnative.TargetInput{TargetID: "target_native_plan", ProjectID: "project_native_plan", Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	snapshot, artifacts := nativePlanPostgresFixture(t, createPlanTestDigest('a'), createPlanTestDigest('b'))
	source := &nativePlanSourceReader{snap: snapshot}
	inspector := &nativePlanArtifactInspector{set: artifacts}
	coord := nativePlanCoordinator(t, db, source, inspector)
	request := nativePlanRequest()
	start := make(chan struct{})
	type result struct {
		plan deploymentmodule.NativeDeliveryPlan
		err  error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			plan, err := coord.CreatePlan(t.Context(), request)
			results <- result{plan: plan, err: err}
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent results errors = %v / %v", first.err, second.err)
	}
	if !reflect.DeepEqual(first.plan, second.plan) {
		t.Fatalf("concurrent results diverged:\nfirst=%+v\nsecond=%+v", first.plan, second.plan)
	}
	var operations, plans, events, audits int
	for query, destination := range map[string]*int{
		`SELECT count(*) FROM platform.operation`:     &operations,
		`SELECT count(*) FROM delivery.delivery_plan`: &plans,
		`SELECT count(*) FROM event.event_log`:        &events,
		`SELECT count(*) FROM audit.audit_event`:      &audits,
	} {
		if err := db.QueryRow(t.Context(), query).Scan(destination); err != nil {
			t.Fatal(err)
		}
	}
	if operations != 1 || plans != 1 || events != 1 || audits != 1 {
		t.Fatalf("concurrent consequence counts = %d/%d/%d/%d, want 1/1/1/1", operations, plans, events, audits)
	}
}
