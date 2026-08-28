package httpmiddleware

import (
	"bufio"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/go-chi/httprate"
)

const DefaultMaxRequestBodyBytes int64 = 128 << 20

type RateLimitConfig struct {
	Enabled             bool
	UseRealIP           bool
	AuthLimit           int
	AuthWindow          time.Duration
	APILimit            int
	APIWindow           time.Duration
	UpdatesLimit        int
	UpdatesWindow       time.Duration
	PublicPageLimit     int
	PublicPageWindow    time.Duration
	PublicCommandLimit  int
	PublicCommandWindow time.Duration
	PublicStreamLimit   int
	PublicStreamWindow  time.Duration
}

type SecurityHeadersConfig struct {
	Enabled bool
	HSTS    bool
}

type RequestBodyLimitConfig struct {
	Enabled  bool
	MaxBytes int64
}

func DefaultRequestBodyLimitConfig() RequestBodyLimitConfig {
	return RequestBodyLimitConfig{Enabled: true, MaxBytes: DefaultMaxRequestBodyBytes}
}

func ProductionRateLimitConfig() RateLimitConfig {
	return RateLimitConfig{
		Enabled:             true,
		UseRealIP:           false,
		AuthLimit:           20,
		AuthWindow:          time.Minute,
		APILimit:            120,
		APIWindow:           time.Minute,
		UpdatesLimit:        120,
		UpdatesWindow:       time.Minute,
		PublicPageLimit:     120,
		PublicPageWindow:    time.Minute,
		PublicCommandLimit:  600,
		PublicCommandWindow: time.Minute,
		PublicStreamLimit:   60,
		PublicStreamWindow:  time.Minute,
	}
}

func SecurityHeaders(hsts bool) SecurityHeadersConfig {
	return SecurityHeadersConfig{Enabled: true, HSTS: hsts}
}

