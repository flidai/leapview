// Package sqlite persists one project-generation deployment and its CAS fence.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/platform/transaction"
)

type Repository struct {
	db    *sql.DB
	hooks ActivationHooks
}
type ActivationHooks struct {
	ApplyAccessSnapshot       func(context.Context, transaction.Transaction, string) error
	ReconcilePublications     func(context.Context, transaction.Transaction, PublicationReconcileInput) error
	ApplyDashboardAppearances func(context.Context, transaction.Transaction, DashboardAppearanceActivationInput) error
	LinkRelease               func(context.Context, transaction.Transaction, deployment.CreateInput) error
	RecordWorkflow            jobs.WorkflowRecorder
}
type DashboardAppearanceActivationInput struct {
	ProjectID, WorkspaceID, ServingStateID, ActorID string
	Appearances                                     map[string]json.RawMessage
}
type PublicationReconcileInput struct {
	ProjectID, WorkspaceID, ServingStateID, ActorID string
	Publications                                    map[string]json.RawMessage
}

func NewRepositoryWithHooks(db *sql.DB, hooks ActivationHooks) *Repository {
	return &Repository{db: db, hooks: hooks}
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
	var status string
	var stateProject, stateEnv, stateDigest string
	err := r.db.QueryRowContext(ctx, `SELECT project_id,environment,digest,status FROM serving_states WHERE id=?`, input.GenerationID).Scan(&stateProject, &stateEnv, &stateDigest, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return deployment.Deployment{}, deployment.ErrNotFound
	}
	if err != nil {
		return deployment.Deployment{}, err
	}
	if stateProject != input.ProjectID || stateEnv != input.Environment || stateDigest != input.ArtifactDigest || status != "validated" && status != "inactive" && status != "active" {
		return deployment.Deployment{}, fmt.Errorf("%w: generation is not activatable", deployment.ErrConflict)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.Deployment{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO project_deployments (id,project_id,environment,generation_id,artifact_digest,prior_generation_id,request_digest,status,created_by) VALUES (?,?,?,?,?,?,?,'pending',?)`, input.ID, input.ProjectID, input.Environment, input.GenerationID, input.ArtifactDigest, nullableString(input.PriorGenerationID), input.RequestDigest, input.CreatedBy)
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
	var d deployment.Deployment
	var status string
	var prior sql.NullString
	var activated, verified sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT id,project_id,environment,generation_id,artifact_digest,prior_generation_id,request_digest,status,created_by,created_at,activated_at,activation_principal,verification_digest,verified_at,error FROM project_deployments WHERE id=?`, strings.TrimSpace(id)).Scan(&d.ID, &d.ProjectID, &d.Environment, &d.GenerationID, &d.ArtifactDigest, &prior, &d.RequestDigest, &status, &d.CreatedBy, &d.CreatedAt, &activated, &d.ActivationPrincipal, &d.VerificationDigest, &verified, &d.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return deployment.Deployment{}, deployment.ErrNotFound
	}
	if err != nil {
		return deployment.Deployment{}, err
	}
	d.Status = deployment.Status(status)
	if prior.Valid {
		d.PriorGenerationID = prior.String
	}
	if activated.Valid {
		d.ActivatedAt = activated.String
	}
	if verified.Valid {
		d.VerifiedAt = verified.String
	}
	return d, nil
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
	if row.ProjectID != input.ProjectID || row.Environment != input.Environment || row.GenerationID != input.GenerationID || row.ArtifactDigest != input.ArtifactDigest || row.PriorGenerationID != input.PriorGenerationID {
		return deployment.Deployment{}, deployment.ErrConflict
	}
	var stateProject, stateEnv, stateDigest, status string
	err = tx.QueryRowContext(ctx, `SELECT project_id,environment,digest,status FROM serving_states WHERE id=?`, row.GenerationID).Scan(&stateProject, &stateEnv, &stateDigest, &status)
	if err != nil {
		return deployment.Deployment{}, err
	}
	if stateProject != row.ProjectID || stateEnv != row.Environment || stateDigest != row.ArtifactDigest || status != "validated" && status != "inactive" && status != "active" {
		return deployment.Deployment{}, deployment.ErrConflict
	}
	if r.hooks.ApplyAccessSnapshot == nil {
		return deployment.Deployment{}, fmt.Errorf("%w: access snapshot activation is not configured", deployment.ErrConflict)
	}
	if err := r.hooks.ApplyAccessSnapshot(ctx, tx, row.GenerationID); err != nil {
		return deployment.Deployment{}, err
	}
	var current string
	err = tx.QueryRowContext(ctx, `SELECT serving_state_id FROM project_active_serving_states WHERE project_id=? AND environment=?`, row.ProjectID, row.Environment).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		current = ""
	} else if err != nil {
		return deployment.Deployment{}, err
	}
	if current != row.PriorGenerationID {
		return deployment.Deployment{}, fmt.Errorf("%w: active generation changed", deployment.ErrConflict)
	}
	if row.PriorGenerationID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE serving_states SET status='draining',superseded_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=? AND environment=? AND status='active'`, row.PriorGenerationID, row.ProjectID, row.Environment); err != nil {
			return deployment.Deployment{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE serving_states SET status='active',activated_at=CURRENT_TIMESTAMP,error='' WHERE id=? AND project_id=? AND environment=?`, row.GenerationID, row.ProjectID, row.Environment); err != nil {
		return deployment.Deployment{}, err
	}
	var result sql.Result
	if row.PriorGenerationID == "" {
		result, err = tx.ExecContext(ctx, `INSERT INTO project_active_serving_states(project_id,environment,serving_state_id,updated_at) VALUES(?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(project_id,environment) DO UPDATE SET serving_state_id=excluded.serving_state_id,updated_at=CURRENT_TIMESTAMP WHERE serving_state_id=''`, row.ProjectID, row.Environment, row.GenerationID)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE project_active_serving_states SET serving_state_id=?,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND environment=? AND serving_state_id=?`, row.GenerationID, row.ProjectID, row.Environment, row.PriorGenerationID)
	}
	if err != nil {
		return deployment.Deployment{}, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return deployment.Deployment{}, fmt.Errorf("%w: active generation CAS failed", deployment.ErrConflict)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE project_deployments SET status='superseded' WHERE project_id=? AND environment=? AND id<>? AND status='active'`, row.ProjectID, row.Environment, row.ID); err != nil {
		return deployment.Deployment{}, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE project_deployments SET status='active',activated_at=CURRENT_TIMESTAMP,activation_principal=?,verification_digest=?,verified_at=CURRENT_TIMESTAMP WHERE id=? AND status='pending'`, input.ActivationPrincipal, input.VerificationDigest, row.ID)
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
	var d deployment.Deployment
	var status string
	var prior, activated, verified sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id,project_id,environment,generation_id,artifact_digest,prior_generation_id,request_digest,status,created_by,created_at,activated_at,activation_principal,verification_digest,verified_at,error FROM project_deployments WHERE id=?`, id).Scan(&d.ID, &d.ProjectID, &d.Environment, &d.GenerationID, &d.ArtifactDigest, &prior, &d.RequestDigest, &status, &d.CreatedBy, &d.CreatedAt, &activated, &d.ActivationPrincipal, &d.VerificationDigest, &verified, &d.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return deployment.Deployment{}, deployment.ErrNotFound
	}
	if err != nil {
		return deployment.Deployment{}, err
	}
	d.Status = deployment.Status(status)
	if prior.Valid {
		d.PriorGenerationID = prior.String
	}
	if activated.Valid {
		d.ActivatedAt = activated.String
	}
	if verified.Valid {
		d.VerifiedAt = verified.String
	}
	return d, nil
}
func sameCreateRequest(row deployment.Deployment, input deployment.CreateInput) bool {
	return row.ID == input.ID && row.ProjectID == input.ProjectID && row.Environment == input.Environment && row.GenerationID == input.GenerationID && row.ArtifactDigest == input.ArtifactDigest && row.PriorGenerationID == input.PriorGenerationID && row.RequestDigest == input.RequestDigest && row.CreatedBy == input.CreatedBy
}
func (r *Repository) FailDeployment(ctx context.Context, id string, cause error) error {
	if cause == nil {
		return fmt.Errorf("deployment failure cause is required")
	}
	res, err := r.db.ExecContext(ctx, `UPDATE project_deployments SET status='failed',error=? WHERE id=? AND status='pending'`, cause.Error(), strings.TrimSpace(id))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return deployment.ErrConflict
	}
	return nil
}
func (r *Repository) CancelDeployment(ctx context.Context, id string) (deployment.Deployment, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE project_deployments SET status='cancelled' WHERE id=? AND status='pending'`, strings.TrimSpace(id))
	if err != nil {
		return deployment.Deployment{}, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return deployment.Deployment{}, deployment.ErrConflict
	}
	return r.DeploymentByID(ctx, id)
}
func nullableString(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
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
