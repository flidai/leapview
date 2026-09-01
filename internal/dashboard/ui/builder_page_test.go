package ui

import (
	"html"
	"strings"
	"testing"

	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/stretchr/testify/require"
	g "maragu.dev/gomponents"
)

func TestDashboardBuilderPageRendersStreamShellAndTypedActions(t *testing.T) {
	envelope := uisignals.DashboardBuilderEnvelope{
		Builder: uisignals.DashboardBuilderSignal{
			DashboardID: "revenue", DraftID: "draft-7", Title: "Revenue draft",
			Revision: uisignals.DashboardBuilderRevisionSignal{ID: "rev-7", Number: 7, ContentHash: "sha256:abc"},
		},
	}
	actions := DashboardBuilderActionBindings{
		BackHref:       "/dashboards",
		PreviewHref:    "/dashboards/revenue/preview",
		ExportYAMLHref: "/dashboards/revenue/export.yaml",
		PageBaseHref:   "/dashboards/revenue/edit",
		CommandPath:    "/dashboards/revenue/commands",
		CommandBinding: dashboardgen.GenUIActionExecuteDashboardAuthoringCommand(),
	}

	var rendered strings.Builder
	err := DashboardBuilderPage(envelope, "csrf-test", actions).Render(&rendered)
	require.NoError(t, err)
	output := html.UnescapeString(rendered.String())
	for _, want := range []string{
		`<lv-dashboard-builder`, `slot="page"`, `dashboard-id="revenue"`, `draft-id="draft-7"`,
		`/static/dashboard-builder.js`, `route=dashboard_builder`, `dashboard=revenue`, `draft=draft-7`,
		`data-on:lv-builder-command`, `@post('/dashboards/revenue/commands'`, `headers: window.LeapViewCommand.headers('executeDashboardAuthoringCommand')`,
		`back-href="/dashboards"`, `preview-href="/dashboards/revenue/preview"`,
		`page-base-href="/dashboards/revenue/edit"`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("builder shell missing %q:\n%s", want, output)
		}
	}
	for _, forbidden := range []string{"sales-chart", "orders.status", "sha256:abc", "data-signals=", `data-builder-revision=`} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("builder shell embedded authored payload %q:\n%s", forbidden, output)
		}
	}
}

func TestDashboardBuilderUpdatesURLCarriesSelectedPage(t *testing.T) {
	selectedPage := "details"
	url := dashboardBuilderUpdatesURL(uisignals.DashboardBuilderSignal{
		DashboardID: "revenue", DraftID: "draft-7", SelectedPageID: &selectedPage,
	})
	if !strings.Contains(url, "route=dashboard_builder") || !strings.Contains(url, "page=details") {
		t.Fatalf("updates URL = %q, want selected page", url)
	}
}

func TestDashboardBuilderBootstrapSignalsStayUnderDedicatedKeys(t *testing.T) {
	envelope := uisignals.DashboardBuilderEnvelope{Builder: uisignals.DashboardBuilderSignal{DashboardID: "revenue", DraftID: "draft-7"}, BuilderVisuals: map[string]uisignals.DashboardVisualizationSignal{}}
	signals := DashboardBuilderBootstrapSignals(envelope)
	if _, ok := signals["builder"].(uisignals.DashboardBuilderSignal); !ok {
		t.Fatalf("builder signal = %T, want DashboardBuilderSignal", signals["builder"])
	}
	if _, ok := signals["runtime"].(uisignals.RouteRuntimeSignal); !ok {
		t.Fatalf("runtime signal = %T, want RouteRuntimeSignal", signals["runtime"])
	}
	if _, ok := signals["status"].(uisignals.DashboardStatus); !ok {
		t.Fatalf("status signal = %T, want DashboardStatus", signals["status"])
	}
	if _, ok := signals["builderVisuals"].(map[string]uisignals.DashboardVisualizationSignal); !ok {
		t.Fatalf("builderVisuals signal = %T, want map[string]DashboardVisualizationSignal", signals["builderVisuals"])
	}
	if _, legacy := signals["page"]; legacy {
		t.Fatal("builder bootstrap reused runtime dashboard page signal")
	}
}

func TestDashboardBuilderPageUsesRouteLocalFocusLayout(t *testing.T) {
	envelope := uisignals.DashboardBuilderEnvelope{Builder: uisignals.DashboardBuilderSignal{DashboardID: "revenue", DraftID: "draft-7", Title: "Revenue"}}
	provider := func(webpage.Context) webpage.Layout {
		return webpage.Layout{
			Presentation: webpage.Presentation{ProductName: "Test", FaviconPath: "/test.svg"},
			ColorMode:    "dark",
			Signal:       "chrome",
			Scripts:      []string{"/static/app-shell.js"},
			Mount: func(content g.Node, attrs ...g.Node) g.Node {
				return g.El("lv-app-shell", append(attrs, content)...)
			},
		}
	}
	var rendered strings.Builder
	require.NoError(t, DashboardBuilderPage(envelope, "", DashboardBuilderActionBindings{}, provider).Render(&rendered))
	output := html.UnescapeString(rendered.String())
	if strings.Contains(output, "lv-app-shell") || strings.Contains(output, "app-shell.js") {
		t.Fatalf("builder route mounted global chrome: %s", output)
	}
	focused := builderFocusLayout(provider, webpage.Context{Active: "dashboards"})
	if focused.Signal != nil || len(focused.Scripts) != 0 || focused.Mount != nil {
		t.Fatalf("focus layout retained shell hooks: %#v", focused)
	}
	if focused.Presentation.ProductName != "Test" || focused.Presentation.FaviconPath != "/test.svg" || focused.ColorMode != "dark" {
		t.Fatalf("focus layout changed injected presentation/theme: %#v", focused)
	}
	untouched := webpage.Resolve(provider, webpage.Context{Active: "dashboards"})
	if untouched.Signal == nil || len(untouched.Scripts) == 0 || untouched.Mount == nil {
		t.Fatal("route-local focus helper mutated the injected provider")
	}
}

func TestDashboardCreateEntryUsesProductLanguage(t *testing.T) {
	var rendered strings.Builder
	if err := DashboardDraftCreatePageWithKey("project", "csrf", "/dashboards/new", "request-1").Render(&rendered); err != nil {
		t.Fatal(err)
	}
	body := rendered.String()
	for _, want := range []string{"New dashboard", "Start with a private draft.", `name="semanticModel"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("create dashboard page missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "Create dashboard draft") {
		t.Fatalf("create dashboard page exposed implementation language: %s", body)
	}
}

func TestDashboardCreateEntryOffersGovernedSemanticModels(t *testing.T) {
	var rendered strings.Builder
	models := []DashboardSemanticModelOption{{ID: "semantic:orders", Title: "Orders"}, {ID: "semantic:customers", Title: "Customers"}}
	if err := DashboardDraftCreatePageWithModelsAndKey("project", "csrf", "/dashboards/new", "request-1", models, "semantic:customers").Render(&rendered); err != nil {
		t.Fatal(err)
	}
	body := html.UnescapeString(rendered.String())
	for _, want := range []string{`<select id="dashboard-semantic-model" name="semanticModel" required>`, `<option value="semantic:orders">Orders</option>`, `<option value="semantic:customers" selected>Customers</option>`} {
		if !strings.Contains(body, want) {
			t.Fatalf("create dashboard model picker missing %q: %s", want, body)
		}
	}
}
