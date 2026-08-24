package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsdb "github.com/flidai/leapview/internal/analytics/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type ConnectionBindingRepository struct {
	db    *sql.DB
	q     *analyticsdb.Queries
	audit access.AuditIntentRecorder
}

var _ connectionbinding.Repository = (*ConnectionBindingRepository)(nil)
var _ connectionbinding.BindingCatalog = (*ConnectionBindingRepository)(nil)

func NewConnectionBindingRepository(database *sql.DB) *ConnectionBindingRepository {
	return &ConnectionBindingRepository{db: database, q: analyticsdb.New(database)}
}

// NewConnectionBindingRepositoryWithAudit wires binding mutations to the
// Access-owned durable audit-intent port. The recorder participates in the
// transaction opened here and never commits or rolls it back.
func NewConnectionBindingRepositoryWithAudit(database *sql.DB, audit access.AuditIntentRecorder) *ConnectionBindingRepository {
	return &ConnectionBindingRepository{db: database, q: analyticsdb.New(database), audit: audit}
}

func (repository *ConnectionBindingRepository) Create(ctx context.Context, binding connectionbinding.TargetBinding) error {
	if repository == nil || repository.q == nil || binding.Revision != 1 {
		return fmt.Errorf("%w: repository and new binding are required", connectionbinding.ErrInvalidBinding)
	}
	if err := binding.Validate(); err != nil {
		return err
	}
	endpoint, err := json.Marshal(binding.Endpoint)
	if err != nil {
		return fmt.Errorf("encode non-secret endpoint: %w", err)
	}
	params := analyticsdb.CreateTargetConnectionBindingParams{
		ID: binding.ID.String(), TargetID: binding.TargetID.String(), ConnectionID: binding.ConnectionID.String(),
		ConnectorKind: binding.ConnectorKind, AuthenticationMode: string(binding.AuthenticationMode),
		ProjectID: binding.Scope.ProjectID.String(), Environment: binding.Scope.Environment, EndpointJson: string(endpoint),
		CredentialProjectID: binding.CredentialReference.ProjectID.String(), CredentialEnvironment: binding.CredentialReference.Environment,
		CredentialSecretPath: binding.CredentialReference.SecretPath, CredentialSecretKey: binding.CredentialReference.SecretKey,
		Enabled: boolInt(binding.Enabled), ValidatedVersion: binding.ValidatedVersion, Health: string(binding.Health),
		HealthReason: binding.HealthReason, LastValidatedAt: nullableTime(binding.LastValidatedAt),
		CreatedAt: sqliteTime(binding.CreatedAt), UpdatedAt: sqliteTime(binding.UpdatedAt), Revision: binding.Revision,
	}
	if intent, ok := connectionbinding.AuditIntentFromContext(ctx); ok {
		if err := repository.withAuditTransaction(ctx, func(tx *sql.Tx) error {
			if err := repository.q.WithTx(tx).CreateTargetConnectionBinding(ctx, params); err != nil {
				return err
			}
			return repository.recordAuditIntent(ctx, tx, intent)
		}); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "constraint") {
				return fmt.Errorf("%w: target connection scope already has a binding", connectionbinding.ErrIncompatibleBinding)
			}
			return err
		}
		return nil
	}
	err = repository.q.CreateTargetConnectionBinding(ctx, params)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "constraint") {
		return fmt.Errorf("%w: target connection scope already has a binding", connectionbinding.ErrIncompatibleBinding)
	}
	return err
}

