package dashboardpublication

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type activationStateStub struct {
	state servingstate.State
	err   error
}

func (s activationStateStub) ByID(context.Context, servingstate.ID) (servingstate.State, error) {
	return s.state, s.err
}

func TestNativeDashboardPublicationDeploymentGuardTreatsEmptySnapshotsAsInert(t *testing.T) {
	for _, raw := range []string{"", "null", `{}`} {
		t.Run(raw, func(t *testing.T) {
			state, activated := dashboardPublicationActivation(raw)
			if err := NewNativeDashboardPublicationReconciler().Reconcile(t.Context(), activationStateStub{state: state}, activated); err != nil {
				t.Fatalf("empty deployment publication snapshot rejected: %v", err)
			}
		})
	}
}

func TestNativeDashboardPublicationDeploymentGuardRejectsControlPlaneMutation(t *testing.T) {
	state, activated := dashboardPublicationActivation(`{"website":{"dashboard":"sales"}}`)
	err := NewNativeDashboardPublicationReconciler().Reconcile(t.Context(), activationStateStub{state: state}, activated)
	if err == nil || !strings.Contains(err.Error(), "publication and sharing state is control-plane owned") {
		t.Fatalf("deployment publication mutation error = %v, want control-plane ownership diagnostic", err)
	}
}

func TestNativeDashboardPublicationDeploymentGuardFailsClosedOnMalformedSnapshot(t *testing.T) {
	state, activated := dashboardPublicationActivation(`{`)
	err := NewNativeDashboardPublicationReconciler().Reconcile(t.Context(), activationStateStub{state: state}, activated)
	if err == nil || !strings.Contains(err.Error(), "decode candidate dashboard publications") {
		t.Fatalf("malformed publication snapshot error = %v, want decode diagnostic", err)
	}
}

func TestNativeDashboardPublicationDeploymentGuardFailsClosedWithoutAuthority(t *testing.T) {
	_, activated := dashboardPublicationActivation(`{}`)
	if err := NewNativeDashboardPublicationReconciler().Reconcile(t.Context(), nil, activated); err == nil || !strings.Contains(err.Error(), "requires a serving-state reader") {
		t.Fatalf("nil serving-state reader error = %v, want fail-closed diagnostic", err)
	}

	authorityErr := errors.New("serving-state authority unavailable")
	err := NewNativeDashboardPublicationReconciler().Reconcile(t.Context(), activationStateStub{err: authorityErr}, activated)
	if !errors.Is(err, authorityErr) {
		t.Fatalf("unavailable serving-state authority error = %v, want %v", err, authorityErr)
	}
}

func TestNativeDashboardPublicationDeploymentGuardRejectsMismatchedGeneration(t *testing.T) {
	state, activated := dashboardPublicationActivation(`{}`)
	state.ID = "generation-other"
	err := NewNativeDashboardPublicationReconciler().Reconcile(t.Context(), activationStateStub{state: state}, activated)
	if err == nil || !strings.Contains(err.Error(), "does not match deployment identity") {
		t.Fatalf("mismatched serving generation error = %v, want identity diagnostic", err)
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
