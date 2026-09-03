package localruntimefactory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/candidatecatalog"
	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/release"
)

func TestCandidateRunnerForcesRestatementRefreshFromReuseBase(t *testing.T) {
	baseCalled := false
	runner := &candidateCatalogRunner{
		config: CandidateCatalogRunnerConfig{Base: func(context.Context, deployment.DeliveryBuildInput) (*candidatecatalog.SealedArtifact, error) {
			baseCalled = true
			return nil, errors.New("restatement base resolver must not run")
		}},
		input:     deployment.DeliveryCandidateBuildInput{Candidate: deployment.Candidate{ID: "candidate-restatement"}},
		artifacts: release.CandidateArtifactSet{Generation: release.CandidateGenerationArtifact{DataMode: release.GenerationDataReuseBase}},
	}
	plan := deployment.DeliveryPlan{Operation: deployment.DeliveryOperationRestatement, BaseGenerationID: "generation-1", Evidence: deployment.DeliveryPlanEvidence{Reuse: []deployment.DeliveryReuseDecision{{ResourceID: "candidate-restatement", Reason: "operation requires explicit full materialization"}}}}
	_, err := runner.Construct(context.Background(), deployment.DeliveryBuildInput{Plan: plan})
	if err == nil || !strings.Contains(err.Error(), "remains reuse_base") || baseCalled {
		t.Fatalf("restatement artifact mismatch: err=%v baseCalled=%v", err, baseCalled)
	}
	if runner.artifacts.Generation.DataMode != release.GenerationDataReuseBase {
		t.Fatalf("restatement data mode mutated to %q", runner.artifacts.Generation.DataMode)
	}
}

func TestReuseEvidenceCoverageRequiresExactCurrentRelations(t *testing.T) {
	artifacts := release.CandidateArtifactSet{Compiler: release.CandidateCompilerEvidence{RelationExecution: map[string]string{
		"model_orders": deliveryPlanDigest('1'), "model_customers": deliveryPlanDigest('2'),
	}}}
	valid := func(ids ...string) *deployment.DeliveryPlan {
		decisions := make([]deployment.DeliveryReuseDecision, len(ids))
		for i, id := range ids {
			decisions[i] = deployment.DeliveryReuseDecision{ResourceID: id, Reusable: true}
		}
		return &deployment.DeliveryPlan{Evidence: deployment.DeliveryPlanEvidence{Reuse: decisions}}
	}
	for name, plan := range map[string]*deployment.DeliveryPlan{
		"missing":   valid("model_orders"),
		"unknown":   valid("model_orders", "model_regions"),
		"duplicate": valid("model_orders", "model_orders"),
	} {
		if err := validateReuseEvidenceCoverage(plan, artifacts, "candidate-1"); err == nil {
			t.Errorf("%s relation evidence unexpectedly accepted", name)
		}
	}
	if err := validateReuseEvidenceCoverage(valid("model_orders", "model_customers"), artifacts, "candidate-1"); err != nil {
		t.Fatalf("exact relation evidence rejected: %v", err)
	}
	if err := validateReuseEvidenceCoverage(&deployment.DeliveryPlan{Evidence: deployment.DeliveryPlanEvidence{Reuse: []deployment.DeliveryReuseDecision{{ResourceID: "other", Reusable: true}}}}, release.CandidateArtifactSet{}, "candidate-1"); err == nil {
		t.Fatal("candidate-level evidence accepted wrong resource ID")
	}
}

func TestCandidateRunnerRejectsPartialRelationEvidenceBeforeBase(t *testing.T) {
	baseCalled := false
	runner := &candidateCatalogRunner{
		config: CandidateCatalogRunnerConfig{Base: func(context.Context, deployment.DeliveryBuildInput) (*candidatecatalog.SealedArtifact, error) {
			baseCalled = true
			return nil, errors.New("base resolver must not run")
		}},
		input: deployment.DeliveryCandidateBuildInput{Candidate: deployment.Candidate{ID: "candidate-1"}},
		artifacts: release.CandidateArtifactSet{Compiler: release.CandidateCompilerEvidence{RelationExecution: map[string]string{
			"model_orders": deliveryPlanDigest('1'), "model_customers": deliveryPlanDigest('2'),
		}}},
	}
	plan := deployment.DeliveryPlan{BaseGenerationID: "generation-1", Evidence: deployment.DeliveryPlanEvidence{Reuse: []deployment.DeliveryReuseDecision{{ResourceID: "model_orders", Reusable: true}}}}
	if _, err := runner.Construct(context.Background(), deployment.DeliveryBuildInput{Plan: plan}); err == nil || baseCalled {
		t.Fatalf("partial relation evidence err=%v baseCalled=%v", err, baseCalled)
	}
}

