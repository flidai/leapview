package platform

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestManagedDataMigrationCreatesProjectDeploymentsWithoutLegacyRollouts(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer store.Close()

	for _, table := range []string{"project_deployments", "managed_data_environment_pointers", "managed_data_serving_state_bindings", "managed_data_serving_state_binding_sets"} {
		assertTableCount(t, ctx, store, table, 1)
	}
	for _, table := range []string{"managed_data_rollouts", "managed_data_rollout_targets"} {
		assertTableCount(t, ctx, store, table, 0)
	}
	var projectColumnCount int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('serving_states') WHERE name = 'project_id' AND type = 'TEXT' AND "notnull" = 1`).Scan(&projectColumnCount); err != nil {
		t.Fatalf("inspect serving state project scope: %v", err)
	}
	if projectColumnCount != 1 {
		t.Fatalf("serving state project scope column count = %d, want 1", projectColumnCount)
	}

	var pointerDDL string
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'managed_data_environment_pointers'`).Scan(&pointerDDL); err != nil {
		t.Fatalf("inspect environment pointer schema: %v", err)
	}
	if !containsAll(pointerDDL, "deployment_id", "project_deployments") || containsAll(pointerDDL, "rollout_id") {
		t.Fatalf("unexpected environment pointer schema: %s", pointerDDL)
	}
}

// TestDeliveryMigrationChainIsContiguousAndRestartSafe keeps the embedded
// Goose chain's current tail explicit. A fresh install and a second Open both
// apply/verify every migration through 094; a missing or duplicated sequence
// entry would either fail the numeric assertion or leave one of the tail
// columns absent after restart.
func TestDeliveryMigrationChainIsContiguousAndRestartSafe(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "delivery-migrations.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open fresh migrated store: %v", err)
	}
	assertDeliveryMigrationTail(t, ctx, store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen migrated store: %v", err)
	}
	defer restarted.Close()
	assertDeliveryMigrationTail(t, ctx, restarted)
}

// TestDeliveryMigrationUpgradeFrom072 exercises the real upgrade path rather
// than only a fresh install: seed a database through the last pre-delivery
// migration, then let Open apply 073..094 in one restart-safe upgrade.
func TestDeliveryMigrationUpgradeFrom072(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "delivery-upgrade.db")
	legacy, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatalf("open pre-delivery database: %v", err)
	}
	legacy.SetMaxOpenConns(1)
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		legacy.Close()
		t.Fatalf("set migration dialect: %v", err)
	}
	if err := goose.UpToContext(ctx, legacy, "migrations", 72); err != nil {
		legacy.Close()
		t.Fatalf("seed pre-delivery migrations: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close pre-delivery database: %v", err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("upgrade pre-delivery database: %v", err)
	}
	defer store.Close()
	assertDeliveryMigrationTail(t, ctx, store)
}

