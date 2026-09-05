package refreshpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/google/uuid"
)

const (
	nativeExecutorTarget       = "target-prod"
	nativeExecutorProject      = "project-prod"
	nativeExecutorEnvironment  = "prod"
	nativeExecutorBase         = "0198f2c0-7c7a-7f00-8a11-000000000101"
	nativeExecutorPlan         = "0198f2c0-7c7a-7f00-8a11-000000000102"
	nativeExecutorBuild        = "0198f2c0-7c7a-7f00-8a11-000000000103"
	nativeExecutorCandidate    = "0198f2c0-7c7a-7f00-8a11-000000000104"
	nativeExecutorSeal         = "0198f2c0-7c7a-7f00-8a11-000000000105"
	nativeExecutorResult       = "0198f2c0-7c7a-7f00-8a11-000000000106"
	nativeExecutorLease        = "0198f2c0-7c7a-7f00-8a11-000000000107"
	nativeExecutorSourceDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	nativeExecutorAttestation  = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	nativeExecutorPlanDigest   = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	nativeExecutorArtifact     = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

func TestPostgresNativeRefreshExecutorBuildsAndRecoversExactSeal(t *testing.T) {
	job := nativeExecutorJob()
	basePlan := nativeExecutorBasePlan(t)
	reader := &nativeExecutorReader{
		snapshot: deploymentnative.DeliveryOperatorSnapshot{TargetID: nativeExecutorTarget, ProjectID: nativeExecutorProject, Environment: nativeExecutorEnvironment, TargetRevision: 9, ActiveGenerationID: nativeExecutorBase},
		plans:    map[string]deploymentnative.DeliveryPlan{nativeExecutorPlan: basePlan},
		generations: map[string]deploymentnative.DeliveryGeneration{
			nativeExecutorBase:   {GenerationID: nativeExecutorBase, TargetID: nativeExecutorTarget, PlanID: nativeExecutorPlan, PlanDigest: basePlan.PlanDigest, CandidateID: nativeExecutorCandidate, SnapshotSealID: nativeExecutorSeal},
			nativeExecutorResult: {GenerationID: nativeExecutorResult, TargetID: nativeExecutorTarget, PlanID: nativeExecutorPlan, PlanDigest: nativeExecutorPlanDigest, CandidateID: nativeExecutorCandidate, SnapshotSealID: nativeExecutorSeal, ServingArtifactDigest: nativeExecutorArtifact},
		},
		attempt:   deploymentnative.DeliveryBuildAttempt{AttemptID: nativeExecutorBuild, PlanID: nativeExecutorPlan, CandidateID: nativeExecutorCandidate, State: deploymentnative.AttemptCommitted, SnapshotID: 42},
		seal:      deploymentnative.SnapshotSeal{SealID: nativeExecutorSeal, AttemptID: nativeExecutorBuild, CandidateID: nativeExecutorCandidate, PlanDigest: nativeExecutorPlanDigest, DuckLakeSnapshotID: 42},
		candidate: deploymentnative.DeliveryCandidate{CandidateID: nativeExecutorCandidate, TargetID: nativeExecutorTarget, PlanID: nativeExecutorPlan, AttemptID: nativeExecutorBuild, SnapshotSealID: nativeExecutorSeal, Status: "qualified"},
	}
	mutations := &nativeExecutorMutations{
		plan:  deploymentmodule.NativeDeliveryPlan{ID: uuid.MustParse(nativeExecutorPlan), ProjectID: job.Identity.ProjectID, TargetID: nativeExecutorTarget, Environment: nativeExecutorEnvironment, Operation: string(deployment.DeliveryOperationRestatement), SourceDigest: nativeExecutorSourceDigest, SourceAttestationDigest: nativeExecutorAttestation, BaseGenerationID: uuid.MustParse(nativeExecutorBase), BaseTargetRevision: 9, PlanDigest: nativeExecutorPlanDigest, Status: "planned"},
		build: deploymentmodule.NativeDeliveryBuild{ID: uuid.MustParse(nativeExecutorBuild), PlanID: uuid.MustParse(nativeExecutorPlan), PlanDigest: nativeExecutorPlanDigest, SourceDigest: nativeExecutorSourceDigest, BaseGenerationID: uuid.MustParse(nativeExecutorBase), ServingArtifactDigest: nativeExecutorArtifact, WriterLeaseID: uuid.MustParse(nativeExecutorLease), ServingStateID: uuid.MustParse(nativeExecutorResult), SealID: uuid.MustParse(nativeExecutorSeal), CandidateID: uuid.MustParse(nativeExecutorCandidate), Status: "sealed"},
	}
	executor, err := NewPostgresNativeRefreshExecutor(mutations, reader, nativeExecutorTarget)
	if err != nil {
		t.Fatalf("construct native refresh executor: %v", err)
	}
	result, err := executor.Execute(t.Context(), job)
	if err != nil {
		t.Fatalf("execute native refresh: %v", err)
	}
	if result != (refreshrun.CanonicalRefreshResult{PlanID: nativeExecutorPlan, ServingStateID: nativeExecutorResult, NativeGenerationID: nativeExecutorResult, SnapshotID: 42}) {
		t.Fatalf("result = %#v", result)
	}
	if mutations.planRequest.IdempotencyKey != "refresh-plan-"+job.RunID || mutations.buildRequest.IdempotencyKey != "refresh-build-"+job.RunID {
		t.Fatalf("idempotency keys = %q/%q", mutations.planRequest.IdempotencyKey, mutations.buildRequest.IdempotencyKey)
	}
	if mutations.planRequest.SourceOwnerID != "source-owner" || mutations.planRequest.SourceDigest != nativeExecutorSourceDigest || mutations.planRequest.SourceAttestationDigest != nativeExecutorAttestation {
		t.Fatalf("plan request source evidence = %#v", mutations.planRequest)
	}
	if !mutations.planCompleted || !mutations.buildCompleted {
		t.Fatalf("native command completion calls = plan:%t build:%t", mutations.planCompleted, mutations.buildCompleted)
	}
}

func TestPostgresNativeRefreshExecutorRejectsChangedActiveBase(t *testing.T) {
	job := nativeExecutorJob()
	reader := &nativeExecutorReader{snapshot: deploymentnative.DeliveryOperatorSnapshot{TargetID: nativeExecutorTarget, ProjectID: nativeExecutorProject, Environment: nativeExecutorEnvironment, TargetRevision: 9, ActiveGenerationID: nativeExecutorResult}}
	mutations := &nativeExecutorMutations{}
	executor, err := NewPostgresNativeRefreshExecutor(mutations, reader, nativeExecutorTarget)
	if err != nil {
		t.Fatalf("construct native refresh executor: %v", err)
	}
	_, err = executor.Execute(t.Context(), job)
	if !errors.Is(err, refreshrun.ErrRunStale) {
		t.Fatalf("error = %v, want stale run", err)
	}
	if mutations.planRequest.IdempotencyKey != "" || mutations.buildRequest.IdempotencyKey != "" {
		t.Fatal("changed active base reached native mutation")
	}
}

func TestPostgresNativeRefreshExecutorRejectsChangedTargetFence(t *testing.T) {
	job := nativeExecutorJob()
	reader := &nativeExecutorReader{snapshot: deploymentnative.DeliveryOperatorSnapshot{TargetID: nativeExecutorTarget, ProjectID: nativeExecutorProject, Environment: nativeExecutorEnvironment, TargetRevision: job.TargetRevision + 1, ActiveGenerationID: nativeExecutorBase}}
	mutations := &nativeExecutorMutations{}
	executor, err := NewPostgresNativeRefreshExecutor(mutations, reader, nativeExecutorTarget)
	if err != nil {
		t.Fatalf("construct native refresh executor: %v", err)
	}
	_, err = executor.Execute(t.Context(), job)
	if !errors.Is(err, refreshrun.ErrRunStale) {
		t.Fatalf("error = %v, want stale run", err)
	}
	if mutations.planRequest.IdempotencyKey != "" {
		t.Fatal("changed target revision reached native planning")
	}
}

func TestPostgresNativeRefreshExecutorPropagatesPlanCompletionFailure(t *testing.T) {
	job := nativeExecutorJob()
	basePlan := nativeExecutorBasePlan(t)
	reader := nativeExecutorReaderFixture(basePlan)
	mutations := &nativeExecutorMutations{plan: deploymentmodule.NativeDeliveryPlan{ID: uuid.MustParse(nativeExecutorPlan), ProjectID: job.Identity.ProjectID, TargetID: nativeExecutorTarget, Environment: nativeExecutorEnvironment, Operation: string(deployment.DeliveryOperationRestatement), SourceDigest: nativeExecutorSourceDigest, SourceAttestationDigest: nativeExecutorAttestation, BaseGenerationID: uuid.MustParse(nativeExecutorBase), BaseTargetRevision: job.TargetRevision, PlanDigest: nativeExecutorPlanDigest, Status: "planned"}, planCompletionErr: errors.New("plan evidence mismatch")}
	executor, err := NewPostgresNativeRefreshExecutor(mutations, reader, nativeExecutorTarget)
	if err != nil {
		t.Fatalf("construct native refresh executor: %v", err)
	}
	_, err = executor.Execute(t.Context(), job)
	if err == nil || !strings.Contains(err.Error(), "plan evidence mismatch") {
		t.Fatalf("error = %v, want plan completion error", err)
	}
	if mutations.buildRequest.IdempotencyKey != "" {
		t.Fatal("plan completion failure reached native build")
	}
}

func nativeExecutorJob() refreshrun.JobRecord {
	plan, _ := deployment.NewPipelinePlan(deployment.PipelinePlan{
		ID: "pipeline-plan", PipelineID: "pipeline-prod", ProjectID: nativeExecutorProject, Environment: nativeExecutorEnvironment, SemanticModelID: "semantic-prod", ServingGenerationID: nativeExecutorBase,
		ArtifactDigest: nativeExecutorSourceDigest, SelectionDigest: nativeExecutorPlanDigest, MaterializationScope: []string{"model-prod"}, QualificationChecks: []string{"compatibility"},
	})
	return refreshrun.JobRecord{ID: "job-refresh", Identity: projectgraph.ServingIdentity{ProjectID: nativeExecutorProject, Environment: nativeExecutorEnvironment, GenerationID: nativeExecutorBase}, SemanticModelID: "semantic-prod", PipelineID: "pipeline-prod", PipelinePlan: &plan, PrincipalID: "principal-refresh", RunID: "run-refresh", TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline-prod", TargetRevision: 9, TriggerType: refreshrun.TriggerManual, Kind: refreshrun.JobKindRefreshPipeline, EstimatedMemoryBytes: 1}
}

func nativeExecutorBasePlan(t *testing.T) deploymentnative.DeliveryPlan {
	now := time.Now().UTC().Truncate(time.Microsecond)
	rich, err := deployment.NewDeliveryPlan(deployment.DeliveryPlan{
		ID: nativeExecutorPlan, ActorID: "source-owner", SourceOwnerID: "source-owner", TargetID: nativeExecutorTarget, ProjectID: nativeExecutorProject, Environment: nativeExecutorEnvironment,
		Operation: deployment.DeliveryOperationCodeChange, SourceDigest: nativeExecutorSourceDigest, ServingArtifactDigest: nativeExecutorArtifact, BaseGenerationID: nativeExecutorBase, BaseTargetRevision: 9,
		Execution:  deployment.DeliveryExecutionInputs{SourceArtifactDigest: nativeExecutorSourceDigest, CompilerDigest: nativeExecutorPlanDigest, ExecutableDigest: nativeExecutorPlanDigest, DependencyDigest: nativeExecutorPlanDigest, ConfigDigest: nativeExecutorPlanDigest, BindingDigest: nativeExecutorPlanDigest, RuntimeDigest: nativeExecutorPlanDigest, CapabilityDigest: nativeExecutorPlanDigest},
		Provenance: deployment.DeliveryProvenance{AttestationDigest: nativeExecutorAttestation},
		Governance: deployment.DeliveryGovernance{PolicyDigest: nativeExecutorPlanDigest, AuthorizationDigest: nativeExecutorPlanDigest, QualificationDigest: nativeExecutorPlanDigest, ApprovalPolicyRevision: 1, ExpiresAt: now.Add(time.Hour)},
		Evidence:   deployment.DeliveryPlanEvidence{ImpactStatement: "impact", PhysicalWorkStatement: "physical", ReuseStatement: "reuse", Qualification: deployment.DeliveryQualificationEvidence{Policy: "default", Steps: []deployment.DeliveryQualificationStep{{ID: "compatibility", Kind: "compatibility", Description: "compatibility"}}}, StalePolicy: deployment.DeliveryStalePolicy{Mode: "reject"}, Rollback: deployment.DeliveryRollbackEvidence{Class: deployment.DeliveryServingSafe}},
		CreatedAt:  now,
	})
	if err != nil {
		t.Fatalf("construct base plan: %v", err)
	}
	document, err := json.Marshal(rich)
	if err != nil {
		t.Fatal(err)
	}
	return deploymentnative.DeliveryPlan{PlanID: rich.ID, TargetID: nativeExecutorTarget, PlanDigest: rich.Digest, CompiledConfigDigest: rich.Execution.ConfigDigest, SecurityDomainFingerprint: rich.Governance.AuthorizationDigest, ArtifactDigest: rich.ServingArtifactDigest, QualificationDigest: rich.Governance.QualificationDigest, ApprovalRequired: rich.Governance.RequiresApproval, ApprovalPolicyRevision: rich.Governance.ApprovalPolicyRevision, PlanDocument: document}
}

type nativeExecutorMutations struct {
	plan                                  deploymentmodule.NativeDeliveryPlan
	build                                 deploymentmodule.NativeDeliveryBuild
	planRequest                           deploymentmodule.NativeDeliveryPlanRequest
	buildRequest                          deploymentmodule.NativeDeliveryBuildRequest
	planCompleted, buildCompleted         bool
	planCompletionErr, buildCompletionErr error
}

func (m *nativeExecutorMutations) CreatePlan(_ context.Context, request deploymentmodule.NativeDeliveryPlanRequest) (deploymentmodule.NativeDeliveryPlan, error) {
	m.planRequest = request
	return m.plan, nil
}

func (m *nativeExecutorMutations) BuildPlan(_ context.Context, request deploymentmodule.NativeDeliveryBuildRequest) (deploymentmodule.NativeDeliveryBuild, error) {
	m.buildRequest = request
	return m.build, nil
}

func (m *nativeExecutorMutations) CompleteNativePlanCommand(context.Context, deploymentmodule.NativeDeliveryPlan) error {
	m.planCompleted = true
	return m.planCompletionErr
}

func (m *nativeExecutorMutations) CompleteNativeBuildCommand(context.Context, deploymentmodule.NativeDeliveryBuild) error {
	m.buildCompleted = true
	return m.buildCompletionErr
}

type nativeExecutorReader struct {
	snapshot    deploymentnative.DeliveryOperatorSnapshot
	plans       map[string]deploymentnative.DeliveryPlan
	generations map[string]deploymentnative.DeliveryGeneration
	attempt     deploymentnative.DeliveryBuildAttempt
	seal        deploymentnative.SnapshotSeal
	candidate   deploymentnative.DeliveryCandidate
}

func (r *nativeExecutorReader) OperatorSnapshot(context.Context, string) (deploymentnative.DeliveryOperatorSnapshot, error) {
	return r.snapshot, nil
}
func (r *nativeExecutorReader) LoadPlan(_ context.Context, id string) (deploymentnative.DeliveryPlan, error) {
	plan, ok := r.plans[id]
	if !ok {
		return deploymentnative.DeliveryPlan{}, deploymentnative.ErrNotFound
	}
	return plan, nil
}
func (r *nativeExecutorReader) LoadBuildAttempt(context.Context, string) (deploymentnative.DeliveryBuildAttempt, error) {
	return r.attempt, nil
}
func (r *nativeExecutorReader) LoadSnapshotSeal(context.Context, string) (deploymentnative.SnapshotSeal, error) {
	return r.seal, nil
}
func (r *nativeExecutorReader) LoadCandidate(context.Context, string) (deploymentnative.DeliveryCandidate, error) {
	return r.candidate, nil
}
func (r *nativeExecutorReader) LoadGeneration(_ context.Context, id string) (deploymentnative.DeliveryGeneration, error) {
	generation, ok := r.generations[id]
	if !ok {
		return deploymentnative.DeliveryGeneration{}, deploymentnative.ErrNotFound
	}
	return generation, nil
}

var _ NativeRefreshDeliveryReader = (*nativeExecutorReader)(nil)

func nativeExecutorReaderFixture(basePlan deploymentnative.DeliveryPlan) *nativeExecutorReader {
	return &nativeExecutorReader{
		snapshot: deploymentnative.DeliveryOperatorSnapshot{TargetID: nativeExecutorTarget, ProjectID: nativeExecutorProject, Environment: nativeExecutorEnvironment, TargetRevision: 9, ActiveGenerationID: nativeExecutorBase},
		plans:    map[string]deploymentnative.DeliveryPlan{nativeExecutorPlan: basePlan},
		generations: map[string]deploymentnative.DeliveryGeneration{
			nativeExecutorBase: {GenerationID: nativeExecutorBase, TargetID: nativeExecutorTarget, PlanID: nativeExecutorPlan, PlanDigest: basePlan.PlanDigest, CandidateID: nativeExecutorCandidate, SnapshotSealID: nativeExecutorSeal},
		},
	}
}

func TestNativeExecutorConstantsRemainCanonical(t *testing.T) {
	for _, id := range []string{nativeExecutorBase, nativeExecutorPlan, nativeExecutorBuild, nativeExecutorCandidate, nativeExecutorSeal, nativeExecutorResult, nativeExecutorLease} {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed.String() != strings.TrimSpace(id) || parsed.Version() != 7 {
			t.Fatalf("id %q is not UUIDv7: %v", id, err)
		}
	}
}
