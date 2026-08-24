package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunRequiresFourSuccessfulLanes(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"success", "success", "success", "success"}, &stderr); code != 0 {
		t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
	}

	for _, result := range []string{"failure", "cancelled", "skipped", "timed_out", "", "unknown"} {
		t.Run(result, func(t *testing.T) {
			stderr.Reset()
			code := run([]string{"success", result, "success", "success"}, &stderr)
			if code == 0 {
				t.Fatalf("run() accepted %q", result)
			}
			if !strings.Contains(stderr.String(), "Security validation result:") {
				t.Fatalf("stderr = %q", stderr.String())
			}
		})
	}
}

func TestRunRejectsWrongLaneCount(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"success", "success", "success"}, &stderr); code != 64 {
		t.Fatalf("run() code = %d, want 64", code)
	}
	if !strings.Contains(stderr.String(), "expected exactly four") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
