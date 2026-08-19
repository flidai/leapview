package app

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
)

func TestCanonicalRefreshExecutorBuildsRestatementFromExactActiveSource(t *testing.T) {
	reader := canonicalRefreshReaderStub{
		generation: deployment.DeliveryGeneration{CandidateID: "candidate-base", ServingStateID: "state-active"},
		candidate:  deployment.DeliveryCandidate{PlanID: "plan-base"},
		plan: deployment.DeliveryPlan{
			ActorID: "principal-source-owner", SourceDigest: "sha256:source",
			Provenance: deployment.DeliveryProvenance{AttestationDigest: "sha256:attestation"},
		},
	}
	mutations := &canonicalRefreshMutationStub{}
	execute := canonicalRefreshExecutor(mutations, reader, "target-evaluation")
	err := execute(t.Context(), refreshrun.JobRecord{
		RunID: "run-refresh", PrincipalID: "principal-refresh",
		Identity: projectgraph.ServingIdentity{ProjectID: "project:test", Environment: "evaluation", GenerationID: "state-active"},
	})
	if err != nil {
		t.Fatalf("canonical refresh: %v", err)
	}
	if mutations.intent.PrincipalID != "principal-refresh" || mutations.intent.Operation != deployment.DeliveryOperationRestatement || mutations.intent.SourceDigest != reader.plan.SourceDigest {
		t.Fatalf("refresh plan intent = %#v", mutations.intent)
	}
	if mutations.buildPrincipal != "principal-refresh" || mutations.publishedCandidate != "candidate-refresh" {
		t.Fatalf("build principal/candidate = %q/%q", mutations.buildPrincipal, mutations.publishedCandidate)
	}
}

func TestCanonicalRefreshExecutorRejectsChangedBase(t *testing.T) {
	execute := canonicalRefreshExecutor(&canonicalRefreshMutationStub{}, canonicalRefreshReaderStub{
		generation: deployment.DeliveryGeneration{ServingStateID: "state-new"},
	}, "target-evaluation")
	err := execute(t.Context(), refreshrun.JobRecord{
		RunID: "run-refresh", PrincipalID: "principal-refresh",
		Identity: projectgraph.ServingIdentity{ProjectID: "project:test", Environment: "evaluation", GenerationID: "state-old"},
	})
	if err == nil {
		t.Fatal("canonical refresh accepted a changed active generation")
	}
}

type canonicalRefreshReaderStub struct {
	deployment.DeliveryReader
	generation deployment.DeliveryGeneration
	candidate  deployment.DeliveryCandidate
	plan       deployment.DeliveryPlan
}

func (s canonicalRefreshReaderStub) ActiveDeliveryGenerationForTarget(context.Context, string, string, string) (deployment.DeliveryGeneration, error) {
	return s.generation, nil
}

func (s canonicalRefreshReaderStub) DeliveryCandidateByID(context.Context, string) (deployment.DeliveryCandidate, error) {
	return s.candidate, nil
}

func (s canonicalRefreshReaderStub) PlanByID(context.Context, string) (deployment.DeliveryPlan, error) {
	return s.plan, nil
}

type canonicalRefreshMutationStub struct {
	intent             deploymentmodule.DeliveryPlanIntent
	buildPrincipal     string
	publishedCandidate string
}

func (s *canonicalRefreshMutationStub) CreatePlan(_ context.Context, intent deploymentmodule.DeliveryPlanIntent, _ string) (deployment.DeliveryPlan, error) {
	s.intent = intent
	return deployment.DeliveryPlan{ID: "plan-refresh"}, nil
}

func (s *canonicalRefreshMutationStub) BuildPlan(_ context.Context, _, _ string, principal, _ string) (deployment.DeliveryBuildAttempt, error) {
	s.buildPrincipal = principal
	return deployment.DeliveryBuildAttempt{CandidateID: "candidate-refresh", Status: deployment.DeliveryBuildSealed}, nil
}

func (s *canonicalRefreshMutationStub) PublishCandidate(_ context.Context, _, candidate, _, _ string) (deployment.DeliveryPublication, error) {
	s.publishedCandidate = candidate
	return deployment.DeliveryPublication{Status: deployment.DeliveryPublicationCommitted}, nil
}

func (*canonicalRefreshMutationStub) RollbackGeneration(context.Context, string, string, string, string) (deployment.DeliveryPublication, error) {
	return deployment.DeliveryPublication{}, nil
}
