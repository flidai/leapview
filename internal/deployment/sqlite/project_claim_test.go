package sqlite

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

func claimInput(project, environment, actor string, at time.Time) deployment.ProjectClaimInput {
	return deployment.ProjectClaimInput{ProjectID: projectgraph.ResourceID(project), Environment: servingstate.Environment(environment), ClaimedBy: actor, ClaimedAt: at}
}

func TestProjectClaimIsIdempotentAcrossRepositoryRestart(t *testing.T) {
	store, repository := openDeploymentRepository(t)
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	first, err := repository.ClaimProject(t.Context(), claimInput("finance", "prod", "principal_1", at))
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewRepositoryWithHooks(store.SQLDB(), ActivationHooks{})
	replay, err := restarted.ClaimProject(t.Context(), claimInput("finance", "prod", "principal_2", at.Add(time.Hour)))
	if err != nil || replay != first {
		t.Fatalf("idempotent claim = %#v, err=%v, want %#v", replay, err, first)
	}
	read, err := restarted.ProjectClaim(t.Context())
	if err != nil || read != first {
		t.Fatalf("restart read = %#v, err=%v, want %#v", read, err, first)
	}
	if _, err := restarted.ClaimProject(t.Context(), claimInput("marketing", "prod", "principal_1", at)); !errors.Is(err, deployment.ErrProjectClaimConflict) {
		t.Fatalf("different project error = %v, want claim conflict", err)
	}
	if _, err := restarted.ClaimProject(t.Context(), claimInput("finance", "staging", "principal_1", at)); !errors.Is(err, deployment.ErrProjectClaimConflict) {
		t.Fatalf("different environment error = %v, want claim conflict", err)
	}
}

func TestProjectClaimConcurrentABHasOneWinner(t *testing.T) {
	_, repository := openDeploymentRepository(t)
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	inputs := []deployment.ProjectClaimInput{claimInput("finance", "prod", "actor_a", at), claimInput("marketing", "prod", "actor_b", at)}
	results := make(chan error, len(inputs))
	var wg sync.WaitGroup
	for _, input := range inputs {
		wg.Add(1)
		go func(input deployment.ProjectClaimInput) {
			defer wg.Done()
			_, err := repository.ClaimProject(t.Context(), input)
			results <- err
		}(input)
	}
	wg.Wait()
	close(results)
	var winners, conflicts int
	for err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, deployment.ErrProjectClaimConflict):
			conflicts++
		default:
			t.Fatalf("concurrent claim error = %v", err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("concurrent outcomes = winners %d conflicts %d, want one each", winners, conflicts)
	}
}

func bootstrapPolicyInput(project, deploymentID, actor, credential string, armed time.Time) deployment.BootstrapActivationPolicy {
	return deployment.BootstrapActivationPolicy{
		ProjectID: projectgraph.ResourceID(project), Environment: servingstate.Environment("prod"), DeploymentID: deploymentID,
		RequestDigest: deploymentDigest("a"), ActorID: actor, CredentialID: credential,
		CredentialExpiresAt: armed.Add(time.Hour), ArmedAt: armed,
	}
}

func TestBootstrapActivationPolicyIsIdempotentAndBound(t *testing.T) {
	_, repository := openDeploymentRepository(t)
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	first, err := repository.ArmBootstrapActivation(t.Context(), bootstrapPolicyInput("finance", "deployment_1", "admin", "token_1", at))
	if err != nil {
		t.Fatal(err)
	}
	replay := bootstrapPolicyInput("finance", "deployment_1", "admin", "token_1", at.Add(time.Minute))
	got, err := repository.ArmBootstrapActivation(t.Context(), replay)
	if err != nil || got != first {
		t.Fatalf("idempotent bootstrap replay = %#v, err=%v, want %#v", got, err, first)
	}
	if _, err := repository.ArmBootstrapActivation(t.Context(), bootstrapPolicyInput("finance", "deployment_2", "admin", "token_1", at)); !errors.Is(err, deployment.ErrBootstrapPolicyConflict) {
		t.Fatalf("different deployment error = %v, want bootstrap policy conflict", err)
	}
	read, err := repository.BootstrapActivationPolicy(t.Context(), "deployment_1")
	if err != nil || read != first {
		t.Fatalf("bootstrap policy read = %#v, err=%v, want %#v", read, err, first)
	}
}

func TestBootstrapActivationPolicyConcurrentDifferentDeploymentsHasOneWinner(t *testing.T) {
	_, repository := openDeploymentRepository(t)
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	results := make(chan error, 2)
	for _, id := range []string{"deployment_a", "deployment_b"} {
		go func(id string) {
			_, err := repository.ArmBootstrapActivation(t.Context(), bootstrapPolicyInput("finance", id, "admin", "token_1", at))
			results <- err
		}(id)
	}
	var winners, conflicts int
	for range 2 {
		switch err := <-results; err {
		case nil:
			winners++
		case deployment.ErrBootstrapPolicyConflict:
			conflicts++
		default:
			t.Fatalf("concurrent bootstrap error = %v", err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("concurrent outcomes = winners %d conflicts %d, want one each", winners, conflicts)
	}
}

func TestCandidateClaimConflictRollsBackCandidate(t *testing.T) {
	ctx, db, repository := testRepository(t)
	insertCandidatePrincipal(t, ctx, db, "principal_1")
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	if _, err := repository.ClaimProject(ctx, claimInput("finance", "prod", "principal_1", at)); err != nil {
		t.Fatal(err)
	}
	candidate := candidateRecord(t, at, "cand_marketing", "marketing", "principal_1", "sha256:"+strings.Repeat("a", 64))
	_, _, err := repository.StartCandidateWithClaim(ctx, candidate, 4, claimInput("marketing", "prod", "principal_1", at))
	if !errors.Is(err, deployment.ErrProjectClaimConflict) {
		t.Fatalf("candidate claim error = %v, want claim conflict", err)
	}
	if _, err := repository.CandidateByID(ctx, candidate.ID); !errors.Is(err, deployment.ErrCandidateNotFound) {
		t.Fatalf("candidate after claim conflict = %v, want not found", err)
	}
}
