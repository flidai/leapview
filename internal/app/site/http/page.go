package http

import (
	"net/url"
	"strings"

	"github.com/flidai/leapview/internal/app/brand"
	"time"

	"github.com/flidai/leapview/pkg/pagestream"
	siteassets "github.com/flidai/leapview/site"
	g "maragu.dev/gomponents"
	dsattr "maragu.dev/gomponents-datastar"
	h "maragu.dev/gomponents/html"
)

type sitePageMetadata struct {
	title       string
	description string
	canonical   string
	contentType string
	robots      string
	showcase    bool
}

const siteDatastarScriptURL = "/static/vendor/datastar-1.0.2.js"
const siteBrandName = brand.Name

func sitePage(metadata sitePageMetadata) g.Node {
	head := siteHead(metadata)
	for _, stylesheet := range []string{"screenshot-hero", "mission", "orbit", "project-explorer", "layers", "enterprise", "involved"} {
		head = append(head, h.Link(h.Rel("stylesheet"), h.Href("/static/home/"+stylesheet+".css")))
	}
	head = append(head, h.Link(h.Rel("preload"), h.Href("/static/product-dashboard-dark.png"), g.Attr("as", "image")))
	for _, script := range []string{"home", "layers", "project-explorer", "orbit"} {
		head = append(head, h.Script(h.Type("module"), h.Src("/static/home/"+script+".js")))
	}
	return pagestream.RenderPage(pagestream.PageSpec{
		Title:             metadata.title,
		HTMLAttrs:         siteHTMLAttrs(),
		Head:              head,
		MainAttrs:         []g.Node{h.Class("site-page")},
		DatastarScriptURL: siteDatastarScriptURL,
		UpdatesURL:        "/updates",
		Body: []g.Node{
			h.A(h.Class("skip-link"), h.Href("#main-content"), g.Text("Skip to content")),
			siteHeader(false, metadata.showcase),
			g.Raw(siteassets.Homepage()),
			siteFooter(),
		},
	})
}

func visualsPage(metadata sitePageMetadata) g.Node {
	return pagestream.RenderPage(pagestream.PageSpec{
		Title:             metadata.title,
		HTMLAttrs:         siteHTMLAttrs(),
		Head:              siteHead(metadata),
		MainAttrs:         []g.Node{h.Class("site-page")},
		DatastarScriptURL: siteDatastarScriptURL,
		UpdatesURL:        "/updates?view=visuals",
		Body: []g.Node{
			h.A(h.Class("skip-link"), h.Href("#main-content"), g.Text("Skip to content")),
			siteHeader(false, metadata.showcase),
			h.Div(h.Class("site-shell site-showcase-shell"),
				h.Section(h.ID("main-content"), h.Class("site-showcase-intro"),
					h.P(h.Class("site-eyebrow"), g.Text(siteBrandName+" visual system")),
					h.H1(g.Text("Every visual type, using one contract.")),
					h.P(h.Class("site-lede"), g.Text("Each item below is a real "+siteBrandName+" visual rendered from the same type-discriminated payload contract.")),
					h.Div(h.Class("site-home-actions"),
						h.A(h.Class("site-button"), h.Href("/visuals/responsive"), g.Text("Responsive widget reference")),
					),
				),
				g.El("lv-site-visual-showcase"),
			),
			siteFooter(),
		},
	})
}

func responsiveWidgetsPage(metadata sitePageMetadata) g.Node {
	return pagestream.RenderPage(pagestream.PageSpec{
		Title:             metadata.title,
		HTMLAttrs:         siteHTMLAttrs(),
		Head:              siteHead(metadata),
		MainAttrs:         []g.Node{h.Class("site-page")},
		DatastarScriptURL: siteDatastarScriptURL,
		UpdatesURL:        "/updates?view=responsive-widgets",
		Body: []g.Node{
			h.A(h.Class("skip-link"), h.Href("#main-content"), g.Text("Skip to content")),
			siteHeader(false, metadata.showcase),
			h.Div(h.Class("site-shell site-showcase-shell"),
				h.Section(h.ID("main-content"), h.Class("site-showcase-intro"),
					h.P(h.Class("site-eyebrow"), g.Text(siteBrandName+" responsive QA")),
					h.H1(g.Text("Every responsive widget state, in one place.")),
					h.P(h.Class("site-lede"), g.Text("Review explicit KPI feature combinations and dashboard filter controls at every registered automatic layout. Fixed frames provide stable regression targets; the playground covers the dimensions between them.")),
					h.Div(h.Class("site-home-actions"),
						h.A(h.Class("site-button"), h.Href("/visuals"), g.Text("Back to all visuals")),
						h.A(h.Class("site-button"), h.Href("/docs/guides/build/pages-layout"), g.Text("Layout guidance")),
					),
				),
				g.El("lv-site-responsive-widget-reference"),
			),
			siteFooter(),
		},
	})
}

