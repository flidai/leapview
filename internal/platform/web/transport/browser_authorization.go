package transport

import (
	"html/template"
	"net/http"
	"strings"

	"github.com/gorilla/csrf"
)

var browserAuthorizationPage = template.Must(template.New("browser-authorization").Parse(`<!doctype html>
<html lang="en" data-color-mode="auto" data-light-theme="light" data-dark-theme="dark">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Access required | LeapView</title>
  <link rel="icon" href="/static/favicon.svg" type="image/svg+xml">
  <link rel="stylesheet" href="/static/app.css">
  <script src="/static/theme.js"></script>
</head>
<body class="min-h-svh bg-app text-fg-default flex items-center justify-center p-6">
  <main class="w-full max-w-lg rounded-xl border border-border-default bg-canvas-default p-6 shadow-lg" aria-labelledby="access-title" aria-describedby="access-detail">
    <p class="text-sm text-fg-muted">LeapView</p>
    <h1 class="mt-3 text-xl font-semibold" id="access-title">You're signed in, but you don't have access to {{.Area}}</h1>
    <p class="mt-3 text-sm text-fg-muted" id="access-detail">A LeapView administrator needs to assign your account the required role or grant before you can open {{.Area}}.</p>
    <form class="mt-5" method="post" action="/auth/switch-account">
      <input type="hidden" name="return_to" value="{{.ReturnTo}}">
      <input type="hidden" name="gorilla.csrf.Token" value="{{.CSRFToken}}">
      <button class="inline-flex items-center rounded-md border border-border-default px-3 py-2 text-sm font-medium hover:bg-canvas-subtle" type="submit">Sign in with a different account</button>
    </form>
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
	_ = browserAuthorizationPage.Execute(w, struct {
		Area      string
		ReturnTo  string
		CSRFToken string
	}{
		Area:      browserRouteArea(r.URL.Path),
		ReturnTo:  r.URL.RequestURI(),
		CSRFToken: csrf.Token(r),
	})
}

// IsHTMLNavigation distinguishes page loads from commands, streams, and API
// requests so authorization recovery never changes their status semantics.
func IsHTMLNavigation(r *http.Request) bool {
	if r == nil || r.URL == nil || strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/updates" || strings.HasSuffix(r.URL.Path, "/updates") {
		return false
	}
	accept := strings.ToLower(strings.TrimSpace(r.Header.Get("Accept")))
	if r.Header.Get("Datastar-Request") != "" || strings.Contains(accept, "text/event-stream") {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		// A native browser form submission is a document navigation even though
		// it mutates state. JSON/Datastar commands keep their status-only
		// semantics, including when a browser sends an HTML Accept header.
		if r.Method != http.MethodPost || !strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "application/x-www-form-urlencoded") {
			return false
		}
	}
	return accept == "" || strings.Contains(accept, "text/html") || strings.Contains(accept, "*/*")
}

func browserRouteArea(path string) string {
	segment := strings.Trim(strings.SplitN(strings.TrimPrefix(path, "/"), "/", 2)[0], " ")
	switch segment {
	case "":
		return "Insights"
	case "admin":
		return "this administration page"
	case "dashboards", "candidates":
		return "this dashboard"
	case "models", "semantic-models", "sources", "pipelines", "connections", "explore":
		return "this data page"
	default:
		return "this page"
	}
}