func AllowedHosts(hosts []string) func(http.Handler) http.Handler {
	allowed := compileAllowedHosts(hosts)
	return func(next http.Handler) http.Handler {
		if len(allowed.exact) == 0 && len(allowed.wildcards) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !allowed.matches(r.Host, r.RemoteAddr) {
				writeMiddlewareError(w, r, http.StatusMisdirectedRequest, "MISDIRECTED_REQUEST", "The request host is not allowed")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type allowedHostSet struct {
	exact     map[string]struct{}
	wildcards []string
}

func compileAllowedHosts(hosts []string) allowedHostSet {
	set := allowedHostSet{exact: map[string]struct{}{}}
	for _, raw := range hosts {
		host := normalizeRequestHost(raw)
		if host == "" {
			continue
		}
		if strings.HasPrefix(host, "*.") {
			suffix := strings.TrimPrefix(host, "*.")
			if suffix != "" {
				set.wildcards = append(set.wildcards, suffix)
			}
			continue
		}
		set.exact[host] = struct{}{}
	}
	return set
}

func (s allowedHostSet) matches(raw, remoteAddr string) bool {
	host := normalizeRequestHost(raw)
	if host == "" {
		return false
	}
	if _, ok := s.exact[host]; ok {
		return true
	}
	for _, suffix := range s.wildcards {
		if strings.HasSuffix(host, "."+suffix) && len(host) > len(suffix)+1 {
			return true
		}
	}
	if isLoopbackHost(host) && isLoopbackRemote(remoteAddr) {
		return true
	}
	return false
}

func normalizeRequestHost(raw string) string {
	host := strings.ToLower(strings.TrimSpace(raw))
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return ""
	}
	if strings.HasPrefix(host, "[") {
		if parsed, _, err := net.SplitHostPort(host); err == nil {
			host = parsed
		}
		return strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	}
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	return host
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isLoopbackRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		host = strings.TrimSpace(remoteAddr)
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c RateLimitConfig) Auth() func(http.Handler) http.Handler {
	return c.middleware(c.AuthLimit, c.AuthWindow)
}

func (c RateLimitConfig) API() func(http.Handler) http.Handler {
	return c.middleware(c.APILimit, c.APIWindow)
}

func (c RateLimitConfig) Updates() func(http.Handler) http.Handler {
	return c.middleware(c.UpdatesLimit, c.UpdatesWindow)
}

func (c RateLimitConfig) PublicPage(rejected func()) func(http.Handler) http.Handler {
	return c.middlewareWithRejection(c.PublicPageLimit, c.PublicPageWindow, rejected)
}

func (c RateLimitConfig) PublicCommand(rejected func()) func(http.Handler) http.Handler {
	return c.middlewareWithRejection(c.PublicCommandLimit, c.PublicCommandWindow, rejected)
}

func (c RateLimitConfig) PublicStream(rejected func()) func(http.Handler) http.Handler {
	return c.middlewareWithRejection(c.PublicStreamLimit, c.PublicStreamWindow, rejected)
}

func (c RateLimitConfig) middleware(limit int, window time.Duration) func(http.Handler) http.Handler {
	return c.middlewareWithRejection(limit, window, nil)
}

func (c RateLimitConfig) middlewareWithRejection(limit int, window time.Duration, rejected func()) func(http.Handler) http.Handler {
	if !c.Enabled || limit <= 0 || window <= 0 {
		return passthrough
	}
	keyFunc := httprate.KeyByIP
	if c.UseRealIP {
		keyFunc = httprate.KeyByRealIP
	}
	return httprate.Limit(limit, window,
		httprate.WithKeyFuncs(keyFunc, httprate.KeyByEndpoint),
		httprate.WithLimitHandler(func(w http.ResponseWriter, r *http.Request) {
			if rejected != nil {
				rejected()
			}
			writeMiddlewareError(w, r, http.StatusTooManyRequests, "RATE_LIMITED", "The request rate limit was exceeded")
		}),
	)
}

func RequestBodyLimit(config RequestBodyLimitConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !config.Enabled || config.MaxBytes <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil && r.Body != http.NoBody {
				if r.ContentLength > config.MaxBytes {
					writeMiddlewareError(w, r, http.StatusRequestEntityTooLarge, "CONTENT_TOO_LARGE", "The request body exceeds the configured size limit")
					return
				}
				r.Body = http.MaxBytesReader(w, r.Body, config.MaxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// PrivateResponse prevents browsers and shared intermediaries from retaining
// authenticated or authentication-flow responses.
func PrivateResponse(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		next.ServeHTTP(w, r)
	})
}

func PanicRecovery(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if recovered := recover(); recovered != nil {
					logger.ErrorContext(r.Context(), "http handler panic",
						"method", r.Method,
						"path", r.URL.Path,
						"panic", recovered,
						"stack", string(debug.Stack()),
					)
					writeMiddlewareError(w, r, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", "The server could not complete the request")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func writeMiddlewareError(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	if r != nil && (strings.HasPrefix(r.URL.Path, "/api/v1/") || strings.HasPrefix(r.URL.Path, "/upload-protocols/")) {
		apitransport.WriteProblem(w, r, status, code, detail, nil)
		return
	}
	http.Error(w, detail, status)
}

func passthrough(next http.Handler) http.Handler {
	return next
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func SecurityHeadersMiddleware(config SecurityHeadersConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !config.Enabled {
			return next
		}
		strictPolicy := ContentSecurityPolicy(ContentSecurityPolicyConfig{})
		documentPolicy := ContentSecurityPolicy(ContentSecurityPolicyConfig{
			DatastarExpressions: true,
			DynamicStyles:       true,
		})
		withContentSecurityPolicy := ContentSecurityPolicyByMediaType(strictPolicy, documentPolicy)(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := w.Header()
			header.Set("X-Content-Type-Options", "nosniff")
			header.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
			header.Set("X-Frame-Options", "SAMEORIGIN")
			if config.HSTS {
				header.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			withContentSecurityPolicy.ServeHTTP(w, r)
		})
	}
}

// RequestCorrelation establishes one request and correlation identity before
// any other process-wide middleware runs. Existing request identities are
// preserved because browser commands also use them for idempotency.
func RequestCorrelation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if requestID == "" {
			requestID = apitransport.NewRequestID()
		}
		correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
		if correlationID == "" {
			correlationID = requestID
		}

		r.Header.Set("X-Request-ID", requestID)
		r.Header.Set("X-Correlation-ID", correlationID)
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("X-Correlation-ID", correlationID)
		next.ServeHTTP(w, r)
	})
}

func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &Recorder{ResponseWriter: w, StatusCode: http.StatusOK}
			next.ServeHTTP(rec, r)
			logger.InfoContext(r.Context(), "http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.StatusCode,
				"bytes", rec.BytesWritten,
				"duration", time.Since(start).String(),
				"remote", r.RemoteAddr,
				"request_id", firstNonEmpty(r.Header.Get("X-Request-Id"), r.Header.Get("X-Request-ID")),
				"correlation_id", firstNonEmpty(r.Header.Get("X-Correlation-Id"), r.Header.Get("X-Correlation-ID"), r.Header.Get("X-Request-Id"), r.Header.Get("X-Request-ID")),
			)
		})
	}
}

type Recorder struct {
	http.ResponseWriter
	StatusCode   int
	BytesWritten int
}

func (r *Recorder) WriteHeader(status int) {
	r.StatusCode = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *Recorder) Write(bytes []byte) (int, error) {
	if r.StatusCode == 0 {
		r.StatusCode = http.StatusOK
	}
	written, err := r.ResponseWriter.Write(bytes)
	r.BytesWritten += written
	return written, err
}

func (r *Recorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *Recorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hijacker.Hijack()
}

func (r *Recorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
