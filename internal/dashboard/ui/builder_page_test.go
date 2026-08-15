package ui

import (
	"html"
	"strings"
	"testing"

	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	"github.com/stretchr/testify/require"
)

func TestDashboardBuilderPageRendersStreamShellAndTypedActions(t *testing.T) {
	envelope := uisignals.DashboardBuilderEnvelope{
		Builder: uisignals.DashboardBuilderSignal{
			WorkspaceID: "sales", DashboardID: "revenue", DraftID: "draft-7", Title: "Revenue draft",
			Revision: uisignals.DashboardBuilderRevisionSignal{ID: "rev-7", Number: 7, ContentHash: "sha256:abc"},
		},
	}
	actions := DashboardBuilderActionBindings{
		BackHref:       "/workspaces/sales/dashboards",
		PreviewHref:    "/workspaces/sales/dashboards/revenue/preview",
		ExportYAMLHref: "/workspaces/sales/dashboards/revenue/export.yaml",
		CommandPath:    "/workspaces/sales/dashboards/revenue/draft/command",
		CommandBinding: dashboardgen.GenUIActionExecuteDashboardAuthoringCommand(),
	}

	var rendered strings.Builder
	err := DashboardBuilderPage(envelope, "csrf-test", actions).Render(&rendered)
	require.NoError(t, err)
	output := html.UnescapeString(rendered.String())
	for _, want := range []string{
		`<lv-dashboard-builder`, `slot="page"`, `workspace-id="sales"`, `dashboard-id="revenue"`, `draft-id="draft-7"`,
		`/static/dashboard-builder.js`, `route=dashboard_builder`, `workspace=sales`, `dashboard=revenue`, `draft=draft-7`,
		`data-on:lv-builder-command`, `@post('/workspaces/sales/dashboards/revenue/draft/command'`, `headers: window.LeapViewCommand.headers('executeDashboardAuthoringCommand')`,
		`back-href="/workspaces/sales/dashboards"`, `preview-href="/workspaces/sales/dashboards/revenue/preview"`,
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

func TestDashboardBuilderBootstrapSignalsStayUnderDedicatedKeys(t *testing.T) {
	envelope := uisignals.DashboardBuilderEnvelope{Builder: uisignals.DashboardBuilderSignal{WorkspaceID: "sales", DashboardID: "revenue", DraftID: "draft-7"}}
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
	if _, legacy := signals["page"]; legacy {
		t.Fatal("builder bootstrap reused runtime dashboard page signal")
	}
}
