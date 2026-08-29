// Package postgres implements the clean-slate PostgreSQL connection-binding
// authority. It accepts only native pgx surfaces; no database/sql adapter or
// process environment is consulted.
package postgres

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	bindingdb "github.com/flidai/leapview/internal/analytics/connectionbinding/postgres/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// DBTX is the native pgx query surface implemented by pools, connections,
// and caller-owned transactions.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Tx is a caller-owned pgx transaction. Methods accepting Tx never commit or
// roll back it; the caller owns the complete transaction boundary.
type Tx interface {
	DBTX
	Commit(context.Context) error
	Rollback(context.Context) error
}

// AuditRepository is the Access-owned direct audit append boundary. It is
// deliberately transaction-shaped: implementations must append through the
// exact pgx transaction supplied by this capability.
type AuditRepository interface {
	RecordAuditEvent(context.Context, Tx, access.AuditIntent) error
}

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// Repository persists target-scoped connection metadata and revisioned health
// evidence. Secret values are represented only by credential references.
type Repository struct {
	db           DBTX
	begin        beginner
	audit        AuditRepository
	requireAudit bool
}

var _ connectionbinding.Repository = (*Repository)(nil)
var _ connectionbinding.BindingCatalog = (*Repository)(nil)
var _ Tx = (pgx.Tx)(nil)

//go:embed schema.sql
var schemaFS embed.FS

var schemaSQL = func() string {
	b, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		panic(err)
	}
	return string(b)
}()

// SchemaSQL returns the capability-owned forward schema.
func SchemaSQL() string { return schemaSQL }

// ApplySchema executes the capability schema in a caller-owned transaction.
func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return errors.New("connection-binding PostgreSQL transaction is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// sqlc-exception: schema-ddl. The migration runner owns transaction
	// boundaries while this capability owns its DDL and role policy.
	_, err := tx.Exec(ctx, schemaSQL)
	return err
}

// New constructs a repository over a pgx pool, connection, or transaction.
func New(db DBTX) *Repository { return &Repository{db: db} }

// NewRepository is an expressive constructor alias.
func NewRepository(db DBTX) *Repository { return New(db) }

// NewWithAudit constructs the production authority. The supplied database
// must expose Begin and the Access audit repository is required so every
// mutation carrying an audit intent commits both rows in one pgx transaction.
func NewWithAudit(db DBTX, audit AuditRepository) (*Repository, error) {
	if db == nil {
		return nil, errors.New("connection-binding PostgreSQL database is required")
	}
	begin, ok := db.(beginner)
	if !ok {
		return nil, errors.New("connection-binding PostgreSQL database must support Begin")
	}
	if audit == nil {
		return nil, errors.New("connection-binding PostgreSQL audit repository is required")
	}
	return &Repository{db: db, begin: begin, audit: audit, requireAudit: true}, nil
}

// NewProduction is an explicit alias for NewWithAudit.
func NewProduction(db DBTX, audit AuditRepository) (*Repository, error) {
	return NewWithAudit(db, audit)
}

// AuditCapable reports whether this repository is configured for production
// same-transaction audit persistence.
func (r *Repository) AuditCapable() bool {
	return r != nil && r.requireAudit && r.begin != nil && r.audit != nil
}

// WithTx returns a repository bound to the caller-owned transaction while
// preserving production audit requirements.
func (r *Repository) WithTx(tx Tx) *Repository {
	if r == nil {
		return New(tx)
	}
	return &Repository{db: tx, audit: r.audit, requireAudit: r.requireAudit}
}

// PostgreSQLAuthority marks this repository as the native production binding
// authority. Analytics module composition uses the marker when a generic
// BindingCatalog is injected so a SQLite adapter cannot be selected by
// accident in production.
func (*Repository) PostgreSQLAuthority() {}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func invalidRepository() error {
	return fmt.Errorf("%w: PostgreSQL connection-binding repository is unavailable", connectionbinding.ErrProviderUnavailable)
}

