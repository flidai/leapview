package httpmiddleware

import (
	"net/http"
	"strings"
)

// ContentSecurityPolicyConfig describes the small set of browser capabilities
// used by LeapView document surfaces. Non-document responses should use the
// zero-value capability flags so executable strings and inline styles remain
// disabled.
type ContentSecurityPolicyConfig struct {
	BaseURI             string
	FrameAncestors      string
	FormAction          string
	FrameSrc            string
	DatastarExpressions bool
	DynamicStyles       bool
}

// ContentSecurityPolicy returns a deterministic CSP for a LeapView surface.
// Datastar currently compiles declarative data-* expressions in the browser,
// so unsafe-eval is deliberately restricted to interactive HTML documents.
// Lit and the server-rendered shell require generated style elements and
// attributes; CSP level-three directives keep that exception out of script
// policy and away from non-document responses.
func ContentSecurityPolicy(config ContentSecurityPolicyConfig) string {
	baseURI := cspValue(config.BaseURI, "'self'")
	frameAncestors := cspValue(config.FrameAncestors, "'self'")
	formAction := cspValue(config.FormAction, "'self'")
	scriptSrc := "script-src 'self'"
	if config.DatastarExpressions {
		scriptSrc += " 'unsafe-eval'"
	}
	styleElem := "style-src-elem 'self'"
	styleAttr := "style-src-attr 'none'"
	if config.DynamicStyles {
		styleElem += " 'unsafe-inline'"
		styleAttr = "style-src-attr 'unsafe-inline'"
	}
	directives := []string{
		"default-src 'self'",
		"base-uri " + baseURI,
		"object-src 'none'",
		"frame-ancestors " + frameAncestors,
		"form-action " + formAction,
		scriptSrc,
		"script-src-attr 'none'",
		"style-src 'self'",
		styleElem,
		styleAttr,
		"img-src 'self' data: blob:",
		"font-src 'self' data:",
		"connect-src 'self'",
		"worker-src 'self' blob:",
		"manifest-src 'self'",
	}
	if frameSrc := strings.TrimSpace(config.FrameSrc); frameSrc != "" {
		directives = append(directives, "frame-src "+frameSrc)
	}
	return strings.Join(directives, "; ")
}

func cspValue(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

// ContentSecurityPolicyByMediaType applies documentPolicy only to HTML
// responses. Everything else receives strictPolicy. A handler-owned CSP is
// preserved so specialized surfaces such as public dashboard embeds can set a
// narrower frame-ancestors policy.
func ContentSecurityPolicyByMediaType(strictPolicy, documentPolicy string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Security-Policy", strictPolicy)
			next.ServeHTTP(&contentSecurityPolicyWriter{
				ResponseWriter: w,
				strictPolicy:   strictPolicy,
				documentPolicy: documentPolicy,
			}, r)
		})
	}
}

type contentSecurityPolicyWriter struct {
	http.ResponseWriter
	strictPolicy   string
	documentPolicy string
	applied        bool
}

func (w *contentSecurityPolicyWriter) WriteHeader(statusCode int) {
	w.apply(w.Header().Get("Content-Type"))
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *contentSecurityPolicyWriter) Write(contents []byte) (int, error) {
	contentType := w.Header().Get("Content-Type")
	if contentType == "" && len(contents) > 0 {
		contentType = http.DetectContentType(contents)
		w.Header().Set("Content-Type", contentType)
	}
	w.apply(contentType)
	return w.ResponseWriter.Write(contents)
}

func (w *contentSecurityPolicyWriter) Flush() {
	w.apply(w.Header().Get("Content-Type"))
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *contentSecurityPolicyWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *contentSecurityPolicyWriter) apply(contentType string) {
	if w.applied {
		return
	}
	w.applied = true
	if current := w.Header().Get("Content-Security-Policy"); current != "" && current != w.strictPolicy {
		return
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		w.Header().Set("Content-Security-Policy", w.documentPolicy)
		return
	}
	w.Header().Set("Content-Security-Policy", w.strictPolicy)
}
