package ui

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	webpage "github.com/flidai/leapview/internal/platform/web/page"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

type SearchResult struct {
	ID, Kind, Title, Description, Domain, Href string
}

type SearchPageOptions struct {
	Query, Domain, Cursor, NextCursor string
	Error                             string
	Kinds                             []string
	Limit                             int
	Results                           []SearchResult
}

// SearchPage keeps the query and every catalog filter in the URL so an
// assistant search can be inspected, refined, and shared as an ordinary page.
func SearchPage(options SearchPageOptions, csrfToken string, provider webpage.Provider) g.Node {
	layout := webpage.Resolve(provider, webpage.Context{Active: "search", PageTitle: "Search"})
	kinds := []string{"dashboard", "semantic_model", "model", "source", "connection", "pipeline", "project"}
	filters := make([]g.Node, 0, len(kinds))
	for _, kind := range kinds {
		filters = append(filters, h.Label(h.Class("search-kind"),
			h.Input(h.Type("checkbox"), h.Name("kind"), h.Value(kind), g.If(slices.Contains(options.Kinds, kind), h.Checked())),
			g.Text(searchKindLabel(kind)),
		))
	}
	results := make([]g.Node, 0, len(options.Results))
	for _, result := range options.Results {
		var title g.Node = g.Text(result.Title)
		if result.Href != "" {
			title = h.A(h.Href(result.Href), g.Text(result.Title))
		}
		results = append(results, h.Li(h.Class("search-result"),
			h.Div(h.Class("search-result-heading"), h.H2(title), h.Span(h.Class("search-badge"), g.Text(searchKindLabel(result.Kind)))),
			g.If(result.Description != "", h.P(g.Text(result.Description))),
			h.Div(h.Class("search-result-meta"), h.Code(g.Text(result.ID)), g.If(result.Domain != "", h.Span(g.Text("Domain: "+result.Domain)))),
		))
	}
	status := fmt.Sprintf("%d results", len(options.Results))
	if len(options.Results) == 1 {
		status = "1 result"
	}
	if options.NextCursor != "" {
		status += " on this page"
	}
	if options.Query == "" {
		status = "Enter a search to find authorized project resources."
	} else if len(options.Results) == 0 {
		status = "No matching resources. Try another query or adjust the filters."
	}
	if options.Error != "" {
		status = options.Error
	}
	scope := "All resource types"
	if len(options.Kinds) > 0 {
		labels := make([]string, 0, len(options.Kinds))
		for _, kind := range options.Kinds {
			labels = append(labels, searchKindLabel(kind))
		}
		scope = strings.Join(labels, ", ")
	}
	if options.Domain != "" {
		scope += " · Domain: " + options.Domain
	}
	scope += fmt.Sprintf(" · Up to %d results per page", options.Limit)
	return webpage.Render(layout, webpage.Spec{
		Title: "Search", CSRFToken: csrfToken, UpdatesURL: "/updates?route=search",
		Head: []g.Node{h.StyleEl(g.Raw(searchPageStyles))},
		Content: h.Section(g.Attr("slot", "page"), h.Class("lv-search-page"),
			h.Header(h.H1(g.Text("Search")), h.P(g.Text("Find dashboards and data assets. Search links preserve your query and filters."))),
			h.Form(h.Method("get"), h.Action("/search"), h.Class("search-form"),
				h.Div(h.Class("search-query-row"),
					h.Label(h.Class("search-query-label"), g.Text("Search resources"), h.Input(h.Type("search"), h.Name("q"), h.Value(options.Query), h.Placeholder("Name, ID, description, or metadata"), g.Attr("maxlength", "200"))),
					h.Button(h.Type("submit"), g.Text("Search")),
				),
				h.FieldSet(h.Legend(g.Text("Resource types")), h.Div(h.Class("search-kinds"), g.Group(filters)), h.P(h.Class("search-hint"), g.Text("Leave all unchecked to search every resource type."))),
				h.Div(h.Class("search-filter-row"),
					h.Label(g.Text("Domain"), h.Input(h.Type("text"), h.Name("domain"), h.Value(options.Domain), h.Placeholder("All domains"))),
					h.Label(g.Text("Results per page"), h.Input(h.Type("number"), h.Name("limit"), h.Value(strconv.Itoa(options.Limit)), g.Attr("min", "1"), g.Attr("max", "200"))),
				),
			),
			h.P(h.Class("search-hint"), g.Attr("aria-label", "Applied search filters"), g.Text(scope)),
			h.Div(h.Class("search-results-heading"), h.P(g.Attr("role", "status"), g.Text(status)),
				g.If(options.Query != "", h.Span(h.Class("search-hint"), g.Text("Results reflect your current access and the active project.")))),
			h.Ul(h.Class("search-results"), g.Group(results)),
			h.Nav(g.Attr("aria-label", "Search result pages"), h.Class("search-pagination"),
				g.If(options.Cursor != "", h.A(h.Href(searchPageHref(options, "")), g.Text("Back to first page"))),
				g.If(options.NextCursor != "", h.A(h.Href(searchPageHref(options, options.NextCursor)), g.Text("Next results"))),
			),
		),
	})
}

