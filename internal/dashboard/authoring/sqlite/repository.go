package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/document"
	dashboarddb "github.com/flidai/leapview/internal/dashboard/internal/db"
	"github.com/flidai/leapview/internal/project/graph"
)

// Repository persists the dashboard authoring projection in the platform
// SQLite database. Queries are generated from the checked-in authoring SQL and
// transaction-scoped so CAS checks, immutable revision insertion, pointer
// updates, and command evidence commit as one unit.
type Repository struct{ db *sql.DB }

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

var _ authoring.Repository = (*Repository)(nil)

func (r *Repository) Create(ctx context.Context, input authoring.CreateInput) (authoring.DashboardLifecycle, error) {
	if err := input.ProjectID.Validate(); err != nil {
		return authoring.DashboardLifecycle{}, fmt.Errorf("project id is required: %w", err)
	}
	projectID := input.ProjectID.String()
	if input.Lifecycle.ProjectID.String() != projectID {
		return authoring.DashboardLifecycle{}, fmt.Errorf("%w: lifecycle project does not match create project", authoring.ErrInvalidAuthoring)
	}
	if err := input.Lifecycle.Validate(); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if input.Lifecycle.Status != authoring.LifecycleStatusDraft || input.Lifecycle.Draft == nil || input.Lifecycle.Published != nil {
		return authoring.DashboardLifecycle{}, fmt.Errorf("%w: dashboard create accepts draft lifecycle only", authoring.ErrInvalidAuthoring)
	}
	if err := input.Revision.Validate(); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if input.Revision.DashboardID != input.Lifecycle.ID {
		return authoring.DashboardLifecycle{}, fmt.Errorf("%w: revision belongs to dashboard %q", authoring.ErrInvalidAuthoring, input.Revision.DashboardID)
	}
	if input.Lifecycle.SemanticModel.String() != input.Revision.Document.Spec.SemanticModel {
		return authoring.DashboardLifecycle{}, fmt.Errorf("%w: lifecycle semantic model does not match initial revision", authoring.ErrInvalidAuthoring)
	}
	if input.Lifecycle.Title != canonicalDocumentTitle(input.Revision.Document) {
		return authoring.DashboardLifecycle{}, fmt.Errorf("%w: lifecycle title does not match initial revision", authoring.ErrInvalidAuthoring)
	}
	if !lifecycleReferencesRevision(input.Lifecycle, input.Revision.Token()) {
		return authoring.DashboardLifecycle{}, fmt.Errorf("%w: initial revision is not selected by lifecycle", authoring.ErrInvalidAuthoring)
	}
	if input.Operation.Enabled() {
		if err := validateCreateOperation(input.Operation, projectID, input.Lifecycle.ID, input.Revision.Token()); err != nil {
			return authoring.DashboardLifecycle{}, err
		}
	}
	documentJSON, provenanceJSON, err := encodeRevision(input.Revision)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}

	tx, err := r.begin(ctx)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	defer tx.Rollback()
	q := dashboarddb.New(r.db).WithTx(tx)
	if input.Operation.Enabled() {
		if replay, found, err := lookupCreateOperation(ctx, tx, input.Operation); err != nil {
			return authoring.DashboardLifecycle{}, err
		} else if found {
			if replay.DashboardID == "" {
				return authoring.DashboardLifecycle{}, fmt.Errorf("%w: create operation has no dashboard result", authoring.ErrInvalidAuthoring)
			}
			lifecycle, err := r.getLifecycle(ctx, q, projectID, replay.DashboardID)
			if err != nil {
				return authoring.DashboardLifecycle{}, err
			}
			return lifecycle, nil
		}
		if err := insertCreateOperation(ctx, tx, input.Operation, input.Lifecycle.ID, input.Revision.Token()); err != nil {
			if !isConstraint(err) {
				return authoring.DashboardLifecycle{}, err
			}
			// Another transaction may have claimed the same operation between
			// lookup and insert. Read its immutable result and replay it.
			replay, found, lookupErr := lookupCreateOperation(ctx, tx, input.Operation)
			if lookupErr != nil {
				return authoring.DashboardLifecycle{}, lookupErr
			}
			if !found {
				return authoring.DashboardLifecycle{}, authoring.ErrCommandReuse
			}
			lifecycle, getErr := r.getLifecycle(ctx, q, projectID, replay.DashboardID)
			if getErr != nil {
				return authoring.DashboardLifecycle{}, getErr
			}
			return lifecycle, nil
		}
	}
	err = q.InsertAuthoringDashboard(ctx, dashboarddb.InsertAuthoringDashboardParams{ProjectID: projectID,
		DashboardID: string(input.Lifecycle.ID), OwnerPrincipalID: input.Lifecycle.OwnerPrincipalID,
		Slug: input.Lifecycle.Slug, Title: input.Lifecycle.Title, SemanticModel: input.Lifecycle.SemanticModel.String(),
		Visibility: string(input.Lifecycle.Visibility), Status: string(input.Lifecycle.Status)})
	if err != nil {
		if isConstraint(err) {
			return authoring.DashboardLifecycle{}, fmt.Errorf("%w: dashboard identity already exists or slug is in use", authoring.ErrConflict)
		}
		return authoring.DashboardLifecycle{}, err
	}
	if err := insertRevision(ctx, q, projectID, input.Revision, documentJSON, provenanceJSON); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if input.Lifecycle.Draft != nil {
		if err := insertDraft(ctx, q, projectID, input.Lifecycle.ID, *input.Lifecycle.Draft); err != nil {
			return authoring.DashboardLifecycle{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	return r.Get(ctx, input.ProjectID, input.Lifecycle.ID)
}

func (r *Repository) Get(ctx context.Context, projectID graph.ResourceID, dashboardID authoring.DashboardID) (authoring.DashboardLifecycle, error) {
	if err := projectID.Validate(); err != nil {
		return authoring.DashboardLifecycle{}, fmt.Errorf("project id is required: %w", err)
	}
	if err := dashboardID.Validate(); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	lifecycle, err := r.getLifecycle(ctx, dashboarddb.New(r.db), projectID.String(), dashboardID)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	failure, ok, err := r.latestRevalidationFailure(ctx, projectID.String(), dashboardID)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if ok {
		lifecycle.Revalidation = &failure
	}
	if err := lifecycle.Validate(); err != nil {
		return authoring.DashboardLifecycle{}, fmt.Errorf("validate stored dashboard lifecycle: %w", err)
	}
	return lifecycle, nil
}

func (r *Repository) List(ctx context.Context, projectID graph.ResourceID) ([]authoring.DashboardLifecycle, error) {
	if err := projectID.Validate(); err != nil {
		return nil, fmt.Errorf("project id is required: %w", err)
	}
	projectKey := projectID.String()
	rows, err := dashboarddb.New(r.db).ListAuthoringDashboards(ctx, projectKey)
	if err != nil {
		return nil, err
	}
	out := make([]authoring.DashboardLifecycle, 0, len(rows))
	for _, row := range rows {
		item, err := r.Get(ctx, projectID, authoring.DashboardID(row.DashboardID))
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

// CommitRevalidation atomically records immutable generation evidence and
// advances only the published compilation pointer whose authored revision and
// prior compiled identity still match. The active-generation check is inside
// the same transaction, so a concurrent activation cannot publish stale
// evidence.
func (r *Repository) CommitRevalidation(ctx context.Context, input authoring.RevalidationCommit) error {
	if err := validateRevalidationCommit(input); err != nil {
		return err
	}
	depsJSON, err := json.Marshal(input.DependencyIDs)
	if err != nil {
		return err
	}
	identityJSON, err := encodeServingIdentity(input.Generation.Identity)
	if err != nil {
		return err
	}
	priorIdentityJSON, err := encodeServingIdentity(input.PriorCompilation.SemanticIdentity)
	if err != nil {
		return err
	}
	compiledIdentityJSON, err := encodeServingIdentity(input.Compilation.SemanticIdentity)
	if err != nil {
		return err
	}
	definitionJSON, err := json.Marshal(input.Compilation.Definition)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var activeGeneration string
	err = tx.QueryRowContext(ctx, `SELECT generation_id FROM project_active_serving_states WHERE project_id = ? AND environment = ?`, input.Generation.Identity.ProjectID.String(), input.Generation.Identity.Environment).Scan(&activeGeneration)
	if errors.Is(err, sql.ErrNoRows) || activeGeneration != input.Generation.Identity.GenerationID {
		return authoring.ErrGenerationSuperseded
	}
	if err != nil {
		return err
	}
	var current struct {
		revisionID, contentHash, compiledID, compiledHash, compiledDefinition, compiledModel, compiledIdentity string
		revisionNumber, compiledNumber                                                                         int64
	}
	err = tx.QueryRowContext(ctx, `SELECT revision_id, revision_number, content_hash, compiled_revision_id, compiled_revision_number, compiled_content_hash, compiled_definition_hash, compiled_semantic_model_id, compiled_semantic_identity_json FROM dashboard_authoring_published WHERE project_id = ? AND dashboard_id = ?`, input.Dashboard.ProjectID.String(), input.Dashboard.ID.String()).Scan(&current.revisionID, &current.revisionNumber, &current.contentHash, &current.compiledID, &current.compiledNumber, &current.compiledHash, &current.compiledDefinition, &current.compiledModel, &current.compiledIdentity)
	if errors.Is(err, sql.ErrNoRows) {
		return authoring.ErrNotFound
	}
	if err != nil {
		return err
	}
	if current.revisionID != string(input.Dashboard.Published.Revision.RevisionID) || current.revisionNumber != int64(input.Dashboard.Published.Revision.Number) || current.contentHash != input.Dashboard.Published.Revision.ContentHash || current.compiledID != string(input.PriorCompilation.AuthoredRevision.RevisionID) || current.compiledNumber != int64(input.PriorCompilation.AuthoredRevision.Number) || current.compiledHash != input.PriorCompilation.AuthoredRevision.ContentHash || current.compiledDefinition != input.PriorCompilation.DefinitionHash || current.compiledIdentity != priorIdentityJSON || current.compiledModel != input.PriorCompilation.SemanticModelID.String() {
		return authoring.ErrRevalidationConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO dashboard_authoring_compiled_revisions (project_id, dashboard_id, revision_id, revision_number, content_hash, definition_json, definition_hash, semantic_model_id, semantic_identity_json, compiled_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(project_id, dashboard_id, revision_id, revision_number, content_hash, definition_hash, semantic_model_id, semantic_identity_json) DO NOTHING`, input.Compilation.ProjectID.String(), input.Compilation.DashboardID.String(), input.Compilation.AuthoredRevision.RevisionID.String(), input.Compilation.AuthoredRevision.Number, input.Compilation.AuthoredRevision.ContentHash, string(definitionJSON), input.Compilation.DefinitionHash, input.Compilation.SemanticModelID.String(), compiledIdentityJSON, formatTime(input.Compilation.CompiledAt))
	if err != nil {
		return err
	}
	var storedDefinition string
	err = tx.QueryRowContext(ctx, `SELECT definition_json FROM dashboard_authoring_compiled_revisions WHERE project_id = ? AND dashboard_id = ? AND revision_id = ? AND revision_number = ? AND content_hash = ? AND definition_hash = ? AND semantic_model_id = ? AND semantic_identity_json = ?`, input.Compilation.ProjectID.String(), input.Compilation.DashboardID.String(), input.Compilation.AuthoredRevision.RevisionID.String(), input.Compilation.AuthoredRevision.Number, input.Compilation.AuthoredRevision.ContentHash, input.Compilation.DefinitionHash, input.Compilation.SemanticModelID.String(), compiledIdentityJSON).Scan(&storedDefinition)
	if err != nil {
		return err
	}
	if storedDefinition != string(definitionJSON) {
		return authoring.ErrRevalidationConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO dashboard_authoring_revalidation_attempts (project_id, dashboard_id, generation_id, attempt_id, generation_identity_json, graph_digest, dependency_ids_json, authored_revision_id, authored_revision_number, authored_content_hash, prior_compiled_identity_json, status, compiled_definition_hash, compiled_semantic_model_id, compiled_semantic_identity_json, attempted_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'succeeded', ?, ?, ?, ?)`, input.Dashboard.ProjectID.String(), input.Dashboard.ID.String(), input.Generation.Identity.GenerationID, input.AttemptID, identityJSON, input.Generation.Graph.Digest(), string(depsJSON), input.AuthoredRevision.ID.String(), input.AuthoredRevision.Number, input.AuthoredRevision.ContentHash, priorIdentityJSON, input.Compilation.DefinitionHash, input.Compilation.SemanticModelID.String(), compiledIdentityJSON, formatTime(input.AttemptedAt))
	if isConstraint(err) {
		return authoring.ErrRevalidationConflict
	}
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE dashboard_authoring_published SET compiled_revision_id = ?, compiled_revision_number = ?, compiled_content_hash = ?, compiled_definition_hash = ?, compiled_semantic_model_id = ?, compiled_semantic_identity_json = ? WHERE project_id = ? AND dashboard_id = ? AND revision_id = ? AND revision_number = ? AND content_hash = ? AND compiled_revision_id = ? AND compiled_revision_number = ? AND compiled_content_hash = ? AND compiled_definition_hash = ? AND compiled_semantic_model_id = ? AND compiled_semantic_identity_json = ?`, input.Compilation.AuthoredRevision.RevisionID.String(), input.Compilation.AuthoredRevision.Number, input.Compilation.AuthoredRevision.ContentHash, input.Compilation.DefinitionHash, input.Compilation.SemanticModelID.String(), compiledIdentityJSON, input.Dashboard.ProjectID.String(), input.Dashboard.ID.String(), input.Dashboard.Published.Revision.RevisionID.String(), input.Dashboard.Published.Revision.Number, input.Dashboard.Published.Revision.ContentHash, input.PriorCompilation.AuthoredRevision.RevisionID.String(), input.PriorCompilation.AuthoredRevision.Number, input.PriorCompilation.AuthoredRevision.ContentHash, input.PriorCompilation.DefinitionHash, input.PriorCompilation.SemanticModelID.String(), priorIdentityJSON)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return authoring.ErrRevalidationConflict
	}
	return tx.Commit()
}

// RecordRevalidationFailure appends immutable failure evidence without
// changing published rows. Each retry supplies a new opaque attempt ID, so
// every diagnostic remains available for forensic inspection.
func (r *Repository) RecordRevalidationFailure(ctx context.Context, input authoring.RevalidationFailureInput) error {
	if err := validateRevalidationFailureInput(input); err != nil {
		return err
	}
	depsJSON, err := json.Marshal(input.DependencyIDs)
	if err != nil {
		return err
	}
	identityJSON, err := encodeServingIdentity(input.Generation.Identity)
	if err != nil {
		return err
	}
	priorIdentityJSON, err := encodeServingIdentity(input.PriorCompilation.SemanticIdentity)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var activeGeneration string
	err = tx.QueryRowContext(ctx, `SELECT generation_id FROM project_active_serving_states WHERE project_id = ? AND environment = ?`, input.Generation.Identity.ProjectID.String(), input.Generation.Identity.Environment).Scan(&activeGeneration)
	if errors.Is(err, sql.ErrNoRows) || activeGeneration != input.Generation.Identity.GenerationID {
		return authoring.ErrGenerationSuperseded
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO dashboard_authoring_revalidation_attempts (project_id, dashboard_id, generation_id, attempt_id, generation_identity_json, graph_digest, dependency_ids_json, authored_revision_id, authored_revision_number, authored_content_hash, prior_compiled_identity_json, status, error_code, error_message, attempted_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'failed', ?, ?, ?)`, input.Dashboard.ProjectID.String(), input.Dashboard.ID.String(), input.Generation.Identity.GenerationID, input.AttemptID, identityJSON, input.Generation.Graph.Digest(), string(depsJSON), input.AuthoredRevision.ID.String(), input.AuthoredRevision.Number, input.AuthoredRevision.ContentHash, priorIdentityJSON, input.Failure.Code, input.Failure.Message, formatTime(input.Failure.FailedAt))
	if isConstraint(err) {
		return authoring.ErrRevalidationConflict
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func validateRevalidationCommit(input authoring.RevalidationCommit) error {
	if err := authoring.ValidateRevalidationAttemptID(input.AttemptID); err != nil {
		return fmt.Errorf("%w: %v", authoring.ErrInvalidAuthoring, err)
	}
	if err := input.Generation.Validate(); err != nil {
		return err
	}
	if input.Dashboard.Published == nil || input.Dashboard.Status != authoring.LifecycleStatusPublished {
		return fmt.Errorf("%w: revalidation requires a published dashboard", authoring.ErrInvalidAuthoring)
	}
	if err := input.AuthoredRevision.Validate(); err != nil {
		return err
	}
	if err := input.Compilation.Validate(); err != nil {
		return err
	}
	if err := input.PriorCompilation.Validate(); err != nil {
		return err
	}
	if input.AttemptedAt.IsZero() || input.AttemptedAt.Location() != time.UTC {
		return fmt.Errorf("%w: revalidation attempt timestamp must be UTC", authoring.ErrInvalidAuthoring)
	}
	return nil
}

func validateRevalidationFailureInput(input authoring.RevalidationFailureInput) error {
	if err := authoring.ValidateRevalidationAttemptID(input.AttemptID); err != nil {
		return fmt.Errorf("%w: %v", authoring.ErrInvalidAuthoring, err)
	}
	if err := input.Generation.Validate(); err != nil {
		return err
	}
	if input.Dashboard.Published == nil || input.Dashboard.Status != authoring.LifecycleStatusPublished {
		return fmt.Errorf("%w: revalidation requires a published dashboard", authoring.ErrInvalidAuthoring)
	}
	if err := input.Failure.Validate(); err != nil {
		return err
	}
	return nil
}

// CountBySemanticModel returns the non-archived authoring dashboard counts for
// each semantic model in deterministic semantic-model order.
func (r *Repository) CountBySemanticModel(ctx context.Context, projectID graph.ResourceID) ([]authoring.SemanticModelUsage, error) {
	if err := projectID.Validate(); err != nil {
		return nil, fmt.Errorf("project id is required: %w", err)
	}
	rows, err := dashboarddb.New(r.db).CountAuthoringDashboardsBySemanticModel(ctx, projectID.String())
	if err != nil {
		return nil, err
	}
	out := make([]authoring.SemanticModelUsage, 0, len(rows))
	for _, row := range rows {
		private, err := nonNegativeCount(row.PrivateCount)
		if err != nil {
			return nil, fmt.Errorf("semantic model %q private count: %w", row.SemanticModel, err)
		}
		organization, err := nonNegativeCount(row.OrganizationCount)
		if err != nil {
			return nil, fmt.Errorf("semantic model %q organization count: %w", row.SemanticModel, err)
		}
		total, err := nonNegativeCount(row.TotalCount)
		if err != nil {
			return nil, fmt.Errorf("semantic model %q total count: %w", row.SemanticModel, err)
		}
		usage, err := authoring.NewSemanticModelUsage(row.SemanticModel, private, organization)
		if err != nil {
			return nil, err
		}
		if usage.Total != total {
			return nil, fmt.Errorf("%w: semantic model %q count buckets do not match total", authoring.ErrInvalidAuthoring, row.SemanticModel)
		}
		out = append(out, usage)
	}
	return out, nil
}

func nonNegativeCount(value int64) (uint64, error) {
	if value < 0 {
		return 0, fmt.Errorf("%w: count cannot be negative", authoring.ErrInvalidAuthoring)
	}
	return uint64(value), nil
}

func (r *Repository) GetRevision(ctx context.Context, projectID graph.ResourceID, dashboardID authoring.DashboardID, revisionID authoring.RevisionID) (authoring.Revision, error) {
	if err := projectID.Validate(); err != nil {
		return authoring.Revision{}, fmt.Errorf("project id is required: %w", err)
	}
	if err := dashboardID.Validate(); err != nil {
		return authoring.Revision{}, err
	}
	if err := revisionID.Validate(); err != nil {
		return authoring.Revision{}, err
	}
	return getRevision(ctx, dashboarddb.New(r.db), projectID.String(), dashboardID, revisionID)
}

// LookupCommandResult returns durable idempotency evidence before a caller
// evaluates its expected revision. The fingerprint check belongs here (and in
// the transaction-scoped CAS methods below) so a reused command ID can never
// be mistaken for an optimistic-concurrency conflict after later edits.
func (r *Repository) LookupCommandResult(ctx context.Context, projectID graph.ResourceID, dashboardID authoring.DashboardID, evidence authoring.CommandEvidence) (authoring.CommandResult, bool, error) {
	if err := projectID.Validate(); err != nil {
		return authoring.CommandResult{}, false, fmt.Errorf("project id is required: %w", err)
	}
	if err := dashboardID.Validate(); err != nil {
		return authoring.CommandResult{}, false, err
	}
	if err := evidence.Validate(); err != nil {
		return authoring.CommandResult{}, false, err
	}
	row, err := dashboarddb.New(r.db).GetAuthoringCommand(ctx, dashboarddb.GetAuthoringCommandParams{ProjectID: projectID.String(), DashboardID: string(dashboardID), CommandID: string(evidence.ID)})
	if errors.Is(err, sql.ErrNoRows) {
		return authoring.CommandResult{}, false, nil
	}
	if err != nil {
		return authoring.CommandResult{}, false, err
	}
	if row.RequestFingerprint != evidence.Fingerprint {
		return authoring.CommandResult{}, false, authoring.ErrCommandReuse
	}
	result := authoring.CommandResult{}
	if row.ResultRevisionID.Valid {
		result.Revision = authoring.RevisionToken{RevisionID: authoring.RevisionID(row.ResultRevisionID.String), Number: uint64(row.ResultRevisionNumber.Int64), ContentHash: row.ResultContentHash.String}
	}
	return result, true, nil
}

// LookupCreateOperation returns the immutable result retained for a create or
// fork retry. It is intentionally independent of dashboard identity so a
// generated dashboard ID can be recovered after a process restart. The stored
// fingerprint is returned without comparison; the service authorizes the
// retained target before deciding whether a caller reused the key.
func (r *Repository) LookupCreateOperation(ctx context.Context, operation authoring.CreateOperation) (authoring.CreateOperationResult, bool, error) {
	if !operation.Enabled() {
		return authoring.CreateOperationResult{}, false, nil
	}
	if err := validateCreateOperationKey(operation); err != nil {
		return authoring.CreateOperationResult{}, false, err
	}
	row, err := r.db.QueryContext(ctx, `SELECT request_fingerprint, dashboard_id, result_revision_id, result_revision_number, result_content_hash
FROM dashboard_authoring_create_operations
WHERE project_id = ? AND actor_id = ? AND operation_kind = ? AND idempotency_key = ?`,
		operation.ProjectID.String(), operation.ActorID, operation.Kind, operation.IdempotencyKey)
	if err != nil {
		return authoring.CreateOperationResult{}, false, err
	}
	defer row.Close()
	if !row.Next() {
		if err := row.Err(); err != nil {
			return authoring.CreateOperationResult{}, false, err
		}
		return authoring.CreateOperationResult{}, false, nil
	}
	var fingerprint, dashboardID, revisionID, contentHash string
	var revisionNumber int64
	if err := row.Scan(&fingerprint, &dashboardID, &revisionID, &revisionNumber, &contentHash); err != nil {
		return authoring.CreateOperationResult{}, false, err
	}
	if revisionNumber <= 0 {
		return authoring.CreateOperationResult{}, false, fmt.Errorf("%w: create operation result revision is invalid", authoring.ErrInvalidAuthoring)
	}
	result := authoring.CreateOperationResult{DashboardID: authoring.DashboardID(dashboardID), Revision: authoring.RevisionToken{RevisionID: authoring.RevisionID(revisionID), Number: uint64(revisionNumber), ContentHash: contentHash}, Fingerprint: fingerprint}
	if err := authoring.ValidateDashboardID(result.DashboardID); err != nil {
		return authoring.CreateOperationResult{}, false, err
	}
	if err := result.Revision.Validate(); err != nil {
		return authoring.CreateOperationResult{}, false, err
	}
	return result, true, nil
}

// GetPublishedCompilation retrieves the immutable compiler output selected by
// the current published pointer, preserving project/dashboard isolation.
func (r *Repository) GetPublishedCompilation(ctx context.Context, projectID graph.ResourceID, dashboardID authoring.DashboardID) (authoring.CompiledRevision, error) {
	if err := projectID.Validate(); err != nil {
		return authoring.CompiledRevision{}, fmt.Errorf("project id is required: %w", err)
	}
	if err := dashboardID.Validate(); err != nil {
		return authoring.CompiledRevision{}, err
	}
	q := dashboarddb.New(r.db)
	projectKey := projectID.String()
	published, err := q.GetAuthoringPublished(ctx, dashboarddb.GetAuthoringPublishedParams{ProjectID: projectKey, DashboardID: string(dashboardID)})
	if errors.Is(err, sql.ErrNoRows) {
		return authoring.CompiledRevision{}, authoring.ErrNotFound
	}
	if err != nil {
		return authoring.CompiledRevision{}, err
	}
	row, err := q.GetAuthoringPublishedCompilation(ctx, dashboarddb.GetAuthoringPublishedCompilationParams{ProjectID: projectKey, DashboardID: string(dashboardID), RevisionID: published.CompiledRevisionID, RevisionNumber: published.CompiledRevisionNumber, ContentHash: published.CompiledContentHash, DefinitionHash: published.CompiledDefinitionHash, SemanticModelID: published.CompiledSemanticModelID, SemanticIdentityJson: published.CompiledSemanticIdentityJson})
	if errors.Is(err, sql.ErrNoRows) {
		return authoring.CompiledRevision{}, authoring.ErrNotFound
	}
	if err != nil {
		return authoring.CompiledRevision{}, err
	}
	var definition dashboarddefinition.Definition
	if err := json.Unmarshal([]byte(row.DefinitionJson), &definition); err != nil {
		return authoring.CompiledRevision{}, fmt.Errorf("decode compiled dashboard definition: %w", err)
	}
	canonicalDefinition, err := json.Marshal(definition)
	if err != nil || string(canonicalDefinition) != row.DefinitionJson {
		return authoring.CompiledRevision{}, fmt.Errorf("%w: stored compiled definition is not canonical", authoring.ErrInvalidAuthoring)
	}
	compiledAt, err := parseTime(row.CompiledAt)
	if err != nil {
		return authoring.CompiledRevision{}, err
	}
	semanticIdentity, err := decodeServingIdentity(row.SemanticIdentityJson)
	if err != nil {
		return authoring.CompiledRevision{}, err
	}
	compiled := authoring.CompiledRevision{ProjectID: graph.ResourceID(row.ProjectID), DashboardID: authoring.DashboardID(row.DashboardID), AuthoredRevision: authoring.RevisionToken{RevisionID: authoring.RevisionID(row.RevisionID), Number: uint64(row.RevisionNumber), ContentHash: row.ContentHash}, Definition: definition, DefinitionHash: row.DefinitionHash, SemanticModelID: graph.ResourceID(row.SemanticModelID), SemanticIdentity: semanticIdentity, CompiledAt: compiledAt}
	if err := compiled.Validate(); err != nil {
		return authoring.CompiledRevision{}, fmt.Errorf("validate stored compiled dashboard: %w", err)
	}
	semanticJSON, err := encodeServingIdentity(compiled.SemanticIdentity)
	if err != nil {
		return authoring.CompiledRevision{}, err
	}
	if published.CompiledDefinitionHash != compiled.DefinitionHash || published.CompiledSemanticModelID != compiled.SemanticModelID.String() || published.CompiledSemanticIdentityJson != semanticJSON || published.RevisionID != string(compiled.AuthoredRevision.RevisionID) || published.RevisionNumber != int64(compiled.AuthoredRevision.Number) || published.ContentHash != compiled.AuthoredRevision.ContentHash {
		return authoring.CompiledRevision{}, fmt.Errorf("%w: published compilation pointer does not match immutable compiled artifact", authoring.ErrInvalidAuthoring)
	}
	return compiled, nil
}

func (r *Repository) AppendDraft(ctx context.Context, input authoring.AppendDraftInput) (authoring.Revision, error) {
	projectID, err := validateAppendInput(input)
	if err != nil {
		return authoring.Revision{}, err
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return authoring.Revision{}, err
	}
	defer tx.Rollback()
	q := dashboarddb.New(r.db).WithTx(tx)
	if replay, err := commandReplay(ctx, q, projectID, input.DashboardID, input.Evidence); err != nil {
		return authoring.Revision{}, err
	} else if replay != nil {
		revision, err := getRevision(ctx, q, projectID, input.DashboardID, replay.RevisionID)
		if err != nil {
			return authoring.Revision{}, err
		}
		return revision, nil
	}
	current, err := r.getLifecycle(ctx, q, projectID, input.DashboardID)
	if err != nil {
		return authoring.Revision{}, err
	}
	if current.Draft == nil {
		return authoring.Revision{}, conflict("dashboard has no draft pointer")
	}
	if current.Status == authoring.LifecycleStatusArchived {
		return authoring.Revision{}, conflict("archived dashboard cannot receive draft revisions")
	}
	if err := validateNextLifecycle(current, input.Next, input.Revision.Token()); err != nil {
		return authoring.Revision{}, err
	}
	if !sameToken(current.Draft.Revision, input.ExpectedDraftRevision) {
		return authoring.Revision{}, staleConflict()
	}
	if input.Revision.Number != current.Draft.Revision.Number+1 {
		return authoring.Revision{}, conflict("revision number is not the next draft revision")
	}
	documentJSON, provenanceJSON, err := encodeRevision(input.Revision)
	if err != nil {
		return authoring.Revision{}, err
	}
	if err := insertRevision(ctx, q, projectID, input.Revision, documentJSON, provenanceJSON); err != nil {
		return authoring.Revision{}, err
	}
	if _, err := q.UpdateAuthoringDashboard(ctx, dashboarddb.UpdateAuthoringDashboardParams{ProjectID: projectID, DashboardID: string(input.DashboardID), Slug: input.Next.Slug, Title: input.Next.Title, SemanticModel: input.Next.SemanticModel.String(), Visibility: string(input.Next.Visibility), Status: string(input.Next.Status)}); err != nil {
		return authoring.Revision{}, err
	}
	nextDraftProvenance, err := json.Marshal(input.Next.Draft.Provenance)
	if err != nil {
		return authoring.Revision{}, err
	}
	result, err := q.UpdateAuthoringDraft(ctx, dashboarddb.UpdateAuthoringDraftParams{RevisionID: string(input.Revision.ID),
		RevisionNumber: int64(input.Revision.Number), ContentHash: input.Revision.ContentHash, ProvenanceJson: string(nextDraftProvenance),
		ProjectID: projectID, DashboardID: string(input.DashboardID), ExpectedRevisionID: string(input.ExpectedDraftRevision.RevisionID),
		ExpectedRevisionNumber: int64(input.ExpectedDraftRevision.Number), ExpectedContentHash: input.ExpectedDraftRevision.ContentHash})
	if err != nil {
		return authoring.Revision{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return authoring.Revision{}, staleConflict()
	}
	if err := insertCommand(ctx, q, projectID, input.DashboardID, input.Evidence, input.Revision.Token()); err != nil {
		return authoring.Revision{}, err
	}
	if err := tx.Commit(); err != nil {
		return authoring.Revision{}, err
	}
	return input.Revision, nil
}

func (r *Repository) Publish(ctx context.Context, input authoring.PublishInput) (authoring.DashboardLifecycle, error) {
	projectID, target, compilation, provenance, publishedAt, err := validatePublishInput(input)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	defer tx.Rollback()
	q := dashboarddb.New(r.db).WithTx(tx)
	if replay, err := commandReplay(ctx, q, projectID, input.DashboardID, input.Evidence); err != nil {
		return authoring.DashboardLifecycle{}, err
	} else if replay != nil {
		return r.getLifecycle(ctx, q, projectID, input.DashboardID)
	}
	lifecycle, err := r.getLifecycle(ctx, q, projectID, input.DashboardID)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if lifecycle.Draft == nil || !sameToken(lifecycle.Draft.Revision, input.ExpectedDraftRevision) {
		return authoring.DashboardLifecycle{}, staleConflict()
	}
	if !sameToken(lifecycle.Draft.Revision, target) {
		return authoring.DashboardLifecycle{}, conflict("published revision must be the current draft revision")
	}
	if lifecycle.Status == authoring.LifecycleStatusArchived {
		return authoring.DashboardLifecycle{}, conflict("archived dashboard cannot be published")
	}
	if lifecycle.Status != authoring.LifecycleStatusDraft && lifecycle.Status != authoring.LifecycleStatusPublished {
		return authoring.DashboardLifecycle{}, conflict("dashboard is not publishable")
	}
	revision, err := getRevision(ctx, q, projectID, input.DashboardID, target.RevisionID)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if !sameToken(revision.Token(), target) {
		return authoring.DashboardLifecycle{}, conflict("published revision token does not match immutable revision")
	}
	if lifecycle.SemanticModel.String() != revision.Document.Spec.SemanticModel || lifecycle.Title != canonicalDocumentTitle(revision.Document) {
		return authoring.DashboardLifecycle{}, fmt.Errorf("%w: lifecycle metadata does not match published revision", authoring.ErrInvalidAuthoring)
	}
	if compilation.Definition.ID != revision.Document.Metadata.ID || compilation.Definition.Title != canonicalDocumentTitle(revision.Document) || compilation.Definition.SemanticModel != revision.Document.Spec.SemanticModel || compilation.Definition.SemanticModel != lifecycle.SemanticModel.String() {
		return authoring.DashboardLifecycle{}, fmt.Errorf("%w: compiled definition metadata does not match published revision", authoring.ErrInvalidAuthoring)
	}
	definitionJSON, err := json.Marshal(compilation.Definition)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if err := insertCompiledRevision(ctx, q, compilation, string(definitionJSON)); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	semanticIdentityJSON, err := encodeServingIdentity(compilation.SemanticIdentity)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	provenanceJSON, err := json.Marshal(provenance)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	_, err = q.UpdateAuthoringDashboard(ctx, dashboarddb.UpdateAuthoringDashboardParams{ProjectID: projectID,
		DashboardID: string(input.DashboardID), Slug: lifecycle.Slug, Title: lifecycle.Title, SemanticModel: lifecycle.SemanticModel.String(),
		Visibility: string(lifecycle.Visibility), Status: string(authoring.LifecycleStatusPublished)})
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	err = q.UpsertAuthoringPublished(ctx, dashboarddb.UpsertAuthoringPublishedParams{ProjectID: projectID,
		DashboardID: string(input.DashboardID), RevisionID: string(target.RevisionID), RevisionNumber: int64(target.Number),
		ContentHash: target.ContentHash, CompiledRevisionID: string(compilation.AuthoredRevision.RevisionID), CompiledRevisionNumber: int64(compilation.AuthoredRevision.Number), CompiledContentHash: compilation.AuthoredRevision.ContentHash,
		CompiledDefinitionHash: compilation.DefinitionHash, CompiledSemanticModelID: compilation.SemanticModelID.String(), CompiledSemanticIdentityJson: semanticIdentityJSON,
		ProvenanceJson: string(provenanceJSON), PublishedAt: formatTime(publishedAt)})
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if err := insertCommand(ctx, q, projectID, input.DashboardID, input.Evidence, target); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if err := tx.Commit(); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	return r.Get(ctx, graph.ResourceID(projectID), input.DashboardID)
}

func (r *Repository) Archive(ctx context.Context, input authoring.ArchiveInput) (authoring.DashboardLifecycle, error) {
	projectID, err := validateArchiveInput(input)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	defer tx.Rollback()
	q := dashboarddb.New(r.db).WithTx(tx)
	if replay, err := commandReplay(ctx, q, projectID, input.DashboardID, input.Evidence); err != nil {
		return authoring.DashboardLifecycle{}, err
	} else if replay != nil {
		return r.getLifecycle(ctx, q, projectID, input.DashboardID)
	}
	lifecycle, err := r.getLifecycle(ctx, q, projectID, input.DashboardID)
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	expected := input.ExpectedCurrentRevision
	if lifecycle.Draft != nil {
		if !sameToken(lifecycle.Draft.Revision, expected) {
			return authoring.DashboardLifecycle{}, staleConflict()
		}
	} else if lifecycle.Published == nil || !sameToken(lifecycle.Published.Revision, expected) {
		return authoring.DashboardLifecycle{}, staleConflict()
	}
	if err := authoring.ValidateTransition(lifecycle.Status, authoring.LifecycleStatusArchived); err != nil {
		return authoring.DashboardLifecycle{}, conflict(err.Error())
	}
	result := expected
	_, err = q.ArchiveAuthoringDashboard(ctx, dashboarddb.ArchiveAuthoringDashboardParams{ProjectID: projectID, DashboardID: string(input.DashboardID)})
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if err := insertCommand(ctx, q, projectID, input.DashboardID, input.Evidence, result); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	if err := tx.Commit(); err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	return r.Get(ctx, graph.ResourceID(projectID), input.DashboardID)
}

type commandResult struct {
	RevisionID authoring.RevisionID
}

func commandReplay(ctx context.Context, q *dashboarddb.Queries, projectID string, dashboardID authoring.DashboardID, evidence authoring.CommandEvidence) (*commandResult, error) {
	row, err := q.GetAuthoringCommand(ctx, dashboarddb.GetAuthoringCommandParams{ProjectID: projectID, DashboardID: string(dashboardID), CommandID: string(evidence.ID)})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if row.RequestFingerprint != evidence.Fingerprint {
		return nil, authoring.ErrCommandReuse
	}
	if !row.ResultRevisionID.Valid || row.ResultRevisionID.String == "" {
		return &commandResult{}, nil
	}
	return &commandResult{RevisionID: authoring.RevisionID(row.ResultRevisionID.String)}, nil
}

func insertCompiledRevision(ctx context.Context, q *dashboarddb.Queries, compiled authoring.CompiledRevision, definitionJSON string) error {
	if err := compiled.Validate(); err != nil {
		return err
	}
	semanticIdentityJSON, err := encodeServingIdentity(compiled.SemanticIdentity)
	if err != nil {
		return err
	}
	row, err := q.GetAuthoringPublishedCompilation(ctx, dashboarddb.GetAuthoringPublishedCompilationParams{ProjectID: compiled.ProjectID.String(), DashboardID: string(compiled.DashboardID), RevisionID: string(compiled.AuthoredRevision.RevisionID), RevisionNumber: int64(compiled.AuthoredRevision.Number), ContentHash: compiled.AuthoredRevision.ContentHash, DefinitionHash: compiled.DefinitionHash, SemanticModelID: compiled.SemanticModelID.String(), SemanticIdentityJson: semanticIdentityJSON})
	if err == nil {
		if row.DefinitionJson != definitionJSON || row.DefinitionHash != compiled.DefinitionHash || row.SemanticModelID != compiled.SemanticModelID.String() || row.SemanticIdentityJson != semanticIdentityJSON {
			return conflict("compiled revision identity is immutable")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	err = q.InsertAuthoringCompiledRevision(ctx, dashboarddb.InsertAuthoringCompiledRevisionParams{ProjectID: compiled.ProjectID.String(), DashboardID: string(compiled.DashboardID), RevisionID: string(compiled.AuthoredRevision.RevisionID), RevisionNumber: int64(compiled.AuthoredRevision.Number), ContentHash: compiled.AuthoredRevision.ContentHash, DefinitionJson: definitionJSON, DefinitionHash: compiled.DefinitionHash, SemanticModelID: compiled.SemanticModelID.String(), SemanticIdentityJson: semanticIdentityJSON, CompiledAt: formatTime(compiled.CompiledAt)})
	if isConstraint(err) {
		return conflict("compiled revision identity is immutable")
	}
	return err
}

func insertCommand(ctx context.Context, q *dashboarddb.Queries, projectID string, dashboardID authoring.DashboardID, evidence authoring.CommandEvidence, token authoring.RevisionToken) error {
	_, err := q.GetAuthoringCommand(ctx, dashboarddb.GetAuthoringCommandParams{ProjectID: projectID, DashboardID: string(dashboardID), CommandID: string(evidence.ID)})
	if err == nil {
		return authoring.ErrCommandReuse
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	provenanceJSON, err := json.Marshal(evidence.Provenance)
	if err != nil {
		return err
	}
	err = q.InsertAuthoringCommand(ctx, dashboarddb.InsertAuthoringCommandParams{ProjectID: projectID, DashboardID: string(dashboardID), CommandID: string(evidence.ID), RequestFingerprint: evidence.Fingerprint, Action: string(evidence.Action), ProvenanceJson: string(provenanceJSON), OccurredAt: formatTime(evidence.OccurredAt),
		ResultRevisionID: sql.NullString{String: string(token.RevisionID), Valid: true}, ResultRevisionNumber: sql.NullInt64{Int64: int64(token.Number), Valid: true}, ResultContentHash: sql.NullString{String: token.ContentHash, Valid: true}})
	if isConstraint(err) {
		return authoring.ErrCommandReuse
	}
	return err
}

func validateCreateOperationKey(operation authoring.CreateOperation) error {
	return operation.Validate()
}

func validateCreateOperation(operation authoring.CreateOperation, projectID string, dashboardID authoring.DashboardID, token authoring.RevisionToken) error {
	if err := validateCreateOperationKey(operation); err != nil {
		return err
	}
	if operation.ProjectID.String() != projectID {
		return fmt.Errorf("%w: create operation project does not match create project", authoring.ErrInvalidAuthoring)
	}
	if err := dashboardID.Validate(); err != nil {
		return err
	}
	if err := token.Validate(); err != nil {
		return err
	}
	return nil
}

func lookupCreateOperation(ctx context.Context, tx *sql.Tx, operation authoring.CreateOperation) (authoring.CreateOperationResult, bool, error) {
	var fingerprint, dashboardID, revisionID, contentHash string
	var revisionNumber int64
	err := tx.QueryRowContext(ctx, `SELECT request_fingerprint, dashboard_id, result_revision_id, result_revision_number, result_content_hash
FROM dashboard_authoring_create_operations
WHERE project_id = ? AND actor_id = ? AND operation_kind = ? AND idempotency_key = ?`,
		operation.ProjectID.String(), operation.ActorID, operation.Kind, operation.IdempotencyKey).
		Scan(&fingerprint, &dashboardID, &revisionID, &revisionNumber, &contentHash)
	if errors.Is(err, sql.ErrNoRows) {
		return authoring.CreateOperationResult{}, false, nil
	}
	if err != nil {
		return authoring.CreateOperationResult{}, false, err
	}
	if fingerprint != operation.Fingerprint {
		return authoring.CreateOperationResult{}, false, authoring.ErrCommandReuse
	}
	result := authoring.CreateOperationResult{DashboardID: authoring.DashboardID(dashboardID), Revision: authoring.RevisionToken{RevisionID: authoring.RevisionID(revisionID), Number: uint64(revisionNumber), ContentHash: contentHash}}
	if err := authoring.ValidateDashboardID(result.DashboardID); err != nil {
		return authoring.CreateOperationResult{}, false, err
	}
	if err := result.Revision.Validate(); err != nil {
		return authoring.CreateOperationResult{}, false, err
	}
	return result, true, nil
}

func insertCreateOperation(ctx context.Context, tx *sql.Tx, operation authoring.CreateOperation, dashboardID authoring.DashboardID, token authoring.RevisionToken) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO dashboard_authoring_create_operations
 (project_id, actor_id, operation_kind, idempotency_key, conversation_id, tool_call_id, request_fingerprint, dashboard_id, result_revision_id, result_revision_number, result_content_hash)
 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		operation.ProjectID.String(), operation.ActorID, operation.Kind, operation.IdempotencyKey, operation.ConversationID, operation.ToolCallID, operation.Fingerprint,
		dashboardID.String(), token.RevisionID.String(), int64(token.Number), token.ContentHash)
	return err
}

func insertRevision(ctx context.Context, q *dashboarddb.Queries, projectID string, revision authoring.Revision, documentJSON, provenanceJSON string) error {
	err := q.InsertAuthoringRevision(ctx, dashboarddb.InsertAuthoringRevisionParams{ProjectID: projectID, DashboardID: string(revision.DashboardID), RevisionID: string(revision.ID), RevisionNumber: int64(revision.Number),
		DocumentJson: documentJSON, ContentHash: revision.ContentHash, ProvenanceJson: provenanceJSON, CreatedAt: formatTime(revision.CreatedAt)})
	if isConstraint(err) {
		return conflict("revision identity or number already exists")
	}
	return err
}

func insertDraft(ctx context.Context, q *dashboarddb.Queries, projectID string, dashboardID authoring.DashboardID, draft authoring.Draft) error {
	provenanceJSON, err := json.Marshal(draft.Provenance)
	if err != nil {
		return err
	}
	err = q.InsertAuthoringDraft(ctx, dashboarddb.InsertAuthoringDraftParams{ProjectID: projectID, DashboardID: string(dashboardID), DraftID: string(draft.ID), RevisionID: string(draft.Revision.RevisionID), RevisionNumber: int64(draft.Revision.Number),
		ContentHash: draft.Revision.ContentHash, ProvenanceJson: string(provenanceJSON)})
	if isConstraint(err) {
		return conflict("draft pointer is invalid")
	}
	return err
}

func (r *Repository) begin(ctx context.Context) (*sql.Tx, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("dashboard authoring persistence is unavailable")
	}
	return r.db.BeginTx(ctx, nil)
}

func getRevision(ctx context.Context, q *dashboarddb.Queries, projectID string, dashboardID authoring.DashboardID, revisionID authoring.RevisionID) (authoring.Revision, error) {
	row, err := q.GetAuthoringRevision(ctx, dashboarddb.GetAuthoringRevisionParams{ProjectID: projectID, DashboardID: string(dashboardID), RevisionID: string(revisionID)})
	if errors.Is(err, sql.ErrNoRows) {
		return authoring.Revision{}, authoring.ErrNotFound
	}
	if err != nil {
		return authoring.Revision{}, err
	}
	var document document.DashboardDocument
	if err := json.Unmarshal([]byte(row.DocumentJson), &document); err != nil {
		return authoring.Revision{}, fmt.Errorf("decode dashboard revision document: %w", err)
	}
	var provenance authoring.Provenance
	if err := json.Unmarshal([]byte(row.ProvenanceJson), &provenance); err != nil {
		return authoring.Revision{}, fmt.Errorf("decode dashboard revision provenance: %w", err)
	}
	createdAt, err := parseTime(row.CreatedAt)
	if err != nil {
		return authoring.Revision{}, err
	}
	revision := authoring.Revision{ID: authoring.RevisionID(row.RevisionID), DashboardID: authoring.DashboardID(row.DashboardID), Number: uint64(row.RevisionNumber), Document: document, ContentHash: row.ContentHash, Provenance: provenance, CreatedAt: createdAt}
	if err := revision.Validate(); err != nil {
		return authoring.Revision{}, fmt.Errorf("validate stored dashboard revision: %w", err)
	}
	canonical, err := json.Marshal(document)
	if err != nil || string(canonical) != row.DocumentJson {
		return authoring.Revision{}, fmt.Errorf("%w: stored dashboard document is not canonical", authoring.ErrInvalidAuthoring)
	}
	return revision, nil
}

func (r *Repository) getLifecycle(ctx context.Context, q *dashboarddb.Queries, projectID string, dashboardID authoring.DashboardID) (authoring.DashboardLifecycle, error) {
	var lifecycle authoring.DashboardLifecycle
	identity, err := q.GetAuthoringDashboard(ctx, dashboarddb.GetAuthoringDashboardParams{ProjectID: projectID, DashboardID: string(dashboardID)})
	if errors.Is(err, sql.ErrNoRows) {
		return authoring.DashboardLifecycle{}, authoring.ErrNotFound
	}
	if err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	lifecycle.ID = authoring.DashboardID(identity.DashboardID)
	lifecycle.ProjectID = graph.ResourceID(identity.ProjectID)
	lifecycle.OwnerPrincipalID = identity.OwnerPrincipalID
	lifecycle.Slug = identity.Slug
	lifecycle.Title = identity.Title
	lifecycle.SemanticModel = graph.ResourceID(identity.SemanticModel)
	lifecycle.Visibility = authoring.Visibility(identity.Visibility)
	lifecycle.Status = authoring.LifecycleStatus(identity.Status)
	draft, err := q.GetAuthoringDraft(ctx, dashboarddb.GetAuthoringDraftParams{ProjectID: projectID, DashboardID: string(dashboardID)})
	if err == nil {
		var provenance authoring.Provenance
		if err := json.Unmarshal([]byte(draft.ProvenanceJson), &provenance); err != nil {
			return authoring.DashboardLifecycle{}, err
		}
		lifecycle.Draft = &authoring.Draft{ID: authoring.DraftID(draft.DraftID), DashboardID: dashboardID, Revision: authoring.RevisionToken{RevisionID: authoring.RevisionID(draft.RevisionID), Number: uint64(draft.RevisionNumber), ContentHash: draft.ContentHash}, Provenance: provenance}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return authoring.DashboardLifecycle{}, err
	}
	published, err := q.GetAuthoringPublished(ctx, dashboarddb.GetAuthoringPublishedParams{ProjectID: projectID, DashboardID: string(dashboardID)})
	if err == nil {
		var provenance authoring.Provenance
		if err := json.Unmarshal([]byte(published.ProvenanceJson), &provenance); err != nil {
			return authoring.DashboardLifecycle{}, err
		}
		at, err := parseTime(published.PublishedAt)
		if err != nil {
			return authoring.DashboardLifecycle{}, err
		}
		semanticIdentity, err := decodeServingIdentity(published.CompiledSemanticIdentityJson)
		if err != nil {
			return authoring.DashboardLifecycle{}, err
		}
		lifecycle.Published = &authoring.Published{Revision: authoring.RevisionToken{RevisionID: authoring.RevisionID(published.RevisionID), Number: uint64(published.RevisionNumber), ContentHash: published.ContentHash}, Compilation: authoring.CompiledRevisionToken{AuthoredRevision: authoring.RevisionToken{RevisionID: authoring.RevisionID(published.CompiledRevisionID), Number: uint64(published.CompiledRevisionNumber), ContentHash: published.CompiledContentHash}, DefinitionHash: published.CompiledDefinitionHash, SemanticModelID: graph.ResourceID(published.CompiledSemanticModelID), SemanticIdentity: semanticIdentity}, PublishedAt: at, Provenance: provenance}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return authoring.DashboardLifecycle{}, err
	}
	if err := lifecycle.Validate(); err != nil {
		return authoring.DashboardLifecycle{}, fmt.Errorf("validate stored dashboard lifecycle: %w", err)
	}
	return lifecycle, nil
}

func (r *Repository) latestRevalidationFailure(ctx context.Context, projectID string, dashboardID authoring.DashboardID) (authoring.RevalidationFailure, bool, error) {
	var status, identityJSON, dependencyJSON, attemptedAt string
	var code, message sql.NullString
	var generationID string
	err := r.db.QueryRowContext(ctx, `SELECT status, generation_id, generation_identity_json, dependency_ids_json, error_code, error_message, attempted_at FROM dashboard_authoring_revalidation_attempts WHERE project_id = ? AND dashboard_id = ? ORDER BY rowid DESC LIMIT 1`, projectID, dashboardID.String()).Scan(&status, &generationID, &identityJSON, &dependencyJSON, &code, &message, &attemptedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return authoring.RevalidationFailure{}, false, nil
	}
	if err != nil {
		return authoring.RevalidationFailure{}, false, err
	}
	if status != "failed" {
		return authoring.RevalidationFailure{}, false, nil
	}
	identity, err := decodeServingIdentity(identityJSON)
	if err != nil {
		return authoring.RevalidationFailure{}, false, err
	}
	var dependencyIDs []graph.ResourceID
	if err := json.Unmarshal([]byte(dependencyJSON), &dependencyIDs); err != nil {
		return authoring.RevalidationFailure{}, false, err
	}
	at, err := parseTime(attemptedAt)
	if err != nil {
		return authoring.RevalidationFailure{}, false, err
	}
	failure := authoring.RevalidationFailure{Identity: identity, DependencyIDs: dependencyIDs, Code: code.String, Message: message.String, FailedAt: at}
	if err := failure.Validate(); err != nil {
		return authoring.RevalidationFailure{}, false, err
	}
	_ = generationID // generation ID is carried by identity and validated above.
	return failure, true, nil
}

func encodeRevision(revision authoring.Revision) (string, string, error) {
	document, err := json.Marshal(revision.Document)
	if err != nil {
		return "", "", err
	}
	provenance, err := json.Marshal(revision.Provenance)
	if err != nil {
		return "", "", err
	}
	return string(document), string(provenance), nil
}

func encodeServingIdentity(identity graph.ServingIdentity) (string, error) {
	if err := identity.Validate(); err != nil {
		return "", fmt.Errorf("%w: serving identity: %v", authoring.ErrInvalidAuthoring, err)
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("encode serving identity: %w", err)
	}
	return string(encoded), nil
}

func decodeServingIdentity(value string) (graph.ServingIdentity, error) {
	var identity graph.ServingIdentity
	if err := json.Unmarshal([]byte(value), &identity); err != nil {
		return graph.ServingIdentity{}, fmt.Errorf("decode serving identity: %w", err)
	}
	canonical, err := encodeServingIdentity(identity)
	if err != nil {
		return graph.ServingIdentity{}, err
	}
	if canonical != value {
		return graph.ServingIdentity{}, fmt.Errorf("%w: serving identity is not canonical", authoring.ErrInvalidAuthoring)
	}
	return identity, nil
}

func lifecycleReferencesRevision(lifecycle authoring.DashboardLifecycle, token authoring.RevisionToken) bool {
	return (lifecycle.Draft != nil && sameToken(lifecycle.Draft.Revision, token)) ||
		(lifecycle.Published != nil && sameToken(lifecycle.Published.Revision, token))
}

func canonicalDocumentTitle(value document.DashboardDocument) string {
	if value.Metadata.DisplayName != nil {
		return *value.Metadata.DisplayName
	}
	return value.Metadata.Name
}

func validateNextLifecycle(current, next authoring.DashboardLifecycle, revision authoring.RevisionToken) error {
	if err := next.Validate(); err != nil {
		return err
	}
	if next.ProjectID != current.ProjectID || next.OwnerPrincipalID != current.OwnerPrincipalID || next.ID != current.ID || next.Status != current.Status || next.Draft == nil || current.Draft == nil || next.Draft.ID != current.Draft.ID || !sameToken(next.Draft.Revision, revision) {
		return fmt.Errorf("%w: append lifecycle metadata must retain identity/status and point at the appended revision", authoring.ErrConflict)
	}
	if (current.Published == nil) != (next.Published == nil) {
		return fmt.Errorf("%w: append lifecycle metadata cannot change published pointer", authoring.ErrConflict)
	}
	if current.Published != nil && !sameToken(current.Published.Revision, next.Published.Revision) {
		return fmt.Errorf("%w: append lifecycle metadata cannot change published pointer", authoring.ErrConflict)
	}
	return nil
}

func sameToken(a, b authoring.RevisionToken) bool {
	return a.RevisionID == b.RevisionID && a.Number == b.Number && a.ContentHash == b.ContentHash
}

func validateAppendInput(input authoring.AppendDraftInput) (string, error) {
	if err := input.ProjectID.Validate(); err != nil {
		return "", fmt.Errorf("project id is required: %w", err)
	}
	projectID := input.ProjectID.String()
	if err := authoring.ValidateDashboardID(input.DashboardID); err != nil {
		return "", err
	}
	if err := input.Evidence.Validate(); err != nil {
		return "", err
	}
	if input.Evidence.Action != authoring.AuthorizationActionEdit {
		return "", fmt.Errorf("%w: append requires edit command evidence", authoring.ErrInvalidAuthoring)
	}
	if err := input.ExpectedDraftRevision.ValidateComplete(); err != nil {
		return "", err
	}
	if err := input.Revision.Validate(); err != nil {
		return "", err
	}
	if input.Revision.DashboardID != input.DashboardID {
		return "", fmt.Errorf("%w: revision belongs to dashboard %q", authoring.ErrInvalidAuthoring, input.Revision.DashboardID)
	}
	if input.Next.ProjectID.String() != projectID {
		return "", fmt.Errorf("%w: next lifecycle project does not match append project", authoring.ErrInvalidAuthoring)
	}
	if err := input.Next.Validate(); err != nil {
		return "", err
	}
	if input.Next.ID != input.DashboardID || input.Next.Draft == nil || !sameToken(input.Next.Draft.Revision, input.Revision.Token()) {
		return "", fmt.Errorf("%w: append requires a next lifecycle pointing at the appended revision", authoring.ErrInvalidAuthoring)
	}
	if input.Next.SemanticModel.String() != input.Revision.Document.Spec.SemanticModel {
		return "", fmt.Errorf("%w: lifecycle semantic model does not match appended revision", authoring.ErrInvalidAuthoring)
	}
	if input.Next.Title != canonicalDocumentTitle(input.Revision.Document) {
		return "", fmt.Errorf("%w: lifecycle title does not match appended revision", authoring.ErrInvalidAuthoring)
	}
	return projectID, nil
}

func validatePublishInput(input authoring.PublishInput) (string, authoring.RevisionToken, authoring.CompiledRevision, authoring.Provenance, time.Time, error) {
	if err := input.ProjectID.Validate(); err != nil {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, fmt.Errorf("project id is required: %w", err)
	}
	projectID := input.ProjectID.String()
	if err := authoring.ValidateDashboardID(input.DashboardID); err != nil {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, err
	}
	if err := input.Evidence.Validate(); err != nil {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, err
	}
	if input.Evidence.Action != authoring.AuthorizationActionPublish {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, fmt.Errorf("%w: publish requires publish command evidence", authoring.ErrInvalidAuthoring)
	}
	if err := input.ExpectedDraftRevision.ValidateComplete(); err != nil {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, err
	}
	target := input.Published.Revision
	if err := target.ValidateComplete(); err != nil {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, err
	}
	if err := input.Published.Validate(); err != nil {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, err
	}
	compilation := input.Compilation
	if err := compilation.Validate(); err != nil {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, err
	}
	if compilation.DashboardID != input.DashboardID || compilation.ProjectID.String() != projectID {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, fmt.Errorf("%w: compiled revision scope does not match publish scope", authoring.ErrInvalidAuthoring)
	}
	if compilation.Token() != input.Published.Compilation || compilation.AuthoredRevision != target {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, fmt.Errorf("%w: published compilation token does not match compiled artifact", authoring.ErrInvalidAuthoring)
	}
	provenance := input.Published.Provenance
	if err := provenance.Validate(); err != nil {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, err
	}
	if input.Published.PublishedAt.IsZero() {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, fmt.Errorf("%w: published timestamp is required", authoring.ErrInvalidAuthoring)
	}
	if input.Published.PublishedAt.Location() != time.UTC {
		return "", authoring.RevisionToken{}, authoring.CompiledRevision{}, authoring.Provenance{}, time.Time{}, fmt.Errorf("%w: published timestamp must be UTC", authoring.ErrInvalidAuthoring)
	}
	return projectID, target, compilation, provenance, input.Published.PublishedAt, nil
}

func validateArchiveInput(input authoring.ArchiveInput) (string, error) {
	if err := input.ProjectID.Validate(); err != nil {
		return "", fmt.Errorf("project id is required: %w", err)
	}
	projectID := input.ProjectID.String()
	if err := authoring.ValidateDashboardID(input.DashboardID); err != nil {
		return "", err
	}
	if err := input.Evidence.Validate(); err != nil {
		return "", err
	}
	if input.Evidence.Action != authoring.AuthorizationActionArchive {
		return "", fmt.Errorf("%w: archive requires archive command evidence", authoring.ErrInvalidAuthoring)
	}
	if err := input.ExpectedCurrentRevision.ValidateComplete(); err != nil {
		return "", err
	}
	return projectID, nil
}

func staleConflict() error {
	return fmt.Errorf("%w: %w", authoring.ErrConflict, authoring.ErrStaleRevision)
}

func conflict(message string) error { return fmt.Errorf("%w: %s", authoring.ErrConflict, message) }

func isConstraint(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "constraint") || strings.Contains(strings.ToLower(err.Error()), "unique")
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: invalid stored timestamp %q", authoring.ErrInvalidAuthoring, value)
	}
	return parsed.UTC(), nil
}
