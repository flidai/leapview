package usage

import (
	"slices"
	"testing"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestKeyCatalogIDIsStableDashboardResource(t *testing.T) {
	key := Key{ProjectID: projectgraph.ResourceID("sales"), DashboardID: projectgraph.ResourceID("executive")}
	if got := key.CatalogID(); got != "executive" {
		t.Fatalf("catalog ID = %q, want stable dashboard resource ID", got)
	}
}

func TestRankPopularityAssignsGlobalLevelsByViewersViewerDaysAndRecency(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	summaries := []Summary{
		{Key: Key{ProjectID: "finance", DashboardID: "high-1"}, ViewerCount: 12, ViewerDays: 22, LastViewedAt: now},
		{Key: Key{ProjectID: "operations", DashboardID: "high-2"}, ViewerCount: 11, ViewerDays: 21, LastViewedAt: now},
		{Key: Key{ProjectID: "sales", DashboardID: "medium-1"}, ViewerCount: 10, ViewerDays: 20, LastViewedAt: now},
		{Key: Key{ProjectID: "sales", DashboardID: "medium-2"}, ViewerCount: 9, ViewerDays: 19, LastViewedAt: now},
		{Key: Key{ProjectID: "small", DashboardID: "low-1"}, ViewerCount: 8, ViewerDays: 18, LastViewedAt: now},
		{Key: Key{ProjectID: "small", DashboardID: "low-2"}, ViewerCount: 7, ViewerDays: 17, LastViewedAt: now},
		{Key: Key{ProjectID: "small", DashboardID: "outside-top-30"}, ViewerCount: 6, ViewerDays: 16, LastViewedAt: now},
		{Key: Key{ProjectID: "personal", DashboardID: "solo"}, ViewerCount: 1, ViewerDays: 30, LastViewedAt: now},
	}

	ranked := RankPopularity(summaries, 20)
	if len(ranked) != 6 {
		t.Fatalf("ranked count = %d, want top 30%% of 20 dashboards: %#v", len(ranked), ranked)
	}
	want := []RankedPopularity{
		{Key: Key{ProjectID: "finance", DashboardID: "high-1"}, Level: LevelHigh},
		{Key: Key{ProjectID: "operations", DashboardID: "high-2"}, Level: LevelHigh},
		{Key: Key{ProjectID: "sales", DashboardID: "medium-1"}, Level: LevelMedium},
		{Key: Key{ProjectID: "sales", DashboardID: "medium-2"}, Level: LevelMedium},
		{Key: Key{ProjectID: "small", DashboardID: "low-1"}, Level: LevelLow},
		{Key: Key{ProjectID: "small", DashboardID: "low-2"}, Level: LevelLow},
	}
	if !slices.Equal(ranked, want) {
		t.Fatalf("popularity ranking = %#v, want %#v", ranked, want)
	}
}

func TestRankPopularityRequiresThreeDistinctViewers(t *testing.T) {
	summaries := []Summary{{
		Key:         Key{ProjectID: "sales", DashboardID: "frequent-solo"},
		ViewerCount: 2, ViewerDays: 60, LastViewedAt: time.Now(),
	}}
	if ranked := RankPopularity(summaries, 1); len(ranked) != 0 {
		t.Fatalf("ranked = %#v, want no dashboard below viewer floor", ranked)
	}
}