// Create persists a new revision-one binding. Duplicate identities and target
// scope collisions are normalized to ErrIncompatibleBinding.
func (r *Repository) Create(ctx context.Context, binding connectionbinding.TargetBinding) error {
	if r == nil || r.db == nil {
		return invalidRepository()
	}
	ctx = contextOrBackground(ctx)
	if err := r.validateAuditContext(ctx); err != nil {
		return err
	}
	if _, ok := connectionbinding.AuditIntentFromContext(ctx); ok {
		if r.begin == nil {
			return r.createTx(ctx, r.db, binding)
		}
		tx, err := r.begin.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if err := r.createTx(ctx, tx, binding); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	return r.createTx(ctx, r.db, binding)
}

// CreateTx performs Create on a caller-owned pgx transaction. It never
// commits or rolls back tx. The transaction-shaped API is intentional: an
// audited mutation must never be allowed to run against an auto-committing
// pool or connection.
func (r *Repository) CreateTx(ctx context.Context, tx Tx, binding connectionbinding.TargetBinding) error {
	if r == nil || r.db == nil || tx == nil {
		return invalidRepository()
	}
	if err := r.validateAuditContext(contextOrBackground(ctx)); err != nil {
		return err
	}
	return r.createTx(contextOrBackground(ctx), tx, binding)
}

func (r *Repository) createTx(ctx context.Context, tx DBTX, binding connectionbinding.TargetBinding) error {
	if r == nil || r.db == nil || tx == nil {
		return invalidRepository()
	}
	if err := r.validateAuditTransaction(contextOrBackground(ctx), tx); err != nil {
		return err
	}
	if binding.Revision != 1 {
		return fmt.Errorf("%w: new binding revision must be one", connectionbinding.ErrInvalidBinding)
	}
	if err := binding.Validate(); err != nil {
		return err
	}
	params, err := createParams(binding)
	if err != nil {
		return err
	}
	rows, err := bindingdb.New(tx).CreateTargetConnectionBinding(contextOrBackground(ctx), params)
	if err != nil {
		return normalizeDatabaseError(err)
	}
	if rows != 1 {
		return fmt.Errorf("%w: target connection scope or binding identity already exists", connectionbinding.ErrIncompatibleBinding)
	}
	return r.recordAuditIntent(contextOrBackground(ctx), tx)
}

func createParams(binding connectionbinding.TargetBinding) (bindingdb.CreateTargetConnectionBindingParams, error) {
	endpoint, err := json.Marshal(binding.Endpoint)
	if err != nil {
		return bindingdb.CreateTargetConnectionBindingParams{}, fmt.Errorf("encode non-secret endpoint: %w", err)
	}
	return bindingdb.CreateTargetConnectionBindingParams{
		ID: binding.ID.String(), TargetID: binding.TargetID.String(), ConnectionID: binding.ConnectionID.String(),
		ConnectorKind: binding.ConnectorKind, AuthenticationMode: string(binding.AuthenticationMode),
		ProjectID: binding.Scope.ProjectID.String(), Environment: binding.Scope.Environment, EndpointJson: endpoint,
		CredentialProjectID: binding.CredentialReference.ProjectID.String(), CredentialEnvironment: binding.CredentialReference.Environment,
		CredentialSecretPath: binding.CredentialReference.SecretPath, CredentialSecretKey: binding.CredentialReference.SecretKey,
		Enabled: binding.Enabled, ValidatedVersion: binding.ValidatedVersion, Health: string(binding.Health),
		HealthReason: binding.HealthReason, LastValidatedAt: nullableTimestamp(binding.LastValidatedAt),
		CreatedAt: timestamp(binding.CreatedAt), UpdatedAt: timestamp(binding.UpdatedAt), Revision: binding.Revision,
	}, nil
}

// Binding loads one binding in an exact target, project, environment scope.
func (r *Repository) Binding(ctx context.Context, scope connectionbinding.BindingScope, targetID connectionbinding.TargetID, connectionID projectgraph.ResourceID) (connectionbinding.TargetBinding, error) {
	if r == nil || r.db == nil {
		return connectionbinding.TargetBinding{}, connectionbinding.ErrBindingNotFound
	}
	row, err := bindingdb.New(r.db).GetTargetConnectionBinding(contextOrBackground(ctx), bindingdb.GetTargetConnectionBindingParams{
		TargetID: targetID.String(), ProjectID: scope.ProjectID.String(), Environment: scope.Environment, ConnectionID: connectionID.String(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return connectionbinding.TargetBinding{}, connectionbinding.ErrBindingNotFound
	}
	if err != nil {
		return connectionbinding.TargetBinding{}, err
	}
	return bindingFromRow(row)
}

// List returns bindings in one exact target/project/environment scope ordered
// by logical connection identity. An empty scope is a valid empty result.
func (r *Repository) List(ctx context.Context, scope connectionbinding.BindingScope, targetID connectionbinding.TargetID) ([]connectionbinding.TargetBinding, error) {
	if r == nil || r.db == nil {
		return nil, connectionbinding.ErrBindingNotFound
	}
	rows, err := bindingdb.New(r.db).ListTargetConnectionBindings(contextOrBackground(ctx), bindingdb.ListTargetConnectionBindingsParams{
		TargetID: targetID.String(), ProjectID: scope.ProjectID.String(), Environment: scope.Environment,
	})
	if err != nil {
		return nil, err
	}
	result := make([]connectionbinding.TargetBinding, 0, len(rows))
	for _, row := range rows {
		binding, err := bindingFromRow(row)
		if err != nil {
			return nil, err
		}
		result = append(result, binding)
	}
	return result, nil
}

// Save applies one optimistic revision transition. The expected revision and
// every immutable identity field are predicates in the generated query, so a
// concurrent writer can never overwrite a newer binding.
func (r *Repository) Save(ctx context.Context, binding connectionbinding.TargetBinding, expectedRevision int64) (connectionbinding.TargetBinding, error) {
	if r == nil || r.db == nil {
		return connectionbinding.TargetBinding{}, invalidRepository()
	}
	ctx = contextOrBackground(ctx)
	if err := r.validateAuditContext(ctx); err != nil {
		return connectionbinding.TargetBinding{}, err
	}
	if _, ok := connectionbinding.AuditIntentFromContext(ctx); ok {
		if r.begin == nil {
			return r.saveTx(ctx, r.db, binding, expectedRevision)
		}
		tx, err := r.begin.Begin(ctx)
		if err != nil {
			return connectionbinding.TargetBinding{}, err
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		result, err := r.saveTx(ctx, tx, binding, expectedRevision)
		if err != nil {
			return connectionbinding.TargetBinding{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return connectionbinding.TargetBinding{}, err
		}
		return result, nil
	}
	return r.saveTx(ctx, r.db, binding, expectedRevision)
}

// SaveTx performs Save on a caller-owned pgx transaction. The transaction-
// shaped API prevents audited mutations from accepting a pool or connection.
func (r *Repository) SaveTx(ctx context.Context, tx Tx, binding connectionbinding.TargetBinding, expectedRevision int64) (connectionbinding.TargetBinding, error) {
	if r == nil || r.db == nil || tx == nil {
		return connectionbinding.TargetBinding{}, invalidRepository()
	}
	if err := r.validateAuditContext(contextOrBackground(ctx)); err != nil {
		return connectionbinding.TargetBinding{}, err
	}
	return r.saveTx(contextOrBackground(ctx), tx, binding, expectedRevision)
}

func (r *Repository) saveTx(ctx context.Context, tx DBTX, binding connectionbinding.TargetBinding, expectedRevision int64) (connectionbinding.TargetBinding, error) {
	if r == nil || r.db == nil || tx == nil {
		return connectionbinding.TargetBinding{}, invalidRepository()
	}
	if err := r.validateAuditTransaction(contextOrBackground(ctx), tx); err != nil {
		return connectionbinding.TargetBinding{}, err
	}
	if expectedRevision <= 0 || binding.Revision != expectedRevision+1 {
		return connectionbinding.TargetBinding{}, fmt.Errorf("%w: invalid binding revision", connectionbinding.ErrIncompatibleBinding)
	}
	if err := binding.Validate(); err != nil {
		return connectionbinding.TargetBinding{}, err
	}
	endpoint, err := json.Marshal(binding.Endpoint)
	if err != nil {
		return connectionbinding.TargetBinding{}, fmt.Errorf("encode non-secret endpoint: %w", err)
	}
	rows, err := bindingdb.New(tx).UpdateTargetConnectionBinding(contextOrBackground(ctx), bindingdb.UpdateTargetConnectionBindingParams{
		EndpointJson: endpoint, CredentialProjectID: binding.CredentialReference.ProjectID.String(),
		CredentialEnvironment: binding.CredentialReference.Environment, CredentialSecretPath: binding.CredentialReference.SecretPath,
		CredentialSecretKey: binding.CredentialReference.SecretKey, Enabled: binding.Enabled, ValidatedVersion: binding.ValidatedVersion,
		Health: string(binding.Health), HealthReason: binding.HealthReason, LastValidatedAt: nullableTimestamp(binding.LastValidatedAt),
		UpdatedAt: timestamp(binding.UpdatedAt), Revision: binding.Revision, ID: binding.ID.String(), ExpectedRevision: expectedRevision,
		TargetID: binding.TargetID.String(), ConnectionID: binding.ConnectionID.String(), ConnectorKind: binding.ConnectorKind,
		AuthenticationMode: string(binding.AuthenticationMode), ProjectID: binding.Scope.ProjectID.String(), Environment: binding.Scope.Environment,
	})
	if err != nil {
		return connectionbinding.TargetBinding{}, normalizeDatabaseError(err)
	}
	if rows != 1 {
		return connectionbinding.TargetBinding{}, connectionbinding.ErrIncompatibleBinding
	}
	if err := r.recordAuditIntent(contextOrBackground(ctx), tx); err != nil {
		return connectionbinding.TargetBinding{}, err
	}
	return binding, nil
}

func (r *Repository) validateAuditContext(ctx context.Context) error {
	_, hasIntent := connectionbinding.AuditIntentFromContext(ctx)
	if !hasIntent {
		return nil
	}
	if r == nil || r.audit == nil {
		return connectionbinding.ErrAdministrationAuditUnavailable
	}
	return nil
}

// validateAuditTransaction is the pre-SQL guard for the private mutation
// paths. Create and Save intentionally retain an unaudited DBTX path for
// internal pool-backed health rotation, but an audit intent requires the
// exact caller-owned transaction so the binding and audit event share a
// commit boundary.
func (r *Repository) validateAuditTransaction(ctx context.Context, tx DBTX) error {
	if _, hasIntent := connectionbinding.AuditIntentFromContext(ctx); !hasIntent {
		return nil
	}
	if r == nil || r.audit == nil {
		return connectionbinding.ErrAdministrationAuditUnavailable
	}
	if _, ok := tx.(Tx); !ok {
		return connectionbinding.ErrAdministrationAuditUnavailable
	}
	return nil
}

func (r *Repository) recordAuditIntent(ctx context.Context, tx DBTX) error {
	intent, ok := connectionbinding.AuditIntentFromContext(ctx)
	if !ok {
		return nil
	}
	auditTx, ok := tx.(Tx)
	if !ok || r == nil || r.audit == nil {
		return connectionbinding.ErrAdministrationAuditUnavailable
	}
	// The connection-binding domain uses the public "succeeded" outcome while
	// Access's PostgreSQL audit vocabulary stores the canonical "success"
	// value. Preserve every other intent field exactly for digest/replay checks.
	if intent.Outcome == "succeeded" {
		intent.Outcome = "success"
	}
	// Access PostgreSQL canonicalizes and validates the intent, then appends it
	// through this exact pgx transaction. It never commits or rolls back tx.
	return r.audit.RecordAuditEvent(ctx, auditTx, intent)
}

func normalizeDatabaseError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return fmt.Errorf("%w: target connection scope or binding identity already exists", connectionbinding.ErrIncompatibleBinding)
		case "23514", "23502", "22P02":
			return fmt.Errorf("%w: persisted binding violates PostgreSQL constraints", connectionbinding.ErrInvalidBinding)
		}
	}
	return err
}

func timestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}

func nullableTimestamp(value time.Time) pgtype.Timestamptz {
	if value.IsZero() {
		return pgtype.Timestamptz{}
	}
	return timestamp(value)
}

func bindingFromRow(row bindingdb.ConnectionBindingTargetConnectionBinding) (connectionbinding.TargetBinding, error) {
	connectionID, err := connectionbinding.ParseConnectionID(row.ConnectionID)
	if err != nil {
		return connectionbinding.TargetBinding{}, err
	}
	var endpoint connectionbinding.EndpointConfig
	if err := json.Unmarshal(row.EndpointJson, &endpoint); err != nil {
		return connectionbinding.TargetBinding{}, fmt.Errorf("decode non-secret endpoint: %w", err)
	}
	if !row.CreatedAt.Valid || !row.UpdatedAt.Valid {
		return connectionbinding.TargetBinding{}, fmt.Errorf("%w: persisted binding timestamps are null", connectionbinding.ErrInvalidBinding)
	}
	lastValidatedAt := time.Time{}
	if row.LastValidatedAt.Valid {
		lastValidatedAt = row.LastValidatedAt.Time.UTC()
	}
	binding := connectionbinding.TargetBinding{
		ID: connectionbinding.BindingID(row.ID), TargetID: connectionbinding.TargetID(row.TargetID), ConnectionID: connectionID,
		ConnectorKind: row.ConnectorKind, AuthenticationMode: connectionbinding.AuthenticationMode(row.AuthenticationMode),
		Scope: connectionbinding.BindingScope{ProjectID: projectgraph.ResourceID(row.ProjectID), Environment: row.Environment}, Endpoint: endpoint,
		CredentialReference: connectionbinding.CredentialReference{ProjectID: projectgraph.ResourceID(row.CredentialProjectID), Environment: row.CredentialEnvironment, SecretPath: row.CredentialSecretPath, SecretKey: row.CredentialSecretKey},
		Enabled:             row.Enabled, ValidatedVersion: row.ValidatedVersion, Health: connectionbinding.BindingHealth(row.Health), HealthReason: row.HealthReason,
		LastValidatedAt: lastValidatedAt, CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(), Revision: row.Revision,
	}
	if err := binding.Validate(); err != nil {
		return connectionbinding.TargetBinding{}, fmt.Errorf("validate persisted binding: %w", err)
	}
	return binding, nil
}
