package architecture

import (
	"strings"
	"testing"
)

func assertFrontendWatchdogBounds(t *testing.T, taskfile string) {
	t.Helper()
	shard := taskfileTaskBlock(t, taskfile, "ci:lane:frontend:shard")
	for _, want := range []string{
		"enum: [core, reports, chat, data, site]",
		`{{if eq .SHARD "reports"}}`,
		"task ci:lane:frontend:reports",
		`node scripts/ci_watchdog.mjs --timeout-seconds 180 --attempts 2 -- task ci:test:frontend:{{.SHARD}}`,
	} {
		if !strings.Contains(shard, want) {
			t.Fatalf("frontend shard lane missing bounded retry contract %q", want)
		}
	}
	reports := taskfileTaskBlock(t, taskfile, "ci:lane:frontend:reports")
	for _, suite := range []string{"viewer", "playground", "builder"} {
		want := "node scripts/ci_watchdog.mjs --timeout-seconds 300 --attempts 2 -- task ci:test:frontend:reports:" + suite
		if !strings.Contains(reports, want) {
			t.Fatalf("frontend reports lane missing bounded retry contract %q", want)
		}
	}
	if strings.Contains(shard, "ignore_error:") || strings.Contains(reports, "ignore_error:") {
		t.Fatal("frontend watchdog failures must remain fatal")
	}
}
