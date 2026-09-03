package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	content "github.com/flidai/leapview/docs"
	docsearch "github.com/flidai/leapview/internal/app/site/search"
	"github.com/flidai/leapview/internal/app/site/visualdocs"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

var markdownRenderer = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAttribute(), parser.WithAutoHeadingID()),
	goldmark.WithRendererOptions(renderer.WithNodeRenderers(util.Prioritized(semanticTableCellRenderer{}, 100))),
)

type siteDocument struct {
	slug               string
	title              string
	breadcrumb         string
	breadcrumbRoot     string
	breadcrumbRootHref string
	summary            string
	markdown           string
	sectionID          string
	groupID            string
	source             string
	navigationTitle    string
	documentType       string
	generated          bool
}

type siteCatalogDocument struct {
	Slug            string `json:"slug"`
	Title           string `json:"title"`
	Type            string `json:"type"`
	NavigationTitle string `json:"navigationTitle"`
	Summary         string `json:"summary"`
	Source          string `json:"source"`
	Breadcrumb      string `json:"breadcrumb"`
	Generated       bool   `json:"generated"`
}

type siteCatalogGroup struct {
	ID        string                `json:"id"`
	Title     string                `json:"title"`
	Summary   string                `json:"summary"`
	Href      string                `json:"href"`
	Documents []siteCatalogDocument `json:"documents"`
}

type siteCatalogSection struct {
	ID        string                `json:"id"`
	Title     string                `json:"title"`
	Summary   string                `json:"summary"`
	Href      string                `json:"href"`
	Documents []siteCatalogDocument `json:"documents"`
	Groups    []siteCatalogGroup    `json:"groups"`
}

type siteDocumentationCatalog struct {
	Sections []siteCatalogSection `json:"sections"`
}

type loadedDocumentation struct {
	catalog   siteDocumentationCatalog
	documents []siteDocument
	bySlug    map[string]siteDocument
}

var documentation = loadDocumentation()
var siteCatalog = documentation.catalog
var siteDocuments = documentation.documents
var siteDocumentsBySlug = documentation.bySlug
var documentationSearchIndex = loadDocumentationSearchIndex()
var visualDocuments = documentsInCatalogGroup("reference", "visuals", true)
var visualDocumentationCatalogValidated = validateVisualDocumentationCatalog()

func loadDocumentationSearchIndex() *docsearch.Index {
	index, err := docsearch.Open(content.Files, docsearch.Filename)
	if err != nil {
		panic(fmt.Sprintf("open documentation search index: %v", err))
	}
	slugs, err := index.Slugs(context.Background())
	if err != nil {
		panic(fmt.Sprintf("read documentation search index: %v", err))
	}
	if len(slugs) != len(siteDocuments) {
		panic(fmt.Sprintf("documentation search index contains %d documents, catalog contains %d", len(slugs), len(siteDocuments)))
	}
	for _, slug := range slugs {
		if _, exists := siteDocumentsBySlug[slug]; !exists {
			panic(fmt.Sprintf("documentation search index contains unknown slug %q", slug))
		}
	}
	return index
}

func loadDocumentation() loadedDocumentation {
	catalogContents, err := content.Files.ReadFile("catalog.json")
	if err != nil {
		panic(fmt.Sprintf("read documentation catalog: %v", err))
	}
	var catalog siteDocumentationCatalog
	if err := json.Unmarshal(catalogContents, &catalog); err != nil {
		panic(fmt.Sprintf("decode documentation catalog: %v", err))
	}
	loaded := loadedDocumentation{catalog: catalog, bySlug: make(map[string]siteDocument)}
	for _, section := range catalog.Sections {
		for _, document := range section.Documents {
			loaded.add(section, siteCatalogGroup{}, document)
		}
		for _, group := range section.Groups {
			for _, document := range group.Documents {
				loaded.add(section, group, document)
			}
		}
	}
	return loaded
}

