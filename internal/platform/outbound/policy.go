// Package outbound defines the application-owned destination boundary for
// outbound network connections.
package outbound

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var (
	// ErrDestinationDenied is returned when an outbound destination does not
	// satisfy the selected policy. It deliberately excludes the resolved
	// address and full URL so callers can safely expose or log it.
	ErrDestinationDenied = errors.New("outbound destination is disallowed")
	ErrUnsupportedScheme = errors.New("outbound URL scheme is disallowed")
)

// Mode distinguishes service-controlled public endpoints from endpoints that
// were admitted through an existing explicit system or customer configuration.
type Mode uint8

const (
	PublicOnly Mode = iota
	ExplicitPrivate
)

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type Dialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

type Options struct {
	Resolver Resolver
	Dialer   Dialer
	Logger   *slog.Logger
}

type Policy struct {
	mode     Mode
	resolver Resolver
	dialer   Dialer
	logger   *slog.Logger
}

func New(mode Mode, options Options) *Policy {
	resolver := options.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	dialer := options.Dialer
	if dialer == nil {
		dialer = &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Policy{mode: mode, resolver: resolver, dialer: dialer, logger: logger}
}

// ValidateHost resolves a hostname and rejects the whole answer set if any
// answer is unsafe. Literal addresses are classified without DNS.
func (p *Policy) ValidateHost(ctx context.Context, host string) error {
	_, err := p.resolve(ctx, host)
	return err
}

// ResolveHost returns a validated snapshot of the current answer set. Native
// clients that support a separate address field (for example PostgreSQL's
// hostaddr) can use it to pin their actual socket while retaining the original
// hostname for TLS verification.
func (p *Policy) ResolveHost(ctx context.Context, host string) ([]netip.Addr, error) {
	addresses, err := p.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	return append([]netip.Addr(nil), addresses...), nil
}

// DialContext validates every connection-time DNS answer and dials a validated
// address directly. This binds classification to the address actually used by
// the socket instead of trusting an earlier hostname check.
func (p *Policy) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("parse outbound address: %w", err)
	}
	addresses, err := p.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, candidate := range addresses {
		conn, dialErr := p.dialer.DialContext(ctx, network, net.JoinHostPort(candidate.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("%w: destination has no usable address", ErrDestinationDenied)
}

func (p *Policy) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	host = strings.TrimSpace(strings.TrimSuffix(host, "."))
	if host == "" {
		return nil, p.denied("empty-host")
	}
	if literal, err := netip.ParseAddr(host); err == nil {
		if !p.allowed(literal) {
			return nil, p.denied("address-class")
		}
		return []netip.Addr{literal.Unmap()}, nil
	}
	addresses, err := p.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve outbound destination: %w", err)
	}
	if len(addresses) == 0 {
		return nil, p.denied("empty-dns-answer")
	}
	validated := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !p.allowed(address) {
			return nil, p.denied("dns-answer-class")
		}
		validated = append(validated, address)
	}
	return validated, nil
}

func (p *Policy) denied(reason string) error {
	if p != nil && p.logger != nil {
		p.logger.Warn("outbound destination denied", "reason", reason, "mode", p.mode.String())
	}
	return fmt.Errorf("%w (%s)", ErrDestinationDenied, reason)
}

func (m Mode) String() string {
	if m == ExplicitPrivate {
		return "explicit-private"
	}
	return "public-only"
}

func (p *Policy) allowed(address netip.Addr) bool {
	if !address.IsValid() {
		return false
	}
	address = address.Unmap()
	if address.IsUnspecified() || address.IsLoopback() || address.IsMulticast() ||
		address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() {
		return false
	}
	if address.IsPrivate() {
		return p.mode == ExplicitPrivate && !metadataPrefixesContain(address)
	}
	for _, prefix := range specialUsePrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	if address.Is6() && !globalIPv6Prefix.Contains(address) {
		return false
	}
	return address.IsGlobalUnicast()
}

