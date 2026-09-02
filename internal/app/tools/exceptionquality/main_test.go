package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidatePolicyRejectsUnreviewableExceptions(t *testing.T) {
	valid := exception{Path: "internal/example/state_machine_test.go", Kind: "cohesive-state-machine", Owner: "runtime", Reason: "Transitions share one invariant and are reviewed as a single table-driven contract.", ReviewedAt: "2026-09-01", ReviewBy: "2027-03-01", MaximumLines: 100}
	tests := []policy{
		{Version: 2, Exceptions: []exception{valid}},
		{Version: policyVersion, Exceptions: []exception{{}}},
		{Version: policyVersion, Exceptions: []exception{valid, valid}},
	}
	for index, exceptions := range tests {
		if err := validatePolicy(exceptions); err == nil {
			t.Fatalf("invalid policy %d was accepted: %#v", index, exceptions)
		}
	}
}

func TestCheckExceptionsRejectsGrowthAndExpiredReviews(t *testing.T) {
	root := t.TempDir()
	path := "internal/example/state_machine_test.go"
	absolute := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolute, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exceptions := policy{Version: policyVersion, Exceptions: []exception{{Path: path, Kind: "cohesive-state-machine", Owner: "runtime", Reason: "Transitions share one invariant and are reviewed as a single table-driven contract.", ReviewedAt: "2026-01-01", ReviewBy: "2026-02-01", MaximumLines: 2}}}
	err := checkExceptions(root, exceptions, time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "exception maximum 2") || !strings.Contains(err.Error(), "review expired") {
		t.Fatalf("exception review error = %v", err)
	}
}

func TestLineCountHandlesEmptyAndUnterminatedFiles(t *testing.T) {
	for body, want := range map[string]int{"": 0, "one": 1, "one\n": 1, "one\ntwo": 2} {
		if got := lineCount([]byte(body)); got != want {
			t.Fatalf("lineCount(%q) = %d, want %d", body, got, want)
		}
	}
}
