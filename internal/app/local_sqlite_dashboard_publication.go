package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmodule "github.com/flidai/leapview/internal/project/module"
	"github.com/flidai/leapview/internal/servingstate"
)

// reconcileSQLiteActivatedDashboardPublications projects the immutable
// publication definitions carried by a serving generation into the local
// SQLite admin/publication table. Deployment activation deliberately commits
// its serving CAS first; this observer then uses a fresh transaction so a
// reconciliation failure cannot make a successfully activated generation look
// failed. Production composition uses the native PostgreSQL reconciler instead.
func reconcileSQLiteActivatedDashboardPublications(
	ctx context.Context,
	database *sql.DB,
	states interface {
		ByID(context.Context, servingstate.ID) (servingstate.State, error)
	},
	activated deployment.Deployment,
) error {
	if database == nil || states == nil {
		return nil
	}
	state, err := states.ByID(ctx, servingstate.ID(activated.ServingIdentity.GenerationID))
	if err != nil {
		return fmt.Errorf("load activated serving state %q for dashboard publications: %w", activated.ServingIdentity.GenerationID, err)
	}
	if state.ProjectID != activated.ServingIdentity.ProjectID {
		return fmt.Errorf("activated serving state project %q does not match deployment project %q", state.ProjectID, activated.ServingIdentity.ProjectID)
	}
	// AfterActivated is intentionally non-transactional and may overlap a
	// subsequent cutover. If the durable active pointer has already advanced,
	// this callback is stale and must not roll the admin projection backward.
	if activeReader, ok := states.(interface {
		ActiveArtifact(context.Context, projectgraph.ResourceID, servingstate.Environment) (servingstate.State, servingstate.Artifact, error)
	}); ok {
		current, _, currentErr := activeReader.ActiveArtifact(ctx, activated.ServingIdentity.ProjectID, servingstate.Environment(activated.ServingIdentity.Environment))
		if currentErr == nil && current.ID != state.ID {
			return nil
		}
		if currentErr != nil && !errors.Is(currentErr, servingstate.ErrNotFound) && !errors.Is(currentErr, sql.ErrNoRows) {
			return fmt.Errorf("check active serving state before dashboard publication reconciliation: %w", currentErr)
		}
	}
	raw := strings.TrimSpace(state.DashboardPublicationsJSON)
	// Older and test-authored serving states predate the compiled publication
	// snapshot. An absent snapshot is not an authoritative empty definition and
	// must not erase publication rows owned by those activation paths. Modern
	// compilation writes an explicit JSON object (including "{}" for none).
	if raw == "" || raw == "null" {
		return nil
	}
	publications := make(map[string]json.RawMessage)
	if err := json.Unmarshal([]byte(raw), &publications); err != nil {
		return fmt.Errorf("decode activated dashboard publications for serving state %q: %w", state.ID, err)
	}
	if err := projectmodule.EnsureIdentity(ctx, projectmodule.NewSQLiteIdentityRepository(database), activated.ServingIdentity.ProjectID); err != nil {
		return fmt.Errorf("ensure project identity for dashboard publications: %w", err)
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin dashboard publication reconciliation: %w", err)
	}
	defer tx.Rollback()
	if err := dashboardmodule.ReconcilePublications(ctx, tx, dashboardmodule.PublicationActivationInput{
		ProjectID:      activated.ServingIdentity.ProjectID.String(),
		ServingStateID: string(state.ID),
		ActorID:        activated.ActivationPrincipal,
		Publications:   publications,
	}, accessmodule.ActivateDashboardPublicationPrincipal); err != nil {
		return fmt.Errorf("reconcile dashboard publications for serving state %q: %w", state.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit dashboard publication reconciliation for serving state %q: %w", state.ID, err)
	}
	return nil
}

type sqliteDashboardPublicationReconciler struct {
	database *sql.DB
}

func newSQLiteDashboardPublicationReconciler(database *sql.DB) dashboardPublicationActivationReconciler {
	if database == nil {
		return nil
	}
	return &sqliteDashboardPublicationReconciler{database: database}
}

func (r *sqliteDashboardPublicationReconciler) Reconcile(ctx context.Context, states dashboardPublicationServingStateReader, activated deployment.Deployment) error {
	return reconcileSQLiteActivatedDashboardPublications(ctx, r.database, states, activated)
}
