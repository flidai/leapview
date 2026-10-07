package module

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestChatDraftPageTargetsSelectionInsteadOfFirstPage(t *testing.T) {
	doc := document.DashboardDocument{Spec: document.DashboardSpec{Pages: []document.DashboardPage{{ID: "overview", Title: "Overview"}, {ID: "pies", Title: "Pie charts"}}}}
	resolved, err := withChatDraftPage(agent.TurnContext{Surface: "chat", DashboardID: "demo", PageTitle: "Forged client title", References: []agent.TurnReference{{Reference: agent.TurnReferenceKey{Kind: "dashboard", ID: "demo"}}}}, doc, "pies")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.PageID != "pies" || resolved.PageTitle != "Pie charts" || resolved.Surface != "chat" {
		t.Fatalf("wrong context: %+v", resolved)
	}
	if resolved.References[0].PageID != "pies" || !strings.Contains(strings.Join(resolved.References[0].Context, " "), "Active builder page: Pie charts (pies)") {
		t.Fatalf("agent reference is missing selected page: %+v", resolved.References)
	}
	if _, err := withChatDraftPage(resolved, doc, "deleted"); err == nil {
		t.Fatal("deleted page must not silently fall back to Overview")
	}
}

func TestBuilderChatRetainsAuthoringModeOnSelectedPage(t *testing.T) {
	doc := document.DashboardDocument{Spec: document.DashboardSpec{Pages: []document.DashboardPage{{ID: "overview", Title: "Overview"}, {ID: "bars", Title: "Bar charts"}}}}
	for _, surface := range []string{"chat", "builder"} {
		resolved, err := withChatDraftPage(agent.TurnContext{Surface: surface, DashboardID: "demo", References: []agent.TurnReference{{Reference: agent.TurnReferenceKey{Kind: "dashboard", ID: "demo"}}}}, doc, "bars")
		if err != nil {
			t.Fatal(err)
		}
		if resolved.Surface != surface {
			t.Fatalf("lost authoring mode: %+v", resolved)
		}
		if resolved.PageID != "bars" {
			t.Fatalf("wrong destination: %+v", resolved)
		}
	}
}
