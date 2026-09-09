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
	for _, shard := range []string{"core", "reports", "chat", "data", "site"} {
		want := "- task: ci:lane:frontend:shard\n        vars: { SHARD: " + shard + " }"
		if !strings.Contains(frontendLane, want) {
			t.Fatalf("frontend aggregate lane missing shard %q", shard)
		}
	}
	frontendShard := taskfileTaskBlock(t, taskfile, "ci:lane:frontend:shard")
	for _, want := range []string{
		"enum: [core, reports, chat, data, site]",
		"node scripts/ci_watchdog.mjs --timeout-seconds 180 --attempts 2 -- task ci:test:frontend:{{.SHARD}}",
	} {
		if !strings.Contains(frontendShard, want) {
			t.Fatalf("frontend shard lane missing bounded retry contract %q", want)
		}
	}
	localFrontendLane := taskfileTaskBlock(t, taskfile, "ci:lane:frontend:local")
	if !strings.Contains(localFrontendLane, "- task: ci:lane:frontend") {
		t.Fatal("local frontend lane must invoke the aggregate frontend shards")
	}
	if strings.Contains(localFrontendLane, "node scripts/ci_watchdog.mjs --timeout-seconds 180 --attempts 2 -- task ci:lane:frontend") {
		t.Fatal("local frontend lane must not apply one aggregate watchdog to all frontend shards")
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
		"- task: ci:full:extras:static",
		"- task: ci:full:extras:runtime:multinode",
		"- task: qa:ui-framework",
		"- task: ci:full:extras:runtime:tail",
	} {
		if !strings.Contains(fullExtras, want) {
			t.Fatalf("ci:full:extras missing %q", want)
		}
	}
	fullExtrasOrder := []string{
		"- task: desktop:test",
		"- task: ci:full:extras:static",
		"- task: ci:full:extras:runtime:multinode",
		"- task: qa:ui-framework",
		"- task: ci:full:extras:runtime:tail",
	}
	for index := 1; index < len(fullExtrasOrder); index++ {
		if strings.Index(fullExtras, fullExtrasOrder[index-1]) > strings.Index(fullExtras, fullExtrasOrder[index]) {
			t.Fatalf("ci:full:extras changed sequential command order: %q before %q", fullExtrasOrder[index-1], fullExtrasOrder[index])
		}
	}
	staticExtras := taskfileTaskBlock(t, taskfile, "ci:full:extras:static")
	for _, want := range []string{
		"go vet ./...",
		"go test -race ./pkg/...",
		"- task: quality:critical:race",
		"- task: test:workload:qualify",
	} {
		if !strings.Contains(staticExtras, want) {
			t.Fatalf("ci:full:extras:static missing %q", want)
		}
	}
	if strings.Contains(staticExtras, "test:go:postgres-multinode-qualification") {
		t.Fatal("container-backed PostgreSQL multinode validation must remain in the runtime branch")
	}
	runtimeExtras := taskfileTaskBlock(t, taskfile, "ci:full:extras:runtime")
	for _, want := range []string{
		"- task: ci:full:extras:runtime:multinode",
		"- task: ci:full:extras:runtime:tail",
	} {
		if !strings.Contains(runtimeExtras, want) {
			t.Fatalf("ci:full:extras:runtime missing %q", want)
		}
	}
	parallelExtras := taskfileTaskBlock(t, taskfile, "ci:full:extras:parallel")
	for _, want := range []string{
		"deps:",
		"- task: ci:full:extras:static",
		"- task: ci:full:extras:runtime",
	} {
		if !strings.Contains(parallelExtras, want) {
			t.Fatalf("ci:full:extras:parallel missing %q", want)
		}
	}
	runtimeMultinode := taskfileTaskBlock(t, taskfile, "ci:full:extras:runtime:multinode")
	if !strings.Contains(runtimeMultinode, "- task: test:go:postgres-multinode-qualification") {
		t.Fatal("runtime multinode group missing PostgreSQL qualification")
	}
	runtimeTail := taskfileTaskBlock(t, taskfile, "ci:full:extras:runtime:tail")
	for _, want := range []string{
		"- task: deploy:check",
		"- task: test:go:minio-conformance",
		"- task: test:go:plan-gc-conformance",
	} {
		if !strings.Contains(runtimeTail, want) {
			t.Fatalf("ci:full:extras:runtime:tail missing %q", want)
		}
	}
	hostedExtras := taskfileTaskBlock(t, taskfile, "ci:full:extras:hosted")
	for _, want := range []string{
		"- task: desktop:test",
		"- task: qa:ui-framework",
		"- task: generate",
		"- task: ci:full:extras:parallel",
	} {
		if !strings.Contains(hostedExtras, want) {
			t.Fatalf("ci:full:extras:hosted missing %q", want)
		}
	}
	hostedExtrasOrder := []string{
		"- task: desktop:test",
		"- task: qa:ui-framework",
		"- task: generate",
		"- task: ci:full:extras:parallel",
	}
	for index := 1; index < len(hostedExtrasOrder); index++ {
		if strings.Index(hostedExtras, hostedExtrasOrder[index-1]) > strings.Index(hostedExtras, hostedExtrasOrder[index]) {
			t.Fatalf("ci:full:extras:hosted changed barrier/order: %q before %q", hostedExtrasOrder[index-1], hostedExtrasOrder[index])
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
		"run: task ci:lane:frontend:shard SHARD=${{ matrix.shard }}",
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
		"run: task ci:lane:frontend:shard SHARD=${{ matrix.shard }}",
		"run: task ci:full:extras:hosted",
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
		"run: task ci:lane:frontend:shard SHARD=${{ matrix.shard }}",
		"run: task ci:full:extras",
		"run: task ci:nightly:extras",
	} {
		if !strings.Contains(nightlyWorkflow, want) {
			t.Fatalf("nightly workflow missing %q", want)
		}
	}
	for workflowName, workflow := range map[string]string{
		"ci.yml":               prWorkflow,
		"merge-validation.yml": mergeWorkflow,
		"nightly.yml":          nightlyWorkflow,
	} {
		frontend := workflowJobBlock(t, workflow, "frontend-validation")
		matrix := "shard: [core, reports, chat, data, site]"
		if workflowName == "ci.yml" {
			matrix = "matrix: ${{ fromJSON(needs.prepare.outputs.frontend_matrix) }}"
		}
		for _, want := range []string{
			"fail-fast: false",
			matrix,
			"run: task ci:lane:frontend:shard SHARD=${{ matrix.shard }}",
		} {
			if !strings.Contains(frontend, want) {
				t.Fatalf("%s frontend validation missing shard contract %q", workflowName, want)
			}
		}
	}
}
