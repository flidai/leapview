//go:build integration && duckdb_arrow

package ducklake

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestPostgresDuckLakeRuntimeLifecycle is the real PostgreSQL + DuckDB
// conformance lane. It is opt-in because it owns a disposable PostgreSQL 18
// container; CI enables it for the required persistence contract.
func TestPostgresDuckLakeRuntimeLifecycle(t *testing.T) {
	ctx := context.Background()
	h := postgrestest.StartTLS(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "ducklake_owner"})
	migrator := h.EnsureRole(t, postgrestest.Role{Name: "ducklake_migrator", Password: "ducklake-migrator-secret", Login: true})
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "ducklake_runtime", Password: "ducklake-runtime-secret", Login: true})
	h.GrantRole(t, owner, migrator)
	db := h.NewDatabase(t, "ducklake_runtime_test")
	h.GrantDatabase(t, db.Name, migrator, "CONNECT", "CREATE", "TEMPORARY")
	h.GrantDatabase(t, db.Name, runtime, "CONNECT")

	dataPath := filepath.Join(t.TempDir(), "data")
	contract := fixturePoolContractFor(t, "local", dataPath)
	poolID := contract.Pool.ID.String()
	metadataSchema := MetadataSchemaForPool(poolID)
	admin, err := sql.Open("pgx", postgresTLSURL(t, db.AdminURL()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if _, err := admin.ExecContext(ctx, "REVOKE ALL ON DATABASE \""+db.Name+"\" FROM PUBLIC"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, "REVOKE ALL ON SCHEMA public FROM PUBLIC"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA \""+metadataSchema+"\" AUTHORIZATION \""+migrator.Name+"\""); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, "GRANT USAGE, CREATE ON SCHEMA \""+metadataSchema+"\" TO \""+migrator.Name+"\""); err != nil {
		t.Fatal(err)
	}
	extensionAdmission := runtimeExtensionAdmission(t)
	initialize := PostgresCatalogConfig{PhysicalPoolID: poolID, DuckLakeSecret: "leapview_lake", PostgresSecret: "leapview_pg_migrator", MetadataSchema: metadataSchema, DataPath: dataPath, Mode: PostgresCatalogInitialize}
	// Only the dedicated catalog migrator may initialize DuckLake metadata;
	// the per-pool schema is owned by that migrator, as in production.
	bootstrap := postgresCatalogCredentialBootstrap(t, db, migrator, initialize.PostgresSecret)
	env, err := Open(ctx, Config{RootDir: t.TempDir(), PoolContract: contract, PhysicalPoolID: poolID, PostgresCatalog: &initialize, CredentialBootstrap: bootstrap, ExtensionAdmission: extensionAdmission, MaxConnections: 2})
	if err != nil {
		if extensionUnavailable(err) || !postgrestest.Required() {
			t.Skipf("PostgreSQL DuckLake extension unavailable: %v", err)
		}
		t.Fatal(err)
	}
	defer env.Close()
	// Keep two physical DuckDB sessions live at once. Each connector must
	// provision its own temporary PostgreSQL secret and attach the same
	// metadata schema; a single pooled connection would not prove that.
	connA, err := env.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connA.Close()
	connB, err := env.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connB.Close()
	for name, conn := range map[string]*sql.Conn{"a": connA, "b": connB} {
		var catalogType string
		if err := conn.QueryRowContext(ctx, "SELECT catalog_type FROM lake.settings() LIMIT 1").Scan(&catalogType); err != nil {
			t.Fatalf("query attached PostgreSQL catalog on connection %s: %v", name, err)
		}
		if catalogType != "postgres" {
			t.Fatalf("connection %s catalog type=%q, want postgres", name, catalogType)
		}
	}
	// Release both held pool slots before Commit, which must acquire one of
	// the same two physical sessions for the transactional write.
	if err := connA.Close(); err != nil {
		t.Fatal(err)
	}
	if err := connB.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Commit(ctx, "init", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS model; CREATE TABLE model.events(id BIGINT); INSERT INTO model.events VALUES (1)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_ = env.Close()

	migratorDB, err := sql.Open("pgx", postgresTLSURL(t, db.URL(migrator)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migratorDB.Close() })
	for _, statement := range []string{
		"ALTER DEFAULT PRIVILEGES FOR ROLE \"" + migrator.Name + "\" IN SCHEMA \"" + metadataSchema + "\" GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO \"" + runtime.Name + "\"",
		"ALTER DEFAULT PRIVILEGES FOR ROLE \"" + migrator.Name + "\" IN SCHEMA \"" + metadataSchema + "\" GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO \"" + runtime.Name + "\"",
	} {
		if _, err := migratorDB.ExecContext(ctx, statement); err != nil {
			t.Fatalf("provision runtime default metadata privileges: %v", err)
		}
	}

	// Runtime receives only the metadata USAGE/DML required by ordinary
	// DuckLake attaches. It has no database/schema CREATE capability; catalog
	// schema changes remain owner/migrator operations.
	for _, statement := range []string{
		"GRANT USAGE ON SCHEMA \"" + metadataSchema + "\" TO \"" + runtime.Name + "\"",
		"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA \"" + metadataSchema + "\" TO \"" + runtime.Name + "\"",
		"GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA \"" + metadataSchema + "\" TO \"" + runtime.Name + "\"",
	} {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatalf("provision runtime metadata privileges: %v", err)
		}
	}
	runtimeDB, err := sql.Open("pgx", postgresTLSURL(t, db.URL(runtime)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtimeDB.Close() })
	var currentUser, currentDatabase string
	var schemaUsage, schemaCreate, databaseCreate bool
	if err := runtimeDB.QueryRowContext(ctx, `SELECT current_user, current_database(), has_schema_privilege(current_user, $1, 'USAGE'), has_schema_privilege(current_user, $1, 'CREATE'), has_database_privilege(current_user, current_database(), 'CREATE')`, metadataSchema).Scan(&currentUser, &currentDatabase, &schemaUsage, &schemaCreate, &databaseCreate); err != nil {
		t.Fatal(err)
	}
	if currentUser != runtime.Name || currentDatabase != db.Name {
		t.Fatalf("runtime PostgreSQL identity user=%q database=%q, want %q/%q", currentUser, currentDatabase, runtime.Name, db.Name)
	}
	if !schemaUsage || schemaCreate || databaseCreate {
		t.Fatalf("runtime PostgreSQL privileges usage=%v schema_create=%v database_create=%v", schemaUsage, schemaCreate, databaseCreate)
	}
	for _, role := range []postgrestest.Role{owner, migrator} {
		if _, err := runtimeDB.ExecContext(ctx, "SET ROLE \""+role.Name+"\""); err == nil {
			t.Fatalf("runtime role unexpectedly assumed PostgreSQL role %q", role.Name)
		}
	}
	if _, err := runtimeDB.ExecContext(ctx, "CREATE SCHEMA \""+metadataSchema+"_forbidden\""); err == nil {
		t.Fatal("runtime role unexpectedly created a PostgreSQL schema")
	}
	if _, err := runtimeDB.ExecContext(ctx, "CREATE TABLE \""+metadataSchema+"\".runtime_forbidden(id BIGINT)"); err == nil {
		t.Fatal("runtime role unexpectedly created a catalog table")
	}

	requestDigest := digestForRuntimeTest("request")
	planDigest := digestForRuntimeTest("plan")
	marker := CommitMarker{SchemaVersion: CommitMarkerSchemaVersion, DeliveryID: "delivery", GenerationID: "generation", AttemptID: "attempt", LeaseEpoch: 1, RequestDigest: requestDigest, PlanDigest: planDigest, Project: "project", Environment: "prod", PhysicalPoolID: poolID}
	writer := initialize
	writer.Mode = PostgresCatalogWriter
	writer.DataPath = ""
	writer.PostgresSecret = "leapview_pg_runtime"
	runtimeBootstrap := postgresCatalogCredentialBootstrap(t, db, runtime, writer.PostgresSecret)
	writerEnv, err := Open(ctx, Config{RootDir: t.TempDir(), PoolContract: contract, PhysicalPoolID: poolID, PostgresCatalog: &writer, CommitMarker: &marker, CredentialBootstrap: runtimeBootstrap, ExtensionAdmission: extensionAdmission, MaxConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := writerEnv.Commit(ctx, "ignored", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO model.events VALUES (2)")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// Commit reconciliation uses a fresh database/sql handle. Closing that
	// handle must not close the environment-owned DuckDB connector; prove the
	// writer remains usable after the exact-marker lookup completes.
	if err := writerEnv.ValidateSnapshot(ctx, snapshot); err != nil {
		t.Fatalf("writer connector after commit reconciliation: %v", err)
	}
	_ = writerEnv.Close()
	// A fresh DuckDB session has no connection-local last_committed_snapshot;
	// reconciliation must scan persistent commit_extra_info by exact marker.
	writerRestart, err := Open(ctx, Config{RootDir: t.TempDir(), PoolContract: contract, PhysicalPoolID: poolID, PostgresCatalog: &writer, CommitMarker: &marker, CredentialBootstrap: runtimeBootstrap, ExtensionAdmission: extensionAdmission, MaxConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveCommittedSnapshot(ctx, writerRestart.sqlDB(), marker); err != nil || got != snapshot {
		t.Fatalf("exact marker reconciliation got=%d err=%v snapshot=%d", got, err, snapshot)
	}
	if _, err := writerRestart.SnapshotSealEvidence(ctx, snapshot); err != nil {
		t.Fatalf("snapshot seal evidence: %v", err)
	}
	closureEvidence, err := writerRestart.NativeSnapshotClosureEvidence(ctx, NativeSnapshotClosureRequest{CatalogID: "catalog-runtime", SnapshotID: snapshot, ObjectRoot: dataPath, RelationNamespace: "model"})
	if err != nil {
		t.Fatalf("native snapshot closure evidence: %v", err)
	}
	if closureEvidence.CatalogID != "catalog-runtime" || closureEvidence.SnapshotID != snapshot || closureEvidence.ObjectRoot == "" || closureEvidence.RelationManifestDigest == "" || closureEvidence.ClosureDigest == "" || closureEvidence.ObjectRootDigest == "" || len(closureEvidence.CanonicalJSON) == 0 {
		t.Fatalf("native snapshot closure evidence is incomplete: %#v", closureEvidence)
	}
	_ = writerRestart.Close()

	serving := writer
	serving.Mode = PostgresCatalogServing
	serving.SnapshotVersion = snapshot
	servingEnv, err := Open(ctx, Config{RootDir: t.TempDir(), PoolContract: contract, PhysicalPoolID: poolID, PostgresCatalog: &serving, CredentialBootstrap: runtimeBootstrap, ExtensionAdmission: extensionAdmission, MaxConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer servingEnv.Close()
	// Hold both physical sessions while querying the immutable snapshot. This
	// proves the serving ATTACH (including SNAPSHOT_VERSION) is available on
	// every pooled connector rather than only on the first warm-up connection.
	servingConnA, err := servingEnv.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer servingConnA.Close()
	servingConnB, err := servingEnv.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer servingConnB.Close()
	relation, err := QualifiedSnapshotRelation(snapshot, "events")
	if err != nil {
		t.Fatal(err)
	}
	for name, conn := range map[string]*sql.Conn{"a": servingConnA, "b": servingConnB} {
		var rows int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+relation).Scan(&rows); err != nil {
			t.Fatalf("query pinned DuckLake snapshot on connection %s: %v", name, err)
		}
		if rows != 2 {
			t.Fatalf("connection %s snapshot rows=%d, want 2", name, rows)
		}
	}
	if err := servingConnA.Close(); err != nil {
		t.Fatal(err)
	}
	if err := servingConnB.Close(); err != nil {
		t.Fatal(err)
	}
	if err := servingEnv.ValidateSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := servingEnv.Commit(ctx, "forbidden", nil, func(*sql.Tx) error { return nil }); err != ErrReadOnlyEnvironment {
		t.Fatalf("serving commit error=%v, want read-only", err)
	}
}

func postgresCatalogCredentialBootstrap(t *testing.T, database *postgrestest.Database, role postgrestest.Role, secretName string) CredentialBootstrap {
	t.Helper()
	parsed, err := url.Parse(postgresTLSURL(t, database.URL(role)))
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
		statement := fmt.Sprintf("CREATE OR REPLACE TEMPORARY SECRET %s (TYPE postgres, HOST '%s', PORT %d, DATABASE '%s', USER '%s', PASSWORD '%s', SSLMODE 'require')", quoteCatalogIdentifier(secretName), sqlLiteral(parsed.Hostname()), port, sqlLiteral(parsed.Path[1:]), sqlLiteral(parsed.User.Username()), sqlLiteral(password))
		_, err := execer.ExecContext(ctx, statement, nil)
		return err
	}
}

func postgresTLSURL(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse PostgreSQL conformance URL: %v", err)
	}
	query := parsed.Query()
	query.Set("sslmode", "require")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