func searchKindLabel(kind string) string {
	switch kind {
	case "semantic_model":
		return "Semantic model"
	case "project":
		return "Project"
	default:
		if kind == "" {
			return "Resource"
		}
		return strings.ToUpper(kind[:1]) + kind[1:]
	}
}

func searchPageHref(options SearchPageOptions, cursor string) string {
	values := url.Values{"q": {options.Query}, "limit": {strconv.Itoa(options.Limit)}}
	for _, kind := range options.Kinds {
		values.Add("kind", kind)
	}
	if options.Domain != "" {
		values.Set("domain", options.Domain)
	}
	if cursor != "" {
		values.Set("cursor", cursor)
	}
	return "/search?" + values.Encode()
}

const searchPageStyles = `
.lv-search-page { box-sizing: border-box; width: min(100%, 64rem); margin: 0 auto; padding: 2rem; color: var(--lv-fg-default); font: var(--lv-type-body); }
.lv-search-page h1 { margin: 0 0 .5rem; font-size: 1.75rem; font-weight: 600; }
.lv-search-page header p, .lv-search-page .search-hint, .lv-search-page .search-result-meta { color: var(--lv-fg-muted); }
.lv-search-page .search-form { margin-top: 1.5rem; padding: 1.25rem; border: var(--lv-border-default); border-radius: var(--lv-radius-large, var(--lv-radius-default)); background: var(--lv-bg-panel); }
.lv-search-page .search-query-row, .lv-search-page .search-filter-row { display: flex; gap: 1rem; align-items: end; }
.lv-search-page label { display: grid; gap: .5rem; }
.lv-search-page .search-query-label { flex: 1; min-width: 0; }
.lv-search-page input:not([type=checkbox]) { box-sizing: border-box; width: 100%; min-width: 0; padding: .65rem .75rem; border: var(--lv-border-default); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); color: inherit; font: inherit; }
.lv-search-page button { padding: .65rem 1.25rem; border: var(--lv-border-default); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); color: inherit; font: inherit; cursor: pointer; }
.lv-search-page input:focus-visible, .lv-search-page button:focus-visible, .lv-search-page a:focus-visible { outline: 2px solid var(--lv-fg-accent); outline-offset: 2px; }
.lv-search-page fieldset { margin: 1.25rem 0; padding: 0; border: 0; }
.lv-search-page legend { margin-bottom: .75rem; }
.lv-search-page .search-kinds { display: flex; flex-wrap: wrap; gap: .75rem 1.25rem; }
.lv-search-page .search-kind { display: inline-flex; align-items: center; gap: .4rem; }
.lv-search-page .search-hint { font-size: .8rem; }
.lv-search-page .search-filter-row label:first-child { flex: 1; }
.lv-search-page .search-filter-row label:last-child { width: 10rem; }
.lv-search-page .search-results-heading { margin-top: 1.5rem; display: flex; justify-content: space-between; align-items: center; gap: 1rem; }
.lv-search-page .search-results { list-style: none; margin: .5rem 0; padding: 0; }
.lv-search-page .search-result { padding: 1.25rem 0; border-bottom: var(--lv-border-default); }
.lv-search-page .search-result-heading { display: flex; align-items: center; gap: .75rem; }
.lv-search-page h2 { margin: 0; font-size: 1rem; font-weight: 600; overflow-wrap: anywhere; }
.lv-search-page a { color: var(--lv-fg-accent); text-decoration: none; }
.lv-search-page a:hover { text-decoration: underline; }
.lv-search-page .search-badge { flex-shrink: 0; font-size: .75rem; padding: .2rem .5rem; background: var(--lv-bg-panel-muted); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); }
.lv-search-page .search-result p { margin: .5rem 0; }
.lv-search-page .search-result-meta { display: flex; flex-wrap: wrap; gap: .5rem 1rem; font-size: .8rem; margin-top: .5rem; overflow-wrap: anywhere; }
.lv-search-page .search-pagination { display: flex; justify-content: space-between; margin-top: 1.5rem; }
@media (max-width: 640px) { .lv-search-page { padding: 1rem; } .lv-search-page .search-results-heading { align-items: start; flex-direction: column; gap: 0; } .lv-search-page .search-filter-row { flex-wrap: wrap; } }
`
