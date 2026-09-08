package ui

import (
	"html"
	"strings"
	"testing"

	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
	g "maragu.dev/gomponents"
)

func TestDashboardDraftPreviewPageUsesTheApplicationDashboardShell(t *testing.T) {
	provider := func(webpage.Context) webpage.Layout {
		return webpage.Layout{
			Presentation: webpage.Presentation{ProductName: "Northstar", FaviconPath: "/brand.svg"},
			Assets:       staticasset.New(staticasset.Config{Version: "test"}),
			Mount: func(content g.Node, attrs ...g.Node) g.Node {
				return g.El("lv-app-shell", append(attrs, content)...)
			},
		}
	}
	editHref := "/dashboards/revenue/edit?draft=draft-7&page=details"
	var rendered strings.Builder
	err := DashboardDraftPreviewPage(
		"Revenue", "revenue", "details", editHref, "/dashboards/revenue/preview?_signals=1", "csrf-test",
		AgentCommandBindings{CreateConversation: agentgen.GenUIActionCreateAgentConversation(), CreateRun: agentgen.GenUIActionCreateAgentRun()},
		provider,
	).Render(&rendered)
	if err != nil {
		t.Fatal(err)
	}
	body := html.UnescapeString(rendered.String())
	for _, want := range []string{
		"<lv-app-shell", `<lv-dashboard-page slot="page"`, `presentation="app"`, `read-only`,
		`authoring-action-label="Edit dashboard"`, `authoring-action-href="/dashboards/revenue/edit?draft=draft-7&page=details"`,
		`data-on:lv-chat-submit`, `data-on:lv-chat-reference-search`, `content="csrf-test"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("draft dashboard shell missing %q:\n%s", want, body)
		}
	}
	for _, unwanted := range []string{"Draft preview", "Back to builder", ">Read only<"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("draft dashboard shell retained hybrid chrome %q:\n%s", unwanted, body)
		}
	}
}

func TestDashboardDraftPreviewRevisionChangedPageRendersBrandedRecovery(t *testing.T) {
	provider := func(webpage.Context) webpage.Layout {
		return webpage.Layout{
			Presentation: webpage.Presentation{ProductName: "Northstar", FaviconPath: "/brand.svg"},
			Assets:       staticasset.New(staticasset.Config{Version: "test"}),
		}
	}
	backHref := "/dashboards/revenue/edit?draft=draft-7&page=details"
	var rendered strings.Builder
	if err := DashboardDraftPreviewRevisionChangedPage(backHref, provider).Render(&rendered); err != nil {
		t.Fatal(err)
	}
	body := html.UnescapeString(rendered.String())
	for _, want := range []string{
		"<title>Draft preview unavailable · Northstar</title>",
		"Northstar",
		"Draft changed",
		"older draft revision",
		"Return to builder",
		`href="/dashboards/revenue/edit?draft=draft-7&page=details"`,
		"Back to builder",
		"/static/app.css?v=test",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("recovery page missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "@get(") {
		t.Fatalf("recovery page unexpectedly opened a signal stream:\n%s", body)
	}
}
