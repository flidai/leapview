package postgres

// The upgrade coordinator is the narrow cross-database operation boundary for
// PostgreSQL-backed DuckLake catalogs. It owns no catalog DDL capability: the
// control coordinator credentials invoke only the guarded authority functions
// while a separately authenticated catalog executor performs bootstrap and
// migration in leapview_ducklake.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
)

const (
	DefaultControlUpgradeCoordinatorRole = "leapview_control_upgrade_coordinator"
	DefaultControlMigratorRole           = "leapview_control_migrator"
	DefaultControlDatabase               = "leapview_control"
	DefaultDuckLakeCatalogMigratorRole   = "leapview_ducklake_migrator"
	DefaultDuckLakeDatabase              = "leapview_ducklake"
)

var (
	ErrWrongDatabaseCredential = errors.New("DuckLake upgrade credential is connected to the wrong database or role")
	ErrAutomaticMigration      = errors.New("DuckLake automatic migration must remain disabled")
	ErrCatalogExecutor         = errors.New("DuckLake catalog executor is unavailable")
)

const fenceRenewalTimeout = 10 * time.Second

type CatalogBootstrapMode string
type CatalogMigrationMode string

const (
	// CatalogBootstrapInitialize may create a missing catalog but does not
	// perform an implicit schema upgrade.
	CatalogBootstrapInitialize CatalogBootstrapMode = "initialize"
	// CatalogMigrationAutomatic is available only to this fenced owner-capable
	// operation. Ordinary runtime attachments have no value of this type and
	// always compile AUTOMATIC_MIGRATION=false, CREATE_IF_NOT_EXISTS=false.
	CatalogMigrationAutomatic CatalogMigrationMode = "automatic"
)

// DatabaseIdentity is checked before any authority mutation. SessionUser is
// retained as evidence so a connection obtained via SET ROLE cannot disguise
// the login credential used by the operation.
type DatabaseIdentity struct {
	Database    string
	User        string
	SessionUser string
}

// ReadDatabaseIdentity reads PostgreSQL's authoritative database and role
// identities. It is deliberately tiny so tests can exercise swapped
// credentials with a real pgx pool.
func ReadDatabaseIdentity(ctx context.Context, db DBTX) (DatabaseIdentity, error) {
	if db == nil {
		return DatabaseIdentity{}, ErrInvalid
	}
	var identity DatabaseIdentity
	if err := db.QueryRow(ctx, `SELECT current_database(), current_user, session_user`).Scan(&identity.Database, &identity.User, &identity.SessionUser); err != nil {
		return DatabaseIdentity{}, err
	}
	return identity, nil
}

// ValidateDatabaseIdentity rejects wrong-database and swapped-credential
// connections before acquiring a fence or invoking a catalog executor.
func ValidateDatabaseIdentity(identity DatabaseIdentity, expectedDatabase, expectedRole string) error {
	expectedDatabase = strings.TrimSpace(expectedDatabase)
	expectedRole = strings.TrimSpace(expectedRole)
	if expectedDatabase == "" || expectedRole == "" || identity.Database != expectedDatabase || identity.User != expectedRole || identity.SessionUser != expectedRole {
		return fmt.Errorf("%w: got database=%q user=%q session_user=%q, expected database=%q role=%q", ErrWrongDatabaseCredential, identity.Database, identity.User, identity.SessionUser, expectedDatabase, expectedRole)
	}
	return nil
}

// CatalogBootstrapOptions are passed to the owner-capable catalog executor.
// Bootstrap is a distinct typed initialize operation; it cannot request an
// implicit migration.
type CatalogBootstrapOptions struct {
	PhysicalPoolID string
	CatalogID      string
	MetadataSchema string
	DataPath       string
	Mode           CatalogBootstrapMode
}

type CatalogMigrationOptions struct {
	PhysicalPoolID string
	CatalogID      string
	Current        RuntimeCompatibility
	Target         RuntimeCompatibility
	Mode           CatalogMigrationMode
	// Renew must be called by a long-running executor before the bounded lease
	// expires. It renews the global and pool fences together and returns a
	// stale/expired error when another owner has taken over.
	Renew func(context.Context) error
}

