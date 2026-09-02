package module

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	physicalpoolpostgres "github.com/flidai/leapview/internal/analytics/physicalpool/postgres"
	deploymentaudit "github.com/flidai/leapview/internal/app/deploymentaudit"
	refreshcomposition "github.com/flidai/leapview/internal/app/refreshpostgres"
	"github.com/flidai/leapview/internal/deployment"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	jobpolicy "github.com/flidai/leapview/internal/platform/jobs"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	operationpostgres "github.com/flidai/leapview/internal/platform/operation/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectpipelineplan "github.com/flidai/leapview/internal/project/contracts/pipelineplan"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
)

type integrationAuditWriter struct{ fail bool }

func (w integrationAuditWriter) RecordRefreshCancelAuditTx(context.Context, refreshpostgres.Tx, access.AuditIntent) error {
	if w.fail {
		return errors.New("audit writer failure")
	}
	return nil
}

type recordingCancelAuditWriter struct{ calls int }

func (w *recordingCancelAuditWriter) RecordRefreshCancelAuditTx(context.Context, refreshpostgres.Tx, access.AuditIntent) error {
	w.calls++
	return nil
}

type integrationCanonicalVerifier struct {
	physicalPoolID string
	catalogID      string
}

type integrationPublicationIdentityResolver struct {
	physicalPoolID string
	catalogID      string
}

func (r integrationPublicationIdentityResolver) ResolvePublicationIdentityTx(_ context.Context, _ refreshpostgres.Tx, _ PostgresPublicationIdentityRequest) (PostgresPublicationIdentity, error) {
	return PostgresPublicationIdentity{PhysicalPoolID: r.physicalPoolID, CatalogID: r.catalogID}, nil
}

func staticPublicationIdentityResolver(physicalPoolID, catalogID string) PostgresPublicationIdentityResolver {
	return integrationPublicationIdentityResolver{physicalPoolID: physicalPoolID, catalogID: catalogID}
}

func unavailablePublicationIdentityResolver() PostgresPublicationIdentityResolver {
	return PostgresPublicationIdentityResolverFunc(func(context.Context, refreshpostgres.Tx, PostgresPublicationIdentityRequest) (PostgresPublicationIdentity, error) {
		return PostgresPublicationIdentity{}, ErrPublicationIdentityUnavailable
	})
}

type failingSupersedeQueue struct {
	*PostgresJobsAdapter
	err error
}

func (q failingSupersedeQueue) SupersedeJobsTx(context.Context, refreshpostgres.Tx, []string) error {
	return q.err
}

type failingCancelClaimQueue struct {
	*PostgresJobsAdapter
	err error
}

func (q failingCancelClaimQueue) CancelClaimedJobTx(context.Context, refreshpostgres.Tx, refreshrun.JobRecord) error {
	return q.err
}

func (v integrationCanonicalVerifier) VerifyCanonicalRefreshTx(_ context.Context, _ refreshpostgres.Tx, job refreshrun.JobRecord, result refreshrun.CanonicalRefreshResult) (refreshpostgres.PublicationInput, error) {
	expected := job.TargetRevision
	if expected == 0 {
		expected = 1
	}
	return refreshpostgres.PublicationInput{RunID: job.RunID, BaseGenerationID: job.Identity.GenerationID, ResultGenerationID: result.ServingStateID, ExpectedTargetRevision: expected, ResultTargetRevision: expected + 1, PhysicalPoolID: v.physicalPoolID, CatalogID: v.catalogID}, nil
}

func modulePostgresTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "refresh_module_authority")
	admin, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatalf("create concrete run: %v", err)
	}
	t.Cleanup(admin.Close)
	tx, err := admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := jobspostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := operationpostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := refreshpostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return admin
}

func createTreeRoot(t *testing.T, runs refreshrun.RunTreeRepository, input refreshrun.RunInput, occurrence *refreshschedule.Occurrence) refreshrun.RunRecord {
	t.Helper()
	root, _, err := runs.CreateRunTree(t.Context(), refreshrun.RunTreeInput{Root: input, Occurrence: occurrence})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func createTreeRootE(ctx context.Context, runs refreshrun.RunTreeRepository, input refreshrun.RunInput, occurrence *refreshschedule.Occurrence) (refreshrun.RunRecord, error) {
	root, _, err := runs.CreateRunTree(ctx, refreshrun.RunTreeInput{Root: input, Occurrence: occurrence})
	return root, err
}

func concreteModulePostgresDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "refresh_module_concrete_authority")
	admin, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	tx, err := admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := accesspostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), eventspostgres.SchemaSQL()); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := deploymentpostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := jobspostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := operationpostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := refreshpostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := physicalpoolpostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := ducklakepostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return admin
}

func accessOnlyPostgresDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "refresh_module_access_authority")
	pool, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := accesspostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return pool
}

