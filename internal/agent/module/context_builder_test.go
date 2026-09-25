package module

import (
	"testing"

	"github.com/flidai/leapview/internal/agent"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
)

func TestResolvedBuilderTurnContextUsesAuthorizedDraftRevision(t *testing.T) {
	pageID := "overview"
	builder := uisignals.DashboardBuilderSignal{
		DashboardID: "dashboard_sales", DraftID: "draft_1", Title: "Sales",
		Revision:       uisignals.DashboardBuilderRevisionSignal{ID: "revision_7", Number: 7, ContentHash: "sha256:abc"},
		SelectedPageID: &pageID,
		Pages:          []uisignals.DashboardBuilderPageSignal{{ID: pageID, Title: "Overview"}},
	}
	candidate := agent.TurnContext{DashboardID: "dashboard_sales", DraftID: "draft_1", PageID: pageID,
		DraftRevision: &agent.DraftRevision{RevisionID: "forged", Number: 99, ContentHash: "forged"}}
	got, err := resolvedBuilderTurnContext(candidate, builder)
	if err != nil {
		t.Fatal(err)
	}
	if got.Surface != "dashboard_builder" || got.DraftID != "draft_1" || got.DraftRevision == nil || got.DraftRevision.RevisionID != "revision_7" || got.DraftRevision.Number != 7 {
		t.Fatalf("resolved context = %#v", got)
	}
	for _, invalid := range []agent.TurnContext{
		{DashboardID: "dashboard_sales", DraftID: "other_draft", PageID: pageID},
		{DashboardID: "dashboard_sales", DraftID: "draft_1", PageID: "missing_page"},
	} {
		if _, err := resolvedBuilderTurnContext(invalid, builder); err == nil {
			t.Errorf("accepted stale builder context: %#v", invalid)
		}
	}
}
