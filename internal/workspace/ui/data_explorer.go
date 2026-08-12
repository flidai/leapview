package ui

import (
	"github.com/flidai/leapview/internal/dashboard"
	uiactions "github.com/flidai/leapview/internal/platform/web/actions"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	catalog "github.com/flidai/leapview/internal/workspace/navigation"
	uisignals "github.com/flidai/leapview/internal/workspace/ui/signals"
	g "maragu.dev/gomponents"
)

func DataExplorerPage(catalog catalog.Catalog, page uisignals.DataExplorerPageSignal, explorer uisignals.DataExplorerSignal, csrfToken string, providers ...webpage.Provider) g.Node {
	catalog = catalogWithoutWorkspaceContext(catalog)
	layout := webpage.Resolve(firstProvider(providers), workspaceLayoutContext(catalog, "data"))
	explorerUpdatesURL := updatesURL(uisignals.RouteData, "workspace", uisignals.ValueOrZero(explorer.Command.WorkspaceID), "object", uisignals.ValueOrZero(explorer.Command.ObjectKey))
	return webpage.Render(layout, webpage.Spec{
		Title: page.Title, CSRFToken: csrfToken, Scripts: []string{"/static/data-explorer.js"},
		UpdatesURL: explorerUpdatesURL,
		Content: g.El("lv-data-explorer",
			g.Attr("slot", "page"),
			g.Attr("data-on:lv-data-explorer-command", "$dataExplorerCommand = evt.detail; "+uiactions.EventPost("/data/command")),
		),
	})
}

func DataExplorerBootstrapSignals(catalog catalog.Catalog, page uisignals.DataExplorerPageSignal, explorer uisignals.DataExplorerSignal, providers ...webpage.Provider) map[string]any {
	catalog = catalogWithoutWorkspaceContext(catalog)
	layout := webpage.Resolve(firstProvider(providers), workspaceLayoutContext(catalog, "data"))
	return webpage.WithSignal(layout, map[string]any{
		"page":                page,
		"dataExplorer":        explorer,
		"dataExplorerCommand": explorer.Command,
		"status":              dashboard.Status{},
	})
}
