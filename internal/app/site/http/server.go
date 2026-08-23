// Package http serves the public LeapView website and documentation portal.
package http

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/Yacobolo/toolbelt/pagestream"
	mapassethttp "github.com/flidai/leapview/internal/dashboard/visualization/mapasset/http"
	siteassets "github.com/flidai/leapview/site"
)

// Options configures public URLs and production behavior for the site handler.
type Options struct {
	// BaseURL is the externally visible site origin. When omitted, canonical URLs
	// are derived from each request, which is convenient for local development.
	BaseURL *url.URL
	// ShowcaseEmbedURL is the live public dashboard iframe URL. When omitted,
	// the showcase route and navigation are not registered.
	ShowcaseEmbedURL *url.URL
	// BuildRevision is the source revision embedded in the running site image.
	BuildRevision string
	// ImageReference is the immutable image reference selected by deployment.
	ImageReference string
}

type siteServer struct {
	baseURL          *url.URL
	buildRevision    string
	imageReference   string
	showcaseEmbedURL *url.URL
	showcaseOrigin   string
}

// NewHandler builds the public site HTTP handler without starting a server.
func NewHandler() http.Handler {
	return NewHandlerWithOptions(Options{})
}

// NewHandlerWithOptions builds the public site HTTP handler with deployment settings.
func NewHandlerWithOptions(options Options) http.Handler {
	server := &siteServer{
		baseURL:          cloneURL(options.BaseURL),
		buildRevision:    strings.TrimSpace(options.BuildRevision),
		imageReference:   strings.TrimSpace(options.ImageReference),
		showcaseEmbedURL: cloneURL(options.ShowcaseEmbedURL),
	}
	if server.showcaseEmbedURL != nil {
		server.showcaseOrigin = (&url.URL{Scheme: server.showcaseEmbedURL.Scheme, Host: server.showcaseEmbedURL.Host}).String()
	}
	mux := http.NewServeMux()
	documentationMCP := newDocumentationMCPHandler()
	mux.Handle("GET /mcp", documentationMCP)
	mux.Handle("POST /mcp", documentationMCP)
	mux.Handle("DELETE /mcp", documentationMCP)
	mux.HandleFunc("GET /{$}", server.home)
	mux.HandleFunc("GET /download", server.desktopDownload)
	mux.HandleFunc("GET /visuals", server.visuals)
	mux.HandleFunc("GET /visuals/responsive", server.responsiveWidgets)
	if server.showcaseEmbedURL != nil {
		mux.HandleFunc("GET /showcase", server.showcase)
	}
	mux.HandleFunc("GET /docs", server.docsIndex)
	mux.HandleFunc("GET /docs/search", server.docsSearch)
	mux.HandleFunc("GET /docs/search/active", docsActiveSearch)
	mux.HandleFunc("GET /docs/openapi.yaml", docsOpenAPISpecification)
	mux.HandleFunc("GET /docs/agent-tools/manifest.json", docsAgentToolManifest)
	mux.HandleFunc("GET /docs/agent-tools/tools/{tool}", docsAgentTool)
	mux.HandleFunc("GET /docs/cli/manifest.json", docsCLIManifest)
	mux.HandleFunc("GET /docs/cli/commands/{command}", docsCLICommand)
	mux.HandleFunc("GET /docs/api/operations.json", docsAPIOperations)
	mux.HandleFunc("GET /docs/api/operations/{operation}", docsAPIOperation)
	mux.HandleFunc("GET /docs/schemas/{schema}", docsConfigurationSchema)
	mux.HandleFunc("GET /docs/{path...}", server.docsArticle)
	mux.HandleFunc("GET /getting-started", gettingStarted)
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /readyz", health)
	mux.HandleFunc("GET /build.json", server.buildIdentity)
	mux.HandleFunc("GET /release.json", docsPublicRelease)
	mux.HandleFunc("GET /desktop-release.json", docsDesktopRelease)
	mux.HandleFunc("GET /robots.txt", server.robots)
	mux.HandleFunc("GET /llms.txt", docsLLMs)
	mux.HandleFunc("GET /sitemap.xml", server.sitemap)
	mux.HandleFunc("GET /updates", updates)
	mux.Handle("GET /static/", compressedAssets(http.StripPrefix("/static/", http.FileServer(http.FS(siteassets.Static())))))
	mux.Handle("GET /openlineage/", compressedAssets(http.FileServer(http.FS(siteassets.Static()))))
	mux.Handle("GET /shared/", compressedAssets(http.StripPrefix("/shared/", http.FileServer(http.FS(siteassets.Shared())))))
	mux.Handle("GET /map-assets/", mapassethttp.CacheHandler(http.StripPrefix("/map-assets/", siteMapAssets())))
	mux.HandleFunc("GET /{path...}", server.notFound)
	return server.productionHeaders(mux)
}

