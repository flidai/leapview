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
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

// ServingStateReader is the read-only serving-state authority needed by the
// activation ownership guard.
type ServingStateReader interface {
	ByID(context.Context, servingstate.ID) (servingstate.State, error)
}

// ActivationReconciler is injected into runtime composition. It retains the
// activation callback contract while enforcing that analytics deployments do
// not acquire dashboard publication or sharing mutation authority.
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

// Reconcile validates the activated generation identity and rejects any
// embedded dashboard publication definition. Empty legacy snapshots are
// tolerated as inert compatibility data; they never represent an instruction
// to remove control-plane-owned publication state.
func (r *NativeDashboardPublicationReconciler) Reconcile(ctx context.Context, states ServingStateReader, activated deployment.Deployment) error {
	if r == nil {
		return errors.New("native dashboard publication deployment guard is not configured")
	}
	state, ok, err := loadActivatedDashboardPublications(ctx, states, activated)
	if err != nil || !ok {
		return err
	}
	raw := strings.TrimSpace(state.DashboardPublicationsJSON)
	if raw == "" || raw == "null" {
		return nil
	}
	publications := make(map[string]json.RawMessage)
	if err := json.Unmarshal([]byte(raw), &publications); err != nil {
		return fmt.Errorf("decode activated dashboard publications for serving state %q: %w", state.ID, err)
	}
	if len(publications) != 0 {
		return fmt.Errorf("activated serving state %q contains dashboard publication definitions; publication and sharing state is control-plane owned", state.ID)
	}
	return nil
}

// loadActivatedDashboardPublications loads and validates the generation
// snapshot. The bool is false for a stale callback or an absent reader.
func loadActivatedDashboardPublications(ctx context.Context, states ServingStateReader, activated deployment.Deployment) (servingstate.State, bool, error) {
	if states == nil {
		return servingstate.State{}, false, nil
	}
	state, err := states.ByID(ctx, servingstate.ID(activated.ServingIdentity.GenerationID))
	if err != nil {
		return servingstate.State{}, false, fmt.Errorf("load activated serving state %q for dashboard publication ownership validation: %w", activated.ServingIdentity.GenerationID, err)
	}
	if state.ID != servingstate.ID(activated.ServingIdentity.GenerationID) || state.ProjectID != activated.ServingIdentity.ProjectID || state.Environment != servingstate.Environment(activated.ServingIdentity.Environment) {
		return servingstate.State{}, false, fmt.Errorf("activated serving state identity (%q, %q, %q) does not match deployment identity (%q, %q, %q)", state.ID, state.ProjectID, state.Environment, activated.ServingIdentity.GenerationID, activated.ServingIdentity.ProjectID, activated.ServingIdentity.Environment)
	}
	// AfterActivated callbacks may overlap a later cutover. A stale callback has
	// no ownership decision to make for the newly active generation.
	if activeReader, ok := states.(interface {
		ActiveArtifact(context.Context, projectgraph.ResourceID, servingstate.Environment) (servingstate.State, servingstate.Artifact, error)
	}); ok {
		current, _, currentErr := activeReader.ActiveArtifact(ctx, activated.ServingIdentity.ProjectID, servingstate.Environment(activated.ServingIdentity.Environment))
		if currentErr == nil && current.ID != state.ID {
			return servingstate.State{}, false, nil
		}
		if currentErr != nil && !errors.Is(currentErr, servingstate.ErrNotFound) {
			return servingstate.State{}, false, fmt.Errorf("check active serving state before dashboard publication ownership validation: %w", currentErr)
		}
	}
	return state, true, nil
}
