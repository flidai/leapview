// Package dashboardpublication contains process-composition bridges for the
// native dashboard publication projection.
//
// The concrete PostgreSQL authorities remain here, at the composition
// boundary. Runtime-facing code consumes only the reconciler contract.
package dashboardpublication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	publication "github.com/flidai/leapview/internal/dashboard/publication"
	publicationpostgres "github.com/flidai/leapview/internal/dashboard/publication/postgres"
	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectpostgres "github.com/flidai/leapview/internal/project/postgres"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/jackc/pgx/v5"
)

// ServingStateReader is the read-only serving-state authority needed by
// activation reconciliation. It is deliberately independent of the storage
// dialect so native and legacy paths share generation/stale-callback checks.
type ServingStateReader interface {
	ByID(context.Context, servingstate.ID) (servingstate.State, error)
}

// ActivationReconciler is injected into runtime composition. Keeping the
// callback behind this capability boundary means a production router never
// discovers a database or selects SQLite based on an optional field.
type ActivationReconciler interface {
	Reconcile(context.Context, ServingStateReader, deployment.Deployment) error
}

// NativeDashboardPublicationTxBeginner is the minimal native pool/connection
// surface needed to acquire a caller-owned pgx transaction.
type NativeDashboardPublicationTxBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// NativeDashboardPublicationGenerationFence checks the durable active pointer
// under the same transaction used for the projection mutation.
type NativeDashboardPublicationGenerationFence interface {
	ValidateActiveGeneration(context.Context, pgx.Tx, projectgraph.ServingIdentity) error
}

// NativeDashboardPublicationActivationConfig supplies the native authorities
// used by PostgreSQL activation reconciliation. Begin returns the transaction
// owned by this operation; the repository and Access activator receive that
// exact transaction and never open, commit, or roll it back themselves.
type NativeDashboardPublicationActivationConfig struct {
	Begin           NativeDashboardPublicationTxBeginner
	Publications    *publicationpostgres.Repository
	Project         *projectpostgres.Repository
	Access          *accessmodule.Module
	GenerationFence NativeDashboardPublicationGenerationFence
}

// NativeDashboardPublicationReconciler projects an activated immutable
// serving generation into the PostgreSQL dashboard publication projection.
// It is intentionally explicit: no database/sql handle or SQLite adapter is
// reachable from this type.
type NativeDashboardPublicationReconciler struct {
	begin           NativeDashboardPublicationTxBeginner
	publications    *publicationpostgres.Repository
	project         *projectpostgres.Repository
	access          *accessmodule.Module
	generationFence NativeDashboardPublicationGenerationFence
}

var _ ActivationReconciler = (*NativeDashboardPublicationReconciler)(nil)

// NewNativeDashboardPublicationReconciler validates the complete native
// authority set before handlers are assembled. In particular, a repository
// created with publicationpostgres.New is required; NewRepository (which is
// useful for read-only tests) is not sufficient for activation because it has
// no event or audit ports.
func NewNativeDashboardPublicationReconciler(config NativeDashboardPublicationActivationConfig) (*NativeDashboardPublicationReconciler, error) {
	if config.Begin == nil {
		return nil, errors.New("native dashboard publication reconciliation transaction beginner is required")
	}
	if config.Publications == nil || !config.Publications.IsNative() {
		return nil, errors.New("native dashboard publication reconciliation requires a PostgreSQL publication repository")
	}
	if config.Project == nil || !config.Project.Configured() {
		return nil, errors.New("native dashboard publication reconciliation requires a PostgreSQL project authority")
	}
	if config.Access == nil {
		return nil, errors.New("native dashboard publication reconciliation requires an Access module")
	}
	if config.GenerationFence == nil {
		return nil, errors.New("native dashboard publication reconciliation requires a transaction-scoped generation fence")
	}
	return &NativeDashboardPublicationReconciler{begin: config.Begin, publications: config.Publications, project: config.Project, access: config.Access, generationFence: config.GenerationFence}, nil
}

// Reconcile performs one activation projection. The active-generation read is
// an optimization that avoids opening a transaction for an obviously stale
// callback; the transaction-scoped GenerationFence is authoritative for the
// final compare-and-swap immediately before any projection write.
func (r *NativeDashboardPublicationReconciler) Reconcile(ctx context.Context, states ServingStateReader, activated deployment.Deployment) error {
	if r == nil || r.begin == nil || r.publications == nil || r.project == nil || r.access == nil || r.generationFence == nil {
		return errors.New("native dashboard publication reconciliation is not configured")
	}
	state, publications, ok, err := loadActivatedDashboardPublications(ctx, states, activated)
	if err != nil || !ok {
		return err
	}
	tx, err := r.begin.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin native dashboard publication reconciliation: %w", err)
	}
	if tx == nil {
		return errors.New("begin native dashboard publication reconciliation returned a nil transaction")
	}
	committed := false
	defer func() {
		if !committed {
			// Preserve the operation error; rollback is best effort because the
			// caller's transaction is no longer usable after a failed statement.
			_ = tx.Rollback(ctx)
		}
	}()
	if err := r.reconcileTx(ctx, tx, state, publications, activated); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit dashboard publication reconciliation for serving state %q: %w", state.ID, err)
	}
	committed = true
	return nil
}

