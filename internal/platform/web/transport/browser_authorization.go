package transport

import (
	"html/template"
	"net/http"
	"strings"
)

var browserAuthorizationPage = template.Must(template.New("browser-authorization").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Access unavailable | LeapView</title>
  <link rel="icon" href="/static/favicon.svg">
  <link rel="stylesheet" href="/static/app.css">
</head>
<body class="min-h-svh bg-app text-fg-default flex items-center justify-center p-6">
  <main class="w-full max-w-lg rounded-xl border border-border-default bg-canvas-default p-6 shadow-lg" aria-labelledby="access-title">
    <p class="text-sm text-fg-muted">LeapView</p>
    <h1 class="mt-3 text-xl font-semibold" id="access-title">You don't have access to this {{.Area}}</h1>
    <p class="mt-3 text-sm text-fg-muted">Your session is active, but your current role does not allow this action. No changes were made.</p>
    <a class="mt-5 inline-flex items-center rounded-md border border-border-default px-3 py-2 text-sm font-medium hover:bg-canvas-subtle" href="/">Return to Insights</a>
  </main>
</body>
</html>`))

// WriteBrowserAuthorizationError preserves normal status semantics for
// commands and non-browser clients, while giving HTML navigation an
// accessible recovery surface. Callers must continue to use 404 when resource
// existence itself is confidential.
func WriteBrowserAuthorizationError(w http.ResponseWriter, r *http.Request, status int) {
	if status != http.StatusForbidden || !IsHTMLNavigation(r) {
		http.Error(w, http.StatusText(status), status)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = browserAuthorizationPage.Execute(w, struct{ Area string }{Area: browserRouteArea(r.URL.Path)})
}

// IsHTMLNavigation distinguishes page loads from commands, streams, and API
// requests so authorization recovery never changes their status semantics.
func IsHTMLNavigation(r *http.Request) bool {
	if r == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) || r.URL == nil || strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/updates" || strings.HasSuffix(r.URL.Path, "/updates") {
		return false
	}
	accept := strings.ToLower(strings.TrimSpace(r.Header.Get("Accept")))
	return accept == "" || strings.Contains(accept, "text/html") || strings.Contains(accept, "*/*")
}

func browserRouteArea(path string) string {
	segment := strings.Trim(strings.SplitN(strings.TrimPrefix(path, "/"), "/", 2)[0], " ")
	switch segment {
	case "admin":
		return "administration page"
	case "dashboards", "candidates":
		return "dashboard"
	case "models", "semantic-models", "sources", "pipelines", "connections", "explore":
		return "data page"
	default:
		return "page"
	}
}
