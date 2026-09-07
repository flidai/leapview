package securitycontracts

import (
	"strings"
	"testing"
)

func TestNightlyEvidenceRefreshIsReadOnlyFailClosedAndReviewable(t *testing.T) {
	workflow := repositoryYAML(t, ".github/workflows/nightly.yml")
	for _, trigger := range []string{"schedule:", "workflow_dispatch:"} {
		if !strings.Contains(workflow, trigger) {
			t.Fatalf("nightly workflow is missing %q", trigger)
		}
	}
	refresh := workflowJobBlock(t, workflow, "dependency-evidence-refresh")
	for _, want := range []string{
		"name: JavaScript dependency evidence refresh",
		"permissions:\n      contents: read",
		"uses: ./.github/actions/setup-ci",
		"run: task security:dependencies:evidence:refresh",
		"run: task security:dependencies\n",
		"if: ${{ steps.evidence-refresh.outcome == 'success' }}",
		"if: ${{ !cancelled() && steps.evidence-refresh.outcome == 'success' && steps.evidence-validation.outcome == 'success' }}",
		"uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a",
		"path: .security/javascript-vulnerability-evidence.json",
	} {
		if !strings.Contains(refresh, want) {
			t.Fatalf("nightly evidence refresh job is missing %q", want)
		}
	}
	if !containsPinnedAction(refresh, "actions/checkout") {
		t.Fatal("nightly evidence refresh job does not use a commit-pinned checkout action")
	}
	for _, forbidden := range []string{"contents: write", "git commit", "git push", "continue-on-error: true", "if: ${{ always() }}"} {
		if strings.Contains(refresh, forbidden) {
			t.Fatalf("nightly evidence refresh job permits or hides %q", forbidden)
		}
	}
	ordered := [][2]string{
		{"uses: actions/checkout@", "uses: ./.github/actions/setup-ci"},
		{"uses: ./.github/actions/setup-ci", "run: task security:dependencies:evidence:refresh"},
		{"run: task security:dependencies:evidence:refresh", "run: task security:dependencies\n"},
		{"run: task security:dependencies\n", "uses: actions/upload-artifact@"},
	}
	for _, pair := range ordered {
		if strings.Index(refresh, pair[0]) >= strings.Index(refresh, pair[1]) {
			t.Fatalf("nightly evidence refresh steps are out of order: %q before %q", pair[0], pair[1])
		}
	}

	gate := workflowJobBlock(t, workflow, "ci-gate")
	for _, want := range []string{
		"dependency-evidence-refresh",
		"DEPENDENCY_EVIDENCE_REFRESH_RESULT: ${{ needs.dependency-evidence-refresh.result }}",
		`"${DEPENDENCY_EVIDENCE_REFRESH_RESULT}"`,
	} {
		if !strings.Contains(gate, want) {
			t.Fatalf("nightly CI gate is missing evidence refresh result %q", want)
		}
	}
}

func workflowJobBlock(t *testing.T, workflow, job string) string {
	t.Helper()
	startMarker := "  " + job + ":"
	lines := strings.Split(workflow, "\n")
	start := -1
	for index, line := range lines {
		if line == startMarker {
			start = index
			break
		}
	}
	if start < 0 {
		t.Fatalf("workflow job %q not found", job)
	}
	end := len(lines)
	for index := start + 1; index < len(lines); index++ {
		line := lines[index]
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") && strings.HasSuffix(line, ":") {
			end = index
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}
