// Package postgres is the native PostgreSQL persistence authority for saved
// explorations. It deliberately accepts pgx surfaces only.
package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"reflect"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/exploration/saved"
	saveddb "github.com/flidai/leapview/internal/analytics/exploration/saved/postgres/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is the native pgx query surface implemented by pools, connections, and
// caller-owned transactions.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Tx is the transaction boundary owned by this repository for mutations.
// AuditRepository receives this exact transaction and must not commit it.
type Tx = pgx.Tx

// AuditRepository is the Access-owned direct audit append boundary.
type AuditRepository interface {
	RecordAuditEvent(context.Context, Tx, access.AuditIntent) error
}

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// Repository stores lifecycle metadata, immutable revisions, and a durable
// actor-scoped operation ledger. A nil audit recorder is allowed for metadata
// reads and exact replays, but every fresh mutation fails closed.
type Repository struct {
	db    DBTX
	begin beginner
	audit AuditRepository
}

var _ saved.Repository = (*Repository)(nil)

//go:embed schema.sql
var schemaFS embed.FS

var schemaSQL = func() string {
	b, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		panic(err)
	}
	return string(b)
}()

// SchemaSQL returns the standalone capability schema for migration runners.
func SchemaSQL() string { return schemaSQL }

// ApplySchema executes the capability schema through a caller-owned pgx
// connection or transaction. It never begins, commits, or rolls back.
func ApplySchema(ctx context.Context, db DBTX) error {
	if isNilInterface(db) {
		return errors.New("saved exploration PostgreSQL database is nil")
	}
	// sqlc-exception: schema-ddl. Only migration runners and disposable test
	// setup execute this embedded capability DDL; request queries use sqlc.
	_, err := db.Exec(ctx, schemaSQL)
	return err
}

// New constructs the repository over a pgx pool, connection, or transaction.
// A fresh mutation or durable replay requires db to implement Begin and a
// fresh mutation additionally requires audit; metadata reads need only DBTX.
func New(db DBTX, audit AuditRepository) *Repository {
	if isNilInterface(db) {
		db = nil
	}
	if isNilInterface(audit) {
		audit = nil
	}
	var begin beginner
	if candidate, ok := db.(beginner); ok {
		begin = candidate
	}
	return &Repository{db: db, begin: begin, audit: audit}
}

// Configured reports whether a native database handle is present.
func (r *Repository) Configured() bool { return r != nil && !isNilInterface(r.db) }

// AuditCapable reports whether fresh mutations can open a transaction and
// hand off a typed audit intent to Access.
func (r *Repository) AuditCapable() bool {
	return r != nil && r.begin != nil && !isNilInterface(r.db) && !isNilInterface(r.audit)
}

func (r *Repository) queryDB() (DBTX, error) {
	if r == nil || isNilInterface(r.db) {
		return nil, saved.ErrUnavailable
	}
	return r.db, nil
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func (r *Repository) GetLifecycle(ctx context.Context, input saved.ReadInput) (saved.Lifecycle, error) {
	if err := input.Validate(); err != nil {
		return saved.Lifecycle{}, err
	}
	db, err := r.queryDB()
	if err != nil {
		return saved.Lifecycle{}, err
	}
	row, err := getLifecycleRow(ctx, db, input.ProjectID.String(), input.ID.String(), false)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return saved.Lifecycle{}, saved.ErrNotFound
		}
		return saved.Lifecycle{}, mapStorageError(err)
	}
	return lifecycleFromRow(row)
}

func (r *Repository) GetRevision(ctx context.Context, input saved.RevisionReadInput) (saved.Revision, error) {
	if err := input.Validate(); err != nil {
		return saved.Revision{}, err
	}
	db, err := r.queryDB()
	if err != nil {
		return saved.Revision{}, err
	}
	row, err := getRevisionRow(ctx, db, input.ProjectID.String(), input.ID.String(), input.Revision)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return saved.Revision{}, saved.ErrNotFound
		}
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

