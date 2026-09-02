package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type captureCatalogSQL struct{ statements []string }

func (c *captureCatalogSQL) ExecContext(_ context.Context, statement string, _ ...any) (sql.Result, error) {
	c.statements = append(c.statements, statement)
	return nil, nil
}

type captureCatalogAdmin struct {
	identity             DatabaseIdentity
	schemaOwner          string
	catalogSchemaVersion string
	statements           []string
}

func (c *captureCatalogAdmin) Exec(_ context.Context, statement string, _ ...any) (pgconn.CommandTag, error) {
	c.statements = append(c.statements, statement)
	return pgconn.NewCommandTag("SELECT 1"), nil
}

func (c *captureCatalogAdmin) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("capture catalog admin Query is unused")
}

func (c *captureCatalogAdmin) QueryRow(_ context.Context, query string, _ ...any) pgx.Row {
	if strings.Contains(query, "ducklake_metadata") {
		version := c.catalogSchemaVersion
		if version == "" {
			version = "catalog-v1"
		}
		return captureCatalogRegistrationRow{version: version}
	}
	if strings.Contains(query, "FROM pg_namespace") {
		owner := c.schemaOwner
		if owner == "" {
			owner = c.identity.User
		}
		return captureCatalogSchemaOwnerRow{currentUser: c.identity.User, owner: owner}
	}
	return captureCatalogIdentityRow{identity: c.identity}
}

type captureCatalogIdentityRow struct{ identity DatabaseIdentity }

func (r captureCatalogIdentityRow) Scan(dest ...any) error {
	if len(dest) != 3 {
		return fmt.Errorf("identity scan received %d destinations", len(dest))
	}
	*(dest[0].(*string)) = r.identity.Database
	*(dest[1].(*string)) = r.identity.User
	*(dest[2].(*string)) = r.identity.SessionUser
	return nil
}

type captureCatalogSchemaOwnerRow struct{ currentUser, owner string }

func (r captureCatalogSchemaOwnerRow) Scan(dest ...any) error {
	if len(dest) != 2 {
		return fmt.Errorf("schema owner scan received %d destinations", len(dest))
	}
	*(dest[0].(*string)) = r.currentUser
	*(dest[1].(*string)) = r.owner
	return nil
}

type captureCatalogRegistrationRow struct{ version string }

func (r captureCatalogRegistrationRow) Scan(dest ...any) error {
	if len(dest) != 2 {
		return fmt.Errorf("catalog registration scan received %d destinations", len(dest))
	}
	*(dest[0].(*string)) = r.version
	*(dest[1].(*int64)) = 1
	return nil
}

type verifySQLState struct {
	version, catalogType, extensionVersion, dataPath, catalogVersion string
	snapshotID                                                       int64
}

type verifySQLDriver struct{ state verifySQLState }

func (d verifySQLDriver) Open(string) (driver.Conn, error) { return verifySQLConn{state: d.state}, nil }

type verifySQLConn struct{ state verifySQLState }

func (c verifySQLConn) Prepare(query string) (driver.Stmt, error) {
	return verifySQLStmt{state: c.state, query: query}, nil
}
func (verifySQLConn) Close() error              { return nil }
func (verifySQLConn) Begin() (driver.Tx, error) { return nil, errors.New("transactions are not used") }

type verifySQLStmt struct {
	state verifySQLState
	query string
}

func (s verifySQLStmt) Close() error                             { return nil }
func (verifySQLStmt) NumInput() int                              { return -1 }
func (verifySQLStmt) Exec([]driver.Value) (driver.Result, error) { return driver.RowsAffected(1), nil }
func (s verifySQLStmt) Query([]driver.Value) (driver.Rows, error) {
	switch {
	case strings.Contains(s.query, "SELECT version()"):
		return &verifySQLRows{columns: []string{"version"}, values: []driver.Value{s.state.version}}, nil
	case strings.Contains(s.query, "FROM lake.settings()"):
		return &verifySQLRows{columns: []string{"catalog_type", "extension_version", "data_path"}, values: []driver.Value{s.state.catalogType, s.state.extensionVersion, s.state.dataPath}}, nil
	case strings.Contains(s.query, "FROM lake.options()"):
		return &verifySQLRows{columns: []string{"value"}, values: []driver.Value{s.state.catalogVersion}}, nil
	case strings.Contains(s.query, "FROM lake.snapshots()"):
		return &verifySQLRows{columns: []string{"count"}, values: []driver.Value{s.state.snapshotID}}, nil
	default:
		return nil, fmt.Errorf("unexpected verification query: %s", s.query)
	}
}

type verifySQLRows struct {
	columns []string
	values  []driver.Value
	done    bool
}

func (r *verifySQLRows) Columns() []string { return r.columns }
func (*verifySQLRows) Close() error        { return nil }
func (r *verifySQLRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	copy(dest, r.values)
	return nil
}

