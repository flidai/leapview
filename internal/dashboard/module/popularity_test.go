package module

import (
	"context"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/usage"
)

type popularityReaderStub struct {
	since     time.Time
	summaries []usage.Summary
}

func (reader *popularityReaderStub) ListSummaries(_ context.Context, since time.Time) ([]usage.Summary, error) {
	reader.since = since
	return reader.summaries, nil
}

func TestPopularityExposesRankedUsageThroughModuleContract(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	reader := &popularityReaderStub{summaries: []usage.Summary{
		{Key: usage.Key{ProjectID: "finance", DashboardID: "executive"}, ViewerCount: 8, ViewerDays: 12, LastViewedAt: now},
	}}
	module := &Module{usageReader: reader, usageNow: func() time.Time { return now }}

	levels, err := module.Popularity(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := levels["finance.executive"]; got != PopularityHigh {
		t.Fatalf("popularity = %q, want %q", got, PopularityHigh)
	}
	if want := now.Add(-usage.PopularityWindow); !reader.since.Equal(want) {
		t.Fatalf("summary cutoff = %s, want %s", reader.since, want)
	}
}
