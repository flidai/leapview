// Package sqlite persists one project-generation deployment and its CAS fence.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/deployment"
	deploydb "github.com/flidai/leapview/internal/deployment/internal/db"
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/platform/transaction"
	graph "github.com/flidai/leapview/internal/project/graph"
)

type Repository struct {
	db                    *sql.DB
	queries               *deploydb.Queries
	hooks                 ActivationHooks
	candidateBaseReadHook func()
	// quarantineBeforeMutation is a deterministic race-test seam. Production
	// leaves it nil; tests use it to commit a competing root change after the
	// fenced read snapshot and before the first mutation statement.
	quarantineBeforeMutation func()
	// catalogSealNow is injectable for durable seal transition tests and for
	// callers which need one deterministic completion timestamp. It is kept on
	// the shared repository so all plan-delivery adapters use the same clock.
	catalogSealNow func() time.Time
	// deliveryNow controls authoritative publication eligibility checks. It is
	// injectable for crash/expiry tests; production defaults to wall clock.
	deliveryNow func() time.Time
}
type ActivationHooks struct {
	LinkRelease    func(context.Context, transaction.Transaction, deployment.CreateInput) error
	RecordWorkflow jobplatform.WorkflowRecorder
	Audit          access.AuditIntentRecorder
	// CommitPublication replaces the final SQLite commit only in tests or
	// controlled adapters. A hook may commit and return an error to model a
	// lost activation acknowledgement; production leaves it nil and commits
	// directly. The repository reconciles the durable publication identity
	// before any retry can activate or clean a candidate.
	CommitPublication func(context.Context, *sql.Tx) error
}

func NewRepositoryWithHooks(db *sql.DB, hooks ActivationHooks) *Repository {
	return &Repository{db: db, queries: deploydb.New(db), hooks: hooks, catalogSealNow: time.Now, deliveryNow: time.Now}
}

// WithDeliveryClock returns the repository with an explicit UTC clock for
// publication eligibility checks. A nil function restores time.Now.
func (r *Repository) WithDeliveryClock(now func() time.Time) *Repository {
	if r == nil {
		return r
	}
	if now == nil {
		r.deliveryNow = time.Now
	} else {
		r.deliveryNow = now
	}
	return r
}

