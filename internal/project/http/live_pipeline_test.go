package http

import (
	"math"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	projectview "github.com/flidai/leapview/internal/project"
)

func TestLegacyRunRoutesRedirectWithFilters(t *testing.T) {
	tests := []struct{ path, want string }{
		{"/runs?q=sales&range=7d&status=failed&pipeline=pipeline%3Asales", "/pipelines/runs?q=sales&range=7d&status=failed&pipeline=pipeline%3Asales"},
		{"/pipelines?view=runs&q=sales&page=2", "/pipelines/runs?page=2&q=sales"},
	}
	for _, test := range tests {
		r := httptest.NewRequest(stdhttp.MethodGet, test.path, nil)
		w := httptest.NewRecorder()
		h := &BrowserHandler{}
		if r.URL.Path == "/runs" {
			h.Runs(w, r)
		} else {
			h.Pipelines(w, r)
		}
		if w.Code != stdhttp.StatusPermanentRedirect || w.Header().Get("Location") != test.want {
			t.Fatalf("%s: status=%d location=%q, want %q", test.path, w.Code, w.Header().Get("Location"), test.want)
		}
	}
}

func TestPipelineCollectionViewUsesCanonicalRunsPath(t *testing.T) {
	for _, test := range []struct{ path, want string }{
		{"/pipelines", "pipelines"},
		{"/pipelines/runs", "runs"},
		{"/pipelines?view=runs", "runs"},
	} {
		if got := pipelineCollectionView(httptest.NewRequest(stdhttp.MethodGet, test.path, nil)); got != test.want {
			t.Fatalf("%s: view = %q, want %q", test.path, got, test.want)
		}
	}
}

func TestVisiblePipelineIDsScopesToAuthorizedExactPipeline(t *testing.T) {
	visible := []projectview.DevelopAssetView{{ID: "pipeline:sales"}, {ID: "pipeline:sales-old"}}
	for _, test := range []struct {
		selected string
		want     int
	}{
		{"", 2}, {"pipeline:sales", 1}, {"pipeline:private", 0},
	} {
		got := visiblePipelineIDs(visible, test.selected)
		if len(got) != test.want || (test.selected != "" && len(got) > 0 && got[0] != test.selected) {
			t.Fatalf("selected=%q: visible=%v", test.selected, got)
		}
	}
}

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
	request = httptest.NewRequest(stdhttp.MethodGet, "/runs?range=all&page=1001", nil)
	filter, selectedRange, page = pipelineRunMonitorFilter(request, now)
	if selectedRange != "all" || page != 1001 || filter.Offset != 25_000 {
		t.Fatalf("deep-page filter = %#v, range = %q, page = %d", filter, selectedRange, page)
	}
	request = httptest.NewRequest(stdhttp.MethodGet, "/runs?page=9223372036854775807", nil)
	filter, _, page = pipelineRunMonitorFilter(request, now)
	wantPage := int64(math.MaxInt64/25) + 1
	if page != wantPage || filter.Offset != (wantPage-1)*25 {
		t.Fatalf("maximum-page filter = %#v, page = %d, want page %d", filter, page, wantPage)
	}
}

func TestLivePipelineRouteSupportsCanonicalAndLegacyAssetIDs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, route, assetID string
		want                 bool
	}{
		{name: "pipeline monitor", route: "pipelines", want: true},
		{name: "pipeline detail", route: "pipeline_detail", assetID: "pipeline:daily", want: true},
		{name: "pipeline run detail", route: "pipeline_run_detail", assetID: "pipeline:daily", want: true},
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
