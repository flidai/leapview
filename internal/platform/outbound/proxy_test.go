package outbound

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func TestProxyRoutesAllowedHTTPAndRejectsUnsafeDestination(t *testing.T) {
	t.Parallel()
	var contacts atomic.Int32
	target := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		contacts.Add(1)
		_, _ = io.WriteString(w, "proxied")
	}))
	dialer := &mappingDialer{targets: map[string]string{"8.8.8.8:80": target}}
	policy := New(PublicOnly, Options{
		Resolver: staticResolver{answers: map[string][]netip.Addr{
			"allowed.example": addresses("8.8.8.8"),
			"private.example": addresses("10.0.0.9"),
		}},
		Dialer: dialer,
	})
	proxy, err := StartProxy(policy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	proxyURL, err := url.Parse(proxy.URL())
	if err != nil {
		t.Fatal(err)
	}
	unauthenticated := &http.Client{
		Timeout:   time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
	}
	unauthorized, err := unauthenticated.Get("http://allowed.example/data")
	if err != nil {
		t.Fatalf("unauthenticated proxy response: %v", err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("unauthenticated status = %d, want 407", unauthorized.StatusCode)
	}
	username, password := proxy.Credentials()
	proxyURL.User = url.UserPassword(username, password)
	client := &http.Client{
		Timeout:   time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
	}
	response, err := client.Get("http://allowed.example/data")
	if err != nil {
		t.Fatalf("allowed proxied request: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("allowed status = %d, want 200", response.StatusCode)
	}
	response, err = client.Get("http://private.example/data")
	if err != nil {
		t.Fatalf("blocked proxy response: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("blocked status = %d, want 403", response.StatusCode)
	}
	if got := contacts.Load(); got != 1 {
		t.Fatalf("upstream contacts = %d, want only the allowed request", got)
	}
}

func newHTTPTestServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server.Listener.Addr().String()
}
