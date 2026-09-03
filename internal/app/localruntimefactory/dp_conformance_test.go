package localruntimefactory

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/deployment"
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
