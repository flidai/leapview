package outbound

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type staticResolver struct {
	answers map[string][]netip.Addr
	err     error
}

func (r staticResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if r.err != nil {
		return nil, r.err
	}
	return append([]netip.Addr(nil), r.answers[host]...), nil
}

type mappingDialer struct {
	mu      sync.Mutex
	targets map[string]string
	dialed  []string
}

func (d *mappingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	d.dialed = append(d.dialed, address)
	target := d.targets[address]
	d.mu.Unlock()
	if target == "" {
		return nil, fmt.Errorf("unexpected dial %q", address)
	}
	return (&net.Dialer{}).DialContext(ctx, network, target)
}

func (d *mappingDialer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.dialed)
}

func TestPolicyAddressClassification(t *testing.T) {
	t.Parallel()
	public := New(PublicOnly, Options{})
	private := New(ExplicitPrivate, Options{})
	tests := []struct {
		address      string
		public       bool
		explicitPriv bool
	}{
		{address: "127.0.0.1"},
		{address: "10.0.0.1", explicitPriv: true},
		{address: "172.16.0.1", explicitPriv: true},
		{address: "192.168.0.1", explicitPriv: true},
		{address: "169.254.169.254"},
		{address: "192.0.2.1"},
		{address: "192.31.196.1"},
		{address: "198.18.0.1"},
		{address: "255.255.255.255"},
		{address: "8.8.8.8", public: true, explicitPriv: true},
		{address: "::1"},
		{address: "fc00::1", explicitPriv: true},
		{address: "fd00:ec2::254"},
		{address: "fe80::1"},
		{address: "2001:db8::1"},
		{address: "3fff::1"},
		{address: "ff02::1"},
		{address: "::ffff:127.0.0.1"},
		{address: "::ffff:169.254.169.254"},
		{address: "::ffff:192.168.1.1", explicitPriv: true},
		{address: "2606:4700:4700::1111", public: true, explicitPriv: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.address, func(t *testing.T) {
			address := netip.MustParseAddr(test.address)
			if got := public.allowed(address); got != test.public {
				t.Fatalf("public allowed = %t, want %t", got, test.public)
			}
			if got := private.allowed(address); got != test.explicitPriv {
				t.Fatalf("explicit-private allowed = %t, want %t", got, test.explicitPriv)
			}
		})
	}
}

