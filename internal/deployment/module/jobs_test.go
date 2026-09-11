package module

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/deployment/apiadapter"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/pkg/jobs"
)

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

type activationMutationCoordinatorStub struct {
	row           apiadapter.Deployment
	activateCalls int
}

func (stub *activationMutationCoordinatorStub) Get(context.Context, apiadapter.Scope) (apiadapter.Deployment, error) {
	return stub.row, nil
}
func (stub *activationMutationCoordinatorStub) Activate(context.Context, apiadapter.ActivateRequest) (apiadapter.Deployment, error) {
	stub.activateCalls++
	stub.row.Status = apiadapter.StatusActive
	return stub.row, nil
}
func (*activationMutationCoordinatorStub) Create(context.Context, apiadapter.CreateRequest) (apiadapter.Deployment, error) {
	return apiadapter.Deployment{}, nil
}
func (*activationMutationCoordinatorStub) CancelRequest(context.Context, apiadapter.CancelRequest) (apiadapter.Deployment, error) {
	return apiadapter.Deployment{}, nil
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
	validated := 0
	module := &Module{protected: true, jobs: JobConfig{Coordinator: coordinator, ValidateActivation: func(context.Context, string) error {
		validated++
		return nil
	}}, bootstrapPolicies: bootstrapPolicyStub{policy: policy}, authorizeBootstrap: func(context.Context, deployment.BootstrapActivationPolicy) error { return nil }}
	payload, err := json.Marshal(ActivateJob{Project: row.Project, Deployment: row.ID, Actor: actor.PrincipalID, Credential: actor, Bootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := module.activate(t.Context(), jobs.Job{Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if !coordinator.activated || validated != 1 {
		t.Fatalf("bootstrap activation: activated=%t validation calls=%d", coordinator.activated, validated)
	}
	coordinator.activated = false
	module.authorizeBootstrap = func(context.Context, deployment.BootstrapActivationPolicy) error {
		return deployment.ErrBootstrapPolicyConflict
	}
	if err := module.activate(t.Context(), jobs.Job{Payload: payload}); err == nil || coordinator.activated || validated != 1 {
		t.Fatalf("invalidated bootstrap policy activation err=%v activated=%t validation calls=%d", err, coordinator.activated, validated)
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
	firstErr := module.activate(t.Context(), jobs.Job{Payload: payload})
	var retryErr *jobs.RetryError
	if !errors.As(firstErr, &retryErr) || !errors.Is(firstErr, reconcileErr) || retryErr.Delay != time.Second {
		t.Fatalf("activation reconciliation error = %v, want retryable %v", firstErr, reconcileErr)
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

func TestActivationJobValidatesCandidateBeforeCommit(t *testing.T) {
	row := apiadapter.Deployment{ID: "deployment_1", Project: "project_demo", Environment: "prod", GenerationID: "generation_candidate", Status: apiadapter.StatusPending}
	payload, err := json.Marshal(ActivateJob{Project: row.Project, Deployment: row.ID, Actor: "publisher", IdempotencyKey: "activate-1"})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("rejection leaves activation unchanged", func(t *testing.T) {
		coordinator := &activationMutationCoordinatorStub{row: row}
		before := coordinator.row
		validationErr := errors.New("candidate contains control-plane publication state")
		validated := 0
		module := &Module{jobs: JobConfig{
			Coordinator: coordinator,
			ValidateActivation: func(context.Context, string) error {
				validated++
				return validationErr
			},
		}}
		err := module.activate(t.Context(), jobs.Job{Payload: payload})
		if !errors.Is(err, validationErr) {
			t.Fatalf("activation validation error = %v, want %v", err, validationErr)
		}
		if coordinator.activateCalls != 0 || coordinator.row != before {
			t.Fatalf("rejected candidate changed activation state: calls=%d row=%#v want %#v", coordinator.activateCalls, coordinator.row, before)
		}
		if validated != 1 {
			t.Fatalf("activation validation calls = %d, want 1", validated)
		}
	})

	t.Run("valid candidate activates", func(t *testing.T) {
		coordinator := &activationMutationCoordinatorStub{row: row}
		validated := 0
		module := &Module{jobs: JobConfig{
			Coordinator: coordinator,
			ValidateActivation: func(_ context.Context, generationID string) error {
				validated++
				if generationID != row.GenerationID {
					t.Fatalf("validated generation = %q, want %q", generationID, row.GenerationID)
				}
				return nil
			},
		}}
		if err := module.activate(t.Context(), jobs.Job{Payload: payload}); err != nil {
			t.Fatal(err)
		}
		if coordinator.activateCalls != 1 || coordinator.row.Status != apiadapter.StatusActive || validated != 1 {
			t.Fatalf("valid activation: calls=%d status=%q validation calls=%d", coordinator.activateCalls, coordinator.row.Status, validated)
		}
	})
}
