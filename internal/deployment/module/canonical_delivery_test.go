package module

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

type legacyPlanStore struct {
	plan    deployment.DeliveryPlan
	created int
}

func (s *legacyPlanStore) CreatePlan(_ context.Context, plan deployment.DeliveryPlan) (deployment.DeliveryPlan, error) {
	s.created++
	s.plan = plan
	return plan, nil
}

func (s *legacyPlanStore) PlanByID(_ context.Context, id string) (deployment.DeliveryPlan, error) {
	if s.plan.ID == "" || s.plan.ID != id {
		return deployment.DeliveryPlan{}, deployment.ErrNotFound
	}
	return s.plan, nil
}

func TestResolveLegacyCandidatePlanReusesPersistedPlan(t *testing.T) {
	projectID := projectgraph.ResourceID("project:demo")
	input := deployment.DeliveryCandidateBuildInput{
		ProjectID: projectID, OwnerID: "principal:reviewer", ArtifactDigest: "sha256:" + "a" + strings.Repeat("0", 63),
		Source:    project.CandidateSourceSnapshot{SourceAttestationDigest: "sha256:" + "c" + strings.Repeat("0", 63)},
		Candidate: deployment.Candidate{ID: "candidate-1", TargetID: "target-1", Scope: deployment.CandidateScope{ProjectID: projectID, Environment: "dev"}},
	}
	persisted := deployment.DeliveryPlan{ID: "plan-candidate-1", ProjectID: projectID, ActorID: "principal:author", TargetID: input.Candidate.TargetID, Environment: "dev", SourceDigest: input.ArtifactDigest, Provenance: deployment.DeliveryProvenance{AttestationDigest: input.Source.SourceAttestationDigest}}
	store := &legacyPlanStore{plan: persisted}
	plannerCalls := 0
	got, err := resolveLegacyCandidatePlan(t.Context(), input, release.CandidateArtifactSet{}, store, func(context.Context, deployment.DeliveryCandidateBuildInput, release.CandidateArtifactSet) (deployment.DeliveryPlan, error) {
		plannerCalls++
		return deployment.DeliveryPlan{}, errors.New("planner must not run for retry")
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != persisted.ID || plannerCalls != 0 || store.created != 0 {
		t.Fatalf("resolved plan = %#v, planner calls = %d, creates = %d", got, plannerCalls, store.created)
	}
}

func TestResolveLegacyCandidatePlanRejectsAttestationDrift(t *testing.T) {
	projectID := projectgraph.ResourceID("project:demo")
	input := deployment.DeliveryCandidateBuildInput{
		ProjectID: projectID, OwnerID: "principal:reviewer", ArtifactDigest: "sha256:" + "d" + strings.Repeat("0", 63),
		Source:    project.CandidateSourceSnapshot{SourceAttestationDigest: "sha256:" + "e" + strings.Repeat("0", 63)},
		Candidate: deployment.Candidate{ID: "candidate-drift", TargetID: "target-1", Scope: deployment.CandidateScope{ProjectID: projectID, Environment: "dev"}},
	}
	store := &legacyPlanStore{plan: deployment.DeliveryPlan{ID: "plan-candidate-drift", ProjectID: projectID, TargetID: input.Candidate.TargetID, Environment: "dev", SourceDigest: input.ArtifactDigest, Provenance: deployment.DeliveryProvenance{AttestationDigest: "sha256:" + "f" + strings.Repeat("0", 63)}}}
	_, err := resolveLegacyCandidatePlan(t.Context(), input, release.CandidateArtifactSet{}, store, func(context.Context, deployment.DeliveryCandidateBuildInput, release.CandidateArtifactSet) (deployment.DeliveryPlan, error) {
		return deployment.DeliveryPlan{}, errors.New("planner must not run on attestation drift")
	})
	if err == nil || !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("error = %v, want delivery conflict", err)
	}
}

func TestResolveLegacyCandidatePlanCreatesMissingPlan(t *testing.T) {
	projectID := projectgraph.ResourceID("project:demo")
	input := deployment.DeliveryCandidateBuildInput{
		ProjectID: projectID, OwnerID: "principal:owner", ArtifactDigest: "sha256:" + "b" + strings.Repeat("0", 63),
		Candidate: deployment.Candidate{ID: "candidate-2", TargetID: "target-1", Scope: deployment.CandidateScope{ProjectID: projectID, Environment: "dev"}},
	}
	store := &legacyPlanStore{}
	got, err := resolveLegacyCandidatePlan(t.Context(), input, release.CandidateArtifactSet{}, store, func(_ context.Context, in deployment.DeliveryCandidateBuildInput, _ release.CandidateArtifactSet) (deployment.DeliveryPlan, error) {
		return deployment.DeliveryPlan{ID: "plan-" + in.Candidate.ID, ProjectID: in.ProjectID, ActorID: in.OwnerID, TargetID: in.Candidate.TargetID, Environment: in.Candidate.Scope.Environment, SourceDigest: in.ArtifactDigest}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "plan-candidate-2" || store.created != 1 {
		t.Fatalf("resolved plan = %#v, creates = %d", got, store.created)
	}
}
