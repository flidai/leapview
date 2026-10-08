package migrations

import "testing"

func TestSavedVisualsUpgradePreservesRowsAndRefusesDowngrade(t *testing.T) {
	pool, _, provider := newDemoUpgradeDatabase(t)
	if _, err := provider.UpTo(t.Context(), 56); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(t.Context(), 57); err != nil {
		t.Fatal(err)
	}
	var canDelete bool
	if err := pool.QueryRow(t.Context(), `SELECT has_table_privilege(
		'leapview_control_runtime', 'dashboard.saved_visuals', 'DELETE')`).Scan(&canDelete); err != nil {
		t.Fatal(err)
	}
	if canDelete {
		t.Fatal("saved visual creation migration unexpectedly permits deletion")
	}
	const visualID = "a0fdeca4-215e-45f5-a1f4-869c975d90af"
	if _, err := pool.Exec(t.Context(), `INSERT INTO dashboard.saved_visuals
		(project_id, principal_id, id, source_key, title, semantic_model_id, definition_json)
		VALUES ('sales', '8d6b6369-ab47-4ba9-954e-04e72c61cc95', $1::uuid,
		'conversation:visual', 'Retained visual', 'orders', '{"type":"bar"}'::jsonb)`, visualID); err != nil {
		t.Fatal(err)
	}
	readSavedRow := func() string {
		t.Helper()
		var row string
		if err := pool.QueryRow(t.Context(), `SELECT row_to_json(saved)::text
			FROM dashboard.saved_visuals AS saved WHERE id = $1::uuid`, visualID).Scan(&row); err != nil {
			t.Fatal(err)
		}
		return row
	}
	before := readSavedRow()
	if _, err := provider.UpTo(t.Context(), 58); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT has_table_privilege(
		'leapview_control_runtime', 'dashboard.saved_visuals', 'DELETE')`).Scan(&canDelete); err != nil {
		t.Fatal(err)
	}
	if !canDelete {
		t.Fatal("runtime cannot unsave visuals after the permission upgrade")
	}
	if _, err := provider.Down(t.Context()); err == nil {
		t.Fatal("saved visual permissions allowed a downgrade")
	}
	if _, err := provider.UpTo(t.Context(), 58); err != nil {
		t.Fatal(err)
	}
	if after := readSavedRow(); after != before {
		t.Fatalf("permission upgrade or replay changed the saved visual: before %s, after %s", before, after)
	}
}
