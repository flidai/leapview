package contractprojection

import (
	"bytes"
	"encoding/json"
	"testing"

	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
)

func TestCanonicalURLPreservesIPv6ZoneEscapeDepth(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "RFC 6874 zone delimiter",
			in:   "HTTPS://[FE80::1%25ETH0]:443/a/../b",
			want: "https://[fe80::1%25eth0]/b",
		},
		{
			name: "zone name starts with25",
			in:   "HTTPS://[FE80::1%2525ETH0]:443/a/../b",
			want: "https://[fe80::1%2525eth0]/b",
		},
	}

	canonicalValues := make([]string, len(tests))
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := canonicalURL(test.in)
			if err != nil {
				t.Fatalf("canonicalURL(%q): %v", test.in, err)
			}
			canonicalValues[index] = got
			if got != test.want {
				t.Fatalf("canonicalURL(%q) = %q, want %q", test.in, got, test.want)
			}
			second, err := canonicalURL(got)
			if err != nil {
				t.Fatalf("canonicalURL(%q): %v", got, err)
			}
			if second != got {
				t.Fatalf("canonicalURL is not idempotent: first %q, second %q", got, second)
			}
		})
	}
	if canonicalValues[0] == canonicalValues[1] {
		t.Fatalf("IPv6 zone escape depth was collapsed: %q", canonicalValues[0])
	}
}

func TestIPv6ZoneEscapeDepthSurvivesSourcePublication(t *testing.T) {
	var source projectcontracts.Source
	const raw = `{"apiVersion":"leapview.dev/v1","kind":"Source","metadata":{"id":"source:zones","name":"zones"},"spec":{"connection":"warehouse","location":{"type":"path","path":"zones.csv","format":"csv"},"schema":{"mode":"strict","fields":{"zone":{"datatype":"String","authoritativeDefinitions":[{"type":"businessDefinition","url":"https://[fe80::1%25eth0]/a"},{"type":"businessDefinition","url":"https://[fe80::1%2525eth0]/a"}]}}}}}`
	if err := json.Unmarshal([]byte(raw), &source); err != nil {
		t.Fatal(err)
	}
	projection, err := ProjectSource(source, Contract{Version: "1.2.3", Compatibility: "backward"})
	if err != nil {
		t.Fatalf("ProjectSource: %v", err)
	}
	wire, err := CanonicalBytes(projection)
	if err != nil {
		t.Fatalf("CanonicalBytes: %v", err)
	}
	for _, want := range []string{`"url":"https://[fe80::1%25eth0]/a"`, `"url":"https://[fe80::1%2525eth0]/a"`} {
		if !bytes.Contains(wire, []byte(want)) {
			t.Fatalf("publication omitted %s: %s", want, wire)
		}
	}
	if _, err := DecodeSourcePublication(wire); err != nil {
		t.Fatalf("DecodeSourcePublication: %v", err)
	}
}