func TestPolicyValidatesEveryDNSAnswer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		answers []netip.Addr
		wantErr error
	}{
		{name: "public", answers: addresses("8.8.8.8", "2606:4700:4700::1111")},
		{name: "mixed", answers: addresses("8.8.8.8", "10.0.0.8"), wantErr: ErrDestinationDenied},
		{name: "private", answers: addresses("192.168.1.2"), wantErr: ErrDestinationDenied},
		{name: "metadata", answers: addresses("169.254.169.254"), wantErr: ErrDestinationDenied},
		{name: "empty", wantErr: ErrDestinationDenied},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			policy := New(PublicOnly, Options{Resolver: staticResolver{answers: map[string][]netip.Addr{"safe-looking.example": test.answers}}})
			err := policy.ValidateHost(t.Context(), "safe-looking.example")
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ValidateHost() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestPolicyDNSFailureFailsClosed(t *testing.T) {
	t.Parallel()
	want := errors.New("resolver unavailable")
	policy := New(PublicOnly, Options{Resolver: staticResolver{err: want}})
	err := policy.ValidateHost(t.Context(), "safe-looking.example")
	if !errors.Is(err, want) {
		t.Fatalf("ValidateHost() error = %v, want wrapped resolver error", err)
	}
}

func TestDialContextUsesValidatedConnectionTimeAnswer(t *testing.T) {
	t.Parallel()
	dialer := &mappingDialer{}
	policy := New(PublicOnly, Options{
		Resolver: staticResolver{answers: map[string][]netip.Addr{
			"rebound.example": addresses("10.0.0.9"),
		}},
		Dialer: dialer,
	})
	_, err := policy.DialContext(t.Context(), "tcp", "rebound.example:443")
	if !errors.Is(err, ErrDestinationDenied) {
		t.Fatalf("DialContext() error = %v, want destination denial", err)
	}
	if got := dialer.count(); got != 0 {
		t.Fatalf("underlying dial count = %d, want 0", got)
	}
}

func TestValidateURLSchemesAndLiteralForms(t *testing.T) {
	t.Parallel()
	policy := New(PublicOnly, Options{Resolver: staticResolver{answers: map[string][]netip.Addr{
		"public.example": addresses("8.8.8.8"),
	}}})
	tests := []struct {
		raw     string
		schemes []string
		wantErr error
	}{
		{raw: "https://public.example/path", schemes: []string{"https"}},
		{raw: "http://public.example/path", schemes: []string{"http", "https"}},
		{raw: "http://10.0.0.1/path", schemes: []string{"http"}, wantErr: ErrDestinationDenied},
		{raw: "http://[fc00::1]/path", schemes: []string{"http"}, wantErr: ErrDestinationDenied},
		{raw: "http://[::ffff:127.0.0.1]/path", schemes: []string{"http"}, wantErr: ErrDestinationDenied},
		{raw: "file:///etc/passwd", schemes: []string{"http", "https"}, wantErr: ErrDestinationDenied},
		{raw: "ftp://public.example/file", schemes: []string{"http", "https"}, wantErr: ErrUnsupportedScheme},
		{raw: "https://user:secret@public.example/", schemes: []string{"https"}, wantErr: ErrDestinationDenied},
	}
	for _, test := range tests {
		test := test
		t.Run(test.raw, func(t *testing.T) {
			target, parseErr := url.Parse(test.raw)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			err := policy.ValidateURL(t.Context(), target, test.schemes...)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ValidateURL() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestHTTPClientRedirectPolicy(t *testing.T) {
	t.Parallel()
	var blockedContacts atomic.Int32
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		blockedContacts.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Errorf("unsafe redirect leaked Authorization header")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(blocked.Close)

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/public":
			http.Redirect(w, r, "http://allowed.example/final", http.StatusFound)
		case "/private":
			http.Redirect(w, r, "http://private.example/blocked", http.StatusFound)
		case "/loopback":
			http.Redirect(w, r, "http://127.0.0.1/blocked", http.StatusFound)
		case "/metadata":
			http.Redirect(w, r, "http://169.254.169.254/latest", http.StatusFound)
		case "/ipv6-private":
			http.Redirect(w, r, "http://[fc00::1]/blocked", http.StatusFound)
		case "/hop":
			http.Redirect(w, r, "http://allowed.example/metadata", http.StatusFound)
		default:
			_, _ = io.WriteString(w, "ok")
		}
	}))
	t.Cleanup(server.Close)

	serverAddress := server.Listener.Addr().String()
	blockedAddress := blocked.Listener.Addr().String()
	dialer := &mappingDialer{targets: map[string]string{
		"8.8.8.8:80":  serverAddress,
		"10.0.0.7:80": blockedAddress,
	}}
	policy := New(PublicOnly, Options{
		Resolver: staticResolver{answers: map[string][]netip.Addr{
			"entry.example":   addresses("8.8.8.8"),
			"allowed.example": addresses("8.8.8.8"),
			"private.example": addresses("10.0.0.7"),
		}},
		Dialer: dialer,
	})
	client := policy.HTTPClient(&http.Client{Timeout: time.Second}, HTTPConfig{
		AllowedSchemes: []string{"http", "https"}, MaxRedirects: 5,
	})

	request := func(path string) error {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://entry.example"+path, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer must-not-leak")
		response, err := client.Do(req)
		if response != nil {
			response.Body.Close()
		}
		return err
	}
	if err := request("/public"); err != nil {
		t.Fatalf("public redirect failed: %v", err)
	}
	for _, path := range []string{"/private", "/loopback", "/metadata", "/ipv6-private", "/hop"} {
		if err := request(path); !errors.Is(err, ErrDestinationDenied) {
			t.Errorf("redirect %s error = %v, want destination denial", path, err)
		}
	}
	if got := blockedContacts.Load(); got != 0 {
		t.Fatalf("blocked destination contacts = %d, want 0", got)
	}
}

func TestHTTPClientExplicitCustomerPrivateDestination(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "customer-ok")
	}))
	t.Cleanup(server.Close)

	dialer := &mappingDialer{targets: map[string]string{"10.20.30.40:8080": server.Listener.Addr().String()}}
	policy := New(ExplicitPrivate, Options{
		Resolver: staticResolver{answers: map[string][]netip.Addr{"customer-db-proxy.internal": addresses("10.20.30.40")}},
		Dialer:   dialer,
	})
	client := policy.HTTPClient(&http.Client{Timeout: time.Second}, HTTPConfig{
		AllowedSchemes: []string{"http", "https"}, MaxRedirects: 3, SameOriginRedirects: true,
	})
	response, err := client.Get("http://customer-db-proxy.internal:8080/data")
	if err != nil {
		t.Fatalf("explicit private request: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(body)); got != "customer-ok" {
		t.Fatalf("body = %q, want customer-ok", got)
	}
}

func addresses(values ...string) []netip.Addr {
	result := make([]netip.Addr, len(values))
	for index, value := range values {
		result[index] = netip.MustParseAddr(value)
	}
	return result
}
