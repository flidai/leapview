package tools

import (
	"encoding/json"
	"github.com/flidai/leapview/internal/dashboard"
	previewservice "github.com/flidai/leapview/internal/dashboard/authoring/preview"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	"strings"
	"testing"
)

func TestDashboardPreviewModelResultKeepsFailuresWithoutRenderData(t *testing.T) {
	message := "Unknown measure"
	value := previewservice.Preview{VisualErrors: map[string]string{"incomplete": "Missing dimension"}, PagePatch: dashboard.Patch{Status: dashboard.Status{Error: "Page query failed"}, Visuals: map[string]visualizationir.VisualizationEnvelope{"bad": {Status: visualizationir.VisualizationStatus{Kind: "error", Message: &message}}, "good": {Status: visualizationir.VisualizationStatus{Kind: "ready"}, SpecRevision: strings.Repeat("render-only", 10000), DataState: visualizationir.VisualizationDataState{Value: &visualizationir.InlineVisualizationDataState{Kind: "inline", Datasets: []visualizationir.VisualizationInlineDataset{{ID: "primary", Columns: []string{"ending_cash"}, Rows: [][]any{{nil}, {nil}, {42.0}}, Completeness: "complete"}}}}}}}}
	result := dashboardPreviewModelResult(dashboardAuthoringPreviewInput{DashboardID: "dashboard-1", DraftID: "draft-1", Page: "page-2"}, value)
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Unknown measure", "Missing dimension", "Page query failed", "dashboard-1", "draft-1", "page-2", "ready", `"ending_cash":1`, `"rows":3`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("lost %q: %s", want, body)
		}
	}
	if len(body) > 2048 || strings.Contains(string(body), "render-only") {
		t.Fatalf("oversized preview receipt: %d", len(body))
	}
	if len(value.VisualErrors) != 1 {
		t.Fatal("mutated authoritative errors")
	}
}
