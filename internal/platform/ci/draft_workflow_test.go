package ci

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Scan all workflows so new PR checks cannot silently bypass the draft policy.
func TestAllPRWorkflowsSkipDraftsAndAllowRequestedRuns(t *testing.T) {
	paths, err := filepath.Glob("../../../.github/workflows/*.*ml")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var workflow struct {
				On   map[string]yaml.Node `yaml:"on"`
				Jobs map[string]struct {
					If    string `yaml:"if"`
					Needs any    `yaml:"needs"`
				} `yaml:"jobs"`
			}
			if err := yaml.Unmarshal(data, &workflow); err != nil {
				t.Fatal(err)
			}
			prNode, ok := workflow.On["pull_request"]
			if !ok {
				return
			}
			var pr struct {
				Types []string `yaml:"types"`
			}
			if err := prNode.Decode(&pr); err != nil {
				t.Fatal(err)
			}
			if _, ok := workflow.On["workflow_dispatch"]; !ok {
				t.Error("PR checks must allow manual runs")
			}
			for _, event := range []string{"opened", "synchronize", "reopened", "ready_for_review"} {
				if !slices.Contains(pr.Types, event) {
					t.Errorf("missing PR trigger %s", event)
				}
			}
			for id, job := range workflow.Jobs {
				// Dependent jobs inherit skipped dependencies unless a status
				// function explicitly overrides the default success() condition.
				if job.Needs != nil && !strings.Contains(job.If, "always()") && !strings.Contains(job.If, "cancelled()") && !strings.Contains(job.If, "failure()") {
					continue
				}
				switch job.If {
				case "${{ github.event_name != 'pull_request' || !github.event.pull_request.draft }}",
					"${{ always() && (github.event_name != 'pull_request' || !github.event.pull_request.draft) }}",
					"${{ github.event_name != 'pull_request' }}", "${{ github.event_name == 'push' }}":
				default:
					t.Errorf("job %s can start on a draft PR: %q", id, job.If)
				}
			}
		})
	}
}
