package module_test

import (
	"context"
	"encoding/json"
	"testing"

	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/transaction"
	appearancesqlite "github.com/flidai/leapview/internal/workspace/appearance/sqlite"
)

func TestDeploymentPatchesOnlyAuthoredAppearanceFields(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, t.TempDir()+"/control.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO workspaces (id, title) VALUES ('sales', 'Sales')`); err != nil {
		t.Fatal(err)
	}
	repository := appearancesqlite.NewRepository(store.SQLDB())
	icon, color := "chart-no-axes-combined", "blue"
	key := dashboardappearance.Key{WorkspaceID: "sales", DashboardID: "executive"}
	if _, err := repository.ApplyPatch(ctx, key, "project", "ui", dashboardappearance.Patch{Icon: &icon, Color: &color}); err != nil {
		t.Fatal(err)
	}

	tx, err := store.SQLDB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(dashboardappearance.Patch{Color: pointer("purple")})
	if err != nil {
		t.Fatal(err)
	}
	if err := dashboardmodule.ApplyAppearancePatches(ctx, transaction.Transaction(tx), "project", "sales", "deploy", map[string]json.RawMessage{"executive": raw}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	row, err := repository.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if row.Icon != icon || row.Color != "purple" {
		t.Fatalf("appearance = %#v, want icon preserved and configured color applied", row.Value)
	}
}

func pointer(value string) *string { return &value }
