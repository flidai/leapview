package product

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// MutationKind identifies the small set of product writes. Storage
// implementations own the transaction and CAS details; the service owns
// validation, command invocation, and blob handling.
type MutationKind string

const (
	MutationDisplayName MutationKind = "display_name"
	MutationLogo        MutationKind = "logo"
	MutationDeleteLogo  MutationKind = "delete_logo"
	MutationReset       MutationKind = "reset"
)

type MutationRequest struct {
	ExpectedRevision int64
	Mutation         Mutation
	Action           string
	MetadataJSON     string
	Kind             MutationKind
	DisplayName      string
	Logo             *Logo
	CheckConcurrency func(context.Context, int64) error
}

// Storage is the product-owned persistence boundary. Implementations may be
// SQLite (legacy) or native PostgreSQL; callers never reach into either
// database directly.
type Storage interface {
	Get(context.Context) (Identity, error)
	Ping(context.Context) error
	Mutate(context.Context, MutationRequest) (Identity, error)
}

// AuditEventID derives an RFC 9562 UUIDv5 from immutable mutation identity.
// It is exported for native storage adapters that append the audit row.
func AuditEventID(m Mutation, action, metadata string, expectedRevision int64) string {
	seed := strings.Join([]string{m.PrincipalID, m.RequestID, m.CorrelationID, action, metadata, fmt.Sprint(expectedRevision)}, "\x00")
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(seed)).String()
}

type sqliteStorage struct{ db *sql.DB }

func newSQLiteStorage(db *sql.DB) *sqliteStorage { return &sqliteStorage{db: db} }

func (s *sqliteStorage) Get(ctx context.Context) (Identity, error) {
	if s == nil || s.db == nil {
		return Identity{}, ErrInvalid
	}
	return scanIdentity(s.db.QueryRowContext(ctx, `
SELECT display_name, logo_sha256, logo_media_type, logo_size_bytes, logo_width, logo_height, revision, updated_at
FROM product_identity WHERE singleton = 1`))
}

func (s *sqliteStorage) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrInvalid
	}
	return s.db.PingContext(ctx)
}

func (s *sqliteStorage) Mutate(ctx context.Context, req MutationRequest) (Identity, error) {
	if s == nil || s.db == nil {
		return Identity{}, ErrInvalid
	}
	if req.ExpectedRevision <= 0 {
		return Identity{}, ErrPrecondition
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Identity{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM product_identity WHERE singleton = 1`).Scan(&current); err != nil {
		return Identity{}, err
	}
	if req.CheckConcurrency != nil {
		if err := req.CheckConcurrency(ctx, current); err != nil {
			return Identity{}, err
		}
	}
	var result sql.Result
	switch req.Kind {
	case MutationDisplayName:
		result, err = tx.ExecContext(ctx, `UPDATE product_identity SET display_name = ?, revision = revision + 1, updated_at = CURRENT_TIMESTAMP WHERE singleton = 1 AND revision = ?`, req.DisplayName, req.ExpectedRevision)
	case MutationLogo:
		if req.Logo == nil {
			return Identity{}, ErrInvalid
		}
		result, err = tx.ExecContext(ctx, `UPDATE product_identity SET logo_sha256 = ?, logo_media_type = ?, logo_size_bytes = ?, logo_width = ?, logo_height = ?, revision = revision + 1, updated_at = CURRENT_TIMESTAMP WHERE singleton = 1 AND revision = ?`, req.Logo.SHA256, req.Logo.MediaType, req.Logo.SizeBytes, req.Logo.Width, req.Logo.Height, req.ExpectedRevision)
	case MutationDeleteLogo:
		result, err = tx.ExecContext(ctx, `UPDATE product_identity SET logo_sha256 = NULL, logo_media_type = NULL, logo_size_bytes = NULL, logo_width = NULL, logo_height = NULL, revision = revision + 1, updated_at = CURRENT_TIMESTAMP WHERE singleton = 1 AND revision = ? AND logo_sha256 IS NOT NULL`, req.ExpectedRevision)
	case MutationReset:
		result, err = tx.ExecContext(ctx, `UPDATE product_identity SET display_name = ?, logo_sha256 = NULL, logo_media_type = NULL, logo_size_bytes = NULL, logo_width = NULL, logo_height = NULL, revision = revision + 1, updated_at = CURRENT_TIMESTAMP WHERE singleton = 1 AND revision = ?`, DefaultDisplayName, req.ExpectedRevision)
	default:
		return Identity{}, ErrInvalid
	}
	if err != nil {
		return Identity{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Identity{}, err
	}
	if rows != 1 {
		return Identity{}, ErrPrecondition
	}
	if err := insertAudit(ctx, tx, req.Mutation, req.Action, req.MetadataJSON, req.ExpectedRevision); err != nil {
		return Identity{}, fmt.Errorf("audit product mutation: %w", err)
	}
	identity, err := scanIdentity(tx.QueryRowContext(ctx, `SELECT display_name, logo_sha256, logo_media_type, logo_size_bytes, logo_width, logo_height, revision, updated_at FROM product_identity WHERE singleton = 1`))
	if err != nil {
		return Identity{}, err
	}
	if err := tx.Commit(); err != nil {
		return Identity{}, err
	}
	return identity, nil
}

func insertAudit(ctx context.Context, tx *sql.Tx, mutation Mutation, action, metadataJSON string, expectedRevision int64) error {
	seed := AuditEventID(mutation, action, metadataJSON, expectedRevision)
	_, err := tx.ExecContext(ctx, `
INSERT INTO audit_events
  (id, principal_id, action, resource_kind, resource_id, capability, status, request_id, correlation_id, metadata_json)
VALUES (?, NULLIF(?, ''), ?, 'product', 'instance', 'RESOURCE_MANAGE', 'success', ?, ?, ?)
ON CONFLICT(id) DO NOTHING`, seed, strings.TrimSpace(mutation.PrincipalID), action,
		strings.TrimSpace(mutation.RequestID), strings.TrimSpace(mutation.CorrelationID), metadataJSON)
	return err
}

var _ Storage = (*sqliteStorage)(nil)
