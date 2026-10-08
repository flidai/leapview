package http

import (
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/preview"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	"github.com/flidai/leapview/internal/platform/testing/ssetest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestDashboardBuilderCommandTranslatesAtomicPlacements(t *testing.T) {
	selectedPage := "overview"
	revisionHash := "sha256:" + strings.Repeat("a", 64)
	fake := &builderAuthoringFake{
		builder: uisignals.DashboardBuilderSignal{
			ProjectID: "sales", DashboardID: "revenue", DraftID: "draft-1",
			Revision: uisignals.DashboardBuilderRevisionSignal{ID: "revision-2", Number: 2, ContentHash: revisionHash},
			Pages:    []uisignals.DashboardBuilderPageSignal{{ID: selectedPage}}, SelectedPageID: &selectedPage,
			Preview: uisignals.DashboardBuilderPreviewStateSignal{Active: false, Loading: false, Error: uisignals.Pointer("unrelated incomplete visual")},
		},
		compileErr: errors.New("unrelated incomplete visual"),
	}
	handler := Handler{Authoring: fake, CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
	req := builderRequest(nethttp.MethodPost, "/dashboards/revenue/draft/command", map[string]any{"builderCommand": map[string]any{
		"projectId": "sales", "dashboardId": "revenue", "draftId": "draft-1", "revisionId": "revision-1", "revisionNumber": "1", "revisionContentHash": revisionHash,
		"pageId": "overview", "action": "set_placements", "compact": true, "placements": []map[string]any{
			{"componentId": "orders-component", "column": 1, "row": 1, "columnSpan": 6, "rowSpan": 4},
			{"visualId": "summary-component", "col": 7, "row": 1, "colSpan": 6, "rowSpan": 4},
		},
	}, "runtime": map[string]any{"servingStateId": "retained-preview-state"}})
	req.Header.Set("X-LeapView-Operation-ID", dashboardBuilderOperationID)
	req.Header.Set("X-Request-ID", "placement-1")
	rec := httptest.NewRecorder()
	handler.DashboardBuilderCommand(rec, withBuilderURLParams(req, "sales", "revenue"))
	if rec.Code != nethttp.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if fake.intentCalls != 1 || fake.executeCalls != 0 || fake.executed.SetPlacements == nil {
		t.Fatalf("builder dispatch calls=%d/%d command=%#v", fake.intentCalls, fake.executeCalls, fake.executed)
	}
	placements := fake.executed.SetPlacements.Placements
	if !fake.executed.SetPlacements.Compact {
		t.Fatal("translated placement command did not preserve compact-after-resize intent")
	}
	if len(placements) != 2 || placements[0].ComponentID != "orders-component" || placements[0].Placement.ColumnSpan != 6 || placements[1].ComponentID != "summary-component" || placements[1].Placement.Column != 7 {
		t.Fatalf("translated placements = %#v", placements)
	}
	if fake.previewCalls != 0 || fake.compileCalls != 0 {
		t.Fatalf("layout projection calls preview=%d compile=%d, want 0/0", fake.previewCalls, fake.compileCalls)
	}
	patches := ssetest.PatchSignals(t, rec.Body.String())
	if len(patches) != 1 {
		t.Fatalf("patches = %#v, want one layout-only patch", patches)
	}
	if _, ok := patches[0]["builderVisuals"]; ok {
		t.Fatalf("layout-only patch replaced builder visuals: %#v", patches[0])
	}
	builder, ok := patches[0]["builder"].(map[string]any)
	if !ok {
		t.Fatalf("layout builder patch = %#v", patches[0]["builder"])
	}
	previewState, ok := builder["preview"].(map[string]any)
	if !ok || previewState["active"] != true || previewState["loading"] != false || previewState["error"] != "" {
		t.Fatalf("layout preview state = %#v, want retained previews active", builder["preview"])
	}
	runtime, ok := patches[0]["runtime"].(map[string]any)
	if !ok || runtime["servingStateId"] != "retained-preview-state" {
		t.Fatalf("layout runtime = %#v", patches[0]["runtime"])
	}
}

func TestDashboardBuilderCommandCompletesMissingFieldsWithMatchingPreview(t *testing.T) {
	selectedPage := "overview"
	revisionHash := "sha256:" + strings.Repeat("a", 64)
	fake := &builderAuthoringFake{
		builder: uisignals.DashboardBuilderSignal{
			ProjectID: "sales", DashboardID: "revenue", DraftID: "draft-1",
			Revision: uisignals.DashboardBuilderRevisionSignal{ID: "revision-2", Number: 2, ContentHash: revisionHash},
			Pages:    []uisignals.DashboardBuilderPageSignal{{ID: selectedPage}}, SelectedPageID: &selectedPage,
		},
		compilation: preview.Compilation{SemanticEvidence: preview.SemanticServingStateEvidence{Identity: projectgraph.ServingIdentity{ProjectID: "sales", Environment: "dev", GenerationID: "generation-4"}}},
	}
	fake.preview = standaloneDraftPreviewFixture(t, authoring.RevisionToken{RevisionID: "revision-2", Number: 2, ContentHash: revisionHash}, selectedPage)
	fake.preview.SemanticEvidence.Identity.GenerationID = "generation-4"
	handler := Handler{Authoring: fake, CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
	req := builderRequest(nethttp.MethodPost, "/dashboards/revenue/draft/command", map[string]any{"builderCommand": map[string]any{
		"projectId": "sales", "dashboardId": "revenue", "draftId": "draft-1", "revisionId": "revision-1", "revisionNumber": "1", "revisionContentHash": revisionHash,
		"pageId": "overview", "action": "set_placements", "fillMissingFields": true, "placements": []map[string]any{
			{"componentId": "orders-component", "column": 1, "row": 1, "columnSpan": 6, "rowSpan": 4},
			{"visualId": "summary-component", "col": 7, "row": 1, "colSpan": 6, "rowSpan": 4},
		},
	}})
	req.Header.Set("X-LeapView-Operation-ID", dashboardBuilderOperationID)
	req.Header.Set("X-Request-ID", "placement-1")
	rec := httptest.NewRecorder()
	handler.DashboardBuilderCommand(rec, withBuilderURLParams(req, "sales", "revenue"))
	if rec.Code != nethttp.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if fake.intentCalls != 1 || fake.executeCalls != 0 || fake.executed.SetPlacements == nil {
		t.Fatalf("builder dispatch calls=%d/%d command=%#v", fake.intentCalls, fake.executeCalls, fake.executed)
	}
	if !fake.executed.SetPlacements.FillMissingFields {
		t.Fatal("field completion flag was lost at the HTTP boundary")
	}
	placements := fake.executed.SetPlacements.Placements
	if len(placements) != 2 || placements[0].ComponentID != "orders-component" || placements[0].Placement.ColumnSpan != 6 || placements[1].ComponentID != "summary-component" || placements[1].Placement.Column != 7 {
		t.Fatalf("translated placements = %#v", placements)
	}
	if fake.previewCalls != 1 || fake.compileCalls != 0 {
		t.Fatalf("layout projection calls preview=%d compile=%d, want 1/0", fake.previewCalls, fake.compileCalls)
	}
	patches := ssetest.PatchSignals(t, rec.Body.String())
	if len(patches) != 2 {
		t.Fatalf("patches = %#v, want a complete preview replacement", patches)
	}
	visuals := patches[1]["builderVisuals"].(map[string]any)
	visual := visuals["orders"].(map[string]any)
	if visual["servingStateID"] != patches[1]["runtime"].(map[string]any)["servingStateId"] {
		t.Fatalf("resized preview identity disagrees with runtime: %#v", patches[1])
	}
	runtime, ok := patches[1]["runtime"].(map[string]any)
	if !ok || runtime["servingStateId"] != "builder:draft-1:revision-2:"+revisionHash+":generation:generation-4" {
		t.Fatalf("layout runtime = %#v", patches[1]["runtime"])
	}
}

func TestDashboardBuilderReturnsToOriginatingChat(t *testing.T) {
	for _, tc := range []struct{ name, query, referrer, chat string }{
		{"chat link", "", "http://example.com/chats/agentconv_origin?preview=dashboard", "agentconv_origin"},
		{"reload or page change", "&returnChat=agentconv_origin", "", "agentconv_origin"},
		{"external referrer", "", "https://other.example/chats/agentconv_origin", ""},
		{"invalid return", "&returnChat=..%2F..%2Foutside", "", ""},
		{"catalog", "", "http://example.com/dashboards", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &builderAuthoringFake{builder: uisignals.DashboardBuilderSignal{ProjectID: "sales", DashboardID: "revenue", DraftID: "draft-1"}}
			handler := Handler{Authoring: fake, CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
			req := httptest.NewRequest(nethttp.MethodGet, "/dashboards/revenue/edit?page=details"+tc.query, nil)
			req.Header.Set("Referer", tc.referrer)
			rec := httptest.NewRecorder()
			handler.DashboardBuilder(rec, withBuilderURLParams(req, "sales", "revenue"))
			want := "/"
			if tc.chat != "" {
				want = "/chats/" + tc.chat
			}
			if rec.Code != nethttp.StatusOK || !strings.Contains(rec.Body.String(), `back-href="`+want+`"`) {
				t.Fatalf("builder did not retain return destination %q (status %d)", want, rec.Code)
			}
			if tc.chat != "" && !strings.Contains(rec.Body.String(), `page-base-href="/dashboards/revenue/edit?draft=draft-1&amp;returnChat=`+tc.chat+`"`) {
				t.Fatal("page navigation lost originating chat")
			}
		})
	}
}
