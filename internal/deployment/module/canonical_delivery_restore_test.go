package module

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

type restoreDeliverySourceFixture struct {
	snapshot project.CandidateSourceSnapshot
}

func (f restoreDeliverySourceFixture) Plan(context.Context, project.CandidateSourceScope, project.CandidateSynchronizationRequest) ([]string, error) {
	return nil, nil
}

func (f restoreDeliverySourceFixture) Upload(context.Context, project.CandidateSourceScope, string, io.Reader) error {
	return nil
}

func (f restoreDeliverySourceFixture) Commit(context.Context, project.CandidateSourceScope, project.CandidateSynchronizationRequest) (project.CandidateSourceSnapshot, error) {
	return f.snapshot, nil
}

func (f restoreDeliverySourceFixture) SnapshotAttestation(context.Context, project.CandidateSourceScope, string, string) (project.CandidateSourceSnapshot, error) {
	return f.snapshot, nil
}

type restoreDeliveryTargetFixture struct {
	target deployment.DeliveryTarget
}

func (f restoreDeliveryTargetFixture) ResolveDeliveryTarget(context.Context, string) (deployment.DeliveryTarget, error) {
	return f.target, nil
}

type restoreDeliveryArtifactsFixture struct{}

func (restoreDeliveryArtifactsFixture) PrepareCandidateArtifacts(context.Context, release.CandidateArtifactRequest) (release.CandidateArtifactSet, error) {
	return release.CandidateArtifactSet{}, nil
}

func (restoreDeliveryArtifactsFixture) InspectCandidateArtifacts(context.Context, release.CandidateArtifactRequest) (release.CandidateArtifactSet, error) {
	return release.CandidateArtifactSet{}, nil
}

func (restoreDeliveryArtifactsFixture) RetainCandidateProvenance(context.Context, projectgraph.ResourceID, release.Provenance) (release.Provenance, error) {
	return release.Provenance{}, nil
}

func (restoreDeliveryArtifactsFixture) CandidateProvenance(context.Context, projectgraph.ResourceID, string, int64) (release.Provenance, error) {
	return release.Provenance{}, nil
}

type restoreDeliveryPlanStore struct {
	plan    deployment.DeliveryPlan
	created int
}

func (s *restoreDeliveryPlanStore) CreatePlan(_ context.Context, plan deployment.DeliveryPlan) (deployment.DeliveryPlan, error) {
	s.created++
	s.plan = plan
	return plan, nil
}

func (s *restoreDeliveryPlanStore) PlanByID(_ context.Context, id string) (deployment.DeliveryPlan, error) {
	if s.plan.ID == id {
		return s.plan, nil
	}
	return deployment.DeliveryPlan{}, deployment.ErrNotFound
}

func (s *restoreDeliveryPlanStore) CreateWriterLeaseAndBuildAttempt(context.Context, deployment.DeliveryWriterLease, deployment.DeliveryBuildAttempt) (deployment.DeliveryWriterLease, deployment.DeliveryBuildAttempt, error) {
	return deployment.DeliveryWriterLease{}, deployment.DeliveryBuildAttempt{}, errors.New("not used by restore plan test")
}

func (s *restoreDeliveryPlanStore) DeliveryBuildAttemptByID(context.Context, string) (deployment.DeliveryBuildAttempt, error) {
	return deployment.DeliveryBuildAttempt{}, errors.New("not used by restore plan test")
}

func (s *restoreDeliveryPlanStore) TransitionBuildAttempt(context.Context, string, int64, deployment.DeliveryBuildAttemptStatus, time.Time) (deployment.DeliveryBuildAttempt, error) {
	return deployment.DeliveryBuildAttempt{}, errors.New("not used by restore plan test")
}

func (s *restoreDeliveryPlanStore) MarkBuildFailed(context.Context, string, int64, string, time.Time) (deployment.DeliveryBuildAttempt, error) {
	return deployment.DeliveryBuildAttempt{}, errors.New("not used by restore plan test")
}

func restoreDeliveryPlanForInput(input deployment.DeliveryCandidateBuildInput) (deployment.DeliveryPlan, error) {
	digest := func(char byte) string {
		return "sha256:" + string(char) + strings.Repeat(string(char), 63)
	}
	plan := deployment.DeliveryPlan{
		ID: input.Candidate.Key, ActorID: input.OwnerID, SourceOwnerID: input.OwnerID,
		TargetID: input.Candidate.TargetID, ProjectID: input.ProjectID, Environment: input.Candidate.Scope.Environment,
		Operation: input.Operation, SourceDigest: input.ArtifactDigest, BaseGenerationID: input.Candidate.Scope.BaseGenerationID,
		BaseTargetRevision: 7, Execution: deployment.DeliveryExecutionInputs{
			SourceArtifactDigest: input.ArtifactDigest, CompilerDigest: digest('b'), ExecutableDigest: digest('c'), DependencyDigest: digest('d'), ConfigDigest: digest('e'), BindingDigest: digest('f'), RuntimeDigest: digest('0'), CapabilityDigest: digest('1'),
		},
		Provenance: deployment.DeliveryProvenance{AttestationDigest: input.Source.SourceAttestationDigest},
		Governance: deployment.DeliveryGovernance{PolicyDigest: digest('2'), AuthorizationDigest: digest('3'), QualificationDigest: digest('4'), ExpiresAt: input.Candidate.ExpiresAt, ObservedInputsAllowed: true},
		Evidence:   deployment.DeliveryPlanEvidence{ImpactStatement: "restore impact", PhysicalWorkStatement: "restore private catalog", ReuseStatement: "no relation reuse", Qualification: deployment.DeliveryQualificationEvidence{Policy: "protected", Steps: []deployment.DeliveryQualificationStep{{ID: "restore-check", Kind: "identity", Description: "check restore intent", Required: true, Blocking: true}}}, StalePolicy: deployment.DeliveryStalePolicy{Mode: "reject"}, Rollback: deployment.DeliveryRollbackEvidence{Class: deployment.DeliveryRollbackSafe}},
		CreatedAt:  input.Candidate.CreatedAt,
	}
	return deployment.NewDeliveryPlan(plan)
}