// CatalogExecutor is implemented by the actual DuckDB/DuckLake adapter. The
// interface keeps this package testable without requiring the DuckLake
// extension in every CI lane. Implementations must use owner-capable
// leapview_ducklake credentials and must never receive control DB credentials.
type CatalogExecutor interface {
	Identity(context.Context) (DatabaseIdentity, error)
	Bootstrap(context.Context, CatalogBootstrapOptions) error
	Migrate(context.Context, CatalogMigrationOptions) error
	// VerifySnapshot must inspect the migrated catalog and return bounded,
	// exact evidence for this retained snapshot. The coordinator never
	// fabricates a successful qualification marker.
	VerifySnapshot(context.Context, SnapshotRef, RuntimeCompatibility) (json.RawMessage, error)
}

// CatalogExecutorFuncs is a convenient adapter for operation wiring and
// conformance tests. A nil callback is an explicit executor-unavailable error.
type CatalogExecutorFuncs struct {
	IdentityFunc       func(context.Context) (DatabaseIdentity, error)
	BootstrapFunc      func(context.Context, CatalogBootstrapOptions) error
	MigrateFunc        func(context.Context, CatalogMigrationOptions) error
	VerifySnapshotFunc func(context.Context, SnapshotRef, RuntimeCompatibility) (json.RawMessage, error)
}

// SQLCatalogExecutor wires the typed parent-package attach constructors to a
// DuckDB SQL session. The session itself is supplied by the caller (normally
// the owner-capable catalog-migrator environment); this package never opens a
// credential or embeds a PostgreSQL DSN in ATTACH SQL.
type SQLCatalogExecutor struct {
	IdentityDB DBTX
	Exec       interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	}
	Query interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	}
	DuckLakeSecret string
	PostgresSecret string
	DataPath       string
	CatalogAdmin   DBTX
	RuntimeRole    string
	// CatalogDatabase and CatalogRole pin the identity of CatalogAdmin. Empty
	// values use the deployment defaults; callers cannot silently substitute
	// the ordinary runtime credential for owner-capable migration grants.
	CatalogDatabase string
	CatalogRole     string
}