func showcasePage(metadata sitePageMetadata, embedURL *url.URL) g.Node {
	standaloneURL := *embedURL
	standaloneURL.Path = strings.Replace(standaloneURL.Path, "/embed/dashboards/", "/public/dashboards/", 1)
	standaloneURL.RawPath = ""
	return pagestream.RenderPage(pagestream.PageSpec{
		Title:             metadata.title,
		HTMLAttrs:         siteHTMLAttrs(),
		Head:              siteHead(metadata),
		MainAttrs:         []g.Node{h.Class("site-page")},
		DatastarScriptURL: siteDatastarScriptURL,
		UpdatesURL:        "/updates",
		Body: []g.Node{
			h.A(h.Class("skip-link"), h.Href("#main-content"), g.Text("Skip to content")),
			siteHeader(false, metadata.showcase),
			h.Div(h.Class("site-shell site-live-showcase-shell"),
				h.Section(h.ID("main-content"), h.Class("site-showcase-intro"),
					h.P(h.Class("site-eyebrow"), g.Text("Live dashboard")),
					h.H1(g.Text("Explore a real "+siteBrandName+" dashboard.")),
					h.P(h.Class("site-lede"), g.Text("Filter, select, and navigate the same published dashboard surface you can embed in your own product.")),
					h.A(h.Class("site-button"), h.Href(standaloneURL.String()), g.Attr("rel", "noreferrer"), g.Text("Open standalone dashboard")),
				),
				h.Div(h.Class("site-live-showcase-frame"),
					h.IFrame(
						h.Src(embedURL.String()),
						h.Title(siteBrandName+" interactive dashboard showcase"),
						g.Attr("sandbox", "allow-scripts allow-same-origin"),
						g.Attr("referrerpolicy", "no-referrer"),
						g.Attr("loading", "eager"),
					),
				),
			),
			siteFooter(),
		},
	})
}

func docsIndexPage(metadata sitePageMetadata) g.Node {
	return pagestream.RenderPage(pagestream.PageSpec{
		Title:             metadata.title,
		HTMLAttrs:         siteHTMLAttrs(),
		Head:              siteHead(metadata),
		MainAttrs:         []g.Node{h.Class("site-page")},
		DatastarScriptURL: siteDatastarScriptURL,
		UpdatesURL:        "/updates",
		Body: []g.Node{
			h.A(h.Class("skip-link"), h.Href("#main-content"), g.Text("Skip to content")),
			siteHeader(true, metadata.showcase),
			siteDocsLayout(nil, siteDocsIndex()),
		},
	})
}

func docsSearchPage(query string, metadata sitePageMetadata) g.Node {
	return pagestream.RenderPage(pagestream.PageSpec{
		Title:             metadata.title,
		HTMLAttrs:         siteHTMLAttrs(),
		Head:              siteHead(metadata),
		MainAttrs:         []g.Node{h.Class("site-page")},
		DatastarScriptURL: siteDatastarScriptURL,
		UpdatesURL:        "/updates",
		Body: []g.Node{
			h.A(h.Class("skip-link"), h.Href("#main-content"), g.Text("Skip to content")),
			siteHeader(true, metadata.showcase),
			siteDocsLayout(nil, siteDocsSearch(query)),
		},
	})
}

func docsArticlePage(document siteDocument, metadata sitePageMetadata) g.Node {
	updatesURL := "/updates"
	if _, ok := visualExamplesForDocument(document.slug); ok {
		updatesURL = "/updates?view=visual-docs&document=" + url.QueryEscape(document.slug)
	}
	return pagestream.RenderPage(pagestream.PageSpec{
		Title:             metadata.title,
		HTMLAttrs:         siteHTMLAttrs(),
		Head:              siteHead(metadata),
		MainAttrs:         []g.Node{h.Class("site-page")},
		DatastarScriptURL: siteDatastarScriptURL,
		UpdatesURL:        updatesURL,
		Body: []g.Node{
			h.A(h.Class("skip-link"), h.Href("#main-content"), g.Text("Skip to content")),
			siteHeader(true, metadata.showcase),
			siteDocsLayout(&document, siteDocsArticle(document)),
		},
	})
}

