package migrations

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestNativeDeliveryEvidenceMigrationsUpgradeRevisionTwentyOne exercises the
// actual embedded upgrade frontier rather than only a clean current-revision
// baseline. It proves both evidence columns and aggregate rollback retention
// authority are installed over a database whose Goose frontier is 21.
func TestNativeDeliveryEvidenceMigrationsUpgradeRevisionTwentyOne(t *testing.T) {
	harness := postgrestest.Start(t)
	owner := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator", Login: true, Password: "native-evidence-migration"})
	for _, role := range []string{"leapview_control_runtime", "leapview_control_maintenance", "leapview_control_readonly", "leapview_control_backup"} {
		harness.EnsureRole(t, postgrestest.Role{Name: role})
	}
	harness.GrantRole(t, owner, migrator)
	database := harness.NewDatabase(t, "native_delivery_evidence_upgrade")
	harness.GrantDatabase(t, database.Name, owner, "CREATE")
	harness.GrantDatabase(t, database.Name, migrator, "CONNECT", "CREATE")

	admin, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(t.Context(), `
		ALTER DATABASE native_delivery_evidence_upgrade OWNER TO leapview_control_owner;
		REVOKE ALL ON SCHEMA public FROM PUBLIC;
		GRANT USAGE, CREATE ON SCHEMA public TO leapview_control_migrator`); err != nil {
		t.Fatal(err)
	}

	migrationDB, err := sql.Open("pgx", database.URL(migrator))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrationDB.Close() })
	provider, err := newProvider(migrationDB, MigrationFS())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(t.Context(), 21); err != nil {
		t.Fatalf("apply revision-21 production frontier: %v", err)
	}
	current, _, err := provider.GetVersions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if current != 21 {
		t.Fatalf("fixture revision = %d, want 21", current)
	}
	for _, column := range []string{"created_at", "resolved_inputs", "resolved_inputs_digest"} {
		var exists bool
		if err := admin.QueryRow(t.Context(), `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				 WHERE table_schema='delivery' AND table_name='delivery_snapshot_seal' AND column_name=$1
			)`, column).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatalf("revision-21 fixture unexpectedly contains delivery_snapshot_seal.%s", column)
		}
	}

	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("upgrade revision-21 production frontier: %v", err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("replay current production frontier: %v", err)
	}
	current, _, err = provider.GetVersions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if current != CurrentRevision {
		t.Fatalf("upgraded revision = %d, want %d", current, CurrentRevision)
	}

	var requiredColumns int
	if err := admin.QueryRow(t.Context(), `
		SELECT count(*)
		  FROM information_schema.columns
		 WHERE table_schema='delivery'
		   AND ((table_name='delivery_snapshot_seal' AND column_name IN ('created_at','resolved_inputs','resolved_inputs_digest'))
		     OR (table_name='delivery_candidate' AND column_name IN ('resolved_inputs','resolved_inputs_digest')))`,
	).Scan(&requiredColumns); err != nil {
		t.Fatal(err)
	}
	if requiredColumns != 5 {
		t.Fatalf("native delivery evidence columns = %d, want 5", requiredColumns)
	}
	var constraintCount int
	if err := admin.QueryRow(t.Context(), `
		SELECT count(*) FROM pg_constraint
		 WHERE conname IN (
			'delivery_snapshot_seal_resolved_inputs_object',
			'delivery_snapshot_seal_resolved_inputs_digest',
			'delivery_snapshot_seal_resolved_inputs_pair',
			'delivery_candidate_resolved_inputs_object',
			'delivery_candidate_resolved_inputs_digest',
			'delivery_candidate_resolved_inputs_pair'
		)`,
	).Scan(&constraintCount); err != nil {
		t.Fatal(err)
	}
	if constraintCount != 6 {
		t.Fatalf("native delivery evidence constraints = %d, want 6", constraintCount)
	}
	var retirementDefinition, expiryDefinition string
	if err := admin.QueryRow(t.Context(), `SELECT pg_get_functiondef('delivery.retire_retention_root(uuid)'::regprocedure)`).Scan(&retirementDefinition); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(t.Context(), `SELECT pg_get_functiondef('delivery.expire_retention_root(uuid,interval)'::regprocedure)`).Scan(&expiryDefinition); err != nil {
		t.Fatal(err)
	}
	for name, definition := range map[string]string{"retirement": retirementDefinition, "expiry": expiryDefinition} {
		if !strings.Contains(definition, "sync_managed_data_generation_root") || !strings.Contains(definition, "root_kind IN ('generation', 'rollback')") {
			t.Fatalf("%s authority does not preserve aggregate generation/rollback reachability", name)
		}
	}
}
