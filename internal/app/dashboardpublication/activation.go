// Package dashboardpublication contains the deployment-side ownership guard
// for dashboard publication state.
package dashboardpublication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/deployment"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

// ServingStateReader is the read-only serving-state authority needed by the
// activation ownership guard.
type ServingStateReader interface {
	ByID(context.Context, servingstate.ID) (servingstate.State, error)
}

// ActivationReconciler is injected into runtime composition. Reconcile is an
// admission check: callers must invoke it before committing activation so an
// invalid analytics generation never becomes active.
type ActivationReconciler interface {
	Reconcile(context.Context, ServingStateReader, deployment.Deployment) error
}

// NativeDashboardPublicationReconciler is a validation-only deployment guard.
// Dashboard publication and sharing lifecycle belongs to control-plane APIs;
// this type deliberately owns no transaction or mutation capability.
type NativeDashboardPublicationReconciler struct{}

var _ ActivationReconciler = (*NativeDashboardPublicationReconciler)(nil)

// NewNativeDashboardPublicationReconciler constructs the validation-only
// deployment guard.
func NewNativeDashboardPublicationReconciler() *NativeDashboardPublicationReconciler {
	return &NativeDashboardPublicationReconciler{}
}

// Reconcile validates the candidate generation identity and rejects any
// embedded dashboard publication definition. Empty legacy snapshots are
// tolerated as inert compatibility data; they never represent an instruction
// to remove control-plane-owned publication state.
func (r *NativeDashboardPublicationReconciler) Reconcile(ctx context.Context, states ServingStateReader, candidate deployment.Deployment) error {
	if r == nil {
		return errors.New("native dashboard publication deployment guard is not configured")
	}
	state, err := loadCandidateDashboardPublications(ctx, states, candidate)
	if err != nil {
		return err
	}
	raw := strings.TrimSpace(state.DashboardPublicationsJSON)
	if raw == "" || raw == "null" {
		return nil
	}
	publications := make(map[string]json.RawMessage)
	if err := json.Unmarshal([]byte(raw), &publications); err != nil {
		return fmt.Errorf("decode candidate dashboard publications for serving state %q: %w", state.ID, err)
	}
	if len(publications) != 0 {
		return fmt.Errorf("candidate serving state %q contains dashboard publication definitions; publication and sharing state is control-plane owned", state.ID)
	}
	return nil
}

// loadCandidateDashboardPublications loads and validates the candidate
// generation snapshot.
func loadCandidateDashboardPublications(ctx context.Context, states ServingStateReader, candidate deployment.Deployment) (servingstate.State, error) {
	if states == nil {
		return servingstate.State{}, errors.New("dashboard publication ownership validation requires a serving-state reader")
	}
	generationID := strings.TrimSpace(candidate.ServingIdentity.GenerationID)
	if generationID == "" {
		return servingstate.State{}, errors.New("dashboard publication ownership validation requires a serving generation identity")
	}
	state, err := states.ByID(ctx, servingstate.ID(generationID))
	if err != nil {
		return servingstate.State{}, fmt.Errorf("load candidate serving state %q for dashboard publication ownership validation: %w", generationID, err)
	}
	if state.ID != servingstate.ID(generationID) || state.ProjectID != candidate.ServingIdentity.ProjectID || state.Environment != servingstate.Environment(candidate.ServingIdentity.Environment) {
		return servingstate.State{}, fmt.Errorf("candidate serving state identity (%q, %q, %q) does not match deployment identity (%q, %q, %q)", state.ID, state.ProjectID, state.Environment, generationID, candidate.ServingIdentity.ProjectID, candidate.ServingIdentity.Environment)
	}
	return state, nil
}