func seedConcreteDelivery(t *testing.T, db *pgxpool.Pool, pipelinePlans ...projectpipelineplan.Plan) (deploymentpostgres.DeliveryPlan, string, string, string, string) {
	t.Helper()
	delivery := deploymentpostgres.New(db)
	digest := func(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }
	planID := "0198f2c0-7c7a-7f00-8a11-000000000101"
	candidateID := "0198f2c0-7c7a-7f00-8a11-000000000102"
	attemptID := "0198f2c0-7c7a-7f00-8a11-000000000103"
	sealID := "0198f2c0-7c7a-7f00-8a11-000000000104"
	generationID := "0198f2c0-7c7a-7f00-8a11-000000000105"
	targetID := "target_concrete_prod"
	catalogDB, catalogUUID := "ducklake", "0198f2c0-7c7a-7f00-8a11-000000000108"
	compatibility := physicalpool.Compatibility{DuckDBRuntime: "duckdb:1", DuckLakeExtension: "ducklake:1", CatalogFormat: "ducklake:v1", StorageImplementation: "local", ObjectNamingContract: "object:v1"}
	pool, err := physicalpool.NewPhysicalPool(physicalpool.PoolIdentity{
		StorageLocation: t.TempDir(), StorageNamespace: "concrete", Region: "us-east", Tenant: "tenant-concrete", EncryptionDomain: "encryption-concrete", IsolationBoundary: targetID,
		RetentionAuthority: targetID, RetentionPolicy: physicalpool.RetentionPolicy{OrphanGracePeriodSeconds: 3600, ReaderGracePeriodSeconds: 300, BuildGracePeriodSeconds: 60}, Compatibility: compatibility,
	})
	if err != nil {
		t.Fatal(err)
	}
	poolID := pool.ID.String()
	poolRepo := physicalpoolpostgres.New(db)
	evidence, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{Compatibility: compatibility, ConformanceVersion: "concrete-v1", Checks: []physicalpool.EvidenceCheck{{ID: "schema", Passed: true}}})
	if err != nil {
		t.Fatal(err)
	}
	_, admission, err := poolRepo.CreateAndAdmit(t.Context(), pool, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ducklakepostgres.New(db).RegisterCatalog(t.Context(), ducklakepostgres.CatalogIdentity{PhysicalPoolID: poolID, CatalogDatabase: catalogDB, CatalogID: "catalog-concrete", CatalogUUID: catalogUUID, MetadataSchema: ducklake.MetadataSchemaForPool(poolID)}); err != nil {
		t.Fatal(err)
	}
	pipePlan := projectpipelineplan.Plan{}
	if len(pipelinePlans) > 0 {
		pipePlan = pipelinePlans[0]
	}
	artifactDigest := digest('e')
	qualificationDigest := digest('3')
	compiledGraphDigest, compiledConfigDigest, securityDigest := digest('b'), digest('c'), digest('d')
	if pipePlan.Digest != "" {
		artifactDigest = pipePlan.ArtifactDigest
		compiledGraphDigest, compiledConfigDigest, securityDigest = pipePlan.ExecutionDigest, pipePlan.ProvenanceDigest, pipePlan.GovernanceDigest
	}
	if _, err := delivery.CreateTarget(t.Context(), deploymentpostgres.TargetInput{TargetID: targetID, ProjectID: "project_concrete", Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	richPlanInput := deployment.DeliveryPlan{
		ID: planID, TargetID: targetID, ProjectID: "project_concrete", Environment: "prod",
		Operation: deployment.DeliveryOperationCodeChange, SourceDigest: artifactDigest, ServingArtifactDigest: artifactDigest,
		Execution: deployment.DeliveryExecutionInputs{
			SourceArtifactDigest: artifactDigest, CompilerDigest: compiledGraphDigest, ExecutableDigest: digest('4'), DependencyDigest: digest('5'),
			ConfigDigest: compiledConfigDigest, BindingDigest: securityDigest, RuntimeDigest: digest('0'), CapabilityDigest: admission.CompatibilityDigest,
		},
		Provenance: deployment.DeliveryProvenance{Builder: "refresh-concrete-test"},
		Governance: deployment.DeliveryGovernance{
			PolicyDigest: digest('2'), AuthorizationDigest: securityDigest, QualificationDigest: qualificationDigest, ApprovalPolicyRevision: 1, ExpiresAt: now.Add(time.Hour),
		},
		Evidence: deployment.DeliveryPlanEvidence{
			ImpactStatement: "refresh fixture impact", PhysicalWorkStatement: "materialize the exact pipeline closure", ReuseStatement: "no physical reuse",
			Qualification: deployment.DeliveryQualificationEvidence{Policy: "exact refresh qualification", Steps: []deployment.DeliveryQualificationStep{{ID: "compatibility", Kind: "contract", Description: "qualify exact snapshot", Required: true, Blocking: true}}},
			StalePolicy:   deployment.DeliveryStalePolicy{Mode: "reject"}, Rollback: deployment.DeliveryRollbackEvidence{Class: deployment.DeliveryRollbackSafe},
		},
		CreatedAt: now,
	}
	if pipePlan.Digest != "" {
		richPlanInput.BaseGenerationID = generationID
		richPlanInput.BaseTargetRevision = 1
		richPlanInput.PipelinePlan = &pipePlan
	}
	richPlan, err := deployment.NewDeliveryPlan(richPlanInput)
	if err != nil {
		t.Fatal(err)
	}
	planDocument, err := json.Marshal(richPlan)
	if err != nil {
		t.Fatal(err)
	}
	planDigest := richPlan.Digest
	plan, err := delivery.CreatePlan(t.Context(), deploymentpostgres.PlanInput{PlanID: planID, TargetID: targetID, PlanRevision: 1, PlanDigest: planDigest, CompiledGraphDigest: compiledGraphDigest, CompiledConfigDigest: compiledConfigDigest, SecurityDomainFingerprint: securityDigest, ArtifactDigest: artifactDigest, QualificationDigest: qualificationDigest, ApprovalRequired: richPlan.Governance.RequiresApproval, ApprovalPolicyRevision: richPlan.Governance.ApprovalPolicyRevision, PlanDocument: planDocument, Evidence: json.RawMessage(`{"source":"concrete"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.CreateCandidate(t.Context(), deploymentpostgres.CandidateInput{CandidateID: candidateID, TargetID: targetID, PlanID: planID, CandidateRevision: 1, ArtifactDigest: artifactDigest}); err != nil {
		t.Fatal(err)
	}
	attempt, err := delivery.BeginBuildAttempt(t.Context(), deploymentpostgres.BuildAttemptInput{AttemptID: attemptID, PlanID: planID, CandidateID: candidateID, OwnerID: "builder-concrete", PhysicalPoolID: poolID, FencingEpoch: 1, RequestDigest: digest('f'), PlanDigest: planDigest, Namespace: "candidate/concrete", SessionIdentity: "session-concrete", LeaseExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.BindBuildArtifact(t.Context(), deploymentpostgres.BuildArtifactBindingInput{AttemptID: attemptID, ServingArtifactID: "artifact-concrete", ServingArtifactDigest: artifactDigest, ServingStateID: generationID, OwnerID: attempt.OwnerID, FencingEpoch: attempt.FencingEpoch}); err != nil {
		t.Fatal(err)
	}
	markerJSON, err := (ducklake.CommitMarker{
		SchemaVersion: ducklake.CommitMarkerSchemaVersion, DeliveryID: targetID, GenerationID: generationID,
		AttemptID: attemptID, LeaseEpoch: 1, RequestDigest: digest('f'), PlanDigest: planDigest,
		Project: "project_concrete", Environment: "prod", PhysicalPoolID: poolID,
	}).CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	marker := json.RawMessage(markerJSON)
	if _, err := delivery.CommitBuildAttempt(t.Context(), deploymentpostgres.CommitAttemptInput{AttemptID: attemptID, OwnerID: "builder-concrete", FencingEpoch: 1, SnapshotID: 777, CommitMarker: marker}); err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.CreateSnapshotSeal(t.Context(), deploymentpostgres.SnapshotSealInput{SealID: sealID, AttemptID: attemptID, CandidateID: candidateID, PhysicalPoolID: poolID, TenantDomain: "tenant-concrete", Region: "us-east", EncryptionDomain: "enc-concrete", ObjectNamespace: "objects/concrete", CatalogDatabase: catalogDB, CatalogID: "catalog-concrete", CatalogUUID: catalogUUID, CatalogVersion: 1, DuckLakeSnapshotID: 777, RelationNamespace: "candidate/concrete", RelationManifestDigest: digest('1'), ClosureDigest: digest('8'), ObjectRoot: "objects/concrete/777", ObjectRootDigest: digest('6'), ArtifactRoot: "artifacts/concrete", ArtifactRootDigest: digest('7'), CompiledGraphDigest: compiledGraphDigest, CompiledConfigDigest: compiledConfigDigest, SecurityDomainFingerprint: securityDigest, RequestDigest: digest('f'), PlanDigest: planDigest, CompatibilityDigest: admission.CompatibilityDigest, ServingArtifactID: "artifact-concrete", ServingArtifactDigest: artifactDigest, DuckDBVersion: "1", RuntimeVersion: "runtime-v1", DuckLakeExtensionVersion: "1", DuckLakeSpecVersion: "1", CatalogSchemaVersion: "1", QualificationEvidence: json.RawMessage(`{"checks":["schema"]}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.QualifyCandidate(t.Context(), candidateID, sealID, digest('3')); err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.CreateGeneration(t.Context(), deploymentpostgres.GenerationInput{GenerationID: generationID, TargetID: targetID, CandidateID: candidateID, SnapshotSealID: sealID, PlanID: planID, PlanDigest: planDigest, ArtifactRoot: "artifacts/concrete", ArtifactRootDigest: digest('7'), ServingArtifactDigest: artifactDigest, CompiledGraphDigest: compiledGraphDigest, CompiledConfigDigest: compiledConfigDigest, SecurityDomainFingerprint: securityDigest, GenerationRevision: 1}); err != nil {
		t.Fatal(err)
	}
	return plan, generationID, poolID, catalogDB, catalogUUID
}

func TestPostgresConcreteVerifierAndAuditUseExactEvidence(t *testing.T) {
	db := concreteModulePostgresDB(t)
	digest := func(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }
	basePlan := projectpipelineplan.Plan{ID: "pipeline-plan-concrete", PipelineID: "pipeline_concrete", ProjectID: "project_concrete", Environment: "prod", SemanticModelID: "semantic_concrete", ServingGenerationID: "0198f2c0-7c7a-7f00-8a11-000000000105", ArtifactDigest: digest('e'), SelectionDigest: digest('f'), MaterializationScope: []string{"model_concrete"}, ModelExecutionOrder: []string{"model_concrete"}, QualificationChecks: []string{"compatibility"}, InvocationSource: "manual"}
	plan, err := projectpipelineplan.New(basePlan)
	if err != nil {
		t.Fatal(err)
	}
	deliveryPlan, generationID, poolID, _, _ := seedConcreteDelivery(t, db, plan)
	delivery := deploymentpostgres.NewWithActivationAudit(db, deploymentaudit.NewWithRepository(accesspostgres.New()))
	basePublicationID := "0198f2c0-7c7a-7f00-8a11-000000000106"
	basePublication, err := delivery.CreatePublication(t.Context(), deploymentpostgres.PublicationInput{PublicationID: basePublicationID, TargetID: "target_concrete_prod", GenerationID: generationID, CandidateID: "0198f2c0-7c7a-7f00-8a11-000000000102", SnapshotSealID: "0198f2c0-7c7a-7f00-8a11-000000000104", ExpectedTargetRevision: 1, ActorID: "operator-concrete", RequestDigest: digest('8')})
	if err != nil {
		t.Fatal(err)
	}
	baseLease, err := delivery.AcquireLease(t.Context(), deploymentpostgres.LeaseInput{LeaseID: "0198f2c0-7c7a-7f00-8a11-000000000107", TargetID: "target_concrete_prod", OwnerID: "operator-concrete", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.Activate(t.Context(), deploymentpostgres.ActivationInput{PublicationID: basePublication.PublicationID, TargetID: basePublication.TargetID, GenerationID: generationID, ExpectedTargetRevision: 1, RequestDigest: basePublication.RequestDigest, ActorID: basePublication.ActorID, LeaseID: baseLease.LeaseID, OwnerID: baseLease.OwnerID, FencingEpoch: baseLease.FencingEpoch, CorrelationID: "0198f2c0-7c7a-7f00-8a11-000000000110"}); err != nil {
		t.Fatal(err)
	}
	resultGenerationID := "0198f2c0-7c7a-7f00-8a11-000000000111"
	if _, err := delivery.CreateGeneration(t.Context(), deploymentpostgres.GenerationInput{GenerationID: resultGenerationID, TargetID: "target_concrete_prod", CandidateID: "0198f2c0-7c7a-7f00-8a11-000000000102", SnapshotSealID: "0198f2c0-7c7a-7f00-8a11-000000000104", PlanID: deliveryPlan.PlanID, PlanDigest: deliveryPlan.PlanDigest, ArtifactRoot: "artifacts/concrete", ArtifactRootDigest: digest('7'), ServingArtifactDigest: deliveryPlan.ArtifactDigest, CompiledGraphDigest: deliveryPlan.CompiledGraphDigest, CompiledConfigDigest: deliveryPlan.CompiledConfigDigest, SecurityDomainFingerprint: deliveryPlan.SecurityDomainFingerprint, GenerationRevision: 2}); err != nil {
		t.Fatal(err)
	}
	resultPublication, err := delivery.CreatePublication(t.Context(), deploymentpostgres.PublicationInput{PublicationID: "0198f2c0-7c7a-7f00-8a11-000000000112", TargetID: "target_concrete_prod", GenerationID: resultGenerationID, CandidateID: "0198f2c0-7c7a-7f00-8a11-000000000102", SnapshotSealID: "0198f2c0-7c7a-7f00-8a11-000000000104", ExpectedBaseGenerationID: generationID, ExpectedTargetRevision: 2, ActorID: "operator-concrete-2", RequestDigest: digest('9')})
	if err != nil {
		t.Fatal(err)
	}
	resultLease, err := delivery.AcquireLease(t.Context(), deploymentpostgres.LeaseInput{LeaseID: "0198f2c0-7c7a-7f00-8a11-000000000113", TargetID: "target_concrete_prod", OwnerID: "operator-concrete-2", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.Activate(t.Context(), deploymentpostgres.ActivationInput{PublicationID: resultPublication.PublicationID, TargetID: resultPublication.TargetID, GenerationID: resultGenerationID, ExpectedTargetRevision: 2, RequestDigest: resultPublication.RequestDigest, ActorID: resultPublication.ActorID, LeaseID: resultLease.LeaseID, OwnerID: resultLease.OwnerID, FencingEpoch: resultLease.FencingEpoch, CorrelationID: "0198f2c0-7c7a-7f00-8a11-000000000114"}); err != nil {
		t.Fatal(err)
	}
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := jobspostgres.New(db)
	identity := projectgraph.ServingIdentity{ProjectID: "project_concrete", Environment: "prod", GenerationID: generationID}
	if plan.ID == deliveryPlan.PlanID || plan.Digest == deliveryPlan.PlanDigest || plan.ServingGenerationID != generationID {
		t.Fatalf("pipeline and delivery identities were not kept distinct: pipeline=%#v delivery=%#v", plan, deliveryPlan)
	}
	richDeliveryPlan, err := deliveryPlan.RichPlan()
	if err != nil || richDeliveryPlan.PipelinePlan == nil || richDeliveryPlan.PipelinePlan.ID != plan.ID || richDeliveryPlan.PipelinePlan.Digest != plan.Digest {
		t.Fatalf("embedded pipeline plan was not retained exactly: plan=%#v err=%v", richDeliveryPlan.PipelinePlan, err)
	}
	queue := NewPostgresJobsAdapter(jobsRepo, refreshRepo)
	verifier, err := refreshcomposition.NewPostgresCanonicalVerifierAdapter(delivery, "target_concrete_prod")
	if err != nil {
		t.Fatal(err)
	}
	persistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{PublicationIdentityResolver: verifier, SchedulerOwner: "scheduler-concrete", Jobs: queue, CanonicalVerifier: verifier, CancelAuditWriter: integrationAuditWriter{}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := createTreeRootE(t.Context(), persistence.Runs, refreshrun.RunInput{RunID: "concrete-refresh-run", Identity: identity, SemanticModelID: projectgraph.ResourceID(plan.SemanticModelID), PipelineID: projectgraph.ResourceID(plan.PipelineID), PipelinePlan: &plan, InvocationSource: "manual", PrincipalID: "principal:concrete", EstimatedMemoryBytes: 1, TargetRevision: 2, TargetType: refreshrun.TargetRefreshPipeline, TargetID: projectgraph.ResourceID(plan.PipelineID), TriggerType: refreshrun.TriggerManual, JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: `{}`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := persistence.Runs.ListExecutableJobs(t.Context(), refreshrun.ReadScope{ProjectID: identity.ProjectID, Environment: identity.Environment}, 1)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%#v err=%v", candidates, err)
	}
	claimed, ok, err := persistence.Runs.ClaimExecutableJob(t.Context(), candidates[0], "worker-concrete", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if _, err := persistence.Runs.MarkRunPrepared(t.Context(), claimed); err != nil {
		t.Fatal(err)
	}
	mismatchedPersistence := &postgresPublicationPersistence{repository: refreshRepo, identityResolver: staticPublicationIdentityResolver(poolID, "catalog-config-mismatch"), canonicalVerifier: verifier, queueLifecycle: queue}
	if err := mismatchedPersistence.CompleteCanonicalRefresh(t.Context(), claimed, refreshrun.CanonicalRefreshResult{PlanID: deliveryPlan.PlanID, ServingStateID: resultGenerationID, SnapshotID: 777}); !errors.Is(err, ErrPublicationIdentityMismatch) || !errors.Is(err, refreshpostgres.ErrConflict) {
		t.Fatalf("canonical completion identity mismatch = %v, want typed conflict", err)
	}
	badPayload := claimed
	badPlanInput := *claimed.PipelinePlan
	badPlanInput.SelectionDigest = digest('a')
	badPlanInput.ExecutionDigest = ""
	badPlanInput.ProvenanceDigest = ""
	badPlanInput.GovernanceDigest = ""
	badPlanInput.EvidenceDigest = ""
	badPlanInput.Digest = ""
	badPlan, err := projectpipelineplan.New(badPlanInput)
	if err != nil {
		t.Fatal(err)
	}
	badPayload.PipelinePlan = &badPlan
	if tx, beginErr := db.Begin(t.Context()); beginErr != nil {
		t.Fatal(beginErr)
	} else {
		_, verifyErr := verifier.VerifyCanonicalRefreshTx(t.Context(), tx, badPayload, refreshrun.CanonicalRefreshResult{PlanID: deliveryPlan.PlanID, ServingStateID: resultGenerationID, SnapshotID: 777})
		_ = tx.Rollback(t.Context())
		if !errors.Is(verifyErr, ErrPublicationIdentityMismatch) || !errors.Is(verifyErr, refreshpostgres.ErrConflict) {
			t.Fatalf("embedded pipeline digest mismatch = %v, want typed conflict", verifyErr)
		}
	}
	publication, ok := persistence.Publication.(refreshrun.CanonicalPublicationUnitOfWork)
	if !ok {
		t.Fatal("canonical publication unit missing")
	}
	if err := publication.CompleteCanonicalRefresh(t.Context(), claimed, refreshrun.CanonicalRefreshResult{PlanID: deliveryPlan.PlanID, ServingStateID: resultGenerationID, SnapshotID: 777}); err != nil {
		t.Fatal(err)
	}
	// A constructed-but-unpublished generation is not canonical evidence.
	uncommittedGenerationID := "0198f2c0-7c7a-7f00-8a11-000000000115"
	if _, err := delivery.CreateGeneration(t.Context(), deploymentpostgres.GenerationInput{GenerationID: uncommittedGenerationID, TargetID: "target_concrete_prod", CandidateID: "0198f2c0-7c7a-7f00-8a11-000000000102", SnapshotSealID: "0198f2c0-7c7a-7f00-8a11-000000000104", PlanID: deliveryPlan.PlanID, PlanDigest: deliveryPlan.PlanDigest, ArtifactRoot: "artifacts/concrete", ArtifactRootDigest: digest('7'), ServingArtifactDigest: deliveryPlan.ArtifactDigest, CompiledGraphDigest: deliveryPlan.CompiledGraphDigest, CompiledConfigDigest: deliveryPlan.CompiledConfigDigest, SecurityDomainFingerprint: deliveryPlan.SecurityDomainFingerprint, GenerationRevision: 3}); err != nil {
		t.Fatal(err)
	}
	if tx, err := db.Begin(t.Context()); err != nil {
		t.Fatal(err)
	} else {
		if _, verifyErr := verifier.VerifyCanonicalRefreshTx(t.Context(), tx, claimed, refreshrun.CanonicalRefreshResult{PlanID: deliveryPlan.PlanID, ServingStateID: uncommittedGenerationID, SnapshotID: 777}); verifyErr == nil {
			t.Fatal("uncommitted delivery generation unexpectedly verified")
		}
		_ = tx.Rollback(t.Context())
	}
	// Activate a successor, then prove the previous result is no longer current.
	if _, err := delivery.CreatePublication(t.Context(), deploymentpostgres.PublicationInput{PublicationID: "0198f2c0-7c7a-7f00-8a11-000000000116", TargetID: "target_concrete_prod", GenerationID: uncommittedGenerationID, CandidateID: "0198f2c0-7c7a-7f00-8a11-000000000102", SnapshotSealID: "0198f2c0-7c7a-7f00-8a11-000000000104", ExpectedBaseGenerationID: resultGenerationID, ExpectedTargetRevision: 3, ActorID: "operator-concrete-3", RequestDigest: digest('0')}); err != nil {
		t.Fatal(err)
	}
	lease3, err := delivery.AcquireLease(t.Context(), deploymentpostgres.LeaseInput{LeaseID: "0198f2c0-7c7a-7f00-8a11-000000000117", TargetID: "target_concrete_prod", OwnerID: "operator-concrete-3", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.Activate(t.Context(), deploymentpostgres.ActivationInput{PublicationID: "0198f2c0-7c7a-7f00-8a11-000000000116", TargetID: "target_concrete_prod", GenerationID: uncommittedGenerationID, ExpectedTargetRevision: 3, RequestDigest: digest('0'), ActorID: "operator-concrete-3", LeaseID: lease3.LeaseID, OwnerID: lease3.OwnerID, FencingEpoch: lease3.FencingEpoch, CorrelationID: "0198f2c0-7c7a-7f00-8a11-000000000118"}); err != nil {
		t.Fatal(err)
	}
	if tx, err := db.Begin(t.Context()); err != nil {
		t.Fatal(err)
	} else {
		if _, verifyErr := verifier.VerifyCanonicalRefreshTx(t.Context(), tx, claimed, refreshrun.CanonicalRefreshResult{PlanID: deliveryPlan.PlanID, ServingStateID: resultGenerationID, SnapshotID: 777}); verifyErr == nil {
			t.Fatal("superseded/non-current delivery generation unexpectedly verified")
		}
		_ = tx.Rollback(t.Context())
	}
	// Completion is an exact replay once publication, data-version, run-tree,
	// and queue evidence are committed.  It remains replayable even after a
	// later deployment advances the active target pointer.
	if err := publication.CompleteCanonicalRefresh(t.Context(), claimed, refreshrun.CanonicalRefreshResult{PlanID: deliveryPlan.PlanID, ServingStateID: resultGenerationID, SnapshotID: 777}); err != nil {
		t.Fatalf("canonical completion replay after later activation: %v", err)
	}
	if err := publication.CompleteCanonicalRefresh(t.Context(), claimed, refreshrun.CanonicalRefreshResult{PlanID: deliveryPlan.PlanID, ServingStateID: resultGenerationID, SnapshotID: 778}); !errors.Is(err, refreshpostgres.ErrConflict) {
		t.Fatalf("snapshot-mismatched canonical replay error = %v, want conflict", err)
	}
	finalRun, err := refreshRepo.LookupRun(t.Context(), run.ID)
	if err != nil || finalRun.Status != refreshrun.RunStatusSucceeded {
		t.Fatalf("final run=%#v err=%v", finalRun, err)
	}
	var resultRevision int64
	if err := db.QueryRow(t.Context(), `SELECT target_revision FROM refresh.data_version WHERE project_id=$1 AND environment=$2 AND semantic_model_id=$3 AND generation_id=$4`, identity.ProjectID.String(), identity.Environment, plan.SemanticModelID, resultGenerationID).Scan(&resultRevision); err != nil {
		t.Fatal(err)
	}
	if resultRevision != 3 {
		t.Fatalf("data-version target revision=%d, want committed result revision 3", resultRevision)
	}
}

func TestPostgresConcreteCancelAuditWriterCallerTransaction(t *testing.T) {
	db := accessOnlyPostgresDB(t)
	writer, err := refreshcomposition.NewPostgresCancelAuditWriterAdapter(accesspostgres.New())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	intent := access.AuditIntent{EventID: "0198f2c0-7c7a-7f00-8a11-000000000109", Source: "refresh", Operation: "cancel", Action: "refresh.cancel", ResourceKind: "refresh_run", ResourceID: "run-audit-concrete", Capability: access.CapabilityResourceUse, Outcome: "failure", AggregateKey: "refresh_run:run-audit-concrete", AggregateSequence: 1, MetadataJSON: `{}`}
	if err := writer.RecordRefreshCancelAuditTx(t.Context(), tx, intent); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var eventID string
	if err := db.QueryRow(t.Context(), `SELECT audit_id::text FROM audit.audit_event WHERE audit_id=$1::uuid`, intent.EventID).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	if eventID != intent.EventID {
		t.Fatalf("stored audit id=%q", eventID)
	}
}

func TestPostgresKeyedRefreshAdmissionReplayConflictsAndAtomicRollback(t *testing.T) {
	db := concreteModulePostgresDB(t)
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := jobspostgres.New(db)
	queue := NewPostgresJobsAdapter(jobsRepo, refreshRepo)
	auditWriter, err := refreshcomposition.NewPostgresCancelAuditWriterAdapter(accesspostgres.New())
	if err != nil {
		t.Fatal(err)
	}
	platformOperations := operationpostgres.New(db)
	operationAuthority, err := refreshcomposition.NewPostgresOperationAuthorityAdapter(platformOperations)
	if err != nil {
		t.Fatal(err)
	}
	persistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{
		PublicationIdentityResolver: staticPublicationIdentityResolver("pool-keyed", "catalog-keyed"),
		SchedulerOwner:              "scheduler-keyed",
		Jobs:                        queue,
		CanonicalVerifier:           integrationCanonicalVerifier{physicalPoolID: "pool-keyed", catalogID: "catalog-keyed"},
		Operations:                  operationAuthority,
		CancelAuditWriter:           integrationAuditWriter{},
		CreateAuditWriter:           auditWriter,
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := func(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }
	identity := projectgraph.ServingIdentity{ProjectID: "project-keyed", Environment: "prod", GenerationID: "generation-keyed"}
	plan, err := deployment.NewPipelinePlan(deployment.PipelinePlan{
		ID: "pipeline-plan-keyed", PipelineID: "pipeline-keyed", ProjectID: identity.ProjectID.String(), Environment: identity.Environment,
		SemanticModelID: "semantic-keyed", ServingGenerationID: identity.GenerationID,
		ArtifactDigest: digest('a'), SelectionDigest: digest('b'), MaterializationScope: []string{"model-keyed"}, InvocationSource: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	audit := &access.AuditIntent{
		EventID: digest('1'), Source: "refresh", Operation: "create_refresh_run", PrincipalID: "",
		Action: "refresh.queued", ResourceKind: "project", ResourceID: identity.ProjectID.String(),
		Capability: access.CapabilityResourceUse, Outcome: "success", RequestID: "request-keyed", CorrelationID: "correlation-keyed",
		AggregateKey: "project:" + identity.ProjectID.String(), MetadataJSON: `{}`,
	}
	root := refreshrun.RunInput{
		RunID: "run-keyed", Identity: identity, SemanticModelID: "semantic-keyed", PipelineID: "pipeline-keyed", PipelinePlan: &plan,
		InvocationSource: "manual", PrincipalID: "principal:keyed", EstimatedMemoryBytes: 1,
		TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline-keyed", TriggerType: refreshrun.TriggerManual,
		JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: `{}`, AuditIntent: audit,
	}
	tree := refreshrun.RunTreeInput{Root: root, DependencyTargets: []projectgraph.ResourceID{"model-keyed"}, IdempotencyKey: "keyed-refresh", RequestDigest: digest('c')}
	first, firstChildren, err := persistence.Runs.CreateRunTree(t.Context(), tree)
	if err != nil {
		t.Fatalf("first keyed admission: %v", err)
	}
	if first.ID != root.RunID || len(firstChildren) != 1 {
		t.Fatalf("first tree root/children = %q/%d, want %q/1", first.ID, len(firstChildren), root.RunID)
	}
	replay, replayChildren, err := persistence.Runs.CreateRunTree(t.Context(), tree)
	if err != nil {
		t.Fatalf("keyed replay: %v", err)
	}
	if replay.ID != first.ID || len(replayChildren) != len(firstChildren) || replayChildren[0].ID != firstChildren[0].ID {
		t.Fatalf("replay tree = %q/%v, want root/child %q/%q", replay.ID, replayChildren, first.ID, firstChildren[0].ID)
	}
	assertKeyedRefreshAdmissionCounts(t, db, jobsRepo, identity.ProjectID.String(), first.ID, tree.IdempotencyKey, 2, 1, 1, 1, 1)

	conflict := tree
	conflict.RequestDigest = digest('d')
	if _, _, err := persistence.Runs.CreateRunTree(t.Context(), conflict); !errors.Is(err, refreshpostgres.ErrConflict) {
		t.Fatalf("same key with changed digest error = %v, want conflict", err)
	}
	assertKeyedRefreshAdmissionCounts(t, db, jobsRepo, identity.ProjectID.String(), first.ID, tree.IdempotencyKey, 2, 1, 1, 1, 1)

	rollbackIdentity := projectgraph.ServingIdentity{ProjectID: "project-keyed-rollback", Environment: "prod", GenerationID: "generation-keyed-rollback"}
	rollbackPlan, err := deployment.NewPipelinePlan(deployment.PipelinePlan{
		ID: "pipeline-plan-keyed-rollback", PipelineID: "pipeline-keyed-rollback", ProjectID: rollbackIdentity.ProjectID.String(), Environment: rollbackIdentity.Environment,
		SemanticModelID: "semantic-keyed-rollback", ServingGenerationID: rollbackIdentity.GenerationID,
		ArtifactDigest: digest('e'), SelectionDigest: digest('f'), MaterializationScope: []string{"model-keyed-rollback"}, InvocationSource: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	badAudit := *audit
	badAudit.EventID = "not-a-uuid"
	rollbackRoot := refreshrun.RunInput{
		RunID: "run-keyed-rollback", Identity: rollbackIdentity, SemanticModelID: "semantic-keyed-rollback", PipelineID: "pipeline-keyed-rollback", PipelinePlan: &rollbackPlan,
		InvocationSource: "manual", PrincipalID: "principal:keyed-rollback", EstimatedMemoryBytes: 1,
		TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline-keyed-rollback", TriggerType: refreshrun.TriggerManual,
		JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: `{}`, AuditIntent: &badAudit,
	}
	rollbackTree := refreshrun.RunTreeInput{Root: rollbackRoot, IdempotencyKey: "keyed-refresh-rollback", RequestDigest: digest('0')}
	if _, _, err := persistence.Runs.CreateRunTree(t.Context(), rollbackTree); err == nil {
		t.Fatal("audit callback failure unexpectedly committed keyed admission")
	}
	assertKeyedRefreshAdmissionCounts(t, db, jobsRepo, rollbackIdentity.ProjectID.String(), rollbackRoot.RunID, rollbackTree.IdempotencyKey, 0, 0, 0, 0, 0)
	retryAudit := *audit
	retryAudit.EventID = digest('2')
	retryAudit.ResourceID = rollbackIdentity.ProjectID.String()
	retryAudit.AggregateKey = "project:" + rollbackIdentity.ProjectID.String()
	rollbackRoot.AuditIntent = &retryAudit
	rollbackTree.Root = rollbackRoot
	retried, _, err := persistence.Runs.CreateRunTree(t.Context(), rollbackTree)
	if err != nil || retried.ID != rollbackRoot.RunID {
		t.Fatalf("retry after callback rollback root=%#v err=%v", retried, err)
	}
	assertKeyedRefreshAdmissionCounts(t, db, jobsRepo, rollbackIdentity.ProjectID.String(), rollbackRoot.RunID, rollbackTree.IdempotencyKey, 1, 1, 1, 1, 1)
}

func TestPostgresKeyedRefreshCancellationReplayConflictsAndRollback(t *testing.T) {
	db := modulePostgresTestDB(t)
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := jobspostgres.New(db)
	queue := NewPostgresJobsAdapter(jobsRepo, refreshRepo)
	platformOperations := operationpostgres.New(db)
	operationAuthority, err := refreshcomposition.NewPostgresOperationAuthorityAdapter(platformOperations)
	if err != nil {
		t.Fatal(err)
	}
	auditWriter := &recordingCancelAuditWriter{}
	identity := projectgraph.ServingIdentity{ProjectID: "project-cancel-keyed", Environment: "prod", GenerationID: "generation-cancel-keyed"}
	plan, err := deployment.NewPipelinePlan(deployment.PipelinePlan{
		ID: "pipeline-plan-cancel-keyed", PipelineID: "pipeline-cancel-keyed", ProjectID: identity.ProjectID.String(), Environment: identity.Environment,
		SemanticModelID: "semantic-cancel-keyed", ServingGenerationID: identity.GenerationID,
		ArtifactDigest: "sha256:" + strings.Repeat("a", 64), SelectionDigest: "sha256:" + strings.Repeat("b", 64), MaterializationScope: []string{"model-cancel-keyed"}, InvocationSource: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	create := func(runID string, audit PostgresCancelAuditWriter) (refreshrun.RunRecord, *postgresRunPersistence) {
		configured, createErr := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{
			PublicationIdentityResolver: staticPublicationIdentityResolver("pool-cancel-keyed", "catalog-cancel-keyed"), SchedulerOwner: "scheduler-cancel-keyed",
			Jobs: queue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool-cancel-keyed", catalogID: "catalog-cancel-keyed"}, Operations: operationAuthority, CancelAuditWriter: audit,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		root, createErr := createTreeRootE(t.Context(), configured.Runs, refreshrun.RunInput{
			RunID: runID, Identity: identity, SemanticModelID: "semantic-cancel-keyed", PipelineID: "pipeline-cancel-keyed", PipelinePlan: &plan,
			InvocationSource: "manual", PrincipalID: "principal:cancel-keyed", EstimatedMemoryBytes: 1,
			TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline-cancel-keyed", TriggerType: refreshrun.TriggerManual, JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: `{}`,
		}, nil)
		if createErr != nil {
			t.Fatal(createErr)
		}
		return root, configured.Runs.(*postgresRunPersistence)
	}
	run, runs := create("run-cancel-keyed", auditWriter)
	key := "cancel-keyed"
	digest, err := refreshrun.CancelRequestDigest(identity, "operator:cancel-keyed", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	keyed := runs
	cancelIntent := &access.AuditIntent{EventID: "event-cancel-keyed", ResourceID: run.ID, Operation: "cancel_refresh_run", Outcome: "success"}
	first, replayed, err := keyed.CancelRunWithAuditKeyed(t.Context(), identity, run.ID, "operator:cancel-keyed", key, digest, cancelIntent)
	if err != nil {
		t.Fatalf("first keyed cancellation: %v", err)
	}
	if replayed || first.Status != refreshrun.RunStatusCancelled || auditWriter.calls != 1 {
		t.Fatalf("first cancellation status/audit=%q/%d, want cancelled/1", first.Status, auditWriter.calls)
	}
	durableRun, err := refreshRepo.LookupRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	job, err := jobsRepo.Get(t.Context(), durableRun.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != jobs.StatusCancelled {
		t.Fatalf("cancelled job status=%q, want cancelled", job.Status)
	}
	replay, replayed, err := keyed.CancelRunWithAuditKeyed(t.Context(), identity, run.ID, "operator:cancel-keyed", key, digest, nil)
	if err != nil {
		t.Fatalf("keyed cancellation replay: %v", err)
	}
	if !replayed || replay.ID != first.ID || replay.Status != first.Status || auditWriter.calls != 1 {
		t.Fatalf("replay id/status/audit=%q/%q/%d, want %q/%q/1", replay.ID, replay.Status, auditWriter.calls, first.ID, first.Status)
	}
	changedDigest := digest[:len(digest)-1] + "0"
	if _, _, err := keyed.CancelRunWithAuditKeyed(t.Context(), identity, run.ID, "operator:cancel-keyed", key, changedDigest, nil); !errors.Is(err, refreshpostgres.ErrConflict) {
		t.Fatalf("changed digest error=%v, want conflict", err)
	}

	// A different operation type occupying the same scope/key is a durable
	// conflict and must not reach the run/job mutation.
	typeConflictKey := "cancel-type-conflict"
	typeConflictDigest := digest
	if _, err := platformOperations.Acquire(t.Context(), operationpostgres.AcquireInput{Scope: refreshOperationScope(identity.ProjectID.String(), identity.Environment), OperationType: "other_operation", IdempotencyKey: typeConflictKey, RequestDigest: typeConflictDigest, OwnerID: "operator:cancel-keyed", Lease: time.Minute, Retention: 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := keyed.CancelRunWithAuditKeyed(t.Context(), identity, run.ID, "operator:cancel-keyed", typeConflictKey, typeConflictDigest, nil); !errors.Is(err, refreshpostgres.ErrConflict) {
		t.Fatalf("changed operation type error=%v, want conflict", err)
	}

	// Audit failure rolls back operation, run, and canonical job together.
	rollbackRun, rollbackRuns := create("run-cancel-keyed-rollback", integrationAuditWriter{fail: true})
	rollbackKey := "cancel-keyed-rollback"
	rollbackDigest, err := refreshrun.CancelRequestDigest(identity, "operator:cancel-keyed", rollbackRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := rollbackRuns.CancelRunWithAuditKeyed(t.Context(), identity, rollbackRun.ID, "operator:cancel-keyed", rollbackKey, rollbackDigest, &access.AuditIntent{EventID: "event-cancel-keyed-rollback", ResourceID: rollbackRun.ID}); err == nil {
		t.Fatal("cancellation audit failure unexpectedly committed")
	}
	unchanged, err := refreshRepo.LookupRun(t.Context(), rollbackRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Status != refreshrun.RunStatusQueued {
		t.Fatalf("rollback run status=%q, want queued", unchanged.Status)
	}
	rollbackDurable, err := refreshRepo.LookupRun(t.Context(), rollbackRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	rollbackJob, err := jobsRepo.Get(t.Context(), rollbackDurable.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if rollbackJob.Status != jobs.StatusQueued {
		t.Fatalf("rollback job status=%q, want queued", rollbackJob.Status)
	}
	var operationCount int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM platform.operation WHERE scope_id=$1 AND idempotency_key=$2`, refreshOperationScope(identity.ProjectID.String(), identity.Environment), rollbackKey).Scan(&operationCount); err != nil {
		t.Fatal(err)
	}
	if operationCount != 0 {
		t.Fatalf("rollback operation rows=%d, want 0", operationCount)
	}
}

func assertKeyedRefreshAdmissionCounts(t *testing.T, db *pgxpool.Pool, jobsRepo *jobspostgres.Repository, projectID, runID, operationKey string, wantRuns, wantJobs, wantAudit, wantEvents, wantOperations int) {
	t.Helper()
	var runs, jobs, audits, operations int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM refresh.run WHERE project_id=$1 AND environment='prod'`, projectID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM jobs.job WHERE resource_kind='refresh_run' AND resource_id=$1`, runID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE resource_id=$1 AND operation='create_refresh_run'`, projectID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM platform.operation WHERE scope_id=$1 AND idempotency_key=$2`, refreshOperationScope(projectID, "prod"), operationKey).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	events, err := jobsRepo.ListEvents(t.Context(), "refresh", runID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	queued := 0
	for _, event := range events {
		if event.EventType == "refresh.queued" {
			queued++
		}
	}
	if runs != wantRuns || jobs != wantJobs || audits != wantAudit || queued != wantEvents || operations != wantOperations {
		t.Fatalf("keyed admission counts runs/jobs/audit/events/operations=%d/%d/%d/%d/%d, want %d/%d/%d/%d/%d", runs, jobs, audits, queued, operations, wantRuns, wantJobs, wantAudit, wantEvents, wantOperations)
	}
}

func TestPostgresPublicationIdentityUnavailableDoesNotWriteDataVersion(t *testing.T) {
	db := modulePostgresTestDB(t)
	refreshRepo := refreshpostgres.New(db)
	queue := NewPostgresJobsAdapter(jobspostgres.New(db), refreshRepo)
	persistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{
		PublicationIdentityResolver: unavailablePublicationIdentityResolver(), SchedulerOwner: "scheduler-identity-unavailable",
		Jobs: queue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool-unused", catalogID: "catalog-unused"}, CancelAuditWriter: integrationAuditWriter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := projectgraph.ServingIdentity{ProjectID: "project_identity_unavailable", Environment: "prod", GenerationID: "generation_identity_unavailable"}
	err = persistence.Schedules.SaveDataVersion(t.Context(), refreshschedule.DataVersion{
		Identity: identity, SemanticModelID: "semantic_identity_unavailable", SnapshotID: 41,
		Source: refreshschedule.DataVersionSourceRefresh, PipelineID: "pipeline_identity_unavailable", RunID: "run_identity_unavailable",
		TargetRevision: 1, LeaseOwner: "worker", LeaseRevision: 1,
	})
	if !errors.Is(err, ErrPublicationIdentityUnavailable) {
		t.Fatalf("SaveDataVersion error=%v, want ErrPublicationIdentityUnavailable", err)
	}
	var count int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM refresh.data_version WHERE project_id=$1`, identity.ProjectID.String()).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("data-version rows=%d after unavailable identity, want 0", count)
	}
}

func TestPostgresPublishedDataVersionWithoutRunIDFailsClosed(t *testing.T) {
	db := modulePostgresTestDB(t)
	refreshRepo := refreshpostgres.New(db)
	queue := NewPostgresJobsAdapter(jobspostgres.New(db), refreshRepo)
	persistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{
		PublicationIdentityResolver: staticPublicationIdentityResolver("pool-publish-provenance", "catalog-publish-provenance"), SchedulerOwner: "scheduler-publish-provenance",
		Jobs: queue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool-publish-provenance", catalogID: "catalog-publish-provenance"}, CancelAuditWriter: integrationAuditWriter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = persistence.Schedules.SaveDataVersion(t.Context(), refreshschedule.DataVersion{
		Identity: projectgraph.ServingIdentity{ProjectID: "project_publish_provenance", Environment: "prod", GenerationID: "generation_publish_provenance"}, SemanticModelID: "semantic_publish_provenance", SnapshotID: 41,
		Source: refreshschedule.DataVersionSourcePublish,
	})
	if err == nil || !strings.Contains(err.Error(), "deployment publication provenance") {
		t.Fatalf("SaveDataVersion error=%v, want publication provenance failure", err)
	}
}

func TestPostgresRefreshCandidatesPaginatePastOtherScopes(t *testing.T) {
	db := modulePostgresTestDB(t)
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := jobspostgres.New(db)
	queue := NewPostgresJobsAdapter(jobsRepo, refreshRepo)
	persistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{PublicationIdentityResolver: staticPublicationIdentityResolver("pool-scope", "catalog-scope"), SchedulerOwner: "scheduler-scope", Jobs: queue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool-scope", catalogID: "catalog-scope"}, CancelAuditWriter: integrationAuditWriter{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO jobs.job(id,kind,workload_class,principal_id,group_ids,partition_key,resource_kind,resource_id,estimated_memory_bytes,payload,request_digest) SELECT 'other-scope-job-'||to_char(i,'FM000'), 'refresh_pipeline', 'background', 'principal-other-'||to_char(i,'FM000'), '{}'::text[], 'refresh:other:prod', 'refresh_run', 'other-scope-run-'||to_char(i,'FM000'), 1, '{}'::jsonb, 'sha256:'||repeat('a',64) FROM generate_series(0,$1) AS s(i)`, refreshpostgres.MaxPageSize+24); err != nil {
		t.Fatal(err)
	}
	identity := projectgraph.ServingIdentity{ProjectID: "project_scope_target", Environment: "prod", GenerationID: "generation_scope_target"}
	plan := integrationPlan(t, identity, "daily")
	created, err := createTreeRootE(t.Context(), persistence.Runs, refreshrun.RunInput{RunID: "target-scope-run", Identity: identity, SemanticModelID: "semantic_sales", PipelineID: "pipeline_daily", PipelinePlan: &plan, InvocationSource: "schedule", MatchingScheduleIDs: []string{"daily"}, NominalTime: time.Now().UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), ConcurrencyPolicy: "Forbid", PrincipalID: "principal-target", EstimatedMemoryBytes: 1, TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline_daily", TriggerType: refreshrun.TriggerSchedule, JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: `{}`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := persistence.Runs.ListExecutableJobs(t.Context(), refreshrun.ReadScope{ProjectID: identity.ProjectID, Environment: identity.Environment}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].RunID != created.ID {
		t.Fatalf("scope candidates=%#v, want target run %q", candidates, created.ID)
	}
}

func TestPostgresRefreshCandidatesPreservePrincipalFIFOAcrossScopePages(t *testing.T) {
	db := modulePostgresTestDB(t)
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := jobspostgres.New(db)
	queue := NewPostgresJobsAdapter(jobsRepo, refreshRepo)
	persistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{PublicationIdentityResolver: staticPublicationIdentityResolver("pool-fifo", "catalog-fifo"), SchedulerOwner: "scheduler-fifo", Jobs: queue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool-fifo", catalogID: "catalog-fifo"}, CancelAuditWriter: integrationAuditWriter{}})
	if err != nil {
		t.Fatal(err)
	}
	oldIdentity := projectgraph.ServingIdentity{ProjectID: "project_fifo_old", Environment: "prod", GenerationID: "generation_fifo_old"}
	newIdentity := projectgraph.ServingIdentity{ProjectID: "project_fifo_target", Environment: "prod", GenerationID: "generation_fifo_target"}
	oldPlan := integrationPlan(t, oldIdentity, "daily")
	newPlan := integrationPlan(t, newIdentity, "daily")
	oldRun, err := createTreeRootE(t.Context(), persistence.Runs, refreshrun.RunInput{RunID: "fifo-old-head", Identity: oldIdentity, SemanticModelID: "semantic_sales", PipelineID: "pipeline_daily", PipelinePlan: &oldPlan, InvocationSource: "schedule", MatchingScheduleIDs: []string{"daily"}, NominalTime: time.Now().UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), ConcurrencyPolicy: "Forbid", PrincipalID: "principal:fifo-shared", EstimatedMemoryBytes: 1, TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline_daily", TriggerType: refreshrun.TriggerSchedule, JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: `{}`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := createTreeRootE(t.Context(), persistence.Runs, refreshrun.RunInput{RunID: "fifo-target-later", Identity: newIdentity, SemanticModelID: "semantic_sales", PipelineID: "pipeline_daily", PipelinePlan: &newPlan, InvocationSource: "schedule", MatchingScheduleIDs: []string{"daily"}, NominalTime: time.Now().UTC().Add(time.Second).Truncate(time.Microsecond).Format(time.RFC3339Nano), ConcurrencyPolicy: "Forbid", PrincipalID: "principal:fifo-shared", EstimatedMemoryBytes: 1, TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline_daily", TriggerType: refreshrun.TriggerSchedule, JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: `{}`}, nil); err != nil {
		t.Fatal(err)
	}
	oldStored, err := refreshRepo.LookupRun(t.Context(), oldRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	targetCandidates, err := persistence.Runs.ListExecutableJobs(t.Context(), refreshrun.ReadScope{ProjectID: newIdentity.ProjectID, Environment: newIdentity.Environment}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(targetCandidates) != 1 || targetCandidates[0].RunID != "fifo-target-later" {
		t.Fatalf("scope-partitioned principal fairness candidates=%#v (old head job=%s)", targetCandidates, oldStored.JobID)
	}
}

func TestPostgresPoisonPayloadTerminalizesRunAndJobAndUnblocksLaterJob(t *testing.T) {
	db := modulePostgresTestDB(t)
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := jobspostgres.New(db)
	queue := NewPostgresJobsAdapter(jobsRepo, refreshRepo)
	identity := projectgraph.ServingIdentity{ProjectID: "project-poison", Environment: "prod", GenerationID: "generation-poison"}
	plan, err := deployment.NewPipelinePlan(deployment.PipelinePlan{ID: "pipeline_poison", PipelineID: "pipeline_poison", ProjectID: identity.ProjectID.String(), Environment: identity.Environment, SemanticModelID: "semantic_poison", ServingGenerationID: identity.GenerationID, ArtifactDigest: "sha256:" + strings.Repeat("a", 64), SelectionDigest: "sha256:" + strings.Repeat("b", 64), MaterializationScope: []string{"model_poison"}, InvocationSource: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jobsRepo.Enqueue(t.Context(), jobs.EnqueueInput{ID: "poison-job", Kind: refreshrun.JobKindRefreshPipeline, WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: "principal-poison", GroupIDs: []string{}, PartitionKey: "refresh:" + identity.ProjectID.String() + ":" + identity.Environment, ResourceKind: refreshJobResourceKind, ResourceID: "poison-run", EstimatedMemoryBytes: 1, Payload: []byte(`{"not":"a refresh envelope"}`)}); err != nil {
		t.Fatal(err)
	}
	poisonInput := refreshpostgres.RunInput{RunID: "poison-run", ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID, PipelineID: plan.PipelineID, SemanticModelID: plan.SemanticModelID, TargetType: refreshrun.TargetRefreshPipeline, TargetID: plan.PipelineID, TriggerType: refreshrun.TriggerManual, InvocationSource: refreshrun.TriggerManual, PlanDigest: plan.Digest, ArtifactDigest: plan.ArtifactDigest, PrincipalID: "principal-poison", JobID: "poison-job"}
	if _, err := refreshRepo.CreateRun(t.Context(), poisonInput); err != nil {
		t.Fatal(err)
	}
	if _, err := refreshRepo.CreateRun(t.Context(), refreshpostgres.RunInput{RunID: "poison-child", ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID, ParentRunID: "poison-run", PipelineID: plan.PipelineID, SemanticModelID: plan.SemanticModelID, TargetType: refreshrun.TargetModelTable, TargetID: "model_poison", TriggerType: refreshrun.TriggerDependency, InvocationSource: refreshrun.TriggerDependency, PlanDigest: plan.Digest, ArtifactDigest: plan.ArtifactDigest, PrincipalID: "principal-poison"}); err != nil {
		t.Fatal(err)
	}
	first, err := queue.ListExecutableJobs(t.Context(), refreshrun.ReadScope{ProjectID: identity.ProjectID, Environment: identity.Environment}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 0 {
		t.Fatalf("poison candidate returned as executable: %#v", first)
	}
	if replay, replayErr := queue.ListExecutableJobs(t.Context(), refreshrun.ReadScope{ProjectID: identity.ProjectID, Environment: identity.Environment}, 8); replayErr != nil || len(replay) != 0 {
		t.Fatalf("poison quarantine replay candidates=%#v err=%v", replay, replayErr)
	}
	poisonJob, err := jobsRepo.Get(t.Context(), "poison-job")
	if err != nil {
		t.Fatal(err)
	}
	if poisonJob.Status != jobs.StatusCancelled || !strings.Contains(string(poisonJob.ErrorJSON), "REFRESH_POISON_PAYLOAD") {
		t.Fatalf("poison job=%#v, want terminal poison evidence", poisonJob)
	}
	poisonRun, err := refreshRepo.LookupRun(t.Context(), "poison-run")
	if err != nil {
		t.Fatal(err)
	}
	if poisonRun.Status != refreshrun.RunStatusFailed || poisonRun.Error != "refresh job payload rejected" {
		t.Fatalf("poison run=%#v, want failed safe evidence", poisonRun)
	}
	poisonChild, err := refreshRepo.LookupRun(t.Context(), "poison-child")
	if err != nil {
		t.Fatal(err)
	}
	if poisonChild.Status != refreshrun.RunStatusFailed || poisonChild.Error != "refresh job payload rejected" {
		t.Fatalf("poison child=%#v, want failed safe evidence", poisonChild)
	}
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	validInput := refreshrun.RunInput{RunID: "valid-after-poison", Identity: identity, SemanticModelID: projectgraph.ResourceID(plan.SemanticModelID), PipelineID: projectgraph.ResourceID(plan.PipelineID), PipelinePlan: &plan, InvocationSource: refreshrun.TriggerManual, PrincipalID: "principal-valid", EstimatedMemoryBytes: 1, TargetType: refreshrun.TargetRefreshPipeline, TargetID: projectgraph.ResourceID(plan.PipelineID), TriggerType: refreshrun.TriggerManual, JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: `{}`}
	validJobID, err := queue.EnqueueRefreshTx(t.Context(), tx, validInput, validInput.RunID)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	_, err = refreshRepo.CreateRunTx(t.Context(), tx, refreshpostgres.RunInput{RunID: validInput.RunID, ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID, PipelineID: plan.PipelineID, SemanticModelID: plan.SemanticModelID, TargetType: refreshrun.TargetRefreshPipeline, TargetID: plan.PipelineID, TriggerType: refreshrun.TriggerManual, InvocationSource: refreshrun.TriggerManual, PlanDigest: plan.Digest, ArtifactDigest: plan.ArtifactDigest, PrincipalID: "principal-valid", JobID: validJobID})
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	laterCandidates, err := queue.ListExecutableJobs(t.Context(), refreshrun.ReadScope{ProjectID: identity.ProjectID, Environment: identity.Environment}, 8)
	if err != nil || len(laterCandidates) != 1 || laterCandidates[0].ID != "refresh-job-valid-after-poison" {
		t.Fatalf("later candidates=%#v err=%v", laterCandidates, err)
	}
}

func TestPostgresPoisonReplayRejectsPartialOrTamperedRunTree(t *testing.T) {
	db := modulePostgresTestDB(t)
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := jobspostgres.New(db)
	identity := projectgraph.ServingIdentity{ProjectID: "project-poison-drift", Environment: "prod", GenerationID: "generation-poison-drift"}
	digest := "sha256:" + strings.Repeat("a", 64)
	if _, err := jobsRepo.Enqueue(t.Context(), jobs.EnqueueInput{ID: "poison-drift-job", Kind: refreshrun.JobKindRefreshPipeline, WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: "principal-poison-drift", PartitionKey: "refresh:" + identity.ProjectID.String() + ":" + identity.Environment, ResourceKind: refreshJobResourceKind, ResourceID: "poison-drift-run", EstimatedMemoryBytes: 1, Payload: []byte(`{"bad":true}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := refreshRepo.CreateRun(t.Context(), refreshpostgres.RunInput{RunID: "poison-drift-run", ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID, PipelineID: "pipeline", SemanticModelID: "semantic", TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline", TriggerType: refreshrun.TriggerManual, InvocationSource: refreshrun.TriggerManual, PlanDigest: digest, ArtifactDigest: digest, PrincipalID: "principal-poison-drift", JobID: "poison-drift-job"}); err != nil {
		t.Fatal(err)
	}
	if _, err := refreshRepo.CreateRun(t.Context(), refreshpostgres.RunInput{RunID: "poison-drift-child", ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID, ParentRunID: "poison-drift-run", PipelineID: "pipeline", SemanticModelID: "semantic", TargetType: refreshrun.TargetModelTable, TargetID: "model", TriggerType: refreshrun.TriggerDependency, InvocationSource: refreshrun.TriggerDependency, PlanDigest: digest, ArtifactDigest: digest, PrincipalID: "principal-poison-drift"}); err != nil {
		t.Fatal(err)
	}
	// A partially terminalized tree must not replay as an already quarantined
	// run. The child carries a tampered message while the root has the expected
	// poison message.
	if _, err := db.Exec(t.Context(), `UPDATE refresh.run SET status='failed',error='tampered',finished_at=clock_timestamp() WHERE run_id='poison-drift-child'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `UPDATE refresh.run SET status='failed',error='refresh job payload rejected',finished_at=clock_timestamp() WHERE run_id='poison-drift-run'`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := refreshRepo.QuarantineQueuedRunTx(t.Context(), tx, "poison-drift-run", "poison-drift-job")
	if !errors.Is(err, refreshpostgres.ErrConflict) || replayed {
		_ = tx.Rollback(t.Context())
		t.Fatalf("tampered poison tree replay=%v err=%v, want conflict", replayed, err)
	}
	_ = tx.Rollback(t.Context())
}

func TestPostgresStartupRecoveryReconcilesPairsWithoutKillingLiveWork(t *testing.T) {
	db := modulePostgresTestDB(t)
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := jobspostgres.New(db)
	queue := NewPostgresJobsAdapter(jobsRepo, refreshRepo)
	persistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{PublicationIdentityResolver: staticPublicationIdentityResolver("pool-recovery", "catalog-recovery"), SchedulerOwner: "scheduler-recovery", Jobs: queue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool-recovery", catalogID: "catalog-recovery"}, CancelAuditWriter: integrationAuditWriter{}})
	if err != nil {
		t.Fatal(err)
	}
	create := func(project, id string) refreshrun.RunRecord {
		t.Helper()
		identity := projectgraph.ServingIdentity{ProjectID: projectgraph.ResourceID(project), Environment: "prod", GenerationID: "generation-" + project}
		plan := integrationPlan(t, identity, "daily")
		plan.PipelineID = "pipeline_" + id
		plan.ID = "plan_" + id
		plan.ExecutionDigest, plan.ProvenanceDigest, plan.GovernanceDigest, plan.EvidenceDigest, plan.Digest = "", "", "", "", ""
		plan, err = deployment.NewPipelinePlan(plan)
		if err != nil {
			t.Fatal(err)
		}
		run, createErr := createTreeRootE(t.Context(), persistence.Runs, refreshrun.RunInput{RunID: id, Identity: identity, SemanticModelID: projectgraph.ResourceID(plan.SemanticModelID), PipelineID: projectgraph.ResourceID(plan.PipelineID), PipelinePlan: &plan, InvocationSource: "schedule", MatchingScheduleIDs: []string{"daily"}, NominalTime: time.Now().UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), ConcurrencyPolicy: "Forbid", PrincipalID: "principal:" + id, EstimatedMemoryBytes: 1, TargetType: refreshrun.TargetRefreshPipeline, TargetID: projectgraph.ResourceID(plan.PipelineID), TriggerType: refreshrun.TriggerSchedule, JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: `{}`}, nil)
		if createErr != nil {
			t.Fatal(createErr)
		}
		return run
	}
	queued := create("recovery-queued", "recovery-queued-run")
	live := create("recovery-live", "recovery-live-run")
	prepared := create("recovery-prepared", "recovery-prepared-run")
	expired := create("recovery-expired", "recovery-expired-run")
	terminalJob := create("recovery-terminal-job", "recovery-terminal-job-run")
	terminalRun := create("recovery-terminal-run", "recovery-terminal-run-run")
	missing := create("recovery-missing", "recovery-missing-run")
	claim := func(run refreshrun.RunRecord, owner string, lease time.Duration) refreshrun.JobRecord {
		t.Helper()
		candidates, listErr := queue.ListExecutableJobs(t.Context(), refreshrun.ReadScope{ProjectID: run.Identity.ProjectID, Environment: run.Identity.Environment}, 1)
		if listErr != nil || len(candidates) != 1 {
			t.Fatalf("recovery candidates=%#v err=%v", candidates, listErr)
		}
		job, ok, claimErr := queue.ClaimExecutableJob(t.Context(), candidates[0], owner, lease)
		if claimErr != nil || !ok {
			t.Fatalf("claim ok=%v err=%v", ok, claimErr)
		}
		return job
	}
	liveJob := claim(live, "node-a", time.Minute)
	preparedJob := claim(prepared, "node-a-prepared", time.Minute)
	if _, err := persistence.Runs.MarkRunPrepared(t.Context(), preparedJob); err != nil {
		t.Fatal(err)
	}
	expiredJob := claim(expired, "node-a-expired", 10*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	terminalJobRecord := claim(terminalJob, "node-a-terminal", time.Minute)
	if err := jobsRepo.Fail(t.Context(), terminalJobRecord.ID, jobs.Fence{Owner: terminalJobRecord.LeaseOwner, Generation: terminalJobRecord.LeaseRevision}, []byte(`{"code":"REFRESH_FAILED"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `UPDATE refresh.run SET status='failed',error='already terminal',finished_at=clock_timestamp() WHERE run_id=$1`, terminalRun.ID); err != nil {
		t.Fatal(err)
	}
	// The FK intentionally prevents manufacturing a missing canonical job.
	// Model the recoverable skew instead: the queue job is terminal while its
	// linked refresh run is still queued.
	missingCandidates, listErr := queue.ListExecutableJobs(t.Context(), refreshrun.ReadScope{ProjectID: missing.Identity.ProjectID, Environment: missing.Identity.Environment}, 1)
	if listErr != nil || len(missingCandidates) != 1 {
		t.Fatalf("missing skew candidates=%#v err=%v", missingCandidates, listErr)
	}
	missingJob, ok, claimErr := jobsRepo.ClaimByID(t.Context(), missingCandidates[0].ID, jobpolicy.WorkloadClassBackground, "node-a-missing", time.Minute)
	if claimErr != nil || !ok {
		t.Fatalf("claim missing skew job ok=%v err=%v", ok, claimErr)
	}
	if err := jobsRepo.Fail(t.Context(), missingJob.ID, jobs.Fence{Owner: missingJob.LeaseOwner, Generation: missingJob.LeaseGeneration}, []byte(`{"code":"REFRESH_FAILED"}`)); err != nil {
		t.Fatal(err)
	}
	recovery, ok := persistence.TerminalRecovery.(*PostgresTerminalRecovery)
	if !ok {
		t.Fatal("postgres terminal recovery adapter missing")
	}
	if err := recovery.FailRunsForTerminalServingStates(t.Context(), "prod", "refresh startup reconciliation"); err != nil {
		t.Fatal(err)
	}
	assertRun := func(id string, want string) {
		t.Helper()
		got, getErr := refreshRepo.LookupRun(t.Context(), id)
		if getErr != nil || got.Status != want {
			t.Fatalf("run %s=%#v err=%v, want %s", id, got, getErr, want)
		}
	}
	assertRun(queued.ID, refreshrun.RunStatusQueued)
	assertRun(live.ID, refreshrun.RunStatusRunning)
	assertRun(prepared.ID, refreshrun.RunStatusPrepared)
	assertRun(expired.ID, refreshrun.RunStatusRunning)
	assertRun(terminalJob.ID, refreshrun.RunStatusFailed)
	assertRun(terminalRun.ID, refreshrun.RunStatusFailed)
	assertRun(missing.ID, refreshrun.RunStatusFailed)
	if got, err := jobsRepo.Get(t.Context(), liveJob.ID); err != nil || got.Status != jobs.StatusRunning || got.LeaseOwner != liveJob.LeaseOwner {
		t.Fatalf("live job was not preserved: %#v err=%v", got, err)
	}
	if got, err := jobsRepo.Get(t.Context(), preparedJob.ID); err != nil || got.Status != jobs.StatusRunning || got.LeaseOwner != preparedJob.LeaseOwner {
		t.Fatalf("prepared pair was not preserved: %#v err=%v", got, err)
	}
	if got, err := jobsRepo.Get(t.Context(), expiredJob.ID); err != nil || got.Status != jobs.StatusRunning || got.LeaseOwner != expiredJob.LeaseOwner {
		t.Fatalf("expired exact-fence pair was not preserved: %#v err=%v", got, err)
	}
	terminalRunStored, err := refreshRepo.LookupRun(t.Context(), terminalRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := jobsRepo.Get(t.Context(), terminalRunStored.JobID); err != nil || got.Status != jobs.StatusFailed {
		t.Fatalf("terminal run job was not reconciled: %#v err=%v", got, err)
	}
}

func TestPostgresStartupRecoveryRejectsAmbiguousPairAndRollsBack(t *testing.T) {
	db := modulePostgresTestDB(t)
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := jobspostgres.New(db)
	queue := NewPostgresJobsAdapter(jobsRepo, refreshRepo)
	persistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{PublicationIdentityResolver: staticPublicationIdentityResolver("pool-recovery-ambiguous", "catalog-recovery-ambiguous"), SchedulerOwner: "scheduler-recovery-ambiguous", Jobs: queue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool-recovery-ambiguous", catalogID: "catalog-recovery-ambiguous"}, CancelAuditWriter: integrationAuditWriter{}})
	if err != nil {
		t.Fatal(err)
	}
	create := func(id string) refreshrun.RunRecord {
		identity := projectgraph.ServingIdentity{ProjectID: projectgraph.ResourceID("recovery-ambiguous-" + id), Environment: "prod", GenerationID: "generation-recovery-" + id}
		plan := integrationPlan(t, identity, "daily")
		run, createErr := createTreeRootE(t.Context(), persistence.Runs, refreshrun.RunInput{RunID: id, Identity: identity, SemanticModelID: projectgraph.ResourceID(plan.SemanticModelID), PipelineID: projectgraph.ResourceID(plan.PipelineID), PipelinePlan: &plan, InvocationSource: "schedule", MatchingScheduleIDs: []string{"daily"}, NominalTime: time.Now().UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), ConcurrencyPolicy: "Forbid", PrincipalID: "principal:" + id, EstimatedMemoryBytes: 1, TargetType: refreshrun.TargetRefreshPipeline, TargetID: projectgraph.ResourceID(plan.PipelineID), TriggerType: refreshrun.TriggerSchedule, JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: `{}`}, nil)
		if createErr != nil {
			t.Fatal(createErr)
		}
		return run
	}
	missing := create("recovery-ambiguous-missing")
	ambiguousRun := create("recovery-ambiguous-running")
	missingCandidates, err := queue.ListExecutableJobs(t.Context(), refreshrun.ReadScope{ProjectID: missing.Identity.ProjectID, Environment: missing.Identity.Environment}, 1)
	if err != nil || len(missingCandidates) != 1 {
		t.Fatalf("missing skew candidates=%#v err=%v", missingCandidates, err)
	}
	missingJob, ok, err := jobsRepo.ClaimByID(t.Context(), missingCandidates[0].ID, jobpolicy.WorkloadClassBackground, "node-missing", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim missing skew job ok=%v err=%v", ok, err)
	}
	if err := jobsRepo.Fail(t.Context(), missingJob.ID, jobs.Fence{Owner: missingJob.LeaseOwner, Generation: missingJob.LeaseGeneration}, []byte(`{"code":"REFRESH_FAILED"}`)); err != nil {
		t.Fatal(err)
	}
	ambiguousIdentity := ambiguousRun.Identity
	candidates, err := queue.ListExecutableJobs(t.Context(), refreshrun.ReadScope{ProjectID: ambiguousIdentity.ProjectID, Environment: ambiguousIdentity.Environment}, 1)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("ambiguous candidates=%#v err=%v", candidates, err)
	}
	if _, ok, err := jobsRepo.ClaimByID(t.Context(), candidates[0].ID, jobpolicy.WorkloadClassBackground, "node-b", time.Minute); err != nil || !ok {
		t.Fatalf("claim ambiguous job ok=%v err=%v", ok, err)
	}
	recovery, ok := persistence.TerminalRecovery.(*PostgresTerminalRecovery)
	if !ok {
		t.Fatal("postgres terminal recovery adapter missing")
	}
	if err := recovery.FailRunsForTerminalServingStates(t.Context(), "prod", "refresh startup reconciliation"); err == nil {
		t.Fatal("ambiguous queued/running pair unexpectedly reconciled")
	}
	if got, err := refreshRepo.LookupRun(t.Context(), missing.ID); err != nil || got.Status != refreshrun.RunStatusQueued {
		t.Fatalf("earlier missing-job repair was not rolled back: %#v err=%v", got, err)
	}
}

func TestPostgresStartupRecoveryPagesActiveRunsAndRollsBackLaterAmbiguity(t *testing.T) {
	db := modulePostgresTestDB(t)
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := jobspostgres.New(db)
	queue := NewPostgresJobsAdapter(jobsRepo, refreshRepo)
	persistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{PublicationIdentityResolver: staticPublicationIdentityResolver("pool-recovery-pages", "catalog-recovery-pages"), SchedulerOwner: "scheduler-recovery-pages", Jobs: queue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool-recovery-pages", catalogID: "catalog-recovery-pages"}, CancelAuditWriter: integrationAuditWriter{}})
	if err != nil {
		t.Fatal(err)
	}
	const rows = 201
	if _, err := db.Exec(t.Context(), `INSERT INTO jobs.job (id,kind,workload_class,principal_id,group_ids,partition_key,resource_kind,resource_id,estimated_memory_bytes,payload,request_digest)
SELECT 'page-job-'||lpad(i::text,3,'0'),'refresh_pipeline','background','page-principal-'||lpad(i::text,3,'0'),'{}'::text[],'refresh:project_page:prod','refresh_run','page-run-'||lpad(i::text,3,'0'),1,'{}'::jsonb,'sha256:'||repeat('a',64)
FROM generate_series(1,$1) AS s(i)`, rows); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO refresh.run (run_id,project_id,environment,generation_id,pipeline_id,semantic_model_id,target_type,target_id,trigger_type,invocation_source,plan_digest,artifact_digest,principal_id,job_id)
SELECT 'page-run-'||lpad(i::text,3,'0'),'project_page','prod','generation-page','pipeline_page_'||lpad(i::text,3,'0'),'semantic_page','refresh_pipeline','pipeline_page_'||lpad(i::text,3,'0'),'manual','manual','sha256:'||repeat('b',64),'sha256:'||repeat('c',64),'page-principal-'||lpad(i::text,3,'0'),'page-job-'||lpad(i::text,3,'0')
FROM generate_series(1,$1) AS s(i)`, rows); err != nil {
		t.Fatal(err)
	}
	pageOne, ok, err := jobsRepo.ClaimByID(t.Context(), "page-job-001", jobpolicy.WorkloadClassBackground, "node-a", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim first-page skew job ok=%v err=%v", ok, err)
	}
	if err := jobsRepo.Fail(t.Context(), pageOne.ID, jobs.Fence{Owner: pageOne.LeaseOwner, Generation: pageOne.LeaseGeneration}, []byte(`{"code":"REFRESH_FAILED"}`)); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := jobsRepo.ClaimByID(t.Context(), "page-job-201", jobpolicy.WorkloadClassBackground, "node-b", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim later-page ambiguity ok=%v err=%v", ok, err)
	}
	recovery, ok := persistence.TerminalRecovery.(*PostgresTerminalRecovery)
	if !ok {
		t.Fatal("postgres terminal recovery adapter missing")
	}
	if err := recovery.FailRunsForTerminalServingStates(t.Context(), "prod", "attacker-controlled detail"); err == nil {
		t.Fatal("later-page ambiguous pair unexpectedly reconciled")
	}
	var status string
	if err := db.QueryRow(t.Context(), `SELECT status FROM refresh.run WHERE run_id='page-run-001'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != refreshrun.RunStatusQueued {
		t.Fatalf("earlier-page repair committed despite later ambiguity: %s", status)
	}
	if got, err := jobsRepo.Get(t.Context(), claimed.ID); err != nil || got.Status != jobs.StatusRunning {
		t.Fatalf("later-page ambiguous job changed unexpectedly: %#v err=%v", got, err)
	}
}

func TestPostgresLinkedLeaseRenewalRollsBackOnStaleRefreshFence(t *testing.T) {
	db := modulePostgresTestDB(t)
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := jobspostgres.New(db)
	queue := NewPostgresJobsAdapter(jobsRepo, refreshRepo)
	identity := projectgraph.ServingIdentity{ProjectID: "project_lease", Environment: "prod", GenerationID: "generation_lease"}
	plan := integrationPlan(t, identity, "daily")
	persistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{
		PublicationIdentityResolver: staticPublicationIdentityResolver("pool-lease", "catalog-lease"), SchedulerOwner: "scheduler-lease",
		Jobs: queue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool-lease", catalogID: "catalog-lease"}, CancelAuditWriter: integrationAuditWriter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := createTreeRootE(t.Context(), persistence.Runs, refreshrun.RunInput{
		RunID: "stale-linked-renewal", Identity: identity, SemanticModelID: "semantic_sales", PipelineID: "pipeline_daily", PipelinePlan: &plan,
		InvocationSource: "schedule", MatchingScheduleIDs: []string{"daily"}, NominalTime: time.Now().UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), ConcurrencyPolicy: "Forbid",
		PrincipalID: "principal:lease", EstimatedMemoryBytes: 1, TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline_daily", TriggerType: refreshrun.TriggerSchedule, JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: `{}`,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := refreshRepo.LookupRun(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.JobID == "" {
		t.Fatal("created run has no canonical job")
	}
	scope := refreshrun.ReadScope{ProjectID: identity.ProjectID, Environment: identity.Environment}
	candidates, err := persistence.Runs.ListExecutableJobs(t.Context(), scope, 1)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("initial candidates=%#v err=%v", candidates, err)
	}
	first, ok, err := persistence.Runs.ClaimExecutableJob(t.Context(), candidates[0], "worker-one", 20*time.Millisecond)
	if err != nil || !ok {
		t.Fatalf("first claim ok=%v err=%v", ok, err)
	}
	if _, err := persistence.Runs.MarkRunPrepared(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	reclaimedCandidates, err := persistence.Runs.ListExecutableJobs(t.Context(), scope, 1)
	if err != nil || len(reclaimedCandidates) != 1 {
		t.Fatalf("reclaim candidates=%#v err=%v", reclaimedCandidates, err)
	}
	second, ok, err := persistence.Runs.ClaimExecutableJob(t.Context(), reclaimedCandidates[0], "worker-two", time.Minute)
	if err != nil || !ok {
		t.Fatalf("second claim ok=%v err=%v", ok, err)
	}
	if second.LeaseRevision <= first.LeaseRevision {
		t.Fatalf("lease revision did not advance: first=%d second=%d", first.LeaseRevision, second.LeaseRevision)
	}
	var leaseBeforeRenewal time.Time
	if err := db.QueryRow(t.Context(), `SELECT lease_expires_at FROM jobs.job WHERE id=$1`, stored.JobID).Scan(&leaseBeforeRenewal); err != nil {
		t.Fatal(err)
	}
	if err := persistence.Runs.RenewJobLease(t.Context(), first, 5*time.Minute); !errors.Is(err, jobs.ErrConflict) {
		t.Fatalf("stale linked renewal error=%v, want %v", err, jobs.ErrConflict)
	}
	var jobOwner string
	var jobFence int64
	var jobExpiry time.Time
	if err := db.QueryRow(t.Context(), `SELECT lease_owner,lease_generation,lease_expires_at FROM jobs.job WHERE id=$1`, stored.JobID).Scan(&jobOwner, &jobFence, &jobExpiry); err != nil {
		t.Fatal(err)
	}
	if jobOwner != second.LeaseOwner || jobFence != second.LeaseRevision || !jobExpiry.Equal(leaseBeforeRenewal) {
		t.Fatalf("job renewal was not rolled back: owner=%q fence=%d expiry=%s before=%s", jobOwner, jobFence, jobExpiry, leaseBeforeRenewal)
	}
	var runOwner string
	var runFence int64
	if err := db.QueryRow(t.Context(), `SELECT lease_owner,fence_generation FROM refresh.run WHERE run_id=$1`, created.ID).Scan(&runOwner, &runFence); err != nil {
		t.Fatal(err)
	}
	if runOwner != second.LeaseOwner || runFence != second.LeaseRevision {
		t.Fatalf("refresh run lease changed across stale renewal: owner=%q fence=%d", runOwner, runFence)
	}
}

func TestPostgresRunTreeAdmissionRollsBackEveryAuthorityOnChildConflict(t *testing.T) {
	db := modulePostgresTestDB(t)
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := jobspostgres.New(db)
	queue := NewPostgresJobsAdapter(jobsRepo, refreshRepo)
	identity := projectgraph.ServingIdentity{ProjectID: "project_tree_atomic", Environment: "prod", GenerationID: "generation_tree_atomic"}
	plan := integrationPlan(t, identity, "daily")
	persistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{
		PublicationIdentityResolver: staticPublicationIdentityResolver("pool-tree-atomic", "catalog-tree-atomic"), SchedulerOwner: "scheduler-tree-atomic",
		Jobs: queue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool-tree-atomic", catalogID: "catalog-tree-atomic"}, CancelAuditWriter: integrationAuditWriter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	treeCreator, ok := persistence.Runs.(refreshrun.RunTreeRepository)
	if !ok {
		t.Fatal("PostgreSQL persistence does not expose atomic tree creation")
	}
	dependency := projectgraph.ResourceID("model_orders")

	assertRollback := func(t *testing.T, rootInput refreshrun.RunInput, occurrence *refreshschedule.Occurrence) {
		t.Helper()
		conflictID := deterministicChildRunID(rootInput.RunID, dependency.String())
		existingParentID := "existing-parent-" + rootInput.RunID
		existingParentJob := "existing-parent-job-" + rootInput.RunID
		if _, err := jobsRepo.Enqueue(t.Context(), jobs.EnqueueInput{ID: existingParentJob, Kind: refreshrun.JobKindRefreshPipeline, WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: "principal:existing", PartitionKey: "refresh:" + rootInput.Identity.ProjectID.String() + ":" + rootInput.Identity.Environment, ResourceKind: refreshJobResourceKind, ResourceID: existingParentID, EstimatedMemoryBytes: 1, Payload: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
		if _, err := refreshRepo.CreateRun(t.Context(), refreshpostgres.RunInput{RunID: existingParentID, ProjectID: rootInput.Identity.ProjectID.String(), Environment: rootInput.Identity.Environment, GenerationID: rootInput.Identity.GenerationID, PipelineID: rootInput.PipelineID.String(), SemanticModelID: rootInput.SemanticModelID.String(), TargetType: refreshrun.TargetRefreshPipeline, TargetID: rootInput.PipelineID.String(), TriggerType: refreshrun.TriggerManual, InvocationSource: refreshrun.TriggerManual, PlanDigest: rootInput.PipelinePlan.Digest, ArtifactDigest: rootInput.PipelinePlan.ArtifactDigest, PrincipalID: "principal:existing", JobID: existingParentJob}); err != nil {
			t.Fatal(err)
		}
		if err := jobsRepo.Cancel(t.Context(), existingParentJob); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(t.Context(), `UPDATE refresh.run SET status='failed',error='existing parent',finished_at=clock_timestamp() WHERE run_id=$1`, existingParentID); err != nil {
			t.Fatal(err)
		}
		if _, err := refreshRepo.CreateRun(t.Context(), refreshpostgres.RunInput{
			RunID: conflictID, ProjectID: rootInput.Identity.ProjectID.String(), Environment: rootInput.Identity.Environment, GenerationID: rootInput.Identity.GenerationID,
			ParentRunID: existingParentID, PipelineID: rootInput.PipelineID.String(), SemanticModelID: rootInput.SemanticModelID.String(), TargetType: refreshrun.TargetModelTable, TargetID: dependency.String(),
			TriggerType: refreshrun.TriggerDependency, InvocationSource: refreshrun.TriggerDependency, PlanDigest: rootInput.PipelinePlan.Digest, ArtifactDigest: rootInput.PipelinePlan.ArtifactDigest, PrincipalID: "principal:existing",
		}); err != nil {
			t.Fatal(err)
		}
		_, _, err := treeCreator.CreateRunTree(t.Context(), refreshrun.RunTreeInput{Root: rootInput, DependencyTargets: []projectgraph.ResourceID{dependency}, Occurrence: occurrence})
		if err == nil {
			t.Fatal("tree admission unexpectedly succeeded with conflicting child")
		}
		if _, lookupErr := refreshRepo.LookupRun(t.Context(), rootInput.RunID); !errors.Is(lookupErr, refreshpostgres.ErrNotFound) {
			t.Fatalf("root survived child conflict: err=%v", lookupErr)
		}
		if existing, lookupErr := refreshRepo.LookupRun(t.Context(), conflictID); lookupErr != nil || existing.ParentRunID != existingParentID {
			t.Fatalf("conflicting child changed: %#v err=%v", existing, lookupErr)
		}
		if _, getErr := jobsRepo.Get(t.Context(), "refresh-job-"+rootInput.RunID); !errors.Is(getErr, jobs.ErrNotFound) {
			t.Fatalf("canonical job survived child conflict: err=%v", getErr)
		}
		if occurrence != nil {
			var status, runID, owner string
			if scanErr := db.QueryRow(t.Context(), `SELECT status,COALESCE(run_id,''),lease_owner FROM refresh.schedule_occurrence WHERE occurrence_id=$1`, occurrence.OccurrenceID).Scan(&status, &runID, &owner); scanErr != nil {
				t.Fatal(scanErr)
			}
			if status != "claimed" || runID != "" || owner != occurrence.LeaseOwner {
				t.Fatalf("occurrence claim changed across rollback: status=%q run=%q owner=%q", status, runID, owner)
			}
		}
	}

	manualPlan, err := deployment.NewPipelinePlan(deployment.PipelinePlan{
		ID: "pipeline_plan_module_manual", PipelineID: "pipeline_daily", ProjectID: identity.ProjectID.String(), Environment: identity.Environment,
		SemanticModelID: "semantic_sales", ServingGenerationID: identity.GenerationID, ArtifactDigest: plan.ArtifactDigest, SelectionDigest: plan.SelectionDigest,
		MaterializationScope: []string{"model_orders"}, InvocationSource: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertRollback(t, refreshrun.RunInput{
		RunID: "atomic-tree-manual", Identity: identity, SemanticModelID: "semantic_sales", PipelineID: "pipeline_daily", PipelinePlan: &manualPlan,
		InvocationSource: refreshrun.TriggerManual, PrincipalID: "principal:tree", EstimatedMemoryBytes: 1, TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline_daily", TriggerType: refreshrun.TriggerManual, JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: `{}`,
	}, nil)

	scheduledIdentity := projectgraph.ServingIdentity{ProjectID: "project_tree_atomic_sched", Environment: "prod", GenerationID: "generation_tree_atomic_sched"}
	scheduledPlan := integrationPlan(t, scheduledIdentity, "daily")
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := refreshRepo.PutSchedule(t.Context(), refreshpostgres.ScheduleInput{
		ProjectID: scheduledIdentity.ProjectID.String(), Environment: scheduledIdentity.Environment, PipelineID: "pipeline_daily", ScheduleID: "daily", SemanticModelID: "semantic_sales", GenerationID: scheduledIdentity.GenerationID,
		ArtifactDigest: scheduledPlan.ArtifactDigest, Cron: "* * * * *", Timezone: "UTC", ConcurrencyPolicy: "Forbid", StartingDeadline: time.Hour,
		ScheduleDigest: "sha256:" + strings.Repeat("c", 64), NextRunAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	scheduled, err := (&postgresSchedulePersistence{repository: refreshRepo, schedulerOwner: "scheduler-tree-atomic", identityResolver: staticPublicationIdentityResolver("pool-tree-atomic", "catalog-tree-atomic")}).ClaimDue(t.Context(), scheduledIdentity, now)
	if err != nil || len(scheduled) != 1 {
		t.Fatalf("claim scheduled occurrence=%#v err=%v", scheduled, err)
	}
	rootInput := refreshrun.RunInput{
		RunID: "atomic-tree-scheduled", Identity: scheduledIdentity, SemanticModelID: "semantic_sales", PipelineID: "pipeline_daily", PipelinePlan: &scheduledPlan,
		InvocationSource: refreshrun.TriggerSchedule, MatchingScheduleIDs: []string{"daily"}, NominalTime: scheduled[0].ScheduledAt.UTC().Format(time.RFC3339Nano), ConcurrencyPolicy: "Forbid", PrincipalID: "principal:tree", EstimatedMemoryBytes: 1,
		TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline_daily", TriggerType: refreshrun.TriggerSchedule, JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: `{}`,
	}
	assertRollback(t, rootInput, &scheduled[0])
}

func TestPostgresRunTreeSuccessTerminalizesChildOccurrenceAndJob(t *testing.T) {
	db := modulePostgresTestDB(t)
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := jobspostgres.New(db)
	queue := NewPostgresJobsAdapter(jobsRepo, refreshRepo)
	identity := projectgraph.ServingIdentity{ProjectID: "project_tree_success", Environment: "prod", GenerationID: "generation_tree_success"}
	plan := integrationPlan(t, identity, "daily")
	persistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{
		PublicationIdentityResolver: staticPublicationIdentityResolver("pool-tree-success", "catalog-tree-success"), SchedulerOwner: "scheduler-tree-success",
		Jobs: queue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool-tree-success", catalogID: "catalog-tree-success"}, CancelAuditWriter: integrationAuditWriter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := refreshRepo.PutSchedule(t.Context(), refreshpostgres.ScheduleInput{
		ProjectID: identity.ProjectID.String(), Environment: identity.Environment, PipelineID: "pipeline_daily", ScheduleID: "daily", SemanticModelID: "semantic_sales", GenerationID: identity.GenerationID,
		ArtifactDigest: plan.ArtifactDigest, Cron: "* * * * *", Timezone: "UTC", ConcurrencyPolicy: "Forbid", StartingDeadline: time.Hour,
		ScheduleDigest: "sha256:" + strings.Repeat("c", 64), NextRunAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := persistence.Schedules.ClaimDue(t.Context(), identity, now)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed occurrence=%#v err=%v", claimed, err)
	}
	rootInput := refreshrun.RunInput{
		RunID: "tree-success-root", Identity: identity, SemanticModelID: "semantic_sales", PipelineID: "pipeline_daily", PipelinePlan: &plan,
		InvocationSource: refreshrun.TriggerSchedule, MatchingScheduleIDs: []string{"daily"}, NominalTime: claimed[0].ScheduledAt.UTC().Format(time.RFC3339Nano),
		ConcurrencyPolicy: "Forbid", PrincipalID: "principal:tree-success", EstimatedMemoryBytes: 1, TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline_daily", TriggerType: refreshrun.TriggerSchedule,
		JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: `{}`,
	}
	root, children, err := persistence.Runs.CreateRunTree(t.Context(), refreshrun.RunTreeInput{Root: rootInput, DependencyTargets: []projectgraph.ResourceID{"model_orders"}, Occurrence: &claimed[0]})
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 || children[0].ParentRunID != root.ID || children[0].Status != refreshrun.RunStatusQueued {
		t.Fatalf("admitted tree root=%#v children=%#v", root, children)
	}
	jobs, err := persistence.Runs.ListExecutableJobs(t.Context(), refreshrun.ReadScope{ProjectID: identity.ProjectID, Environment: identity.Environment}, 1)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("tree executable jobs=%#v err=%v", jobs, err)
	}
	claimedJob, ok, err := persistence.Runs.ClaimExecutableJob(t.Context(), jobs[0], "worker-tree-success", time.Minute)
	if err != nil || !ok {
		t.Fatalf("tree job claim ok=%v err=%v", ok, err)
	}
	finalRoot, err := persistence.Runs.MarkRunSucceededClaimed(t.Context(), claimedJob)
	if err != nil {
		t.Fatal(err)
	}
	if finalRoot.Status != refreshrun.RunStatusSucceeded {
		t.Fatalf("root status=%q, want succeeded", finalRoot.Status)
	}
	storedChild, err := refreshRepo.LookupRun(t.Context(), children[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedChild.Status != refreshrun.RunStatusSucceeded {
		t.Fatalf("child status=%q, want succeeded", storedChild.Status)
	}
	var occurrenceStatus, occurrenceRun string
	if err := db.QueryRow(t.Context(), `SELECT status,run_id FROM refresh.schedule_occurrence WHERE occurrence_id=$1`, claimed[0].OccurrenceID).Scan(&occurrenceStatus, &occurrenceRun); err != nil {
		t.Fatal(err)
	}
	if occurrenceStatus != "succeeded" || occurrenceRun != root.ID {
		t.Fatalf("occurrence status=%q run=%q", occurrenceStatus, occurrenceRun)
	}
	job, err := jobsRepo.Get(t.Context(), claimedJob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "succeeded" {
		t.Fatalf("job status=%q, want succeeded", job.Status)
	}
}

func TestPostgresRunPersistenceRejectsStandaloneAdmission(t *testing.T) {
	db := modulePostgresTestDB(t)
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := jobspostgres.New(db)
	queue := NewPostgresJobsAdapter(jobsRepo, refreshRepo)
	identity := projectgraph.ServingIdentity{ProjectID: "project_standalone_rejected", Environment: "prod", GenerationID: "generation_standalone_rejected"}
	plan := integrationPlan(t, identity, "daily")
	persistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{
		PublicationIdentityResolver: staticPublicationIdentityResolver("pool-standalone-rejected", "catalog-standalone-rejected"), SchedulerOwner: "scheduler-standalone-rejected",
		Jobs: queue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool-standalone-rejected", catalogID: "catalog-standalone-rejected"}, CancelAuditWriter: integrationAuditWriter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	root := refreshrun.RunInput{RunID: "standalone-root", Identity: identity, SemanticModelID: "semantic_sales", PipelineID: "pipeline_daily", PipelinePlan: &plan, InvocationSource: "manual", PrincipalID: "principal:standalone", TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline_daily", TriggerType: refreshrun.TriggerManual, JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: `{}`}
	if _, err := persistence.Runs.CreateRun(t.Context(), root); err == nil {
		t.Fatal("standalone root admission unexpectedly succeeded")
	}
	child := root
	child.RunID = "standalone-child"
	child.ParentRunID = "standalone-parent"
	child.TargetType = refreshrun.TargetModelTable
	child.TargetID = "model_orders"
	child.TriggerType = refreshrun.TriggerDependency
	child.InvocationSource = refreshrun.TriggerDependency
	child.JobKind = refreshrun.JobKindChildRun
	if _, err := persistence.Runs.CreateRun(t.Context(), child); err == nil {
		t.Fatal("standalone child admission unexpectedly succeeded")
	}
}

func TestPostgresProducerFailureCancelsLinkedRootJob(t *testing.T) {
	db := modulePostgresTestDB(t)
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := jobspostgres.New(db)
	queue := NewPostgresJobsAdapter(jobsRepo, refreshRepo)
	identity := projectgraph.ServingIdentity{ProjectID: "project_producer_failure", Environment: "prod", GenerationID: "generation_producer_failure"}
	plan, err := deployment.NewPipelinePlan(deployment.PipelinePlan{
		ID: "pipeline_plan_producer_failure", PipelineID: "pipeline_failure", ProjectID: identity.ProjectID.String(), Environment: identity.Environment,
		SemanticModelID: "semantic_failure", ServingGenerationID: identity.GenerationID,
		ArtifactDigest: "sha256:" + strings.Repeat("a", 64), SelectionDigest: "sha256:" + strings.Repeat("b", 64), MaterializationScope: []string{"model_failure"}, InvocationSource: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	persistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{
		PublicationIdentityResolver: staticPublicationIdentityResolver("pool-producer-failure", "catalog-producer-failure"), SchedulerOwner: "scheduler-producer-failure",
		Jobs: queue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool-producer-failure", catalogID: "catalog-producer-failure"}, CancelAuditWriter: integrationAuditWriter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := createTreeRootE(t.Context(), persistence.Runs, refreshrun.RunInput{
		RunID: "producer-failure-root", Identity: identity, SemanticModelID: "semantic_failure", PipelineID: "pipeline_failure", PipelinePlan: &plan,
		InvocationSource: "manual", PrincipalID: "principal:producer-failure", EstimatedMemoryBytes: 1, TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline_failure", TriggerType: refreshrun.TriggerManual, JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: `{}`,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	producer, ok := persistence.Runs.(refreshrun.ProducerFailureRepository)
	if !ok {
		t.Fatal("postgres run persistence lacks producer failure capability")
	}
	if _, err := producer.MarkQueuedRunFailed(t.Context(), identity, created.ID, "dependency creation failed"); err != nil {
		t.Fatal(err)
	}
	failed, err := refreshRepo.LookupRun(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != refreshrun.RunStatusFailed {
		t.Fatalf("root status=%q, want failed", failed.Status)
	}
	job, err := jobsRepo.Get(t.Context(), failed.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "cancelled" {
		t.Fatalf("linked job status=%q, want cancelled", job.Status)
	}
}

func integrationPlan(t *testing.T, identity projectgraph.ServingIdentity, scheduleID string) deployment.PipelinePlan {
	t.Helper()
	plan, err := deployment.NewPipelinePlan(deployment.PipelinePlan{
		ID: "pipeline_plan_module_integration", PipelineID: "pipeline_daily", ProjectID: identity.ProjectID.String(), Environment: identity.Environment,
		SemanticModelID: "semantic_sales", ServingGenerationID: identity.GenerationID,
		ArtifactDigest: "sha256:" + strings.Repeat("a", 64), SelectionDigest: "sha256:" + strings.Repeat("b", 64), MaterializationScope: []string{"model_orders"},
		InvocationSource: "schedule", MatchingScheduleIDs: []string{scheduleID}, StartingDeadlineSeconds: 3600, ConcurrencyPolicy: "Forbid",
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestPostgresScheduledClaimCreatesRunAndJobAtomically(t *testing.T) {
	db := modulePostgresTestDB(t)
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := jobspostgres.New(db)
	identity := projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: "prod", GenerationID: "generation_module"}
	now := time.Now().UTC().Truncate(time.Second)
	schedule, err := refreshRepo.PutSchedule(t.Context(), refreshpostgres.ScheduleInput{
		ProjectID: identity.ProjectID.String(), Environment: identity.Environment, PipelineID: "pipeline_daily", ScheduleID: "daily", SemanticModelID: "semantic_sales", GenerationID: identity.GenerationID,
		ArtifactDigest: "sha256:" + strings.Repeat("a", 64), Cron: "* * * * *", Timezone: "UTC", ConcurrencyPolicy: "Forbid", StartingDeadline: time.Hour,
		ScheduleDigest: "sha256:" + strings.Repeat("c", 64), NextRunAt: now.Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = schedule
	queue := NewPostgresJobsAdapter(jobsRepo, refreshRepo)
	persistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{
		PublicationIdentityResolver: staticPublicationIdentityResolver("pool-primary", "catalog-primary"), SchedulerOwner: "scheduler-module-1",
		Jobs: queue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool-primary", catalogID: "catalog-primary"}, CancelAuditWriter: integrationAuditWriter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := persistence.Schedules.ClaimDue(t.Context(), identity, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].OccurrenceID == "" || claimed[0].LeaseOwner != "scheduler-module-1" || claimed[0].LeaseRevision <= 0 || claimed[0].LeaseExpiresAt.IsZero() {
		t.Fatalf("claimed occurrence = %#v", claimed)
	}
	plan := integrationPlan(t, identity, "daily")
	runInput := refreshrun.RunInput{
		Identity: identity, SemanticModelID: "semantic_sales", PipelineID: "pipeline_daily", PipelinePlan: &plan,
		InvocationSource: "schedule", MatchingScheduleIDs: []string{"daily"}, NominalTime: claimed[0].ScheduledAt.UTC().Format(time.RFC3339Nano),
		ConcurrencyPolicy: "Forbid", PrincipalID: "principal:sales", EstimatedMemoryBytes: 1, TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline_daily", TriggerType: refreshrun.TriggerSchedule, JobKind: refreshrun.JobKindRefreshPipeline,
		PayloadJSON: string(mustJSON(t, map[string]any{"pipelinePlan": plan})),
	}
	created, _, err := persistence.Runs.CreateRunTree(t.Context(), refreshrun.RunTreeInput{Root: runInput, Occurrence: &claimed[0]})
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != refreshrun.RunStatusQueued {
		t.Fatalf("created run status = %q", created.Status)
	}
	stored, err := refreshRepo.LookupRun(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.JobID == "" {
		t.Fatal("scheduled run did not link a canonical job")
	}
	job, err := jobsRepo.Get(t.Context(), stored.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.ResourceKind != refreshJobResourceKind || job.ResourceID != created.ID || job.Status != "queued" {
		t.Fatalf("canonical job = %#v", job)
	}
	var occurrenceStatus, occurrenceRun string
	if err := db.QueryRow(t.Context(), `SELECT status,run_id FROM refresh.schedule_occurrence WHERE occurrence_id=$1`, claimed[0].OccurrenceID).Scan(&occurrenceStatus, &occurrenceRun); err != nil {
		t.Fatal(err)
	}
	if occurrenceStatus != "queued" || occurrenceRun != created.ID {
		t.Fatalf("occurrence status=%q run=%q", occurrenceStatus, occurrenceRun)
	}
	jobs, err := persistence.Runs.ListExecutableJobs(t.Context(), refreshrun.ReadScope{ProjectID: identity.ProjectID, Environment: identity.Environment}, 1)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("scheduled executable jobs=%#v err=%v", jobs, err)
	}
	claimedJob, ok, err := persistence.Runs.ClaimExecutableJob(t.Context(), jobs[0], "worker-scheduled", time.Minute)
	if err != nil || !ok {
		t.Fatalf("scheduled claim ok=%v err=%v", ok, err)
	}
	if err := db.QueryRow(t.Context(), `SELECT status FROM refresh.schedule_occurrence WHERE occurrence_id=$1`, claimed[0].OccurrenceID).Scan(&occurrenceStatus); err != nil {
		t.Fatal(err)
	}
	if occurrenceStatus != "running" {
		t.Fatalf("occurrence claim status=%q, want running", occurrenceStatus)
	}
	if _, err := persistence.Runs.MarkRunSucceededClaimed(t.Context(), claimedJob); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT status FROM refresh.schedule_occurrence WHERE occurrence_id=$1`, claimed[0].OccurrenceID).Scan(&occurrenceStatus); err != nil {
		t.Fatal(err)
	}
	if occurrenceStatus != "succeeded" {
		t.Fatalf("occurrence terminal status=%q, want succeeded", occurrenceStatus)
	}
}

func TestPostgresScheduledReplaceTerminalizesOldRefreshTreeAndJob(t *testing.T) {
	db := modulePostgresTestDB(t)
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := jobspostgres.New(db)
	baseQueue := NewPostgresJobsAdapter(jobsRepo, refreshRepo)
	persistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{
		PublicationIdentityResolver: staticPublicationIdentityResolver("pool-replace", "catalog-replace"), SchedulerOwner: "scheduler-replace",
		Jobs: baseQueue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool-replace", catalogID: "catalog-replace"}, CancelAuditWriter: integrationAuditWriter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := projectgraph.ServingIdentity{ProjectID: "project_replace", Environment: "prod", GenerationID: "generation_replace"}
	plan := integrationPlan(t, identity, "daily")
	input := refreshrun.RunInput{Identity: identity, SemanticModelID: "semantic_sales", PipelineID: "pipeline_daily", PipelinePlan: &plan, InvocationSource: "schedule", MatchingScheduleIDs: []string{"daily"}, NominalTime: time.Now().UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), ConcurrencyPolicy: "Forbid", PrincipalID: "principal:replace", EstimatedMemoryBytes: 1, TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline_daily", TriggerType: refreshrun.TriggerSchedule, JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: `{}`}
	input.RunID = "replace-old-run"
	oldRun, err := createTreeRootE(t.Context(), persistence.Runs, input, nil)
	if err != nil {
		t.Fatal(err)
	}
	oldStored, err := refreshRepo.LookupRun(t.Context(), oldRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.RunID = "replace-new-run"
	input.NominalTime = time.Now().UTC().Add(time.Second).Truncate(time.Microsecond).Format(time.RFC3339Nano)
	input.ConcurrencyPolicy = "Replace"
	newRun, err := createTreeRootE(t.Context(), persistence.Runs, input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if oldState, err := refreshRepo.LookupRun(t.Context(), oldRun.ID); err != nil || oldState.Status != refreshrun.RunStatusSuperseded {
		t.Fatalf("old refresh state=%#v err=%v", oldState, err)
	}
	oldJob, err := jobsRepo.Get(t.Context(), oldStored.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if oldJob.Status != "cancelled" {
		t.Fatalf("old job status=%q, want cancelled", oldJob.Status)
	}
	newStored, err := refreshRepo.LookupRun(t.Context(), newRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	newJob, err := jobsRepo.Get(t.Context(), newStored.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if newJob.Status != "queued" {
		t.Fatalf("new job status=%q, want queued", newJob.Status)
	}
}

func TestPostgresScheduledReplaceRollbackKeepsRefreshAndJobLive(t *testing.T) {
	db := modulePostgresTestDB(t)
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := jobspostgres.New(db)
	baseQueue := NewPostgresJobsAdapter(jobsRepo, refreshRepo)
	failingQueue := failingSupersedeQueue{PostgresJobsAdapter: baseQueue, err: errors.New("supersede queue unavailable")}
	persistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{
		PublicationIdentityResolver: staticPublicationIdentityResolver("pool-replace-rollback", "catalog-replace-rollback"), SchedulerOwner: "scheduler-replace-rollback",
		Jobs: failingQueue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool-replace-rollback", catalogID: "catalog-replace-rollback"}, CancelAuditWriter: integrationAuditWriter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := projectgraph.ServingIdentity{ProjectID: "project_replace_rollback", Environment: "prod", GenerationID: "generation_replace_rollback"}
	plan := integrationPlan(t, identity, "daily")
	input := refreshrun.RunInput{RunID: "replace-rollback-old", Identity: identity, SemanticModelID: "semantic_sales", PipelineID: "pipeline_daily", PipelinePlan: &plan, InvocationSource: "schedule", MatchingScheduleIDs: []string{"daily"}, NominalTime: time.Now().UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), ConcurrencyPolicy: "Forbid", PrincipalID: "principal:replace-rollback", EstimatedMemoryBytes: 1, TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline_daily", TriggerType: refreshrun.TriggerSchedule, JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: `{}`}
	oldRun, err := createTreeRootE(t.Context(), persistence.Runs, input, nil)
	if err != nil {
		t.Fatal(err)
	}
	oldStored, err := refreshRepo.LookupRun(t.Context(), oldRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.RunID = "replace-rollback-new"
	input.NominalTime = time.Now().UTC().Add(time.Second).Truncate(time.Microsecond).Format(time.RFC3339Nano)
	input.ConcurrencyPolicy = "Replace"
	if _, err := createTreeRootE(t.Context(), persistence.Runs, input, nil); err == nil {
		t.Fatal("replace unexpectedly succeeded with failing jobs terminalization")
	}
	oldState, err := refreshRepo.LookupRun(t.Context(), oldRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if oldState.Status != refreshrun.RunStatusQueued {
		t.Fatalf("old refresh status=%q after rollback, want queued", oldState.Status)
	}
	oldJob, err := jobsRepo.Get(t.Context(), oldStored.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if oldJob.Status != "queued" {
		t.Fatalf("old job status=%q after rollback, want queued", oldJob.Status)
	}
	if _, err := refreshRepo.LookupRun(t.Context(), "replace-rollback-new"); !errors.Is(err, refreshpostgres.ErrNotFound) {
		t.Fatalf("replacement run lookup error=%v, want not found", err)
	}
}

func TestPostgresWorkerSupersedeTerminalizesRefreshTreeAndJobAtomically(t *testing.T) {
	db := modulePostgresTestDB(t)
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := jobspostgres.New(db)
	queue := NewPostgresJobsAdapter(jobsRepo, refreshRepo)
	persistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{PublicationIdentityResolver: staticPublicationIdentityResolver("pool-worker-supersede", "catalog-worker-supersede"), SchedulerOwner: "scheduler-worker-supersede", Jobs: queue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool-worker-supersede", catalogID: "catalog-worker-supersede"}, CancelAuditWriter: integrationAuditWriter{}})
	if err != nil {
		t.Fatal(err)
	}
	identity := projectgraph.ServingIdentity{ProjectID: "project_worker_supersede", Environment: "prod", GenerationID: "generation_worker_supersede"}
	plan := integrationPlan(t, identity, "daily")
	created, err := createTreeRootE(t.Context(), persistence.Runs, refreshrun.RunInput{RunID: "worker-supersede-root", Identity: identity, SemanticModelID: "semantic_sales", PipelineID: "pipeline_daily", PipelinePlan: &plan, InvocationSource: "schedule", MatchingScheduleIDs: []string{"daily"}, NominalTime: time.Now().UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), ConcurrencyPolicy: "Forbid", PrincipalID: "principal:worker-supersede", EstimatedMemoryBytes: 1, TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline_daily", TriggerType: refreshrun.TriggerSchedule, JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: `{}`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := persistence.Runs.ListExecutableJobs(t.Context(), refreshrun.ReadScope{ProjectID: identity.ProjectID, Environment: identity.Environment}, 1)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%#v err=%v", candidates, err)
	}
	claimed, ok, err := persistence.Runs.ClaimExecutableJob(t.Context(), candidates[0], "worker-supersede", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if _, err := persistence.Runs.MarkRunPrepared(t.Context(), claimed); err != nil {
		t.Fatal(err)
	}
	superseder, ok := persistence.Runs.(refreshrun.LeaseFencedSupersedeRepository)
	if !ok {
		t.Fatal("postgres run persistence lacks supersession capability")
	}
	if err := superseder.MarkRunTreeSupersededClaimed(t.Context(), claimed, "canonical delivery is stale"); err != nil {
		t.Fatal(err)
	}
	state, err := refreshRepo.LookupRun(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != refreshrun.RunStatusSuperseded {
		t.Fatalf("refresh status=%q, want superseded", state.Status)
	}
	job, err := jobsRepo.Get(t.Context(), claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "cancelled" {
		t.Fatalf("job status=%q, want cancelled", job.Status)
	}
}

func TestPostgresWorkerSupersedeRollbackKeepsRefreshAndJobLive(t *testing.T) {
	db := modulePostgresTestDB(t)
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := jobspostgres.New(db)
	baseQueue := NewPostgresJobsAdapter(jobsRepo, refreshRepo)
	failingQueue := failingCancelClaimQueue{PostgresJobsAdapter: baseQueue, err: errors.New("supersede job cancellation unavailable")}
	persistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{PublicationIdentityResolver: staticPublicationIdentityResolver("pool-worker-rollback", "catalog-worker-rollback"), SchedulerOwner: "scheduler-worker-rollback", Jobs: failingQueue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool-worker-rollback", catalogID: "catalog-worker-rollback"}, CancelAuditWriter: integrationAuditWriter{}})
	if err != nil {
		t.Fatal(err)
	}
	identity := projectgraph.ServingIdentity{ProjectID: "project_worker_rollback", Environment: "prod", GenerationID: "generation_worker_rollback"}
	plan := integrationPlan(t, identity, "daily")
	created, err := createTreeRootE(t.Context(), persistence.Runs, refreshrun.RunInput{RunID: "worker-rollback-root", Identity: identity, SemanticModelID: "semantic_sales", PipelineID: "pipeline_daily", PipelinePlan: &plan, InvocationSource: "schedule", MatchingScheduleIDs: []string{"daily"}, NominalTime: time.Now().UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), ConcurrencyPolicy: "Forbid", PrincipalID: "principal:worker-rollback", EstimatedMemoryBytes: 1, TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline_daily", TriggerType: refreshrun.TriggerSchedule, JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: `{}`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := persistence.Runs.ListExecutableJobs(t.Context(), refreshrun.ReadScope{ProjectID: identity.ProjectID, Environment: identity.Environment}, 1)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%#v err=%v", candidates, err)
	}
	claimed, ok, err := persistence.Runs.ClaimExecutableJob(t.Context(), candidates[0], "worker-rollback", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if _, err := persistence.Runs.MarkRunPrepared(t.Context(), claimed); err != nil {
		t.Fatal(err)
	}
	superseder, ok := persistence.Runs.(refreshrun.LeaseFencedSupersedeRepository)
	if !ok {
		t.Fatal("postgres run persistence lacks supersession capability")
	}
	if err := superseder.MarkRunTreeSupersededClaimed(t.Context(), claimed, "canonical delivery is stale"); err == nil {
		t.Fatal("supersession unexpectedly succeeded with failing jobs capability")
	}
	state, err := refreshRepo.LookupRun(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != refreshrun.RunStatusPrepared {
		t.Fatalf("refresh status=%q after rollback, want prepared", state.Status)
	}
	job, err := jobsRepo.Get(t.Context(), claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "running" {
		t.Fatalf("job status=%q after rollback, want running", job.Status)
	}
}

func TestPostgresPrepareVerifiedPublicationDataVersionAndSuccess(t *testing.T) {
	db := modulePostgresTestDB(t)
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := jobspostgres.New(db)
	identity := projectgraph.ServingIdentity{ProjectID: "project_publish", Environment: "prod", GenerationID: "generation_publish"}
	queue := NewPostgresJobsAdapter(jobsRepo, refreshRepo)
	physicalPoolID, catalogID := "pool-publish", "catalog-publish"
	persistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{
		PublicationIdentityResolver: staticPublicationIdentityResolver(physicalPoolID, catalogID), SchedulerOwner: "scheduler-publish",
		Jobs: queue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: physicalPoolID, catalogID: catalogID}, CancelAuditWriter: integrationAuditWriter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := deployment.NewPipelinePlan(deployment.PipelinePlan{
		ID: "pipeline_plan_publish", PipelineID: "pipeline_publish", ProjectID: identity.ProjectID.String(), Environment: identity.Environment,
		SemanticModelID: "semantic_publish", ServingGenerationID: identity.GenerationID,
		ArtifactDigest: "sha256:" + strings.Repeat("d", 64), SelectionDigest: "sha256:" + strings.Repeat("e", 64), MaterializationScope: []string{"model_publish"},
		InvocationSource: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := createTreeRootE(t.Context(), persistence.Runs, refreshrun.RunInput{
		Identity: identity, SemanticModelID: "semantic_publish", PipelineID: "pipeline_publish", PipelinePlan: &plan,
		InvocationSource: "manual", PrincipalID: "principal:publish", EstimatedMemoryBytes: 1, TargetRevision: 1, TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline_publish", TriggerType: refreshrun.TriggerManual, JobKind: refreshrun.JobKindRefreshPipeline,
		PayloadJSON: string(mustJSON(t, map[string]any{"pipelinePlan": plan})),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := refreshrun.ReadScope{ProjectID: identity.ProjectID, Environment: identity.Environment}
	candidates, err := persistence.Runs.ListExecutableJobs(t.Context(), scope, 1)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%#v err=%v", candidates, err)
	}
	claimed, ok, err := persistence.Runs.ClaimExecutableJob(t.Context(), candidates[0], "worker-publish", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	prepared, err := persistence.Runs.MarkRunPrepared(t.Context(), claimed)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Status != refreshrun.RunStatusPrepared {
		t.Fatalf("prepared status=%q", prepared.Status)
	}
	unavailablePersistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{
		PublicationIdentityResolver: unavailablePublicationIdentityResolver(), SchedulerOwner: "scheduler-publish-unavailable",
		Jobs: queue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: physicalPoolID, catalogID: catalogID}, CancelAuditWriter: integrationAuditWriter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	unavailablePublication := unavailablePersistence.Publication.(refreshrun.CanonicalPublicationUnitOfWork)
	if err := unavailablePublication.CompleteCanonicalRefresh(t.Context(), claimed, refreshrun.CanonicalRefreshResult{PlanID: plan.ID, ServingStateID: identity.GenerationID, SnapshotID: 901}); !errors.Is(err, ErrPublicationIdentityUnavailable) {
		t.Fatalf("canonical completion without admitted identity = %v, want unavailable", err)
	}
	unchanged, err := refreshRepo.LookupRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Status != refreshrun.RunStatusPrepared {
		t.Fatalf("run status after unavailable identity=%q, want prepared", unchanged.Status)
	}
	if _, found, err := refreshRepo.DataVersion(t.Context(), identity.ProjectID.String(), identity.Environment, "semantic_publish", identity.GenerationID); err != nil || found {
		t.Fatalf("data version after unavailable identity found=%v err=%v", found, err)
	}
	unchangedJob, err := jobsRepo.Get(t.Context(), unchanged.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if unchangedJob.Status != "running" {
		t.Fatalf("job status after unavailable identity=%q, want running", unchangedJob.Status)
	}
	publication, ok := persistence.Publication.(refreshrun.CanonicalPublicationUnitOfWork)
	if !ok {
		t.Fatal("postgres publication does not implement canonical unit of work")
	}
	if err := publication.CompleteCanonicalRefresh(t.Context(), claimed, refreshrun.CanonicalRefreshResult{PlanID: plan.ID, ServingStateID: identity.GenerationID, SnapshotID: 901}); err != nil {
		t.Fatal(err)
	}
	if err := publication.CompleteCanonicalRefresh(t.Context(), claimed, refreshrun.CanonicalRefreshResult{PlanID: plan.ID, ServingStateID: identity.GenerationID, SnapshotID: 901}); err != nil {
		t.Fatalf("canonical completion replay = %v", err)
	}
	tamperedJob := claimed
	tamperedJob.PayloadJSON = `{"tampered":true}`
	if err := publication.CompleteCanonicalRefresh(t.Context(), tamperedJob, refreshrun.CanonicalRefreshResult{PlanID: plan.ID, ServingStateID: identity.GenerationID, SnapshotID: 901}); !errors.Is(err, refreshpostgres.ErrConflict) {
		t.Fatalf("canonical completion payload mismatch = %v, want conflict", err)
	}
	if err := publication.CompleteCanonicalRefresh(t.Context(), claimed, refreshrun.CanonicalRefreshResult{PlanID: plan.ID, ServingStateID: identity.GenerationID, SnapshotID: 902}); !errors.Is(err, refreshpostgres.ErrConflict) {
		t.Fatalf("canonical completion snapshot mismatch = %v, want conflict", err)
	}
	finalRun, err := refreshRepo.LookupRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finalRun.Status != refreshrun.RunStatusSucceeded {
		t.Fatalf("final run status=%q", finalRun.Status)
	}
	version, found, err := refreshRepo.DataVersion(t.Context(), identity.ProjectID.String(), identity.Environment, "semantic_publish", identity.GenerationID)
	if err != nil || !found {
		t.Fatalf("data version found=%v err=%v", found, err)
	}
	if version.SnapshotID != 901 || version.PhysicalPoolID != physicalPoolID || version.CatalogID != catalogID || version.RunID != run.ID {
		t.Fatalf("data version=%#v", version)
	}
	job, err := jobsRepo.Get(t.Context(), finalRun.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "succeeded" {
		t.Fatalf("job status=%q", job.Status)
	}
}

func TestPostgresCancellationAuditFailureRollsBackRun(t *testing.T) {
	db := modulePostgresTestDB(t)
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := jobspostgres.New(db)
	identity := projectgraph.ServingIdentity{ProjectID: "project_cancel", Environment: "prod", GenerationID: "generation_cancel"}
	queue := NewPostgresJobsAdapter(jobsRepo, refreshRepo)
	plan, err := deployment.NewPipelinePlan(deployment.PipelinePlan{
		ID: "pipeline_plan_cancel", PipelineID: "pipeline_cancel", ProjectID: identity.ProjectID.String(), Environment: identity.Environment,
		SemanticModelID: "semantic_cancel", ServingGenerationID: identity.GenerationID,
		ArtifactDigest: "sha256:" + strings.Repeat("f", 64), SelectionDigest: "sha256:" + strings.Repeat("0", 64), MaterializationScope: []string{"model_cancel"}, InvocationSource: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	persistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{
		PublicationIdentityResolver: staticPublicationIdentityResolver("pool-cancel", "catalog-cancel"), SchedulerOwner: "scheduler-cancel",
		Jobs: queue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool-cancel", catalogID: "catalog-cancel"}, CancelAuditWriter: integrationAuditWriter{fail: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := createTreeRootE(t.Context(), persistence.Runs, refreshrun.RunInput{
		Identity: identity, SemanticModelID: "semantic_cancel", PipelineID: "pipeline_cancel", PipelinePlan: &plan,
		InvocationSource: "manual", PrincipalID: "principal:cancel", EstimatedMemoryBytes: 1, TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline_cancel", TriggerType: refreshrun.TriggerManual, JobKind: refreshrun.JobKindRefreshPipeline,
		PayloadJSON: string(mustJSON(t, map[string]any{"pipelinePlan": plan})),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = persistence.Runs.CancelRunWithAudit(t.Context(), identity, run.ID, &access.AuditIntent{EventID: "event-cancel", Source: "refresh", Operation: "cancel", PrincipalID: "principal:cancel", Action: "refresh.cancel", ResourceKind: "refresh_run", ResourceID: run.ID, Outcome: "cancelled"})
	if err == nil {
		t.Fatal("cancellation unexpectedly succeeded with failing audit writer")
	}
	unchanged, err := refreshRepo.LookupRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Status != refreshrun.RunStatusQueued {
		t.Fatalf("run status after audit rollback=%q", unchanged.Status)
	}
}

func TestPostgresManualRunIdentityReplayAndDistinctCommands(t *testing.T) {
	db := modulePostgresTestDB(t)
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := jobspostgres.New(db)
	identity := projectgraph.ServingIdentity{ProjectID: "project_identity", Environment: "prod", GenerationID: "generation_identity"}
	queue := NewPostgresJobsAdapter(jobsRepo, refreshRepo)
	persistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{
		PublicationIdentityResolver: staticPublicationIdentityResolver("pool-identity", "catalog-identity"), SchedulerOwner: "scheduler-identity",
		Jobs: queue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool-identity", catalogID: "catalog-identity"}, CancelAuditWriter: integrationAuditWriter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := deployment.NewPipelinePlan(deployment.PipelinePlan{
		ID: "pipeline_plan_identity", PipelineID: "pipeline_identity", ProjectID: identity.ProjectID.String(), Environment: identity.Environment,
		SemanticModelID: "semantic_identity", ServingGenerationID: identity.GenerationID,
		ArtifactDigest: "sha256:" + strings.Repeat("1", 64), SelectionDigest: "sha256:" + strings.Repeat("2", 64), MaterializationScope: []string{"model_identity"}, InvocationSource: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	input := refreshrun.RunInput{RunID: "command-identity-1", Identity: identity, SemanticModelID: "semantic_identity", PipelineID: "pipeline_identity", PipelinePlan: &plan, InvocationSource: "manual", PrincipalID: "principal:identity", EstimatedMemoryBytes: 1, TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline_identity", TriggerType: refreshrun.TriggerManual, JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: string(mustJSON(t, map[string]any{"pipelinePlan": plan}))}
	first, err := createTreeRootE(t.Context(), persistence.Runs, input, nil)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := createTreeRootE(t.Context(), persistence.Runs, input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if replay.ID != first.ID {
		t.Fatalf("replay id=%q first=%q", replay.ID, first.ID)
	}
	conflictingIdentity := input
	conflictingIdentity.PrincipalID = "principal:other"
	if _, err := createTreeRootE(t.Context(), persistence.Runs, conflictingIdentity, nil); !errors.Is(err, refreshpostgres.ErrConflict) {
		t.Fatalf("same run identity with changed canonical request error=%v, want conflict", err)
	}
	distinct := input
	distinct.RunID = "command-identity-2"
	if _, err := createTreeRootE(t.Context(), persistence.Runs, distinct, nil); !errors.Is(err, refreshpostgres.ErrConflict) {
		t.Fatalf("distinct active command error=%v, want conflict", err)
	}
	if _, err := persistence.Runs.CancelRun(t.Context(), identity, first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := createTreeRootE(t.Context(), persistence.Runs, distinct, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("distinct manual command reused first run identity")
	}
}

func TestPostgresAdvancedQueueReplayPreservesFencedRunAndJob(t *testing.T) {
	db := modulePostgresTestDB(t)
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := jobspostgres.New(db)
	queue := NewPostgresJobsAdapter(jobsRepo, refreshRepo)
	identity := projectgraph.ServingIdentity{ProjectID: "project_advanced_replay", Environment: "prod", GenerationID: "generation_advanced_replay"}
	plan, err := deployment.NewPipelinePlan(deployment.PipelinePlan{ID: "pipeline_plan_advanced_replay", PipelineID: "pipeline_advanced_replay", ProjectID: identity.ProjectID.String(), Environment: identity.Environment, SemanticModelID: "semantic_advanced_replay", ServingGenerationID: identity.GenerationID, ArtifactDigest: "sha256:" + strings.Repeat("a", 64), SelectionDigest: "sha256:" + strings.Repeat("b", 64), MaterializationScope: []string{"model_advanced_replay"}, InvocationSource: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	persistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{PublicationIdentityResolver: staticPublicationIdentityResolver("pool-advanced-replay", "catalog-advanced-replay"), SchedulerOwner: "scheduler-advanced-replay", Jobs: queue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool-advanced-replay", catalogID: "catalog-advanced-replay"}, CancelAuditWriter: integrationAuditWriter{}})
	if err != nil {
		t.Fatal(err)
	}
	input := refreshrun.RunInput{RunID: "advanced-replay-run", Identity: identity, SemanticModelID: projectgraph.ResourceID(plan.SemanticModelID), PipelineID: projectgraph.ResourceID(plan.PipelineID), PipelinePlan: &plan, InvocationSource: refreshrun.TriggerManual, PrincipalID: "principal:advanced-replay", EstimatedMemoryBytes: 1, TargetType: refreshrun.TargetRefreshPipeline, TargetID: projectgraph.ResourceID(plan.PipelineID), TriggerType: refreshrun.TriggerManual, JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: `{}`}
	first, err := createTreeRootE(t.Context(), persistence.Runs, input, nil)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := queue.ListExecutableJobs(t.Context(), refreshrun.ReadScope{ProjectID: identity.ProjectID, Environment: identity.Environment}, 1)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("advanced replay candidates=%#v err=%v", candidates, err)
	}
	claimed, ok, err := queue.ClaimExecutableJob(t.Context(), candidates[0], "advanced-replay-worker", time.Minute)
	if err != nil || !ok {
		t.Fatalf("advanced replay claim ok=%v err=%v", ok, err)
	}
	replay, err := createTreeRootE(t.Context(), persistence.Runs, input, nil)
	if err != nil || replay.ID != first.ID {
		t.Fatalf("advanced exact replay=%#v err=%v", replay, err)
	}
	storedRun, err := refreshRepo.LookupRun(t.Context(), first.ID)
	if err != nil || storedRun.Status != refreshrun.RunStatusRunning || storedRun.AttemptCount != 1 {
		t.Fatalf("advanced replay run=%#v err=%v", storedRun, err)
	}
	storedJob, err := jobsRepo.Get(t.Context(), claimed.ID)
	if err != nil || storedJob.Status != jobs.StatusRunning || storedJob.Attempts != 1 {
		t.Fatalf("advanced replay job=%#v err=%v", storedJob, err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
