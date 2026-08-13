// Package sqlite persists project-scoped atomic deployments.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/deployment"
	platformdb "github.com/flidai/leapview/internal/deployment/internal/db"
	"github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/platform/transaction"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type Repository struct {
	db    *sql.DB
	q     *platformdb.Queries
	hooks ActivationHooks
	// candidateBaseReadHook is test-only synchronization for exercising the
	// SQLite publication fence at the transaction boundary.
	candidateBaseReadHook func()
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
	return &Repository{db: db, q: platformdb.New(db), hooks: hooks}
}

func (r *Repository) CreateDeployment(ctx context.Context, input deployment.CreateInput) (deployment.Deployment, error) {
	input = normalizeCreateInput(input)
	if err := validateCreateInput(input); err != nil {
		return deployment.Deployment{}, err
	}
	if existing, err := r.DeploymentByID(ctx, input.ID); err == nil {
		if sameCreateRequest(existing, input) {
			if err := r.recordCreationConsequences(ctx, input); err != nil {
				return deployment.Deployment{}, err
			}
			return existing, nil
		}
		return deployment.Deployment{}, fmt.Errorf("%w: deployment id is already used", deployment.ErrConflict)
	} else if !errors.Is(err, deployment.ErrNotFound) {
		return deployment.Deployment{}, err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.Deployment{}, err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	if err := q.CreateProjectDeployment(ctx, platformdb.CreateProjectDeploymentParams{
		ID: input.ID, ProjectID: input.ProjectID, Environment: input.Environment,
		RequestDigest: input.RequestDigest, CreatedBy: input.CreatedBy,
	}); err != nil {
		return deployment.Deployment{}, mapError(err)
	}

	bindings := make(map[string]string)
	var contract projectContract
	for _, target := range input.Targets {
		candidate, err := q.GetServingState(ctx, target.ServingStateID)
		if err != nil {
			return deployment.Deployment{}, mapError(err)
		}
		if candidate.WorkspaceID != target.WorkspaceID || candidate.ProjectID != input.ProjectID || candidate.Environment != input.Environment || !servingStateCanActivate(candidate.Status) {
			return deployment.Deployment{}, fmt.Errorf("%w: serving state %q is not an activatable target", deployment.ErrConflict, target.ServingStateID)
		}
		candidateContract, err := parseProjectContract(candidate)
		if err != nil {
			return deployment.Deployment{}, fmt.Errorf("%w: serving state %q has invalid project metadata: %v", deployment.ErrConflict, target.ServingStateID, err)
		}
		if contract.Digest == "" {
			contract = candidateContract
		} else if contract.Digest != candidateContract.Digest || !slices.Equal(contract.Workspaces, candidateContract.Workspaces) {
			return deployment.Deployment{}, fmt.Errorf("%w: deployment targets were not validated from the same project source", deployment.ErrConflict)
		}
		priorID, err := activeServingStateID(ctx, q, target.WorkspaceID, input.Environment)
		if err != nil {
			return deployment.Deployment{}, err
		}
		if priorID == target.ServingStateID {
			return deployment.Deployment{}, fmt.Errorf("%w: serving state %q is already active", deployment.ErrConflict, target.ServingStateID)
		}
		if err := q.CreateProjectDeploymentTarget(ctx, platformdb.CreateProjectDeploymentTargetParams{
			DeploymentID: input.ID, WorkspaceID: target.WorkspaceID, ServingStateID: target.ServingStateID,
			PriorServingStateID: nullable(priorID),
		}); err != nil {
			return deployment.Deployment{}, mapError(err)
		}
		rows, err := q.ListManagedDataServingStateBindings(ctx, target.ServingStateID)
		if err != nil {
			return deployment.Deployment{}, err
		}
		for _, binding := range rows {
			if binding.ServingStateID != target.ServingStateID || binding.Environment != input.Environment {
				return deployment.Deployment{}, fmt.Errorf("%w: candidate managed-data bindings are incomplete", deployment.ErrConflict)
			}
			collection, err := q.GetManagedDataCollection(ctx, binding.CollectionID)
			if err != nil {
				return deployment.Deployment{}, mapError(err)
			}
			revision, err := q.GetManagedDataRevision(ctx, binding.RevisionID)
			if err != nil {
				return deployment.Deployment{}, mapError(err)
			}
			if collection.ProjectID != input.ProjectID || collection.Status != "active" || revision.CollectionID != collection.ID || revision.Status != "ready" {
				return deployment.Deployment{}, fmt.Errorf("%w: candidate managed-data binding is outside the project or unavailable", deployment.ErrConflict)
			}
			if previous, exists := bindings[collection.ID]; exists && previous != revision.ID {
				return deployment.Deployment{}, fmt.Errorf("%w: candidate workspaces bind different revisions for connection %q", deployment.ErrConflict, collection.ConnectionName)
			}
			bindings[collection.ID] = revision.ID
		}
	}
	if !slices.Equal(targetWorkspaceIDs(input.Targets), contract.Workspaces) {
		return deployment.Deployment{}, fmt.Errorf("%w: deployment targets must contain the complete project workspace set", deployment.ErrConflict)
	}

	collections := sortedKeys(bindings)
	for _, collectionID := range collections {
		priorRevisionID := ""
		priorGeneration := int64(0)
		pointer, err := q.GetManagedDataEnvironmentPointer(ctx, platformdb.GetManagedDataEnvironmentPointerParams{CollectionID: collectionID, Environment: input.Environment})
		if err == nil {
			priorRevisionID = pointer.RevisionID
			priorGeneration = pointer.Generation
		} else if !errors.Is(err, sql.ErrNoRows) {
			return deployment.Deployment{}, err
		}
		if err := q.CreateProjectDeploymentConnection(ctx, platformdb.CreateProjectDeploymentConnectionParams{
			DeploymentID: input.ID, CollectionID: collectionID, RevisionID: bindings[collectionID],
			PriorRevisionID: nullable(priorRevisionID), PriorGeneration: priorGeneration,
		}); err != nil {
			return deployment.Deployment{}, mapError(err)
		}
	}
	if err := r.applyCreationConsequences(ctx, tx, input); err != nil {
		return deployment.Deployment{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.Deployment{}, mapError(err)
	}
	return r.DeploymentByID(ctx, input.ID)
}

func (r *Repository) recordCreationConsequences(ctx context.Context, input deployment.CreateInput) error {
	if input.ReleaseID == "" && input.Workflow.Event.Key == "" && input.Workflow.Job.ID == "" {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := r.applyCreationConsequences(ctx, tx, input); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) applyCreationConsequences(ctx context.Context, tx transaction.Transaction, input deployment.CreateInput) error {
	if input.ReleaseID != "" {
		if r.hooks.LinkRelease == nil {
			return fmt.Errorf("deployment release linkage is required")
		}
		if err := r.hooks.LinkRelease(ctx, tx, input); err != nil {
			return err
		}
	}
	if input.Workflow.Event.Key != "" || input.Workflow.Job.ID != "" {
		if r.hooks.RecordWorkflow == nil {
			return fmt.Errorf("deployment workflow recorder is required")
		}
		if err := r.hooks.RecordWorkflow.RecordWorkflow(ctx, tx, input.Workflow); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) DeploymentByID(ctx context.Context, id string) (deployment.Deployment, error) {
	row, err := r.q.GetProjectDeployment(ctx, strings.TrimSpace(id))
	if err != nil {
		return deployment.Deployment{}, mapError(err)
	}
	targets, err := r.q.ListProjectDeploymentTargets(ctx, row.ID)
	if err != nil {
		return deployment.Deployment{}, err
	}
	connections, err := r.q.ListProjectDeploymentConnections(ctx, row.ID)
	if err != nil {
		return deployment.Deployment{}, err
	}
	return mapDeployment(row, targets, connections), nil
}

func (r *Repository) ActivateDeployment(
	ctx context.Context,
	input deployment.ActivationInput,
) (deployment.Deployment, error) {
	id := strings.TrimSpace(input.DeploymentID)
	input.ActivationPrincipal = strings.TrimSpace(input.ActivationPrincipal)
	input.VerificationDigest = strings.TrimSpace(input.VerificationDigest)
	if id == "" || input.ActivationPrincipal == "" ||
		digest.ValidateSHA256Identity(input.VerificationDigest) != nil {
		return deployment.Deployment{}, fmt.Errorf(
			"deployment, activation principal, and verification digest are required",
		)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.Deployment{}, err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	row, err := q.GetProjectDeployment(ctx, id)
	if err != nil {
		return deployment.Deployment{}, mapError(err)
	}
	if row.Status == string(deployment.StatusActive) {
		if err := tx.Rollback(); err != nil {
			return deployment.Deployment{}, err
		}
		return r.DeploymentByID(ctx, id)
	}
	if row.Status != string(deployment.StatusPending) {
		return deployment.Deployment{}, fmt.Errorf("%w: deployment is %s", deployment.ErrConflict, row.Status)
	}
	targets, err := q.ListProjectDeploymentTargets(ctx, id)
	if err != nil {
		return deployment.Deployment{}, err
	}
	connections, err := q.ListProjectDeploymentConnections(ctx, id)
	if err != nil {
		return deployment.Deployment{}, err
	}

	currentBindings := make(map[string]string, len(connections))
	var contract projectContract
	for _, target := range targets {
		candidate, err := q.GetServingState(ctx, target.ServingStateID)
		if err != nil {
			return deployment.Deployment{}, mapError(err)
		}
		if candidate.WorkspaceID != target.WorkspaceID || candidate.ProjectID != row.ProjectID || candidate.Environment != row.Environment || !servingStateCanActivate(candidate.Status) {
			return deployment.Deployment{}, fmt.Errorf("%w: deployment target %q is no longer activatable", deployment.ErrConflict, target.ServingStateID)
		}
		candidateContract, err := parseProjectContract(candidate)
		if err != nil {
			return deployment.Deployment{}, fmt.Errorf("%w: deployment target %q has invalid project metadata: %v", deployment.ErrConflict, target.ServingStateID, err)
		}
		if contract.Digest == "" {
			contract = candidateContract
		} else if contract.Digest != candidateContract.Digest || !slices.Equal(contract.Workspaces, candidateContract.Workspaces) {
			return deployment.Deployment{}, fmt.Errorf("%w: deployment targets no longer share one project source", deployment.ErrConflict)
		}
		activeID, err := activeServingStateID(ctx, q, target.WorkspaceID, row.Environment)
		if err != nil {
			return deployment.Deployment{}, err
		}
		if activeID != target.PriorServingStateID.String {
			return deployment.Deployment{}, fmt.Errorf("%w: workspace %q active serving state changed", deployment.ErrConflict, target.WorkspaceID)
		}
		bindings, err := q.ListManagedDataServingStateBindings(ctx, target.ServingStateID)
		if err != nil {
			return deployment.Deployment{}, err
		}
		for _, binding := range bindings {
			if binding.Environment != row.Environment {
				return deployment.Deployment{}, fmt.Errorf("%w: candidate managed-data bindings changed", deployment.ErrConflict)
			}
			if previous, exists := currentBindings[binding.CollectionID]; exists && previous != binding.RevisionID {
				return deployment.Deployment{}, fmt.Errorf("%w: candidate managed-data bindings disagree", deployment.ErrConflict)
			}
			currentBindings[binding.CollectionID] = binding.RevisionID
		}
	}
	if !slices.Equal(deploymentTargetWorkspaceIDs(targets), contract.Workspaces) {
		return deployment.Deployment{}, fmt.Errorf("%w: deployment no longer contains the complete project workspace set", deployment.ErrConflict)
	}
	if !sameConnectionBindings(connections, currentBindings) {
		return deployment.Deployment{}, fmt.Errorf("%w: candidate managed-data bindings changed", deployment.ErrConflict)
	}

	for _, connection := range connections {
		pointer, err := q.GetManagedDataEnvironmentPointer(ctx, platformdb.GetManagedDataEnvironmentPointerParams{CollectionID: connection.CollectionID, Environment: row.Environment})
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if connection.PriorGeneration != 0 || connection.PriorRevisionID.Valid {
				return deployment.Deployment{}, fmt.Errorf("%w: managed connection pointer changed", deployment.ErrConflict)
			}
		case err != nil:
			return deployment.Deployment{}, err
		case pointer.Generation != connection.PriorGeneration || pointer.RevisionID != connection.PriorRevisionID.String:
			return deployment.Deployment{}, fmt.Errorf("%w: managed connection pointer changed", deployment.ErrConflict)
		}
		generation := connection.PriorGeneration + 1
		if err := q.UpsertManagedDataEnvironmentPointer(ctx, platformdb.UpsertManagedDataEnvironmentPointerParams{
			CollectionID: connection.CollectionID, Environment: row.Environment, RevisionID: connection.RevisionID,
			DeploymentID: row.ID, Generation: generation, UpdatedBy: row.CreatedBy,
		}); err != nil {
			return deployment.Deployment{}, err
		}
		result, err := q.ActivateProjectDeploymentConnection(ctx, platformdb.ActivateProjectDeploymentConnectionParams{
			ActivatedGeneration: sql.NullInt64{Int64: generation, Valid: true}, DeploymentID: row.ID, CollectionID: connection.CollectionID,
		})
		if err := requireOne(result, err, "deployment connection changed while activating"); err != nil {
			return deployment.Deployment{}, err
		}
	}

	for _, target := range targets {
		candidate, err := q.GetServingState(ctx, target.ServingStateID)
		if err != nil {
			return deployment.Deployment{}, mapError(err)
		}
		if r.hooks.ApplyAccessSnapshot == nil {
			return deployment.Deployment{}, fmt.Errorf("%w: access snapshot activation is not configured", deployment.ErrConflict)
		}
		if err := r.hooks.ApplyAccessSnapshot(ctx, tx, candidate.ID); err != nil {
			return deployment.Deployment{}, fmt.Errorf("%w: apply access snapshot for workspace %q: %v", deployment.ErrConflict, target.WorkspaceID, err)
		}
		if row.Environment == "prod" {
			var publications map[string]json.RawMessage
			if err := json.Unmarshal([]byte(candidate.DashboardPublicationsJson), &publications); err != nil {
				return deployment.Deployment{}, fmt.Errorf("%w: decode publication snapshot for workspace %q: %v", deployment.ErrConflict, target.WorkspaceID, err)
			}
			if publications == nil {
				publications = map[string]json.RawMessage{}
			}
			if r.hooks.ReconcilePublications == nil {
				return deployment.Deployment{}, fmt.Errorf("%w: publication activation is not configured", deployment.ErrConflict)
			}
			if err := r.hooks.ReconcilePublications(ctx, tx, PublicationReconcileInput{
				ProjectID: candidate.ProjectID, WorkspaceID: candidate.WorkspaceID, ServingStateID: candidate.ID,
				ActorID: row.CreatedBy, Publications: publications,
			}); err != nil {
				return deployment.Deployment{}, fmt.Errorf("%w: reconcile publications for workspace %q: %v", deployment.ErrConflict, target.WorkspaceID, err)
			}
		}
		if r.hooks.ApplyDashboardAppearances != nil {
			var appearances map[string]json.RawMessage
			if err := json.Unmarshal([]byte(candidate.DashboardAppearancesJson), &appearances); err != nil {
				return deployment.Deployment{}, fmt.Errorf("%w: decode dashboard appearance snapshot for workspace %q: %v", deployment.ErrConflict, target.WorkspaceID, err)
			}
			if appearances == nil {
				appearances = map[string]json.RawMessage{}
			}
			if err := r.hooks.ApplyDashboardAppearances(ctx, tx, DashboardAppearanceActivationInput{
				ProjectID: candidate.ProjectID, WorkspaceID: candidate.WorkspaceID, ServingStateID: candidate.ID,
				ActorID: row.CreatedBy, Appearances: appearances,
			}); err != nil {
				return deployment.Deployment{}, fmt.Errorf("%w: apply dashboard appearances for workspace %q: %v", deployment.ErrConflict, target.WorkspaceID, err)
			}
		}
		if err := q.MarkOtherServingStatesDraining(ctx, platformdb.MarkOtherServingStatesDrainingParams{WorkspaceID: target.WorkspaceID, Environment: row.Environment, ID: target.ServingStateID}); err != nil {
			return deployment.Deployment{}, err
		}
		if err := q.MarkServingStateActive(ctx, target.ServingStateID); err != nil {
			return deployment.Deployment{}, err
		}
		if err := q.SetActiveServingState(ctx, platformdb.SetActiveServingStateParams{WorkspaceID: target.WorkspaceID, Environment: row.Environment, ServingStateID: target.ServingStateID}); err != nil {
			return deployment.Deployment{}, err
		}
		if candidate.DucklakeSnapshotID > 0 {
			if err := persistPublishDataVersions(ctx, q, candidate, row.Environment); err != nil {
				return deployment.Deployment{}, err
			}
			if err := q.DeleteUndeployedSemanticModelDataVersions(ctx, platformdb.DeleteUndeployedSemanticModelDataVersionsParams{
				WorkspaceID: candidate.WorkspaceID, Environment: row.Environment, TargetServingStateID: candidate.ID,
			}); err != nil {
				return deployment.Deployment{}, err
			}
		}
		result, err := q.ActivateProjectDeploymentTarget(ctx, platformdb.ActivateProjectDeploymentTargetParams{DeploymentID: row.ID, WorkspaceID: target.WorkspaceID})
		if err := requireOne(result, err, "deployment target changed while activating"); err != nil {
			return deployment.Deployment{}, err
		}
	}
	if err := q.SupersedeOtherProjectDeployments(ctx, platformdb.SupersedeOtherProjectDeploymentsParams{ProjectID: row.ProjectID, Environment: row.Environment, ID: row.ID}); err != nil {
		return deployment.Deployment{}, err
	}
	result, err := q.ActivateProjectDeployment(
		ctx,
		platformdb.ActivateProjectDeploymentParams{
			ActivationPrincipal: sql.NullString{
				String: input.ActivationPrincipal,
				Valid:  true,
			},
			VerificationDigest: sql.NullString{
				String: input.VerificationDigest,
				Valid:  true,
			},
			ID: row.ID,
		},
	)
	if err := requireOne(result, err, "deployment changed while activating"); err != nil {
		return deployment.Deployment{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.Deployment{}, mapError(err)
	}
	return r.DeploymentByID(ctx, id)
}

func persistPublishDataVersions(ctx context.Context, q *platformdb.Queries, candidate platformdb.ServingState, environment string) error {
	return q.PersistPublishSemanticModelDataVersions(ctx, platformdb.PersistPublishSemanticModelDataVersionsParams{
		Environment: environment, SnapshotID: candidate.DucklakeSnapshotID, TargetServingStateID: candidate.ID,
	})
}

type projectContract struct {
	Digest     string
	Workspaces []string
}

func parseProjectContract(candidate platformdb.ServingState) (projectContract, error) {
	if err := digest.ValidateSHA256Identity(candidate.ProjectDigest); err != nil {
		return projectContract{}, err
	}
	var workspaces []string
	if err := json.Unmarshal([]byte(candidate.ProjectWorkspacesJson), &workspaces); err != nil {
		return projectContract{}, fmt.Errorf("decode project workspaces: %w", err)
	}
	if len(workspaces) == 0 || !sort.StringsAreSorted(workspaces) {
		return projectContract{}, fmt.Errorf("project workspaces must be non-empty and sorted")
	}
	for index, workspaceID := range workspaces {
		if strings.TrimSpace(workspaceID) == "" || workspaceID != strings.TrimSpace(workspaceID) || (index > 0 && workspaces[index-1] == workspaceID) {
			return projectContract{}, fmt.Errorf("project workspaces must contain unique canonical identifiers")
		}
	}
	if !slices.Contains(workspaces, candidate.WorkspaceID) {
		return projectContract{}, fmt.Errorf("project workspaces omit candidate workspace %q", candidate.WorkspaceID)
	}
	return projectContract{Digest: candidate.ProjectDigest, Workspaces: workspaces}, nil
}

func targetWorkspaceIDs(targets []deployment.TargetInput) []string {
	workspaces := make([]string, len(targets))
	for index, target := range targets {
		workspaces[index] = target.WorkspaceID
	}
	return workspaces
}

func deploymentTargetWorkspaceIDs(targets []platformdb.ProjectDeploymentTarget) []string {
	workspaces := make([]string, len(targets))
	for index, target := range targets {
		workspaces[index] = target.WorkspaceID
	}
	sort.Strings(workspaces)
	return workspaces
}

func (r *Repository) FailDeployment(ctx context.Context, id string, cause error) error {
	if cause == nil || strings.TrimSpace(cause.Error()) == "" {
		return fmt.Errorf("deployment failure cause is required")
	}
	result, err := r.q.FailProjectDeployment(ctx, platformdb.FailProjectDeploymentParams{Error: cause.Error(), ID: strings.TrimSpace(id)})
	return requireOne(result, err, "deployment is not pending")
}

func (r *Repository) CancelDeployment(ctx context.Context, id string) (deployment.Deployment, error) {
	id = strings.TrimSpace(id)
	result, err := r.q.CancelProjectDeployment(ctx, id)
	if err := requireOne(result, err, "deployment is not pending"); err != nil {
		return deployment.Deployment{}, err
	}
	return r.DeploymentByID(ctx, id)
}

func normalizeCreateInput(input deployment.CreateInput) deployment.CreateInput {
	input.ID = strings.TrimSpace(input.ID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.Environment = strings.TrimSpace(input.Environment)
	input.RequestDigest = strings.TrimSpace(input.RequestDigest)
	input.CreatedBy = strings.TrimSpace(input.CreatedBy)
	input.ReleaseID = strings.TrimSpace(input.ReleaseID)
	input.RollbackOf = strings.TrimSpace(input.RollbackOf)
	input.Targets = append([]deployment.TargetInput(nil), input.Targets...)
	for index := range input.Targets {
		input.Targets[index].WorkspaceID = strings.TrimSpace(input.Targets[index].WorkspaceID)
		input.Targets[index].ServingStateID = strings.TrimSpace(input.Targets[index].ServingStateID)
	}
	sort.Slice(input.Targets, func(i, j int) bool { return input.Targets[i].WorkspaceID < input.Targets[j].WorkspaceID })
	return input
}

func validateCreateInput(input deployment.CreateInput) error {
	if input.ID == "" || input.ProjectID == "" || input.Environment == "" || input.RequestDigest == "" || len(input.Targets) == 0 {
		return fmt.Errorf("deployment id, project, environment, request digest, and targets are required")
	}
	workspaces := map[string]struct{}{}
	states := map[string]struct{}{}
	for _, target := range input.Targets {
		if target.WorkspaceID == "" || target.ServingStateID == "" {
			return fmt.Errorf("deployment target workspace and serving state are required")
		}
		if _, duplicate := workspaces[target.WorkspaceID]; duplicate {
			return fmt.Errorf("duplicate deployment workspace %q", target.WorkspaceID)
		}
		if _, duplicate := states[target.ServingStateID]; duplicate {
			return fmt.Errorf("duplicate deployment serving state %q", target.ServingStateID)
		}
		workspaces[target.WorkspaceID] = struct{}{}
		states[target.ServingStateID] = struct{}{}
	}
	return nil
}

func sameCreateRequest(row deployment.Deployment, input deployment.CreateInput) bool {
	if row.ID != input.ID || row.ProjectID != input.ProjectID || row.Environment != input.Environment || row.RequestDigest != input.RequestDigest || row.CreatedBy != input.CreatedBy || len(row.Targets) != len(input.Targets) {
		return false
	}
	for index := range input.Targets {
		if row.Targets[index].WorkspaceID != input.Targets[index].WorkspaceID || row.Targets[index].ServingStateID != input.Targets[index].ServingStateID {
			return false
		}
	}
	return true
}

func sameConnectionBindings(rows []platformdb.ProjectDeploymentConnection, bindings map[string]string) bool {
	if len(rows) != len(bindings) {
		return false
	}
	for _, row := range rows {
		if bindings[row.CollectionID] != row.RevisionID {
			return false
		}
	}
	return true
}

func activeServingStateID(ctx context.Context, q *platformdb.Queries, workspaceID, environment string) (string, error) {
	id, err := q.GetWorkspaceActiveServingStateID(ctx, platformdb.GetWorkspaceActiveServingStateIDParams{WorkspaceID: workspaceID, Environment: environment})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func servingStateCanActivate(status string) bool {
	return servingstate.State{Status: servingstate.Status(status)}.CanActivate()
}

func mapDeployment(row platformdb.ProjectDeployment, targets []platformdb.ProjectDeploymentTarget, connections []platformdb.ProjectDeploymentConnection) deployment.Deployment {
	out := deployment.Deployment{
		ID: row.ID, ProjectID: row.ProjectID, Environment: row.Environment, RequestDigest: row.RequestDigest,
		Status: deployment.Status(row.Status), CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, Error: row.Error,
		Targets: make([]deployment.Target, 0, len(targets)), Connections: make([]deployment.ConnectionPointer, 0, len(connections)),
	}
	if row.ActivatedAt.Valid {
		out.ActivatedAt = row.ActivatedAt.String
	}
	if row.ActivationPrincipal.Valid {
		out.ActivationPrincipal = row.ActivationPrincipal.String
	}
	if row.VerificationDigest.Valid {
		out.VerificationDigest = row.VerificationDigest.String
	}
	if row.VerifiedAt.Valid {
		out.VerifiedAt = row.VerifiedAt.String
	}
	for _, target := range targets {
		mapped := deployment.Target{
			DeploymentID: target.DeploymentID, WorkspaceID: target.WorkspaceID, ServingStateID: target.ServingStateID,
			PriorServingStateID: target.PriorServingStateID.String, Status: deployment.TargetStatus(target.Status), Error: target.Error,
		}
		if target.ActivatedAt.Valid {
			mapped.ActivatedAt = target.ActivatedAt.String
		}
		out.Targets = append(out.Targets, mapped)
	}
	for _, connection := range connections {
		out.Connections = append(out.Connections, deployment.ConnectionPointer{
			DeploymentID: connection.DeploymentID, CollectionID: connection.CollectionID, RevisionID: connection.RevisionID,
			PriorRevisionID: connection.PriorRevisionID.String, PriorGeneration: connection.PriorGeneration,
			ActivatedGeneration: connection.ActivatedGeneration.Int64,
		})
	}
	return out
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func nullable(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

func requireOne(result sql.Result, err error, message string) error {
	if err != nil {
		return mapError(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("%w: %s", deployment.ErrConflict, message)
	}
	return nil
}

func mapError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return deployment.ErrNotFound
	}
	if strings.Contains(strings.ToLower(err.Error()), "constraint") {
		return fmt.Errorf("%w: %v", deployment.ErrConflict, err)
	}
	return err
}

var _ deployment.Repository = (*Repository)(nil)