func metadataPrefixesContain(address netip.Addr) bool {
	for _, prefix := range metadataPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

var metadataPrefixes = mustPrefixes(
	"169.254.169.254/32",
	"fd00:ec2::254/128",
)

var globalIPv6Prefix = netip.MustParsePrefix("2000::/3")

var specialUsePrefixes = mustPrefixes(
	"0.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"192.0.0.0/24",
	"192.0.2.0/24",
	"192.31.196.0/24",
	"192.52.193.0/24",
	"192.88.99.0/24",
	"192.175.48.0/24",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"224.0.0.0/4",
	"240.0.0.0/4",
	"64:ff9b::/96",
	"64:ff9b:1::/48",
	"100::/64",
	"2001::/23",
	"2001:db8::/32",
	"2002::/16",
	"3fff::/20",
	"fe80::/10",
	"ff00::/8",
)

func mustPrefixes(values ...string) []netip.Prefix {
	prefixes := make([]netip.Prefix, len(values))
	for index, value := range values {
		prefixes[index] = netip.MustParsePrefix(value)
	}
	return prefixes
}

type HTTPConfig struct {
	AllowedSchemes      []string
	MaxRedirects        int
	SameOriginRedirects bool
}

// HTTPClient copies a client and installs the policy at both the request and
// socket layers. Environment proxy variables are intentionally not inherited:
// an unvalidated proxy would bypass the guarded dial path.
func (p *Policy) HTTPClient(base *http.Client, config HTTPConfig) *http.Client {
	if base == nil {
		base = &http.Client{}
	}
	client := *base
	transport := cloneTransport(base.Transport)
	transport.Proxy = nil
	transport.DialContext = p.DialContext
	client.Transport = validatingRoundTripper{policy: p, next: transport, schemes: normalizedSchemes(config.AllowedSchemes)}
	previousRedirect := base.CheckRedirect
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if config.MaxRedirects <= 0 {
			return http.ErrUseLastResponse
		}
		if len(via) >= config.MaxRedirects {
			return fmt.Errorf("outbound redirect limit exceeded")
		}
		if err := p.ValidateURL(request.Context(), request.URL, config.AllowedSchemes...); err != nil {
			return err
		}
		if config.SameOriginRedirects && len(via) > 0 && !sameOrigin(via[0].URL, request.URL) {
			return p.denied("redirect-origin")
		}
		if previousRedirect != nil {
			return previousRedirect(request, via)
		}
		return nil
	}
	return &client
}

// ValidateURL validates syntax, scheme, credentials, and all current DNS
// answers. DialContext repeats address validation at connection time.
func (p *Policy) ValidateURL(ctx context.Context, target *url.URL, allowedSchemes ...string) error {
	if target == nil || target.Host == "" || target.Hostname() == "" || target.User != nil {
		return p.denied("invalid-url")
	}
	schemes := normalizedSchemes(allowedSchemes)
	if _, ok := schemes[strings.ToLower(target.Scheme)]; !ok {
		return fmt.Errorf("%w", ErrUnsupportedScheme)
	}
	return p.ValidateHost(ctx, target.Hostname())
}

type validatingRoundTripper struct {
	policy  *Policy
	next    http.RoundTripper
	schemes map[string]struct{}
}

func (v validatingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("outbound HTTP request is nil")
	}
	allowed := make([]string, 0, len(v.schemes))
	for scheme := range v.schemes {
		allowed = append(allowed, scheme)
	}
	if err := v.policy.ValidateURL(request.Context(), request.URL, allowed...); err != nil {
		return nil, err
	}
	return v.next.RoundTrip(request)
}

func cloneTransport(base http.RoundTripper) *http.Transport {
	if base == nil {
		return http.DefaultTransport.(*http.Transport).Clone()
	}
	if transport, ok := base.(*http.Transport); ok {
		return transport.Clone()
	}
	// Production integrations use *http.Transport. A non-transport round
	// tripper cannot expose a dial boundary, so use a fresh guarded transport.
	return http.DefaultTransport.(*http.Transport).Clone()
}

func normalizedSchemes(values []string) map[string]struct{} {
	if len(values) == 0 {
		values = []string{"https"}
	}
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.ToLower(strings.TrimSpace(value)); value != "" {
			result[value] = struct{}{}
		}
	}
	return result
}

func sameOrigin(left, right *url.URL) bool {
	return left != nil && right != nil && strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(left.Host, right.Host)
}
