package ui

import (
	"html"
	"strings"
	"testing"

	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
)

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
