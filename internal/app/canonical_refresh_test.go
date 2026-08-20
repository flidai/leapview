package app

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
)

func TestCanonicalRefreshExecutorBuildsRestatementFromExactActiveSource(t *testing.T) {
	planID := deploymentmodule.CanonicalDeliveryPlanID("target-evaluation", "project:test", "evaluation", deployment.DeliveryOperationRestatement, "refresh-plan-run-refresh")
	reader := canonicalRefreshReaderStub{
		generation:          deployment.DeliveryGeneration{ID: "delivery-base", CandidateID: "candidate-base", ServingStateID: "state-active"},
		publishedGeneration: deployment.DeliveryGeneration{PlanID: planID, ServingStateID: "state-refresh"},
		candidate:           deployment.DeliveryCandidate{PlanID: "plan-base"},
		plan: deployment.DeliveryPlan{
			ActorID: "principal-source-owner", SourceDigest: "sha256:source",
			Provenance: deployment.DeliveryProvenance{AttestationDigest: "sha256:attestation"},
		},
	}
	mutations := &canonicalRefreshMutationStub{planBaseGenerationID: "delivery-base"}
	execute := canonicalRefreshExecutor(mutations, reader, "target-evaluation")
	result, err := execute(t.Context(), refreshrun.JobRecord{
		RunID: "run-refresh", PrincipalID: "principal-refresh",
		Identity: projectgraph.ServingIdentity{ProjectID: "project:test", Environment: "evaluation", GenerationID: "state-active"},
	})
	if err != nil {
		t.Fatalf("canonical refresh: %v", err)
	}
	if result.PlanID != planID || result.ServingStateID != "state-refresh" || result.SnapshotID != 84 {
		t.Fatalf("canonical refresh result = %#v", result)
	}
	if mutations.intent.PrincipalID != "principal-refresh" || mutations.intent.SourceOwnerID != "principal-source-owner" || mutations.intent.Operation != deployment.DeliveryOperationRestatement || mutations.intent.SourceDigest != reader.plan.SourceDigest {
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
	_, err := execute(t.Context(), refreshrun.JobRecord{
		RunID: "run-refresh", PrincipalID: "principal-refresh",
		Identity: projectgraph.ServingIdentity{ProjectID: "project:test", Environment: "evaluation", GenerationID: "state-old"},
	})
	if err == nil {
		t.Fatal("canonical refresh accepted a changed active generation")
	}
}

func TestCanonicalRefreshExecutorRecoversOwnCommittedRestatement(t *testing.T) {
	job := refreshrun.JobRecord{RunID: "run-refresh", PrincipalID: "principal-refresh", Identity: projectgraph.ServingIdentity{ProjectID: "project:test", Environment: "evaluation", GenerationID: "state-old"}}
	planID := deploymentmodule.CanonicalDeliveryPlanID("target-evaluation", job.Identity.ProjectID, job.Identity.Environment, deployment.DeliveryOperationRestatement, "refresh-plan-"+job.RunID)
	candidateID := "candidate-" + strings.TrimPrefix(deployment.CanonicalDeliveryDigest([]byte(planID+"\x00refresh-build-"+job.RunID)), "sha256:")
	publicationID := "publication-" + strings.TrimPrefix(deployment.CanonicalDeliveryDigest([]byte("candidate-publication:"+candidateID)), "sha256:")
	execute := canonicalRefreshExecutor(&canonicalRefreshMutationStub{}, canonicalRefreshReaderStub{
		generation: deployment.DeliveryGeneration{ID: "delivery-later", PlanID: "plan-later", ServingStateID: "state-later"},
		publication: deployment.DeliveryPublication{
			ID: publicationID, TargetID: "target-evaluation", ProjectID: job.Identity.ProjectID, Environment: job.Identity.Environment,
			PlanID: planID, CandidateID: candidateID, GenerationID: "delivery-refresh", ExpectedBaseGenerationID: "delivery-old", Status: deployment.DeliveryPublicationCommitted,
		},
		generations: map[string]deployment.DeliveryGeneration{
			"delivery-old":     {ID: "delivery-old", ServingStateID: "state-old"},
			"delivery-refresh": {ID: "delivery-refresh", PlanID: planID, CandidateID: candidateID, ServingStateID: "state-refresh"},
		},
		candidates: map[string]deployment.DeliveryCandidate{
			candidateID: {ID: candidateID, PlanID: planID, SealID: "seal-refresh", Status: deployment.DeliveryCandidateReady},
		},
		seals: map[string]deployment.CatalogSeal{
			"seal-refresh": {ID: "seal-refresh", AttemptID: "attempt-refresh"},
		},
		attempts: map[string]deployment.DeliveryBuildAttempt{
			"attempt-refresh": {ID: "attempt-refresh", PlanID: planID, CandidateID: candidateID, Status: deployment.DeliveryBuildSealed, QualifiedSnapshotID: 84},
		},
	}, "target-evaluation")
	result, err := execute(t.Context(), job)
	if err != nil {
		t.Fatalf("recover committed canonical refresh: %v", err)
	}
	if result.PlanID != planID || result.ServingStateID != "state-refresh" || result.SnapshotID != 84 {
		t.Fatalf("recovered canonical refresh result = %#v", result)
	}
}

func TestCanonicalRefreshExecutorRejectsBaseChangedDuringPlanning(t *testing.T) {
	mutations := &canonicalRefreshMutationStub{planBaseGenerationID: "delivery-new"}
	execute := canonicalRefreshExecutor(mutations, canonicalRefreshReaderStub{
		generation: deployment.DeliveryGeneration{ID: "delivery-old", CandidateID: "candidate-base", ServingStateID: "state-old"},
		candidate:  deployment.DeliveryCandidate{PlanID: "plan-base"},
		plan: deployment.DeliveryPlan{
			ActorID: "principal-source-owner", SourceDigest: "sha256:source",
			Provenance: deployment.DeliveryProvenance{AttestationDigest: "sha256:attestation"},
		},
	}, "target-evaluation")
	_, err := execute(t.Context(), refreshrun.JobRecord{
		RunID: "run-refresh", PrincipalID: "principal-refresh",
		Identity: projectgraph.ServingIdentity{ProjectID: "project:test", Environment: "evaluation", GenerationID: "state-old"},
	})
	if err == nil || !strings.Contains(err.Error(), "base changed during planning") {
		t.Fatalf("canonical refresh error = %v, want planning base fence", err)
	}
	if mutations.buildPrincipal != "" {
		t.Fatalf("stale refresh reached build as %q", mutations.buildPrincipal)
	}
}

type canonicalRefreshReaderStub struct {
	deployment.DeliveryReader
	generation          deployment.DeliveryGeneration
	publishedGeneration deployment.DeliveryGeneration
	candidate           deployment.DeliveryCandidate
	plan                deployment.DeliveryPlan
	publication         deployment.DeliveryPublication
	publicationErr      error
	generations         map[string]deployment.DeliveryGeneration
	candidates          map[string]deployment.DeliveryCandidate
	seals               map[string]deployment.CatalogSeal
	attempts            map[string]deployment.DeliveryBuildAttempt
}

func (s canonicalRefreshReaderStub) ActiveDeliveryGenerationForTarget(context.Context, string, string, string) (deployment.DeliveryGeneration, error) {
	return s.generation, nil
}

func (s canonicalRefreshReaderStub) DeliveryCandidateByID(_ context.Context, id string) (deployment.DeliveryCandidate, error) {
	if candidate, ok := s.candidates[id]; ok {
		return candidate, nil
	}
	return s.candidate, nil
}

func (s canonicalRefreshReaderStub) DeliveryCatalogSealByID(_ context.Context, id string) (deployment.CatalogSeal, error) {
	if seal, ok := s.seals[id]; ok {
		return seal, nil
	}
	return deployment.CatalogSeal{}, sql.ErrNoRows
}

func (s canonicalRefreshReaderStub) DeliveryBuildAttemptByID(_ context.Context, id string) (deployment.DeliveryBuildAttempt, error) {
	if attempt, ok := s.attempts[id]; ok {
		return attempt, nil
	}
	return deployment.DeliveryBuildAttempt{}, sql.ErrNoRows
}

func (s canonicalRefreshReaderStub) PlanByID(context.Context, string) (deployment.DeliveryPlan, error) {
	return s.plan, nil
}

func (s canonicalRefreshReaderStub) DeliveryGenerationByID(_ context.Context, id string) (deployment.DeliveryGeneration, error) {
	if generation, ok := s.generations[id]; ok {
		return generation, nil
	}
	return s.publishedGeneration, nil
}

func (s canonicalRefreshReaderStub) DeliveryPublicationByID(context.Context, string) (deployment.DeliveryPublication, error) {
	if s.publicationErr != nil {
		return deployment.DeliveryPublication{}, s.publicationErr
	}
	if s.publication.ID == "" {
		return deployment.DeliveryPublication{}, sql.ErrNoRows
	}
	return s.publication, nil
}

type canonicalRefreshMutationStub struct {
	intent               deploymentmodule.DeliveryPlanIntent
	buildPrincipal       string
	publishedCandidate   string
	planBaseGenerationID string
}

func (s *canonicalRefreshMutationStub) CreatePlan(_ context.Context, intent deploymentmodule.DeliveryPlanIntent, key string) (deployment.DeliveryPlan, error) {
	s.intent = intent
	return deployment.DeliveryPlan{ID: deploymentmodule.CanonicalDeliveryPlanID(intent.TargetID, intent.ProjectID, intent.Environment, intent.Operation, key), BaseGenerationID: s.planBaseGenerationID}, nil
}

func (s *canonicalRefreshMutationStub) BuildPlan(_ context.Context, _, _ string, principal, _ string) (deployment.DeliveryBuildAttempt, error) {
	s.buildPrincipal = principal
	return deployment.DeliveryBuildAttempt{CandidateID: "candidate-refresh", Status: deployment.DeliveryBuildSealed, QualifiedSnapshotID: 84}, nil
}

func (s *canonicalRefreshMutationStub) PublishCandidate(_ context.Context, _, candidate, _, _ string) (deployment.DeliveryPublication, error) {
	s.publishedCandidate = candidate
	return deployment.DeliveryPublication{GenerationID: "delivery-generation-refresh", Status: deployment.DeliveryPublicationCommitted}, nil
}

func (*canonicalRefreshMutationStub) RollbackGeneration(context.Context, string, string, string, string) (deployment.DeliveryPublication, error) {
	return deployment.DeliveryPublication{}, nil
}