// TestDashboardPublicationRelayMigrationUpgradeFromPredecessor proves the
// narrow FAI-596 forward migration removes only the legacy relay after the
// migration-093 predecessor. The publication history and stream registry
// tables/indexes remain available for the SQLite compatibility path.
func TestDashboardPublicationRelayMigrationUpgradeFromPredecessor(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dashboard-publication-relay-upgrade.db")
	legacy, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatalf("open migration predecessor: %v", err)
	}
	legacy.SetMaxOpenConns(1)
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		legacy.Close()
		t.Fatalf("set migration dialect: %v", err)
	}
	if err := goose.UpToContext(ctx, legacy, "migrations", 93); err != nil {
		legacy.Close()
		t.Fatalf("seed migration-093 predecessor: %v", err)
	}
	seedDashboardPublicationRelayPredecessor(t, ctx, legacy)
	assertDashboardPublicationRelayPresent(t, ctx, legacy)
	if err := legacy.Close(); err != nil {
		t.Fatalf("close migration predecessor: %v", err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("upgrade migration-093 predecessor: %v", err)
	}
	assertDashboardPublicationRelayRemoved(t, ctx, store)
	assertDashboardPublicationRows(t, ctx, store)
	if err := store.Close(); err != nil {
		t.Fatalf("close upgraded store: %v", err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen upgraded store: %v", err)
	}
	defer reopened.Close()
	assertDashboardPublicationRelayRemoved(t, ctx, reopened)
	assertDashboardPublicationRows(t, ctx, reopened)
}

func TestRefreshPipelineContractMigrationDropsLegacyExecutionState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "occurrence-identity-upgrade.db")
	legacy, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	legacy.SetMaxOpenConns(1)
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if err := goose.UpToContext(ctx, legacy, "migrations", 89); err != nil {
		legacy.Close()
		t.Fatalf("seed migration 089: %v", err)
	}
	for _, generation := range []string{"generation-old", "generation-new"} {
		if _, err := legacy.ExecContext(ctx, `INSERT INTO serving_states (id, project_id, environment, status) VALUES (?, 'project-sales', 'prod', 'superseded')`, generation); err != nil {
			legacy.Close()
			t.Fatalf("insert serving state %s: %v", generation, err)
		}
	}
	if _, err := legacy.ExecContext(ctx, `INSERT INTO refresh_jobs (
		id, project_id, generation_id, semantic_model_id, pipeline_id, principal_id,
		group_ids_json, estimated_memory_bytes, status
	) VALUES ('job-attached', 'project-sales', 'generation-new', 'semantic-sales',
		'pipeline-sales', 'scheduler', '[]', 1, 'completed')`); err != nil {
		legacy.Close()
		t.Fatalf("insert refresh job: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `INSERT INTO refresh_job_runs (
		id, job_id, status, target_type, target_id, environment, created_sequence
	) VALUES ('run-attached', 'job-attached', 'completed', 'refresh_pipeline',
		'pipeline-sales', 'prod', 1)`); err != nil {
		legacy.Close()
		t.Fatalf("insert refresh run: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `INSERT INTO refresh_pipeline_occurrences (
		project_id, environment, pipeline_id, generation_id, artifact_digest,
		scheduled_at, run_id, claimed_at
	) VALUES
		('project-sales', 'prod', 'pipeline-sales', 'generation-old', 'sha256:old', '2026-08-20T06:00:00Z', NULL, '2026-08-20T06:00:01Z'),
		('project-sales', 'prod', 'pipeline-sales', 'generation-new', 'sha256:new', '2026-08-20T06:00:00Z', 'run-attached', '2026-08-20T06:00:02Z')`); err != nil {
		legacy.Close()
		t.Fatalf("insert duplicate logical occurrences: %v", err)
	}
	if err := goose.UpToContext(ctx, legacy, "migrations", 90); err != nil {
		legacy.Close()
		t.Fatalf("apply occurrence identity migration: %v", err)
	}
	for table, want := range map[string]int{"refresh_jobs": 0, "refresh_job_runs": 0, "refresh_pipeline_occurrences": 0, "refresh_pipeline_schedules": 0} {
		var count int
		if err := legacy.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
			legacy.Close()
			t.Fatal(err)
		}
		if count != want {
			legacy.Close()
			t.Fatalf("%s rows after refresh contract migration = %d, want %d", table, count, want)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryQualificationLedgerMigrationUpDownAndLegacyUpgrade(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "recovery-ledger-upgrade.db")
	legacy, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	legacy.SetMaxOpenConns(1)
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(ctx, legacy, "migrations", 92); err != nil {
		t.Fatalf("seed legacy migration 092: %v", err)
	}
	for _, table := range []string{
		"recovery_qualification_schedules", "recovery_qualification_occurrences",
		"recovery_qualification_attempts", "recovery_qualification_evidence_attempts",
	} {
		assertSQLTableCount(t, ctx, legacy, table, 0)
	}
	if err := goose.UpToContext(ctx, legacy, "migrations", 93); err != nil {
		t.Fatalf("upgrade legacy database to recovery ledger: %v", err)
	}
	for _, table := range []string{
		"recovery_qualification_schedules", "recovery_qualification_occurrences",
		"recovery_qualification_attempts", "recovery_qualification_evidence_attempts",
	} {
		assertSQLTableCount(t, ctx, legacy, table, 1)
	}
	if err := goose.DownToContext(ctx, legacy, "migrations", 92); err != nil {
		t.Fatalf("downgrade recovery ledger migration: %v", err)
	}
	for _, table := range []string{
		"recovery_qualification_schedules", "recovery_qualification_occurrences",
		"recovery_qualification_attempts", "recovery_qualification_evidence_attempts",
	} {
		assertSQLTableCount(t, ctx, legacy, table, 0)
	}
	if err := goose.UpToContext(ctx, legacy, "migrations", 93); err != nil {
		t.Fatalf("reapply recovery ledger migration: %v", err)
	}
}

func assertDeliveryMigrationTail(t *testing.T, ctx context.Context, store *Store) {
	t.Helper()
	var latest int64
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COALESCE(max(version_id), 0) FROM goose_db_version WHERE is_applied = 1`).Scan(&latest); err != nil {
		t.Fatalf("inspect applied Goose migrations: %v", err)
	}
	if latest != 94 {
		t.Fatalf("latest applied migration = %d, want 94", latest)
	}
	rows, err := store.SQLDB().QueryContext(ctx, `
		SELECT version_id
		FROM goose_db_version
		WHERE is_applied = 1 AND version_id BETWEEN 73 AND 94
		ORDER BY version_id`)
	if err != nil {
		t.Fatalf("inspect applied delivery migration sequence: %v", err)
	}
	defer rows.Close()
	for want := int64(73); want <= 94; want++ {
		if !rows.Next() {
			t.Fatalf("applied delivery migration sequence ended before %d", want)
		}
		var got int64
		if err := rows.Scan(&got); err != nil {
			t.Fatalf("scan applied delivery migration %d: %v", want, err)
		}
		if got != want {
			t.Fatalf("applied delivery migration sequence contains %d at position %d, want %d", got, want-73, want)
		}
	}
	if rows.Next() {
		var extra int64
		if err := rows.Scan(&extra); err != nil {
			t.Fatalf("scan extra applied delivery migration: %v", err)
		}
		t.Fatalf("applied delivery migration sequence has unexpected extra version %d", extra)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate applied delivery migration sequence: %v", err)
	}
	assertDashboardPublicationRelayRemoved(t, ctx, store)
	for _, field := range []struct{ table, column string }{
		{table: "delivery_gc_cycles", column: "actor_id"},
		{table: "delivery_build_attempts", column: "idempotency_key"},
		{table: "delivery_plans", column: "actor_id"},
		{table: "delivery_plans", column: "source_owner_id"},
		{table: "project_dashboard_appearances", column: "revision"},
		{table: "delivery_publications", column: "refresh_run_id"},
		{table: "delivery_publications", column: "refresh_lease_owner"},
		{table: "delivery_publications", column: "refresh_lease_revision"},
		{table: "delivery_publications", column: "refresh_target_revision"},
		{table: "refresh_job_runs", column: "invocation_source"},
	} {
		var count int
		if err := store.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, field.table, field.column).Scan(&count); err != nil {
			t.Fatalf("inspect migration tail %s.%s: %v", field.table, field.column, err)
		}
		if count != 1 {
			t.Fatalf("migration tail column %s.%s count = %d, want 1", field.table, field.column, count)
		}
	}
	for _, table := range []string{
		"recovery_qualification_schedules", "recovery_qualification_occurrences",
		"recovery_qualification_attempts", "recovery_qualification_evidence_attempts",
	} {
		assertTableCount(t, ctx, store, table, 1)
	}
	var eventDDL string
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'delivery_events'`).Scan(&eventDDL); err != nil {
		t.Fatalf("inspect delivery event schema: %v", err)
	}
	if !strings.Contains(eventDDL, "'approval_revoked'") {
		t.Fatalf("delivery event schema does not include approval_revoked: %s", eventDDL)
	}
	if rows, err := store.SQLDB().QueryContext(ctx, `PRAGMA foreign_key_check`); err != nil {
		t.Fatalf("check delivery migration foreign keys: %v", err)
	} else {
		defer rows.Close()
		if rows.Next() {
			t.Fatal("delivery migration leaves a foreign-key violation")
		}
	}
}

func assertDashboardPublicationRelayPresent(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	for _, object := range []struct {
		typeName string
		name     string
	}{
		{typeName: "table", name: "dashboard_publication_stream_events"},
		{typeName: "index", name: "dashboard_publication_stream_events_stream_idx"},
	} {
		var count int
		if err := database.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = ? AND name = ?`, object.typeName, object.name).Scan(&count); err != nil {
			t.Fatalf("inspect predecessor %s %s: %v", object.typeName, object.name, err)
		}
		if count != 1 {
			t.Fatalf("predecessor %s %s count = %d, want 1", object.typeName, object.name, count)
		}
	}
}