func TestCanonicalDeliveryPlanPersistsRestoreIntentAndRejectsChangedRetry(t *testing.T) {
	projectID := projectgraph.ResourceID("project:restore")
	sourceDigest := "sha256:a" + strings.Repeat("0", 63)
	attestationDigest := "sha256:b" + strings.Repeat("0", 63)
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	store := &restoreDeliveryPlanStore{}
	lifecycle := &deployment.DeliveryLifecycle{
		Targets: restoreDeliveryTargetFixture{target: deployment.DeliveryTarget{TargetID: "target-restore", ProjectID: projectID.String(), Environment: "prod", ActiveGenerationID: "generation-restore", TargetRevision: 7}},
		Store:   store, Now: func() time.Time { return now },
	}
	sources := restoreDeliverySourceFixture{snapshot: project.CandidateSourceSnapshot{ProjectID: projectID, ArtifactDigest: sourceDigest, SourceAttestationDigest: attestationDigest}}
	captured := (*deployment.RestoreIntent)(nil)
	mutations := &CanonicalDeliveryMutations{
		Lifecycle: lifecycle, Sources: sources, Artifacts: restoreDeliveryArtifactsFixture{},
		Plan: func(_ context.Context, input deployment.DeliveryCandidateBuildInput, _ release.CandidateArtifactSet) (deployment.DeliveryPlan, error) {
			captured = input.Candidate.Restore
			return restoreDeliveryPlanForInput(input)
		},
	}
	intent := DeliveryPlanIntent{ProjectID: projectID, PrincipalID: "principal:restore", Environment: "prod", TargetID: "target-restore", Operation: deployment.DeliveryOperationCodeChange, SourceDigest: sourceDigest, SourceAttestationDigest: attestationDigest, Restore: &deployment.RestoreIntent{AuthoredIDs: []projectgraph.ResourceID{"orders", "customers"}, Reason: "approved recovery"}}
	plan, err := mutations.CreatePlan(t.Context(), intent, "restore-plan-1")
	if err != nil {
		t.Fatalf("create restore plan: %v", err)
	}
	if captured == nil || !reflect.DeepEqual(captured.AuthoredIDs, []projectgraph.ResourceID{"customers", "orders"}) || captured.Reason != intent.Restore.Reason {
		t.Fatalf("planner candidate restore = %#v", captured)
	}
	if plan.Restore == nil || !reflect.DeepEqual(plan.Restore.AuthoredIDs, []projectgraph.ResourceID{"customers", "orders"}) || plan.Evidence.Restore == nil {
		t.Fatalf("persisted plan restore = %#v", plan.Restore)
	}
	changed := intent
	changed.Restore = &deployment.RestoreIntent{AuthoredIDs: []projectgraph.ResourceID{"customers", "orders"}, Reason: "changed reason"}
	if _, err := mutations.CreatePlan(t.Context(), changed, "restore-plan-1"); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("changed restore retry error = %v, want delivery conflict", err)
	}
	if store.created != 1 {
		t.Fatalf("plan creates = %d, want one immutable plan", store.created)
	}
}

func TestCanonicalDeliveryPlanRejectsRestoreForRestatement(t *testing.T) {
	projectID := projectgraph.ResourceID("project:restore")
	mutations := &CanonicalDeliveryMutations{Lifecycle: &deployment.DeliveryLifecycle{}, Sources: restoreDeliverySourceFixture{}}
	_, err := mutations.CreatePlan(t.Context(), DeliveryPlanIntent{
		ProjectID: projectID, PrincipalID: "principal:restore", Environment: "prod", TargetID: "target-restore",
		Operation: deployment.DeliveryOperationRestatement, SourceDigest: "sha256:" + strings.Repeat("a", 64), SourceAttestationDigest: "sha256:" + strings.Repeat("b", 64),
		Restore: &deployment.RestoreIntent{AuthoredIDs: []projectgraph.ResourceID{"orders"}, Reason: "approved recovery"},
	}, "restore-restatement")
	if !errors.Is(err, deployment.ErrDeliveryInvalid) {
		t.Fatalf("restore restatement error = %v, want delivery invalid", err)
	}
}
