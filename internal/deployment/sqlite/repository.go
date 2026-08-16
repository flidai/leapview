// Package sqlite persists one project-generation deployment and its CAS fence.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/deployment"
	deploydb "github.com/flidai/leapview/internal/deployment/internal/db"
	"github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/platform/transaction"
	graph "github.com/flidai/leapview/internal/project/graph"
)

type Repository struct {
	db      *sql.DB
	queries *deploydb.Queries
	hooks   ActivationHooks
}
type ActivationHooks struct {
	LinkRelease    func(context.Context, transaction.Transaction, deployment.CreateInput) error
	RecordWorkflow jobs.WorkflowRecorder
}

func NewRepositoryWithHooks(db *sql.DB, hooks ActivationHooks) *Repository {
	return &Repository{db: db, queries: deploydb.New(db), hooks: hooks}
}

func (r *Repository) CreateDeployment(ctx context.Context, input deployment.CreateInput) (deployment.Deployment, error) {
	if err := deployment.ValidateCreate(input); err != nil {
		return deployment.Deployment{}, err
	}
	if existing, err := r.DeploymentByID(ctx, input.ID); err == nil {
		if sameCreateRequest(existing, input) {
			return existing, nil
		}
		return deployment.Deployment{}, deployment.ErrConflict
	} else if !errors.Is(err, deployment.ErrNotFound) {
		return deployment.Deployment{}, err
	}
	state, err := r.queries.GetServingStateForDeployment(ctx, input.ServingIdentity.GenerationID)
	if errors.Is(err, sql.ErrNoRows) {
		return deployment.Deployment{}, deployment.ErrNotFound
	}
	if err != nil {
		return deployment.Deployment{}, err
	}
	if state.ProjectID != input.ServingIdentity.ProjectID.String() || state.Environment != input.ServingIdentity.Environment || state.Digest != input.ArtifactDigest || state.Status != "validated" && state.Status != "inactive" {
		return deployment.Deployment{}, fmt.Errorf("%w: generation is not activatable", deployment.ErrConflict)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.Deployment{}, err
	}
	defer tx.Rollback()
	qtx := deploydb.New(tx)
	err = qtx.CreateProjectDeployment(ctx, deploydb.CreateProjectDeploymentParams{ID: input.ID, ProjectID: input.ServingIdentity.ProjectID.String(), Environment: input.ServingIdentity.Environment, GenerationID: input.ServingIdentity.GenerationID, ArtifactDigest: input.ArtifactDigest, PriorGenerationID: nullableSQLString(input.PriorGenerationID), RequestDigest: input.RequestDigest, CreatedBy: input.CreatedBy})
	if err != nil {
		return deployment.Deployment{}, mapError(err)
	}
	if input.ReleaseID != "" {
		if r.hooks.LinkRelease == nil {
			return deployment.Deployment{}, fmt.Errorf("deployment release linkage is required")
		}
		if err := r.hooks.LinkRelease(ctx, tx, input); err != nil {
			return deployment.Deployment{}, err
		}
	}
	if input.Workflow.Event.Key != "" || input.Workflow.Job.ID != "" {
		if r.hooks.RecordWorkflow == nil {
			return deployment.Deployment{}, fmt.Errorf("deployment workflow recorder is required")
		}
		if err := r.hooks.RecordWorkflow.RecordWorkflow(ctx, tx, input.Workflow); err != nil {
			return deployment.Deployment{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return deployment.Deployment{}, err
	}
	return r.DeploymentByID(ctx, input.ID)
}

func (r *Repository) DeploymentByID(ctx context.Context, id string) (deployment.Deployment, error) {
	row, err := r.queries.GetProjectDeployment(ctx, strings.TrimSpace(id))
	if errors.Is(err, sql.ErrNoRows) {
		return deployment.Deployment{}, deployment.ErrNotFound
	}
	if err != nil {
		return deployment.Deployment{}, err
	}
	return mapDeployment(row), nil
}

func (r *Repository) ActivateDeployment(ctx context.Context, input deployment.ActivationInput) (deployment.Deployment, error) {
	if err := deployment.ValidateActivation(input); err != nil {
		return deployment.Deployment{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.Deployment{}, err
	}
	defer tx.Rollback()
	row, err := deploymentByTx(ctx, tx, input.DeploymentID)
	if err != nil {
		return deployment.Deployment{}, err
	}
	if row.Status == deployment.StatusActive {
		return row, nil
	}
	if row.Status != deployment.StatusPending {
		return deployment.Deployment{}, deployment.ErrConflict
	}
	if row.ServingIdentity != input.ServingIdentity || row.ArtifactDigest != input.ArtifactDigest || row.PriorGenerationID != input.PriorGenerationID {
		return deployment.Deployment{}, deployment.ErrConflict
	}
	state, err := deploydb.New(tx).GetServingStateForDeployment(ctx, row.GenerationID)
	if err != nil {
		return deployment.Deployment{}, err
	}
	if state.ProjectID != row.ServingIdentity.ProjectID.String() || state.Environment != row.ServingIdentity.Environment || state.Digest != row.ArtifactDigest || state.Status != "validated" && state.Status != "inactive" && state.Status != "active" {
		return deployment.Deployment{}, deployment.ErrConflict
	}
	var current string
	current, err = deploydb.New(tx).GetActiveServingState(ctx, deploydb.GetActiveServingStateParams{ProjectID: row.ServingIdentity.ProjectID.String(), Environment: row.ServingIdentity.Environment})
	if errors.Is(err, sql.ErrNoRows) {
		current = ""
	} else if err != nil {
		return deployment.Deployment{}, err
	}
	if current != row.PriorGenerationID {
		return deployment.Deployment{}, fmt.Errorf("%w: active generation changed", deployment.ErrConflict)
	}
	// The following state/pointer writes are one multi-row CAS fence. They
	// intentionally remain handwritten SQL because generated sqlc methods
	// cannot express the required ordering and RowsAffected checks across the
	// prior generation drain, candidate activation, and active-pointer swap.
	var result sql.Result
	if row.PriorGenerationID != "" {
		var drainResult sql.Result
		drainResult, err = tx.ExecContext(ctx, `UPDATE serving_states SET status='draining',superseded_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=? AND environment=? AND status='active'`, row.PriorGenerationID, row.ServingIdentity.ProjectID.String(), row.ServingIdentity.Environment)
		if err != nil {
			return deployment.Deployment{}, err
		}
		n, _ := drainResult.RowsAffected()
		if n != 1 {
			return deployment.Deployment{}, fmt.Errorf("%w: prior generation changed while draining", deployment.ErrConflict)
		}
	}
	result, err = tx.ExecContext(ctx, `UPDATE serving_states SET status='active',activated_at=CURRENT_TIMESTAMP,error='' WHERE id=? AND project_id=? AND environment=? AND status IN ('validated','inactive')`, row.ServingIdentity.GenerationID, row.ServingIdentity.ProjectID.String(), row.ServingIdentity.Environment)
	if err != nil {
		return deployment.Deployment{}, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return deployment.Deployment{}, fmt.Errorf("%w: candidate generation changed while activating", deployment.ErrConflict)
	}
	if row.PriorGenerationID == "" {
		result, err = tx.ExecContext(ctx, `INSERT INTO project_active_serving_states(project_id,environment,serving_state_id,updated_at) VALUES(?,?,?,CURRENT_TIMESTAMP)`, row.ServingIdentity.ProjectID.String(), row.ServingIdentity.Environment, row.ServingIdentity.GenerationID)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE project_active_serving_states SET serving_state_id=?,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND environment=? AND serving_state_id=?`, row.ServingIdentity.GenerationID, row.ServingIdentity.ProjectID.String(), row.ServingIdentity.Environment, row.PriorGenerationID)
	}
	if err != nil {
		return deployment.Deployment{}, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return deployment.Deployment{}, fmt.Errorf("%w: active generation CAS failed", deployment.ErrConflict)
	}
	if err := deploydb.New(tx).SupersedeOtherProjectDeployments(ctx, deploydb.SupersedeOtherProjectDeploymentsParams{ProjectID: row.ServingIdentity.ProjectID.String(), Environment: row.ServingIdentity.Environment, ID: row.ID}); err != nil {
		return deployment.Deployment{}, err
	}
	result, err = deploydb.New(tx).ActivateProjectDeployment(ctx, deploydb.ActivateProjectDeploymentParams{ActivationPrincipal: sql.NullString{String: input.ActivationPrincipal, Valid: true}, VerificationDigest: sql.NullString{String: input.VerificationDigest, Valid: true}, ID: row.ID})
	if err != nil {
		return deployment.Deployment{}, err
	}
	n, _ = result.RowsAffected()
	if n != 1 {
		return deployment.Deployment{}, deployment.ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return deployment.Deployment{}, err
	}
	return r.DeploymentByID(ctx, row.ID)
}

func deploymentByTx(ctx context.Context, tx *sql.Tx, id string) (deployment.Deployment, error) {
	row, err := deploydb.New(tx).GetProjectDeployment(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return deployment.Deployment{}, deployment.ErrNotFound
	}
	if err != nil {
		return deployment.Deployment{}, err
	}
	return mapDeployment(row), nil
}

func mapDeployment(row deploydb.ProjectDeployment) deployment.Deployment {
	d := deployment.Deployment{ID: row.ID, ServingIdentity: graph.ServingIdentity{ProjectID: graph.ResourceID(row.ProjectID), Environment: row.Environment, GenerationID: row.GenerationID}, ArtifactDigest: row.ArtifactDigest, RequestDigest: row.RequestDigest, Status: deployment.Status(row.Status), CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, Error: row.Error}
	if row.PriorGenerationID.Valid {
		d.PriorGenerationID = row.PriorGenerationID.String
	}
	if row.ActivatedAt.Valid {
		d.ActivatedAt = row.ActivatedAt.String
	}
	if row.ActivationPrincipal.Valid {
		d.ActivationPrincipal = row.ActivationPrincipal.String
	}
	if row.VerificationDigest.Valid {
		d.VerificationDigest = row.VerificationDigest.String
	}
	if row.VerifiedAt.Valid {
		d.VerifiedAt = row.VerifiedAt.String
	}
	return d
}
func sameCreateRequest(row deployment.Deployment, input deployment.CreateInput) bool {
	return row.ID == input.ID && row.ServingIdentity == input.ServingIdentity && row.ArtifactDigest == input.ArtifactDigest && row.PriorGenerationID == input.PriorGenerationID && row.RequestDigest == input.RequestDigest && row.CreatedBy == input.CreatedBy
}
func (r *Repository) FailDeployment(ctx context.Context, id string, cause error) error {
	if cause == nil {
		return fmt.Errorf("deployment failure cause is required")
	}
	n, err := deploydb.New(r.db).FailProjectDeployment(ctx, deploydb.FailProjectDeploymentParams{Error: cause.Error(), ID: strings.TrimSpace(id)})
	if err != nil {
		return err
	}
	if n != 1 {
		return deployment.ErrConflict
	}
	return nil
}
func (r *Repository) CancelDeployment(ctx context.Context, id string) (deployment.Deployment, error) {
	n, err := deploydb.New(r.db).CancelProjectDeployment(ctx, strings.TrimSpace(id))
	if err != nil {
		return deployment.Deployment{}, err
	}
	if n != 1 {
		return deployment.Deployment{}, deployment.ErrConflict
	}
	return r.DeploymentByID(ctx, id)
}
func nullableSQLString(value string) sql.NullString {
	value = strings.TrimSpace(value)
	return sql.NullString{String: value, Valid: value != ""}
}
func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return deployment.ErrNotFound
	}
	if strings.Contains(strings.ToLower(err.Error()), "constraint") || strings.Contains(strings.ToLower(err.Error()), "unique") {
		return deployment.ErrConflict
	}
	return err
}

var _ deployment.Repository = (*Repository)(nil)
