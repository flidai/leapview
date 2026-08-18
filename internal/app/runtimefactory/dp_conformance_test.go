package runtimefactory

import (
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

func TestReuseEvidenceCoverageRejectsMismatchedRelationSet(t *testing.T) {
	artifacts := release.CandidateArtifactSet{Compiler: release.CandidateCompilerEvidence{RelationExecution: map[string]string{
		"model_orders":    "sha256:" + strings.Repeat("a", 64),
		"model_customers": "sha256:" + strings.Repeat("b", 64),
	}}}
	basePlan := func(decisions ...deployment.DeliveryReuseDecision) *deployment.DeliveryPlan {
		return &deployment.DeliveryPlan{Evidence: deployment.DeliveryPlanEvidence{Reuse: decisions}}
	}
	decision := func(resourceID string, reusable bool) deployment.DeliveryReuseDecision {
		return deployment.DeliveryReuseDecision{ResourceID: resourceID, Reusable: reusable}
	}
	for name, plan := range map[string]*deployment.DeliveryPlan{
		"missing relation":   basePlan(decision("model_orders", true)),
		"unknown relation":   basePlan(decision("model_orders", true), decision("model_missing", true)),
		"duplicate relation": basePlan(decision("model_orders", true), decision("model_orders", false)),
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateReuseEvidenceCoverage(plan, artifacts, "candidate_target"); err == nil {
				t.Fatalf("reuse evidence mismatch unexpectedly accepted: %#v", plan.Evidence.Reuse)
			}
		})
	}
	if err := validateReuseEvidenceCoverage(basePlan(decision("model_orders", true), decision("model_customers", false)), artifacts, "candidate_target"); err != nil {
		t.Fatalf("exact relation reuse evidence rejected: %v", err)
	}
	if err := validateReuseEvidenceCoverage(basePlan(decision("wrong_candidate", true)), release.CandidateArtifactSet{}, "candidate_target"); err == nil {
		t.Fatal("candidate-level reuse evidence with wrong identity was accepted")
	}
}

// TestPromotionReplansPortableArtifactForEachDestinationTarget proves that
// promotion carries the same retained bytes while deriving a new target-bound
// plan/candidate identity and target-specific execution configuration.
func TestPromotionReplansPortableArtifactForEachDestinationTarget(t *testing.T) {
	projectID := projectgraph.ResourceID("project_promotion")
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "portable-inspection")
	if err != nil {
		t.Fatal(err)
	}
	portableDigest := "sha256:" + strings.Repeat("a", 64)
	artifacts := release.CandidateArtifactSet{
		Artifact: release.ProjectArtifactProvenance{
			SourceDigest: portableDigest, ProjectDigest: "sha256:" + strings.Repeat("b", 64),
			CompilerVersion: "compiler:v1", SchemaVersion: 1,
		},
		Generation: release.CandidateGenerationArtifact{
			Identity: identity, DataRevision: "sources:portable", DataMode: release.GenerationDataRefreshSources, Deterministic: true,
		},
		Compiler: release.CandidateCompilerEvidence{Plan: projectcompiler.ProjectPlan{Project: "project_promotion"}},
	}
	newInput := func(targetID, candidateID string) deployment.DeliveryCandidateBuildInput {
		return deployment.DeliveryCandidateBuildInput{
			ProjectID: projectID, OwnerID: "publisher", ArtifactDigest: portableDigest,
			Candidate: deployment.Candidate{ID: candidateID, TargetID: targetID, Scope: deployment.CandidateScope{ProjectID: projectID, Environment: "prod"}},
		}
	}
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	sourcePlan, err := CandidatePlanRequest(newInput("target_source", "candidate_source"), artifacts, "runtime:v1", now)
	if err != nil {
		t.Fatal(err)
	}
	destinationPlan, err := CandidatePlanRequest(newInput("target_destination", "candidate_destination"), artifacts, "runtime:v1", now)
	if err != nil {
		t.Fatal(err)
	}
	if sourcePlan.SourceDigest != destinationPlan.SourceDigest || sourcePlan.Execution.MaterializationDigest != destinationPlan.Execution.MaterializationDigest {
		t.Fatalf("promotion changed portable source evidence: source=%#v destination=%#v", sourcePlan, destinationPlan)
	}
	if sourcePlan.TargetID == destinationPlan.TargetID || sourcePlan.ID == destinationPlan.ID {
		t.Fatalf("promotion reused target-bound plan identity: source=%q/%q destination=%q/%q", sourcePlan.TargetID, sourcePlan.ID, destinationPlan.TargetID, destinationPlan.ID)
	}
	if sourcePlan.Execution.ConfigDigest == destinationPlan.Execution.ConfigDigest {
		t.Fatalf("promotion reused target-specific execution configuration: %q", sourcePlan.Execution.ConfigDigest)
	}
	if newInput("target_source", "candidate_source").Candidate.ID == newInput("target_destination", "candidate_destination").Candidate.ID {
		t.Fatal("promotion candidate identities are not destination-specific")
	}
}
