package http

import (
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring/preview"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
	"github.com/flidai/leapview/internal/platform/testing/ssetest"
)

func TestDashboardBuilderVisualTypeCommandRefreshesOnlyChangedVisual(t *testing.T) {
	page := "overview"
	revisionHash := "sha256:" + strings.Repeat("a", 64)
	targetEnvelope, err := visualizationruntime.EmptyEnvelopeFromDefinition(canonicalBuilderVisualDefinition(t), 2, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	targetEnvelope.VisualID = "revenue"
	fake := &builderAuthoringFake{
		builder: uisignals.DashboardBuilderSignal{
			ProjectID: "sales", DashboardID: "revenue", DraftID: "draft-1",
			Revision: uisignals.DashboardBuilderRevisionSignal{ID: "revision-2", Number: 2, ContentHash: revisionHash},
			Pages: []uisignals.DashboardBuilderPageSignal{{ID: page, Visuals: []uisignals.DashboardBuilderVisualSignal{
				{ID: "revenue-component", VisualID: "revenue"},
				{ID: "cash-component", VisualID: "cash"},
			}}},
			SelectedPageID: &page,
		},
		preview: preview.Preview{PagePatch: dashboard.Patch{Visuals: map[string]visualizationir.VisualizationEnvelope{"revenue": targetEnvelope}}},
	}
	handler := Handler{Authoring: fake, ProjectID: "sales", CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
	req := builderRequest(nethttp.MethodPost, "/dashboards/revenue/draft/command", map[string]any{
		"builderCommand": map[string]any{
			"dashboardId": "revenue", "draftId": "draft-1", "revisionId": "revision-1", "revisionNumber": "1",
			"revisionContentHash": revisionHash, "pageId": page, "visualId": "revenue-component", "type": "line", "action": "set_visual_type",
		},
		"runtime": map[string]any{"servingStateId": "builder:stable-preview"},
	})
	req.Header.Set("X-LeapView-Operation-ID", dashboardBuilderOperationID)
	req.Header.Set("X-Request-ID", "targeted-visual-preview-1")
	recorder := httptest.NewRecorder()
	handler.DashboardBuilderCommand(recorder, withBuilderURLParams(req, "sales", "revenue"))
	if recorder.Code != nethttp.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if fake.previewReq.VisualID != "revenue" {
		t.Fatalf("preview visual = %q, want revenue", fake.previewReq.VisualID)
	}
	patches := ssetest.PatchSignals(t, recorder.Body.String())
	if len(patches) != 2 {
		t.Fatalf("patches = %#v", patches)
	}
	reset, ok := patches[0]["builderVisuals"].(map[string]any)
	if !ok || len(reset) != 1 || reset["revenue"] != nil {
		t.Fatalf("targeted reset = %#v", patches[0]["builderVisuals"])
	}
	replacements, ok := patches[1]["builderVisuals"].(map[string]any)
	if !ok || len(replacements) != 1 || replacements["revenue"] == nil {
		t.Fatalf("targeted replacement = %#v", patches[1]["builderVisuals"])
	}
	runtimePatch, ok := patches[1]["runtime"].(map[string]any)
	if !ok || runtimePatch["servingStateId"] != "builder:stable-preview" {
		t.Fatalf("runtime patch = %#v", patches[1]["runtime"])
	}
	revenue, ok := replacements["revenue"].(map[string]any)
	if !ok || revenue["servingStateID"] != "builder:stable-preview" {
		t.Fatalf("replacement context = %#v", replacements["revenue"])
	}
}
