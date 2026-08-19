package app

import "testing"

func TestCanonicalSourceObservationIDMapsRuntimeAliasToResourceID(t *testing.T) {
	sources := map[string]string{
		"olist.customers": "source:olist.customers",
	}
	if got := canonicalSourceObservationID(sources, "olist_customers"); got != "source:olist.customers" {
		t.Fatalf("canonical observation id = %q", got)
	}
	if got := canonicalSourceObservationID(sources, "source:already-canonical"); got != "source:already-canonical" {
		t.Fatalf("unknown observation id changed to %q", got)
	}
}
