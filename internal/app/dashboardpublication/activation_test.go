package dashboardpublication

import (
	"context"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type activationStateStub struct {
	state   servingstate.State
	current servingstate.State
}

func (s activationStateStub) ByID(context.Context, servingstate.ID) (servingstate.State, error) {
	return s.state, nil
}

func (s activationStateStub) ActiveArtifact(context.Context, projectgraph.ResourceID, servingstate.Environment) (servingstate.State, servingstate.Artifact, error) {
	return s.current, servingstate.Artifact{}, nil
}

func TestNativeDashboardPublicationDeploymentGuardTreatsEmptySnapshotsAsInert(t *testing.T) {
	for _, raw := range []string{"", "null", `{}`} {
		t.Run(raw, func(t *testing.T) {
			state, activated := dashboardPublicationActivation(raw)
			if err := NewNativeDashboardPublicationReconciler().Reconcile(t.Context(), activationStateStub{state: state, current: state}, activated); err != nil {
				t.Fatalf("empty deployment publication snapshot rejected: %v", err)
			}
		})
	}
}

func TestNativeDashboardPublicationDeploymentGuardRejectsControlPlaneMutation(t *testing.T) {
	state, activated := dashboardPublicationActivation(`{"website":{"dashboard":"sales"}}`)
	err := NewNativeDashboardPublicationReconciler().Reconcile(t.Context(), activationStateStub{state: state, current: state}, activated)
	if err == nil || !strings.Contains(err.Error(), "publication and sharing state is control-plane owned") {
		t.Fatalf("deployment publication mutation error = %v, want control-plane ownership diagnostic", err)
	}
}

func TestNativeDashboardPublicationDeploymentGuardFailsClosedOnMalformedSnapshot(t *testing.T) {
	state, activated := dashboardPublicationActivation(`{`)
	err := NewNativeDashboardPublicationReconciler().Reconcile(t.Context(), activationStateStub{state: state, current: state}, activated)
	if err == nil || !strings.Contains(err.Error(), "decode activated dashboard publications") {
		t.Fatalf("malformed publication snapshot error = %v, want decode diagnostic", err)
	}
}

func TestNativeDashboardPublicationDeploymentGuardSkipsStaleGeneration(t *testing.T) {
	state, activated := dashboardPublicationActivation(`{"website":{"dashboard":"sales"}}`)
	current := state
	current.ID = "generation-new"
	if err := NewNativeDashboardPublicationReconciler().Reconcile(t.Context(), activationStateStub{state: state, current: current}, activated); err != nil {
		t.Fatalf("stale activation returned error: %v", err)
	}
}

func dashboardPublicationActivation(raw string) (servingstate.State, deployment.Deployment) {
	state := servingstate.State{
		ID:                        "generation-active",
		ProjectID:                 projectgraph.ResourceID("project:activation"),
		Environment:               "dev",
		DashboardPublicationsJSON: raw,
	}
	activated := deployment.Deployment{
		ServingIdentity: projectgraph.ServingIdentity{
			ProjectID:    state.ProjectID,
			Environment:  string(state.Environment),
			GenerationID: string(state.ID),
		},
		ActivationPrincipal: "principal:publisher",
	}
	return state, activated
}