func seedDashboardPublicationRelayPredecessor(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	for _, statement := range []string{
		`INSERT INTO projects (id, title) VALUES ('fai596-project', 'FAI-596')`,
		`INSERT INTO serving_states (id, project_id, environment, status, source) VALUES ('fai596-state', 'fai596-project', 'prod', 'active', 'publish')`,
		`INSERT INTO dashboard_publications (
  id, project_id, name, public_id, dashboard, default_page, configuration_digest,
  allowed_origins_json, dependency_asset_ids_json, revision, configured,
  active_serving_state_id, configured_at
) VALUES (
  'fai596-publication', 'fai596-project', 'website', 'fai596-public-id',
  'sales', 'overview', 'sha256:fai596', '["https://example.test"]',
  '["dashboard:sales"]', 7, 1, 'fai596-state', '2026-08-31T00:00:00Z'
)`,
		`INSERT INTO dashboard_publication_events (publication_id, event_type, actor_id, serving_state_id, created_at)
VALUES ('fai596-publication', 'configured', 'fai596-actor', 'fai596-state', '2026-08-31T00:00:01Z')`,
		`INSERT INTO dashboard_publication_streams (
  publication_id, stream_id, public_id, serving_state_id, registration_id,
  filters_json, generation, expires_at, updated_at
) VALUES (
  'fai596-publication', 'fai596-stream', 'fai596-public-id', 'fai596-state',
  'fai596-registration', '{"controls":{"region":"west"},"selections":[]}',
  4, '2999-01-01T00:00:00Z', '2026-08-31T00:00:02Z'
)`,
		`INSERT INTO dashboard_publication_stream_events (stream_id, envelope_json, created_at)
VALUES ('fai596-stream', '{"signals":{"status":{"generation":4}}}', '2026-08-31T00:00:03Z')`,
	} {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed dashboard publication predecessor: %v", err)
		}
	}
}

