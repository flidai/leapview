package localruntimefactory

import (
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/runtimefactory"
	"github.com/flidai/leapview/internal/deployment"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

func TestRestatementPlanUsesExplicitCandidateLevelFullRefresh(t *testing.T) {
	projectID := projectgraph.ResourceID("project_delivery")
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "candidate_restatement")
	if err != nil {
		t.Fatal(err)
	}
	artifacts := release.CandidateArtifactSet{
		Artifact:                 release.ProjectArtifactProvenance{SourceDigest: deliveryPlanDigest('a'), ProjectDigest: deliveryPlanDigest('b'), CompilerVersion: "compiler:v1", SchemaVersion: 1},
		AuthorizationFingerprint: deliveryPlanDigest('c'),
		Generation:               release.CandidateGenerationArtifact{Identity: identity, DataRevision: "sources:revision", DataMode: release.GenerationDataRefreshSources, Deterministic: true},
		Compiler:                 release.CandidateCompilerEvidence{Plan: projectcompiler.ProjectPlan{Project: "project_delivery"}, RelationExecution: map[string]string{"model_orders": deliveryPlanDigest('1')}, BaseRelationExecution: map[string]string{"model_orders": deliveryPlanDigest('1')}},
	}
	input := deployment.DeliveryCandidateBuildInput{ProjectID: projectID, OwnerID: "owner_1", ArtifactDigest: artifacts.Artifact.SourceDigest, Operation: deployment.DeliveryOperationRestatement, Candidate: deployment.Candidate{ID: "candidate_restatement", TargetID: "target_prod", Scope: deployment.CandidateScope{ProjectID: projectID, Environment: "prod", BaseGenerationID: "generation_0"}}}
	request, err := runtimefactory.CandidatePlanRequestWithPolicyAndReuse(input, artifacts, "runtime:v1", runtimefactory.CandidateDeliveryPolicy{ApprovalPolicyRevision: runtimefactory.CurrentApprovalPolicyRevision}, time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC), &deployment.DeliveryReuseInput{
		BaseExecutionDigest: deliveryPlanDigest('1'), CatalogDigest: deliveryPlanDigest('2'), BaseCatalogDigest: deliveryPlanDigest('2'), PhysicalPoolID: "pool-1", BasePhysicalPoolID: "pool-1", CompatibilityDigest: deliveryPlanDigest('3'), BaseCompatibilityDigest: deliveryPlanDigest('3'), Deterministic: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Evidence.Reuse) != 1 || request.Evidence.Reuse[0].ResourceID != input.Candidate.ID || request.Evidence.Reuse[0].Reusable || request.Evidence.Reuse[0].RetainBase {
		t.Fatalf("restatement reuse evidence = %#v, want explicit candidate-level full refresh", request.Evidence.Reuse)
	}
	plan := &deployment.DeliveryPlan{Operation: deployment.DeliveryOperationRestatement, Evidence: request.Evidence}
	if err := validateReuseEvidenceCoverage(plan, artifacts, input.Candidate.ID); err != nil {
		t.Fatalf("restatement candidate-level evidence rejected: %v", err)
	}
}

func TestRestatementPlanEvidenceAcceptsStablePlanIdentityDuringPhysicalBuild(t *testing.T) {
	artifacts := release.CandidateArtifactSet{Compiler: release.CandidateCompilerEvidence{RelationExecution: map[string]string{"model_orders": deliveryPlanDigest('1')}}}
	plan := &deployment.DeliveryPlan{ID: "plan-planning-candidate", Operation: deployment.DeliveryOperationRestatement, Evidence: deployment.DeliveryPlanEvidence{Reuse: []deployment.DeliveryReuseDecision{{ResourceID: "planning-candidate", Reason: "operation requires explicit full materialization"}}}}
	if err := validateReuseEvidenceCoverage(plan, artifacts, "candidate-physical-build"); err != nil {
		t.Fatalf("durable restatement plan identity rejected during physical build: %v", err)
	}
	plan.Evidence.Reuse[0].Reusable = true
	if err := validateReuseEvidenceCoverage(plan, artifacts, "candidate-physical-build"); err == nil {
		t.Fatal("durable restatement plan identity retained a base")
	}
}
