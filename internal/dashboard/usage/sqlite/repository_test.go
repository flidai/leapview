package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/usage"
	"github.com/flidai/leapview/internal/platform"
)

func TestRepositoryDeduplicatesViewerDaysAndSummarizesGlobalUsage(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repository := NewRepository(store.SQLDB())
	base := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)

	views := []usage.View{
		{ProjectID: "sales", DashboardID: "executive", PageID: "overview", PrincipalID: "alice", ViewedAt: base},
		{ProjectID: "sales", DashboardID: "executive", PageID: "details", PrincipalID: "alice", ViewedAt: base.Add(time.Hour)},
		{ProjectID: "sales", DashboardID: "executive", PageID: "overview", PrincipalID: "bob", ViewedAt: base},
		{ProjectID: "sales", DashboardID: "executive", PageID: "overview", PrincipalID: "alice", ViewedAt: base.Add(24 * time.Hour)},
		{ProjectID: "operations", DashboardID: "health", PageID: "overview", PrincipalID: "carol", ViewedAt: base},
	}
	for _, view := range views {
		if err := repository.RecordView(ctx, view); err != nil {
			t.Fatal(err)
		}
	}

	summaries, err := repository.ListSummaries(ctx, base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 2 {
		t.Fatalf("summaries = %#v", summaries)
	}
	if got := summaries[0]; got.Key != (usage.Key{ProjectID: "sales", DashboardID: "executive"}) || got.ViewerCount != 2 || got.ViewerDays != 3 || !got.LastViewedAt.Equal(base.Add(24*time.Hour)) {
		t.Fatalf("executive summary = %#v", got)
	}
}

func TestRepositoryRejectsIncompleteViews(t *testing.T) {
	repository := NewRepository(nil)
	if err := repository.RecordView(context.Background(), usage.View{}); err == nil {
		t.Fatal("RecordView error = nil, want validation failure")
	}
}
