package postgres

import "testing"

func TestCanonicalServingDocumentRecursivelySortsNestedObjects(t *testing.T) {
	got, err := canonicalServingDocument(`{"z":{"b":2,"a":1},"a":[{"d":4,"c":3}]}`)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"a":[{"c":3,"d":4}],"z":{"a":1,"b":2}}`
	if got != want {
		t.Fatalf("canonical object = %s, want %s", got, want)
	}
}
