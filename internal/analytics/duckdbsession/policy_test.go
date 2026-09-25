package duckdbsession

import (
	"strings"
	"testing"
)

func TestResourcePolicyConfiguresOnlyLoopbackOutboundProxy(t *testing.T) {
	policy := ResourcePolicy{
		MemoryMaxBytes: 1, TempMaxBytes: 1, MaxThreads: 1,
		HTTPProxyURL: "http://127.0.0.1:43123", HTTPProxyUser: "leapview", HTTPProxyPassword: "test-proxy-secret",
	}
	statements, err := policy.BoundedStatements()
	if err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(statements, "\n"); !strings.Contains(joined, "SET http_proxy = 'http://127.0.0.1:43123'") {
		t.Fatalf("HTTP proxy statement missing:\n%s", joined)
	}
	for _, endpoint := range []string{
		"https://proxy.example", "http://10.0.0.1:8080", "http://user:secret@127.0.0.1:8080",
	} {
		policy.HTTPProxyURL = endpoint
		if _, err := policy.BoundedStatements(); err == nil {
			t.Fatalf("non-loopback/internal proxy %q was accepted", endpoint)
		}
	}
}
