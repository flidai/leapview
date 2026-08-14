package sqlite_test

import (
	"context"
	"testing"

	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	"github.com/flidai/leapview/internal/platform"
	appearancesqlite "github.com/flidai/leapview/internal/workspace/appearance/sqlite"
)

func TestUIPatchesPreserveDeploymentProjectAndSupportReset(t *testing.T) {
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
	if _, err := repository.ApplyPatch(ctx, key, "project", "deploy", dashboardappearance.Patch{Icon: &icon, Color: &color}); err != nil {
		t.Fatal(err)
	}
	uiColor := "purple"
	row, err := repository.ApplyPatch(ctx, key, "", "ui", dashboardappearance.Patch{Color: &uiColor})
	if err != nil {
		t.Fatal(err)
	}
	if row.ProjectID != "project" || row.Icon != icon || row.Color != uiColor {
		t.Fatalf("UI patch = %#v, want project and omitted icon preserved", row)
	}
	reset := dashboardappearance.ResetValue
	row, err = repository.ApplyPatch(ctx, key, "", "ui", dashboardappearance.Patch{Icon: &reset})
	if err != nil {
		t.Fatal(err)
	}
	resolved := dashboardappearance.Resolve(row.Value)
	if row.Icon != "" || resolved.Icon != dashboardappearance.DefaultIcon {
		t.Fatalf("reset appearance = stored %#v resolved %#v", row.Value, resolved)
	}
}
