package manifest

import "testing"

func TestRuntimeSourceAliasMatchesCanonicalRuntimeNamespace(t *testing.T) {
	tests := map[string]string{
		"olist.customers": "olist_customers",
		"source:orders":   "source_orders",
		"9-sales":         "__sales",
		"":                "source_",
	}
	for input, want := range tests {
		if got := RuntimeSourceAlias(input); got != want {
			t.Fatalf("RuntimeSourceAlias(%q) = %q, want %q", input, got, want)
		}
	}
}
