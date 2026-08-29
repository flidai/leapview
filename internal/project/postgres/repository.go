// Package postgres implements the clean-slate project identity authority.
//
// The repository uses native pgx values and accepts a caller-owned transaction
// for every write. It never opens a transaction, selects SQLite, or rewrites
// authored metadata already committed by another caller.
package postgres

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	project "github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is implemented by pgx connections, transactions, and pools. Keeping
// this surface small lets callers compose identity persistence with another
// control-plane mutation in one transaction.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Tx is the transaction surface accepted by ApplySchema and EnsureTx.
type Tx = DBTX

var (
	// ErrInvalid indicates a malformed identity or metadata value.
	ErrInvalid = errors.New("invalid project identity")
	// ErrConflict indicates an existing identity whose authored metadata does
	// not exactly match the requested ensure input.
	ErrConflict = errors.New("project identity conflict")
	// ErrNotFound indicates that no identity exists for the requested ID.
	ErrNotFound = errors.New("project identity not found")
)

const (
	maxProjectIDBytes   = 255
	maxTitleBytes       = 255
	maxDescriptionBytes = 4096
)

// Record is the durable project identity and authored metadata. Timestamps
// are read from PostgreSQL and cannot be supplied by callers.
type Record struct {
	ID          projectgraph.ResourceID
	Title       string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Identity is an alias retained for callers that prefer the domain term.
type Identity = Record

// EnsureInput contains the project identity and authored metadata to ensure.
// An empty title is canonicalized to the project ID; description may be empty.
type EnsureInput struct {
	ID          projectgraph.ResourceID
	Title       string
	Description string
}

// IdentityInput is an alias for the engine-neutral input vocabulary.
type IdentityInput = EnsureInput

// Repository persists project identity records in PostgreSQL.
type Repository struct{ db DBTX }

//go:embed schema.sql
var schemaSQL string

// SchemaSQL returns the capability-owned schema without transaction control.
func SchemaSQL() string { return schemaSQL }

// ApplySchema executes this capability schema through a caller-owned
// transaction. It intentionally does not BEGIN or COMMIT.
func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := tx.Exec(ctx, schemaSQL)
	return err
}

// New constructs a repository over a pgx pool, connection, or transaction.
func New(db DBTX) *Repository { return &Repository{db: db} }

// NewRepository is an expressive compatibility constructor.
func NewRepository(db DBTX) *Repository { return New(db) }

// WithTx returns a repository using tx as its database surface. The caller
// retains ownership of commit and rollback.
func (r *Repository) WithTx(tx Tx) *Repository { return New(tx) }

// Ensure installs or exactly replays a project identity and its authored
// metadata. Existing rows are never updated: a differing title or
// description returns ErrConflict.
func (r *Repository) Ensure(ctx context.Context, input EnsureInput) (Record, error) {
	if r == nil || r.db == nil {
		return Record{}, ErrInvalid
	}
	return ensure(contextOrBackground(ctx), r.db, input)
}

// EnsureTx is the caller-owned transaction form of Ensure.
func (r *Repository) EnsureTx(ctx context.Context, tx Tx, input EnsureInput) (Record, error) {
	if tx == nil {
		return Record{}, ErrInvalid
	}
	return ensure(contextOrBackground(ctx), tx, input)
}

// EnsureIdentity implements project.IdentityRepository. It creates the
// minimum row with title equal to the canonical ID and leaves existing
// authored metadata untouched, preserving the legacy module contract.
func (r *Repository) EnsureIdentity(ctx context.Context, id projectgraph.ResourceID) error {
	if r == nil || r.db == nil {
		return ErrInvalid
	}
	return ensureIdentity(contextOrBackground(ctx), r.db, id)
}

// EnsureIdentityTx is the transaction form of EnsureIdentity.
func (r *Repository) EnsureIdentityTx(ctx context.Context, tx Tx, id projectgraph.ResourceID) error {
	if tx == nil {
		return ErrInvalid
	}
	return ensureIdentity(contextOrBackground(ctx), tx, id)
}

// ByID loads one identity by canonical ID.
func (r *Repository) ByID(ctx context.Context, id projectgraph.ResourceID) (Record, error) {
	if r == nil || r.db == nil {
		return Record{}, ErrInvalid
	}
	validated, err := validateID(id)
	if err != nil {
		return Record{}, err
	}
	return load(contextOrBackground(ctx), r.db, validated)
}

