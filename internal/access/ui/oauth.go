package ui

import (
	"net/url"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access/http/mcpoauth"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
	g "maragu.dev/gomponents"
	c "maragu.dev/gomponents/components"
	h "maragu.dev/gomponents/html"
)

func OAuthConsentPage(consent mcpoauth.Consent, values url.Values, csrfToken string, presentation webpage.Presentation, assets staticasset.Resolver) g.Node {
	if strings.TrimSpace(presentation.ProductName) == "" {
		presentation.ProductName = "Application"
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hidden := g.Group{}
	for _, key := range keys {
		if key == "decision" || key == "gorilla.csrf.Token" {
			continue
		}
		for _, value := range values[key] {
			hidden = append(hidden, h.Input(h.Type("hidden"), h.Name(key), h.Value(value)))
		}
	}
	hidden = append(hidden, h.Input(h.Type("hidden"), h.Name("gorilla.csrf.Token"), h.Value(csrfToken)))
	return c.HTML5(c.HTML5Props{
		Title: "Authorize MCP access · " + presentation.ProductName, Language: "en",
		Head: g.Group{
			h.Link(h.Rel("icon"), h.Href(assets.URL(presentation.FaviconPath)), h.Type("image/svg+xml")),
			h.Link(h.Rel("stylesheet"), h.Href(assets.URL("/static/app.css"))),
		},
		Body: g.Group{
			h.Main(h.Class("min-h-svh bg-app text-fg-default flex items-center justify-center p-6"),
				h.Section(h.Class("w-full max-w-lg rounded-xl border border-border-default bg-canvas-default p-6 shadow-lg"),
					h.H1(h.Class("text-xl font-semibold"), g.Text("Authorize MCP access")),
					h.P(h.Class("mt-3 text-sm text-fg-muted"),
						g.Text(consent.ClientName+" is requesting permission to use "+presentation.ProductName+" tools as your signed-in account.")),
					h.Div(h.Class("mt-5 rounded-md border border-border-muted bg-canvas-subtle p-4"),
						h.P(h.Class("text-sm font-medium"), g.Text("Permission")),
						h.P(h.Class("mt-1 text-sm text-fg-muted"), g.Text("Use governed read-only BI tools. Resource capabilities are checked for every call.")),
						h.P(h.Class("mt-3 text-xs text-fg-subtle"), g.Text("Resource: "+consent.Resource)),
					),
					h.Form(h.Method("post"), h.Action("/oauth/authorize"), h.Class("mt-6 flex justify-end gap-3"),
						hidden,
						h.Button(h.Type("submit"), h.Name("decision"), h.Value("deny"), h.Class("btn"), g.Text("Cancel")),
						h.Button(h.Type("submit"), h.Name("decision"), h.Value("approve"), h.Class("btn btn-accent"), g.Text("Authorize")),
					),
				),
			),
		},
	})
}
