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
	immediate := activationWorkflow(execution, true, "project", "deployment-1", "release-1", actor, deployment.Approval{}, "key", "prod")
	gated := activationWorkflow(execution, false, "project", "deployment-2", "release-1", actor, deployment.Approval{}, "key", "prod")

	if immediate.Event.EventType != execution.InitialEvent || immediate.Event.ResourceKind != execution.ResourceKind || immediate.Job.Kind != execution.JobKind {
		t.Fatalf("immediate workflow = %#v", immediate)
	}
	if immediate.Job.PartitionKey != "deployment:project:prod" {
		t.Fatalf("immediate workflow partition = %q", immediate.Job.PartitionKey)
	}
	if gated.Event.EventType != execution.InitialEvent || gated.Event.ResourceKind != execution.ResourceKind || gated.Job.ID != "" {
		t.Fatalf("approval-gated workflow = %#v", gated)
	}
}

func TestActivationJobsBoundCrashRecoveryLease(t *testing.T) {
	handlers := (&Module{}).JobHandlers()
	if len(handlers) != 2 {
		t.Fatalf("activation handlers = %d, want 2", len(handlers))
	}
	for _, handler := range handlers {
		lease, ok := handler.(interface{ LeaseTimeout() time.Duration })
		if !ok {
			t.Fatalf("activation handler %q has no lease policy", handler.Kind())
		}
		if lease.LeaseTimeout() != activationJobLeaseTimeout {
			t.Fatalf("activation handler %q lease = %v, want %v", handler.Kind(), lease.LeaseTimeout(), activationJobLeaseTimeout)
		}
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
func (*activationCoordinatorStub) CancelRequest(context.Context, apiadapter.CancelRequest) (apiadapter.Deployment, error) {
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

func TestActivationJobRequiresPostCommitReconciliation(t *testing.T) {
	row := apiadapter.Deployment{ID: "deployment_1", Project: "project_demo", Environment: "prod", GenerationID: "0198f2c0-7c7a-7f00-8a11-000000001111", Status: apiadapter.StatusActive}
	coordinator := &activationCoordinatorStub{row: row}
	reconcileErr := errors.New("runtime cutover unavailable")
	reconciled := 0
	module := &Module{jobs: JobConfig{
		Coordinator: coordinator,
		ReconcileActivation: func(_ context.Context, activated apiadapter.Deployment) error {
			reconciled++
			if activated != row {
				t.Fatalf("reconciled deployment = %#v, want %#v", activated, row)
			}
			return reconcileErr
		},
	}}
	payload, err := json.Marshal(ActivateJob{Project: row.Project, Deployment: row.ID, Actor: "publisher", IdempotencyKey: "activate-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := module.activate(t.Context(), jobs.Job{Payload: payload}); !errors.Is(err, reconcileErr) {
		t.Fatalf("activation reconciliation error = %v, want %v", err, reconcileErr)
	}
	if !coordinator.activated || reconciled != 1 {
		t.Fatalf("activated=%t reconciled=%d", coordinator.activated, reconciled)
	}

	module.jobs.ReconcileActivation = func(context.Context, apiadapter.Deployment) error {
		reconciled++
		return nil
	}
	if err := module.activate(t.Context(), jobs.Job{Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if reconciled != 2 {
		t.Fatalf("successful replay reconciliation calls = %d, want 2", reconciled)
	}
}
