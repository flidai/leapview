package duckdbsql

import "testing"

func TestLimitsDefaultsAndValidation(t *testing.T) {
	got, err := Limits{}.normalized()
	if err != nil || got.MaxDepth == 0 || got.MaxJSONBytes == 0 {
		t.Fatalf("limits=%#v err=%v", got, err)
	}
	if _, err := (Limits{MaxDepth: -1}).normalized(); err == nil {
		t.Fatal("negative limit accepted")
	}
}
