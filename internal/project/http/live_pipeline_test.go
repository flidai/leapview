package http

import (
	stdhttp "net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPipelineRunMonitorFilterNormalizesAndBoundsRequest(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	request := httptest.NewRequest(stdhttp.MethodGet, "/runs?q=sales&range=7d&status=failed&trigger=schedule&page=3", nil)
	filter, selectedRange, page := pipelineRunMonitorFilter(request, now)
	if selectedRange != "7d" || page != 3 || filter.Since != now.Add(-7*24*time.Hour) || filter.Offset != 50 || filter.Limit != 25 || filter.Search != "sales" || filter.Status != "failed" || filter.Trigger != "schedule" {
		t.Fatalf("filter = %#v, range = %q, page = %d", filter, selectedRange, page)
	}
	request = httptest.NewRequest(stdhttp.MethodGet, "/runs?range=invalid&status=unknown&trigger=dependency&page=-4", nil)
	filter, selectedRange, page = pipelineRunMonitorFilter(request, now)
	if selectedRange != "24h" || page != 1 || filter.Status != "" || filter.Trigger != "" || filter.Since != now.Add(-24*time.Hour) {
		t.Fatalf("normalized filter = %#v, range = %q, page = %d", filter, selectedRange, page)
	}
}

func TestLivePipelineRouteSupportsCanonicalAndLegacyAssetIDs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, route, assetID string
		want                 bool
	}{
		{name: "pipeline monitor", route: "pipelines", want: true},
		{name: "canonical pipeline asset", route: "data", assetID: "pipeline:daily", want: true},
		{name: "legacy pipeline asset", route: "data", assetID: "refresh_pipeline:daily", want: true},
		{name: "legacy asset route", route: "asset", assetID: "refresh_pipeline:daily", want: true},
		{name: "non-pipeline asset", route: "data", assetID: "model:orders", want: false},
		{name: "unrelated route", route: "catalog", assetID: "pipeline:daily", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := livePipelineRoute(test.route, test.assetID); got != test.want {
				t.Fatalf("livePipelineRoute(%q, %q) = %t, want %t", test.route, test.assetID, got, test.want)
			}
		})
	}
}