// Get is an alias for ByID.
func (r *Repository) Get(ctx context.Context, id projectgraph.ResourceID) (Record, error) {
	return r.ByID(ctx, id)
}

// List returns all identities in stable creation/ID order.
func (r *Repository) List(ctx context.Context) ([]Record, error) {
	if r == nil || r.db == nil {
		return nil, ErrInvalid
	}
	rows, err := r.db.Query(contextOrBackground(ctx), `
		SELECT project_id, title, description, created_at, updated_at
		FROM project.project_identity
		ORDER BY created_at, project_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Record, 0)
	for rows.Next() {
		var row Record
		var rawID string
		if err := rows.Scan(&rawID, &row.Title, &row.Description, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, err
		}
		row.ID, err = projectgraph.NewResourceID(rawID)
		if err != nil {
			return nil, fmt.Errorf("stored project id: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func ensure(ctx context.Context, db DBTX, input EnsureInput) (Record, error) {
	id, title, description, err := normalizeInput(input)
	if err != nil {
		return Record{}, err
	}
	// ON CONFLICT DO NOTHING is intentionally not an upsert. PostgreSQL
	// serializes concurrent inserts on the primary key; the subsequent read
	// observes the winner and exact comparison turns divergent metadata into a
	// deterministic hard conflict.
	if _, err := db.Exec(ctx, `
		INSERT INTO project.project_identity(project_id, title, description)
		VALUES ($1, $2, $3)
		ON CONFLICT (project_id) DO NOTHING`, id.String(), title, description); err != nil {
		return Record{}, err
	}
	record, err := load(ctx, db, id)
	if err != nil {
		return Record{}, err
	}
	if record.Title != title || record.Description != description {
		return Record{}, fmt.Errorf("%w: authored metadata for %q differs", ErrConflict, id.String())
	}
	return record, nil
}

func ensureIdentity(ctx context.Context, db DBTX, raw projectgraph.ResourceID) error {
	id, err := validateID(raw)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `
		INSERT INTO project.project_identity(project_id, title, description)
		VALUES ($1, $1, '')
		ON CONFLICT (project_id) DO NOTHING`, id.String())
	return err
}

func load(ctx context.Context, db DBTX, id projectgraph.ResourceID) (Record, error) {
	var row Record
	var rawID string
	err := db.QueryRow(ctx, `
		SELECT project_id, title, description, created_at, updated_at
		FROM project.project_identity WHERE project_id = $1`, id.String()).
		Scan(&rawID, &row.Title, &row.Description, &row.CreatedAt, &row.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	row.ID, err = projectgraph.NewResourceID(rawID)
	if err != nil {
		return Record{}, fmt.Errorf("stored project id: %w", err)
	}
	return row, nil
}

func normalizeInput(input EnsureInput) (projectgraph.ResourceID, string, string, error) {
	id, err := validateID(input.ID)
	if err != nil {
		return "", "", "", err
	}
	title := input.Title
	if title == "" {
		title = id.String()
	}
	if title != strings.TrimSpace(title) {
		return "", "", "", fmt.Errorf("%w: title must be canonical (without surrounding whitespace)", ErrInvalid)
	}
	if title == "" || len([]byte(title)) > maxTitleBytes {
		return "", "", "", fmt.Errorf("%w: title must be 1-%d bytes", ErrInvalid, maxTitleBytes)
	}
	if len([]byte(input.Description)) > maxDescriptionBytes {
		return "", "", "", fmt.Errorf("%w: description must be at most %d bytes", ErrInvalid, maxDescriptionBytes)
	}
	return id, title, input.Description, nil
}

func validateID(id projectgraph.ResourceID) (projectgraph.ResourceID, error) {
	if id.String() != strings.TrimSpace(id.String()) || len([]byte(id.String())) > maxProjectIDBytes {
		return "", fmt.Errorf("%w: project id must be canonical and at most %d bytes", ErrInvalid, maxProjectIDBytes)
	}
	validated, err := projectgraph.NewResourceID(id.String())
	if err != nil {
		return "", fmt.Errorf("%w: project id: %v", ErrInvalid, err)
	}
	return validated, nil
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// Compile-time assertion that the PostgreSQL adapter satisfies the narrow
// engine-neutral module port.
var _ project.IdentityRepository = (*Repository)(nil)