// ReconcileTx applies the projection using the exact caller-owned transaction.
// It is useful when activation already has a wider PostgreSQL transaction
// boundary. This method never commits or rolls back tx; callers retain that
// ownership.
func (r *NativeDashboardPublicationReconciler) ReconcileTx(ctx context.Context, tx pgx.Tx, states ServingStateReader, activated deployment.Deployment) error {
	if r == nil || r.publications == nil || r.project == nil || r.access == nil || r.generationFence == nil {
		return errors.New("native dashboard publication reconciliation is not configured")
	}
	if tx == nil {
		return errors.New("native dashboard publication reconciliation transaction is nil")
	}
	state, publications, ok, err := loadActivatedDashboardPublications(ctx, states, activated)
	if err != nil || !ok {
		return err
	}
	return r.reconcileTx(ctx, tx, state, publications, activated)
}

func (r *NativeDashboardPublicationReconciler) reconcileTx(ctx context.Context, tx pgx.Tx, state servingstate.State, publications map[string]json.RawMessage, activated deployment.Deployment) error {
	if err := r.generationFence.ValidateActiveGeneration(ctx, tx, activated.ServingIdentity); err != nil {
		return fmt.Errorf("validate active serving generation for dashboard publications: %w", err)
	}
	if err := r.project.EnsureIdentityTx(ctx, tx, activated.ServingIdentity.ProjectID); err != nil {
		return fmt.Errorf("ensure project identity for dashboard publications: %w", err)
	}
	input := publication.ReconcileInput{
		ProjectID:      activated.ServingIdentity.ProjectID,
		ServingStateID: string(state.ID),
		ActorID:        activated.ActivationPrincipal,
		Publications:   make(map[string]publication.Definition, len(publications)),
	}
	for name, raw := range publications {
		var definition publication.Definition
		if err := json.Unmarshal(raw, &definition); err != nil {
			return fmt.Errorf("decode activated dashboard publication %q: %w", name, err)
		}
		input.Publications[name] = definition
	}
	if err := r.publications.ReconcileTx(ctx, tx, input, func(activateCtx context.Context, activateTx publicationpostgres.Tx, projectID projectgraph.ResourceID, name string) error {
		return r.access.ActivateDashboardPublicationThroughPersistence(activateCtx, activateTx, projectID, name)
	}); err != nil {
		return fmt.Errorf("reconcile dashboard publications for serving state %q: %w", state.ID, err)
	}
	return nil
}

// loadActivatedDashboardPublications loads and validates the generation
// snapshot shared by native and legacy reconciliation. The bool is false for
// a stale callback or an intentionally absent pre-publication snapshot.
func loadActivatedDashboardPublications(ctx context.Context, states ServingStateReader, activated deployment.Deployment) (servingstate.State, map[string]json.RawMessage, bool, error) {
	if states == nil {
		return servingstate.State{}, nil, false, nil
	}
	state, err := states.ByID(ctx, servingstate.ID(activated.ServingIdentity.GenerationID))
	if err != nil {
		return servingstate.State{}, nil, false, fmt.Errorf("load activated serving state %q for dashboard publications: %w", activated.ServingIdentity.GenerationID, err)
	}
	if state.ID != servingstate.ID(activated.ServingIdentity.GenerationID) || state.ProjectID != activated.ServingIdentity.ProjectID || state.Environment != servingstate.Environment(activated.ServingIdentity.Environment) {
		return servingstate.State{}, nil, false, fmt.Errorf("activated serving state identity (%q, %q, %q) does not match deployment identity (%q, %q, %q)", state.ID, state.ProjectID, state.Environment, activated.ServingIdentity.GenerationID, activated.ServingIdentity.ProjectID, activated.ServingIdentity.Environment)
	}
	// AfterActivated callbacks are non-transactional and may overlap a later
	// cutover. Never let a delayed callback roll the durable projection back.
	if activeReader, ok := states.(interface {
		ActiveArtifact(context.Context, projectgraph.ResourceID, servingstate.Environment) (servingstate.State, servingstate.Artifact, error)
	}); ok {
		current, _, currentErr := activeReader.ActiveArtifact(ctx, activated.ServingIdentity.ProjectID, servingstate.Environment(activated.ServingIdentity.Environment))
		if currentErr == nil && current.ID != state.ID {
			return servingstate.State{}, nil, false, nil
		}
		if currentErr != nil && !errors.Is(currentErr, servingstate.ErrNotFound) {
			return servingstate.State{}, nil, false, fmt.Errorf("check active serving state before dashboard publication reconciliation: %w", currentErr)
		}
	}
	raw := strings.TrimSpace(state.DashboardPublicationsJSON)
	// An absent snapshot is not an authoritative empty definition. This keeps
	// old/test-authored serving states from erasing rows created elsewhere.
	if raw == "" || raw == "null" {
		return state, nil, false, nil
	}
	publications := make(map[string]json.RawMessage)
	if err := json.Unmarshal([]byte(raw), &publications); err != nil {
		return servingstate.State{}, nil, false, fmt.Errorf("decode activated dashboard publications for serving state %q: %w", state.ID, err)
	}
	return state, publications, true, nil
}
