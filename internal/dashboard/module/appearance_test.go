package module_test

import (
	"encoding/json"
	"testing"

	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	"github.com/flidai/leapview/internal/platform"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestApplySQLiteAppearancePatchesSharesBrowserAppearancePersistence(t *testing.T) {
	store, err := platform.Open(t.Context(), t.TempDir()+"/control.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	tx, err := store.SQLDB().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	icon, color := "chart-no-axes-combined", "blue"
	encoded := map[string]json.RawMessage{
		"dashboard:executive": json.RawMessage(`{"icon":"` + icon + `","color":"` + color + `"}`),
	}
	projectID := projectgraph.ResourceID("project:test")
	if err := dashboardmodule.ApplySQLiteAppearancePatches(t.Context(), tx, projectID, "principal:deploy", encoded); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	rows, err := dashboardmodule.NewSQLiteAppearanceStore(store.SQLDB()).ListProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	row := rows[projectgraph.ResourceID("dashboard:executive")]
	if row.Icon != icon || row.Color != color || row.Revision != 1 {
		t.Fatalf("deployed appearance = %#v", row)
	}
	if resolved := dashboardappearance.Resolve(row.Value); resolved.Icon != icon || resolved.Color != color {
		t.Fatalf("resolved appearance = %#v", resolved)
	}
}

func TestNewSQLiteAppearanceStoreRequiresDatabase(t *testing.T) {
	if store := dashboardmodule.NewSQLiteAppearanceStore(nil); store != nil {
		t.Fatalf("nil database returned appearance store %T", store)
	}
}
