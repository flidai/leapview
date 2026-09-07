// Package postgres persists the mutable admin product identity on PostgreSQL.
// Logo bytes remain in the caller-owned blob store; this package stores only
// validated metadata and uses optimistic revision CAS for every mutation.
package postgres

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	product "github.com/flidai/leapview/internal/admin/product"
	productdb "github.com/flidai/leapview/internal/admin/product/postgres/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Tx = pgx.Tx

type Repository struct {
	db    DBTX
	audit AuditPort
}

var (
	ErrInvalid      = product.ErrInvalid
	ErrPrecondition = product.ErrPrecondition
)

//go:embed schema.sql
var schemaSQL string

func SchemaSQL() string { return schemaSQL }

func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return ErrInvalid
	}
	_, err := tx.Exec(ctx, schemaSQL) // sqlc-exception: schema-ddl
	return err
}

type Options struct{ Audit AuditPort }

// NewWithOptions constructs the production repository. The audit port is
// mandatory so no native PostgreSQL mutation can commit without its canonical
// Access audit row.
func NewWithOptions(db DBTX, options Options) (*Repository, error) {
	if db == nil {
		return nil, ErrInvalid
	}
	if options.Audit == nil {
		return nil, ErrAuditUnavailable
	}
	return &Repository{db: db, audit: options.Audit}, nil
}

// PostgreSQLAuthority marks this repository as the native product identity
// authority. Native admin composition uses the marker together with
// Configured so a generic product.Storage cannot silently select an
// unsupported persistence implementation.
func (*Repository) PostgreSQLAuthority() {}

// Configured reports whether the repository has a native PostgreSQL handle.
// Pool readiness and schema revision remain owned by the application
// lifecycle, so this is intentionally a shallow composition check.
func (r *Repository) Configured() bool { return r != nil && r.db != nil }

func (r *Repository) Ping(ctx context.Context) error {
	if r == nil || r.db == nil {
		return ErrInvalid
	}
	_, err := productdb.New(r.db).Ping(ctx)
	return err
}

// Mutate implements the product storage boundary. It obtains one native
// transaction, performs CAS and optional command concurrency checks, appends
// the audit intent through the configured strict port, then commits once.
func (r *Repository) Mutate(ctx context.Context, req product.MutationRequest) (product.Identity, error) {
	if r == nil || r.db == nil {
		return product.Identity{}, ErrInvalid
	}
	if r.audit == nil {
		return product.Identity{}, ErrAuditUnavailable
	}
	b, ok := r.db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		return product.Identity{}, ErrInvalid
	}
	if req.ExpectedRevision <= 0 {
		return product.Identity{}, product.ErrPrecondition
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return product.Identity{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := get(ctx, tx)
	if err != nil {
		return product.Identity{}, err
	}
	if req.CheckConcurrency != nil {
		if err := req.CheckConcurrency(ctx, current.Revision); err != nil {
			return product.Identity{}, err
		}
	}
	var rows int64
	switch req.Kind {
	case product.MutationDisplayName:
		rows, err = productdb.New(tx).UpdateDisplayName(ctx, productdb.UpdateDisplayNameParams{DisplayName: req.DisplayName, ExpectedRevision: req.ExpectedRevision})
	case product.MutationLogo:
		if req.Logo == nil || !validLogo(*req.Logo) {
			return product.Identity{}, ErrInvalid
		}
		rows, err = productdb.New(tx).UpdateLogo(ctx, productdb.UpdateLogoParams{LogoSha256: &req.Logo.SHA256, LogoMediaType: &req.Logo.MediaType, LogoSizeBytes: &req.Logo.SizeBytes, LogoWidth: int32Ptr(int32(req.Logo.Width)), LogoHeight: int32Ptr(int32(req.Logo.Height)), ExpectedRevision: req.ExpectedRevision})
	case product.MutationDeleteLogo:
		rows, err = productdb.New(tx).DeleteLogo(ctx, req.ExpectedRevision)
	case product.MutationReset:
		rows, err = productdb.New(tx).ResetIdentity(ctx, productdb.ResetIdentityParams{DisplayName: product.DefaultDisplayName, ExpectedRevision: req.ExpectedRevision})
	default:
		return product.Identity{}, ErrInvalid
	}
	if err != nil {
		return product.Identity{}, err
	}
	if rows != 1 {
		return product.Identity{}, product.ErrPrecondition
	}
	intent := AuditInput{EventID: product.AuditEventID(req.Mutation, req.Action, req.MetadataJSON, req.ExpectedRevision), PrincipalID: req.Mutation.PrincipalID, Source: "admin.product", Operation: req.Action, Action: req.Action, ResourceKind: "product", ResourceID: "instance", Capability: "RESOURCE_MANAGE", Outcome: "success", RequestID: req.Mutation.RequestID, CorrelationID: req.Mutation.CorrelationID, AggregateKey: "product:instance", AggregateSequence: req.ExpectedRevision, MetadataJSON: req.MetadataJSON}
	if err := r.audit.RecordAuditEvent(ctx, tx, intent); err != nil {
		return product.Identity{}, err
	}
	identity, err := get(ctx, tx)
	if err != nil {
		return product.Identity{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return product.Identity{}, err
	}
	return identity, nil
}

func (r *Repository) Get(ctx context.Context) (product.Identity, error) {
	if r == nil || r.db == nil {
		return product.Identity{}, ErrInvalid
	}
	return get(ctx, r.db)
}

func get(ctx context.Context, db DBTX) (product.Identity, error) {
	row, err := productdb.New(db).GetIdentity(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return product.Identity{}, product.ErrNotFound
	}
	if err != nil {
		return product.Identity{}, err
	}
	identity := product.Identity{DisplayName: row.DisplayName, Revision: row.Revision}
	if !row.UpdatedAt.Valid {
		return product.Identity{}, fmt.Errorf("stored product identity timestamp is invalid")
	}
	identity.UpdatedAt = row.UpdatedAt.Time.UTC().Format(time.RFC3339Nano)
	if row.LogoSha256 != nil {
		if row.LogoMediaType == nil || row.LogoSizeBytes == nil || row.LogoWidth == nil || row.LogoHeight == nil {
			return product.Identity{}, fmt.Errorf("stored product logo metadata is incomplete")
		}
		identity.Logo = &product.Logo{SHA256: *row.LogoSha256, MediaType: *row.LogoMediaType, SizeBytes: *row.LogoSizeBytes, Width: int(*row.LogoWidth), Height: int(*row.LogoHeight)}
	}
	return identity, nil
}

func validLogo(logo product.Logo) bool {
	if len(logo.SHA256) != 64 || strings.Trim(logo.SHA256, "0123456789abcdef") != "" || logo.SizeBytes <= 0 || logo.SizeBytes > product.MaxLogoBytes || logo.Width <= 0 || logo.Height <= 0 || int64(logo.Width) > math.MaxInt32 || int64(logo.Height) > math.MaxInt32 {
		return false
	}
	return logo.MediaType == "image/jpeg" || logo.MediaType == "image/png" || logo.MediaType == "image/webp"
}

func int32Ptr(value int32) *int32 { return &value }