func (loaded *loadedDocumentation) add(section siteCatalogSection, group siteCatalogGroup, document siteCatalogDocument) {
	markdown, err := content.Files.ReadFile(document.Source)
	if err != nil {
		panic(fmt.Sprintf("read documentation source %q: %v", document.Source, err))
	}
	if _, exists := loaded.bySlug[document.Slug]; exists {
		panic(fmt.Sprintf("duplicate documentation slug %q", document.Slug))
	}
	rootTitle, rootHref := section.Title, section.Href
	if group.ID != "" {
		rootTitle, rootHref = group.Title, group.Href
	}
	if rootHref == "/docs/"+document.Slug {
		rootTitle, rootHref = "Documentation", "/docs"
	}
	entry := siteDocument{
		slug:               document.Slug,
		title:              document.Title,
		breadcrumb:         firstNonEmpty(document.Breadcrumb, document.Title),
		breadcrumbRoot:     rootTitle,
		breadcrumbRootHref: rootHref,
		summary:            document.Summary,
		markdown:           string(markdown),
		sectionID:          section.ID,
		groupID:            group.ID,
		source:             document.Source,
		navigationTitle:    document.NavigationTitle,
		documentType:       document.Type,
		generated:          document.Generated,
	}
	loaded.documents = append(loaded.documents, entry)
	loaded.bySlug[entry.slug] = entry
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func documentsInCatalogGroup(sectionID, groupID string, skipFirst bool) []siteDocument {
	for _, section := range siteCatalog.Sections {
		if section.ID != sectionID {
			continue
		}
		for _, group := range section.Groups {
			if group.ID != groupID {
				continue
			}
			documents := make([]siteDocument, 0, len(group.Documents))
			for index, document := range group.Documents {
				if skipFirst && index == 0 {
					continue
				}
				documents = append(documents, siteDocumentsBySlug[document.Slug])
			}
			return documents
		}
	}
	return nil
}

func allSiteDocuments() []siteDocument {
	return siteDocuments
}

func siteDocumentBySlug(slug string) (siteDocument, bool) {
	document, ok := siteDocumentsBySlug[strings.Trim(slug, "/")]
	return document, ok
}

func searchSiteDocuments(query string) []siteDocument {
	matches, err := documentationSearchIndex.Search(context.Background(), query, len(siteDocuments))
	if err != nil {
		panic(fmt.Sprintf("search documentation: %v", err))
	}
	results := make([]siteDocument, 0, len(matches))
	for _, match := range matches {
		document, exists := siteDocumentsBySlug[match.Slug]
		if !exists {
			panic(fmt.Sprintf("documentation search returned unknown slug %q", match.Slug))
		}
		results = append(results, document)
	}
	return results
}

func siteConfigurationSchema(name string) ([]byte, bool) {
	if strings.Contains(name, "/") || !strings.HasSuffix(name, ".schema.json") {
		return nil, false
	}
	schema, err := content.Files.ReadFile("reference/config/schemas/" + name)
	return schema, err == nil
}

func siteOpenAPISpecification() []byte {
	specification, err := content.Files.ReadFile("api/openapi.yaml")
	if err != nil {
		panic(fmt.Sprintf("read generated OpenAPI specification: %v", err))
	}
	return specification
}

func docsPublicRelease(w http.ResponseWriter, _ *http.Request) {
	manifest, err := content.Files.ReadFile("public-release.json")
	if err != nil {
		http.Error(w, "read public release manifest", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(manifest)
}

var docsVisualShortcode = regexp.MustCompile(`\{\{<\s*visual\s+id="([a-z0-9_]+)"\s*>}}`)

func siteDocsArticle(document siteDocument) g.Node {
	source := document.markdown
	shortcodes := docsVisualShortcode.FindAllStringSubmatch(source, -1)
	for index, shortcode := range shortcodes {
		id := shortcode[1]
		if !documentHasVisualExample(document.slug, id) {
			panic(fmt.Sprintf("visual shortcode %q is not generated for documentation %s", id, document.slug))
		}
		placeholder := fmt.Sprintf("LEAPVIEW_DOCS_CHART_PLACEHOLDER_%d", index)
		source = strings.Replace(source, shortcode[0], placeholder, 1)
	}
	if strings.Contains(source, "{{< visual") {
		panic(fmt.Sprintf("invalid visual shortcode in documentation: %s", document.slug))
	}

	var rendered bytes.Buffer
	if err := markdownRenderer.Convert([]byte(source), &rendered); err != nil {
		panic(fmt.Sprintf("render documentation Markdown: %v", err))
	}
	renderedHTML := rendered.String()
	for index, shortcode := range shortcodes {
		placeholderID := fmt.Sprintf("LEAPVIEW_DOCS_CHART_PLACEHOLDER_%d", index)
		placeholder := "<p>" + placeholderID + "</p>\n"
		_, ok := visualExampleForDocument(document.slug, shortcode[1])
		if !ok {
			panic(fmt.Sprintf("generated visual example %q is missing for documentation: %s", shortcode[1], document.slug))
		}
		exampleReference, ok := visualExampleReferenceForDocument(document.slug, shortcode[1])
		if !ok {
			panic(fmt.Sprintf("generated visual example reference %q is missing for documentation: %s", shortcode[1], document.slug))
		}
		component := fmt.Sprintf("<lv-site-visual-example example-id=\"%s\"></lv-site-visual-example>\n%s", shortcode[1], renderVisualKeyFields(exampleReference.KeyFields))
		if !strings.Contains(renderedHTML, placeholder) {
			panic(fmt.Sprintf("render visual shortcode %q for documentation: %s", shortcode[1], document.slug))
		}
		renderedHTML = strings.Replace(renderedHTML, placeholder, component, 1)
	}
	if reference, ok := visualReferenceForDocument(document.slug); ok {
		renderedHTML += renderVisualAPIReference(reference)
	}

	return h.Article(
		h.ID("main-content"),
		h.Class("site-docs-article"),
		h.Div(h.Class("site-docs-article-actions"), g.El("lv-site-markdown-copy", g.Attr("markdown", document.markdown))),
		g.Raw(renderedHTML),
		siteDocsArticleFooter(document),
	)
}

func renderVisualAPIReference(reference visualdocs.DocumentReference) string {
	node := g.Group([]g.Node{
		h.H2(h.ID("site-visual-api-reference"), g.Text("API reference")),
		h.P(
			g.Text("Kind: "), g.Group(visualCodeNodes(strings.Split(reference.Kind, ", "))),
			g.Text(". Renderer: "), g.Group(visualCodeNodes(strings.Split(reference.Renderer, ", "))),
			g.Text(". Supported result shapes: "), g.Group(visualCodeNodes(reference.Shapes)),
			g.Text("."),
		),
		h.Table(
			g.Attr("aria-labelledby", "site-visual-api-reference"),
			h.THead(h.Tr(
				h.Th(g.Attr("scope", "col"), g.Text("Field")),
				h.Th(g.Attr("scope", "col"), g.Text("Type")),
				h.Th(g.Attr("scope", "col"), g.Text("Default")),
				h.Th(g.Attr("scope", "col"), g.Text("Allowed values")),
				h.Th(g.Attr("scope", "col"), g.Text("Description")),
			)),
			h.TBody(g.Map(reference.Fields, renderVisualFieldReference)...),
		),
		h.P(h.Strong(g.Text("Accessibility. ")), g.Text(reference.Accessibility)),
	})
	return renderSiteNode(node)
}

func renderVisualKeyFields(fields []string) string {
	encoded, err := json.Marshal(fields)
	if err != nil {
		panic(fmt.Sprintf("encode visual key fields: %v", err))
	}
	buttons := g.Map(fields, func(field string) g.Node {
		return h.Button(
			h.Type("button"),
			h.Class("site-visual-key-field"),
			g.Attr("data-visual-key-field", field),
			g.Attr("aria-label", "Highlight "+field+" in YAML"),
			h.Code(g.Text(field)),
		)
	})
	return renderSiteNode(h.Div(
		h.Class("site-visual-key-fields"),
		g.Attr("aria-label", "Key fields"),
		g.Attr("data-key-fields", string(encoded)),
		h.Strong(g.Text("Key fields")),
		g.Group(buttons),
	))
}

func renderVisualFieldReference(field visualdocs.FieldReference) g.Node {
	return h.Tr(
		h.Th(g.Attr("scope", "row"), h.Code(g.Text(field.Path))),
		h.Td(h.Code(g.Text(field.Type))),
		h.Td(visualFieldValueNodes(field.Default)...),
		h.Td(visualFieldValueNodes(field.AllowedValues...)...),
		h.Td(g.Text(field.Description)),
	)

}

func visualCodeNodes(values []string) []g.Node {
	if len(values) == 0 {
		return []g.Node{h.Span(g.Text("None"))}
	}
	nodes := make([]g.Node, 0, len(values)*2-1)
	for index, value := range values {
		if index > 0 {
			nodes = append(nodes, g.Text(" "))
		}
		nodes = append(nodes, h.Code(g.Text(value)))
	}
	return nodes
}

func visualFieldValueNodes(values ...string) []g.Node {
	nonEmpty := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			nonEmpty = append(nonEmpty, value)
		}
	}
	if len(nonEmpty) == 0 {
		return []g.Node{g.Text("—")}
	}
	return visualCodeNodes(nonEmpty)
}

func renderSiteNode(node g.Node) string {
	var rendered strings.Builder
	if err := node.Render(&rendered); err != nil {
		panic(fmt.Sprintf("render site component: %v", err))
	}
	return rendered.String()
}

func siteDocsArticleFooter(document siteDocument) g.Node {
	sourceLabel, sourceHref := documentationSourceLink(document)
	return h.Footer(h.Class("site-docs-article-footer"),
		siteDocsPagination(document),
		h.Section(h.Class("site-docs-page-meta"), g.Attr("aria-labelledby", "site-docs-about-this-page"),
			h.H2(h.ID("site-docs-about-this-page"), g.Text("About this page")),
			h.Ul(
				h.Li(h.A(h.Href(documentationIssueLink(document)), g.Attr("rel", "external"), g.Text("Report content issue"))),
				h.Li(h.A(h.Href(documentationMarkdownLink(document)), g.Attr("rel", "external"), g.Text("See this page as Markdown"))),
				h.Li(h.A(h.Href(sourceHref), g.Attr("rel", "external"), g.Text(sourceLabel))),
			),
		),
	)
}

func siteDocsPagination(document siteDocument) g.Node {
	previous, next := adjacentSiteDocuments(document.slug)
	if previous == nil && next == nil {
		return nil
	}
	return h.Nav(
		h.Class("site-docs-pagination"),
		g.Attr("aria-label", "Documentation pagination"),
		siteDocsPaginationCard(previous, "previous"),
		siteDocsPaginationCard(next, "next"),
	)
}

func adjacentSiteDocuments(slug string) (previous, next *siteDocument) {
	for index := range siteDocuments {
		if siteDocuments[index].slug != slug {
			continue
		}
		if index > 0 {
			previous = &siteDocuments[index-1]
		}
		if index+1 < len(siteDocuments) {
			next = &siteDocuments[index+1]
		}
		return previous, next
	}
	return nil, nil
}

func siteDocsPaginationCard(document *siteDocument, direction string) g.Node {
	if document == nil {
		return nil
	}
	label, rel, arrow := "Next", "next", "→"
	if direction == "previous" {
		label, rel, arrow = "Previous", "prev", "←"
	}
	directionNodes := []g.Node{g.Text(label), h.Span(h.Class("site-docs-pagination-arrow"), g.Attr("aria-hidden", "true"), g.Text(arrow))}
	if direction == "previous" {
		directionNodes[0], directionNodes[1] = directionNodes[1], directionNodes[0]
	}
	return h.A(
		h.Class("site-docs-pagination-card site-docs-pagination-"+direction),
		h.Href("/docs/"+document.slug),
		h.Rel(rel),
		g.Attr("aria-label", label+" page: "+document.title),
		h.Span(h.Class("site-docs-pagination-direction"), g.Group(directionNodes)),
		h.Span(h.Class("site-docs-pagination-title"), g.Text(document.title)),
	)
}

func documentationIssueLink(document siteDocument) string {
	query := url.Values{}
	query.Set("title", "Docs: "+document.title)
	query.Set("labels", "documentation")
	query.Set("body", "Page: /docs/"+document.slug+"\n\nDescribe the content issue or suggested improvement.")
	return "https://github.com/flidai/leapview/issues/new?" + query.Encode()
}

func documentationMarkdownLink(document siteDocument) string {
	return "https://raw.githubusercontent.com/flidai/leapview/main/docs/" + document.source
}

func documentationSourceLink(document siteDocument) (string, string) {
	const repository = "https://github.com/flidai/leapview/"
	if !strings.HasPrefix(document.markdown, "<!-- Code generated") {
		return "Edit this page on GitHub", repository + "edit/main/docs/" + document.source
	}
	switch {
	case document.source == "configuration.md":
		return "View source contract on GitHub", repository + "blob/main/internal/app/config/spec/spec.go"
	case strings.HasPrefix(document.source, "reference/config/"):
		return "View source contract on GitHub", repository + "blob/main/internal/project/schema/contracts/contracts.cue"
	case strings.HasPrefix(document.source, "reference/cli/"):
		return "View source contract on GitHub", repository + "tree/main/internal/app/cli"
	case strings.HasPrefix(document.source, "api/"):
		return "View source contract on GitHub", repository + "tree/main/api/typespec"
	default:
		return "View source on GitHub", repository + "blob/main/docs/" + document.source
	}
}