func notFoundPage(metadata sitePageMetadata) g.Node {
	return pagestream.RenderPage(pagestream.PageSpec{
		Title:             metadata.title,
		HTMLAttrs:         siteHTMLAttrs(),
		Head:              siteHead(metadata),
		MainAttrs:         []g.Node{h.Class("site-page")},
		DatastarScriptURL: siteDatastarScriptURL,
		UpdatesURL:        "/updates",
		Body: []g.Node{
			h.A(h.Class("skip-link"), h.Href("#main-content"), g.Text("Skip to content")),
			siteHeader(false, metadata.showcase),
			h.Div(h.Class("site-shell"),
				h.Section(h.ID("main-content"), h.Class("site-showcase-intro"),
					h.P(h.Class("site-eyebrow"), g.Text("404")),
					h.H1(g.Text("Page not found")),
					h.P(h.Class("site-lede"), g.Text("The page may have moved, or the address may be incomplete.")),
					h.Div(h.Class("site-actions"),
						h.A(h.Class("site-button site-button-primary"), h.Href("/docs"), g.Text("Browse documentation")),
						h.A(h.Class("site-button"), h.Href("/"), g.Text("Go to "+siteBrandName)),
					),
				),
			),
			siteFooter(),
		},
	})
}

func siteHead(metadata sitePageMetadata) []g.Node {
	nodes := []g.Node{
		h.Meta(h.Name("view-transition"), h.Content("same-origin")),
		h.Meta(h.Name("description"), h.Content(metadata.description)),
		h.Link(h.Rel("canonical"), h.Href(metadata.canonical)),
		h.Meta(g.Attr("property", "og:site_name"), h.Content(siteBrandName)),
		h.Meta(g.Attr("property", "og:title"), h.Content(metadata.title)),
		h.Meta(g.Attr("property", "og:description"), h.Content(metadata.description)),
		h.Meta(g.Attr("property", "og:type"), h.Content(metadata.contentType)),
		h.Meta(g.Attr("property", "og:url"), h.Content(metadata.canonical)),
		h.Meta(h.Name("twitter:card"), h.Content("summary")),
		h.Meta(h.Name("twitter:title"), h.Content(metadata.title)),
		h.Meta(h.Name("twitter:description"), h.Content(metadata.description)),
		h.Link(h.Rel("icon"), h.Href("/static/favicon.svg"), h.Type("image/svg+xml")),
		h.Link(h.Rel("preload"), h.Href("/shared/files/inter-latin-wght-normal.woff2"), g.Attr("as", "font"), h.Type("font/woff2"), g.Attr("crossorigin", "anonymous")),
		h.Link(h.Rel("stylesheet"), h.Href("/shared/app.css")),
		h.Link(h.Rel("stylesheet"), h.Href("/static/site.css")),
		h.Script(h.Src("/shared/theme.js")),
		h.Script(h.Type("module"), h.Src("/static/site-page.js")),
	}
	if metadata.robots != "" {
		nodes = append(nodes, h.Meta(h.Name("robots"), h.Content(metadata.robots)))
	}
	return nodes
}

func siteHeader(isDocs, showcase bool) g.Node {
	headerClass := "site-header"
	if isDocs {
		headerClass += " site-header--docs"
	}
	var actions []g.Node
	if isDocs {
		actions = append(actions, h.Div(h.Class("site-nav-links site-nav-links-docs"), siteActiveSearch()))
	} else {
		actions = append(actions, h.Div(h.Class("site-nav-links"),
			h.A(h.Href("/docs"), g.Text("Docs")),
			g.If(showcase, h.A(h.Href("/showcase"), g.Text("Live demo"))),
		))
		actions = append(actions, h.Div(h.Class("site-social-links"),
			h.A(h.Class("site-social-link"), h.Href("https://github.com/flidai/leapview"), g.Attr("aria-label", "GitHub"), h.Title("GitHub"), h.Target("_blank"), g.Attr("rel", "noopener noreferrer"),
				h.Span(h.Class("site-github-mark"), g.Attr("aria-hidden", "true")),
			),
			h.A(h.Class("site-social-link"), h.Href("https://discord.gg/pcfV4zAeRV"), g.Attr("aria-label", "Discord"), h.Title("Discord"), h.Target("_blank"), g.Attr("rel", "noopener noreferrer"),
				h.Span(h.Class("site-discord-mark"), g.Attr("aria-hidden", "true")),
			),
		))
	}
	actions = append(actions, g.El("lv-site-theme-toggle"))
	if !isDocs {
		actions = append(actions, g.El("lv-site-mobile-menu", g.If(showcase, g.Attr("showcase", ""))))
	}

	return h.Header(h.Class(headerClass),
		h.Nav(h.Class("site-nav"),
			siteBrandLink(),
			h.Div(h.Class("site-nav-actions"), g.Group(actions)),
		),
	)
}