func (s *siteServer) buildIdentity(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(struct {
		SchemaVersion int    `json:"schemaVersion"`
		Revision      string `json:"revision"`
		Image         string `json:"image"`
	}{
		SchemaVersion: 1,
		Revision:      s.buildRevision,
		Image:         s.imageReference,
	}); err != nil {
		panic(fmt.Sprintf("encode site build identity: %v", err))
	}
}

func siteMapAssets() http.Handler {
	diskFS := os.DirFS(".data/map-assets")
	disk := http.FileServer(http.FS(diskFS))
	embedded := http.FileServer(http.FS(siteassets.MapAssets()))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path != "" && fs.ValidPath(path) {
			if _, err := fs.Stat(diskFS, path); err == nil {
				disk.ServeHTTP(w, r)
				return
			}
		}
		embedded.ServeHTTP(w, r)
	})
}

func cloneURL(value *url.URL) *url.URL {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func (s *siteServer) productionHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		policy := "default-src 'self'; base-uri 'self'; connect-src 'self'; font-src 'self'; frame-ancestors 'none'; form-action 'self'; img-src 'self' data: blob:; object-src 'none'; script-src 'self' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; worker-src 'self' blob:"
		if r.URL.Path == "/showcase" && s.showcaseOrigin != "" {
			policy += "; frame-src " + s.showcaseOrigin
		}
		w.Header().Set("Content-Security-Policy", policy)
		w.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=()")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		if s.baseURL != nil && s.baseURL.Scheme == "https" {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/map-assets/"):
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			w.Header().Set("Accept-Ranges", "bytes")
		case strings.HasPrefix(r.URL.Path, "/static/chunks/"):
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		case strings.HasPrefix(r.URL.Path, "/openlineage/facets/"):
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		case strings.HasPrefix(r.URL.Path, "/static/"), strings.HasPrefix(r.URL.Path, "/shared/"):
			w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
		default:
			w.Header().Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

func compressedAssets(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !acceptsGzip(r.Header.Get("Accept-Encoding")) || r.Header.Get("Range") != "" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		compressed := gzip.NewWriter(w)
		defer compressed.Close()
		next.ServeHTTP(&gzipResponseWriter{ResponseWriter: w, writer: compressed}, r)
	})
}

func acceptsGzip(header string) bool {
	for _, value := range strings.Split(header, ",") {
		parts := strings.Split(strings.TrimSpace(value), ";")
		if parts[0] != "gzip" {
			continue
		}
		for _, parameter := range parts[1:] {
			if strings.TrimSpace(parameter) == "q=0" {
				return false
			}
		}
		return true
	}
	return false
}

type gzipResponseWriter struct {
	http.ResponseWriter
	writer io.Writer
}

type docsActiveSearchResult struct {
	Href    string `json:"href"`
	Summary string `json:"summary"`
	Title   string `json:"title"`
}

