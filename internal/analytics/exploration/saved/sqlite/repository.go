// Package sqlite contains the durable SQLite adapter for saved explorations.
//
// The adapter deliberately keeps lifecycle reads and exact revision reads on
// separate SQL paths.  A lifecycle is safe to use for authorization and list
// filtering even when an old authored payload can no longer be decoded.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/exploration/saved"
	saveddb "github.com/flidai/leapview/internal/analytics/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// Repository persists stable saved-exploration identities, immutable authored
// revisions, and actor-scoped mutation results in one SQLite database.
type Repository struct {
	db *sql.DB
	q  *saveddb.Queries

	audit access.AuditIntentRecorder
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db, q: saveddb.New(db)}
}

// NewRepositoryWithAudit is the mutation-capable constructor.  A repository
// made with NewRepository remains useful for metadata and exact revision
// reads, but fresh mutations fail closed because they cannot durably hand off
// an Access audit intent.
func NewRepositoryWithAudit(db *sql.DB, audit access.AuditIntentRecorder) *Repository {
	return &Repository{db: db, q: saveddb.New(db), audit: audit}
}

var _ saved.Repository = (*Repository)(nil)

func (r *Repository) GetLifecycle(ctx context.Context, input saved.ReadInput) (saved.Lifecycle, error) {
	if err := input.Validate(); err != nil {
		return saved.Lifecycle{}, err
	}
	q, err := r.readQueries()
	if err != nil {
		return saved.Lifecycle{}, err
	}
	row, err := q.GetSavedExplorationLifecycle(ctx, saveddb.GetSavedExplorationLifecycleParams{
		ProjectID: input.ProjectID.String(), ExplorationID: input.ID.String(),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return saved.Lifecycle{}, saved.ErrNotFound
	}
	if err != nil {
		return saved.Lifecycle{}, mapStorageError(err)
	}
	lifecycle, err := lifecycleFromProjection(row.ProjectID, row.ExplorationID, row.OwnerPrincipalID,
		row.Title, row.Slug, row.Visibility, row.Status, row.SemanticModelID,
		row.CreatedAt, row.UpdatedAt, row.ArchivedAt,
		row.RevisionID, row.RevisionNumber, row.ContentHash, row.CreatedBy,
		row.RevisionCreatedAt, row.ServingProjectID, row.ServingEnvironment, row.ServingGenerationID)
	if err != nil {
		return saved.Lifecycle{}, err
	}
	return lifecycle, nil
}

func (r *Repository) GetRevision(ctx context.Context, input saved.RevisionReadInput) (saved.Revision, error) {
	if err := input.Validate(); err != nil {
		return saved.Revision{}, err
	}
	q, err := r.readQueries()
	if err != nil {
		return saved.Revision{}, err
	}
	revisionNumber, err := sqliteNumber(input.Revision.Number)
	if err != nil {
		return saved.Revision{}, err
	}
	row, err := q.GetSavedExplorationRevision(ctx, saveddb.GetSavedExplorationRevisionParams{
		ProjectID: input.ProjectID.String(), ExplorationID: input.ID.String(),
		RevisionID: input.Revision.RevisionID.String(), RevisionNumber: revisionNumber,
		ContentHash: input.Revision.ContentHash,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return saved.Revision{}, saved.ErrNotFound
	}
	if err != nil {
		return saved.Revision{}, mapStorageError(err)
	}
	revision, err := revisionFromRow(row)
	if err != nil {
		return saved.Revision{}, err
	}
	if revision.Token() != input.Revision {
		return saved.Revision{}, saved.ErrStaleRevision
	}
	return revision, nil
}

// ListPage reads one bounded keyset batch. The query asks SQLite for one
// extra metadata row so NextCursor can be set without loading every project
// row into memory. Payloads remain excluded from this projection.
func (r *Repository) ListPage(ctx context.Context, input saved.ListInput) (saved.ListPage, error) {
	if err := input.Validate(); err != nil {
		return saved.ListPage{}, err
	}
	q, err := r.readQueries()
	if err != nil {
		return saved.ListPage{}, err
	}
	limit := input.Limit
	if limit == 0 {
		limit = saved.MaxListLimit
	}
	rows, err := q.ListSavedExplorationLifecycles(ctx, saveddb.ListSavedExplorationLifecyclesParams{
		ProjectID: input.ProjectID.String(), IncludeArchived: boolInt(input.IncludeArchived), Cursor: input.Cursor, Limit: int64(limit + 1),
	})
	if err != nil {
		return saved.ListPage{}, mapStorageError(err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	out := make([]saved.Lifecycle, 0, len(rows))
	for _, row := range rows {
		lifecycle, err := lifecycleFromProjection(row.ProjectID, row.ExplorationID, row.OwnerPrincipalID,
			row.Title, row.Slug, row.Visibility, row.Status, row.SemanticModelID,
			row.CreatedAt, row.UpdatedAt, row.ArchivedAt,
			row.RevisionID, row.RevisionNumber, row.ContentHash, row.CreatedBy,
			row.RevisionCreatedAt, row.ServingProjectID, row.ServingEnvironment, row.ServingGenerationID)
		if err != nil {
			return saved.ListPage{}, err
		}
		out = append(out, lifecycle)
	}
	page := saved.ListPage{Items: out}
	if hasMore && len(out) > 0 {
		page.NextCursor = out[len(out)-1].ID.String()
	}
	return page, nil
}

// List preserves the metadata-only legacy repository method for browser and
// internal callers while implementing it as bounded SQL keyset batches.
func (r *Repository) List(ctx context.Context, input saved.ListInput) ([]saved.Lifecycle, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	all := make([]saved.Lifecycle, 0)
	cursor := input.Cursor
	for {
		page, err := r.ListPage(ctx, saved.ListInput{ProjectID: input.ProjectID, IncludeArchived: input.IncludeArchived, Cursor: cursor, Limit: saved.MaxListLimit})
		if err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if page.NextCursor == "" {
			return all, nil
		}
		cursor = page.NextCursor
	}
}

func (r *Repository) LookupMutation(ctx context.Context, input saved.MutationLookupInput) (saved.MutationReplayMetadata, bool, error) {
	if err := input.Validate(); err != nil {
		return saved.MutationReplayMetadata{}, false, err
	}
	q, err := r.readQueries()
	if err != nil {
		return saved.MutationReplayMetadata{}, false, err
	}
	row, err := q.GetSavedExplorationOperation(ctx, saveddb.GetSavedExplorationOperationParams{
		ProjectID: input.ProjectID.String(), ActorID: input.ActorID,
		OperationKind: string(input.Action), IdempotencyKey: input.IdempotencyKey,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return saved.MutationReplayMetadata{}, false, nil
	}
	if err != nil {
		return saved.MutationReplayMetadata{}, false, mapStorageError(err)
	}
	if row.RequestFingerprint != input.Fingerprint {
		return saved.MutationReplayMetadata{}, false, commandReuseError()
	}
	metadata, err := replayMetadata(row)
	if err != nil {
		return saved.MutationReplayMetadata{}, false, err
	}
	return metadata, true, nil
}

func (r *Repository) Create(ctx context.Context, input saved.CreateInput) (saved.MutationResult, error) {
	if err := input.Validate(); err != nil {
		return saved.MutationResult{}, err
	}
	tx, q, replay, found, err := r.beginMutation(ctx, input.ProjectID, input.Evidence)
	if err != nil {
		return saved.MutationResult{}, err
	}
	defer tx.Rollback()
	if found {
		return r.replayResult(ctx, q, replay)
	}

	revision := input.Revision.Clone()
	revisionNumber, err := sqliteNumber(revision.Metadata.Number)
	if err != nil {
		return saved.MutationResult{}, err
	}
	if err := q.InsertSavedExploration(ctx, saveddb.InsertSavedExplorationParams{
		ProjectID: input.ProjectID.String(), ExplorationID: input.ID.String(),
		OwnerPrincipalID: input.OwnerPrincipalID, Title: input.Title, Slug: input.Slug,
		Visibility: string(input.Visibility), SemanticModelID: input.SemanticModelID.String(),
		CreatedAt: formatTime(input.CreatedAt), UpdatedAt: formatTime(input.CreatedAt),
		CurrentRevisionID: revision.Metadata.ID.String(), CurrentRevisionNumber: revisionNumber,
		CurrentContentHash: revision.Metadata.ContentHash,
	}); err != nil {
		return saved.MutationResult{}, mapCreateConstraint(ctx, tx, input.ProjectID.String(), input.ID.String(), input.Slug, err)
	}
	if err := insertRevision(ctx, q, input.ProjectID.String(), input.ID.String(), revision); err != nil {
		return saved.MutationResult{}, err
	}
	lifecycle, err := lifecycleByID(ctx, q, input.ProjectID.String(), input.ID.String())
	if err != nil {
		return saved.MutationResult{}, err
	}
	result := saved.MutationResult{Lifecycle: lifecycle, Revision: revisionPtr(revision), AppliedRevision: revision.Token(), Evidence: input.Evidence}
	if err := result.Validate(); err != nil {
		return saved.MutationResult{}, fmt.Errorf("validate create result: %w", err)
	}
	if err := insertOperation(ctx, q, result, input.Evidence); err != nil {
		return r.operationInsertFailure(ctx, q, input.ProjectID.String(), input.Evidence, err)
	}
	if err := r.recordAuditIntent(ctx, tx, lifecycle, revision.Metadata, input.Evidence); err != nil {
		return saved.MutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return saved.MutationResult{}, mapStorageError(err)
	}
	return result, nil
}

func (r *Repository) UpdateVersion(ctx context.Context, input saved.UpdateVersionInput) (saved.MutationResult, error) {
	if err := input.Validate(); err != nil {
		return saved.MutationResult{}, err
	}
	tx, q, replay, found, err := r.beginMutation(ctx, input.ProjectID, input.Evidence)
	if err != nil {
		return saved.MutationResult{}, err
	}
	defer tx.Rollback()
	if found {
		return r.replayResult(ctx, q, replay, input.ExpectedRevision)
	}

	current, err := lifecycleByID(ctx, q, input.ProjectID.String(), input.ID.String())
	if err != nil {
		return saved.MutationResult{}, err
	}
	if current.Status == saved.StatusArchived {
		return saved.MutationResult{}, saved.ErrArchived
	}
	if current.CurrentRevision.Token() != input.ExpectedRevision {
		return saved.MutationResult{}, saved.ErrStaleRevision
	}
	if input.Revision.Metadata.Number != current.CurrentRevision.Number+1 || input.Revision.Metadata.ID == current.CurrentRevision.ID {
		return saved.MutationResult{}, fmt.Errorf("%w: next revision must append exactly one immutable version", saved.ErrInvalidRevision)
	}
	if input.UpdatedAt.Before(current.UpdatedAt) {
		return saved.MutationResult{}, fmt.Errorf("%w: updatedAt precedes current updatedAt", saved.ErrInvalid)
	}
	revision := input.Revision.Clone()
	if err := insertRevision(ctx, q, input.ProjectID.String(), input.ID.String(), revision); err != nil {
		return saved.MutationResult{}, err
	}
	nextNumber, err := sqliteNumber(revision.Metadata.Number)
	if err != nil {
		return saved.MutationResult{}, err
	}
	expectedNumber, err := sqliteNumber(input.ExpectedRevision.Number)
	if err != nil {
		return saved.MutationResult{}, err
	}
	changed, err := q.UpdateSavedExplorationVersion(ctx, saveddb.UpdateSavedExplorationVersionParams{
		Title: input.Title, Slug: input.Slug, Visibility: string(input.Visibility),
		SemanticModelID: input.SemanticModelID.String(), UpdatedAt: formatTime(input.UpdatedAt),
		RevisionID: revision.Metadata.ID.String(), RevisionNumber: nextNumber,
		ContentHash: revision.Metadata.ContentHash, ProjectID: input.ProjectID.String(), ExplorationID: input.ID.String(),
		ExpectedRevisionID: input.ExpectedRevision.RevisionID.String(), ExpectedRevisionNumber: expectedNumber, ExpectedContentHash: input.ExpectedRevision.ContentHash,
	})
	if err != nil {
		return saved.MutationResult{}, mapStorageError(err)
	}
	rows, err := changed.RowsAffected()
	if err != nil {
		return saved.MutationResult{}, mapStorageError(err)
	}
	if rows != 1 {
		return saved.MutationResult{}, classifyCASFailure(ctx, q, input.ProjectID.String(), input.ID.String(), false)
	}
	lifecycle, err := lifecycleByID(ctx, q, input.ProjectID.String(), input.ID.String())
	if err != nil {
		return saved.MutationResult{}, err
	}
	result := saved.MutationResult{Lifecycle: lifecycle, Revision: revisionPtr(revision), AppliedRevision: revision.Token(), ConcurrencyRevision: current.CurrentRevision.Token(), Evidence: input.Evidence}
	if err := result.Validate(); err != nil {
		return saved.MutationResult{}, fmt.Errorf("validate update result: %w", err)
	}
	if err := insertOperation(ctx, q, result, input.Evidence); err != nil {
		return r.operationInsertFailure(ctx, q, input.ProjectID.String(), input.Evidence, err, input.ExpectedRevision)
	}
	if err := r.recordAuditIntent(ctx, tx, lifecycle, revision.Metadata, input.Evidence); err != nil {
		return saved.MutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return saved.MutationResult{}, mapStorageError(err)
	}
	return result, nil
}

func (r *Repository) Duplicate(ctx context.Context, input saved.DuplicateInput) (saved.MutationResult, error) {
	if err := input.Validate(); err != nil {
		return saved.MutationResult{}, err
	}
	tx, q, replay, found, err := r.beginMutation(ctx, input.ProjectID, input.Evidence)
	if err != nil {
		return saved.MutationResult{}, err
	}
	defer tx.Rollback()
	if found {
		return r.replayResult(ctx, q, replay, input.ExpectedSourceRevision)
	}

	source, err := lifecycleByID(ctx, q, input.ProjectID.String(), input.SourceID.String())
	if err != nil {
		return saved.MutationResult{}, err
	}
	if source.CurrentRevision.Token() != input.ExpectedSourceRevision {
		return saved.MutationResult{}, saved.ErrStaleRevision
	}
	sourceRevision, err := revisionByToken(ctx, q, input.ProjectID.String(), input.SourceID.String(), input.ExpectedSourceRevision)
	if err != nil {
		return saved.MutationResult{}, err
	}
	// The source payload is the authority. The destination metadata may carry
	// a fresh revision identity, but its authored bytes must be copied exactly
	// from the source revision selected by the source CAS token.
	destinationRevision := input.Destination.Revision.Clone()
	destinationRevision.Payload = sourceRevision.Payload.Clone()
	spec, err := destinationRevision.Payload.Spec()
	if err != nil {
		return saved.MutationResult{}, err
	}
	if projectgraph.ResourceID(spec.ModelID) != input.Destination.SemanticModelID {
		return saved.MutationResult{}, fmt.Errorf("%w: duplicate semantic model does not match source payload", saved.ErrConflict)
	}
	if err := destinationRevision.Validate(); err != nil {
		return saved.MutationResult{}, err
	}
	destination := input.Destination
	destination.Revision = destinationRevision
	if _, err := saved.NewSavedExploration(saved.NewInput{
		ProjectID: destination.ProjectID, ID: destination.ID, OwnerPrincipalID: destination.OwnerPrincipalID,
		Title: destination.Title, Slug: destination.Slug, Visibility: destination.Visibility,
		SemanticModelID: destination.SemanticModelID, CreatedAt: destination.CreatedAt, Revision: destination.Revision,
	}); err != nil {
		return saved.MutationResult{}, err
	}
	destinationNumber, err := sqliteNumber(destinationRevision.Metadata.Number)
	if err != nil {
		return saved.MutationResult{}, err
	}
	if err := q.InsertSavedExploration(ctx, saveddb.InsertSavedExplorationParams{
		ProjectID: input.ProjectID.String(), ExplorationID: destination.ID.String(), OwnerPrincipalID: destination.OwnerPrincipalID,
		Title: destination.Title, Slug: destination.Slug, Visibility: string(destination.Visibility), SemanticModelID: destination.SemanticModelID.String(),
		CreatedAt: formatTime(destination.CreatedAt), UpdatedAt: formatTime(destination.CreatedAt), CurrentRevisionID: destinationRevision.Metadata.ID.String(),
		CurrentRevisionNumber: destinationNumber, CurrentContentHash: destinationRevision.Metadata.ContentHash,
	}); err != nil {
		return saved.MutationResult{}, mapCreateConstraint(ctx, tx, input.ProjectID.String(), destination.ID.String(), destination.Slug, err)
	}
	if err := insertRevision(ctx, q, input.ProjectID.String(), destination.ID.String(), destinationRevision); err != nil {
		return saved.MutationResult{}, err
	}
	lifecycle, err := lifecycleByID(ctx, q, input.ProjectID.String(), destination.ID.String())
	if err != nil {
		return saved.MutationResult{}, err
	}
	result := saved.MutationResult{Lifecycle: lifecycle, Revision: revisionPtr(destinationRevision), AppliedRevision: destinationRevision.Token(), ConcurrencyRevision: source.CurrentRevision.Token(), Evidence: input.Evidence}
	if err := result.Validate(); err != nil {
		return saved.MutationResult{}, fmt.Errorf("validate duplicate result: %w", err)
	}
	if err := insertOperation(ctx, q, result, input.Evidence); err != nil {
		return r.operationInsertFailure(ctx, q, input.ProjectID.String(), input.Evidence, err, input.ExpectedSourceRevision)
	}
	if err := r.recordAuditIntent(ctx, tx, lifecycle, destinationRevision.Metadata, input.Evidence); err != nil {
		return saved.MutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return saved.MutationResult{}, mapStorageError(err)
	}
	return result, nil
}

func (r *Repository) Archive(ctx context.Context, input saved.ArchiveInput) (saved.MutationResult, error) {
	if err := input.Validate(); err != nil {
		return saved.MutationResult{}, err
	}
	tx, q, replay, found, err := r.beginMutation(ctx, input.ProjectID, input.Evidence)
	if err != nil {
		return saved.MutationResult{}, err
	}
	defer tx.Rollback()
	if found {
		return r.replayResult(ctx, q, replay, input.ExpectedRevision)
	}
	current, err := lifecycleByID(ctx, q, input.ProjectID.String(), input.ID.String())
	if err != nil {
		return saved.MutationResult{}, err
	}
	if current.Status == saved.StatusArchived {
		return saved.MutationResult{}, saved.ErrArchived
	}
	if current.CurrentRevision.Token() != input.ExpectedRevision {
		return saved.MutationResult{}, saved.ErrStaleRevision
	}
	if input.ArchivedAt.Before(current.UpdatedAt) {
		return saved.MutationResult{}, fmt.Errorf("%w: archivedAt precedes current updatedAt", saved.ErrInvalid)
	}
	expectedNumber, err := sqliteNumber(input.ExpectedRevision.Number)
	if err != nil {
		return saved.MutationResult{}, err
	}
	changed, err := q.ArchiveSavedExploration(ctx, saveddb.ArchiveSavedExplorationParams{
		ArchivedAt: sql.NullString{String: formatTime(input.ArchivedAt), Valid: true},
		ProjectID:  input.ProjectID.String(), ExplorationID: input.ID.String(),
		ExpectedRevisionID: input.ExpectedRevision.RevisionID.String(), ExpectedRevisionNumber: expectedNumber, ExpectedContentHash: input.ExpectedRevision.ContentHash,
	})
	if err != nil {
		return saved.MutationResult{}, mapStorageError(err)
	}
	rows, err := changed.RowsAffected()
	if err != nil {
		return saved.MutationResult{}, mapStorageError(err)
	}
	if rows != 1 {
		return saved.MutationResult{}, classifyCASFailure(ctx, q, input.ProjectID.String(), input.ID.String(), true)
	}
	lifecycle, err := lifecycleByID(ctx, q, input.ProjectID.String(), input.ID.String())
	if err != nil {
		return saved.MutationResult{}, err
	}
	result := saved.MutationResult{Lifecycle: lifecycle, AppliedRevision: lifecycle.CurrentRevision.Token(), ConcurrencyRevision: current.CurrentRevision.Token(), Evidence: input.Evidence}
	if err := result.Validate(); err != nil {
		return saved.MutationResult{}, fmt.Errorf("validate archive result: %w", err)
	}
	if err := insertOperation(ctx, q, result, input.Evidence); err != nil {
		return r.operationInsertFailure(ctx, q, input.ProjectID.String(), input.Evidence, err, input.ExpectedRevision)
	}
	if err := r.recordAuditIntent(ctx, tx, lifecycle, lifecycle.CurrentRevision, input.Evidence); err != nil {
		return saved.MutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return saved.MutationResult{}, mapStorageError(err)
	}
	return result, nil
}
