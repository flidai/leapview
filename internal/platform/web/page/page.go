package page

import (
	"strings"

	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

const RootClass = "min-h-svh bg-app text-fg-default"

// Context is the product-agnostic information a layout needs to present the
// current route. Capabilities provide the values; application composition
// decides how they are rendered.
type Context struct {
	Active       string
	ScopeID      string
	ScopeTitle   string
	SectionID    string
	SectionTitle string
	PageID       string
	PageTitle    string
	RelatedID    string
	RelatedTitle string
	HistoryID    string
	Compact      bool
}

type Presentation struct {
	ProductName string
	FaviconPath string
}

// Layout is injected by application composition. Signal is deliberately
// opaque: platform web owns the mechanism, while app owns the product chrome.
type Layout struct {
	Presentation Presentation
	Assets       staticasset.Resolver
	ColorMode    string
	Signal       any
	Scripts      []string
	Mount        func(g.Node, ...g.Node) g.Node
}

type Provider func(Context) Layout

type Spec struct {
	Title        string
	CSRFToken    string
	Stylesheets  []string
	Scripts      []string
	Head         []g.Node
	HTMLAttrs    []g.Node
	MainAttrs    []g.Node
	UpdatesURL   string
	Content      g.Node
	ContentAttrs []g.Node
	BodyBefore   []g.Node
	BodyAfter    []g.Node
}

func Resolve(provider Provider, context Context) Layout {
	if provider == nil {
		return Layout{}
	}
	return provider(context)
}

func WithSignal(layout Layout, signals map[string]any) map[string]any {
	if layout.Signal != nil {
		signals["chrome"] = layout.Signal
	}
	return signals
}

func Render(layout Layout, spec Spec) g.Node {
	title := documentTitle(spec.Title, layout.Presentation.ProductName)
	head := make([]g.Node, 0, 8+len(layout.Scripts)+len(spec.Stylesheets)+len(spec.Scripts)+len(spec.Head))
	if favicon := strings.TrimSpace(layout.Presentation.FaviconPath); favicon != "" {
		head = append(head, h.Link(h.Rel("icon"), h.Href(layout.Assets.URL(favicon)), h.Type("image/svg+xml")))
	}
	head = append(head,
		h.Link(h.Rel("stylesheet"), h.Href(layout.Assets.URL("/static/app.css"))),
		h.Script(h.Src(layout.Assets.URL("/static/theme.js"))),
		h.Script(h.Type("module"), h.Src(layout.Assets.URL("/static/command.js"))),
	)
	head = append(head, csrfMeta(spec.CSRFToken))
	for _, path := range spec.Stylesheets {
		head = append(head, h.Link(h.Rel("stylesheet"), h.Href(layout.Assets.URL(path))))
	}
	for _, path := range append(append([]string(nil), layout.Scripts...), spec.Scripts...) {
		head = append(head, h.Script(h.Type("module"), h.Src(layout.Assets.URL(path))))
	}
	head = append(head, spec.Head...)
	head = append(head, inspectorScript(layout.Assets))

	content := spec.Content
	if layout.Mount != nil {
		content = layout.Mount(content, spec.ContentAttrs...)
	} else if len(spec.ContentAttrs) > 0 && content != nil {
		content = g.Group{g.El("div", append(spec.ContentAttrs, content)...)}
	}
	body := append([]g.Node(nil), spec.BodyBefore...)
	body = append(body, content)
	body = append(body, spec.BodyAfter...)
	body = append(body, inspectorElement(layout.Assets))
	htmlAttrs := spec.HTMLAttrs
	if len(htmlAttrs) == 0 {
		theme := themeAttributes(layout.ColorMode)
		htmlAttrs = []g.Node{
			g.Attr("data-color-mode", theme.colorMode),
			g.Attr("data-light-theme", theme.lightTheme),
			g.Attr("data-dark-theme", theme.darkTheme),
		}
		if theme.preference != "" {
			htmlAttrs = append(htmlAttrs, g.Attr("data-theme-preference", theme.preference))
		}
	}
	mainAttrs := spec.MainAttrs
	if len(mainAttrs) == 0 {
		mainAttrs = []g.Node{h.Class(RootClass)}
	}
	return pagestream.RenderPage(pagestream.PageSpec{
		Title: title, DatastarScriptURL: layout.Assets.URL(staticasset.DatastarScriptPath),
		HTMLAttrs: htmlAttrs, Head: head, MainAttrs: mainAttrs,
		UpdatesURL: spec.UpdatesURL, Body: body,
	})
}

func documentTitle(pageTitle, productName string) string {
	pageTitle = strings.TrimSpace(pageTitle)
	productName = strings.TrimSpace(productName)
	if pageTitle == "" {
		return productName
	}
	if productName == "" || strings.Contains(pageTitle, productName) {
		return pageTitle
	}
	if productName != "LeapView" && strings.Contains(pageTitle, "LeapView") {
		return strings.Replace(pageTitle, "LeapView", productName, 1)
	}
	return pageTitle + " · " + productName
}

type documentTheme struct {
	colorMode  string
	preference string
	lightTheme string
	darkTheme  string
}

func themeAttributes(value string) documentTheme {
	switch strings.TrimSpace(value) {
	case "light":
		return documentTheme{colorMode: "light", preference: "light", lightTheme: "light", darkTheme: "dark"}
	case "dark":
		return documentTheme{colorMode: "dark", preference: "dark", lightTheme: "light", darkTheme: "dark"}
	case "dark_dimmed":
		return documentTheme{colorMode: "dark", preference: "dark_dimmed", lightTheme: "light", darkTheme: "dark_dimmed"}
	case "light_colorblind":
		return documentTheme{colorMode: "light", preference: "light_colorblind", lightTheme: "light_colorblind", darkTheme: "dark"}
	case "dark_colorblind":
		return documentTheme{colorMode: "dark", preference: "dark_colorblind", lightTheme: "light", darkTheme: "dark_colorblind"}
	case "light_tritanopia":
		return documentTheme{colorMode: "light", preference: "light_tritanopia", lightTheme: "light_tritanopia", darkTheme: "dark"}
	case "dark_tritanopia":
		return documentTheme{colorMode: "dark", preference: "dark_tritanopia", lightTheme: "light", darkTheme: "dark_tritanopia"}
	case "system":
		return documentTheme{colorMode: "auto", preference: "system", lightTheme: "light", darkTheme: "dark"}
	default:
		return documentTheme{colorMode: "auto", lightTheme: "light", darkTheme: "dark"}
	}
}

func csrfMeta(token string) g.Node {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	return h.Meta(h.Name("csrf-token"), h.Content(token))
}

func inspectorScript(assets staticasset.Resolver) g.Node {
	if assets.Production() {
		return nil
	}
	return h.Script(h.Type("module"), h.Src(assets.URL("/static/datastar-inspector.js")))
}

func inspectorElement(assets staticasset.Resolver) g.Node {
	if assets.Production() {
		return nil
	}
	return g.El("datastar-inspector", g.Attr("signals-url", "/__dev/pagestream/signals"))
}
