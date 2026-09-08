package contractprojection

import (
	"strings"
	"testing"
)

func TestCanonicalURLNormalizesRFC3986Syntax(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "hierarchical URL",
			in:   "HTTPS://EXAMPLE.COM:443/a/./b/../%7ealice?b=%7e&a=%2f#frag%7e%2f",
			want: "https://example.com/a/~alice?b=~&a=%2F#frag~%2F",
		},
		{
			name: "empty authority path",
			in:   "http://example.com",
			want: "http://example.com/",
		},
		{
			name: "opaque urn",
			in:   "URN:example:animal:%7eferret?part=%2f#frag%7e",
			want: "urn:example:animal:~ferret?part=%2F#frag~",
		},
		{
			name: "non-default port",
			in:   "HtTpS://Example.COM:8443/a",
			want: "https://example.com:8443/a",
		},
		{
			name: "ipv6 host without port",
			in:   "HtTpS://[2001:DB8::1]/a",
			want: "https://[2001:db8::1]/a",
		},
		{
			name: "ipv6 zone and default port",
			in:   "HtTpS://[FE80::1%25ETH0]:443/a",
			want: "https://[fe80::1%25eth0]/a",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := canonicalURL(test.in)
			if err != nil {
				t.Fatalf("canonicalURL(%q): %v", test.in, err)
			}
			if got != test.want {
				t.Fatalf("canonicalURL(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}

func TestCanonicalURLRejectsInvalidSyntaxAndUserInfo(t *testing.T) {
	for _, value := range []string{
		"/relative/path",
		"https://",
		"https://user:password@example.com/path",
		"https://example.com/%ZZ",
		"https://example.com/path with spaces",
		"https://example.com:not-a-port/path",
		"https://[2001:db8::1/path",
	} {
		t.Run(strings.ReplaceAll(value, "/", "_"), func(t *testing.T) {
			if got, err := canonicalURL(value); err == nil {
				t.Fatalf("canonicalURL(%q) = %q, want an error", value, got)
			}
		})
	}
}

func TestCanonicalURLPreservesQueryOrderAndFragmentSignificance(t *testing.T) {
	first, err := canonicalURL("https://example.com/path?b=2&a=1#one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := canonicalURL("https://example.com/path?a=1&b=2#one")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("query-pair order was discarded: %q and %q", first, second)
	}
	third, err := canonicalURL("https://example.com/path?b=2&a=1#two")
	if err != nil {
		t.Fatal(err)
	}
	if first == third {
		t.Fatalf("fragment significance was discarded: %q and %q", first, third)
	}
	emptyFragment, err := canonicalURL("https://example.com/path#")
	if err != nil {
		t.Fatal(err)
	}
	if emptyFragment != "https://example.com/path#" {
		t.Fatalf("empty fragment marker = %q, want %q", emptyFragment, "https://example.com/path#")
	}
}

func TestCanonicalURLIsIdempotentForIPv6Zone(t *testing.T) {
	first, err := canonicalURL("HTTPS://[FE80::1%25ETH0]:443/a/../b")
	if err != nil {
		t.Fatal(err)
	}
	second, err := canonicalURL(first)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("canonicalURL is not idempotent: first %q, second %q", first, second)
	}
}
