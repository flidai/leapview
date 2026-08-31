package module

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	deploymentaudit "github.com/flidai/leapview/internal/app/deploymentaudit"
	apprefreshpostgres "github.com/flidai/leapview/internal/app/refreshpostgres"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	projectpipelineplan "github.com/flidai/leapview/internal/project/contracts/pipelineplan"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/jackc/pgx/v5/pgxpool"
)

type failingNativeCompletionQueue struct {
	*PostgresJobsAdapter
	err error
}

func (q failingNativeCompletionQueue) CompleteJobTx(context.Context, refreshpostgres.Tx, refreshrun.JobRecord) error {
	return q.err
}

type nativeRefreshFixture struct {
	db        *pgxpool.Pool
	delivery  *deploymentpostgres.Repository
	refresh   *refreshpostgres.Repository
	jobs      *PostgresJobsAdapter
	finalizer *apprefreshpostgres.PostgresNativeRefreshFinalizerAdapter
	job       refreshrun.JobRecord
	result    refreshrun.CanonicalRefreshResult
	evidence  refreshpostgres.PublicationInput
	plan      deploymentpostgres.DeliveryPlan
	baseID    string
	resultID  string
	poolID    string
	catalogID string
	targetID  string
}

func newNativeRefreshFixture(t *testing.T) nativeRefreshFixture {
	t.Helper()
	db := concreteModulePostgresDB(t)
	digest := func(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }
	baseID := "0198f2c0-7c7a-7f00-8a11-000000000105"
	resultID := "0198f2c0-7c7a-7f00-8a11-000000000111"
	pipeline, err := projectpipelineplan.New(projectpipelineplan.Plan{
		ID: "pipeline-plan-native-finalizer", PipelineID: "pipeline_concrete", ProjectID: "project_concrete", Environment: "prod", SemanticModelID: "semantic_concrete", ServingGenerationID: baseID,
		ArtifactDigest: digest('e'), SelectionDigest: digest('f'), MaterializationScope: []string{"model_concrete"}, ModelExecutionOrder: []string{"model_concrete"}, QualificationChecks: []string{"compatibility"}, InvocationSource: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, _, poolID, _, _ := seedConcreteDelivery(t, db, pipeline)
	delivery := deploymentpostgres.NewWithActivationAudit(db, deploymentaudit.NewWithRepository(accesspostgres.New()))
	basePub, err := delivery.CreatePublication(t.Context(), deploymentpostgres.PublicationInput{PublicationID: "0198f2c0-7c7a-7f00-8a11-000000000106", TargetID: "target_concrete_prod", GenerationID: baseID, CandidateID: "0198f2c0-7c7a-7f00-8a11-000000000102", SnapshotSealID: "0198f2c0-7c7a-7f00-8a11-000000000104", ExpectedTargetRevision: 1, ActorID: "operator-native-finalizer", RequestDigest: digest('8')})
	if err != nil {
		t.Fatal(err)
	}
	baseLease, err := delivery.AcquireLease(t.Context(), deploymentpostgres.LeaseInput{LeaseID: "0198f2c0-7c7a-7f00-8a11-000000000107", TargetID: "target_concrete_prod", OwnerID: "operator-native-finalizer", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.Activate(t.Context(), deploymentpostgres.ActivationInput{PublicationID: basePub.PublicationID, TargetID: basePub.TargetID, GenerationID: baseID, ExpectedTargetRevision: 1, RequestDigest: basePub.RequestDigest, ActorID: basePub.ActorID, LeaseID: baseLease.LeaseID, OwnerID: baseLease.OwnerID, FencingEpoch: baseLease.FencingEpoch, CorrelationID: "0198f2c0-7c7a-7f00-8a11-000000000110"}); err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.CreateGeneration(t.Context(), deploymentpostgres.GenerationInput{GenerationID: resultID, TargetID: "target_concrete_prod", CandidateID: "0198f2c0-7c7a-7f00-8a11-000000000102", SnapshotSealID: "0198f2c0-7c7a-7f00-8a11-000000000104", PlanID: plan.PlanID, PlanDigest: plan.PlanDigest, ArtifactRoot: "artifacts/concrete", ArtifactRootDigest: digest('7'), ServingArtifactDigest: plan.ArtifactDigest, CompiledGraphDigest: plan.CompiledGraphDigest, CompiledConfigDigest: plan.CompiledConfigDigest, SecurityDomainFingerprint: plan.SecurityDomainFingerprint, GenerationRevision: 2}); err != nil {
		t.Fatal(err)
	}
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := NewPostgresJobsAdapter(jobspostgresForNativeFixture(t, db), refreshRepo)
	identity := projectgraph.ServingIdentity{ProjectID: "project_concrete", Environment: "prod", GenerationID: baseID}
	runsPersistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{PublicationIdentityResolver: staticPublicationIdentityResolver(poolID, "catalog-concrete"), SchedulerOwner: "scheduler-native-fixture", Jobs: jobsRepo, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: poolID, catalogID: "catalog-concrete"}, CancelAuditWriter: integrationAuditWriter{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = createTreeRootE(t.Context(), runsPersistence.Runs, refreshrun.RunInput{RunID: "native-finalizer-run", Identity: identity, SemanticModelID: "semantic_concrete", PipelineID: "pipeline_concrete", PipelinePlan: &pipeline, InvocationSource: "manual", PrincipalID: "principal:native-finalizer", EstimatedMemoryBytes: 1, TargetRevision: 2, TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline_concrete", TriggerType: refreshrun.TriggerManual, JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: `{}`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := refreshrun.ReadScope{ProjectID: identity.ProjectID, Environment: identity.Environment}
	candidates, err := jobsRepo.ListExecutableJobs(t.Context(), scope, 1)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("list executable jobs = %#v, %v", candidates, err)
	}
	claimed, ok, err := jobsRepo.ClaimExecutableJob(t.Context(), candidates[0], "worker-native-finalizer", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim = %v, %v", ok, err)
	}
	if _, err := runsPersistence.Runs.MarkRunPrepared(t.Context(), claimed); err != nil {
		t.Fatal(err)
	}
	prepared := claimed
	finalizer, err := apprefreshpostgres.NewPostgresNativeRefreshFinalizer(refreshRepo, delivery, "target_concrete_prod")
	if err != nil {
		t.Fatal(err)
	}
	result := refreshrun.CanonicalRefreshResult{PlanID: plan.PlanID, ServingStateID: resultID, NativeGenerationID: resultID, SnapshotID: 777}
	evidence := refreshpostgres.PublicationInput{RunID: prepared.RunID, BaseGenerationID: baseID, ResultGenerationID: resultID, ExpectedTargetRevision: 2, ResultTargetRevision: 3, PhysicalPoolID: poolID, CatalogID: "catalog-concrete"}
	return nativeRefreshFixture{db: db, delivery: delivery, refresh: refreshRepo, jobs: jobsRepo, finalizer: finalizer, job: prepared, result: result, evidence: evidence, plan: plan, baseID: baseID, resultID: resultID, poolID: poolID, catalogID: "catalog-concrete", targetID: "target_concrete_prod"}
}

// These tiny helpers keep the fixture independent of app composition while
// reusing the canonical queue/refresh adapters already exercised by the
// integration suite.
func jobspostgresForNativeFixture(t *testing.T, db *pgxpool.Pool) *jobspostgres.Repository {
	t.Helper()
	return jobspostgres.New(db)
}

func TestPostgresNativeRefreshFinalizerRollsBackAndReplaysExactly(t *testing.T) {
	f := newNativeRefreshFixture(t)
	failing := failingNativeCompletionQueue{PostgresJobsAdapter: f.jobs, err: errors.New("terminal job failure")}
	failedPersistence, err := NewPostgresPersistence(f.refresh, PostgresPersistenceConfig{PublicationIdentityResolver: staticPublicationIdentityResolver(f.poolID, f.catalogID), SchedulerOwner: "scheduler-native-failure", Jobs: failing, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: f.poolID, catalogID: f.catalogID}, NativeFinalizer: f.finalizer, CancelAuditWriter: integrationAuditWriter{}})
	if err != nil {
		t.Fatal(err)
	}
	pubID, _, _, _ := apprefreshpostgres.NativeRefreshIdentities(f.job, f.result, f.evidence)
	completionErr := failedPersistence.Publication.(refreshrun.CanonicalPublicationUnitOfWork).CompleteCanonicalRefresh(t.Context(), f.job, f.result)
	if completionErr == nil {
		t.Fatal("completion unexpectedly succeeded with failing job transition")
	}
	if _, err := f.delivery.Publication(t.Context(), pubID); !errors.Is(err, deploymentpostgres.ErrNotFound) {
		t.Fatalf("native publication after rollback = %v, want not found", err)
	}
	target, err := f.delivery.Target(t.Context(), f.targetID)
	if err != nil {
		t.Fatal(err)
	}
	if target.TargetRevision != 2 || target.ActiveGenerationID != f.baseID {
		t.Fatalf("target after rollback = %#v", target)
	}
	run, err := f.refresh.LookupRun(t.Context(), f.job.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != refreshrun.RunStatusPrepared {
		t.Fatalf("run after rollback = %q, want prepared", run.Status)
	}

	persistence, err := NewPostgresPersistence(f.refresh, PostgresPersistenceConfig{PublicationIdentityResolver: staticPublicationIdentityResolver(f.poolID, f.catalogID), SchedulerOwner: "scheduler-native-success", Jobs: f.jobs, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: f.poolID, catalogID: f.catalogID}, NativeFinalizer: f.finalizer, CancelAuditWriter: integrationAuditWriter{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.Publication.(refreshrun.CanonicalPublicationUnitOfWork).CompleteCanonicalRefresh(t.Context(), f.job, f.result); err != nil {
		t.Fatal(err)
	}
	nativePub, err := f.delivery.Publication(t.Context(), pubID)
	if err != nil || nativePub.State != "committed" {
		t.Fatalf("native publication = %#v, %v", nativePub, err)
	}
	target, err = f.delivery.Target(t.Context(), f.targetID)
	if err != nil || target.TargetRevision != 3 || target.ActiveGenerationID != f.resultID || target.ActivePublicationID != pubID {
		t.Fatalf("target after native completion = %#v, %v", target, err)
	}
	// The refresh completion replay returns from its durable refresh evidence;
	// direct finalizer replay also remains exact after the worker fence is gone.
	tx, err := f.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.finalizer.FinalizeCanonicalRefreshTx(t.Context(), tx, f.job, f.result, f.evidence); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	_ = tx.Rollback(t.Context())
}

func TestPostgresNativeRefreshFinalizerRejectsStaleRunFence(t *testing.T) {
	f := newNativeRefreshFixture(t)
	stale := f.job
	stale.LeaseRevision++
	pubID, _, _, _ := apprefreshpostgres.NativeRefreshIdentities(stale, f.result, f.evidence)
	tx, err := f.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = f.finalizer.FinalizeCanonicalRefreshTx(t.Context(), tx, stale, f.result, f.evidence)
	_ = tx.Rollback(t.Context())
	if !errors.Is(err, refreshpostgres.ErrStaleFence) {
		t.Fatalf("stale run fence error = %v, want stale fence", err)
	}
	if _, err := f.delivery.Publication(t.Context(), pubID); !errors.Is(err, deploymentpostgres.ErrNotFound) {
		t.Fatalf("publication after stale fence = %v", err)
	}
}

func TestPostgresNativeRefreshFinalizerRequiresExplicitGenerationEquality(t *testing.T) {
	f := newNativeRefreshFixture(t)
	result := f.result
	result.ServingStateID = "different-serving-state"
	tx, err := f.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = f.finalizer.FinalizeCanonicalRefreshTx(t.Context(), tx, f.job, result, f.evidence)
	_ = tx.Rollback(t.Context())
	if !errors.Is(err, refreshpostgres.ErrConflict) || !strings.Contains(err.Error(), "serving-state") {
		t.Fatalf("generation identity mismatch error = %v, want conflict", err)
	}
}

func TestPostgresNativeRefreshFinalizerRejectsCrossScopeTarget(t *testing.T) {
	f := newNativeRefreshFixture(t)
	if _, err := f.delivery.CreateTarget(t.Context(), deploymentpostgres.TargetInput{TargetID: "target_other_prod", ProjectID: "project_other", Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	finalizer, err := apprefreshpostgres.NewPostgresNativeRefreshFinalizerWithResolver(f.refresh, f.delivery, apprefreshpostgres.PostgresNativeRefreshTargetResolverFunc(func(context.Context, refreshpostgres.Tx, refreshrun.JobRecord) (string, error) {
		return "target_other_prod", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = finalizer.FinalizeCanonicalRefreshTx(t.Context(), tx, f.job, f.result, f.evidence)
	_ = tx.Rollback(t.Context())
	if !errors.Is(err, refreshpostgres.ErrConflict) {
		t.Fatalf("cross-scope target error = %v, want conflict", err)
	}
}

func TestPostgresNativeRefreshFinalizerRequiresTargetResolver(t *testing.T) {
	_, err := apprefreshpostgres.NewPostgresNativeRefreshFinalizerWithResolver(refreshpostgres.New(nil), deploymentpostgres.New(nil), nil)
	if err == nil || !strings.Contains(err.Error(), "target resolver") {
		t.Fatalf("missing target resolver error = %v", err)
	}
}

func TestPostgresNativeRefreshFinalizerRejectsExpiredDeliveryLease(t *testing.T) {
	f := newNativeRefreshFixture(t)
	pubID, leaseID, _, requestDigest := apprefreshpostgres.NativeRefreshIdentities(f.job, f.result, f.evidence)
	generation, err := f.delivery.Generation(t.Context(), f.result.ServingStateID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.delivery.CreatePublication(t.Context(), deploymentpostgres.PublicationInput{PublicationID: pubID, TargetID: f.targetID, GenerationID: f.resultID, ExpectedBaseGenerationID: f.baseID, CandidateID: generation.CandidateID, SnapshotSealID: generation.SnapshotSealID, ExpectedTargetRevision: 2, ActorID: f.job.PrincipalID, RequestDigest: requestDigest}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.delivery.AcquireLease(t.Context(), deploymentpostgres.LeaseInput{LeaseID: leaseID, TargetID: f.targetID, OwnerID: f.job.LeaseOwner, ExpiresAt: time.Now().UTC().Add(20 * time.Millisecond)}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	tx, err := f.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = f.finalizer.FinalizeCanonicalRefreshTx(t.Context(), tx, f.job, f.result, f.evidence)
	_ = tx.Rollback(t.Context())
	if !errors.Is(err, deploymentpostgres.ErrStaleFence) {
		t.Fatalf("expired delivery lease error = %v, want stale fence", err)
	}
	publication, err := f.delivery.Publication(t.Context(), pubID)
	if err != nil || publication.State != "pending" {
		t.Fatalf("pending publication after expired lease = %#v, %v", publication, err)
	}
}
