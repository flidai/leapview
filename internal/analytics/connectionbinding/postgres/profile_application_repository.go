package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	profiledb "github.com/flidai/leapview/internal/analytics/connectionbinding/postgres/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// ProfileApplicationRepository is the durable PostgreSQL implementation of
// connectionbinding.ProfileApplicationStore. It is separate from Repository
// because the existing binding authority already exposes a Save method for a
// different domain type.
type ProfileApplicationRepository struct{ db DBTX }

var _ connectionbinding.ProfileApplicationStore = (*ProfileApplicationRepository)(nil)

// NewProfileApplicationRepository constructs a profile-application store over
// a pgx pool, connection, or caller-owned transaction.
func NewProfileApplicationRepository(db DBTX) *ProfileApplicationRepository {
	return &ProfileApplicationRepository{db: db}
}

// NewProfileApplicationStore is the explicit store-named constructor used by
// composition code that depends only on the narrow domain interface.
func NewProfileApplicationStore(db DBTX) *ProfileApplicationRepository {
	return NewProfileApplicationRepository(db)
}

// Application loads the one checkpoint for an exact checkout/runtime/target
// and project/environment scope. Required intent and applied evidence are
// reconstructed from their separate durable child tables.
func (r *ProfileApplicationRepository) Application(ctx context.Context, scope connectionbinding.ProfileApplicationScope, targetID connectionbinding.TargetID) (connectionbinding.ProfileApplicationRecord, error) {
	if r == nil || r.db == nil || ctx == nil {
		return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrProfileApplicationNotFound
	}
	if err := ctx.Err(); err != nil {
		return connectionbinding.ProfileApplicationRecord{}, err
	}
	if _, err := connectionbinding.ParseTargetID(targetID.String()); err != nil || validateProfileApplicationScope(scope) != nil {
		return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrProfileApplicationNotFound
	}
	row, err := profiledb.New(r.db).GetProfileApplication(ctx, profileApplicationLookupParams(scope, targetID))
	if errors.Is(err, pgx.ErrNoRows) {
		return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrProfileApplicationNotFound
	}
	if err != nil {
		return connectionbinding.ProfileApplicationRecord{}, normalizeProfileApplicationDatabaseError(err)
	}
	record, err := loadProfileApplication(ctx, r.db, row)
	if err != nil {
		return connectionbinding.ProfileApplicationRecord{}, err
	}
	return record, nil
}