func (w *gzipResponseWriter) WriteHeader(statusCode int) {
	w.Header().Del("Content-Length")
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *gzipResponseWriter) Write(contents []byte) (int, error) {
	w.Header().Del("Content-Length")
	return w.writer.Write(contents)
}

func (s *siteServer) home(w http.ResponseWriter, r *http.Request) {
	metadata := s.metadata(r, siteBrandName+" — agent-native BI and analytics as code", "Build dashboards as code, keep analytics in version control, and explore data with native AI agents.", "website", "")
	renderHTML(w, http.StatusOK, sitePage(metadata), "render site page")
}

func (s *siteServer) visuals(w http.ResponseWriter, r *http.Request) {
	metadata := s.metadata(r, siteBrandName+" visual showcase", "Explore "+siteBrandName+" charts, KPIs, tables, matrices, and pivots.", "website", "")
	renderHTML(w, http.StatusOK, visualsPage(metadata), "render visuals page")
}

func (s *siteServer) desktopDownload(w http.ResponseWriter, r *http.Request) {
	metadata := s.metadata(r, "Download "+siteBrandName+" Desktop", "Install "+siteBrandName+" Desktop for macOS, Windows, or Ubuntu.", "website", "")
	renderHTML(w, http.StatusOK, desktopDownloadPage(metadata, desktopRelease), "render desktop download page")
}

func (s *siteServer) responsiveWidgets(w http.ResponseWriter, r *http.Request) {
	metadata := s.metadata(r, siteBrandName+" responsive widget reference", "Inspect every responsive KPI and dashboard filter layout at deterministic and intermediate dimensions.", "website", "")
	renderHTML(w, http.StatusOK, responsiveWidgetsPage(metadata), "render responsive widget reference")
}

func (s *siteServer) showcase(w http.ResponseWriter, r *http.Request) {
	metadata := s.metadata(r, siteBrandName+" live dashboard showcase", "Explore a live, interactive "+siteBrandName+" dashboard.", "website", "")
	renderHTML(w, http.StatusOK, showcasePage(metadata, s.showcaseEmbedURL), "render dashboard showcase")
}

func gettingStarted(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/docs/getting-started", http.StatusPermanentRedirect)
}

func (s *siteServer) docsIndex(w http.ResponseWriter, r *http.Request) {
	metadata := s.metadata(r, siteBrandName+" documentation", "Learn how to install, model, build, secure, integrate, and operate "+siteBrandName+".", "website", "")
	renderHTML(w, http.StatusOK, docsIndexPage(metadata), "render docs index")
}

func (s *siteServer) docsSearch(w http.ResponseWriter, r *http.Request) {
	metadata := s.metadata(r, "Search "+siteBrandName+" documentation", "Search "+siteBrandName+" concepts, guides, commands, and API documentation.", "website", "noindex,follow")
	renderHTML(w, http.StatusOK, docsSearchPage(strings.TrimSpace(r.URL.Query().Get("q")), metadata), "render documentation search")
}

func docsActiveSearch(w http.ResponseWriter, r *http.Request) {
	var signals struct {
		DocsSearch struct {
			Query string `json:"query"`
		} `json:"docsSearch"`
	}
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		http.Error(w, "read documentation search signals", http.StatusBadRequest)
		return
	}

	query := strings.TrimSpace(signals.DocsSearch.Query)
	matches := searchSiteDocuments(query)
	const resultLimit = 8
	visible := matches
	if len(visible) > resultLimit {
		visible = visible[:resultLimit]
	}
	results := make([]docsActiveSearchResult, 0, len(visible))
	for _, document := range visible {
		results = append(results, docsActiveSearchResult{
			Href:    "/docs/" + document.slug,
			Summary: document.summary,
			Title:   document.title,
		})
	}

	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{
		"docsSearch": map[string]any{
			"loading":     false,
			"resultQuery": query,
			"results":     results,
			"total":       len(matches),
		},
	})
}

