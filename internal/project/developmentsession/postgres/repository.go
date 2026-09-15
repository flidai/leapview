// Package postgres implements the native development-session authority.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/project/developmentsession"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

type Repository struct{ db DBTX }

func New(db DBTX) *Repository              { return &Repository{db: db} }
func NewRepository(db DBTX) *Repository    { return New(db) }
func (r *Repository) PostgreSQLAuthority() {}
func (r *Repository) Configured() bool     { return r != nil && r.db != nil }

const selectSession = `
	SELECT id, owner_id, checkout_id, worktree_id, project_id, target_id, environment,
	       attempted_candidate_id, attempted_artifact_digest, attempted_graph_digest,
	       attempted_preview_url, last_valid_candidate_id, last_valid_artifact_digest, last_valid_graph_digest,
	       last_valid_preview_url,
       diagnostics_json, revision, created_at, updated_at
  FROM project.development_session
 WHERE owner_id = $1 AND checkout_id = $2 AND worktree_id = $3 AND project_id = $4 AND target_id = $5 AND environment = $6`

func (r *Repository) Resolve(ctx context.Context, key developmentsession.Key) (developmentsession.Record, error) {
	if r == nil || r.db == nil {
		return developmentsession.Record{}, developmentsession.ErrInvalid
	}
	if err := key.Validate(); err != nil {
		return developmentsession.Record{}, err
	}
	return scan(r.db.QueryRow(ctx, selectSession, key.OwnerID, key.CheckoutID, key.WorktreeID, key.ProjectID.String(), key.TargetID, key.Environment), key)
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
	current, loadErr := scan(tx.QueryRow(ctx, selectSession+" FOR UPDATE", record.Key.OwnerID, record.Key.CheckoutID, record.Key.WorktreeID, record.Key.ProjectID.String(), record.Key.TargetID, record.Key.Environment), record.Key)
	if errors.Is(loadErr, developmentsession.ErrNotFound) {
		if expectedRevision != 0 {
			return developmentsession.Record{}, developmentsession.ErrConflict
		}
		if err := insert(ctx, tx, record); err != nil {
			if isUniqueViolation(err) {
				return developmentsession.Record{}, developmentsession.ErrConflict
			}
			return developmentsession.Record{}, err
		}
	} else if loadErr != nil {
		return developmentsession.Record{}, loadErr
	} else {
		if current.Revision != expectedRevision {
			return developmentsession.Record{}, developmentsession.ErrConflict
		}
		if err := update(ctx, tx, record, expectedRevision); err != nil {
			return developmentsession.Record{}, err
		}
	}
	result, err := scan(tx.QueryRow(ctx, selectSession, record.Key.OwnerID, record.Key.CheckoutID, record.Key.WorktreeID, record.Key.ProjectID.String(), record.Key.TargetID, record.Key.Environment), record.Key)
	if err != nil {
		return developmentsession.Record{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return developmentsession.Record{}, err
	}
	return result, nil
}

func insert(ctx context.Context, tx DBTX, record developmentsession.Record) error {
	diagnostics, err := json.Marshal(record.Diagnostics)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO project.development_session
 (id, owner_id, checkout_id, worktree_id, project_id, target_id, environment,
  attempted_candidate_id, attempted_artifact_digest, attempted_graph_digest,
  attempted_preview_url, last_valid_candidate_id, last_valid_artifact_digest, last_valid_graph_digest,
  last_valid_preview_url,
  diagnostics_json, revision, created_at, updated_at)
	 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,1,clock_timestamp(),clock_timestamp())`,
		record.ID, record.Key.OwnerID, record.Key.CheckoutID, record.Key.WorktreeID, record.Key.ProjectID.String(), record.Key.TargetID, record.Key.Environment,
		record.Attempted.CandidateID, record.Attempted.ArtifactDigest, record.Attempted.GraphDigest, record.Attempted.PreviewURL,
		record.LastValid.CandidateID, record.LastValid.ArtifactDigest, record.LastValid.GraphDigest, record.LastValid.PreviewURL, diagnostics)
	return err
}

func update(ctx context.Context, tx DBTX, record developmentsession.Record, expected int64) error {
	diagnostics, err := json.Marshal(record.Diagnostics)
	if err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `UPDATE project.development_session SET
 attempted_candidate_id=$1, attempted_artifact_digest=$2, attempted_graph_digest=$3, attempted_preview_url=$4,
 last_valid_candidate_id=$5, last_valid_artifact_digest=$6, last_valid_graph_digest=$7, last_valid_preview_url=$8,
 diagnostics_json=$9, revision=revision+1, updated_at=clock_timestamp()
 WHERE id=$10 AND owner_id=$11 AND checkout_id=$12 AND worktree_id=$13 AND revision=$14`,
		record.Attempted.CandidateID, record.Attempted.ArtifactDigest, record.Attempted.GraphDigest, record.Attempted.PreviewURL,
		record.LastValid.CandidateID, record.LastValid.ArtifactDigest, record.LastValid.GraphDigest, record.LastValid.PreviewURL,
		diagnostics, record.ID, record.Key.OwnerID, record.Key.CheckoutID, record.Key.WorktreeID, expected)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return developmentsession.ErrConflict
	}
	return nil
}

func scan(row pgx.Row, key developmentsession.Key) (developmentsession.Record, error) {
	var record developmentsession.Record
	var owner, checkoutID, worktreeID, projectID, target, environment string
	var attempted, lastValid developmentsession.Identity
	var diagnosticsJSON []byte
	if err := row.Scan(&record.ID, &owner, &checkoutID, &worktreeID, &projectID, &target, &environment,
		&attempted.CandidateID, &attempted.ArtifactDigest, &attempted.GraphDigest, &attempted.PreviewURL,
		&lastValid.CandidateID, &lastValid.ArtifactDigest, &lastValid.GraphDigest, &lastValid.PreviewURL,
		&diagnosticsJSON, &record.Revision, &record.CreatedAt, &record.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return developmentsession.Record{}, developmentsession.ErrNotFound
		}
		return developmentsession.Record{}, err
	}
	if owner != key.OwnerID {
		return developmentsession.Record{}, developmentsession.ErrOwnerMismatch
	}
	if checkoutID != key.CheckoutID || worktreeID != key.WorktreeID {
		return developmentsession.Record{}, developmentsession.ErrOwnerMismatch
	}
	parsedProject, err := projectIDValue(projectID)
	if err != nil {
		return developmentsession.Record{}, err
	}
	if parsedProject != key.ProjectID || target != key.TargetID || environment != key.Environment {
		return developmentsession.Record{}, developmentsession.ErrOwnerMismatch
	}
	if len(diagnosticsJSON) != 0 {
		if err := json.Unmarshal(diagnosticsJSON, &record.Diagnostics); err != nil {
			return developmentsession.Record{}, fmt.Errorf("decode development session diagnostics: %w", err)
		}
	}
	record.Key = key
	record.Attempted, record.LastValid = attempted, lastValid
	normalized, err := record.Normalize()
	if err != nil {
		return developmentsession.Record{}, err
	}
	return normalized, nil
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