func (e *SQLCatalogExecutor) Identity(ctx context.Context) (DatabaseIdentity, error) {
	if e == nil {
		return DatabaseIdentity{}, ErrCatalogExecutor
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return ReadDatabaseIdentity(ctx, e.IdentityDB)
}
func (e *SQLCatalogExecutor) Bootstrap(ctx context.Context, options CatalogBootstrapOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if e == nil || e.Exec == nil || options.Mode != CatalogBootstrapInitialize || e.CatalogAdmin == nil || !isSQLIdentifier(e.RuntimeRole) || !validID(options.PhysicalPoolID) || !validID(options.CatalogID) {
		return ErrCatalogExecutor
	}
	if err := e.validateCatalogAdmin(ctx); err != nil {
		return err
	}
	dataPath := options.DataPath
	if dataPath == "" {
		dataPath = e.DataPath
	}
	config := ducklake.PostgresCatalogConfig{PhysicalPoolID: options.PhysicalPoolID, DuckLakeSecret: e.DuckLakeSecret, PostgresSecret: e.PostgresSecret, MetadataSchema: options.MetadataSchema, DataPath: dataPath, Mode: ducklake.PostgresCatalogInitialize}
	statements, err := config.Statements()
	if err != nil {
		return err
	}
	if err := EnsureCatalogMetadataSchema(ctx, e.CatalogAdmin, options.MetadataSchema); err != nil {
		return err
	}
	// Bootstrap and migration may share one DuckDB session. Detach the
	// initialized catalog before the migration attach reuses the canonical lake
	// alias; persistent PostgreSQL metadata survives DETACH.
	statements = append(statements, `DETACH "lake"`)
	if err := executeCatalogStatements(ctx, e.Exec, statements); err != nil {
		return err
	}
	if err := ProvisionCatalogRuntimePrivileges(ctx, e.CatalogAdmin, options.MetadataSchema, e.RuntimeRole); err != nil {
		return err
	}
	return nil
}

// EnsureCatalogMetadataSchema creates the exact per-pool metadata namespace
// through the authenticated catalog migrator. The runtime role is never
// granted database CREATE and therefore cannot create or redirect a catalog
// schema. Callers must validate CatalogAdmin identity before invoking this
// helper.
func EnsureCatalogMetadataSchema(ctx context.Context, db DBTX, metadataSchema string) error {
	if db == nil || !isSQLIdentifier(metadataSchema) {
		return ErrInvalid
	}
	if _, err := db.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS `+quoteSQLIdentifier(metadataSchema)); err != nil {
		return fmt.Errorf("create DuckLake metadata schema: %w", err)
	}
	var currentUser, owner string
	if err := db.QueryRow(ctx, `SELECT current_user, r.rolname FROM pg_namespace n JOIN pg_roles r ON r.oid=n.nspowner WHERE n.nspname=$1`, metadataSchema).Scan(&currentUser, &owner); err != nil {
		return fmt.Errorf("verify DuckLake metadata schema owner: %w", err)
	}
	if strings.TrimSpace(currentUser) == "" || currentUser != owner {
		return fmt.Errorf("%w: DuckLake metadata schema owner is not the catalog migrator", ErrWrongDatabaseCredential)
	}
	return nil
}

func (e *SQLCatalogExecutor) validateCatalogAdmin(ctx context.Context) error {
	if e == nil || e.CatalogAdmin == nil {
		return ErrCatalogExecutor
	}
	database := strings.TrimSpace(e.CatalogDatabase)
	if database == "" {
		database = DefaultDuckLakeDatabase
	}
	role := strings.TrimSpace(e.CatalogRole)
	if role == "" {
		role = DefaultDuckLakeCatalogMigratorRole
	}
	identity, err := ReadDatabaseIdentity(ctx, e.CatalogAdmin)
	if err != nil {
		return err
	}
	return ValidateDatabaseIdentity(identity, database, role)
}
func (e *SQLCatalogExecutor) Migrate(ctx context.Context, options CatalogMigrationOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if e == nil || e.Exec == nil || options.Mode != CatalogMigrationAutomatic || e.CatalogAdmin == nil || !isSQLIdentifier(e.RuntimeRole) || !validID(options.PhysicalPoolID) || !validID(options.CatalogID) || options.Current.validate() != nil || options.Target.validate() != nil {
		return ErrCatalogExecutor
	}
	if err := e.validateCatalogAdmin(ctx); err != nil {
		return err
	}
	config := ducklake.PostgresCatalogConfig{PhysicalPoolID: options.PhysicalPoolID, DuckLakeSecret: e.DuckLakeSecret, PostgresSecret: e.PostgresSecret, MetadataSchema: ducklake.MetadataSchemaForPool(options.PhysicalPoolID), Mode: ducklake.PostgresCatalogMigrate}
	statements, err := config.MigrationStatements()
	if err != nil {
		return err
	}
	if options.Renew != nil {
		if err := options.Renew(ctx); err != nil {
			return err
		}
	}
	statements = append(statements, `DETACH "lake"`)
	if err := executeCatalogStatements(ctx, e.Exec, statements); err != nil {
		return err
	}
	return ProvisionCatalogRuntimePrivileges(ctx, e.CatalogAdmin, config.MetadataSchema, e.RuntimeRole)
}
func (e *SQLCatalogExecutor) VerifySnapshot(ctx context.Context, snapshot SnapshotRef, compatibility RuntimeCompatibility) (json.RawMessage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if e == nil {
		return nil, ErrCatalogExecutor
	}
	if e.Exec == nil || e.Query == nil || !validID(snapshot.PhysicalPoolID) || !validID(snapshot.CatalogID) || snapshot.SnapshotID <= 0 || compatibility.validate() != nil || strings.TrimSpace(e.DataPath) == "" {
		return nil, ErrCatalogExecutor
	}
	expectedDataPath, err := ducklake.CanonicalDataPath(e.DataPath)
	if err != nil {
		return nil, ErrCatalogExecutor
	}
	config := ducklake.PostgresCatalogConfig{PhysicalPoolID: snapshot.PhysicalPoolID, DuckLakeSecret: e.DuckLakeSecret, PostgresSecret: e.PostgresSecret, MetadataSchema: ducklake.MetadataSchemaForPool(snapshot.PhysicalPoolID), Mode: ducklake.PostgresCatalogServing, SnapshotVersion: snapshot.SnapshotID}
	statements, err := config.Statements()
	if err != nil {
		return nil, err
	}
	if err := executeCatalogStatements(ctx, e.Exec, statements); err != nil {
		return nil, err
	}
	// Build the cleanup context only when the query path is done. Creating it
	// before a long verification query would consume its entire budget and can
	// leave an attached catalog behind when cleanup finally runs.
	defer func() {
		detachCtx, detachCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer detachCancel()
		_, _ = e.Exec.ExecContext(detachCtx, `DETACH "lake"`)
	}()
	var duckdbVersion string
	if err := e.Query.QueryRowContext(ctx, `SELECT version()`).Scan(&duckdbVersion); err != nil {
		return nil, fmt.Errorf("verify DuckDB runtime: %w", err)
	}
	var catalogType, extensionVersion, dataPath string
	if err := e.Query.QueryRowContext(ctx, `SELECT catalog_type, extension_version, data_path FROM lake.settings()`).Scan(&catalogType, &extensionVersion, &dataPath); err != nil {
		return nil, fmt.Errorf("verify DuckLake settings: %w", err)
	}
	var catalogVersion string
	if err := e.Query.QueryRowContext(ctx, `SELECT value FROM lake.options() WHERE lower(option_name) = 'version' AND upper(scope) = 'GLOBAL'`).Scan(&catalogVersion); err != nil {
		return nil, fmt.Errorf("verify DuckLake catalog version: %w", err)
	}
	actualRuntime, err := canonicalRuntimeComponent("duckdb", duckdbVersion)
	if err != nil {
		return nil, fmt.Errorf("verify DuckDB runtime: %w", err)
	}
	actualExtension, err := canonicalRuntimeComponent("ducklake", extensionVersion)
	if err != nil {
		return nil, fmt.Errorf("verify DuckLake extension: %w", err)
	}
	expectedRuntime, err := canonicalRuntimeComponent("duckdb", compatibility.DuckDBRuntime)
	if err != nil {
		return nil, fmt.Errorf("verify target DuckDB runtime: %w", err)
	}
	expectedExtension, err := canonicalRuntimeComponent("ducklake", compatibility.DuckLakeExtension)
	if err != nil {
		return nil, fmt.Errorf("verify target DuckLake extension: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(catalogType), "postgres") {
		return nil, fmt.Errorf("verify DuckLake catalog: expected postgres catalog, got %q", catalogType)
	}
	actualDataPath, err := ducklake.CanonicalDataPath(dataPath)
	if err != nil {
		return nil, fmt.Errorf("verify DuckLake data path: %w", err)
	}
	if actualDataPath != expectedDataPath {
		return nil, fmt.Errorf("verify DuckLake data path: admitted path mismatch")
	}
	if actualRuntime != expectedRuntime || actualExtension != expectedExtension {
		return nil, fmt.Errorf("%w: loaded runtime or extension differs from target", ErrCompatibilityMismatch)
	}
	actualCatalogVersion, err := canonicalCatalogVersion(catalogVersion)
	if err != nil {
		return nil, fmt.Errorf("verify DuckLake catalog version: %w", err)
	}
	expectedCatalogVersion, err := canonicalCatalogVersion(compatibility.CatalogFormat)
	if err != nil || actualCatalogVersion != expectedCatalogVersion {
		return nil, fmt.Errorf("%w: catalog version differs from target", ErrCompatibilityMismatch)
	}
	var found int64
	if err := e.Query.QueryRowContext(ctx, `SELECT count(*) FROM lake.snapshots() WHERE snapshot_id = ?`, snapshot.SnapshotID).Scan(&found); err != nil {
		return nil, fmt.Errorf("verify DuckLake snapshot: %w", err)
	}
	if found != 1 {
		return nil, fmt.Errorf("verify DuckLake snapshot: expected one exact snapshot, found %d", found)
	}
	evidence, err := json.Marshal(map[string]any{
		"catalog_snapshot":         snapshot.SnapshotID,
		"snapshot_verified":        true,
		"compatibility_digest":     compatibility.CompatibilityDigest,
		"catalog_type":             strings.TrimSpace(catalogType),
		"data_path":                actualDataPath,
		"duckdb_runtime":           actualRuntime,
		"ducklake_extension":       actualExtension,
		"ducklake_catalog_version": actualCatalogVersion,
		"metadata_schema":          config.MetadataSchema,
		"physical_pool_id":         snapshot.PhysicalPoolID,
	})
	if err != nil {
		return nil, err
	}
	return evidence, nil
}

// canonicalCatalogVersion compares DuckLake's options() GLOBAL "version"
// value (typically a bare value such as "1.1-dev1") with the platform's
// CatalogFormat tuple component ("ducklake:v1.1-dev1" or the equivalent
// "ducklake-catalog:" prefix). A leading v is presentation-only.
func canonicalCatalogVersion(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ErrCompatibilityMismatch
	}
	if i := strings.IndexByte(value, ':'); i >= 0 {
		prefix := value[:i]
		if prefix != "ducklake" && prefix != "ducklake-catalog" {
			return "", ErrCompatibilityMismatch
		}
		value = value[i+1:]
	}
	value = strings.TrimPrefix(value, "v")
	if value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return "", ErrCompatibilityMismatch
	}
	return value, nil
}

// canonicalRuntimeComponent gives version() and ducklake_settings() values a
// stable representation matching the platform compatibility tuple. DuckDB
// commonly prefixes versions with "v" while deployment tuples use
// "duckdb:<version>" / "ducklake:<version>".
func canonicalRuntimeComponent(prefix, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ErrCompatibilityMismatch
	}
	if i := strings.IndexByte(value, ':'); i >= 0 {
		if value[:i] != prefix {
			return "", ErrCompatibilityMismatch
		}
		value = value[i+1:]
	}
	value = strings.TrimPrefix(value, "v")
	if value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return "", ErrCompatibilityMismatch
	}
	return prefix + ":" + value, nil
}

func executeCatalogStatements(ctx context.Context, exec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, statements []string) error {
	for _, statement := range statements {
		if _, err := exec.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("DuckLake catalog statement failed: %w", err)
		}
	}
	return nil
}

func withBoundedRenewal(ctx context.Context, renew func(context.Context) error) error {
	if renew == nil {
		return ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	boundedCtx, cancel := context.WithTimeout(ctx, fenceRenewalTimeout)
	defer cancel()
	return renew(boundedCtx)
}

// ProvisionCatalogRuntimePrivileges grants only the exact metadata schema
// needed by ordinary runtime attachments. It intentionally does not grant
// CREATE/ALTER/DROP; migration remains owner-capable and separately fenced.
func ProvisionCatalogRuntimePrivileges(ctx context.Context, db DBTX, metadataSchema, runtimeRole string) error {
	if db == nil || !isSQLIdentifier(metadataSchema) || !isSQLIdentifier(runtimeRole) {
		return ErrInvalid
	}
	schema := quoteSQLIdentifier(metadataSchema)
	role := quoteSQLIdentifier(runtimeRole)
	for _, statement := range []string{
		fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO %s", schema, role),
		fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %s TO %s", schema, role),
		fmt.Sprintf("GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA %s TO %s", schema, role),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %s", schema, role),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO %s", schema, role),
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			return fmt.Errorf("provision DuckLake runtime schema privileges: %w", err)
		}
	}
	return nil
}

func isSQLIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		if (i == 0 && !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z')) || (i > 0 && !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

func quoteSQLIdentifier(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }

func (f CatalogExecutorFuncs) Identity(ctx context.Context) (DatabaseIdentity, error) {
	if f.IdentityFunc == nil {
		return DatabaseIdentity{}, ErrCatalogExecutor
	}
	return f.IdentityFunc(ctx)
}
func (f CatalogExecutorFuncs) Bootstrap(ctx context.Context, options CatalogBootstrapOptions) error {
	if f.BootstrapFunc == nil {
		return ErrCatalogExecutor
	}
	return f.BootstrapFunc(ctx, options)
}
func (f CatalogExecutorFuncs) Migrate(ctx context.Context, options CatalogMigrationOptions) error {
	if f.MigrateFunc == nil {
		return ErrCatalogExecutor
	}
	return f.MigrateFunc(ctx, options)
}
func (f CatalogExecutorFuncs) VerifySnapshot(ctx context.Context, snapshot SnapshotRef, compatibility RuntimeCompatibility) (json.RawMessage, error) {
	if f.VerifySnapshotFunc == nil {
		return nil, ErrCatalogExecutor
	}
	return f.VerifySnapshotFunc(ctx, snapshot, compatibility)
}

// UpgradeRequest contains all operator evidence needed to begin and terminally
// resolve one catalog migration. The coordinator discovers retained snapshots
// from control state rather than trusting a caller-provided list.
type UpgradeRequest struct {
	MigrationID    string
	PhysicalPoolID string
	CatalogID      string
	MetadataSchema string
	DataPath       string
	OwnerID        string
	Current        RuntimeCompatibility
	Target         RuntimeCompatibility
	LeaseExpiresAt time.Time

	// BeginEvidence must positively prove drain and backup verification. The
	// two booleans are merged into the evidence object when supplied.
	BeginEvidence      json.RawMessage
	DrainVerified      bool
	BackupVerified     bool
	CompletionEvidence json.RawMessage
	FailureEvidence    json.RawMessage
	DecisionEvidence   json.RawMessage
	RecoveryDecision   string
}

// UpgradeCoordinator binds a control authority repository and a separate
// catalog-migrator executor. ControlDB must be opened with the coordinator
// role; Catalog is checked against the independent catalog role/database.
type UpgradeCoordinator struct {
	Control   *Repository
	ControlDB DBTX
	Catalog   CatalogExecutor

	ControlDatabase string
	ControlRole     string
	CatalogDatabase string
	CatalogRole     string
}

func (c *UpgradeCoordinator) defaults() {
	if c.ControlDatabase == "" {
		c.ControlDatabase = DefaultControlDatabase
	}
	if c.ControlRole == "" {
		c.ControlRole = DefaultControlUpgradeCoordinatorRole
	}
	if c.CatalogDatabase == "" {
		c.CatalogDatabase = DefaultDuckLakeDatabase
	}
	if c.CatalogRole == "" {
		c.CatalogRole = DefaultDuckLakeCatalogMigratorRole
	}
}

func (c *UpgradeCoordinator) validate(ctx context.Context) error {
	if c == nil || c.Control == nil || c.ControlDB == nil || c.Catalog == nil {
		return ErrInvalid
	}
	c.defaults()
	controlIdentity, err := ReadDatabaseIdentity(ctx, c.ControlDB)
	if err != nil {
		return err
	}
	if err := ValidateDatabaseIdentity(controlIdentity, c.ControlDatabase, c.ControlRole); err != nil {
		return err
	}
	catalogIdentity, err := c.Catalog.Identity(ctx)
	if err != nil {
		return err
	}
	return ValidateDatabaseIdentity(catalogIdentity, c.CatalogDatabase, c.CatalogRole)
}

func beginEvidence(in UpgradeRequest) (json.RawMessage, error) {
	if len(in.BeginEvidence) != 0 {
		canonical, err := canonicalBeginEvidence(in.BeginEvidence)
		return json.RawMessage(canonical), err
	}
	if !in.DrainVerified || !in.BackupVerified {
		return nil, fmt.Errorf("%w: begin evidence must prove drain and backup verification", ErrMigrationEvidenceRequired)
	}
	encoded, err := json.Marshal(map[string]bool{"backup_verified": in.BackupVerified, "drain_verified": in.DrainVerified})
	if err != nil {
		return nil, err
	}
	canonical, err := canonicalBeginEvidence(encoded)
	return json.RawMessage(canonical), err
}

func operationEvidence(raw json.RawMessage, fallback map[string]any) (json.RawMessage, error) {
	if len(raw) != 0 {
		canonical, err := canonicalEvidence(raw)
		return json.RawMessage(canonical), err
	}
	encoded, err := json.Marshal(fallback)
	if err != nil {
		return nil, err
	}
	canonical, err := canonicalEvidence(encoded)
	return json.RawMessage(canonical), err
}

// Run performs an explicit existing-catalog migration, complete snapshot
// requalification, and terminal completion. Catalog initialization (including
// CREATE_IF_NOT_EXISTS) is a distinct operation on CatalogExecutor.Bootstrap;
// it is never replayed as a prelude to an upgrade. Every path releases both
// fences; if migration has begun, failures are persisted with an explicit
// rollback or forward-recovery decision before the original error is returned.
func (c *UpgradeCoordinator) Run(ctx context.Context, in UpgradeRequest) (migration CatalogMigration, runErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := c.validate(ctx); err != nil {
		return CatalogMigration{}, err
	}
	if !validUUID(in.MigrationID) || !validID(in.PhysicalPoolID) || !validID(in.CatalogID) || !validID(in.OwnerID) || in.Current.validate() != nil || in.Target.validate() != nil {
		return CatalogMigration{}, ErrInvalid
	}
	if in.RecoveryDecision != "rollback" && in.RecoveryDecision != "forward_recovery" {
		return CatalogMigration{}, ErrInvalid
	}
	if in.MetadataSchema != ducklake.MetadataSchemaForPool(in.PhysicalPoolID) {
		return CatalogMigration{}, fmt.Errorf("%w: metadata schema is not qualified for physical pool", ErrInvalid)
	}
	evidence, err := beginEvidence(in)
	if err != nil {
		return CatalogMigration{}, err
	}
	global, err := c.Control.AcquireMigrationFence(ctx, AcquireMigrationFenceInput{Scope: MigrationFenceGlobal, OwnerID: in.OwnerID, LeaseExpiresAt: in.LeaseExpiresAt})
	if err != nil {
		return CatalogMigration{}, err
	}
	releaseGlobal := true
	defer func() {
		if releaseGlobal {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if err := c.Control.ReleaseMigrationFence(cleanupCtx, global); err != nil {
				runErr = errors.Join(runErr, err)
			}
		}
	}()
	pool, err := c.Control.AcquireMigrationFence(ctx, AcquireMigrationFenceInput{Scope: MigrationFencePool, PhysicalPoolID: in.PhysicalPoolID, OwnerID: in.OwnerID, LeaseExpiresAt: in.LeaseExpiresAt})
	if err != nil {
		return CatalogMigration{}, err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := c.Control.ReleaseMigrationFence(cleanupCtx, pool); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}()

	// Record the durable running operation before any external catalog effect.
	// If migration loses its acknowledgement, recovery can inspect this row and
	// use the same migration ID rather than guessing whether DDL committed.
	migration, err = c.Control.BeginCatalogMigration(ctx, BeginCatalogMigrationInput{MigrationID: in.MigrationID, PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, GlobalFence: global, PoolFence: pool, Current: in.Current, Target: in.Target, Evidence: evidence})
	if err != nil {
		return CatalogMigration{}, err
	}
	operationCtx, operationCancel := context.WithCancel(ctx)
	defer operationCancel()
	heartbeatCtx, heartbeatCancel := context.WithCancel(context.Background())
	defer heartbeatCancel()
	heartbeatErr := make(chan error, 1)
	renew := func(renewCtx context.Context) error {
		// A wedged control database must not strand the heartbeat goroutine (or
		// let an operation continue indefinitely without a renewal result).
		return withBoundedRenewal(renewCtx, func(boundedCtx context.Context) error {
			return c.Control.RenewUpgradeFences(boundedCtx, global, pool, time.Now().UTC().Add(maxMigrationLease-time.Minute))
		})
	}
	// Renewal runs independently of the catalog executor. An adapter that
	// blocks inside one SQL statement cannot silently outlive its authority;
	// the operation context is canceled as soon as renewal loses the fence.
	go func() {
		interval := time.Until(global.LeaseExpiresAt) / 3
		if interval <= 0 || interval > 30*time.Second {
			interval = 30 * time.Second
		}
		if interval < 100*time.Millisecond {
			interval = 100 * time.Millisecond
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				if err := renew(context.Background()); err != nil {
					select {
					case heartbeatErr <- err:
					default:
					}
					operationCancel()
					return
				}
			}
		}
	}()
	checkHeartbeat := func() error {
		select {
		case err := <-heartbeatErr:
			return fmt.Errorf("%w: heartbeat lost migration fence: %v", ErrMigrationFenceExpired, err)
		default:
			return nil
		}
	}
	started := true
	failMigration := func(cause error) (CatalogMigration, error) {
		if !started {
			return CatalogMigration{}, cause
		}
		failure, evidenceErr := operationEvidence(in.FailureEvidence, map[string]any{"error_class": classifyUpgradeFailure(cause), "migration_id": in.MigrationID})
		if evidenceErr != nil {
			return migration, errors.Join(cause, evidenceErr)
		}
		decisionEvidence, evidenceErr := operationEvidence(in.DecisionEvidence, map[string]any{"recovery": recoveryDecision(in)})
		if evidenceErr != nil {
			return migration, errors.Join(cause, evidenceErr)
		}
		decision := recoveryDecision(in)
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		failed, failErr := c.Control.FailCatalogMigration(cleanupCtx, FailCatalogMigrationInput{MigrationID: in.MigrationID, GlobalFence: global, PoolFence: pool, Evidence: failure, RecoveryDecision: decision, DecisionEvidence: decisionEvidence})
		if failErr != nil {
			return migration, errors.Join(cause, failErr)
		}
		return failed, cause
	}
	// Existing-catalog upgrades use the dedicated automatic migration attach
	// directly. Running an initialize attach first would reject catalogs whose
	// current format differs from the runtime and make the upgrade unreachable.
	if err := c.Catalog.Migrate(operationCtx, CatalogMigrationOptions{PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, Current: in.Current, Target: in.Target, Mode: CatalogMigrationAutomatic, Renew: renew}); err != nil {
		return failMigration(err)
	}
	if err := checkHeartbeat(); err != nil {
		return failMigration(err)
	}
	if err := renew(operationCtx); err != nil {
		return failMigration(err)
	}
	retained, err := c.Control.ListRetainedSnapshots(operationCtx, in.PhysicalPoolID, in.CatalogID)
	if err != nil {
		return failMigration(err)
	}
	for _, snapshot := range retained {
		evidence, err := c.Catalog.VerifySnapshot(operationCtx, snapshot, in.Target)
		if err != nil {
			return failMigration(err)
		}
		if err := checkHeartbeat(); err != nil {
			return failMigration(err)
		}
		if _, err := canonicalEvidence(evidence); err != nil {
			return failMigration(fmt.Errorf("%w: snapshot verification evidence", ErrMigrationEvidenceRequired))
		}
		if _, err := c.Control.RequalifySnapshot(operationCtx, RequalifySnapshotInput{QualificationID: deterministicQualificationID(in.MigrationID, snapshot.SnapshotID), PhysicalPoolID: snapshot.PhysicalPoolID, CatalogID: snapshot.CatalogID, SnapshotID: snapshot.SnapshotID, MigrationID: in.MigrationID, GlobalFence: global, PoolFence: pool, Compatibility: in.Target, Evidence: evidence}); err != nil {
			return failMigration(err)
		}
	}
	completion, err := operationEvidence(in.CompletionEvidence, map[string]any{"catalog_migrated": true, "migration_id": in.MigrationID})
	if err != nil {
		return failMigration(err)
	}
	completed, err := c.Control.CompleteCatalogMigration(ctx, CompleteCatalogMigrationInput{MigrationID: in.MigrationID, GlobalFence: global, PoolFence: pool, Evidence: completion})
	if err != nil {
		return failMigration(err)
	}
	return completed, nil
}

func recoveryDecision(in UpgradeRequest) string {
	if in.RecoveryDecision == "forward_recovery" {
		return "forward_recovery"
	}
	return "rollback"
}

func classifyUpgradeFailure(err error) string {
	switch {
	case err == nil:
		return "unknown"
	case errors.Is(err, ErrMigrationFenceExpired):
		return "fence_expired"
	case errors.Is(err, ErrStaleFence):
		return "stale_fence"
	case errors.Is(err, ErrQualificationMissing):
		return "qualification_missing"
	case errors.Is(err, ErrQualificationRejected):
		return "qualification_rejected"
	case errors.Is(err, ErrCompatibilityMismatch):
		return "compatibility_mismatch"
	case errors.Is(err, ErrWrongDatabaseCredential):
		return "credential_mismatch"
	case errors.Is(err, ErrCatalogExecutor):
		return "catalog_executor"
	default:
		return "catalog_upgrade_failed"
	}
}

// deterministicQualificationID yields a stable UUID-shaped idempotency key
// without depending on a node wall clock. It is intentionally scoped to one
// migration and snapshot; retries replay the same evidence row.
func deterministicQualificationID(migrationID string, snapshotID int64) string {
	// UUID v5-like formatting over SHA-256 is collision-resistant enough for
	// the bounded idempotency key while preserving deterministic replay.
	seed := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", migrationID, snapshotID)))
	seed[6] = (seed[6] & 0x0f) | 0x50
	seed[8] = (seed[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(seed[0:4]), hex.EncodeToString(seed[4:6]), hex.EncodeToString(seed[6:8]), hex.EncodeToString(seed[8:10]), hex.EncodeToString(seed[10:16]))
}
