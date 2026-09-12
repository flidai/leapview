package http

import "testing"

func TestParseAPILimitRejectsOverLimit(t *testing.T) {
	if got, err := parseAPILimit("200"); err != nil || got != 200 {
		t.Fatalf("maximum limit = %d, %v", got, err)
	}
	if _, err := parseAPILimit("201"); err == nil {
		t.Fatal("over-limit request was accepted")
	}
}
