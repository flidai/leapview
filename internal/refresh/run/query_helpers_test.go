package run

import "testing"

func TestLatestRecord(t *testing.T) {
	if got, ok := LatestRecord(nil); ok || got.ID != "" {
		t.Fatalf("empty records = (%#v, %v), want zero and false", got, ok)
	}
	records := []RunRecord{{ID: "first"}, {ID: "second"}}
	got, ok := LatestRecord(records)
	if !ok || got.ID != "first" {
		t.Fatalf("latest record = (%#v, %v), want first and true", got, ok)
	}
}