func siteActiveSearch() g.Node {
	return g.El("lv-site-search",
		h.A(h.Class("site-search-fallback"), h.Href("/docs/search"), g.Text("Search")),
		h.Input(
			h.Class("site-search-active-input"),
			g.Attr("slot", "input"),
			h.Type("search"),
			h.Name("q"),
			g.Attr("aria-label", "Search documentation"),
			h.Placeholder("Search concepts, guides, commands, and APIs"),
			g.Attr("autocomplete", "off"),
			dsattr.Bind("docsSearch.query"),
			dsattr.On("input", "@get('/docs/search/active', {filterSignals: {include: /^docsSearch\\./}})", dsattr.ModifierDebounce, dsattr.Duration(200*time.Millisecond)),
			dsattr.Indicator("docsSearch.loading"),
		),
	)
}

func siteBrandLink() g.Node {
	return h.A(h.Class("site-brand"), h.Href("/"),
		g.El("lv-brand-mark", g.Attr("aria-hidden", "true")),
		h.Span(g.Text(siteBrandName)),
	)
}

func siteHTMLAttrs() []g.Node {
	return []g.Node{
		g.Attr("data-color-mode", "auto"),
		g.Attr("data-light-theme", "light"),
		g.Attr("data-dark-theme", "dark"),
	}
}

func siteFooter() g.Node {
	return h.Footer(h.Class("site-footer"), g.Attr("role", "contentinfo"),
		h.Div(h.Class("site-footer-content"),
			h.Div(h.Class("site-footer-brand-block"),
				siteBrandLink(),
				h.P(g.Text("Open-source business intelligence for teams and AI agents.")),
			),
			siteFooterGroup("Learn", []siteFooterLink{
				{label: "Documentation", href: "/docs"},
				{label: "Get started", href: "/docs/getting-started"},
			}),
			siteFooterGroup("Project", []siteFooterLink{
				{label: "GitHub", href: "https://github.com/flidai/leapview"},
				{label: "Report an issue", href: "https://github.com/flidai/leapview/issues"},
			}),
		),
		h.Div(h.Class("site-footer-bottom"), h.P(
			g.Text("A project by "),
			h.A(h.Href("https://flid.ai/"), g.Text("Flid AI")),
			g.Text("."),
		)),
	)
}

type siteFooterLink struct {
	label string
	href  string
}

func siteFooterGroup(title string, links []siteFooterLink) g.Node {
	items := make([]g.Node, 0, len(links))
	for _, link := range links {
		items = append(items, h.A(h.Href(link.href), g.Text(link.label)))
	}
	return h.Nav(g.Attr("aria-label", title), h.H2(g.Text(title)), g.Group(items))
}

func siteDocsLayout(document *siteDocument, content ...g.Node) g.Node {
	return h.Div(h.Class("site-docs-layout"),
		siteDocsSidebar(document),
		h.Button(h.Class("site-docs-drawer-backdrop"), h.Type("button"), g.Attr("aria-label", "Close documentation menu"), g.Attr("aria-hidden", "true"), g.Attr("tabindex", "-1"), g.Attr("data-site-docs-drawer-close", "true")),
		h.Div(h.Class("site-docs-content"),
			h.Div(h.Class("site-docs-reading-layout"),
				h.Div(h.Class("site-guide-shell"),
					siteDocsArticleHeader(),
					g.Group(content),
				),
			),
		),
	)
}

func siteDocsArticleHeader() g.Node {
	return h.Header(h.Class("site-docs-article-header"),
		g.El("lv-site-docs-drawer-toggle"),
	)
}

func siteDocsIndex() g.Node {
	items := make([]g.Node, 0, len(siteCatalog.Sections))
	for _, section := range siteCatalog.Sections {
		items = append(items, h.Li(
			h.A(h.Href(section.Href), h.H2(g.Text(section.Title)), h.P(g.Text(section.Summary))),
		))
	}
	return h.Article(h.ID("main-content"), h.Class("site-docs-article site-docs-index"),
		h.H1(g.Text("Documentation")),
		h.P(g.Text("Follow a task-oriented path or open the generated reference for an exact contract.")),
		docsSearchForm(""),
		h.Nav(g.Attr("aria-label", "Documentation sections"), h.Ul(h.Class("site-docs-index-list"), g.Group(items))),
	)
}