func (s *siteServer) docsArticle(w http.ResponseWriter, r *http.Request) {
	document, ok := siteDocumentBySlug(r.PathValue("path"))
	if !ok {
		if location, legacy := legacyCLICommandLocation(r.PathValue("path")); legacy {
			http.Redirect(w, r, location, http.StatusPermanentRedirect)
			return
		}
		s.notFound(w, r)
		return
	}
	metadata := s.metadata(r, document.title, document.summary, "article", "")
	renderHTML(w, http.StatusOK, docsArticlePage(document, metadata), "render documentation article")
}

func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok\n")
}

func (s *siteServer) notFound(w http.ResponseWriter, r *http.Request) {
	metadata := s.metadata(r, "Page not found — "+siteBrandName, "The requested "+siteBrandName+" page could not be found.", "website", "noindex,follow")
	renderHTML(w, http.StatusNotFound, notFoundPage(metadata), "render not found page")
}

type renderable interface {
	Render(io.Writer) error
}

func renderHTML(w http.ResponseWriter, status int, page renderable, errorMessage string) {
	var body bytes.Buffer
	if err := page.Render(&body); err != nil {
		http.Error(w, errorMessage, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body.Bytes())
}

func (s *siteServer) metadata(r *http.Request, title, description, contentType, robots string) sitePageMetadata {
	return sitePageMetadata{
		title:       title,
		description: description,
		canonical:   s.absoluteURL(r, r.URL.Path),
		contentType: contentType,
		robots:      robots,
		showcase:    s.showcaseEmbedURL != nil,
	}
}

func (s *siteServer) absoluteURL(r *http.Request, requestedPath string) string {
	base := url.URL{Scheme: "http", Host: r.Host}
	if r.TLS != nil {
		base.Scheme = "https"
	}
	if s.baseURL != nil {
		base = *s.baseURL
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/" + strings.TrimLeft(requestedPath, "/")
	if requestedPath == "/" {
		base.Path = strings.TrimRight(base.Path, "/") + "/"
	}
	base.RawPath = ""
	base.RawQuery = ""
	base.Fragment = ""
	return base.String()
}

func (s *siteServer) sitemap(w http.ResponseWriter, r *http.Request) {
	paths := []string{"/", "/download", "/visuals", "/visuals/responsive", "/docs"}
	if s.showcaseEmbedURL != nil {
		paths = append(paths, "/showcase")
	}
	for _, document := range siteDocuments {
		paths = append(paths, "/docs/"+document.slug)
	}
	var body bytes.Buffer
	body.WriteString(xml.Header)
	body.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
	for _, path := range paths {
		body.WriteString("<url><loc>")
		_ = xml.EscapeText(&body, []byte(s.absoluteURL(r, path)))
		body.WriteString("</loc></url>")
	}
	body.WriteString("</urlset>\n")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write(body.Bytes())
}

func (s *siteServer) robots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprintf(w, "User-agent: *\nAllow: /\nDisallow: /docs/search\nSitemap: %s\n", s.absoluteURL(r, "/sitemap.xml"))
}

func docsOpenAPISpecification(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	_, _ = w.Write(siteOpenAPISpecification())
}

func docsConfigurationSchema(w http.ResponseWriter, r *http.Request) {
	schema, ok := siteConfigurationSchema(r.PathValue("schema"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/schema+json; charset=utf-8")
	_, _ = w.Write(schema)
}

func updates(w http.ResponseWriter, r *http.Request) {
	patch := pagestream.SignalPatch{"site": map[string]any{"ready": true}}
	switch r.URL.Query().Get("view") {
	case "visuals":
		patch = visualShowcasePatch()
	case "responsive-widgets":
		patch = responsiveWidgetReferencePatch()
	case "visual-docs":
		examples, ok := visualExamplesForDocument(r.URL.Query().Get("document"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		patch = pagestream.SignalPatch{"visuals": examples}
	}
	stream := pagestream.NewSignalStream(w, r)
	if err := stream.Patch(patch); err != nil {
		return
	}
}
