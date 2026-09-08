package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCrossLanguageQualityPRLaneContract(t *testing.T) {
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
	qualityTask := taskfileTaskBlock(t, taskfile, "ci:lane:quality")
	for _, want := range []string{
		"- task: quality:budget:check",
		"- task: quality:exceptions:check",
		"- task: quality:trends:report",
		"- go test ./internal/platform/architecture -count=1",
	} {
		if !strings.Contains(qualityTask, want) {
			t.Fatalf("quality lane missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"db:check",
		"quality:critical:coverage",
		"test:go:packages",
		"test:go:app",
		"test:go:external",
	} {
		if strings.Contains(qualityTask, forbidden) {
			t.Fatalf("quality lane includes backend/package work %q", forbidden)
		}
	}

	workflow := read(".github", "workflows", "ci.yml")
	for _, want := range []string{
		"quality_validation: ${{ steps.plan.outputs.quality_validation }}",
		"quality-validation:",
		"quality-validation]",
		"QUALITY_RESULT: ${{ needs.quality-validation.result }}",
		`"quality-validation": env.QUALITY_RESULT`,
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("PR workflow missing quality contract %q", want)
		}
	}
	qualityJob := workflowJobBlock(t, workflow, "quality-validation")
	for _, want := range []string{
		"name: Cross-language quality (PR)",
		"needs: [prepare]",
		"if: needs.prepare.outputs.quality_validation == 'true'",
		"runs-on: ubuntu-24.04",
		"uses: ./.github/actions/setup-ci",
		"browser: \"false\"",
		"run: node scripts/ci_watchdog.mjs --timeout-seconds 420 --attempts 2 -- task ci:prepare",
		"run: task ci:lane:quality",
	} {
		if !strings.Contains(qualityJob, want) {
			t.Fatalf("quality job missing %q", want)
		}
	}
	prepareAt := strings.Index(qualityJob, "task ci:prepare")
	qualityAt := strings.Index(qualityJob, "task ci:lane:quality")
	if prepareAt < 0 || qualityAt < 0 || prepareAt > qualityAt {
		t.Fatal("quality architecture scans must follow generated and embedded asset preparation")
	}
	if !strings.Contains(qualityJob, "fetch-depth: 0") {
		t.Fatal("quality lane must inspect the complete candidate checkout")
	}

	packages := taskfileTaskBlock(t, taskfile, "ci:lane:go:packages")
	for _, want := range []string{
		"- task: db:check",
		"- task: quality:budget:check",
		"- task: quality:exceptions:check",
		"- task: quality:trends:report",
		"- task: quality:critical:coverage",
		"- task: test:go:packages",
	} {
		if !strings.Contains(packages, want) {
			t.Fatalf("existing Go package quality contract changed: missing %q", want)
		}
	}

	for _, workflowName := range []string{"merge-validation.yml", "nightly.yml"} {
		text := read(".github", "workflows", workflowName)
		if strings.Contains(text, "quality-validation:") || strings.Contains(text, "quality_validation") {
			t.Fatalf("%s must retain its existing exhaustive workflow inventory", workflowName)
		}
	}

}
