package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContinuousIntegrationHasExplicitPRFullAndNightlyTiers(t *testing.T) {
	root := repoRoot(t)
	read := func(path ...string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(append([]string{root}, path...)...))
		if err != nil {
			t.Fatalf("read %s: %v", filepath.Join(path...), err)
		}
		return string(data)
	}

	taskfile := read("Taskfile.yml")
	prWorkflow := read(".github", "workflows", "ci.yml")
	mergeWorkflow := read(".github", "workflows", "merge-validation.yml")
	nightlyWorkflow := read(".github", "workflows", "nightly.yml")
	pr := taskfileTaskBlock(t, taskfile, "ci:pr")
	for _, want := range []string{
		"- task: ci:prepare",
		"- task: ci:lane:go",
		"- task: ci:lane:frontend:local",
		"- task: generated:check",
	} {
		if !strings.Contains(pr, want) {
			t.Fatalf("ci:pr missing %q", want)
		}
	}
	if strings.Index(pr, "- task: ci:lane:go") > strings.Index(pr, "- task: ci:lane:frontend:local") {
		t.Fatal("local CI must finish the Go lane before starting Bun bundling")
	}
	prepare := taskfileTaskBlock(t, taskfile, "ci:prepare")
	for _, want := range []string{"- task: ci:extensions:prepare", "- task: generate", "- task: build", "- task: site:build"} {
		if !strings.Contains(prepare, want) {
			t.Fatalf("ci:prepare missing %q", want)
		}
	}
	if strings.Contains(prepare, "- task: db:check") {
		t.Fatal("ci:prepare must not repeat the SQL quality gate on watchdog retries")
	}
	packagesLane := taskfileTaskBlock(t, taskfile, "ci:lane:go:packages")
	if !strings.Contains(packagesLane, "- task: db:check") {
		t.Fatal("Go package lane must run the SQL quality gate once after shared preparation")
	}
	aggregateGoLane := taskfileTaskBlock(t, taskfile, "ci:lane:go")
	if !strings.Contains(aggregateGoLane, "- task: db:check") {
		t.Fatal("local Go aggregate lane must retain the SQL quality gate")
	}
	for _, lane := range []string{"ci:lane:go", "ci:lane:frontend"} {
		if !strings.Contains(taskfile, "  "+lane+":\n") {
			t.Fatalf("Taskfile missing bounded CI lane %q", lane)
		}
	}
	goLane := taskfileTaskBlock(t, taskfile, "test:go:prepared")
	if !strings.Contains(goLane, "task --parallel test:go:packages test:go:app:shards") {
		t.Fatal("prepared Go lane must allow the package sweep and two application shards to overlap")
	}
	appShards := taskfileTaskBlock(t, taskfile, "test:go:app:shards")
	if !strings.Contains(appShards, "task --parallel --concurrency 3 test:go:app:0 test:go:app:1 test:go:app:2 test:go:app:3") {
		t.Fatal("application test shards must retain a three-process bound")
	}
	frontendLane := taskfileTaskBlock(t, taskfile, "ci:lane:frontend")
	if strings.Contains(frontendLane, "- task: build") {
		t.Fatal("frontend lane must not replace production assets while Go tests are running")
	}
	localFrontendLane := taskfileTaskBlock(t, taskfile, "ci:lane:frontend:local")
	if !strings.Contains(localFrontendLane, "node scripts/ci_watchdog.mjs --timeout-seconds 180 --attempts 2 -- task ci:lane:frontend") {
		t.Fatal("local frontend lane must diagnose and retry a hung Bun process")
	}
	frontendSite := taskfileTaskBlock(t, taskfile, "ci:test:frontend:site")
	if !strings.Contains(frontendSite, "bun run test:site:prepared") {
		t.Fatal("frontend site tests must use the site tree prepared before concurrent lanes")
	}
	full := taskfileTaskBlock(t, taskfile, "ci:full")
	for _, want := range []string{"- task: ci:pr", "- task: ci:full:extras"} {
		if !strings.Contains(full, want) {
			t.Fatalf("ci:full missing %q", want)
		}
	}
	fullExtras := taskfileTaskBlock(t, taskfile, "ci:full:extras")
	for _, want := range []string{
		"- task: desktop:test",
		"go vet ./...",
		"go test -race ./pkg/...",
		"- task: quality:critical:race",
		"- task: test:go:postgres-multinode-qualification",
		"- task: qa:ui-framework",
		"- task: deploy:check",
	} {
		if !strings.Contains(fullExtras, want) {
			t.Fatalf("ci:full:extras missing %q", want)
		}
	}
	nightly := taskfileTaskBlock(t, taskfile, "ci:nightly")
	for _, want := range []string{"- task: ci:full", "- task: ci:nightly:extras"} {
		if !strings.Contains(nightly, want) {
			t.Fatalf("ci:nightly missing %q", want)
		}
	}
	nightlyExtras := taskfileTaskBlock(t, taskfile, "ci:nightly:extras")
	for _, want := range []string{"- task: generate", "- task: security:check", "- task: dependency-security"} {
		if !strings.Contains(nightlyExtras, want) {
			t.Fatalf("ci:nightly:extras missing %q", want)
		}
	}
	ciLocal := taskfileTaskBlock(t, taskfile, "ci:local")
	if !strings.Contains(ciLocal, "- task: ci:full") {
		t.Fatal("ci:local must remain a compatibility alias for the full current-machine contract")
	}

	for _, want := range []string{
		"run: task ci:lane:go:apigen",
		"run: node scripts/ci_watchdog.mjs --timeout-seconds 420 --attempts 2 -- task ci:prepare",
		"run: task ci:lane:go:packages",
		"run: task ci:lane:go:application",
		"run: node scripts/ci_watchdog.mjs --timeout-seconds 180 --attempts 2 -- task ci:lane:frontend",
		"run: task generated:check",
	} {
		if !strings.Contains(prWorkflow, want) {
			t.Fatalf("pull-request workflow missing split fast-tier command %q", want)
		}
	}
	if strings.Contains(prWorkflow, "\n        run: task ci:pr\n") || strings.Contains(prWorkflow, "\n        run: task ci:full\n") {
		t.Fatal("pull-request workflow must distribute the fast tier across independent runners")
	}
	for _, want := range []string{
		"merge_group:",
		"run: task ci:lane:go:apigen",
		"run: task ci:lane:go:packages",
		"run: task ci:lane:go:application",
		"run: node scripts/ci_watchdog.mjs --timeout-seconds 180 --attempts 2 -- task ci:lane:frontend",
		"run: task ci:full:extras",
	} {
		if !strings.Contains(mergeWorkflow, want) {
			t.Fatalf("merge queue must run the split full tier against the exact merge group: missing %q", want)
		}
	}
	for _, want := range []string{
		"name: Nightly CI",
		"schedule:",
		"cron: '17 2 * * *'",
		"workflow_dispatch:",
		"run: task ci:lane:go:apigen",
		"run: task ci:lane:go:packages",
		"run: task ci:lane:go:application",
		"run: node scripts/ci_watchdog.mjs --timeout-seconds 180 --attempts 2 -- task ci:lane:frontend",
		"run: task ci:full:extras",
		"run: task ci:nightly:extras",
	} {
		if !strings.Contains(nightlyWorkflow, want) {
			t.Fatalf("nightly workflow missing %q", want)
		}
	}
}