func TestSQLCatalogExecutorVerifySnapshotInspectsRuntimeAndSettings(t *testing.T) {
	driverName := "leapview-verify-catalog"
	sql.Register(driverName, verifySQLDriver{state: verifySQLState{version: "v1.6.0", catalogType: "postgres", extensionVersion: "v0.4.0", dataPath: "s3://Bucket/lake/", catalogVersion: "v2", snapshotID: 1}})
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	adapter := &SQLCatalogExecutor{Exec: db, Query: db, DataPath: "s3://bucket/lake", DuckLakeSecret: "lake_secret", PostgresSecret: "pg_secret"}
	target := RuntimeCompatibility{RuntimeTuple: RuntimeTuple{DuckDBRuntime: "duckdb:1.6.0", DuckLakeExtension: "ducklake:0.4.0", CatalogFormat: "ducklake:v2"}, CompatibilityDigest: digest('b'), CatalogSchemaVersion: "catalog-v2"}
	evidence, err := adapter.VerifySnapshot(t.Context(), SnapshotRef{PhysicalPoolID: "pool-verify", CatalogID: "catalog-verify", SnapshotID: 1}, target)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(evidence, &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{"catalog_type": "postgres", "data_path": "s3://bucket/lake", "duckdb_runtime": "duckdb:1.6.0", "ducklake_extension": "ducklake:0.4.0", "ducklake_catalog_version": "2"} {
		if got[key] != want {
			t.Fatalf("verification evidence %s=%v, want %v", key, got[key], want)
		}
	}
}

func TestSQLCatalogExecutorVerifySnapshotAcceptsZeroMinorCatalogVersion(t *testing.T) {
	driverName := "leapview-verify-catalog-zero-minor"
	sql.Register(driverName, verifySQLDriver{state: verifySQLState{
		version: "v1.5.4", catalogType: "postgres", extensionVersion: "d318a545",
		dataPath: "s3://Bucket/lake/", catalogVersion: "1.0", snapshotID: 1,
	}})
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	adapter := &SQLCatalogExecutor{Exec: db, Query: db, DataPath: "s3://bucket/lake", DuckLakeSecret: "lake_secret", PostgresSecret: "pg_secret"}
	target := RuntimeCompatibility{RuntimeTuple: RuntimeTuple{
		DuckDBRuntime: "duckdb:1.5.4", DuckLakeExtension: "ducklake:d318a545", CatalogFormat: "ducklake-catalog:v1",
	}, CompatibilityDigest: digest('c'), CatalogSchemaVersion: "catalog-v1"}
	evidence, err := adapter.VerifySnapshot(t.Context(), SnapshotRef{PhysicalPoolID: "pool-zero-minor", CatalogID: "catalog-zero-minor", SnapshotID: 1}, target)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(evidence, &got); err != nil {
		t.Fatal(err)
	}
	if got["ducklake_catalog_version"] != "1" {
		t.Fatalf("verification catalog version = %v, want canonical major 1", got["ducklake_catalog_version"])
	}
}

func TestCanonicalCatalogVersionRejectsUnqualifiedFormats(t *testing.T) {
	for _, test := range []struct {
		value, want string
	}{
		{value: "v1.1-dev1", want: "1.1-dev1"},
		{value: "ducklake:v2", want: "2"},
		{value: "ducklake-catalog:v3", want: "3"},
	} {
		got, err := canonicalCatalogVersion(test.value)
		if err != nil || got != test.want {
			t.Fatalf("canonical catalog version %q = %q, err=%v", test.value, got, err)
		}
	}
	if _, err := canonicalCatalogVersion("format:v2"); !errors.Is(err, ErrCompatibilityMismatch) {
		t.Fatalf("unqualified catalog format error = %v", err)
	}
}

func TestCanonicalDataPathMatchesDuckLakeNormalization(t *testing.T) {
	got, err := ducklake.CanonicalDataPath("s3://Bucket/lake/")
	if err != nil || got != "s3://bucket/lake" {
		t.Fatalf("canonical S3 data path = %q, err=%v", got, err)
	}
	localInput := filepath.Join(".", "pool", "..", "lake")
	got, err = ducklake.CanonicalDataPath(localInput)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs("lake")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(want) {
		t.Fatalf("canonical local data path = %q, want %q", got, filepath.Clean(want))
	}
}

func TestProvisionCatalogMaintenancePrivilegesConformance(t *testing.T) {
	h := postgrestest.Start(t)
	catalogRole := h.EnsureRole(t, postgrestest.Role{Name: "ducklake_maintenance_owner", Password: "catalog-secret", Login: true})
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "ducklake_maintenance_runtime", Password: "runtime-secret", Login: true})
	maintenanceRole := h.EnsureRole(t, postgrestest.Role{Name: "ducklake_maintenance", Password: "maintenance-secret", Login: true})
	catalogDB := h.NewDatabase(t, "ducklake_maintenance_privilege_test")
	controlDB := h.NewDatabase(t, "ducklake_maintenance_control_test")
	catalogAdmin, err := pgxpool.New(t.Context(), catalogDB.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(catalogAdmin.Close)
	if _, err := catalogAdmin.Exec(t.Context(), `REVOKE ALL ON DATABASE ducklake_maintenance_privilege_test FROM PUBLIC`); err != nil {
		t.Fatal(err)
	}
	if _, err := catalogAdmin.Exec(t.Context(), `GRANT CONNECT, CREATE ON DATABASE ducklake_maintenance_privilege_test TO ducklake_maintenance_owner`); err != nil {
		t.Fatal(err)
	}
	if _, err := catalogAdmin.Exec(t.Context(), `GRANT CONNECT ON DATABASE ducklake_maintenance_privilege_test TO ducklake_maintenance_runtime, ducklake_maintenance`); err != nil {
		t.Fatal(err)
	}
	controlAdmin, err := pgxpool.New(t.Context(), controlDB.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controlAdmin.Close)
	if _, err := controlAdmin.Exec(t.Context(), `REVOKE ALL ON DATABASE ducklake_maintenance_control_test FROM PUBLIC`); err != nil {
		t.Fatal(err)
	}
	catalogOwnerDB, err := pgxpool.New(t.Context(), catalogDB.URL(catalogRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(catalogOwnerDB.Close)
	maintenanceDB, err := pgxpool.New(t.Context(), catalogDB.URL(maintenanceRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(maintenanceDB.Close)
	runtimeDB, err := pgxpool.New(t.Context(), catalogDB.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	metadataSchema := ducklake.MetadataSchemaForPool("maintenance-conformance")
	if err := EnsureCatalogMetadataSchema(t.Context(), catalogOwnerDB, metadataSchema); err != nil {
		t.Fatal(err)
	}
	if err := ProvisionCatalogMaintenancePrivileges(t.Context(), catalogOwnerDB, metadataSchema, maintenanceRole.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := catalogOwnerDB.Exec(t.Context(), `CREATE TABLE "`+metadataSchema+`".probe (id integer)`); err != nil {
		t.Fatal(err)
	}
	if _, err := catalogOwnerDB.Exec(t.Context(), `CREATE SEQUENCE "`+metadataSchema+`".probe_seq`); err != nil {
		t.Fatal(err)
	}
	if _, err := maintenanceDB.Exec(t.Context(), `INSERT INTO "`+metadataSchema+`".probe VALUES (nextval('"`+metadataSchema+`".probe_seq'))`); err != nil {
		t.Fatalf("maintenance metadata DML rejected: %v", err)
	}
	if _, err := maintenanceDB.Exec(t.Context(), `UPDATE "`+metadataSchema+`".probe SET id = id + 1`); err != nil {
		t.Fatalf("maintenance metadata UPDATE rejected: %v", err)
	}
	if _, err := maintenanceDB.Exec(t.Context(), `DELETE FROM "`+metadataSchema+`".probe`); err != nil {
		t.Fatalf("maintenance metadata DELETE rejected: %v", err)
	}
	for name, statement := range map[string]string{
		"create table": `CREATE TABLE "` + metadataSchema + `".forbidden (id integer)`,
		"alter table":  `ALTER TABLE "` + metadataSchema + `".probe ADD COLUMN forbidden integer`,
		"drop table":   `DROP TABLE "` + metadataSchema + `".probe`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := maintenanceDB.Exec(t.Context(), statement); err == nil {
				t.Fatalf("maintenance role unexpectedly executed %s", name)
			}
		})
	}
	if _, err := catalogAdmin.Exec(t.Context(), `CREATE SCHEMA other_schema`); err != nil {
		t.Fatal(err)
	}
	if _, err := catalogAdmin.Exec(t.Context(), `CREATE TABLE other_schema.secret (value text)`); err != nil {
		t.Fatal(err)
	}
	if _, err := maintenanceDB.Exec(t.Context(), `SELECT * FROM other_schema.secret`); err == nil {
		t.Fatal("maintenance role unexpectedly accessed another schema")
	}
	if err := runtimeDB.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	controlMaintenanceDB, err := pgxpool.New(t.Context(), controlDB.URL(maintenanceRole))
	if err != nil {
		t.Fatal(err)
	}
	if err := controlMaintenanceDB.Ping(t.Context()); err == nil {
		t.Fatal("maintenance role unexpectedly connected to control database")
	}
	controlMaintenanceDB.Close()
}

func TestValidateDatabaseIdentityRejectsSwappedCredentials(t *testing.T) {
	identity := DatabaseIdentity{Database: DefaultControlDatabase, User: DefaultControlUpgradeCoordinatorRole, SessionUser: DefaultControlUpgradeCoordinatorRole}
	if err := ValidateDatabaseIdentity(identity, DefaultControlDatabase, DefaultControlUpgradeCoordinatorRole); err != nil {
		t.Fatalf("valid coordinator identity rejected: %v", err)
	}
	for _, bad := range []DatabaseIdentity{
		{Database: DefaultDuckLakeDatabase, User: DefaultControlUpgradeCoordinatorRole, SessionUser: DefaultControlUpgradeCoordinatorRole},
		{Database: DefaultControlDatabase, User: DefaultControlMigratorRole, SessionUser: DefaultControlMigratorRole},
		{Database: DefaultControlDatabase, User: DefaultControlUpgradeCoordinatorRole, SessionUser: DefaultControlMigratorRole},
	} {
		if err := ValidateDatabaseIdentity(bad, DefaultControlDatabase, DefaultControlUpgradeCoordinatorRole); !errors.Is(err, ErrWrongDatabaseCredential) {
			t.Fatalf("swapped identity %#v error = %v", bad, err)
		}
	}
}

func TestWithBoundedRenewalAlwaysSetsDeadline(t *testing.T) {
	called := false
	if err := withBoundedRenewal(context.Background(), func(ctx context.Context) error {
		called = true
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.After(time.Now()) {
			t.Fatalf("renewal context has no future deadline: %v", deadline)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("renew callback was not invoked")
	}
}

func TestUpgradeEvidenceAndExecutorUsesTypedMigrationMode(t *testing.T) {
	evidence, err := beginEvidence(UpgradeRequest{DrainVerified: true, BackupVerified: true})
	if err != nil || !strings.Contains(string(evidence), "backup_verified") {
		t.Fatalf("begin evidence = %s, err=%v", evidence, err)
	}
	if _, err := beginEvidence(UpgradeRequest{DrainVerified: true}); !errors.Is(err, ErrMigrationEvidenceRequired) {
		t.Fatalf("incomplete begin evidence error = %v", err)
	}
	var bootstrap, migrate bool
	executor := CatalogExecutorFuncs{
		IdentityFunc: func(context.Context) (DatabaseIdentity, error) {
			return DatabaseIdentity{Database: DefaultDuckLakeDatabase, User: DefaultDuckLakeCatalogMigratorRole, SessionUser: DefaultDuckLakeCatalogMigratorRole}, nil
		},
		BootstrapFunc: func(_ context.Context, options CatalogBootstrapOptions) error {
			bootstrap = true
			if options.Mode != CatalogBootstrapInitialize {
				t.Fatal("bootstrap did not use initialize mode")
			}
			return nil
		},
		MigrateFunc: func(_ context.Context, options CatalogMigrationOptions) error {
			migrate = true
			if options.Mode != CatalogMigrationAutomatic {
				t.Fatal("migration did not use explicit automatic mode")
			}
			return nil
		},
		VerifySnapshotFunc: func(context.Context, SnapshotRef, RuntimeCompatibility) (json.RawMessage, error) {
			return json.RawMessage(`{"verified":true}`), nil
		},
	}
	if err := executor.Bootstrap(t.Context(), CatalogBootstrapOptions{Mode: CatalogBootstrapInitialize}); err != nil {
		t.Fatal(err)
	}
	if err := executor.Migrate(t.Context(), CatalogMigrationOptions{Mode: CatalogMigrationAutomatic}); err != nil {
		t.Fatal(err)
	}
	if !bootstrap || !migrate {
		t.Fatal("executor callbacks were not invoked")
	}
}

func TestDeterministicQualificationIDIsStableUUID(t *testing.T) {
	first := deterministicQualificationID("0198f2c0-7c7a-7f00-8a11-000000000001", 41)
	if first != deterministicQualificationID("0198f2c0-7c7a-7f00-8a11-000000000001", 41) {
		t.Fatal("qualification id changed across retries")
	}
	if first == deterministicQualificationID("0198f2c0-7c7a-7f00-8a11-000000000001", 42) || len(first) != 36 {
		t.Fatalf("qualification id = %q", first)
	}
}

func TestSQLCatalogExecutorUsesTypedAttachConstructors(t *testing.T) {
	exec := &captureCatalogSQL{}
	admin := &captureCatalogAdmin{identity: DatabaseIdentity{Database: DefaultDuckLakeDatabase, User: DefaultDuckLakeCatalogMigratorRole, SessionUser: DefaultDuckLakeCatalogMigratorRole}}
	adapter := &SQLCatalogExecutor{Exec: exec, CatalogAdmin: admin, RuntimeRole: "leapview_ducklake_runtime", DuckLakeSecret: "lake_secret", PostgresSecret: "pg_secret", DataPath: "s3://bucket/lake"}
	poolID := "pool-adapter"
	missingAdmin := *adapter
	missingAdmin.CatalogAdmin = nil
	if err := missingAdmin.Bootstrap(t.Context(), CatalogBootstrapOptions{PhysicalPoolID: poolID, CatalogID: "catalog-adapter", MetadataSchema: ducklake.MetadataSchemaForPool(poolID), Mode: CatalogBootstrapInitialize}); !errors.Is(err, ErrCatalogExecutor) {
		t.Fatalf("bootstrap without catalog admin error = %v", err)
	}
	wrongAdmin := *adapter
	wrongAdmin.CatalogAdmin = &captureCatalogAdmin{identity: DatabaseIdentity{Database: DefaultControlDatabase, User: DefaultControlUpgradeCoordinatorRole, SessionUser: DefaultControlUpgradeCoordinatorRole}}
	if err := wrongAdmin.Bootstrap(t.Context(), CatalogBootstrapOptions{PhysicalPoolID: poolID, CatalogID: "catalog-adapter", MetadataSchema: ducklake.MetadataSchemaForPool(poolID), Mode: CatalogBootstrapInitialize}); !errors.Is(err, ErrWrongDatabaseCredential) {
		t.Fatalf("bootstrap with swapped catalog admin error = %v", err)
	}
	wrongOwner := *adapter
	wrongOwner.CatalogAdmin = &captureCatalogAdmin{identity: admin.identity, schemaOwner: "leapview_ducklake_owner"}
	if err := EnsureCatalogMetadataSchema(t.Context(), wrongOwner.CatalogAdmin, ducklake.MetadataSchemaForPool(poolID)); !errors.Is(err, ErrWrongDatabaseCredential) {
		t.Fatalf("metadata schema with wrong owner error = %v", err)
	}
	if err := adapter.Bootstrap(t.Context(), CatalogBootstrapOptions{PhysicalPoolID: poolID, CatalogID: "catalog-adapter", MetadataSchema: ducklake.MetadataSchemaForPool(poolID), Mode: CatalogBootstrapInitialize}); err != nil {
		t.Fatal(err)
	}
	compat := RuntimeCompatibility{RuntimeTuple: RuntimeTuple{DuckDBRuntime: "duckdb:1.0", DuckLakeExtension: "ducklake:1.0", CatalogFormat: "ducklake:v1"}, CompatibilityDigest: digest('a'), CatalogSchemaVersion: "catalog-v1"}
	if err := adapter.Migrate(t.Context(), CatalogMigrationOptions{PhysicalPoolID: poolID, CatalogID: "catalog-adapter", Current: compat, Target: compat, Mode: CatalogMigrationAutomatic}); err != nil {
		t.Fatal(err)
	}
	if len(exec.statements) != 6 || !strings.Contains(exec.statements[1], "AUTOMATIC_MIGRATION false") || !strings.Contains(exec.statements[1], "CREATE_IF_NOT_EXISTS true") || exec.statements[2] != `DETACH "lake"` || !strings.Contains(exec.statements[4], "AUTOMATIC_MIGRATION true") || !strings.Contains(exec.statements[4], "CREATE_IF_NOT_EXISTS false") || exec.statements[5] != `DETACH "lake"` {
		t.Fatalf("catalog statements = %#v", exec.statements)
	}
	mismatch := *adapter
	mismatch.CatalogAdmin = &captureCatalogAdmin{identity: admin.identity, catalogSchemaVersion: "catalog-v2"}
	if err := mismatch.Migrate(t.Context(), CatalogMigrationOptions{PhysicalPoolID: poolID, CatalogID: "catalog-adapter", Current: compat, Target: compat, Mode: CatalogMigrationAutomatic}); !errors.Is(err, ErrCompatibilityMismatch) {
		t.Fatalf("mismatched migrated catalog version error = %v", err)
	}
}

type fakeUpgradeCatalog struct {
	identity      DatabaseIdentity
	bootstrap     int
	migrate       int
	verified      []int64
	renewed       int
	delay         time.Duration
	skipRenew     bool
	bootstrapErr  error
	failMigration error
	failVerifyAt  int
	cancel        context.CancelFunc
}

func (f *fakeUpgradeCatalog) Identity(context.Context) (DatabaseIdentity, error) {
	return f.identity, nil
}
func (f *fakeUpgradeCatalog) Bootstrap(_ context.Context, options CatalogBootstrapOptions) error {
	if options.Mode != CatalogBootstrapInitialize {
		return ErrAutomaticMigration
	}
	f.bootstrap++
	return f.bootstrapErr
}
func (f *fakeUpgradeCatalog) Migrate(ctx context.Context, options CatalogMigrationOptions) error {
	if options.Mode != CatalogMigrationAutomatic {
		return ErrAutomaticMigration
	}
	f.migrate++
	if options.Renew != nil && !f.skipRenew {
		if err := options.Renew(ctx); err != nil {
			return err
		}
		f.renewed++
	}
	if f.delay > 0 {
		timer := time.NewTimer(f.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	if f.cancel != nil {
		f.cancel()
		return context.Canceled
	}
	return f.failMigration
}
func (f *fakeUpgradeCatalog) VerifySnapshot(_ context.Context, snapshot SnapshotRef, _ RuntimeCompatibility) (json.RawMessage, error) {
	f.verified = append(f.verified, snapshot.SnapshotID)
	if f.failVerifyAt > 0 && len(f.verified) == f.failVerifyAt {
		return nil, errors.New("snapshot verification failed")
	}
	return json.RawMessage(`{"snapshot_verified":true}`), nil
}

// This conformance test uses real PostgreSQL authority roles and a fake
// catalog executor. It exercises the complete cross-database sequencing
// without requiring the optional DuckLake extension in CI.
func TestUpgradeCoordinatorRunSequencingAndRecovery(t *testing.T) {
	h := postgrestest.Start(t)
	coordinatorRole := h.EnsureRole(t, postgrestest.Role{Name: DefaultControlUpgradeCoordinatorRole, Password: "coordinator-secret", Login: true})
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "runtime-secret", Login: true})
	catalogRole := h.EnsureRole(t, postgrestest.Role{Name: DefaultDuckLakeCatalogMigratorRole, Password: "catalog-secret", Login: true})
	ducklakeRuntimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_ducklake_runtime", Password: "ducklake-runtime-secret", Login: true})
	db := h.NewDatabase(t, "ducklake_upgrade_coordinator_run_test")
	catalogDB := h.NewDatabase(t, "ducklake_upgrade_catalog_run_test")
	admin, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	catalogAdmin, err := pgxpool.New(t.Context(), catalogDB.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(catalogAdmin.Close)
	if _, err := admin.Exec(t.Context(), `REVOKE ALL ON DATABASE ducklake_upgrade_coordinator_run_test FROM PUBLIC`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `GRANT CONNECT ON DATABASE ducklake_upgrade_coordinator_run_test TO leapview_control_upgrade_coordinator`); err != nil {
		t.Fatal(err)
	}
	if _, err := catalogAdmin.Exec(t.Context(), `REVOKE ALL ON DATABASE ducklake_upgrade_catalog_run_test FROM PUBLIC`); err != nil {
		t.Fatal(err)
	}
	if _, err := catalogAdmin.Exec(t.Context(), `GRANT CONNECT, CREATE ON DATABASE ducklake_upgrade_catalog_run_test TO leapview_ducklake_migrator`); err != nil {
		t.Fatal(err)
	}
	if _, err := catalogAdmin.Exec(t.Context(), `GRANT CONNECT ON DATABASE ducklake_upgrade_catalog_run_test TO leapview_ducklake_runtime`); err != nil {
		t.Fatal(err)
	}
	tx, err := admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	coordinatorDB, err := pgxpool.New(t.Context(), db.URL(coordinatorRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(coordinatorDB.Close)
	runtimeDB, err := pgxpool.New(t.Context(), db.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	catalogRoleDB, err := pgxpool.New(t.Context(), catalogDB.URL(catalogRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(catalogRoleDB.Close)
	catalogRuntimeDB, err := pgxpool.New(t.Context(), catalogDB.URL(ducklakeRuntimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(catalogRuntimeDB.Close)
	metadataSchema := ducklake.MetadataSchemaForPool("coord-success")
	wrongOwnerSchema := ducklake.MetadataSchemaForPool("coord-wrong-owner")
	if _, err := catalogAdmin.Exec(t.Context(), `CREATE SCHEMA "`+wrongOwnerSchema+`" AUTHORIZATION postgres`); err != nil {
		t.Fatal(err)
	}
	if err := EnsureCatalogMetadataSchema(t.Context(), catalogRoleDB, wrongOwnerSchema); !errors.Is(err, ErrWrongDatabaseCredential) {
		t.Fatalf("wrong-owner metadata schema error = %v", err)
	}
	if err := EnsureCatalogMetadataSchema(t.Context(), catalogRoleDB, metadataSchema); err != nil {
		t.Fatal(err)
	}
	if err := ProvisionCatalogRuntimePrivileges(t.Context(), catalogRoleDB, metadataSchema, ducklakeRuntimeRole.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := catalogRoleDB.Exec(t.Context(), `CREATE TABLE "`+metadataSchema+`".probe (id integer)`); err != nil {
		t.Fatal(err)
	}
	var usage, create bool
	if err := catalogRuntimeDB.QueryRow(t.Context(), `SELECT has_schema_privilege(current_user,$1,'USAGE'), has_schema_privilege(current_user,$1,'CREATE')`, metadataSchema).Scan(&usage, &create); err != nil {
		t.Fatal(err)
	}
	if !usage || create {
		t.Fatalf("runtime schema privileges usage=%v create=%v", usage, create)
	}
	if _, err := catalogRuntimeDB.Exec(t.Context(), `INSERT INTO "`+metadataSchema+`".probe VALUES (1)`); err != nil {
		t.Fatalf("runtime metadata DML rejected: %v", err)
	}
	if identity, err := ReadDatabaseIdentity(t.Context(), coordinatorDB); err != nil {
		t.Fatal(err)
	} else if err := ValidateDatabaseIdentity(identity, db.Name, coordinatorRole.Name); err != nil {
		t.Fatal(err)
	}
	if identity, err := ReadDatabaseIdentity(t.Context(), catalogRoleDB); err != nil {
		t.Fatal(err)
	} else if err := ValidateDatabaseIdentity(identity, catalogDB.Name, catalogRole.Name); err != nil {
		t.Fatal(err)
	}
	swappedCatalog, err := pgxpool.New(t.Context(), db.URL(catalogRole))
	if err != nil {
		t.Fatal(err)
	}
	if err := swappedCatalog.Ping(t.Context()); err == nil {
		t.Fatal("DuckLake catalog migrator unexpectedly connected to control database")
	}
	swappedCatalog.Close()

	current := RuntimeCompatibility{RuntimeTuple: RuntimeTuple{DuckDBRuntime: "duckdb:1.5", DuckLakeExtension: "ducklake:0.3", CatalogFormat: "ducklake:v1"}, CompatibilityDigest: digest('a'), CatalogSchemaVersion: "catalog-v1"}
	target := RuntimeCompatibility{RuntimeTuple: RuntimeTuple{DuckDBRuntime: "duckdb:1.6", DuckLakeExtension: "ducklake:0.4", CatalogFormat: "ducklake:v2"}, CompatibilityDigest: digest('b'), CatalogSchemaVersion: "catalog-v2"}
	setup := func(t *testing.T, suffix string) {
		t.Helper()
		poolID, catalogID := "coord-"+suffix, "catalog-"+suffix
		if _, err := New(admin).RegisterCatalog(t.Context(), CatalogIdentity{PhysicalPoolID: poolID, CatalogDatabase: catalogDB.Name, CatalogID: catalogID, CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000000014", MetadataSchema: ducklake.MetadataSchemaForPool(poolID)}); err != nil {
			t.Fatal(err)
		}
		for _, snapshotID := range []int64{1, 2} {
			if err := ensureSnapshotLive(t.Context(), admin, SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: snapshotID}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := admin.Exec(t.Context(), `UPDATE ducklake.snapshot_retention SET state='retiring', retired_at=clock_timestamp() WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=2`, poolID, catalogID); err != nil {
			t.Fatal(err)
		}
	}
	run := func(t *testing.T, suffix string, catalog *fakeUpgradeCatalog) (CatalogMigration, error) {
		t.Helper()
		poolID, catalogID := "coord-"+suffix, "catalog-"+suffix
		migrationIDs := map[string]string{"success": "0198f2c0-7c7a-7f00-0000-000000000401", "catalog-failure": "0198f2c0-7c7a-7f00-0000-000000000402", "verify-failure": "0198f2c0-7c7a-7f00-0000-000000000403", "canceled": "0198f2c0-7c7a-7f00-0000-000000000404", "direct-migrate": "0198f2c0-7c7a-7f00-0000-000000000406"}
		return (&UpgradeCoordinator{Control: New(coordinatorDB), ControlDB: coordinatorDB, Catalog: catalog, ControlDatabase: db.Name}).Run(t.Context(), UpgradeRequest{MigrationID: migrationIDs[suffix], PhysicalPoolID: poolID, CatalogID: catalogID, MetadataSchema: ducklake.MetadataSchemaForPool(poolID), OwnerID: "operator-1", Current: current, Target: target, DrainVerified: true, BackupVerified: true, RecoveryDecision: "rollback", LeaseExpiresAt: time.Now().UTC().Add(5 * time.Minute)})
	}

	t.Run("success requalifies exact retained set and releases fences", func(t *testing.T) {
		setup(t, "success")
		catalog := &fakeUpgradeCatalog{identity: DatabaseIdentity{Database: DefaultDuckLakeDatabase, User: DefaultDuckLakeCatalogMigratorRole, SessionUser: DefaultDuckLakeCatalogMigratorRole}}
		migration, err := run(t, "success", catalog)
		if err != nil || migration.State != "completed" {
			t.Fatalf("migration=%#v err=%v", migration, err)
		}
		if len(catalog.verified) != 2 || catalog.verified[0] != 1 || catalog.verified[1] != 2 || catalog.renewed == 0 {
			t.Fatalf("verified snapshots=%v renewals=%d", catalog.verified, catalog.renewed)
		}
		var held int
		if err := admin.QueryRow(t.Context(), `SELECT count(*) FROM ducklake.migration_fence WHERE owner_id IS NOT NULL`).Scan(&held); err != nil || held != 0 {
			t.Fatalf("held fences=%d err=%v", held, err)
		}
	})

	t.Run("existing catalog upgrade skips initialize attach", func(t *testing.T) {
		setup(t, "direct-migrate")
		catalog := &fakeUpgradeCatalog{identity: DatabaseIdentity{Database: DefaultDuckLakeDatabase, User: DefaultDuckLakeCatalogMigratorRole, SessionUser: DefaultDuckLakeCatalogMigratorRole}, bootstrapErr: errors.New("initialize attach rejected old catalog format")}
		migration, err := run(t, "direct-migrate", catalog)
		if err != nil || migration.State != "completed" || catalog.bootstrap != 0 || catalog.migrate != 1 {
			t.Fatalf("direct migration=%#v err=%v bootstrap=%d migrate=%d", migration, err, catalog.bootstrap, catalog.migrate)
		}
	})

	t.Run("recovery decision is explicit", func(t *testing.T) {
		setup(t, "missing-decision")
		catalog := &fakeUpgradeCatalog{identity: DatabaseIdentity{Database: DefaultDuckLakeDatabase, User: DefaultDuckLakeCatalogMigratorRole, SessionUser: DefaultDuckLakeCatalogMigratorRole}}
		coord := &UpgradeCoordinator{Control: New(coordinatorDB), ControlDB: coordinatorDB, Catalog: catalog, ControlDatabase: db.Name}
		_, err := coord.Run(t.Context(), UpgradeRequest{MigrationID: "0198f2c0-7c7a-7f00-0000-000000000407", PhysicalPoolID: "coord-missing-decision", CatalogID: "catalog-missing-decision", MetadataSchema: ducklake.MetadataSchemaForPool("coord-missing-decision"), OwnerID: "operator-1", Current: current, Target: target, DrainVerified: true, BackupVerified: true})
		if !errors.Is(err, ErrInvalid) || catalog.migrate != 0 {
			t.Fatalf("missing recovery decision err=%v migrate=%d", err, catalog.migrate)
		}
	})

	t.Run("catalog failure records bounded rollback evidence", func(t *testing.T) {
		setup(t, "catalog-failure")
		catalog := &fakeUpgradeCatalog{identity: DatabaseIdentity{Database: DefaultDuckLakeDatabase, User: DefaultDuckLakeCatalogMigratorRole, SessionUser: DefaultDuckLakeCatalogMigratorRole}, failMigration: errors.New("sensitive catalog path must not persist")}
		migration, err := run(t, "catalog-failure", catalog)
		if err == nil || migration.State != "failed" || string(migration.FailureEvidence) == "" || strings.Contains(string(migration.FailureEvidence), "sensitive catalog path") {
			t.Fatalf("migration=%#v err=%v", migration, err)
		}
	})

	t.Run("independent heartbeat renews a short lease while executor blocks", func(t *testing.T) {
		setup(t, "heartbeat")
		catalog := &fakeUpgradeCatalog{identity: DatabaseIdentity{Database: DefaultDuckLakeDatabase, User: DefaultDuckLakeCatalogMigratorRole, SessionUser: DefaultDuckLakeCatalogMigratorRole}, delay: 400 * time.Millisecond, skipRenew: true}
		coord := &UpgradeCoordinator{Control: New(coordinatorDB), ControlDB: coordinatorDB, Catalog: catalog, ControlDatabase: db.Name}
		migration, err := coord.Run(t.Context(), UpgradeRequest{MigrationID: "0198f2c0-7c7a-7f00-0000-000000000405", PhysicalPoolID: "coord-heartbeat", CatalogID: "catalog-heartbeat", MetadataSchema: ducklake.MetadataSchemaForPool("coord-heartbeat"), OwnerID: "operator-1", Current: current, Target: target, DrainVerified: true, BackupVerified: true, RecoveryDecision: "rollback", LeaseExpiresAt: time.Now().UTC().Add(250 * time.Millisecond)})
		if err != nil || migration.State != "completed" {
			t.Fatalf("heartbeat migration=%#v err=%v", migration, err)
		}
	})

	t.Run("verification failure and canceled context still terminalize", func(t *testing.T) {
		setup(t, "verify-failure")
		catalog := &fakeUpgradeCatalog{identity: DatabaseIdentity{Database: DefaultDuckLakeDatabase, User: DefaultDuckLakeCatalogMigratorRole, SessionUser: DefaultDuckLakeCatalogMigratorRole}, failVerifyAt: 1}
		migration, err := run(t, "verify-failure", catalog)
		if err == nil || migration.State != "failed" {
			t.Fatalf("verification migration=%#v err=%v", migration, err)
		}

		setup(t, "canceled")
		cancelCtx, cancel := context.WithCancel(t.Context())
		catalog = &fakeUpgradeCatalog{identity: DatabaseIdentity{Database: DefaultDuckLakeDatabase, User: DefaultDuckLakeCatalogMigratorRole, SessionUser: DefaultDuckLakeCatalogMigratorRole}, cancel: cancel}
		coord := &UpgradeCoordinator{Control: New(coordinatorDB), ControlDB: coordinatorDB, Catalog: catalog, ControlDatabase: db.Name}
		migration, err = coord.Run(cancelCtx, UpgradeRequest{MigrationID: "0198f2c0-7c7a-7f00-0000-000000000404", PhysicalPoolID: "coord-canceled", CatalogID: "catalog-canceled", MetadataSchema: ducklake.MetadataSchemaForPool("coord-canceled"), OwnerID: "operator-1", Current: current, Target: target, DrainVerified: true, BackupVerified: true, RecoveryDecision: "rollback", LeaseExpiresAt: time.Now().UTC().Add(5 * time.Minute)})
		if err == nil || migration.State != "failed" {
			t.Fatalf("canceled migration=%#v err=%v", migration, err)
		}
	})

	t.Run("swapped control credential rejected before bootstrap", func(t *testing.T) {
		setup(t, "swapped")
		catalog := &fakeUpgradeCatalog{identity: DatabaseIdentity{Database: DefaultDuckLakeDatabase, User: DefaultDuckLakeCatalogMigratorRole, SessionUser: DefaultDuckLakeCatalogMigratorRole}}
		coord := &UpgradeCoordinator{Control: New(coordinatorDB), ControlDB: catalogRoleDB, Catalog: catalog, ControlDatabase: db.Name}
		_, err := coord.Run(t.Context(), UpgradeRequest{MigrationID: "0198f2c0-7c7a-7f00-8a11-000000000403", PhysicalPoolID: "coord-swapped", CatalogID: "catalog-swapped", OwnerID: "operator-1", Current: current, Target: target, RecoveryDecision: "rollback", BackupVerified: true, DrainVerified: true})
		if !errors.Is(err, ErrWrongDatabaseCredential) || catalog.bootstrap != 0 {
			t.Fatalf("swapped credentials err=%v bootstrap=%d", err, catalog.bootstrap)
		}
	})
	var held int
	if err := admin.QueryRow(t.Context(), `SELECT count(*) FROM ducklake.migration_fence WHERE owner_id IS NOT NULL`).Scan(&held); err != nil || held != 0 {
		t.Fatalf("coordinator leaked migration fences=%d err=%v", held, err)
	}
}
