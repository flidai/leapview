package migrations

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"

	jobpostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	recoverypostgres "github.com/flidai/leapview/internal/recoveryset/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestEmbeddedGooseBaselineIsImmutableAndForwardMigrationsAreOrdered(t *testing.T) {
	entries, err := fs.ReadDir(MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	var sqlFiles []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sql") {
			sqlFiles = append(sqlFiles, entry.Name())
		}
	}
	if got, want := strings.Join(sqlFiles, ","), "001_control_plane.sql,002_project_free_source_bundle.sql,003_dashboard_authoring_runtime_lock.sql,004_dashboard_authoring_capability_evidence.sql,005_resource_uid_registry.sql,006_recovery_successor_v3.sql,007_contract_publication_evidence.sql,008_managed_provider_version_observation.sql,009_managed_data_retention_lifecycle.sql"; got != want {
		t.Fatalf("embedded Goose migrations = %v", sqlFiles)
	}
	contents, err := fs.ReadFile(MigrationFS(), "001_control_plane.sql")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contents)
	if got, want := hex.EncodeToString(sum[:]), "d81bca2a7a9ab96a2a3e9788ca43974a71a08b37b103608cdd1f5489b2161d6a"; got != want {
		t.Fatalf("immutable Goose baseline digest = %s, want %s", got, want)
	}
	text := string(contents)
	for _, required := range []string{"-- +goose Up", "SET LOCAL ROLE leapview_control_owner", "CREATE TABLE IF NOT EXISTS event.event_log"} {
		if !strings.Contains(text, required) {
			t.Errorf("Goose baseline missing %q", required)
		}
	}
	for _, forbidden := range []string{"platform.schema_revision", "watermill", "cache.cache_l3", "CREATE SCHEMA IF NOT EXISTS cache"} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(forbidden)) {
			t.Errorf("Goose baseline retains removed contract %q", forbidden)
		}
	}
}