func (repository *ConnectionBindingRepository) Binding(
	ctx context.Context,
	scope connectionbinding.BindingScope,
	targetID connectionbinding.TargetID,
	connectionID projectgraph.ResourceID,
) (connectionbinding.TargetBinding, error) {
	if repository == nil || repository.q == nil {
		return connectionbinding.TargetBinding{}, connectionbinding.ErrBindingNotFound
	}
	row, err := repository.q.GetTargetConnectionBinding(ctx, analyticsdb.GetTargetConnectionBindingParams{
		TargetID: targetID.String(), ProjectID: scope.ProjectID.String(),
		Environment: scope.Environment, ConnectionID: connectionID.String(),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return connectionbinding.TargetBinding{}, connectionbinding.ErrBindingNotFound
	}
	if err != nil {
		return connectionbinding.TargetBinding{}, err
	}
	return bindingFromDB(row)
}

func (repository *ConnectionBindingRepository) List(
	ctx context.Context,
	scope connectionbinding.BindingScope,
	targetID connectionbinding.TargetID,
) ([]connectionbinding.TargetBinding, error) {
	if repository == nil || repository.q == nil {
		return nil, connectionbinding.ErrBindingNotFound
	}
	rows, err := repository.q.ListTargetConnectionBindings(ctx, analyticsdb.ListTargetConnectionBindingsParams{
		TargetID: targetID.String(), ProjectID: scope.ProjectID.String(), Environment: scope.Environment,
	})
	if err != nil {
		return nil, err
	}
	bindings := make([]connectionbinding.TargetBinding, 0, len(rows))
	for _, row := range rows {
		binding, err := bindingFromDB(row)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, nil
}

func (repository *ConnectionBindingRepository) Save(
	ctx context.Context,
	binding connectionbinding.TargetBinding,
	expectedRevision int64,
) (connectionbinding.TargetBinding, error) {
	if repository == nil || repository.q == nil || expectedRevision <= 0 || binding.Revision != expectedRevision+1 {
		return connectionbinding.TargetBinding{}, fmt.Errorf("%w: invalid binding revision", connectionbinding.ErrIncompatibleBinding)
	}
	if err := binding.Validate(); err != nil {
		return connectionbinding.TargetBinding{}, err
	}
	endpoint, err := json.Marshal(binding.Endpoint)
	if err != nil {
		return connectionbinding.TargetBinding{}, fmt.Errorf("encode non-secret endpoint: %w", err)
	}
	params := analyticsdb.UpdateTargetConnectionBindingParams{
		EndpointJson: string(endpoint), CredentialProjectID: binding.CredentialReference.ProjectID.String(),
		CredentialEnvironment: binding.CredentialReference.Environment,
		CredentialSecretPath:  binding.CredentialReference.SecretPath, CredentialSecretKey: binding.CredentialReference.SecretKey,
		Enabled: boolInt(binding.Enabled), ValidatedVersion: binding.ValidatedVersion, Health: string(binding.Health),
		HealthReason: binding.HealthReason, LastValidatedAt: nullableTime(binding.LastValidatedAt),
		UpdatedAt: sqliteTime(binding.UpdatedAt), Revision: binding.Revision, ID: binding.ID.String(), Revision_2: expectedRevision,
		TargetID: binding.TargetID.String(), ConnectionID: binding.ConnectionID.String(),
		ConnectorKind: binding.ConnectorKind, AuthenticationMode: string(binding.AuthenticationMode),
		ProjectID: binding.Scope.ProjectID.String(), Environment: binding.Scope.Environment,
	}
	if intent, ok := connectionbinding.AuditIntentFromContext(ctx); ok {
		if err := repository.withAuditTransaction(ctx, func(tx *sql.Tx) error {
			count, err := repository.q.WithTx(tx).UpdateTargetConnectionBinding(ctx, params)
			if err != nil {
				return err
			}
			if count != 1 {
				return connectionbinding.ErrIncompatibleBinding
			}
			return repository.recordAuditIntent(ctx, tx, intent)
		}); err != nil {
			return connectionbinding.TargetBinding{}, err
		}
		return binding, nil
	}
	count, err := repository.q.UpdateTargetConnectionBinding(ctx, params)
	if err != nil {
		return connectionbinding.TargetBinding{}, err
	}
	if count != 1 {
		return connectionbinding.TargetBinding{}, connectionbinding.ErrIncompatibleBinding
	}
	return binding, nil
}

func (repository *ConnectionBindingRepository) withAuditTransaction(ctx context.Context, fn func(*sql.Tx) error) error {
	if repository == nil || repository.db == nil {
		return fmt.Errorf("connection binding audit transaction database is required")
	}
	if repository.audit == nil {
		return fmt.Errorf("connection binding audit intent recorder is required")
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (repository *ConnectionBindingRepository) recordAuditIntent(ctx context.Context, tx *sql.Tx, intent access.AuditIntent) error {
	if repository.audit == nil {
		return fmt.Errorf("connection binding audit intent recorder is required")
	}
	return repository.audit.RecordAuditIntent(ctx, tx, intent)
}

func bindingFromDB(row analyticsdb.TargetConnectionBinding) (connectionbinding.TargetBinding, error) {
	connectionID, err := connectionbinding.ParseConnectionID(row.ConnectionID)
	if err != nil {
		return connectionbinding.TargetBinding{}, err
	}
	var endpoint connectionbinding.EndpointConfig
	if err := json.Unmarshal([]byte(row.EndpointJson), &endpoint); err != nil {
		return connectionbinding.TargetBinding{}, fmt.Errorf("decode non-secret endpoint: %w", err)
	}
	createdAt, err := parseTime(row.CreatedAt)
	if err != nil {
		return connectionbinding.TargetBinding{}, fmt.Errorf("parse binding creation: %w", err)
	}
	updatedAt, err := parseTime(row.UpdatedAt)
	if err != nil {
		return connectionbinding.TargetBinding{}, fmt.Errorf("parse binding update: %w", err)
	}
	lastValidatedAt, err := parseNullableTime(row.LastValidatedAt)
	if err != nil {
		return connectionbinding.TargetBinding{}, fmt.Errorf("parse binding validation: %w", err)
	}
	binding := connectionbinding.TargetBinding{
		ID: connectionbinding.BindingID(row.ID), TargetID: connectionbinding.TargetID(row.TargetID), ConnectionID: connectionID, ConnectorKind: row.ConnectorKind,
		AuthenticationMode: connectionbinding.AuthenticationMode(row.AuthenticationMode),
		Scope:              connectionbinding.BindingScope{ProjectID: projectgraph.ResourceID(row.ProjectID), Environment: row.Environment},
		Endpoint:           endpoint, CredentialReference: connectionbinding.CredentialReference{
			ProjectID: projectgraph.ResourceID(row.CredentialProjectID), Environment: row.CredentialEnvironment,
			SecretPath: row.CredentialSecretPath, SecretKey: row.CredentialSecretKey,
		},
		Enabled: row.Enabled == 1, ValidatedVersion: row.ValidatedVersion,
		Health: connectionbinding.BindingHealth(row.Health), HealthReason: row.HealthReason,
		LastValidatedAt: lastValidatedAt, CreatedAt: createdAt, UpdatedAt: updatedAt, Revision: row.Revision,
	}
	if err := binding.Validate(); err != nil {
		return connectionbinding.TargetBinding{}, fmt.Errorf("validate persisted binding: %w", err)
	}
	return binding, nil
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func sqliteTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func nullableTime(value time.Time) sql.NullString {
	if value.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: sqliteTime(value), Valid: true}
}

func parseTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}

func parseNullableTime(value sql.NullString) (time.Time, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return time.Time{}, nil
	}
	return parseTime(value.String)
}
