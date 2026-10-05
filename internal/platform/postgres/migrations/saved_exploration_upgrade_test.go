package migrations

import "testing"

func TestSavedExplorationRevisionUpgradePreservesMainSavedRows(t *testing.T) {
	pool, _, provider := newDemoUpgradeDatabase(t)
	if _, err := provider.UpTo(t.Context(), 54); err != nil {
		t.Fatalf("apply published main migrations through revision 54: %v", err)
	}
	const savedID = "74dd035c-1a85-4c7e-af84-6d2edce11a17"
	if _, err := pool.Exec(t.Context(), `INSERT INTO project.saved_exploration
		(id, project_id, environment, principal_id, title, command_json, created_at, updated_at)
		VALUES ($1::uuid, 'sales', 'prod', 'alice', 'Retained exploration',
		'{"semanticModelId":"orders","limit":25}'::jsonb,
		'2026-09-01T12:00:00Z', '2026-09-02T12:00:00Z')`, savedID); err != nil {
		t.Fatalf("seed the existing saved-exploration foundation: %v", err)
	}
	readSavedRow := func() string {
		t.Helper()
		var row string
		if err := pool.QueryRow(t.Context(), `SELECT row_to_json(saved)::text
			FROM project.saved_exploration AS saved WHERE id = $1::uuid`, savedID).Scan(&row); err != nil {
			t.Fatal(err)
		}
		return row
	}
	before := readSavedRow()
	var exists bool
	if err := pool.QueryRow(t.Context(), `SELECT to_regnamespace('saved_exploration') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("revision 54 unexpectedly contains the revisioned saved-exploration schema")
	}
	if _, err := provider.UpTo(t.Context(), 55); err != nil {
		t.Fatalf("upgrade published main revision 54 to revision 55: %v", err)
	}
	current, _, err := provider.GetVersions(t.Context())
	if err != nil || current != 55 {
		t.Fatalf("upgraded revision = %d, error = %v; want 55", current, err)
	}
	if after := readSavedRow(); after != before {
		t.Fatalf("existing saved row changed during upgrade: before %s, after %s", before, after)
	}
	for _, table := range []string{"saved_explorations", "saved_exploration_revisions", "saved_exploration_operations"} {
		var runtimeCanRead bool
		if err := pool.QueryRow(t.Context(), `SELECT has_table_privilege(
			'leapview_control_runtime', $1, 'SELECT')`, "saved_exploration."+table).Scan(&runtimeCanRead); err != nil {
			t.Fatalf("new revisioned table %s is unavailable: %v", table, err)
		}
		if !runtimeCanRead {
			t.Fatalf("runtime cannot read new revisioned table %s", table)
		}
	}
	if _, err := provider.UpTo(t.Context(), 55); err != nil {
		t.Fatalf("replay completed revision 55: %v", err)
	}
	if after := readSavedRow(); after != before {
		t.Fatal("existing saved row changed during migration replay")
	}
}