// Save persists one checkpoint using a transaction-owned compare-and-swap.
// A first write uses expectedRevision zero; every changed existing record
// advances its durable revision exactly once. The transaction includes parent,
// required-intent, and applied-evidence rows, so restart cannot expose a
// partially written checkpoint.
func (r *ProfileApplicationRepository) Save(ctx context.Context, record connectionbinding.ProfileApplicationRecord, expectedRevision int64) (connectionbinding.ProfileApplicationRecord, error) {
	if r == nil || r.db == nil || ctx == nil {
		return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrProfileApplicationConflict
	}
	if err := ctx.Err(); err != nil {
		return connectionbinding.ProfileApplicationRecord{}, err
	}
	if expectedRevision < 0 {
		return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrInvalidProfileApplication
	}
	normalized, err := connectionbinding.NewProfileApplication(record)
	if err != nil {
		return connectionbinding.ProfileApplicationRecord{}, err
	}
	beginner, ok := r.db.(beginner)
	if !ok {
		// Save owns a complete transaction boundary. A caller-owned pgx.Tx still
		// satisfies beginner and uses a nested savepoint; an arbitrary DBTX does
		// not silently receive a multi-statement partial write.
		return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrProfileApplicationConflict
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return connectionbinding.ProfileApplicationRecord{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	saved, err := saveProfileApplicationTx(ctx, tx, normalized, expectedRevision)
	if err != nil {
		return connectionbinding.ProfileApplicationRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return connectionbinding.ProfileApplicationRecord{}, err
	}
	return saved, nil
}

// Replace explicitly supersedes an immutable checkpoint. It is intentionally
// narrower than Save: only a fresh applying record with no post-mutation
// evidence can replace the locked current intent. The old child rows and
// parent row are removed and the new complete intent is inserted in the same
// transaction, preserving the original creation time and advancing the CAS
// revision exactly once.
func (r *ProfileApplicationRepository) Replace(ctx context.Context, record connectionbinding.ProfileApplicationRecord, expectedRevision int64) (connectionbinding.ProfileApplicationRecord, error) {
	if r == nil || r.db == nil || ctx == nil {
		return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrProfileApplicationConflict
	}
	if err := ctx.Err(); err != nil {
		return connectionbinding.ProfileApplicationRecord{}, err
	}
	normalized, err := connectionbinding.NewProfileApplication(record)
	if err != nil {
		return connectionbinding.ProfileApplicationRecord{}, err
	}
	if expectedRevision < 1 || normalized.Status != connectionbinding.ProfileApplicationApplying || len(normalized.AppliedConnections) != 0 {
		return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrInvalidProfileApplication
	}
	beginner, ok := r.db.(beginner)
	if !ok {
		return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrProfileApplicationConflict
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return connectionbinding.ProfileApplicationRecord{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	queries := profiledb.New(tx)
	scope := recordProfileApplicationScope(normalized)
	row, err := queries.GetProfileApplicationForUpdate(ctx, profileApplicationForUpdateParams(scope, normalized.TargetID))
	if errors.Is(err, pgx.ErrNoRows) {
		return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrProfileApplicationNotFound
	}
	if err != nil {
		return connectionbinding.ProfileApplicationRecord{}, normalizeProfileApplicationDatabaseError(err)
	}
	current, err := loadProfileApplication(ctx, tx, row)
	if err != nil {
		return connectionbinding.ProfileApplicationRecord{}, err
	}
	if current.Revision != expectedRevision {
		return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrProfileApplicationConflict
	}
	if normalized.ID == current.ID {
		return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrProfileApplicationReplacement
	}
	normalized.LastCompletedApplicationID = current.LastCompletedApplicationID
	normalized.LastCompletedAt = current.LastCompletedAt
	rows, err := queries.DeleteProfileApplicationForReplacement(ctx, profiledb.DeleteProfileApplicationForReplacementParams{
		ApplicationID: current.ID.String(), ExpectedRevision: current.Revision,
		ReplacementApplicationID: normalized.ID.String(),
		ExpectedCheckoutID:       current.CheckoutID, ExpectedRuntimeID: current.RuntimeID, ExpectedTargetID: current.TargetID.String(),
		ExpectedProjectID: current.ProjectID.String(), ExpectedEnvironment: current.Environment,
	})
	if err != nil {
		return connectionbinding.ProfileApplicationRecord{}, normalizeProfileApplicationDatabaseError(err)
	}
	if rows != 1 {
		return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrProfileApplicationConflict
	}
	normalized.CreatedAt = current.CreatedAt
	normalized.Revision = current.Revision + 1
	if _, err := insertProfileApplicationRows(ctx, queries, normalized); err != nil {
		return connectionbinding.ProfileApplicationRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return connectionbinding.ProfileApplicationRecord{}, err
	}
	return normalized, nil
}

func saveProfileApplicationTx(ctx context.Context, tx DBTX, record connectionbinding.ProfileApplicationRecord, expectedRevision int64) (connectionbinding.ProfileApplicationRecord, error) {
	if tx == nil {
		return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrProfileApplicationConflict
	}
	queries := profiledb.New(tx)
	scope := recordProfileApplicationScope(record)
	row, err := queries.GetProfileApplicationForUpdate(ctx, profileApplicationForUpdateParams(scope, record.TargetID))
	if errors.Is(err, pgx.ErrNoRows) {
		// The ID is also immutable. Looking it up separately turns a same-ID,
		// changed-scope write into the domain replacement error instead of a
		// provider-specific unique-key diagnostic.
		_, idErr := queries.GetProfileApplicationByIDForUpdate(ctx, record.ID.String())
		if idErr == nil {
			return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrProfileApplicationReplacement
		}
		if !errors.Is(idErr, pgx.ErrNoRows) {
			return connectionbinding.ProfileApplicationRecord{}, normalizeProfileApplicationDatabaseError(idErr)
		}
		if expectedRevision != 0 {
			return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrProfileApplicationConflict
		}
		return insertProfileApplicationTx(ctx, tx, queries, record)
	}
	if err != nil {
		return connectionbinding.ProfileApplicationRecord{}, normalizeProfileApplicationDatabaseError(err)
	}
	current, err := loadProfileApplication(ctx, tx, row)
	if err != nil {
		return connectionbinding.ProfileApplicationRecord{}, err
	}
	if !sameProfileApplicationIntent(current, record) {
		return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrProfileApplicationReplacement
	}
	if current.Revision != expectedRevision {
		return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrProfileApplicationConflict
	}
	// Completion history is database-owned derived state. Callers cannot erase
	// or forge it while advancing the current attempt.
	record.LastCompletedApplicationID = current.LastCompletedApplicationID
	record.LastCompletedAt = current.LastCompletedAt
	if sameProfileApplicationPayload(current, record) {
		return current, nil
	}
	if current.Status == connectionbinding.ProfileApplicationApplied &&
		(!sameProfileApplicationEvidence(current, record) || record.Status != connectionbinding.ProfileApplicationApplied) {
		return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrProfileApplicationReplacement
	}
	if !validProfileApplicationStatusTransition(current.Status, record.Status) {
		return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrProfileApplicationConflict
	}
	if record.UpdatedAt.Before(current.UpdatedAt) {
		return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrProfileApplicationConflict
	}

	record.CreatedAt = current.CreatedAt
	record.Revision = current.Revision + 1
	if _, err := queries.DeleteProfileApplicationAppliedConnections(ctx, string(record.ID)); err != nil {
		return connectionbinding.ProfileApplicationRecord{}, normalizeProfileApplicationDatabaseError(err)
	}
	if err := insertProfileApplicationAppliedConnections(ctx, queries, record); err != nil {
		return connectionbinding.ProfileApplicationRecord{}, err
	}
	var rows int64
	if record.Status == connectionbinding.ProfileApplicationApplied && current.Status != connectionbinding.ProfileApplicationApplied {
		rows, err = queries.CompleteProfileApplication(ctx, profiledb.CompleteProfileApplicationParams{
			ApplicationID: record.ID.String(), ExpectedRevision: current.Revision,
			ExpectedCheckoutID: record.CheckoutID, ExpectedRuntimeID: record.RuntimeID, ExpectedTargetID: record.TargetID.String(),
			ExpectedProjectID: record.ProjectID.String(), ExpectedEnvironment: record.Environment,
			CompletionUpdatedAt: profileTimestamp(record.UpdatedAt),
		})
	} else {
		rows, err = queries.UpdateProfileApplication(ctx, profiledb.UpdateProfileApplicationParams{
			Status: string(record.Status), UpdatedAt: profileTimestamp(record.UpdatedAt), Revision: record.Revision,
			ID: record.ID.String(), ExpectedRevision: current.Revision,
			CheckoutID: record.CheckoutID, RuntimeID: record.RuntimeID, TargetID: record.TargetID.String(),
			ProjectID: record.ProjectID.String(), Environment: record.Environment,
		})
	}
	if err != nil {
		return connectionbinding.ProfileApplicationRecord{}, normalizeProfileApplicationDatabaseError(err)
	}
	if rows != 1 {
		return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrProfileApplicationConflict
	}
	if record.Status == connectionbinding.ProfileApplicationApplied {
		record.LastCompletedApplicationID = record.ID
		record.LastCompletedAt = record.UpdatedAt
	}
	return record, nil
}

func insertProfileApplicationTx(ctx context.Context, tx DBTX, queries *profiledb.Queries, record connectionbinding.ProfileApplicationRecord) (connectionbinding.ProfileApplicationRecord, error) {
	if record.LastCompletedApplicationID != "" || !record.LastCompletedAt.IsZero() {
		return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrInvalidProfileApplication
	}
	record.Revision = 1
	return insertProfileApplicationRows(ctx, queries, record)
}

func insertProfileApplicationRows(ctx context.Context, queries *profiledb.Queries, record connectionbinding.ProfileApplicationRecord) (connectionbinding.ProfileApplicationRecord, error) {
	rows, err := queries.InsertProfileApplication(ctx, profiledb.InsertProfileApplicationParams{
		ID: record.ID.String(), CheckoutID: record.CheckoutID, RuntimeID: record.RuntimeID,
		TargetID: record.TargetID.String(), ProjectID: record.ProjectID.String(), Environment: record.Environment,
		ProfileName: record.ProfileName, GraphDigest: record.GraphDigest, SourceDigest: record.SourceDigest, ProfileDigest: record.ProfileDigest,
		LastCompletedApplicationID: record.LastCompletedApplicationID.String(), LastCompletedAt: profileNullableTimestamp(record.LastCompletedAt),
		RetiredConnections: retiredConnectionsJSON(record.RetiredConnections),
		Status:             string(record.Status), Revision: record.Revision,
		CreatedAt: profileTimestamp(record.CreatedAt), UpdatedAt: profileTimestamp(record.UpdatedAt),
	})
	if err != nil {
		return connectionbinding.ProfileApplicationRecord{}, normalizeProfileApplicationDatabaseError(err)
	}
	if rows != 1 {
		// ON CONFLICT DO NOTHING can race with a concurrent first writer. The
		// surrounding transaction is deliberately rolled back and the caller
		// retries with the durable revision it reads next.
		return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrProfileApplicationConflict
	}
	for _, required := range record.RequiredConnections {
		rows, err := queries.InsertProfileApplicationRequiredConnection(ctx, profiledb.InsertProfileApplicationRequiredConnectionParams{
			ApplicationID: record.ID.String(), ConnectionID: required.ConnectionID.String(), ConnectorKind: required.ConnectorKind,
		})
		if err != nil {
			return connectionbinding.ProfileApplicationRecord{}, normalizeProfileApplicationDatabaseError(err)
		}
		if rows != 1 {
			return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrProfileApplicationConflict
		}
	}
	for _, expected := range record.ExpectedConnections {
		rows, err := queries.InsertProfileApplicationExpectedConnection(ctx, profiledb.InsertProfileApplicationExpectedConnectionParams{
			ApplicationID: record.ID.String(), BindingID: expected.BindingID.String(), ConnectionID: expected.ConnectionID.String(), ConnectorKind: expected.ConnectorKind,
			AuthenticationMode: string(expected.AuthenticationMode), EndpointJson: profileEndpointJSON(expected.Endpoint),
			CredentialProjectID: expected.CredentialReference.ProjectID.String(), CredentialEnvironment: expected.CredentialReference.Environment,
			CredentialSecretPath: expected.CredentialReference.SecretPath, CredentialSecretKey: expected.CredentialReference.SecretKey,
			BindingRevision: expected.BindingRevision, ProviderVersion: expected.ProviderVersion,
		})
		if err != nil {
			return connectionbinding.ProfileApplicationRecord{}, normalizeProfileApplicationDatabaseError(err)
		}
		if rows != 1 {
			return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrProfileApplicationConflict
		}
	}
	if err := insertProfileApplicationAppliedConnections(ctx, queries, record); err != nil {
		return connectionbinding.ProfileApplicationRecord{}, err
	}
	return record, nil
}

func insertProfileApplicationAppliedConnections(ctx context.Context, queries *profiledb.Queries, record connectionbinding.ProfileApplicationRecord) error {
	for _, applied := range record.AppliedConnections {
		if _, err := queries.InsertProfileApplicationAppliedConnection(ctx, profiledb.InsertProfileApplicationAppliedConnectionParams{
			ApplicationID: record.ID.String(), BindingID: applied.BindingID.String(), ConnectionID: applied.ConnectionID.String(), ConnectorKind: applied.ConnectorKind,
			AuthenticationMode: string(applied.AuthenticationMode), EndpointJson: profileEndpointJSON(applied.Endpoint),
			CredentialProjectID: applied.CredentialReference.ProjectID.String(), CredentialEnvironment: applied.CredentialReference.Environment,
			CredentialSecretPath: applied.CredentialReference.SecretPath, CredentialSecretKey: applied.CredentialReference.SecretKey,
			BindingRevision: applied.BindingRevision, ProviderVersion: applied.ProviderVersion,
		}); err != nil {
			return normalizeProfileApplicationDatabaseError(err)
		}
	}
	return nil
}

func loadProfileApplication(ctx context.Context, db DBTX, row profiledb.ConnectionBindingProfileApplication) (connectionbinding.ProfileApplicationRecord, error) {
	if !row.CreatedAt.Valid || !row.UpdatedAt.Valid {
		return connectionbinding.ProfileApplicationRecord{}, fmt.Errorf("%w: persisted profile application timestamps are null", connectionbinding.ErrInvalidProfileApplication)
	}
	queries := profiledb.New(db)
	requiredRows, err := queries.ListProfileApplicationRequiredConnections(ctx, row.ID)
	if err != nil {
		return connectionbinding.ProfileApplicationRecord{}, normalizeProfileApplicationDatabaseError(err)
	}
	expectedRows, err := queries.ListProfileApplicationExpectedConnections(ctx, row.ID)
	if err != nil {
		return connectionbinding.ProfileApplicationRecord{}, normalizeProfileApplicationDatabaseError(err)
	}
	appliedRows, err := queries.ListProfileApplicationAppliedConnections(ctx, row.ID)
	if err != nil {
		return connectionbinding.ProfileApplicationRecord{}, normalizeProfileApplicationDatabaseError(err)
	}
	var retiredConnections []connectionbinding.ProfileApplicationConnection
	if err := json.Unmarshal(row.RetiredConnections, &retiredConnections); err != nil {
		return connectionbinding.ProfileApplicationRecord{}, fmt.Errorf("%w: persisted retired profile connections are invalid", connectionbinding.ErrInvalidProfileApplication)
	}
	requiredConnections := make([]connectionbinding.ProfileApplicationRequiredConnection, 0, len(requiredRows))
	for _, required := range requiredRows {
		requiredConnections = append(requiredConnections, connectionbinding.ProfileApplicationRequiredConnection{
			ConnectionID: projectResourceID(required.ConnectionID), ConnectorKind: required.ConnectorKind,
		})
	}
	expected := make([]connectionbinding.ProfileApplicationConnection, 0, len(expectedRows))
	for _, stored := range expectedRows {
		connection, err := profileApplicationConnectionFromStorage(stored.BindingID, stored.ConnectionID, stored.ConnectorKind, stored.AuthenticationMode, stored.EndpointJson, stored.CredentialProjectID, stored.CredentialEnvironment, stored.CredentialSecretPath, stored.CredentialSecretKey, stored.BindingRevision, stored.ProviderVersion)
		if err != nil {
			return connectionbinding.ProfileApplicationRecord{}, err
		}
		expected = append(expected, connection)
	}
	applied := make([]connectionbinding.ProfileApplicationConnection, 0, len(appliedRows))
	for _, evidence := range appliedRows {
		connection, err := profileApplicationConnectionFromStorage(evidence.BindingID, evidence.ConnectionID, evidence.ConnectorKind, evidence.AuthenticationMode, evidence.EndpointJson, evidence.CredentialProjectID, evidence.CredentialEnvironment, evidence.CredentialSecretPath, evidence.CredentialSecretKey, evidence.BindingRevision, evidence.ProviderVersion)
		if err != nil {
			return connectionbinding.ProfileApplicationRecord{}, err
		}
		applied = append(applied, connection)
	}
	record := connectionbinding.ProfileApplicationRecord{
		ID: connectionbinding.ProfileApplicationID(row.ID), CheckoutID: row.CheckoutID, RuntimeID: row.RuntimeID,
		TargetID: connectionbinding.TargetID(row.TargetID), ProjectID: projectResourceID(row.ProjectID), Environment: row.Environment,
		ProfileName: row.ProfileName, GraphDigest: row.GraphDigest, SourceDigest: row.SourceDigest, ProfileDigest: row.ProfileDigest,
		LastCompletedApplicationID: connectionbinding.ProfileApplicationID(row.LastCompletedApplicationID), LastCompletedAt: nullableProfileTime(row.LastCompletedAt),
		RequiredConnections: requiredConnections, ExpectedConnections: expected, RetiredConnections: retiredConnections, AppliedConnections: applied, Status: connectionbinding.ProfileApplicationStatus(row.Status),
		Revision: row.Revision, CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(),
	}
	normalized, err := connectionbinding.NewProfileApplication(record)
	if err != nil {
		return connectionbinding.ProfileApplicationRecord{}, fmt.Errorf("validate persisted profile application: %w", err)
	}
	return normalized, nil
}

func profileApplicationConnectionFromStorage(bindingID, connectionID, connectorKind, authenticationMode string, endpointJSON []byte, credentialProjectID, credentialEnvironment, credentialSecretPath, credentialSecretKey string, bindingRevision int64, providerVersion string) (connectionbinding.ProfileApplicationConnection, error) {
	var endpoint connectionbinding.EndpointConfig
	if err := json.Unmarshal(endpointJSON, &endpoint); err != nil {
		return connectionbinding.ProfileApplicationConnection{}, fmt.Errorf("%w: persisted profile connection endpoint is invalid", connectionbinding.ErrInvalidProfileApplication)
	}
	return connectionbinding.ProfileApplicationConnection{
		BindingID: connectionbinding.BindingID(bindingID), ConnectionID: projectResourceID(connectionID), ConnectorKind: connectorKind,
		AuthenticationMode: connectionbinding.AuthenticationMode(authenticationMode), Endpoint: endpoint,
		CredentialReference: connectionbinding.CredentialReference{ProjectID: projectResourceID(credentialProjectID), Environment: credentialEnvironment, SecretPath: credentialSecretPath, SecretKey: credentialSecretKey},
		BindingRevision:     bindingRevision, ProviderVersion: providerVersion,
	}, nil
}

func profileEndpointJSON(endpoint connectionbinding.EndpointConfig) []byte {
	encoded, err := json.Marshal(endpoint)
	if err != nil {
		return []byte("{}")
	}
	return encoded
}

func retiredConnectionsJSON(connections []connectionbinding.ProfileApplicationConnection) []byte {
	if len(connections) == 0 {
		return []byte("[]")
	}
	encoded, err := json.Marshal(connections)
	if err != nil {
		return []byte("[]")
	}
	return encoded
}

// projectResourceID keeps row-to-domain conversion explicit while allowing
// persisted corruption to be rejected by ProfileApplication.Validate rather
// than accidentally treated as a symbolic identifier.
func projectResourceID(value string) (id projectgraph.ResourceID) {
	return projectgraph.ResourceID(value)
}

func profileApplicationLookupParams(scope connectionbinding.ProfileApplicationScope, targetID connectionbinding.TargetID) profiledb.GetProfileApplicationParams {
	return profiledb.GetProfileApplicationParams{
		CheckoutID: scope.CheckoutID, RuntimeID: scope.RuntimeID, TargetID: targetID.String(),
		ProjectID: scope.ProjectID.String(), Environment: scope.Environment,
	}
}

func profileApplicationForUpdateParams(scope connectionbinding.ProfileApplicationScope, targetID connectionbinding.TargetID) profiledb.GetProfileApplicationForUpdateParams {
	return profiledb.GetProfileApplicationForUpdateParams{
		CheckoutID: scope.CheckoutID, RuntimeID: scope.RuntimeID, TargetID: targetID.String(),
		ProjectID: scope.ProjectID.String(), Environment: scope.Environment,
	}
}

func recordProfileApplicationScope(record connectionbinding.ProfileApplicationRecord) connectionbinding.ProfileApplicationScope {
	return connectionbinding.ProfileApplicationScope{CheckoutID: record.CheckoutID, RuntimeID: record.RuntimeID, ProjectID: record.ProjectID, Environment: record.Environment}
}

func sameProfileApplicationIntent(left, right connectionbinding.ProfileApplicationRecord) bool {
	if left.ID != right.ID || left.CheckoutID != right.CheckoutID || left.RuntimeID != right.RuntimeID || left.TargetID != right.TargetID || left.ProjectID != right.ProjectID || left.Environment != right.Environment || left.ProfileName != right.ProfileName || left.GraphDigest != right.GraphDigest || left.SourceDigest != right.SourceDigest || left.ProfileDigest != right.ProfileDigest || len(left.RequiredConnections) != len(right.RequiredConnections) || len(left.ExpectedConnections) != len(right.ExpectedConnections) || len(left.RetiredConnections) != len(right.RetiredConnections) {
		return false
	}
	for index := range left.RequiredConnections {
		if left.RequiredConnections[index] != right.RequiredConnections[index] {
			return false
		}
	}
	for index := range left.ExpectedConnections {
		if !reflect.DeepEqual(left.ExpectedConnections[index], right.ExpectedConnections[index]) {
			return false
		}
	}
	for index := range left.RetiredConnections {
		if !reflect.DeepEqual(left.RetiredConnections[index], right.RetiredConnections[index]) {
			return false
		}
	}
	return true
}

func sameProfileApplicationEvidence(left, right connectionbinding.ProfileApplicationRecord) bool {
	if len(left.AppliedConnections) != len(right.AppliedConnections) {
		return false
	}
	for index := range left.AppliedConnections {
		if !reflect.DeepEqual(left.AppliedConnections[index], right.AppliedConnections[index]) {
			return false
		}
	}
	return true
}

func sameProfileApplicationPayload(left, right connectionbinding.ProfileApplicationRecord) bool {
	return sameProfileApplicationIntent(left, right) && sameProfileApplicationEvidence(left, right) && left.Status == right.Status
}

func validProfileApplicationStatusTransition(from, to connectionbinding.ProfileApplicationStatus) bool {
	if from == to {
		return true
	}
	switch from {
	case connectionbinding.ProfileApplicationApplying:
		return to == connectionbinding.ProfileApplicationIncomplete || to == connectionbinding.ProfileApplicationApplied
	case connectionbinding.ProfileApplicationIncomplete:
		return to == connectionbinding.ProfileApplicationApplying || to == connectionbinding.ProfileApplicationApplied
	default:
		return false
	}
}

func validateProfileApplicationScope(scope connectionbinding.ProfileApplicationScope) error {
	if scope.CheckoutID == "" || scope.RuntimeID == "" || scope.CheckoutID != strings.TrimSpace(scope.CheckoutID) || scope.RuntimeID != strings.TrimSpace(scope.RuntimeID) {
		return connectionbinding.ErrInvalidProfileApplication
	}
	return projectgraph.ValidateServingScope(scope.ProjectID, scope.Environment)
}

func normalizeProfileApplicationDatabaseError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505", "40001", "40P01":
			return connectionbinding.ErrProfileApplicationConflict
		case "23503", "23514", "23502", "22P02":
			return connectionbinding.ErrInvalidProfileApplication
		}
	}
	return err
}

func profileTimestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}

func profileNullableTimestamp(value time.Time) pgtype.Timestamptz {
	if value.IsZero() {
		return pgtype.Timestamptz{}
	}
	return profileTimestamp(value)
}

func nullableProfileTime(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time.UTC()
}
