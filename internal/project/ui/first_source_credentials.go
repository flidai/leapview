package ui

import (
	"net/url"
	"strconv"

	uiactions "github.com/flidai/leapview/internal/platform/web/actions"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	g "maragu.dev/gomponents"
)

func FirstSourceCredentialsPage(state projectsignals.FirstSourceCredentialSignal, csrf string, provider webpage.Provider, bindings map[string]uicommand.Binding) g.Node {
	path := "/connections/" + url.PathEscape(state.ConnectionID) + "/first-source"
	initialize := "$firstSourceCredentials = {command: evt.detail}; "
	mutation := "(async () => { try { switch (evt.detail.action) {"
	for _, action := range []string{"save", "validate", "prepare", "renew", "abort"} {
		mutation += "case '" + action + "': await " + uiactions.CommandPost(bindings[action], path+"/commands/"+action, "firstSourceCredentials") + "; break; "
	}
	mutation += "} } finally { $firstSourceCredentials.command.password = ''; evt.detail.password = ''; } })()"
	layout := webpage.Resolve(provider, webpage.Context{Active: "connections", PageTitle: "First source credentials"})
	return webpage.Render(layout, webpage.Spec{Title: "First source credentials", CSRFToken: csrf, UpdatesURL: "/updates?route=first_source_credentials&connection=" + url.QueryEscape(state.ConnectionID), Scripts: []string{"/static/project-page.js"}, Content: g.El("lv-first-source-credentials",
		g.Attr("slot", "page"), g.Attr("connection-id", state.ConnectionID), g.Attr("source-host", state.Host), g.Attr("source-database", state.Database), g.Attr("source-identity", state.SourceIdentity), g.Attr("target-revision", strconv.FormatInt(state.TargetRevision, 10)),
		g.Attr("data-on:lv-first-source-command", initialize+mutation),
		g.Attr("data-on:lv-first-source-query", initialize+"$firstSourceCredentials.command.password = ''; "+uiactions.Get(path+"/state", "firstSourceCredentials")),
	)})
}