func TestManagedDataRetentionLifecycleMigrationIsAdditiveAndImmutable(t *testing.T) {
	contents, err := fs.ReadFile(MigrationFS(), "009_managed_data_retention_lifecycle.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration := string(contents)
	for _, required := range []string{
		"retention_root_revision_state_idx",
		"retention_root_generation_state_idx",
		"retention_root_reachability_epoch",
		"retention root must begin in live state",
		"delivery.sync_managed_data_generation_root",
		"evidence->>'kind' = 'serving-generation'",
		"REVOKE ALL ON FUNCTION delivery.sync_managed_data_generation_root(uuid, text) FROM PUBLIC",
		"destructive down is forbidden",
	} {
		if !strings.Contains(migration, required) {
			t.Errorf("retention lifecycle migration missing %q", required)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(migration), "RESET ROLE;") {
		t.Error("retention lifecycle migration must restore the migrator role")
	}
	down := migration[strings.Index(migration, "-- +goose Down"):]
	if strings.Contains(strings.ToUpper(down), "DROP TABLE") {
		t.Error("retention lifecycle Down must refuse instead of deleting evidence")
	}
}

func TestManagedProviderVersionObservationMigrationIsAdditiveAndImmutable(t *testing.T) {
	contents, err := fs.ReadFile(MigrationFS(), "008_managed_provider_version_observation.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration := string(contents)
	for _, required := range []string{
		"managed_data.provider_observation_profile",
		"managed_data.provider_version_observation",
		"PRIMARY KEY (profile_id, object_key)",
		"provider-version observations are immutable",
		"GRANT SELECT, INSERT",
		"destructive down is forbidden",
	} {
		if !strings.Contains(migration, required) {
			t.Errorf("provider observation migration missing %q", required)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(migration), "RESET ROLE;") {
		t.Error("provider observation migration must restore the migrator role")
	}
	down := migration[strings.Index(migration, "-- +goose Down"):]
	if strings.Contains(strings.ToUpper(down), "DROP TABLE") {
		t.Error("provider observation Down must refuse instead of deleting evidence")
	}
}

func TestProjectFreeSourceBundleQuarantinesLegacyLineageVersions(t *testing.T) {
	contents, err := fs.ReadFile(MigrationFS(), "002_project_free_source_bundle.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		"ADD CONSTRAINT graphs_graph_version_v2_ck",
		"CHECK (graph_version >= 2) NOT VALID",
		"ALTER TABLE lineage.graphs",
		"DROP INDEX IF EXISTS lineage.lineage_edges_project_from_idx",
		"ALTER TABLE project.source_snapshot",
		"source_snapshot_source_identity_version_v2_ck",
		"ALTER TABLE project.source_sync_plan",
		"source_sync_plan_source_identity_version_v2_ck",
		"CHECK (source_identity_version >= 2) NOT VALID",
		"source_identity_version = 2",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("forward migration missing %q", required)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(text), "RESET ROLE;") {
		t.Error("forward migration must restore the migrator role before Goose records its version")
	}
	for _, forbidden := range []string{
		"source_blob_source_identity_version",
		"source_snapshot_entry_source_identity_version",
		"source_attestation_source_identity_version",
		"source_sync_plan_entry_source_identity_version",
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("forward migration quarantines reusable child/blob evidence %q", forbidden)
		}
	}
}

func TestSuccessorV3MigrationMirrorsOwnerSchemaAndRefusesDestructiveDown(t *testing.T) {
	migrationBytes, err := fs.ReadFile(MigrationFS(), "006_recovery_successor_v3.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration := string(migrationBytes)
	owner := recoverypostgres.SuccessorSchemaSQL()
	for _, marker := range []string{
		"recovery.set_identity_registry",
		"recovery.successor_evidence_v2",
		"recovery.successor_evidence_locator_v2",
		"recovery.successor_manifest_binding",
		"recovery.successor_trust_generation",
		"recovery.recovery_set_v3",
		"recovery.recovery_set_v3_root",
		"successor_domain_sha256",
		"lock_successor_generation",
		"guard_successor_set_complete",
		"successor_registry_immutable",
	} {
		if !strings.Contains(owner, marker) || !strings.Contains(migration, marker) {
			t.Errorf("successor schema/migration missing %q", marker)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(migration), "RESET ROLE;") {
		t.Error("successor migration must restore the migrator role after its Down refusal")
	}
	down := migration[strings.Index(migration, "-- +goose Down"):]
	if strings.Contains(strings.ToUpper(down), "DROP TABLE") || !strings.Contains(down, "destructive down is forbidden") {
		t.Error("successor Down must refuse, never destroy, persisted evidence")
	}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?m)^CREATE TABLE IF NOT EXISTS (recovery\.[a-z0-9_]+)`),
		regexp.MustCompile(`(?m)^CREATE OR REPLACE FUNCTION (recovery\.[a-z0-9_]+)`),
		regexp.MustCompile(`(?m)^CREATE (?:CONSTRAINT )?TRIGGER (recovery\.[a-z0-9_]+)`),
		regexp.MustCompile(`(?m)^CREATE INDEX IF NOT EXISTS ([a-z0-9_]+)`),
	}
	for _, pattern := range patterns {
		matches := func(source string) []string {
			all := pattern.FindAllStringSubmatch(source, -1)
			names := make([]string, 0, len(all))
			for _, match := range all {
				names = append(names, match[1])
			}
			return names
		}
		ownerNames := matches(owner)
		migrationNames := matches(migration)
		sort.Strings(ownerNames)
		sort.Strings(migrationNames)
		if len(ownerNames) != len(migrationNames) {
			t.Errorf("owner/migration object count for %q = %d/%d", pattern, len(ownerNames), len(migrationNames))
			continue
		}
		for i := range ownerNames {
			if ownerNames[i] != migrationNames[i] {
				t.Errorf("owner/migration object %d for %q = %q/%q", i, pattern, ownerNames[i], migrationNames[i])
			}
		}
	}
}

func TestEmbeddedGooseBaselineMirrorsCanonicalJobsRiverFence(t *testing.T) {
	contents, err := fs.ReadFile(MigrationFS(), "001_control_plane.sql")
	if err != nil {
		t.Fatal(err)
	}
	baseline := strings.ReplaceAll(strings.ReplaceAll(string(contents), "-- +goose StatementBegin\n", ""), "-- +goose StatementEnd\n", "")
	canonical := strings.ReplaceAll(strings.ReplaceAll(jobpostgres.SchemaSQL(), "-- +goose StatementBegin\n", ""), "-- +goose StatementEnd\n", "")
	const start = "CREATE OR REPLACE FUNCTION jobs.guard_river_result_fence()"
	const end = "CREATE TABLE IF NOT EXISTS jobs.job_history ("
	if got, want := recoverySQLBlock(t, baseline, start, end), recoverySQLBlock(t, canonical, start, end); got != want {
		t.Error("Goose baseline River result fence differs from canonical jobs schema")
	}
}

func TestEmbeddedGooseBaselineMirrorsCanonicalRecoveryGuards(t *testing.T) {
	contents, err := fs.ReadFile(MigrationFS(), "001_control_plane.sql")
	if err != nil {
		t.Fatal(err)
	}
	baseline := strings.ReplaceAll(strings.ReplaceAll(string(contents), "-- +goose StatementBegin\n", ""), "-- +goose StatementEnd\n", "")
	canonical := recoverypostgres.SchemaSQL()
	for _, block := range []struct {
		name, start, end string
	}{
		{name: "object root constraints", start: "CREATE TABLE IF NOT EXISTS recovery.recovery_object_root (", end: "CREATE TABLE IF NOT EXISTS recovery.validation_attempt ("},
		{name: "publication guard", start: "CREATE OR REPLACE FUNCTION recovery.reject_frontier_mutation()", end: "CREATE OR REPLACE FUNCTION recovery.reject_frontier_insert()"},
		{name: "validation guard", start: "CREATE OR REPLACE FUNCTION recovery.guard_validation_result_insert()", end: "DROP TRIGGER IF EXISTS recovery_validation_result_guard"},
	} {
		want := recoverySQLBlock(t, canonical, block.start, block.end)
		got := recoverySQLBlock(t, baseline, block.start, block.end)
		if got != want {
			t.Errorf("Goose baseline %s differs from canonical recovery schema", block.name)
		}
	}
}

func recoverySQLBlock(t *testing.T, source, start, end string) string {
	t.Helper()
	startAt := strings.Index(source, start)
	if startAt < 0 {
		t.Fatalf("SQL block start %q is missing", start)
	}
	endAt := strings.Index(source[startAt:], end)
	if endAt < 0 {
		t.Fatalf("SQL block end %q is missing", end)
	}
	return source[startAt : startAt+endAt]
}

func TestGooseEntryPointsRejectNilDatabase(t *testing.T) {
	if _, err := NewProvider(nil); err == nil {
		t.Fatal("NewProvider(nil) unexpectedly succeeded")
	}
	if err := ApplyGoose(t.Context(), nil); err == nil {
		t.Fatal("ApplyGoose(nil) unexpectedly succeeded")
	}
	if err := VerifyGoose(t.Context(), nil); err == nil {
		t.Fatal("VerifyGoose(nil) unexpectedly succeeded")
	}
	if err := ReconcileRolePolicy(t.Context(), nil, "SELECT 1"); err == nil {
		t.Fatal("ReconcileRolePolicy(nil) unexpectedly succeeded")
	}
}

func TestVerifyGooseFailsClosedOnFreshDatabase(t *testing.T) {
	harness := postgrestest.Start(t)
	database := harness.NewDatabase(t, "goose_verify_fresh")
	db, err := sql.Open("pgx", database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := VerifyGoose(t.Context(), db); err == nil {
		t.Fatal("VerifyGoose unexpectedly succeeded on a fresh database")
	}

	var eventSchemaExists, versionTableExists bool
	if err := db.QueryRowContext(t.Context(), `
		SELECT EXISTS (
		           SELECT 1 FROM information_schema.schemata
		           WHERE schema_name = 'event'
		       ),
		       EXISTS (
		           SELECT 1 FROM information_schema.tables
		           WHERE table_schema = 'public' AND table_name = 'goose_db_version'
		       )`).Scan(&eventSchemaExists, &versionTableExists); err != nil {
		t.Fatal(err)
	}
	if eventSchemaExists {
		t.Fatal("VerifyGoose applied the event schema while checking a fresh database")
	}
	if !versionTableExists {
		t.Fatal("VerifyGoose did not initialize its authoritative version table")
	}
	var version int64
	if err := db.QueryRowContext(t.Context(), `
		SELECT version_id
		FROM public.goose_db_version
		ORDER BY id DESC LIMIT 1`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 0 {
		t.Fatalf("fresh Goose version row = %d, want 0", version)
	}
}