// WithCatalogSealClock returns the repository with an explicit UTC clock for
// catalog-seal completion timestamps. A nil function restores time.Now.
// Existing constructors remain valid and default to wall-clock time.
func (r *Repository) WithCatalogSealClock(now func() time.Time) *Repository {
	if r == nil {
		return r
	}
	if now == nil {
		r.catalogSealNow = time.Now
	} else {
		r.catalogSealNow = now
	}
	return r
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
	if err := r.recordAuditIntent(ctx, tx); err != nil {
		return deployment.Deployment{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.Deployment{}, err
	}
	return r.DeploymentByID(ctx, input.ID)
}

func (r *Repository) recordAuditIntent(ctx context.Context, tx *sql.Tx) error {
	intent, ok := deployment.AuditIntentFromContext(ctx)
	if !ok {
		return nil
	}
	if r.hooks.Audit == nil {
		return fmt.Errorf("deployment audit intent recorder is required")
	}
	return r.hooks.Audit.RecordAuditIntent(ctx, tx, intent)
}

func (r *Repository) DeploymentByID(ctx context.Context, id string) (deployment.Deployment, error) {
	row, err := r.queries.GetProjectDeployment(ctx, strings.TrimSpace(id))
	if errors.Is(err, sql.ErrNoRows) {
		return deployment.Deployment{}, deployment.ErrNotFound
	}
	if err != nil {
		return deployment.Deployment{}, err
	}
	return mapDeployment(row)
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
	state, err := deploydb.New(tx).GetServingStateForDeployment(ctx, row.ServingIdentity.GenerationID)
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
	// The following state/pointer writes are one multi-row CAS fence. Each
	// generated method preserves the statement's RowsAffected result so the
	// repository can enforce the ordering and compare-and-swap checks here.
	qtx := deploydb.New(tx)
	var result sql.Result
	if row.PriorGenerationID != "" {
		var drainResult sql.Result
		drainResult, err = qtx.DrainServingStateForActivation(ctx, deploydb.DrainServingStateForActivationParams{ID: row.PriorGenerationID, ProjectID: row.ServingIdentity.ProjectID.String(), Environment: row.ServingIdentity.Environment})
		if err != nil {
			return deployment.Deployment{}, err
		}
		n, _ := drainResult.RowsAffected()
		if n != 1 {
			return deployment.Deployment{}, fmt.Errorf("%w: prior generation changed while draining", deployment.ErrConflict)
		}
	}
	result, err = qtx.ActivateServingStateForActivation(ctx, deploydb.ActivateServingStateForActivationParams{ID: row.ServingIdentity.GenerationID, ProjectID: row.ServingIdentity.ProjectID.String(), Environment: row.ServingIdentity.Environment})
	if err != nil {
		return deployment.Deployment{}, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return deployment.Deployment{}, fmt.Errorf("%w: candidate generation changed while activating", deployment.ErrConflict)
	}
	if row.PriorGenerationID == "" {
		result, err = qtx.InsertActiveServingStateForActivation(ctx, deploydb.InsertActiveServingStateForActivationParams{ProjectID: row.ServingIdentity.ProjectID.String(), Environment: row.ServingIdentity.Environment, GenerationID: row.ServingIdentity.GenerationID})
	} else {
		result, err = qtx.UpdateActiveServingStateForActivation(ctx, deploydb.UpdateActiveServingStateForActivationParams{CandidateGenerationID: row.ServingIdentity.GenerationID, ProjectID: row.ServingIdentity.ProjectID.String(), Environment: row.ServingIdentity.Environment, PriorGenerationID: row.PriorGenerationID})
	}
	if err != nil {
		return deployment.Deployment{}, err
	}
	n, _ = result.RowsAffected()
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

// ActivateSealedDeployment updates only the legacy deployment status
// projection after the sealed delivery coordinator has committed its durable
// target CAS. It deliberately does not read or mutate serving_states,
// project_active_serving_states, DuckLake snapshots, or runtime leases.
func (r *Repository) ActivateSealedDeployment(ctx context.Context, input deployment.ActivationInput) (deployment.Deployment, error) {
	if err := deployment.ValidateActivation(input); err != nil {
		return deployment.Deployment{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.Deployment{}, err
	}
	defer tx.Rollback()
	row, err := deploydb.New(tx).GetProjectDeployment(ctx, input.DeploymentID)
	if errors.Is(err, sql.ErrNoRows) {
		return deployment.Deployment{}, deployment.ErrNotFound
	}
	if err != nil {
		return deployment.Deployment{}, err
	}
	if row.Status == string(deployment.StatusActive) {
		if row.ProjectID != input.ServingIdentity.ProjectID.String() || row.Environment != input.ServingIdentity.Environment || row.GenerationID != input.ServingIdentity.GenerationID || row.ArtifactDigest != input.ArtifactDigest {
			return deployment.Deployment{}, deployment.ErrConflict
		}
		if err := tx.Commit(); err != nil {
			return deployment.Deployment{}, err
		}
		return r.DeploymentByID(ctx, input.DeploymentID)
	}
	if row.Status != string(deployment.StatusPending) || row.ProjectID != input.ServingIdentity.ProjectID.String() || row.Environment != input.ServingIdentity.Environment || row.GenerationID != input.ServingIdentity.GenerationID || row.ArtifactDigest != input.ArtifactDigest {
		return deployment.Deployment{}, deployment.ErrConflict
	}
	if row.PriorGenerationID.Valid && row.PriorGenerationID.String != input.PriorGenerationID || !row.PriorGenerationID.Valid && input.PriorGenerationID != "" {
		return deployment.Deployment{}, deployment.ErrConflict
	}
	if err := deploydb.New(tx).SupersedeOtherProjectDeployments(ctx, deploydb.SupersedeOtherProjectDeploymentsParams{ProjectID: row.ProjectID, Environment: row.Environment, ID: row.ID}); err != nil {
		return deployment.Deployment{}, err
	}
	result, err := deploydb.New(tx).ActivateProjectDeployment(ctx, deploydb.ActivateProjectDeploymentParams{ActivationPrincipal: sql.NullString{String: input.ActivationPrincipal, Valid: true}, VerificationDigest: sql.NullString{String: input.VerificationDigest, Valid: true}, ID: row.ID})
	if err != nil {
		return deployment.Deployment{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return deployment.Deployment{}, deployment.ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return deployment.Deployment{}, err
	}
	return r.DeploymentByID(ctx, input.DeploymentID)
}

func deploymentByTx(ctx context.Context, tx *sql.Tx, id string) (deployment.Deployment, error) {
	row, err := deploydb.New(tx).GetProjectDeployment(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return deployment.Deployment{}, deployment.ErrNotFound
	}
	if err != nil {
		return deployment.Deployment{}, err
	}
	return mapDeployment(row)
}

func mapDeployment(row deploydb.ProjectDeployment) (deployment.Deployment, error) {
	identity, err := graph.NewServingIdentity(graph.ResourceID(row.ProjectID), row.Environment, row.GenerationID)
	if err != nil {
		return deployment.Deployment{}, fmt.Errorf("deployment %q has invalid serving identity: %w", row.ID, err)
	}
	d := deployment.Deployment{ID: row.ID, ServingIdentity: identity, ArtifactDigest: row.ArtifactDigest, RequestDigest: row.RequestDigest, Status: deployment.Status(row.Status), CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, Error: row.Error}
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
	return d, nil
}
func sameCreateRequest(row deployment.Deployment, input deployment.CreateInput) bool {
	return row.ID == input.ID && row.ServingIdentity == input.ServingIdentity && row.ArtifactDigest == input.ArtifactDigest && row.PriorGenerationID == input.PriorGenerationID && row.RequestDigest == input.RequestDigest && row.CreatedBy == input.CreatedBy
}
func (r *Repository) FailDeployment(ctx context.Context, id string, cause error) error {
	if cause == nil {
		return fmt.Errorf("deployment failure cause is required")
	}
	if id == "" || id != strings.TrimSpace(id) {
		return deployment.ErrConflict
	}
	result, err := deploydb.New(r.db).FailProjectDeployment(ctx, deploydb.FailProjectDeploymentParams{Error: cause.Error(), ID: id})
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return deployment.ErrConflict
	}
	return nil
}
func (r *Repository) CancelDeployment(ctx context.Context, id string) (deployment.Deployment, error) {
	if id == "" || id != strings.TrimSpace(id) {
		return deployment.Deployment{}, deployment.ErrConflict
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.Deployment{}, err
	}
	defer tx.Rollback()
	result, err := deploydb.New(tx).CancelProjectDeployment(ctx, id)
	if err != nil {
		return deployment.Deployment{}, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return deployment.Deployment{}, deployment.ErrConflict
	}
	if err := r.recordAuditIntent(ctx, tx); err != nil {
		return deployment.Deployment{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.Deployment{}, err
	}
	return r.DeploymentByID(ctx, id)
}
func nullableSQLString(value string) sql.NullString {
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
