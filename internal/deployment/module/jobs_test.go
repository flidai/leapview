package module

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/deployment/apiadapter"
	"github.com/flidai/leapview/internal/deployment/sealedcontrol"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/pkg/jobs"
)

func TestActivationWorkflowDistinguishesImmediateAndApprovalGatedStarts(t *testing.T) {
	execution := apigencommand.AsyncExecutionContract{
		JobKind: "deployment.activate", ResourceKind: "deployment",
		InitialEvent: "deployment.queued", InitialState: "queued",
	}
	actor := deployment.ApprovalActor{PrincipalID: "publisher"}
	immediate := activationWorkflow(execution, true, "project", "deployment-1", "release-1", actor, deployment.Approval{}, "key")
	gated := activationWorkflow(execution, false, "project", "deployment-2", "release-1", actor, deployment.Approval{}, "key")

	if immediate.Event.EventType != execution.InitialEvent || immediate.Event.ResourceKind != execution.ResourceKind || immediate.Job.Kind != execution.JobKind {
		t.Fatalf("immediate workflow = %#v", immediate)
	}
	if gated.Event.EventType != execution.InitialEvent || gated.Event.ResourceKind != execution.ResourceKind || gated.Job.ID != "" {
		t.Fatalf("approval-gated workflow = %#v", gated)
	}
}

type bootstrapPolicyStub struct {
	policy deployment.BootstrapActivationPolicy
}

func (stub bootstrapPolicyStub) ArmBootstrapActivation(context.Context, deployment.BootstrapActivationPolicy) (deployment.BootstrapActivationPolicy, error) {
	return stub.policy, nil
}
func (stub bootstrapPolicyStub) BootstrapActivationPolicy(context.Context, string) (deployment.BootstrapActivationPolicy, error) {
	return stub.policy, nil
}

type activationCoordinatorStub struct {
	row       apiadapter.Deployment
	activated bool
}

func (stub *activationCoordinatorStub) Get(context.Context, apiadapter.Scope) (apiadapter.Deployment, error) {
	return stub.row, nil
}
func (stub *activationCoordinatorStub) Activate(context.Context, apiadapter.ActivateRequest) (apiadapter.Deployment, error) {
	stub.activated = true
	return stub.row, nil
}
func (*activationCoordinatorStub) Create(context.Context, apiadapter.CreateRequest) (apiadapter.Deployment, error) {
	return apiadapter.Deployment{}, nil
}
func (*activationCoordinatorStub) Cancel(context.Context, apiadapter.Scope) (apiadapter.Deployment, error) {
	return apiadapter.Deployment{}, nil
}