func assertDashboardPublicationRows(t *testing.T, ctx context.Context, store *Store) {
	t.Helper()
	var publicationID, publicID, dashboard, servingStateID string
	var revision int
	if err := store.SQLDB().QueryRowContext(ctx, `
SELECT id, public_id, dashboard, active_serving_state_id, revision
FROM dashboard_publications WHERE id = 'fai596-publication'`).Scan(
		&publicationID, &publicID, &dashboard, &servingStateID, &revision,
	); err != nil {
		t.Fatalf("read retained dashboard publication: %v", err)
	}
	if publicationID != "fai596-publication" || publicID != "fai596-public-id" || dashboard != "sales" || servingStateID != "fai596-state" || revision != 7 {
		t.Fatalf("retained dashboard publication = id %q public_id %q dashboard %q state %q revision %d", publicationID, publicID, dashboard, servingStateID, revision)
	}

	var eventCount int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM dashboard_publication_events WHERE publication_id = 'fai596-publication'`).Scan(&eventCount); err != nil {
		t.Fatalf("count retained dashboard publication events: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("retained dashboard publication event count = %d, want 1", eventCount)
	}
	var eventType, actorID, eventServingState string
	if err := store.SQLDB().QueryRowContext(ctx, `
SELECT event_type, actor_id, serving_state_id
FROM dashboard_publication_events WHERE publication_id = 'fai596-publication'`).Scan(&eventType, &actorID, &eventServingState); err != nil {
		t.Fatalf("read retained dashboard publication event: %v", err)
	}
	if eventType != "configured" || actorID != "fai596-actor" || eventServingState != "fai596-state" {
		t.Fatalf("retained dashboard publication event = type %q actor %q state %q", eventType, actorID, eventServingState)
	}

	var streamCount int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM dashboard_publication_streams WHERE publication_id = 'fai596-publication' AND stream_id = 'fai596-stream'`).Scan(&streamCount); err != nil {
		t.Fatalf("count retained dashboard publication streams: %v", err)
	}
	if streamCount != 1 {
		t.Fatalf("retained dashboard publication stream count = %d, want 1", streamCount)
	}
	var streamPublicID, streamStateID, registrationID, filtersJSON string
	var generation int
	if err := store.SQLDB().QueryRowContext(ctx, `
SELECT public_id, serving_state_id, registration_id, filters_json, generation
FROM dashboard_publication_streams
WHERE publication_id = 'fai596-publication' AND stream_id = 'fai596-stream'`).Scan(
		&streamPublicID, &streamStateID, &registrationID, &filtersJSON, &generation,
	); err != nil {
		t.Fatalf("read retained dashboard publication stream: %v", err)
	}
	if streamPublicID != "fai596-public-id" || streamStateID != "fai596-state" || registrationID != "fai596-registration" || filtersJSON != `{"controls":{"region":"west"},"selections":[]}` || generation != 4 {
		t.Fatalf("retained dashboard publication stream = public_id %q state %q registration %q filters %q generation %d", streamPublicID, streamStateID, registrationID, filtersJSON, generation)
	}
}

