//go:build integration && duckdb_arrow

package postgres

import (
	"context"
	"database/sql/driver"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	extensionfixture "github.com/flidai/leapview/internal/app/testing/extensionfixture"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPostgresCatalogUpgradeExistingCatalog exercises the real cross-database
// A→B path: DuckLake creates an existing PostgreSQL catalog and snapshot,
// then UpgradeCoordinator uses SQLCatalogExecutor and a dedicated DuckDB
// session to run the fenced automatic-migration attach and requalify that
// retained snapshot. It is opt-in because it owns a PostgreSQL 18 container
// and requires the reviewed DuckLake extension artifact.
func TestPostgresCatalogUpgradeExistingCatalog(t *testing.T) {
	ctx := t.Context()
	h := postgrestest.Start(t)
	coordinatorRole := h.EnsureRole(t, postgrestest.Role{Name: DefaultControlUpgradeCoordinatorRole, Password: "upgrade-coordinator-secret", Login: true})
	catalogRole := h.EnsureRole(t, postgrestest.Role{Name: DefaultDuckLakeCatalogMigratorRole, Password: "upgrade-catalog-secret", Login: true})
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_ducklake_runtime", Password: "upgrade-runtime-secret", Login: true})
	controlDB := h.NewDatabase(t, "ducklake_catalog_upgrade_control_test")
	catalogDB := h.NewDatabase(t, "ducklake_catalog_upgrade_catalog_test")
	h.GrantDatabase(t, controlDB.Name, coordinatorRole, "CONNECT")
	h.GrantDatabase(t, catalogDB.Name, catalogRole, "CONNECT", "CREATE", "TEMPORARY")
	h.GrantDatabase(t, catalogDB.Name, runtimeRole, "CONNECT")

	controlAdmin, err := pgxpool.New(ctx, controlDB.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controlAdmin.Close)
	tx, err := controlAdmin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	coordinatorDBConn, err := pgxpool.New(ctx, controlDB.URL(coordinatorRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(coordinatorDBConn.Close)
	catalogAdmin, err := pgxpool.New(ctx, catalogDB.URL(catalogRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(catalogAdmin.Close)
	catalogBootstrapAdmin, err := pgxpool.New(ctx, catalogDB.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(catalogBootstrapAdmin.Close)

	poolID := "catalog-upgrade-real-pool"
	catalogID := "catalog-upgrade-real"
	metadataSchema := ducklake.MetadataSchemaForPool(poolID)
	if _, err := catalogBootstrapAdmin.Exec(ctx, `CREATE SCHEMA "`+metadataSchema+`" AUTHORIZATION "`+catalogRole.Name+`"`); err != nil {
		t.Fatal(err)
	}
	dataPath := filepath.Join(t.TempDir(), "ducklake-data")
	fixture := extensionfixture.New(t, "ducklake")
	credentialBootstrap := postgresCatalogUpgradeCredentialBootstrap(t, catalogDB, catalogRole)
	openSession := func() *ducklake.PostgresCatalogUpgradeSession {
		session, openErr := ducklake.OpenPostgresCatalogUpgradeSession(ctx, ducklake.PostgresCatalogUpgradeSessionConfig{
			DataPath: dataPath, TempDir: filepath.Join(t.TempDir(), "duckdb-tmp"),
			MemoryMaxBytes: 256 << 20, TempMaxBytes: 512 << 20, MaxThreads: 2,
			ExtensionAdmission: fixture.Admission, CredentialBootstrap: credentialBootstrap,
		})
		if openErr != nil {
			if !postgrestest.Required() {
				t.Skipf("DuckLake extension unavailable: %v", openErr)
			}
			t.Fatal(openErr)
		}
		return session
	}

	// Create a genuine existing PostgreSQL catalog and one retained snapshot
	// through the same bounded DuckDB session shape used by migration.
	session := openSession()
	t.Cleanup(func() { _ = session.Close() })
	initialConfig := ducklake.PostgresCatalogConfig{
		PhysicalPoolID: poolID, DuckLakeSecret: "lake_upgrade_secret", PostgresSecret: "pg_upgrade_secret",
		MetadataSchema: metadataSchema, DataPath: dataPath, Mode: ducklake.PostgresCatalogInitialize,
	}
	initialStatements, err := initialConfig.Statements()
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range initialStatements {
		if _, err := session.Conn().ExecContext(ctx, statement); err != nil {
			if !postgrestest.Required() {
				t.Skipf("DuckLake PostgreSQL catalog unavailable: %v", err)
			}
			t.Fatalf("initialize existing PostgreSQL catalog: %v", err)
		}
	}
	if _, err := session.Conn().ExecContext(ctx, `USE lake`); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Conn().ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS model`); err != nil {
		t.Fatal(err)
	}
	transaction, err := session.Conn().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `CREATE TABLE model.events (id BIGINT); INSERT INTO model.events VALUES (1)`); err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	var snapshotID int64
	if err := session.Conn().QueryRowContext(ctx, `SELECT snapshot_id FROM lake.snapshots() ORDER BY snapshot_id DESC LIMIT 1`).Scan(&snapshotID); err != nil {
		t.Fatal(err)
	}
	if snapshotID <= 0 {
		t.Fatalf("DuckLake committed snapshot = %d, want positive", snapshotID)
	}
	var duckdbVersion, extensionVersion, catalogOption string
	if err := session.Conn().QueryRowContext(ctx, `SELECT version()`).Scan(&duckdbVersion); err != nil {
		t.Fatal(err)
	}
	if err := session.Conn().QueryRowContext(ctx, `SELECT extension_version FROM lake.settings() LIMIT 1`).Scan(&extensionVersion); err != nil {
		t.Fatal(err)
	}
	if err := session.Conn().QueryRowContext(ctx, `SELECT value FROM lake.options() WHERE lower(option_name)='version' AND upper(scope)='GLOBAL' LIMIT 1`).Scan(&catalogOption); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Conn().ExecContext(ctx, `USE memory`); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Conn().ExecContext(ctx, `DETACH "lake"`); err != nil {
		t.Fatal(err)
	}
	var metadataVersion string
	if err := catalogAdmin.QueryRow(ctx, `SELECT value FROM "`+metadataSchema+`".ducklake_metadata WHERE key='version' AND scope IS NULL AND scope_id IS NULL LIMIT 1`).Scan(&metadataVersion); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(metadataVersion) == "" || strings.TrimSpace(catalogOption) == "" {
		t.Fatalf("DuckLake catalog versions are empty: metadata=%q option=%q", metadataVersion, catalogOption)
	}

	current := RuntimeCompatibility{
		RuntimeTuple: RuntimeTuple{
			DuckDBRuntime:     "duckdb:" + strings.TrimPrefix(strings.TrimSpace(duckdbVersion), "v"),
			DuckLakeExtension: "ducklake:" + strings.TrimPrefix(strings.TrimSpace(extensionVersion), "v"),
			CatalogFormat:     "ducklake:" + strings.TrimPrefix(strings.TrimSpace(catalogOption), "v"),
		},
		CompatibilityDigest: digest('a'), CatalogSchemaVersion: metadataVersion,
	}
	target := current
	target.CompatibilityDigest = digest('b')
	identity := CatalogIdentity{
		PhysicalPoolID: poolID, CatalogDatabase: catalogDB.Name, CatalogID: catalogID,
		CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000000801", MetadataSchema: metadataSchema,
	}
	if _, _, err := BootstrapCatalog(ctx, controlAdmin, identity, current); err != nil {
		t.Fatal(err)
	}
	if err := ensureSnapshotLive(ctx, controlAdmin, SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: snapshotID}); err != nil {
		t.Fatal(err)
	}
	executor := &SQLCatalogExecutor{
		IdentityDB: catalogAdmin, Exec: session.Conn(), Query: session.Conn(),
		DuckLakeSecret: "lake_upgrade_secret", PostgresSecret: "pg_upgrade_secret", DataPath: dataPath,
		CatalogAdmin: catalogAdmin, RuntimeRole: runtimeRole.Name, CatalogDatabase: catalogDB.Name, CatalogRole: catalogRole.Name,
	}
	const migrationID = "0198f2c0-7c7a-7f00-8a11-000000000802"
	migration, err := (&UpgradeCoordinator{
		Control: New(coordinatorDBConn), ControlDB: coordinatorDBConn, Catalog: executor,
		ControlDatabase: controlDB.Name, ControlRole: coordinatorRole.Name,
		CatalogDatabase: catalogDB.Name, CatalogRole: catalogRole.Name,
	}).Run(ctx, UpgradeRequest{
		MigrationID: migrationID, PhysicalPoolID: poolID, CatalogID: catalogID, MetadataSchema: metadataSchema,
		OwnerID: "catalog-upgrade-operator", Current: current, Target: target,
		DrainVerified: true, BackupVerified: true, RecoveryDecision: "rollback", LeaseExpiresAt: time.Now().UTC().Add(5 * time.Minute),
	})
	if err != nil || migration.State != "completed" {
		t.Fatalf("existing catalog migration=%#v err=%v", migration, err)
	}
	updated, err := LoadCatalogRuntimeCompatibility(ctx, controlAdmin, poolID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.RuntimeCompatibility != target || updated.CurrentMigrationID != migrationID {
		t.Fatalf("mutable catalog runtime after A→B migration=%#v, want target=%#v migration=%s", updated.RuntimeCompatibility, target, migrationID)
	}
	stable, err := LoadCatalog(ctx, controlAdmin, poolID)
	if err != nil {
		t.Fatal(err)
	}
	if !sameCatalog(stable, identity) {
		t.Fatalf("stable catalog identity changed across upgrade: got=%#v want=%#v", stable, identity)
	}
}

func postgresCatalogUpgradeCredentialBootstrap(t *testing.T, database *postgrestest.Database, role postgrestest.Role) ducklake.CredentialBootstrap {
	t.Helper()
	parsed, err := url.Parse(database.URL(role))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	password, _ := parsed.User.Password()
	return func(ctx context.Context, execer driver.ExecerContext) error {
		if _, err := execer.ExecContext(ctx, "INSTALL postgres_scanner FROM core", nil); err != nil {
			return err
		}
		if _, err := execer.ExecContext(ctx, "LOAD postgres_scanner", nil); err != nil {
			return err
		}
		secret := fmt.Sprintf("CREATE OR REPLACE TEMPORARY SECRET pg_upgrade_secret (TYPE postgres, HOST '%s', PORT %d, DATABASE '%s', USER '%s', PASSWORD '%s', SSLMODE 'disable')", sqlLiteralForUpgrade(parsed.Hostname()), port, sqlLiteralForUpgrade(parsed.Path[1:]), sqlLiteralForUpgrade(parsed.User.Username()), sqlLiteralForUpgrade(password))
		_, err := execer.ExecContext(ctx, secret, nil)
		return err
	}
}

func sqlLiteralForUpgrade(value string) string { return strings.ReplaceAll(value, "'", "''") }