func TestBootstrapActivationJobRevalidatesPolicyAndSkipsApproval(t *testing.T) {
	now := time.Now().UTC()
	row := apiadapter.Deployment{ID: "deployment_1", Project: "project_demo", Environment: "prod", RequestDigest: "sha256:" + strings.Repeat("a", 64), Status: apiadapter.StatusPending}
	actor := deployment.ApprovalActor{PrincipalID: "admin", CredentialClass: deployment.CredentialClassAPIToken, CredentialID: "token_1", CredentialExpiresAt: now.Add(time.Hour)}
	policy := deployment.BootstrapActivationPolicy{ProjectID: projectgraph.ResourceID("project_demo"), Environment: servingstate.Environment("prod"), DeploymentID: row.ID, RequestDigest: row.RequestDigest, ActorID: actor.PrincipalID, CredentialID: actor.CredentialID, CredentialExpiresAt: actor.CredentialExpiresAt, ArmedAt: now}
	coordinator := &activationCoordinatorStub{row: row}
	module := &Module{protected: true, jobs: JobConfig{Coordinator: coordinator}, bootstrapPolicies: bootstrapPolicyStub{policy: policy}, authorizeBootstrap: func(context.Context, deployment.BootstrapActivationPolicy) error { return nil }}
	payload, err := json.Marshal(ActivateJob{Project: row.Project, Deployment: row.ID, Actor: actor.PrincipalID, Credential: actor, Bootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := module.activate(t.Context(), jobs.Job{Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if !coordinator.activated {
		t.Fatal("bootstrap activation did not activate the deployment")
	}
	coordinator.activated = false
	module.authorizeBootstrap = func(context.Context, deployment.BootstrapActivationPolicy) error {
		return deployment.ErrBootstrapPolicyConflict
	}
	if err := module.activate(t.Context(), jobs.Job{Payload: payload}); err == nil || coordinator.activated {
		t.Fatalf("invalidated bootstrap policy activation err=%v activated=%t", err, coordinator.activated)
	}
}

type sealedJobCoordinatorStub struct {
	events         *[]string
	publishReplay  bool
	rollbackReplay bool
	publishCalls   int
	rollbackCalls  int
}

func (stub *sealedJobCoordinatorStub) Publish(_ context.Context, request sealedcontrol.PublishRequest) (deployment.PublicationIntent, error) {
	return request.Publication, nil
}

func (stub *sealedJobCoordinatorStub) Rollback(_ context.Context, request sealedcontrol.RollbackRequest) (deployment.RollbackResult, error) {
	return deployment.RollbackResult{}, nil
}

func (stub *sealedJobCoordinatorStub) PublishWithActivation(ctx context.Context, request sealedcontrol.PublishRequest, activate sealedcontrol.PublicationActivation) (deployment.PublicationIntent, error) {
	stub.publishCalls++
	if stub.publishReplay {
		*stub.events = append(*stub.events, "replay")
	}
	commit := func() error {
		if !stub.publishReplay {
			*stub.events = append(*stub.events, "CAS")
		}
		return nil
	}
	if err := activate(ctx, commit); err != nil {
		return request.Publication, err
	}
	return request.Publication, nil
}

func (stub *sealedJobCoordinatorStub) RollbackWithActivation(ctx context.Context, request sealedcontrol.RollbackRequest, activate sealedcontrol.PublicationActivation) (deployment.RollbackResult, error) {
	stub.rollbackCalls++
	if stub.rollbackReplay {
		*stub.events = append(*stub.events, "replay")
	}
	commit := func() error {
		if !stub.rollbackReplay {
			*stub.events = append(*stub.events, "CAS")
		}
		return nil
	}
	if err := activate(ctx, commit); err != nil {
		return deployment.RollbackResult{}, err
	}
	return deployment.RollbackResult{RequestDigest: request.Request.RequestDigest, TargetID: request.Request.TargetID, GenerationID: request.Request.GenerationID}, nil
}

func sealedJobDigest(ch byte) string {
	return "sha256:" + strings.Repeat(string(ch), 64)
}

func sealedJobPendingDeployment() apiadapter.Deployment {
	return apiadapter.Deployment{
		ID: "deployment-1", Project: "project-1", Environment: "prod", GenerationID: "generation-1",
		ArtifactDigest: sealedJobDigest('a'), PriorGenerationID: "generation-0", RequestDigest: sealedJobDigest('b'), Status: apiadapter.StatusPending,
	}
}

func sealedJobPublishRequest() sealedcontrol.PublishRequest {
	seal := deployment.VerifiedSeal{
		CatalogDigest: sealedJobDigest('c'),
	}
	return sealedcontrol.PublishRequest{
		Publication: deployment.PublicationIntent{TargetID: "target-1", ProjectID: projectgraph.ResourceID("project-1"), Environment: "prod", ExpectedTargetRevision: 2},
		Generation:  deployment.Generation{ServingStateID: "generation-1"},
		Seal:        seal,
	}
}

func sealedJobRollbackRequest() sealedcontrol.RollbackRequest {
	return sealedcontrol.RollbackRequest{Request: deployment.RollbackRequest{
		TargetID: "target-1", ProjectID: projectgraph.ResourceID("project-1"), Environment: "prod", GenerationID: "generation-1", ExpectedTargetRevision: 3,
	}}
}

func newSealedJobModule(events *[]string, pending apiadapter.Deployment, coordinator *sealedJobCoordinatorStub, targetErr error) (*Module, *int, *sealedGenerationActivation) {
	markerCalls := 0
	verified := &sealedGenerationActivation{}
	module := &Module{
		jobs:              JobConfig{Coordinator: &activationCoordinatorStub{row: pending}},
		sealedCoordinator: coordinator,
		sealedActivate: func(_ context.Context, _ string, commit func() error) error {
			*events = append(*events, "prepare")
			if err := commit(); err != nil {
				return err
			}
			*events = append(*events, "runtime publish")
			return nil
		},
		sealedTargetVerifier: func(_ context.Context, targetID, projectID, environment, generationID string, revision int64) error {
			*events = append(*events, "target verify")
			*verified = sealedGenerationActivation{TargetID: targetID, ProjectID: projectID, Environment: environment, GenerationID: generationID, TargetRevision: revision}
			return targetErr
		},
		sealedActivationMarker: func(context.Context, deployment.ActivationInput) (deployment.Deployment, error) {
			markerCalls++
			*events = append(*events, "marker")
			return deployment.Deployment{ID: pending.ID, Status: deployment.StatusActive}, nil
		},
	}
	module.sealedRollbackRequest = func(context.Context, apiadapter.Deployment, string, deployment.ApprovalActor, string, int64) (sealedcontrol.RollbackRequest, error) {
		return sealedJobRollbackRequest(), nil
	}
	module.sealedPublishRequest = func(context.Context, apiadapter.Deployment, string, deployment.ApprovalActor, bool) (sealedcontrol.PublishRequest, error) {
		return sealedJobPublishRequest(), nil
	}
	return module, &markerCalls, verified
}

func sealedActivationJob(t *testing.T, pending apiadapter.Deployment, rollback bool) jobs.Job {
	t.Helper()
	payload, err := json.Marshal(ActivateJob{Project: pending.Project, Deployment: pending.ID, Actor: "actor-1", Rollback: rollback})
	if err != nil {
		t.Fatal(err)
	}
	return jobs.Job{Payload: payload}
}

func TestSealedActivationJobPublishesFreshGenerationInOrder(t *testing.T) {
	events := []string{}
	coordinator := &sealedJobCoordinatorStub{events: &events}
	pending := sealedJobPendingDeployment()
	module, markerCalls, verified := newSealedJobModule(&events, pending, coordinator, nil)

	if err := module.activate(t.Context(), sealedActivationJob(t, pending, false)); err != nil {
		t.Fatal(err)
	}
	if want := []string{"prepare", "CAS", "target verify", "runtime publish", "marker"}; !equalStrings(events, want) {
		t.Fatalf("fresh publish ordering = %v, want %v", events, want)
	}
	if coordinator.publishCalls != 1 || *markerCalls != 1 {
		t.Fatalf("publish calls=%d marker calls=%d, want 1/1", coordinator.publishCalls, *markerCalls)
	}
	if want := (sealedGenerationActivation{TargetID: "target-1", ProjectID: "project-1", Environment: "prod", GenerationID: "generation-1", TargetRevision: 3}); *verified != want {
		t.Fatalf("publish target verification = %+v, want %+v", *verified, want)
	}
}

func TestSealedActivationJobCommittedPublishReplayTargetConflictSkipsRuntimeAndMarker(t *testing.T) {
	events := []string{}
	coordinator := &sealedJobCoordinatorStub{events: &events, publishReplay: true}
	pending := sealedJobPendingDeployment()
	module, markerCalls, _ := newSealedJobModule(&events, pending, coordinator, deployment.ErrDeliveryConflict)

	err := module.activate(t.Context(), sealedActivationJob(t, pending, false))
	if !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("committed publish replay error = %v, want target conflict", err)
	}
	if want := []string{"replay", "prepare", "target verify"}; !equalStrings(events, want) {
		t.Fatalf("committed publish replay ordering = %v, want %v", events, want)
	}
	if coordinator.publishCalls != 1 || *markerCalls != 0 {
		t.Fatalf("publish calls=%d marker calls=%d, want 1/0", coordinator.publishCalls, *markerCalls)
	}
}

func TestSealedRollbackActivationJobPublishesOrSkipsAfterTargetVerification(t *testing.T) {
	tests := []struct {
		name      string
		targetErr error
		replay    bool
		wantErr   error
		want      []string
		markers   int
	}{
		{name: "success", want: []string{"prepare", "CAS", "target verify", "runtime publish", "marker"}, markers: 1},
		{name: "committed replay target conflict", targetErr: deployment.ErrDeliveryConflict, replay: true, wantErr: deployment.ErrDeliveryConflict, want: []string{"replay", "prepare", "target verify"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := []string{}
			coordinator := &sealedJobCoordinatorStub{events: &events, rollbackReplay: test.replay}
			pending := sealedJobPendingDeployment()
			module, markerCalls, verified := newSealedJobModule(&events, pending, coordinator, test.targetErr)

			err := module.activate(t.Context(), sealedActivationJob(t, pending, true))
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("rollback error = %v, want %v", err, test.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !equalStrings(events, test.want) {
				t.Fatalf("rollback ordering = %v, want %v", events, test.want)
			}
			if coordinator.rollbackCalls != 1 || *markerCalls != test.markers {
				t.Fatalf("rollback calls=%d marker calls=%d, want 1/%d", coordinator.rollbackCalls, *markerCalls, test.markers)
			}
			if want := (sealedGenerationActivation{TargetID: "target-1", ProjectID: "project-1", Environment: "prod", GenerationID: "generation-1", TargetRevision: 4}); *verified != want {
				t.Fatalf("rollback target verification = %+v, want %+v", *verified, want)
			}
		})
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