func assertDashboardPublicationRelayRemoved(t *testing.T, ctx context.Context, store *Store) {
	t.Helper()
	for _, object := range []struct {
		typeName string
		name     string
	}{
		{typeName: "table", name: "dashboard_publication_stream_events"},
		{typeName: "index", name: "dashboard_publication_stream_events_stream_idx"},
	} {
		var count int
		if err := store.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = ? AND name = ?`, object.typeName, object.name).Scan(&count); err != nil {
			t.Fatalf("inspect removed relay %s %s: %v", object.typeName, object.name, err)
		}
		if count != 0 {
			t.Fatalf("removed relay %s %s count = %d, want 0", object.typeName, object.name, count)
		}
	}
	for _, object := range []struct {
		typeName string
		name     string
	}{
		{typeName: "table", name: "dashboard_publications"},
		{typeName: "table", name: "dashboard_publication_events"},
		{typeName: "table", name: "dashboard_publication_streams"},
		{typeName: "index", name: "dashboard_publications_project_idx"},
		{typeName: "index", name: "dashboard_publication_events_publication_idx"},
		{typeName: "index", name: "dashboard_publication_streams_expiry_idx"},
	} {
		var count int
		if err := store.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = ? AND name = ?`, object.typeName, object.name).Scan(&count); err != nil {
			t.Fatalf("inspect retained publication object %s %s: %v", object.typeName, object.name, err)
		}
		if count != 1 {
			t.Fatalf("retained publication %s %s count = %d, want 1", object.typeName, object.name, count)
		}
	}
}

func TestCanonicalApprovalParentTriggersEnforceExactScopeAndCascade(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "approval-parent.db"))
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer store.Close()
	conn, err := store.SQLDB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	if _, err := conn.ExecContext(ctx, `INSERT INTO project_deployments (
		id, project_id, environment, generation_id, artifact_digest, request_digest,
		status, created_by, created_at
	) VALUES ('deployment-scope', 'project-scope', 'prod', 'generation-scope', ?, ?,
		'pending', 'principal-requester', '2026-08-19T00:00:00Z')`, digest, digest); err != nil {
		t.Fatalf("insert approval parent: %v", err)
	}
	insertApproval := func(id, project string) error {
		_, err := conn.ExecContext(ctx, `INSERT INTO deployment_approvals (
			id, project_id, deployment_id, environment, request_digest, release_id,
			status, requested_by, request_credential_class, request_credential_id,
			requested_at, expires_at, revision
		) VALUES (?, ?, 'deployment-scope', 'prod', ?, 'release-scope',
			'pending', 'principal-requester', 'api_token', 'token-requester',
			'2026-08-19T00:00:00Z', '2026-08-19T01:00:00Z', 1)`, id, project, digest)
		return err
	}
	if err := insertApproval("approval-foreign", "project-foreign"); err == nil || !strings.Contains(err.Error(), "parent scope is missing") {
		t.Fatalf("cross-scope approval error = %v, want parent-scope rejection", err)
	}
	if err := insertApproval("approval-scope", "project-scope"); err != nil {
		t.Fatalf("insert exact-scope approval: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM project_deployments WHERE id = 'deployment-scope'`); err != nil {
		t.Fatalf("delete approval parent: %v", err)
	}
	var count int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM deployment_approvals WHERE id = 'approval-scope'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("cascaded approval count = %d, want 0", count)
	}
}

func assertTableCount(t *testing.T, ctx context.Context, store *Store, table string, want int) {
	t.Helper()
	var got int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&got); err != nil {
		t.Fatalf("inspect table %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("table %s count = %d, want %d", table, got, want)
	}
}

func assertSQLTableCount(t *testing.T, ctx context.Context, database *sql.DB, table string, want int) {
	t.Helper()
	var got int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&got); err != nil {
		t.Fatalf("inspect table %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("table %s count = %d, want %d", table, got, want)
	}
}

func containsAll(value string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(value, needle) {
			return false
		}
	}
	return true
}
