package sqlite_test

import (
	"context"
	"testing"

	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	appearancesqlite "github.com/flidai/leapview/internal/dashboard/appearance/sqlite"
	"github.com/flidai/leapview/internal/platform"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestProjectAppearancePatchesPersistAndSupportReset(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, t.TempDir()+"/control.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repository := appearancesqlite.NewRepository(store.SQLDB())
	projectID := projectgraph.ResourceID("project:test")
	dashboardID := projectgraph.ResourceID("dashboard:executive")
	key := dashboardappearance.Key{ProjectID: projectID, DashboardID: dashboardID}
	icon, color := "chart-no-axes-combined", "blue"
	row, err := repository.ApplyPatch(ctx, key, "principal:test", dashboardappearance.Patch{Icon: &icon, Color: &color})
	if err != nil {
		t.Fatal(err)
	}
	if row.Icon != icon || row.Color != color || row.Revision != 1 {
		t.Fatalf("created appearance = %#v", row)
	}
	reset := dashboardappearance.ResetValue
	row, err = repository.ApplyPatch(ctx, key, "principal:test", dashboardappearance.Patch{Icon: &reset})
	if err != nil {
		t.Fatal(err)
	}
	resolved := dashboardappearance.Resolve(row.Value)
	if row.Icon != "" || row.Color != color || row.Revision != 2 || resolved.Icon != dashboardappearance.DefaultIcon {
		t.Fatalf("reset appearance = stored %#v resolved %#v", row, resolved)
	}
	listed, err := repository.ListProject(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if got := listed[dashboardID]; got.Revision != 2 || got.Color != color {
		t.Fatalf("listed appearance = %#v", got)
	}
}