func (r *Repository) ListPage(ctx context.Context, input saved.ListInput) (saved.ListPage, error) {
	if err := input.Validate(); err != nil {
		return saved.ListPage{}, err
	}
	db, err := r.queryDB()
	if err != nil {
		return saved.ListPage{}, err
	}
	limit := input.Limit
	if limit == 0 {
		limit = saved.MaxListLimit
	}
	rows, err := listLifecycleRows(ctx, db, input.ProjectID.String(), input.IncludeArchived, input.Cursor, limit+1)
	if err != nil {
		return saved.ListPage{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	page := saved.ListPage{Items: make([]saved.Lifecycle, 0, len(rows))}
	for _, row := range rows {
		lifecycle, err := lifecycleFromRow(row)
		if err != nil {
			return saved.ListPage{}, err
		}
		page.Items = append(page.Items, lifecycle)
	}
	if hasMore && len(page.Items) != 0 {
		page.NextCursor = page.Items[len(page.Items)-1].ID.String()
	}
	return page, nil
}

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
	db, err := r.queryDB()
	if err != nil {
		return saved.MutationReplayMetadata{}, false, err
	}
	row, err := getOperationRow(ctx, db, input.ProjectID.String(), input.ActorID, string(input.Action), input.IdempotencyKey)
	if errors.Is(err, pgx.ErrNoRows) {
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
	tx, operation, found, err := r.beginMutation(ctx, input.ProjectID, input.Evidence)
	if err != nil {
		return saved.MutationResult{}, err
	}
	defer rollback(tx)
	if found {
		return r.replayResult(ctx, tx, operation)
	}
	revision := input.Revision.Clone()
	revisionNumber, err := postgresNumber(revision.Metadata.Number)
	if err != nil {
		return saved.MutationResult{}, err
	}
	created, err := saveddb.New(tx).InsertSavedExploration(ctx, saveddb.InsertSavedExplorationParams{
		ProjectID: input.ProjectID.String(), ExplorationID: input.ID.String(), OwnerPrincipalID: input.OwnerPrincipalID,
		Title: input.Title, Slug: input.Slug, Visibility: string(input.Visibility), Status: string(saved.StatusActive),
		SemanticModelID: input.SemanticModelID.String(), CreatedAt: formatTime(input.CreatedAt), UpdatedAt: formatTime(input.CreatedAt),
		CurrentRevisionID: revision.Metadata.ID.String(), CurrentRevisionNumber: revisionNumber, CurrentContentHash: revision.Metadata.ContentHash,
	})
	if err != nil {
		return saved.MutationResult{}, mapStorageError(err)
	}
	if created != 1 {
		return saved.MutationResult{}, mapCreateConflict(ctx, tx, input.ProjectID.String(), input.ID.String())
	}
	if err := insertRevision(ctx, tx, input.ProjectID.String(), input.ID.String(), revision); err != nil {
		return saved.MutationResult{}, err
	}
	lifecycle, err := lifecycleByID(ctx, tx, input.ProjectID.String(), input.ID.String(), false)
	if err != nil {
		return saved.MutationResult{}, err
	}
	result := saved.MutationResult{Lifecycle: lifecycle, Revision: revisionPtr(revision), AppliedRevision: revision.Token(), Evidence: input.Evidence}
	if err := result.Validate(); err != nil {
		return saved.MutationResult{}, fmt.Errorf("validate create result: %w", err)
	}
	if inserted, err := insertOperation(ctx, tx, result, input.Evidence); err != nil {
		return saved.MutationResult{}, err
	} else if !inserted {
		return saved.MutationResult{}, saved.ErrConflict
	}
	if err := r.recordAuditIntent(ctx, tx, lifecycle, revision.Metadata, input.Evidence); err != nil {
		return saved.MutationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return saved.MutationResult{}, mapStorageError(err)
	}
	return result, nil
}

func (r *Repository) UpdateVersion(ctx context.Context, input saved.UpdateVersionInput) (saved.MutationResult, error) {
	if err := input.Validate(); err != nil {
		return saved.MutationResult{}, err
	}
	tx, operation, found, err := r.beginMutation(ctx, input.ProjectID, input.Evidence)
	if err != nil {
		return saved.MutationResult{}, err
	}
	defer rollback(tx)
	if found {
		return r.replayResult(ctx, tx, operation, input.ExpectedRevision)
	}
	current, err := lifecycleByID(ctx, tx, input.ProjectID.String(), input.ID.String(), true)
	if err != nil {
		return saved.MutationResult{}, err
	}
	if current.Status == saved.StatusArchived {
		return saved.MutationResult{}, saved.ErrArchived
	}
	if current.CurrentRevision.Token() != input.ExpectedRevision {
		return saved.MutationResult{}, saved.ErrStaleRevision
	}
	if input.UpdatedAt.Before(current.UpdatedAt) {
		return saved.MutationResult{}, fmt.Errorf("%w: updatedAt precedes current updatedAt", saved.ErrInvalid)
	}
	revision := input.Revision.Clone()
	revisionNumber, err := postgresNumber(revision.Metadata.Number)
	if err != nil {
		return saved.MutationResult{}, err
	}
	expectedNumber, err := postgresNumber(input.ExpectedRevision.Number)
	if err != nil {
		return saved.MutationResult{}, err
	}
	if err := insertRevision(ctx, tx, input.ProjectID.String(), input.ID.String(), revision); err != nil {
		return saved.MutationResult{}, err
	}
	updated, err := saveddb.New(tx).UpdateSavedExplorationVersion(ctx, saveddb.UpdateSavedExplorationVersionParams{
		Title: input.Title, Slug: input.Slug, Visibility: string(input.Visibility), SemanticModelID: input.SemanticModelID.String(), UpdatedAt: formatTime(input.UpdatedAt),
		RevisionID: revision.Metadata.ID.String(), RevisionNumber: revisionNumber, ContentHash: revision.Metadata.ContentHash,
		ProjectID: input.ProjectID.String(), ExplorationID: input.ID.String(), ExpectedRevisionID: input.ExpectedRevision.RevisionID.String(), ExpectedRevisionNumber: expectedNumber, ExpectedContentHash: input.ExpectedRevision.ContentHash,
	})
	if err != nil {
		return saved.MutationResult{}, mapStorageError(err)
	}
	if updated != 1 {
		return saved.MutationResult{}, classifyCASFailure(ctx, tx, input.ProjectID.String(), input.ID.String(), false)
	}
	lifecycle, err := lifecycleByID(ctx, tx, input.ProjectID.String(), input.ID.String(), false)
	if err != nil {
		return saved.MutationResult{}, err
	}
	result := saved.MutationResult{Lifecycle: lifecycle, Revision: revisionPtr(revision), AppliedRevision: revision.Token(), ConcurrencyRevision: current.CurrentRevision.Token(), Evidence: input.Evidence}
	if err := result.Validate(); err != nil {
		return saved.MutationResult{}, fmt.Errorf("validate update result: %w", err)
	}
	if inserted, err := insertOperation(ctx, tx, result, input.Evidence); err != nil {
		return saved.MutationResult{}, err
	} else if !inserted {
		return saved.MutationResult{}, saved.ErrConflict
	}
	if err := r.recordAuditIntent(ctx, tx, lifecycle, revision.Metadata, input.Evidence); err != nil {
		return saved.MutationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return saved.MutationResult{}, mapStorageError(err)
	}
	return result, nil
}

func (r *Repository) Duplicate(ctx context.Context, input saved.DuplicateInput) (saved.MutationResult, error) {
	if err := input.Validate(); err != nil {
		return saved.MutationResult{}, err
	}
	tx, operation, found, err := r.beginMutation(ctx, input.ProjectID, input.Evidence)
	if err != nil {
		return saved.MutationResult{}, err
	}
	defer rollback(tx)
	if found {
		return r.replayResult(ctx, tx, operation, input.ExpectedSourceRevision)
	}
	source, err := lifecycleByID(ctx, tx, input.ProjectID.String(), input.SourceID.String(), true)
	if err != nil {
		return saved.MutationResult{}, err
	}
	if source.CurrentRevision.Token() != input.ExpectedSourceRevision {
		return saved.MutationResult{}, saved.ErrStaleRevision
	}
	sourceRevision, err := revisionByToken(ctx, tx, input.ProjectID.String(), input.SourceID.String(), input.ExpectedSourceRevision)
	if err != nil {
		return saved.MutationResult{}, err
	}
	destinationRevision := input.Destination.Revision.Clone()
	destinationRevision.Payload = sourceRevision.Payload.Clone()
	destinationNumber, err := postgresNumber(destinationRevision.Metadata.Number)
	if err != nil {
		return saved.MutationResult{}, err
	}
	if err := destinationRevision.Validate(); err != nil {
		return saved.MutationResult{}, err
	}
	destination := input.Destination
	destination.Revision = destinationRevision
	if _, err := saved.NewSavedExploration(saved.NewInput{ProjectID: destination.ProjectID, ID: destination.ID, OwnerPrincipalID: destination.OwnerPrincipalID, Title: destination.Title, Slug: destination.Slug, Visibility: destination.Visibility, SemanticModelID: destination.SemanticModelID, CreatedAt: destination.CreatedAt, Revision: destination.Revision}); err != nil {
		return saved.MutationResult{}, err
	}
	created, err := saveddb.New(tx).InsertSavedExploration(ctx, saveddb.InsertSavedExplorationParams{
		ProjectID: destination.ProjectID.String(), ExplorationID: destination.ID.String(), OwnerPrincipalID: destination.OwnerPrincipalID,
		Title: destination.Title, Slug: destination.Slug, Visibility: string(destination.Visibility), Status: string(saved.StatusActive), SemanticModelID: destination.SemanticModelID.String(),
		CreatedAt: formatTime(destination.CreatedAt), UpdatedAt: formatTime(destination.CreatedAt), CurrentRevisionID: destinationRevision.Metadata.ID.String(), CurrentRevisionNumber: destinationNumber, CurrentContentHash: destinationRevision.Metadata.ContentHash,
	})
	if err != nil {
		return saved.MutationResult{}, mapStorageError(err)
	}
	if created != 1 {
		return saved.MutationResult{}, mapCreateConflict(ctx, tx, input.ProjectID.String(), destination.ID.String())
	}
	if err := insertRevision(ctx, tx, input.ProjectID.String(), destination.ID.String(), destinationRevision); err != nil {
		return saved.MutationResult{}, err
	}
	lifecycle, err := lifecycleByID(ctx, tx, input.ProjectID.String(), destination.ID.String(), false)
	if err != nil {
		return saved.MutationResult{}, err
	}
	result := saved.MutationResult{Lifecycle: lifecycle, Revision: revisionPtr(destinationRevision), AppliedRevision: destinationRevision.Token(), ConcurrencyRevision: source.CurrentRevision.Token(), Evidence: input.Evidence}
	if err := result.Validate(); err != nil {
		return saved.MutationResult{}, fmt.Errorf("validate duplicate result: %w", err)
	}
	if inserted, err := insertOperation(ctx, tx, result, input.Evidence); err != nil {
		return saved.MutationResult{}, err
	} else if !inserted {
		return saved.MutationResult{}, saved.ErrConflict
	}
	if err := r.recordAuditIntent(ctx, tx, lifecycle, destinationRevision.Metadata, input.Evidence); err != nil {
		return saved.MutationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return saved.MutationResult{}, mapStorageError(err)
	}
	return result, nil
}

func (r *Repository) Archive(ctx context.Context, input saved.ArchiveInput) (saved.MutationResult, error) {
	if err := input.Validate(); err != nil {
		return saved.MutationResult{}, err
	}
	tx, operation, found, err := r.beginMutation(ctx, input.ProjectID, input.Evidence)
	if err != nil {
		return saved.MutationResult{}, err
	}
	defer rollback(tx)
	if found {
		return r.replayResult(ctx, tx, operation, input.ExpectedRevision)
	}
	current, err := lifecycleByID(ctx, tx, input.ProjectID.String(), input.ID.String(), true)
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
	expectedNumber, err := postgresNumber(input.ExpectedRevision.Number)
	if err != nil {
		return saved.MutationResult{}, err
	}
	archivedAt := formatTime(input.ArchivedAt)
	archived, err := saveddb.New(tx).ArchiveSavedExploration(ctx, saveddb.ArchiveSavedExplorationParams{
		ArchivedAt: &archivedAt, ProjectID: input.ProjectID.String(), ExplorationID: input.ID.String(),
		ExpectedRevisionID: input.ExpectedRevision.RevisionID.String(), ExpectedRevisionNumber: expectedNumber, ExpectedContentHash: input.ExpectedRevision.ContentHash,
	})
	if err != nil {
		return saved.MutationResult{}, mapStorageError(err)
	}
	if archived != 1 {
		return saved.MutationResult{}, classifyCASFailure(ctx, tx, input.ProjectID.String(), input.ID.String(), true)
	}
	lifecycle, err := lifecycleByID(ctx, tx, input.ProjectID.String(), input.ID.String(), false)
	if err != nil {
		return saved.MutationResult{}, err
	}
	result := saved.MutationResult{Lifecycle: lifecycle, AppliedRevision: lifecycle.CurrentRevision.Token(), ConcurrencyRevision: current.CurrentRevision.Token(), Evidence: input.Evidence}
	if err := result.Validate(); err != nil {
		return saved.MutationResult{}, fmt.Errorf("validate archive result: %w", err)
	}
	if inserted, err := insertOperation(ctx, tx, result, input.Evidence); err != nil {
		return saved.MutationResult{}, err
	} else if !inserted {
		return saved.MutationResult{}, saved.ErrConflict
	}
	if err := r.recordAuditIntent(ctx, tx, lifecycle, lifecycle.CurrentRevision, input.Evidence); err != nil {
		return saved.MutationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return saved.MutationResult{}, mapStorageError(err)
	}
	return result, nil
}

func rollback(tx pgx.Tx) {
	if tx != nil {
		_ = tx.Rollback(context.Background())
	}
}