func TestCandidateRunnerRebuildsWhenReuseDecisionMismatches(t *testing.T) {
	basePlan := deployment.DeliveryPlan{Evidence: deployment.DeliveryPlanEvidence{Reuse: []deployment.DeliveryReuseDecision{{ResourceID: "candidate_1", Reusable: false, Reason: "catalog compatibility identity changed"}}}}
	baseCalled := false
	runner := &candidateCatalogRunner{
		config: CandidateCatalogRunnerConfig{
			Base: func(context.Context, deployment.DeliveryBuildInput) (*candidatecatalog.SealedArtifact, error) {
				baseCalled = true
				return nil, nil
			},
		},
		input:     deployment.DeliveryCandidateBuildInput{Candidate: deployment.Candidate{ID: "candidate_1"}},
		artifacts: release.CandidateArtifactSet{Generation: release.CandidateGenerationArtifact{DataMode: release.GenerationDataReuseBase}},
	}
	_, err := runner.Construct(context.Background(), deployment.DeliveryBuildInput{Plan: basePlan})
	if err == nil || baseCalled {
		t.Fatalf("mismatching reuse decision err=%v baseCalled=%v", err, baseCalled)
	}
	if runner.artifacts.Generation.DataMode != release.GenerationDataReuseBase {
		t.Fatalf("mismatching reuse decision mutated data mode to %q", runner.artifacts.Generation.DataMode)
	}
}

func TestCandidateRunnerUsesBaseForExactReuseDecision(t *testing.T) {
	baseCalled := false
	runner := &candidateCatalogRunner{
		config: CandidateCatalogRunnerConfig{
			Base: func(context.Context, deployment.DeliveryBuildInput) (*candidatecatalog.SealedArtifact, error) {
				baseCalled = true
				return nil, errors.New("base resolver reached")
			},
		},
		input:     deployment.DeliveryCandidateBuildInput{Candidate: deployment.Candidate{ID: "candidate_1"}},
		artifacts: release.CandidateArtifactSet{Generation: release.CandidateGenerationArtifact{DataMode: release.GenerationDataReuseBase}},
	}
	plan := deployment.DeliveryPlan{BaseGenerationID: "generation-1", Evidence: deployment.DeliveryPlanEvidence{Reuse: []deployment.DeliveryReuseDecision{{ResourceID: "candidate_1", Reusable: true, Reason: "exact identity"}}}}
	_, err := runner.Construct(context.Background(), deployment.DeliveryBuildInput{Plan: plan})
	if err == nil || !strings.Contains(err.Error(), "base resolver reached") || !baseCalled {
		t.Fatalf("exact reuse err=%v baseCalled=%v", err, baseCalled)
	}
}

func TestCandidateRunnerMissingReuseDecisionRebuilds(t *testing.T) {
	baseCalled := false
	runner := &candidateCatalogRunner{
		config: CandidateCatalogRunnerConfig{
			Base: func(context.Context, deployment.DeliveryBuildInput) (*candidatecatalog.SealedArtifact, error) {
				baseCalled = true
				return nil, errors.New("base resolver must not run")
			},
		},
		input:     deployment.DeliveryCandidateBuildInput{Candidate: deployment.Candidate{ID: "candidate_1"}},
		artifacts: release.CandidateArtifactSet{Generation: release.CandidateGenerationArtifact{DataMode: release.GenerationDataReuseBase}},
	}
	_, err := runner.Construct(context.Background(), deployment.DeliveryBuildInput{Plan: deployment.DeliveryPlan{BaseGenerationID: "generation-1"}})
	if err == nil || baseCalled {
		t.Fatalf("missing reuse decision err=%v baseCalled=%v", err, baseCalled)
	}
	if runner.artifacts.Generation.DataMode != release.GenerationDataReuseBase {
		t.Fatalf("missing reuse decision mutated data mode to %q", runner.artifacts.Generation.DataMode)
	}
}

func deliveryPlanDigest(char byte) string { return "sha256:" + strings.Repeat(string(char), 64) }