func siteDocsSidebar(current *siteDocument) g.Node {
	sections := make([]g.Node, 0, len(siteCatalog.Sections))
	for _, section := range siteCatalog.Sections {
		sectionActive := current != nil && current.sectionID == section.ID
		links := make([]g.Node, 0, len(section.Documents)+len(section.Groups))
		for _, document := range section.Documents {
			isCurrent := current != nil && current.slug == document.Slug
			links = append(links, h.Li(siteDocsLink("/docs/"+document.Slug, siteDocsNavigationLabel(section.Title, document.Title, document.NavigationTitle), isCurrent)))
		}
		for _, group := range section.Groups {
			groupActive := sectionActive && current.groupID == group.ID
			groupLinks := make([]g.Node, 0, len(group.Documents))
			for _, document := range group.Documents {
				isCurrent := current != nil && current.slug == document.Slug
				groupLinks = append(groupLinks, h.Li(siteDocsLink("/docs/"+document.Slug, siteDocsNavigationLabel(group.Title, document.Title, document.NavigationTitle), isCurrent)))
			}
			links = append(links, h.Li(siteDocsNavGroup(section.ID+"-"+group.ID, group.Title, groupActive, groupLinks)))
		}
		sections = append(sections, siteDocsNavGroup(section.ID, section.Title, sectionActive, links))
	}
	return h.Aside(h.Class("site-docs-sidebar"), h.ID("site-docs-sidebar"),
		h.Div(h.Class("site-docs-drawer-actions"),
			g.El("lv-site-docs-drawer-toggle", g.Attr("placement", "drawer")),
		),
		h.Nav(g.Attr("aria-label", "Documentation"), g.Group(sections)),
	)
}

func siteDocsNavigationLabel(parent, document, navigationTitle string) string {
	if strings.TrimSpace(navigationTitle) != "" {
		return navigationTitle
	}
	if strings.EqualFold(strings.TrimSpace(parent), strings.TrimSpace(document)) {
		return "Overview"
	}
	return document
}

func siteDocsSearch(query string) g.Node {
	results := searchSiteDocuments(query)
	items := make([]g.Node, 0, len(results))
	for index, document := range results {
		if index == 50 {
			break
		}
		items = append(items, h.Li(h.A(h.Href("/docs/"+document.slug), h.H2(g.Text(document.title)), h.P(g.Text(document.summary)))))
	}
	content := []g.Node{h.H1(g.Text("Search documentation")), docsSearchForm(query)}
	if query != "" {
		content = append(content, h.P(g.Textf("%d results for %q", len(results), query)), h.Ul(h.Class("site-docs-index-list site-docs-search-results"), g.Group(items)))
	}
	return h.Article(h.ID("main-content"), h.Class("site-docs-article site-docs-index"), g.Group(content))
}

func docsSearchForm(query string) g.Node {
	return h.Form(h.Class("site-docs-search"), h.Action("/docs/search"), h.Method("get"),
		h.Label(h.For("docs-search-query"), g.Text("Search documentation")),
		h.Div(h.Class("site-docs-search-controls"),
			h.Input(h.ID("docs-search-query"), h.Name("q"), h.Type("search"), h.Value(query), h.Placeholder("Search concepts, guides, commands, and APIs")),
			h.Button(h.Type("submit"), g.Text("Search")),
		),
	)
}

func siteDocsNavGroup(group, label string, active bool, links []g.Node) g.Node {
	class := "site-docs-nav-group"
	if active {
		class += " site-docs-nav-group-active"
	}
	attributes := []g.Node{h.Class(class), g.Attr("data-site-docs-group", group)}
	if active {
		attributes = append(attributes, g.Attr("open", "true"))
	}
	return g.El("details", g.Group(attributes),
		g.El("summary", g.Attr("title", label), h.Span(h.Class("site-docs-nav-label"), g.Text(label))),
		h.Ul(h.Class("site-docs-nav-tree"), g.Group(links)),
	)
}

func siteDocsLink(href, label string, current bool) g.Node {
	class := "site-docs-link"
	if current {
		class += " site-docs-link-current"
	}
	attrs := []g.Node{h.Class(class), h.Href(href), g.Attr("title", label)}
	if current {
		attrs = append(attrs, g.Attr("aria-current", "page"))
	}
	return h.A(g.Group(attrs), g.Text(label))
}
