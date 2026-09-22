// Package postgres implements the native development-session authority.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/project/developmentsession"
	sessiondb "github.com/flidai/leapview/internal/project/developmentsession/postgres/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type DBTX = sessiondb.DBTX

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

type Repository struct{ db DBTX }

func New(db DBTX) *Repository              { return &Repository{db: db} }
func NewRepository(db DBTX) *Repository    { return New(db) }
func (r *Repository) PostgreSQLAuthority() {}
func (r *Repository) Configured() bool     { return r != nil && r.db != nil }

func (r *Repository) Resolve(ctx context.Context, key developmentsession.Key) (developmentsession.Record, error) {
	if r == nil || r.db == nil {
		return developmentsession.Record{}, developmentsession.ErrInvalid
	}
	if err := key.Validate(); err != nil {
		return developmentsession.Record{}, err
	}
	row, err := sessiondb.New(r.db).GetDevelopmentSession(ctx, getParams(key))
	if err != nil {
		return developmentsession.Record{}, readError(err)
	}
	return scan(row, key)
}

// Load is an expressive alias retained for non-HTTP callers.
func (r *Repository) Load(ctx context.Context, key developmentsession.Key) (developmentsession.Record, error) {
	return r.Resolve(ctx, key)
}

func (r *Repository) Save(ctx context.Context, input developmentsession.Record, expectedRevision int64) (developmentsession.Record, error) {
	if r == nil || r.db == nil {
		return developmentsession.Record{}, developmentsession.ErrInvalid
	}
	if _, ok := r.db.(beginner); !ok {
		return developmentsession.Record{}, fmt.Errorf("%w: PostgreSQL session handle must support transactions", developmentsession.ErrInvalid)
	}
	record, err := input.Normalize()
	if err != nil {
		return developmentsession.Record{}, err
	}
	if expectedRevision < 0 {
		return developmentsession.Record{}, developmentsession.ErrConflict
	}
	tx, err := r.db.(beginner).Begin(ctx)
	if err != nil {
		return developmentsession.Record{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := sessiondb.New(tx)
	currentRow, loadErr := queries.GetDevelopmentSessionForUpdate(ctx, getForUpdateParams(record.Key))
	if errors.Is(loadErr, pgx.ErrNoRows) {
		if expectedRevision != 0 {
			return developmentsession.Record{}, developmentsession.ErrConflict
		}
		if err := insert(ctx, queries, record); err != nil {
			if isUniqueViolation(err) {
				return developmentsession.Record{}, developmentsession.ErrConflict
			}
			return developmentsession.Record{}, err
		}
	} else if loadErr != nil {
		return developmentsession.Record{}, readError(loadErr)
	} else {
		current, scanErr := scan(currentRow, record.Key)
		if scanErr != nil {
			return developmentsession.Record{}, scanErr
		}
		if current.Revision != expectedRevision {
			return developmentsession.Record{}, developmentsession.ErrConflict
		}
		if err := update(ctx, queries, record, expectedRevision); err != nil {
			return developmentsession.Record{}, err
		}
	}
	resultRow, err := queries.GetDevelopmentSession(ctx, getParams(record.Key))
	if err != nil {
		return developmentsession.Record{}, readError(err)
	}
	result, err := scan(resultRow, record.Key)
	if err != nil {
		return developmentsession.Record{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return developmentsession.Record{}, err
	}
	return result, nil
}

func insert(ctx context.Context, queries *sessiondb.Queries, record developmentsession.Record) error {
	diagnostics, err := marshalDiagnostics(record.Diagnostics)
	if err != nil {
		return err
	}
	rows, err := queries.InsertDevelopmentSession(ctx, sessiondb.InsertDevelopmentSessionParams{
		ID: record.ID, OwnerID: record.Key.OwnerID, CheckoutID: record.Key.CheckoutID, WorktreeID: record.Key.WorktreeID,
		ProjectID: record.Key.ProjectID.String(), TargetID: record.Key.TargetID, Environment: record.Key.Environment,
		AttemptedCandidateID: record.Attempted.CandidateID, AttemptedArtifactDigest: record.Attempted.ArtifactDigest,
		AttemptedGraphDigest: record.Attempted.GraphDigest, AttemptedPreviewUrl: record.Attempted.PreviewURL,
		LastValidCandidateID: record.LastValid.CandidateID, LastValidArtifactDigest: record.LastValid.ArtifactDigest,
		LastValidGraphDigest: record.LastValid.GraphDigest, LastValidPreviewUrl: record.LastValid.PreviewURL,
		DiagnosticsJson: diagnostics,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return developmentsession.ErrConflict
	}
	return nil
}

func update(ctx context.Context, queries *sessiondb.Queries, record developmentsession.Record, expected int64) error {
	diagnostics, err := marshalDiagnostics(record.Diagnostics)
	if err != nil {
		return err
	}
	rows, err := queries.UpdateDevelopmentSession(ctx, sessiondb.UpdateDevelopmentSessionParams{
		AttemptedCandidateID: record.Attempted.CandidateID, AttemptedArtifactDigest: record.Attempted.ArtifactDigest,
		AttemptedGraphDigest: record.Attempted.GraphDigest, AttemptedPreviewUrl: record.Attempted.PreviewURL,
		LastValidCandidateID: record.LastValid.CandidateID, LastValidArtifactDigest: record.LastValid.ArtifactDigest,
		LastValidGraphDigest: record.LastValid.GraphDigest, LastValidPreviewUrl: record.LastValid.PreviewURL,
		DiagnosticsJson: diagnostics, ID: record.ID, OwnerID: record.Key.OwnerID,
		CheckoutID: record.Key.CheckoutID, WorktreeID: record.Key.WorktreeID, ExpectedRevision: expected,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return developmentsession.ErrConflict
	}
	return nil
}

func marshalDiagnostics(values []developmentsession.Diagnostic) ([]byte, error) {
	// The durable column requires a JSON array. A freshly initialized session
	// has no diagnostics, and json.Marshal(nil) would store JSON null instead.
	if values == nil {
		values = []developmentsession.Diagnostic{}
	}
	return json.Marshal(values)
}

func scan(row sessiondb.ProjectDevelopmentSession, key developmentsession.Key) (developmentsession.Record, error) {
	if row.OwnerID != key.OwnerID {
		return developmentsession.Record{}, developmentsession.ErrOwnerMismatch
	}
	if row.CheckoutID != key.CheckoutID || row.WorktreeID != key.WorktreeID {
		return developmentsession.Record{}, developmentsession.ErrOwnerMismatch
	}
	parsedProject, err := projectIDValue(row.ProjectID)
	if err != nil {
		return developmentsession.Record{}, err
	}
	if parsedProject != key.ProjectID || row.TargetID != key.TargetID || row.Environment != key.Environment {
		return developmentsession.Record{}, developmentsession.ErrOwnerMismatch
	}
	record := developmentsession.Record{
		ID: row.ID, Key: key, Revision: row.Revision, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		Attempted: developmentsession.Identity{CandidateID: row.AttemptedCandidateID, ArtifactDigest: row.AttemptedArtifactDigest, GraphDigest: row.AttemptedGraphDigest, PreviewURL: row.AttemptedPreviewUrl},
		LastValid: developmentsession.Identity{CandidateID: row.LastValidCandidateID, ArtifactDigest: row.LastValidArtifactDigest, GraphDigest: row.LastValidGraphDigest, PreviewURL: row.LastValidPreviewUrl},
	}
	if len(row.DiagnosticsJson) != 0 {
		if err := json.Unmarshal(row.DiagnosticsJson, &record.Diagnostics); err != nil {
			return developmentsession.Record{}, fmt.Errorf("decode development session diagnostics: %w", err)
		}
	}
	normalized, err := record.Normalize()
	if err != nil {
		return developmentsession.Record{}, err
	}
	return normalized, nil
}

func getParams(key developmentsession.Key) sessiondb.GetDevelopmentSessionParams {
	return sessiondb.GetDevelopmentSessionParams{OwnerID: key.OwnerID, CheckoutID: key.CheckoutID, WorktreeID: key.WorktreeID, ProjectID: key.ProjectID.String(), TargetID: key.TargetID, Environment: key.Environment}
}

func getForUpdateParams(key developmentsession.Key) sessiondb.GetDevelopmentSessionForUpdateParams {
	return sessiondb.GetDevelopmentSessionForUpdateParams{OwnerID: key.OwnerID, CheckoutID: key.CheckoutID, WorktreeID: key.WorktreeID, ProjectID: key.ProjectID.String(), TargetID: key.TargetID, Environment: key.Environment}
}

func readError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return developmentsession.ErrNotFound
	}
	return err
}

func projectIDValue(value string) (projectgraph.ResourceID, error) {
	id, err := projectgraph.NewResourceID(value)
	if err != nil {
		return "", fmt.Errorf("%w: stored project identity is invalid", developmentsession.ErrInvalid)
	}
	return id, err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
